package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/core/schemas"
)

type ClientManager interface {
	GetClientByName(clientName string) *schemas.MCPClientState
	GetClientForTool(toolName string) *schemas.MCPClientState
	GetToolPerClient(ctx context.Context) map[string][]schemas.ChatTool
}

type ToolsManager struct {
	toolExecutionTimeout atomic.Value
	maxAgentDepth        atomic.Int32
	clientManager        ClientManager
	logMu                sync.Mutex // Protects concurrent access to logs slice in codemode execution

	// Function to fetch a new request ID for each tool call result message in agent mode,
	// this is used to ensure that the tool call result messages are unique and can be tracked in plugins or by the user.
	// This id is attached to ctx.Value(schemas.BifrostContextKeyRequestID) in the agent mode.
	// If not provider, same request ID is used for all tool call result messages without any overrides.
	fetchNewRequestIDFunc func(ctx context.Context) string
}

const (
	ToolTypeListToolFiles   string = "listToolFiles"
	ToolTypeReadToolFile    string = "readToolFile"
	ToolTypeExecuteToolCode string = "executeToolCode"
)

// NewToolsManager creates and initializes a new tools manager instance.
// It validates the configuration, sets defaults if needed, and initializes atomic values
// for thread-safe configuration updates.
//
// Parameters:
//   - config: Tool manager configuration with execution timeout and max agent depth
//   - clientManager: Client manager interface for accessing MCP clients and tools
//   - fetchNewRequestIDFunc: Optional function to generate unique request IDs for agent mode
//
// Returns:
//   - *ToolsManager: Initialized tools manager instance
func NewToolsManager(config *schemas.MCPToolManagerConfig, clientManager ClientManager, fetchNewRequestIDFunc func(ctx context.Context) string) *ToolsManager {
	if config == nil {
		config = &schemas.MCPToolManagerConfig{
			ToolExecutionTimeout: schemas.DefaultToolExecutionTimeout,
			MaxAgentDepth:        schemas.DefaultMaxAgentDepth,
		}
	}
	if config.MaxAgentDepth <= 0 {
		config.MaxAgentDepth = schemas.DefaultMaxAgentDepth
	}
	if config.ToolExecutionTimeout <= 0 {
		config.ToolExecutionTimeout = schemas.DefaultToolExecutionTimeout
	}
	manager := &ToolsManager{
		clientManager:         clientManager,
		fetchNewRequestIDFunc: fetchNewRequestIDFunc,
	}
	// Initialize atomic values
	manager.toolExecutionTimeout.Store(config.ToolExecutionTimeout)
	manager.maxAgentDepth.Store(int32(config.MaxAgentDepth))

	logger.Info(fmt.Sprintf("%s tool manager initialized with tool execution timeout: %v and max agent depth: %d", MCPLogPrefix, config.ToolExecutionTimeout, config.MaxAgentDepth))
	return manager
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
func (m *ToolsManager) ParseAndAddToolsToRequest(ctx context.Context, req *schemas.BifrostRequest) *schemas.BifrostRequest {
	// MCP is only supported for chat and responses requests
	if req.ChatRequest == nil && req.ResponsesRequest == nil {
		return req
	}

	availableToolsPerClient := m.clientManager.GetToolPerClient(ctx)
	// Flatten tools from all clients into a single slice, avoiding duplicates
	var availableTools []schemas.ChatTool
	var includeCodeModeTools bool
	// Track tool names to prevent duplicates
	seenToolNames := make(map[string]bool)

	for clientName, clientTools := range availableToolsPerClient {
		client := m.clientManager.GetClientByName(clientName)
		if client == nil {
			logger.Warn(fmt.Sprintf("%s Client %s not found, skipping", MCPLogPrefix, clientName))
			continue
		}
		if client.ExecutionConfig.IsCodeModeClient {
			includeCodeModeTools = true
		} else {
			// Add tools from this client, checking for duplicates
			for _, tool := range clientTools {
				if tool.Function != nil && tool.Function.Name != "" {
					if !seenToolNames[tool.Function.Name] {
						availableTools = append(availableTools, tool)
						seenToolNames[tool.Function.Name] = true
					}
				}
			}
		}
	}

	if includeCodeModeTools {
		codeModeTools := []schemas.ChatTool{
			m.createListToolFilesTool(),
			m.createReadToolFileTool(),
			m.createExecuteToolCodeTool(),
		}
		// Add code mode tools, checking for duplicates
		for _, tool := range codeModeTools {
			if tool.Function != nil && tool.Function.Name != "" {
				if !seenToolNames[tool.Function.Name] {
					availableTools = append(availableTools, tool)
					seenToolNames[tool.Function.Name] = true
				}
			}
		}
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
func (m *ToolsManager) ExecuteTool(ctx context.Context, toolCall schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, error) {
	if toolCall.Function.Name == nil {
		return nil, fmt.Errorf("tool call missing function name")
	}
	toolName := *toolCall.Function.Name

	// Handle code mode tools
	switch toolName {
	case ToolTypeListToolFiles:
		return m.handleListToolFiles(ctx, toolCall)
	case ToolTypeReadToolFile:
		return m.handleReadToolFile(ctx, toolCall)
	case ToolTypeExecuteToolCode:
		return m.handleExecuteToolCode(ctx, toolCall)
	default:
		// Check if the user has permission to execute the tool call
		availableTools := m.clientManager.GetToolPerClient(ctx)
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

		client := m.clientManager.GetClientForTool(toolName)
		if client == nil {
			return nil, fmt.Errorf("client not found for tool %s", toolName)
		}

		// Parse tool arguments
		var arguments map[string]interface{}
		if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &arguments); err != nil {
			return nil, fmt.Errorf("failed to parse tool arguments for '%s': %v", toolName, err)
		}

		// Strip the client name prefix from tool name before calling MCP server
		// The MCP server expects the original tool name, not the prefixed version
		originalToolName := stripClientPrefix(toolName, client.ExecutionConfig.Name)

		// Call the tool via MCP client -> MCP server
		callRequest := mcp.CallToolRequest{
			Request: mcp.Request{
				Method: string(mcp.MethodToolsCall),
			},
			Params: mcp.CallToolParams{
				Name:      originalToolName,
				Arguments: arguments,
			},
		}

		logger.Debug(fmt.Sprintf("%s Starting tool execution: %s via client: %s", MCPLogPrefix, toolName, client.ExecutionConfig.Name))

		// Create timeout context for tool execution
		toolExecutionTimeout := m.toolExecutionTimeout.Load().(time.Duration)
		toolCtx, cancel := context.WithTimeout(ctx, toolExecutionTimeout)
		defer cancel()

		toolResponse, callErr := client.Conn.CallTool(toolCtx, callRequest)
		if callErr != nil {
			// Check if it was a timeout error
			if toolCtx.Err() == context.DeadlineExceeded {
				return nil, fmt.Errorf("MCP tool call timed out after %v: %s", toolExecutionTimeout, toolName)
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

// ExecuteAgentForChatRequest executes agent mode for a chat request, handling
// iterative tool calls up to the configured maximum depth. It delegates to the
// shared agent execution logic with the manager's configuration and dependencies.
//
// Parameters:
//   - ctx: Context for agent execution
//   - req: The original chat request
//   - resp: The initial chat response containing tool calls
//   - makeReq: Function to make subsequent chat requests during agent execution
//
// Returns:
//   - *schemas.BifrostChatResponse: The final response after agent execution
//   - *schemas.BifrostError: Any error that occurred during agent execution
func (m *ToolsManager) ExecuteAgentForChatRequest(
	ctx *context.Context,
	req *schemas.BifrostChatRequest,
	resp *schemas.BifrostChatResponse,
	makeReq func(ctx context.Context, req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError),
) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	return ExecuteAgentForChatRequest(
		ctx,
		int(m.maxAgentDepth.Load()),
		req,
		resp,
		makeReq,
		m.fetchNewRequestIDFunc,
		m.ExecuteTool,
		m.clientManager,
	)
}

// ExecuteAgentForResponsesRequest executes agent mode for a responses request, handling
// iterative tool calls up to the configured maximum depth. It delegates to the
// shared agent execution logic with the manager's configuration and dependencies.
//
// Parameters:
//   - ctx: Context for agent execution
//   - req: The original responses request
//   - resp: The initial responses response containing tool calls
//   - makeReq: Function to make subsequent responses requests during agent execution
//
// Returns:
//   - *schemas.BifrostResponsesResponse: The final response after agent execution
//   - *schemas.BifrostError: Any error that occurred during agent execution
func (m *ToolsManager) ExecuteAgentForResponsesRequest(
	ctx *context.Context,
	req *schemas.BifrostResponsesRequest,
	resp *schemas.BifrostResponsesResponse,
	makeReq func(ctx context.Context, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError),
) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	return ExecuteAgentForResponsesRequest(
		ctx,
		int(m.maxAgentDepth.Load()),
		req,
		resp,
		makeReq,
		m.fetchNewRequestIDFunc,
		m.ExecuteTool,
		m.clientManager,
	)
}

// UpdateConfig updates both tool execution timeout and max agent depth atomically.
// This method is safe to call concurrently from multiple goroutines.
func (m *ToolsManager) UpdateConfig(config *schemas.MCPToolManagerConfig) {
	if config == nil {
		return
	}
	if config.ToolExecutionTimeout > 0 {
		m.toolExecutionTimeout.Store(config.ToolExecutionTimeout)
	}
	if config.MaxAgentDepth > 0 {
		m.maxAgentDepth.Store(int32(config.MaxAgentDepth))
	}

	logger.Info(fmt.Sprintf("%s tool manager configuration updated with tool execution timeout: %v and max agent depth: %d", MCPLogPrefix, config.ToolExecutionTimeout, config.MaxAgentDepth))
}
