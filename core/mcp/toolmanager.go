package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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
	codeModeBindingLevel atomic.Value // Stores CodeModeBindingLevel
	clientManager        ClientManager
	logMu                sync.Mutex // Protects concurrent access to logs slice in codemode execution

	// Function to fetch a new request ID for each tool call result message in agent mode,
	// this is used to ensure that the tool call result messages are unique and can be tracked in plugins or by the user.
	// This id is attached to ctx.Value(schemas.BifrostContextKeyRequestID) in the agent mode.
	// If not provided, same request ID is used for all tool call result messages without any overrides.
	fetchNewRequestIDFunc func(ctx *schemas.BifrostContext) string
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
func NewToolsManager(config *schemas.MCPToolManagerConfig, clientManager ClientManager, fetchNewRequestIDFunc func(ctx *schemas.BifrostContext) string) *ToolsManager {
	if config == nil {
		config = &schemas.MCPToolManagerConfig{
			ToolExecutionTimeout: schemas.DefaultToolExecutionTimeout,
			MaxAgentDepth:        schemas.DefaultMaxAgentDepth,
			CodeModeBindingLevel: schemas.CodeModeBindingLevelServer,
		}
	}
	if config.MaxAgentDepth <= 0 {
		config.MaxAgentDepth = schemas.DefaultMaxAgentDepth
	}
	if config.ToolExecutionTimeout <= 0 {
		config.ToolExecutionTimeout = schemas.DefaultToolExecutionTimeout
	}
	// Default to server-level binding if not specified
	if config.CodeModeBindingLevel == "" {
		config.CodeModeBindingLevel = schemas.CodeModeBindingLevelServer
	}
	manager := &ToolsManager{
		clientManager:         clientManager,
		fetchNewRequestIDFunc: fetchNewRequestIDFunc,
	}
	// Initialize atomic values
	manager.toolExecutionTimeout.Store(config.ToolExecutionTimeout)
	manager.maxAgentDepth.Store(int32(config.MaxAgentDepth))
	manager.codeModeBindingLevel.Store(config.CodeModeBindingLevel)

	logger.Info(fmt.Sprintf("%s tool manager initialized with tool execution timeout: %v, max agent depth: %d, and code mode binding level: %s", MCPLogPrefix, config.ToolExecutionTimeout, config.MaxAgentDepth, config.CodeModeBindingLevel))
	return manager
}

// GetAvailableTools returns the available tools for the given context.
func (m *ToolsManager) GetAvailableTools(ctx context.Context) []schemas.ChatTool {
	availableToolsPerClient := m.clientManager.GetToolPerClient(ctx)
	// Flatten tools from all clients into a single slice, avoiding duplicates
	var availableTools []schemas.ChatTool
	var includeCodeModeTools bool
	// Track tool names to prevent duplicates
	seenToolNames := make(map[string]bool)

	for clientName, clientTools := range availableToolsPerClient {
		client := m.clientManager.GetClientByName(clientName)
		if client == nil {
			logger.Warn("%s Client %s not found, skipping", MCPLogPrefix, clientName)
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

	return availableTools
}

// buildIntegrationDuplicateCheckMap builds a map of tool names to check for duplicates
// based on the integration user agent. This includes both direct tool names and
// integration-specific naming patterns from existing tools in the request.
//
// Parameters:
//   - existingTools: List of existing tools in the request
//   - integrationUserAgent: Integration user agent string (e.g., "claude-cli")
//
// Returns:
//   - map[string]bool: Map of tool names/patterns to check against
func buildIntegrationDuplicateCheckMap(existingTools []schemas.ChatTool, integrationUserAgent string) map[string]bool {
	duplicateCheckMap := make(map[string]bool)

	// Add direct tool names
	for _, tool := range existingTools {
		if tool.Function != nil && tool.Function.Name != "" {
			duplicateCheckMap[tool.Function.Name] = true
		}
	}

	// Add integration-specific patterns from existing tools
	switch integrationUserAgent {
	case "claude-cli":
		// Claude CLI uses pattern: mcp__{foreign_name}__{tool_name}
		// The middle part is a foreign name we cannot check for, so we extract the last part
		// Examples:
		//   mcp__bifrost__executeToolCode -> executeToolCode
		//   mcp__bifrost__listToolFiles -> listToolFiles
		//   mcp__bifrost__readToolFile -> readToolFile
		//   mcp__calculator__calculator_add -> calculator_add
		for _, tool := range existingTools {
			if tool.Function != nil && tool.Function.Name != "" {
				existingToolName := tool.Function.Name
				// Check if existing tool matches Claude CLI pattern: mcp__*__{tool_name}
				if strings.HasPrefix(existingToolName, "mcp__") {
					// Split on __ and take the last entry (the tool_name)
					parts := strings.Split(existingToolName, "__")
					if len(parts) >= 3 {
						toolName := parts[len(parts)-1] // Last part is the tool name
						// Map Claude CLI pattern back to our tool name format
						// This handles both regular MCP tools and code mode tools
						if toolName != "" {
							duplicateCheckMap[toolName] = true
							// Also keep the original pattern for direct matching
							duplicateCheckMap[existingToolName] = true
						}
					}
				}
			}
		}
		// Add more integration-specific patterns here as needed
		// case "another-integration":
		//     // Add patterns for other integrations
	}

	return duplicateCheckMap
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

	availableTools := m.GetAvailableTools(ctx)

	if len(availableTools) == 0 {
		return req
	}

	// Get integration user agent for duplicate checking
	var integrationUserAgentStr string
	integrationUserAgent := ctx.Value(schemas.BifrostContextKey("integration-user-agent"))
	if integrationUserAgent != nil {
		if str, ok := integrationUserAgent.(string); ok {
			integrationUserAgentStr = str
		}
	}

	if len(availableTools) > 0 {
		switch req.RequestType {
		case schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest:
			// Only allocate new Params if it's nil to preserve caller-supplied settings
			if req.ChatRequest.Params == nil {
				req.ChatRequest.Params = &schemas.ChatParameters{}
			}

			tools := req.ChatRequest.Params.Tools

			// Build integration-aware duplicate check map
			duplicateCheckMap := buildIntegrationDuplicateCheckMap(tools, integrationUserAgentStr)

			// Add MCP tools that are not already present
			for _, mcpTool := range availableTools {
				// Skip tools with nil Function or empty Name
				if mcpTool.Function == nil || mcpTool.Function.Name == "" {
					continue
				}

				toolName := mcpTool.Function.Name

				// Check for duplicates using integration-aware logic
				if !duplicateCheckMap[toolName] {
					tools = append(tools, mcpTool)
					// Update the map to prevent duplicates within MCP tools as well
					duplicateCheckMap[toolName] = true
				}
			}
			req.ChatRequest.Params.Tools = tools
		case schemas.ResponsesRequest, schemas.ResponsesStreamRequest:
			// Only allocate new Params if it's nil to preserve caller-supplied settings
			if req.ResponsesRequest.Params == nil {
				req.ResponsesRequest.Params = &schemas.ResponsesParameters{}
			}

			tools := req.ResponsesRequest.Params.Tools

			// Convert Responses tools to ChatTool format for duplicate checking
			existingChatTools := make([]schemas.ChatTool, 0, len(tools))
			for _, tool := range tools {
				if tool.Name != nil {
					existingChatTools = append(existingChatTools, schemas.ChatTool{
						Type: schemas.ChatToolTypeFunction,
						Function: &schemas.ChatToolFunction{
							Name: *tool.Name,
						},
					})
				}
			}

			// Build integration-aware duplicate check map
			duplicateCheckMap := buildIntegrationDuplicateCheckMap(existingChatTools, integrationUserAgentStr)

			// Add MCP tools that are not already present
			for _, mcpTool := range availableTools {
				// Skip tools with nil Function or empty Name
				if mcpTool.Function == nil || mcpTool.Function.Name == "" {
					continue
				}

				toolName := mcpTool.Function.Name

				// Check for duplicates using integration-aware logic
				if !duplicateCheckMap[toolName] {
					responsesTool := mcpTool.ToResponsesTool()
					// Skip if the converted tool has nil Name
					if responsesTool.Name == nil {
						continue
					}

					tools = append(tools, *responsesTool)
					// Update the map to prevent duplicates within MCP tools as well
					duplicateCheckMap[toolName] = true
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

// ExecuteChatTool executes a tool call in Chat Completions API format and returns the result as a chat tool message.
// This is the primary tool executor that works with both Chat Completions and Responses APIs.
//
// For Responses API users, use ExecuteResponsesTool() for a more type-safe interface.
// However, internally this method is format-agnostic - it executes the tool and returns
// a ChatMessage which can then be converted to ResponsesMessage via ToResponsesToolMessage().
//
// Parameters:
//   - ctx: Execution context
//   - toolCall: The tool call to execute (from assistant message)
//
// Returns:
//   - *schemas.ChatMessage: Tool message with execution result
//   - error: Any execution error
func (m *ToolsManager) ExecuteChatTool(ctx *schemas.BifrostContext, toolCall schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, error) {
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

// ExecuteToolForResponses executes a tool call from a Responses API tool message and returns
// the result in Responses API format. This is a type-safe wrapper around ExecuteTool that
// handles the conversion between Responses and Chat API formats.
//
// This method:
// 1. Converts the Responses tool message to Chat API format
// 2. Executes the tool using the standard tool executor
// 3. Converts the result back to Responses API format
//
// Parameters:
//   - ctx: Execution context
//   - toolMessage: The Responses API tool message to execute
//   - callID: The original call ID from the Responses API
//
// Returns:
//   - *schemas.ResponsesMessage: Tool result message in Responses API format
//   - error: Any execution error
//
// Example:
//
//	responsesToolMsg := &schemas.ResponsesToolMessage{
//	    Name:      Ptr("calculate"),
//	    Arguments: Ptr("{\"x\": 10, \"y\": 20}"),
//	}
//	resultMsg, err := toolsManager.ExecuteResponsesTool(ctx, responsesToolMsg, "call-123")
//	// resultMsg is a ResponsesMessage with type=function_call_output
func (m *ToolsManager) ExecuteResponsesTool(
	ctx *schemas.BifrostContext,
	toolMessage *schemas.ResponsesToolMessage,
) (*schemas.ResponsesMessage, error) {
	if toolMessage == nil {
		return nil, fmt.Errorf("tool message is nil")
	}
	if toolMessage.Name == nil {
		return nil, fmt.Errorf("tool call missing function name")
	}

	// Convert Responses format to Chat format for execution
	chatToolCall := toolMessage.ToChatAssistantMessageToolCall()
	if chatToolCall == nil {
		return nil, fmt.Errorf("failed to convert Responses tool message to Chat format")
	}

	// Execute the tool using the standard executor
	chatResult, err := m.ExecuteChatTool(ctx, *chatToolCall)
	if err != nil {
		return nil, err
	}

	// Convert the result back to Responses format
	responsesMessage := chatResult.ToResponsesToolMessage()
	if responsesMessage == nil {
		return nil, fmt.Errorf("failed to convert tool result to Responses format")
	}

	return responsesMessage, nil
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
	ctx *schemas.BifrostContext,
	req *schemas.BifrostChatRequest,
	resp *schemas.BifrostChatResponse,
	makeReq func(ctx *schemas.BifrostContext, req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError),
) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	return ExecuteAgentForChatRequest(
		ctx,
		int(m.maxAgentDepth.Load()),
		req,
		resp,
		makeReq,
		m.fetchNewRequestIDFunc,
		m.ExecuteChatTool,
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
	ctx *schemas.BifrostContext,
	req *schemas.BifrostResponsesRequest,
	resp *schemas.BifrostResponsesResponse,
	makeReq func(ctx *schemas.BifrostContext, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError),
) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	return ExecuteAgentForResponsesRequest(
		ctx,
		int(m.maxAgentDepth.Load()),
		req,
		resp,
		makeReq,
		m.fetchNewRequestIDFunc,
		m.ExecuteChatTool,
		m.clientManager,
	)
}

// UpdateConfig updates tool manager configuration atomically.
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
	if config.CodeModeBindingLevel != "" {
		m.codeModeBindingLevel.Store(config.CodeModeBindingLevel)
	}

	logger.Info(fmt.Sprintf("%s tool manager configuration updated with tool execution timeout: %v, max agent depth: %d, and code mode binding level: %s", MCPLogPrefix, config.ToolExecutionTimeout, config.MaxAgentDepth, config.CodeModeBindingLevel))
}

// GetCodeModeBindingLevel returns the current code mode binding level.
// This method is safe to call concurrently from multiple goroutines.
func (m *ToolsManager) GetCodeModeBindingLevel() schemas.CodeModeBindingLevel {
	val := m.codeModeBindingLevel.Load()
	if val == nil {
		return schemas.CodeModeBindingLevelServer
	}
	return val.(schemas.CodeModeBindingLevel)
}
