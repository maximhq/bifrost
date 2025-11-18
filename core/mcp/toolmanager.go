package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/core/schemas"
)

type ClientManager interface {
	GetClientByName(clientName string) *schemas.MCPClientState
	GetClientForTool(toolName string) *schemas.MCPClientState
	GetToolPerClient(ctx context.Context) map[string][]schemas.ChatTool
}

type ToolsHandler struct {
	toolExecutionTimeout  time.Duration
	maxAgentDepth         int
	fetchNewRequestIDFunc func(ctx context.Context) string
	clientManager         ClientManager
	logMu                 sync.Mutex // Protects concurrent access to logs slice
}

type ToolsHandlerConfig struct {
	toolExecutionTimeout time.Duration
	maxAgentDepth        int
}

const (
	ToolTypeListToolFiles   string = "listToolFiles"
	ToolTypeReadToolFile    string = "readToolFile"
	ToolTypeExecuteToolCode string = "executeToolCode"
)

func NewToolsHandler(config *ToolsHandlerConfig, clientManager ClientManager) (*ToolsHandler, error) {
	if clientManager == nil {
		return nil, fmt.Errorf("client manager is required")
	}
	if config == nil {
		config = &ToolsHandlerConfig{
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
	return &ToolsHandler{
		toolExecutionTimeout: config.toolExecutionTimeout,
		maxAgentDepth:        config.maxAgentDepth,
		clientManager:        clientManager,
	}, nil
}

// SetFetchNewRequestIDFunc sets the function to get a new request ID.
func (m *ToolsHandler) SetFetchNewRequestIDFunc(fetchNewRequestIDFunc func(ctx context.Context) string) {
	m.fetchNewRequestIDFunc = fetchNewRequestIDFunc
}

// ParseAndAddToolsToRequest parses the available tools per client and adds them to the Bifrost request.
//
// Parameters:
//   - ctx: Execution context
//   - req: Bifrost request
//   - availableToolsPerClient: Map of client name to its available tools
//
// Returns:
//   - *schemas.BifrostRequest: Bifrost request with MCP tools added
func (h *ToolsHandler) ParseAndAddToolsToRequest(ctx context.Context, req *schemas.BifrostRequest) *schemas.BifrostRequest {
	availableToolsPerClient := h.clientManager.GetToolPerClient(ctx)
	// Flatten tools from all clients into a single slice
	var availableTools []schemas.ChatTool
	var includeCodeModeTools bool
	for clientName, clientTools := range availableToolsPerClient {
		client := h.clientManager.GetClientByName(clientName)
		if client == nil {
			logger.Warn(fmt.Sprintf("%s Client %s not found, skipping", MCPLogPrefix, clientName))
			continue
		}
		if client.ExecutionConfig.IsCodeModeClient {
			includeCodeModeTools = true
		} else {
			availableTools = append(availableTools, clientTools...)
		}
	}

	if includeCodeModeTools {
		codeModeTools := []schemas.ChatTool{
			h.createListToolFilesTool(),
			h.createReadToolFileTool(),
			h.createExecuteToolCodeTool(),
		}
		availableTools = append(availableTools, codeModeTools...)
	}

	if len(availableTools) > 0 {
		logger.Debug(fmt.Sprintf("%s Adding %d MCP tools to request from %d clients", MCPLogPrefix, len(availableTools), len(availableToolsPerClient)))
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
func (h *ToolsHandler) ExecuteTool(ctx context.Context, toolCall schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, error) {
	if toolCall.Function.Name == nil {
		return nil, fmt.Errorf("tool call missing function name")
	}
	toolName := *toolCall.Function.Name

	// Handle code mode tools
	switch toolName {
	case ToolTypeListToolFiles:
		return h.handleListToolFiles(ctx, toolCall)
	case ToolTypeReadToolFile:
		return h.handleReadToolFile(ctx, toolCall)
	case ToolTypeExecuteToolCode:
		return h.handleExecuteToolCode(ctx, toolCall)
	default:
		// Check if the user has permission to execute the tool call
		availableTools := h.clientManager.GetToolPerClient(ctx)
		toolFound := false
		for _, tools := range availableTools {
			for _, mcpTool := range tools {
				if mcpTool.Function != nil && mcpTool.Function.Name == toolName {
					toolFound = true
					break
				}
			}
			if toolFound {
				break
			}
		}

		if !toolFound {
			return nil, fmt.Errorf("tool '%s' is not available or not permitted", toolName)
		}

		client := h.clientManager.GetClientForTool(toolName)
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
		toolCtx, cancel := context.WithTimeout(ctx, h.toolExecutionTimeout)
		defer cancel()

		toolResponse, callErr := client.Conn.CallTool(toolCtx, callRequest)
		if callErr != nil {
			// Check if it was a timeout error
			if toolCtx.Err() == context.DeadlineExceeded {
				return nil, fmt.Errorf("MCP tool call timed out after %v: %s", h.toolExecutionTimeout, toolName)
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
}

func (h *ToolsHandler) ExecuteAgent(ctx context.Context, req *schemas.BifrostChatRequest, resp *schemas.BifrostChatResponse, llmCaller schemas.BifrostLLMCaller) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	return ExecuteAgent(
		&ctx,
		h.maxAgentDepth,
		req,
		resp,
		llmCaller,
		h.fetchNewRequestIDFunc,
		h.ExecuteTool,
		h.clientManager,
	)
}
