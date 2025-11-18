package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/core/schemas"
)

type DefaultToolsHandler struct {
	toolExecutionTimeout  time.Duration
	maxAgentDepth         int
	fetchNewRequestIDFunc func(ctx context.Context) string
	clientForToolFetcher  func(toolName string) *schemas.MCPClientState
	toolsGetterFunc       func(ctx context.Context) []schemas.ChatTool
}

type DefaultHandlerConfig struct {
	toolExecutionTimeout time.Duration
	maxAgentDepth        int
}

func NewDefaultToolsHandler(config *DefaultHandlerConfig) (*DefaultToolsHandler, error) {
	if config == nil {
		config = &DefaultHandlerConfig{
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
	return &DefaultToolsHandler{
		toolExecutionTimeout: config.toolExecutionTimeout,
		maxAgentDepth:        config.maxAgentDepth,
	}, nil
}

// SetupCompleted checks if the handler is fully setup and ready to use.
func (m *DefaultToolsHandler) SetupCompleted() bool {
	return m.toolsFetcherFunc != nil && m.clientForToolFetcher != nil && m.fetchNewRequestIDFunc != nil
}

// SetToolsFetcherFunc sets the function to get the available tools.
// TO BE CALLED JUST AFTER THE HANDLER IS CREATED AND BEFORE THE FIRST USE.
func (m *DefaultToolsHandler) SetToolsFetcherFunc(toolsGetterFunc func(ctx context.Context) []schemas.ChatTool) {
	m.toolsGetterFunc = toolsGetterFunc
}

// SetClientForToolFetcherFunc sets the function to get the client for a tool.
// TO BE CALLED JUST AFTER THE HANDLER IS CREATED AND BEFORE THE FIRST USE.
func (m *DefaultToolsHandler) SetClientForToolFetcherFunc(clientForToolFetcherFunc func(toolName string) *schemas.MCPClientState) {
	m.clientForToolFetcher = clientForToolFetcherFunc
}

// SetFetchNewRequestIDFunc sets the function to get a new request ID.
// TO BE CALLED JUST AFTER THE HANDLER IS CREATED AND BEFORE THE FIRST USE.
func (m *DefaultToolsHandler) SetFetchNewRequestIDFunc(fetchNewRequestIDFunc func(ctx context.Context) string) {
	m.fetchNewRequestIDFunc = fetchNewRequestIDFunc
}

// ParseAndAddToolsToRequest parses the available tools and adds them to the Bifrost request.
//
// Parameters:
//   - ctx: Execution context
//   - req: Bifrost request
//
// Returns:
//   - *schemas.BifrostRequest: Bifrost request with MCP tools added
func (m *DefaultToolsHandler) ParseAndAddToolsToRequest(ctx context.Context, req *schemas.BifrostRequest) *schemas.BifrostRequest {
	availableTools := m.toolsGetterFunc(ctx)
	if len(availableTools) > 0 {
		logger.Debug(fmt.Sprintf("%s Adding %d MCP tools to request", MCPLogPrefix, len(availableTools)))
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

			// Add MCP tools that are not already present
			for _, mcpTool := range availableTools {
				// Skip tools with nil Function or empty Name
				if mcpTool.Function == nil || mcpTool.Function.Name == "" {
					continue
				}

				if !existingToolsMap[mcpTool.Function.Name] {
					tools = append(tools, mcpTool)
					// Update the map to prevent duplicates within MCP tools as well
					existingToolsMap[mcpTool.Function.Name] = true
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

			// Add MCP tools that are not already present
			for _, mcpTool := range availableTools {
				// Skip tools with nil Function or empty Name
				if mcpTool.Function == nil || mcpTool.Function.Name == "" {
					continue
				}

				if !existingToolsMap[mcpTool.Function.Name] {
					responsesTool := mcpTool.ToResponsesTool()
					// Skip if the converted tool has nil Name
					if responsesTool.Name == nil {
						continue
					}

					tools = append(tools, *responsesTool)
					// Update the map to prevent duplicates within MCP tools as well
					existingToolsMap[*responsesTool.Name] = true
				}
			}
			req.ResponsesRequest.Params.Tools = tools
		}
	}
	return req
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
func (m *DefaultToolsHandler) ExecuteTool(ctx context.Context, toolCall schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, error) {
	if toolCall.Function.Name == nil {
		return nil, fmt.Errorf("tool call missing function name")
	}
	toolName := *toolCall.Function.Name

	client := m.clientForToolFetcher(toolName)
	if client == nil {
		return nil, fmt.Errorf("client not found for tool %s", toolName)
	}

	// Parse tool arguments
	var arguments map[string]interface{}
	if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &arguments); err != nil {
		return nil, fmt.Errorf("failed to parse tool arguments for '%s': %v", toolName, err)
	}

	// Call the tool via MCP client -> MCP server
	callRequest := mcp.CallToolRequest{
		Request: mcp.Request{
			Method: string(mcp.MethodToolsCall),
		},
		Params: mcp.CallToolParams{
			Name:      toolName,
			Arguments: arguments,
		},
	}

	logger.Debug(fmt.Sprintf("%s Starting tool execution: %s via client: %s", MCPLogPrefix, toolName, client.ExecutionConfig.Name))

	// Create timeout context for tool execution
	toolCtx, cancel := context.WithTimeout(ctx, m.toolExecutionTimeout)
	defer cancel()

	toolResponse, callErr := client.Conn.CallTool(toolCtx, callRequest)
	if callErr != nil {
		// Check if it was a timeout error
		if toolCtx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("MCP tool call timed out after %v: %s", m.toolExecutionTimeout, toolName)
		}
		logger.Error("%s Tool execution failed for %s via client %s: %v", MCPLogPrefix, toolName, client.ExecutionConfig.Name, callErr)
		return nil, fmt.Errorf("MCP tool call failed: %v", callErr)
	}

	logger.Debug(fmt.Sprintf("%s Tool execution completed: %s", MCPLogPrefix, toolName))

	// Extract text from MCP response
	responseText := extractTextFromMCPResponse(toolResponse, toolName)

	// Create tool response message
	return createToolResponseMessage(toolCall, responseText), nil
}

func (m *DefaultToolsHandler) ExecuteAgent(ctx context.Context, req *schemas.BifrostChatRequest, resp *schemas.BifrostChatResponse, llmCaller schemas.BifrostLLMCaller) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
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
