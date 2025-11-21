package mcp

import (
	"context"
	"fmt"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

type CodeModeToolsHandler struct {
	toolExecutionTimeout  time.Duration
	maxAgentDepth         int
	fetchNewRequestIDFunc func(ctx context.Context) string
	clientForToolFetcher  func(toolName string) *schemas.MCPClientState
	toolsFetcherFunc      func(ctx context.Context) map[string][]schemas.ChatTool
}

type CodeModeHandlerConfig struct {
	toolExecutionTimeout time.Duration
	maxAgentDepth        int
}

func NewCodeModeToolsHandler(config *CodeModeHandlerConfig) *CodeModeToolsHandler {
	if config == nil {
		config = &CodeModeHandlerConfig{
			toolExecutionTimeout: 30 * time.Second,
			maxAgentDepth:        10,
		}
	}
	if config.maxAgentDepth <= 0 {
		config.maxAgentDepth = 10
	}
	if config.toolExecutionTimeout <= 0 {
		config.toolExecutionTimeout = 30 * time.Second
	}
	return &CodeModeToolsHandler{
		toolExecutionTimeout: config.toolExecutionTimeout,
		maxAgentDepth:        config.maxAgentDepth,
	}
}

// SetupCompleted checks if the handler is fully setup and ready to use.
func (m *CodeModeToolsHandler) SetupCompleted() bool {
	return m.toolsFetcherFunc != nil && m.clientForToolFetcher != nil
}

// SetToolsFetcherFunc sets the function to get the available tools.
// TO BE CALLED JUST AFTER THE HANDLER IS CREATED AND BEFORE THE FIRST USE.
func (m *CodeModeToolsHandler) SetToolsFetcherFunc(toolsFetcherFunc func(ctx context.Context) map[string][]schemas.ChatTool) {
	m.toolsFetcherFunc = toolsFetcherFunc
}

// SetClientForToolFetcherFunc sets the function to get the client for a tool.
// TO BE CALLED JUST AFTER THE HANDLER IS CREATED AND BEFORE THE FIRST USE.
func (m *CodeModeToolsHandler) SetClientForToolFetcherFunc(clientForToolFetcherFunc func(toolName string) *schemas.MCPClientState) {
	m.clientForToolFetcher = clientForToolFetcherFunc
}

// SetFetchNewRequestIDFunc sets the function to get a new request ID.
// TO BE CALLED JUST AFTER THE HANDLER IS CREATED AND BEFORE THE FIRST USE.
func (m *CodeModeToolsHandler) SetFetchNewRequestIDFunc(fetchNewRequestIDFunc func(ctx context.Context) string) {
	m.fetchNewRequestIDFunc = fetchNewRequestIDFunc
}

// ParseAndAddToolsToRequest parses the available tools per client and adds code mode tools to the Bifrost request.
//
// Parameters:
//   - ctx: Execution context
//   - req: Bifrost request
//   - availableToolsPerClient: Map of client ID to its available tools
//
// Returns:
//   - *schemas.BifrostRequest: Bifrost request with code mode MCP tools added
func (m *CodeModeToolsHandler) ParseAndAddToolsToRequest(ctx context.Context, req *schemas.BifrostRequest) *schemas.BifrostRequest {
	// Create the three code mode tools
	codeModeTools := m.createCodeModeTools()

	if len(codeModeTools) > 0 {
		switch req.RequestType {
		case schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest:
			// Only allocate new Params if it's nil to preserve caller-supplied settings
			if req.ChatRequest.Params == nil {
				req.ChatRequest.Params = &schemas.ChatParameters{}
			}

			tools := req.ChatRequest.Params.Tools

			// Create a map of existing tool names for O(1) lookup
			existingToolsMap := make(map[string]bool)
			for _, tool := range tools {
				if tool.Function != nil && tool.Function.Name != "" {
					existingToolsMap[tool.Function.Name] = true
				}
			}

			// Add code mode tools that are not already present
			for _, codeModeTool := range codeModeTools {
				if codeModeTool.Function != nil && codeModeTool.Function.Name != "" {
					if !existingToolsMap[codeModeTool.Function.Name] {
						tools = append(tools, codeModeTool)
						existingToolsMap[codeModeTool.Function.Name] = true
					}
				}
			}
			req.ChatRequest.Params.Tools = tools
		case schemas.ResponsesRequest, schemas.ResponsesStreamRequest:
			// Only allocate new Params if it's nil to preserve caller-supplied settings
			if req.ResponsesRequest.Params == nil {
				req.ResponsesRequest.Params = &schemas.ResponsesParameters{}
			}

			tools := req.ResponsesRequest.Params.Tools

			// Create a map of existing tool names for O(1) lookup
			existingToolsMap := make(map[string]bool)
			for _, tool := range tools {
				if tool.Name != nil {
					existingToolsMap[*tool.Name] = true
				}
			}

			// Add code mode tools that are not already present
			for _, codeModeTool := range codeModeTools {
				if codeModeTool.Function != nil && codeModeTool.Function.Name != "" {
					if !existingToolsMap[codeModeTool.Function.Name] {
						responsesTool := codeModeTool.ToResponsesTool()
						if responsesTool.Name != nil {
							tools = append(tools, *responsesTool)
							existingToolsMap[*responsesTool.Name] = true
						}
					}
				}
			}
			req.ResponsesRequest.Params.Tools = tools
		}
	}
	return req
}

// createCodeModeTools creates the three code mode tools: listToolFiles, readToolFile, and executeToolCode
func (m *CodeModeToolsHandler) createCodeModeTools() []schemas.ChatTool {
	tools := make([]schemas.ChatTool, 0, 3)

	// listToolFiles tool
	tools = append(tools, m.createListToolFilesTool())

	// readToolFile tool
	tools = append(tools, m.createReadToolFileTool())

	// executeToolCode tool
	tools = append(tools, m.createExecuteToolCodeTool())

	return tools
}

// ============================================================================
// TOOL REGISTRATION AND DISCOVERY
// ============================================================================

// executeTool executes a tool call and returns the result as a tool message.
//
// Parameters:
//   - ctx: Execution context
//   - toolCall: The tool call to execute (from assistant message)
//
// Returns:
//   - schemas.ChatMessage: Tool message with execution result
//   - error: Any execution error
func (m *CodeModeToolsHandler) ExecuteTool(ctx context.Context, toolCall schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, error) {
	if toolCall.Function.Name == nil {
		return nil, fmt.Errorf("tool call missing function name")
	}
	toolName := *toolCall.Function.Name

	// Handle code mode tools
	switch toolName {
	case "listToolFiles":
		return m.handleListToolFiles(ctx, toolCall)
	case "readToolFile":
		return m.handleReadToolFile(ctx, toolCall)
	case "executeToolCode":
		return m.handleExecuteToolCode(ctx, toolCall)
	default:
		return nil, fmt.Errorf("unsupported tool name: %s", toolName)
	}
}

func (m *CodeModeToolsHandler) ExecuteAgent(ctx context.Context, req *schemas.BifrostChatRequest, resp *schemas.BifrostChatResponse, llmCaller schemas.BifrostLLMCaller) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	return ExecuteAgent(
		&ctx,
		m.maxAgentDepth,
		req,
		resp,
		llmCaller,
		m.fetchNewRequestIDFunc,
		m.ExecuteTool,
		m.clientForToolFetcher,
	)
}
