//go:build !tinygo && !wasm

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/core/mcp/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// ClientManager interface for accessing MCP clients and tools
type ClientManager interface {
	GetClientByName(clientName string) *schemas.MCPClientState
	GetClientForTool(toolName string) *schemas.MCPClientState
	GetToolPerClient(ctx context.Context) map[string][]schemas.ChatTool
}

// PluginPipeline represents the plugin execution pipeline interface
// This allows ToolsManager to run plugin hooks without direct dependency on Bifrost
type PluginPipeline interface {
	RunMCPPreHooks(ctx *schemas.BifrostContext, req *schemas.BifrostMCPRequest) (*schemas.BifrostMCPRequest, *schemas.MCPPluginShortCircuit, int)
	RunMCPPostHooks(ctx *schemas.BifrostContext, mcpResp *schemas.BifrostMCPResponse, bifrostErr *schemas.BifrostError, runFrom int) (*schemas.BifrostMCPResponse, *schemas.BifrostError)
}

// ToolsManager manages MCP tool execution and agent mode.
type ToolsManager struct {
	toolExecutionTimeout  atomic.Value
	maxAgentDepth         atomic.Int32
	disableAutoToolInject atomic.Bool
	clientManager         ClientManager
	logger                schemas.Logger
	agentModeExecutor     *AgentModeExecutor

	// OAuth2Provider for per-user OAuth token management
	oauth2Provider schemas.OAuth2Provider

	// CodeMode implementation for code execution (Starlark by default)
	codeMode CodeMode

	// Function to fetch a new request ID for each tool call result message in agent mode,
	// this is used to ensure that the tool call result messages are unique and can be tracked in plugins or by the user.
	// This id is attached to ctx.Value(schemas.BifrostContextKeyRequestID) in the agent mode.
	// If not provided, same request ID is used for all tool call result messages without any overrides.
	fetchNewRequestIDFunc func(ctx *schemas.BifrostContext) string

	// Function to get a plugin pipeline from the pool for running MCP plugin hooks
	// Used when executeCode tool calls nested MCP tools to ensure plugins run for them
	pluginPipelineProvider func() PluginPipeline

	// Function to release a plugin pipeline back to the pool
	releasePluginPipeline func(pipeline PluginPipeline)
}

// NewToolsManager creates and initializes a new tools manager instance.
// It validates the configuration, sets defaults if needed, and initializes atomic values
// for thread-safe configuration updates.
//
// Parameters:
//   - config: Tool manager configuration with execution timeout and max agent depth
//   - clientManager: Client manager interface for accessing MCP clients and tools
//   - fetchNewRequestIDFunc: Optional function to generate unique request IDs for agent mode
//   - pluginPipelineProvider: Optional function to get a plugin pipeline for running MCP hooks
//   - releasePluginPipeline: Optional function to release a plugin pipeline back to the pool
//
// Returns:
//   - *ToolsManager: Initialized tools manager instance
func NewToolsManager(
	config *schemas.MCPToolManagerConfig,
	clientManager ClientManager,
	fetchNewRequestIDFunc func(ctx *schemas.BifrostContext) string,
	pluginPipelineProvider func() PluginPipeline,
	releasePluginPipeline func(pipeline PluginPipeline),
	oauth2Provider schemas.OAuth2Provider,
	logger schemas.Logger,
) *ToolsManager {
	return NewToolsManagerWithCodeMode(
		config,
		clientManager,
		fetchNewRequestIDFunc,
		pluginPipelineProvider,
		releasePluginPipeline,
		nil, // Use default code mode (will be set later via SetCodeMode)
		oauth2Provider,
		logger,
	)
}

// NewToolsManagerWithCodeMode creates a new tools manager with a custom CodeMode implementation.
// This allows using alternative code execution environments (e.g., Lua, JavaScript, WASM).
//
// Parameters:
//   - config: Tool manager configuration with execution timeout and max agent depth
//   - clientManager: Client manager interface for accessing MCP clients and tools
//   - fetchNewRequestIDFunc: Optional function to generate unique request IDs for agent mode
//   - pluginPipelineProvider: Optional function to get a plugin pipeline for running MCP hooks
//   - releasePluginPipeline: Optional function to release a plugin pipeline back to the pool
//   - codeMode: Optional CodeMode implementation (if nil, must be set later via SetCodeMode)
//
// Returns:
//   - *ToolsManager: Initialized tools manager instance
func NewToolsManagerWithCodeMode(
	config *schemas.MCPToolManagerConfig,
	clientManager ClientManager,
	fetchNewRequestIDFunc func(ctx *schemas.BifrostContext) string,
	pluginPipelineProvider func() PluginPipeline,
	releasePluginPipeline func(pipeline PluginPipeline),
	codeMode CodeMode,
	oauth2Provider schemas.OAuth2Provider,
	logger schemas.Logger,
) *ToolsManager {
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

	if logger == nil {
		logger = defaultLogger
	}

	agentModeExecutor := &AgentModeExecutor{
		logger: logger,
	}

	manager := &ToolsManager{
		clientManager:          clientManager,
		fetchNewRequestIDFunc:  fetchNewRequestIDFunc,
		pluginPipelineProvider: pluginPipelineProvider,
		releasePluginPipeline:  releasePluginPipeline,
		codeMode:               codeMode,
		logger:                 logger,
		agentModeExecutor:      agentModeExecutor,
		oauth2Provider:         oauth2Provider,
	}

	// Initialize atomic values
	manager.toolExecutionTimeout.Store(config.ToolExecutionTimeout)
	manager.maxAgentDepth.Store(int32(config.MaxAgentDepth))
	manager.disableAutoToolInject.Store(config.DisableAutoToolInject)

	manager.logger.Info("%s tool manager initialized with tool execution timeout: %v, max agent depth: %d, and code mode binding level: %s", MCPLogPrefix, config.ToolExecutionTimeout, config.MaxAgentDepth, config.CodeModeBindingLevel)
	return manager
}

// SetCodeMode sets the CodeMode implementation for code execution.
// This should be called after construction if no CodeMode was provided to the constructor.
func (m *ToolsManager) SetCodeMode(codeMode CodeMode) {
	m.codeMode = codeMode
}

// GetCodeMode returns the current CodeMode implementation.
func (m *ToolsManager) GetCodeMode() CodeMode {
	return m.codeMode
}

// GetCodeModeDependencies returns the dependencies needed by CodeMode implementations.
// This is useful when constructing a CodeMode implementation externally.
func (m *ToolsManager) GetCodeModeDependencies() *CodeModeDependencies {
	return &CodeModeDependencies{
		ClientManager:          m.clientManager,
		PluginPipelineProvider: m.pluginPipelineProvider,
		ReleasePluginPipeline:  m.releasePluginPipeline,
		FetchNewRequestIDFunc:  m.fetchNewRequestIDFunc,
	}
}

// SetPluginPipeline updates the plugin pipeline provider and release function
// on both the ToolsManager and its CodeMode implementation.
// This is used when an externally-created MCPManager is attached to a Bifrost instance
// via SetMCPManager, so the CodeMode can route nested tool calls through Bifrost's plugin hooks.
func (m *ToolsManager) SetPluginPipeline(provider func() PluginPipeline, release func(PluginPipeline)) {
	m.pluginPipelineProvider = provider
	m.releasePluginPipeline = release
	if m.codeMode != nil {
		m.codeMode.SetDependencies(m.GetCodeModeDependencies())
	}
}

// GetAvailableTools returns the available tools for the given context.
func (m *ToolsManager) GetAvailableTools(ctx *schemas.BifrostContext) []schemas.ChatTool {
	availableToolsPerClient := m.clientManager.GetToolPerClient(ctx)
	// Flatten tools from all clients into a single slice, avoiding duplicates
	var availableTools []schemas.ChatTool
	var includeCodeModeTools bool
	// Track tool names to prevent duplicates
	seenToolNames := make(map[string]bool)

	for clientName, clientTools := range availableToolsPerClient {
		client := m.clientManager.GetClientByName(clientName)
		if client == nil {
			m.logger.Warn("%s Client %s not found, skipping", MCPLogPrefix, clientName)
			continue
		}
		if client.ExecutionConfig.IsCodeModeClient {
			includeCodeModeTools = true
		}
		// Add tools from this client, checking for duplicates
		for _, tool := range clientTools {
			if tool.Function != nil && tool.Function.Name != "" && !seenToolNames[tool.Function.Name] {
				seenToolNames[tool.Function.Name] = true
				schemas.AppendToContextList(ctx, schemas.BifrostContextKeyMCPAddedTools, tool.Function.Name)
				if !client.ExecutionConfig.IsCodeModeClient {
					availableTools = append(availableTools, tool)
				}
			}
		}
	}

	// Add code mode tools if any client is configured for code mode and we have a CodeMode implementation
	if includeCodeModeTools && m.codeMode != nil {
		codeModeTools := m.codeMode.GetTools()
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
func buildIntegrationDuplicateCheckMap(existingTools []schemas.ChatTool, integrationUserAgent string, _ schemas.Logger) map[string]bool {
	duplicateCheckMap := make(map[string]bool)

	// Add direct tool names
	for _, tool := range existingTools {
		if tool.Function != nil && tool.Function.Name != "" {
			duplicateCheckMap[tool.Function.Name] = true
		}
	}

	// Add integration-specific patterns from existing tools
	switch {
	case schemas.ClaudeCLI.Matches(integrationUserAgent):
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
	case schemas.GeminiCLI.Matches(integrationUserAgent):
		// Gemini CLI uses pattern: mcp_{server_name}_{tool_name}
		// where {server_name} is the user-configured MCP server name (no underscores)
		// and {tool_name} is Bifrost's full tool name (may contain hyphens and underscores).
		// Extract by stripping "mcp_" then skipping to the first "_" (server name boundary).
		// mcp_bifrost_testing_exa-web_fetch_exa -> testing_exa-web_fetch_exa
		// mcp_bifrost_ctx7-resolve-library-id   -> ctx7-resolve-library-id
		// mcp_bifrost_testing_websets-cancel_enrichment -> testing_websets-cancel_enrichment
		for _, tool := range existingTools {
			if tool.Function != nil && tool.Function.Name != "" {
				existingToolName := tool.Function.Name
				if strings.HasPrefix(existingToolName, "mcp_") {
					// Strip "mcp_" then find the first "_" which ends the server name
					withoutPrefix := existingToolName[len("mcp_"):]
					underscoreIdx := strings.Index(withoutPrefix, "_")
					if underscoreIdx != -1 && underscoreIdx < len(withoutPrefix)-1 {
						toolName := withoutPrefix[underscoreIdx+1:]
						if toolName != "" {
							duplicateCheckMap[toolName] = true
							duplicateCheckMap[existingToolName] = true
						}
					}
				}
			}
		}
	case schemas.QwenCodeCLI.Matches(integrationUserAgent):
		// Qwen CLI uses pattern: mcp__{server_name}__{tool_name}  (double underscores)
		// Strip "mcp__" then skip past the first "__" (server name boundary) to get tool_name.
		// Hyphens in the original Bifrost tool name are preserved.
		// mcp__bifrost__testing_exa-web_search_exa -> testing_exa-web_search_exa
		// mcp__bifrost__ctx7-resolve-library-id    -> ctx7-resolve-library-id
		for _, tool := range existingTools {
			if tool.Function != nil && tool.Function.Name != "" {
				existingToolName := tool.Function.Name
				if strings.HasPrefix(existingToolName, "mcp__") {
					withoutPrefix := existingToolName[len("mcp__"):]
					separatorIdx := strings.Index(withoutPrefix, "__")
					if separatorIdx != -1 && separatorIdx < len(withoutPrefix)-2 {
						toolName := withoutPrefix[separatorIdx+2:]
						if toolName != "" {
							duplicateCheckMap[toolName] = true
							duplicateCheckMap[existingToolName] = true
						}
					}
				}
			}
		}
	case schemas.CodexCLI.Matches(integrationUserAgent):
		// Codex CLI uses pattern: mcp__{server_name}__{tool_name} (double underscores)
		// but ALL hyphens in the original Bifrost tool name are converted to underscores.
		// Strip "mcp__" then skip past the first "__" to get the all-underscore tool name.
		// mcp__bifrost__testing_exa_web_fetch_exa -> testing_exa_web_fetch_exa
		// mcp__bifrost__ctx7_query_docs           -> ctx7_query_docs
		// Callers must also normalize Bifrost tool names (replace "-" with "_") before lookup.
		for _, tool := range existingTools {
			if tool.Function != nil && tool.Function.Name != "" {
				existingToolName := tool.Function.Name
				if strings.HasPrefix(existingToolName, "mcp__") {
					withoutPrefix := existingToolName[len("mcp__"):]
					separatorIdx := strings.Index(withoutPrefix, "__")
					if separatorIdx != -1 && separatorIdx < len(withoutPrefix)-2 {
						toolName := withoutPrefix[separatorIdx+2:]
						if toolName != "" {
							duplicateCheckMap[toolName] = true
							duplicateCheckMap[existingToolName] = true
						}
					}
				}
			}
		}
	case schemas.OpenCode.Matches(integrationUserAgent):
		// OpenCode uses pattern: {server_name}_{tool_name} (no mcp_ prefix, single underscore, hyphens preserved)
		// Strip up to and including the first "_" to extract the Bifrost tool name.
		// bifrost_testing_exa-web_fetch_exa    -> testing_exa-web_fetch_exa
		// bifrost_ctx7-query-docs              -> ctx7-query-docs
		// bifrost_filesystem-create_directory  -> filesystem-create_directory
		for _, tool := range existingTools {
			if tool.Function != nil && tool.Function.Name != "" {
				existingToolName := tool.Function.Name
				underscoreIdx := strings.Index(existingToolName, "_")
				if underscoreIdx != -1 && underscoreIdx < len(existingToolName)-1 {
					toolName := existingToolName[underscoreIdx+1:]
					if toolName != "" {
						duplicateCheckMap[toolName] = true
						duplicateCheckMap[existingToolName] = true
					}
				}
			}
		}
	}

	return duplicateCheckMap
}

// integrationDuplicateCheck reports whether toolName is already represented in duplicateCheckMap,
// including Codex CLI's hyphen-to-underscore normalization when matching existing tools.
func integrationDuplicateCheck(duplicateCheckMap map[string]bool, toolName string, integrationUserAgent string) bool {
	if duplicateCheckMap[toolName] {
		return true
	}
	if schemas.CodexCLI.Matches(integrationUserAgent) && duplicateCheckMap[strings.ReplaceAll(toolName, "-", "_")] {
		return true
	}
	return false
}

// markToolSeenInDuplicateCheckMap records toolName in duplicateCheckMap for subsequent
// integrationDuplicateCheck calls. For Codex CLI it also marks the hyphen-to-underscore
// form so MCP-only batches cannot inject both "foo-bar" and "foo_bar".
func markToolSeenInDuplicateCheckMap(duplicateCheckMap map[string]bool, toolName string, integrationUserAgent string) {
	duplicateCheckMap[toolName] = true
	if schemas.CodexCLI.Matches(integrationUserAgent) {
		duplicateCheckMap[strings.ReplaceAll(toolName, "-", "_")] = true
	}
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
func (m *ToolsManager) ParseAndAddToolsToRequest(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) *schemas.BifrostRequest {
	// MCP is only supported for chat and responses requests
	if req.ChatRequest == nil && req.ResponsesRequest == nil {
		return req
	}

	// When auto tool injection is disabled, only inject tools if the request
	// has explicit context filters set (e.g. via x-bf-mcp-include-tools header).
	if m.disableAutoToolInject.Load() {
		includeTools := ctx.Value(schemas.MCPContextKeyIncludeTools)
		includeClients := ctx.Value(schemas.MCPContextKeyIncludeClients)
		if includeTools == nil && includeClients == nil {
			return req
		}
	}

	availableTools := m.GetAvailableTools(ctx)

	if len(availableTools) == 0 {
		return req
	}

	// Get integration user agent for duplicate checking
	var integrationUserAgentStr string
	integrationUserAgent := ctx.Value(schemas.BifrostContextKeyUserAgent)
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
			duplicateCheckMap := buildIntegrationDuplicateCheckMap(tools, integrationUserAgentStr, m.logger)

			// Add MCP tools that are not already present
			for _, mcpTool := range availableTools {
				// Skip tools with nil Function or empty Name
				if mcpTool.Function == nil || mcpTool.Function.Name == "" {
					continue
				}

				toolName := mcpTool.Function.Name

				isDuplicate := integrationDuplicateCheck(duplicateCheckMap, toolName, integrationUserAgentStr)
				if !isDuplicate {
					tools = append(tools, mcpTool)
					// Update the duplicate check map to prevent duplicates within MCP tools as well
					markToolSeenInDuplicateCheckMap(duplicateCheckMap, toolName, integrationUserAgentStr)
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
			duplicateCheckMap := buildIntegrationDuplicateCheckMap(existingChatTools, integrationUserAgentStr, m.logger)

			// Add MCP tools that are not already present
			for _, mcpTool := range availableTools {
				// Skip tools with nil Function or empty Name
				if mcpTool.Function == nil || mcpTool.Function.Name == "" {
					continue
				}

				toolName := mcpTool.Function.Name

				isDuplicate := integrationDuplicateCheck(duplicateCheckMap, toolName, integrationUserAgentStr)
				if !isDuplicate {
					responsesTool := mcpTool.ToResponsesTool()
					if responsesTool.Name == nil {
						continue
					}
					tools = append(tools, *responsesTool)
					markToolSeenInDuplicateCheckMap(duplicateCheckMap, toolName, integrationUserAgentStr)
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

// ExecuteTool executes a tool call and returns the result.
// This is the primary tool executor that works with both Chat Completions and Responses APIs.
//
// Parameters:
//   - ctx: Execution context
//   - request: The MCP request containing the tool call (Chat or Responses format)
//
// Returns:
//   - *schemas.BifrostMCPResponse: Tool execution result (Chat or Responses format)
//   - error: Any execution error
func (m *ToolsManager) ExecuteTool(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
	// Validate request is not nil
	if request == nil {
		return nil, fmt.Errorf("request cannot be nil")
	}

	// Extract tool call based on request type
	var toolCall *schemas.ChatAssistantMessageToolCall
	switch request.RequestType {
	case schemas.MCPRequestTypeChatToolCall:
		toolCall = request.ChatAssistantMessageToolCall
	case schemas.MCPRequestTypeResponsesToolCall:
		// Validate ResponsesToolMessage is not nil before conversion
		if request.ResponsesToolMessage == nil {
			return nil, fmt.Errorf("ResponsesToolMessage cannot be nil for ResponsesToolCall request type")
		}
		// Convert Responses format to Chat format for internal execution
		toolCall = request.ResponsesToolMessage.ToChatAssistantMessageToolCall()
		if toolCall == nil {
			return nil, fmt.Errorf("failed to convert Responses tool message to Chat format")
		}
	default:
		return nil, fmt.Errorf("invalid request type: %s", request.RequestType)
	}

	// Validate toolCall and nested fields
	if toolCall == nil {
		return nil, fmt.Errorf("tool call cannot be nil")
	}
	// Function is a struct value (not a pointer), so it always exists, but Name can be nil
	if toolCall.Function.Name == nil {
		return nil, fmt.Errorf("tool call missing function name")
	}

	now := time.Now()

	// Execute the tool in Chat format (internal execution format)
	chatResult, clientName, originalToolName, err := m.executeToolInternal(ctx, toolCall)
	if err != nil {
		return nil, err
	}

	latency := time.Since(now).Milliseconds()

	extraFields := schemas.BifrostMCPResponseExtraFields{
		ClientName: clientName,
		ToolName:   originalToolName,
		Latency:    latency,
	}

	// Return result in the appropriate format
	switch request.RequestType {
	case schemas.MCPRequestTypeChatToolCall:
		return &schemas.BifrostMCPResponse{
			ChatMessage: chatResult,
			ExtraFields: extraFields,
		}, nil
	case schemas.MCPRequestTypeResponsesToolCall:
		// Validate chatResult is not nil before conversion
		if chatResult == nil {
			return nil, fmt.Errorf("chat result cannot be nil for ResponsesToolCall request type")
		}
		responsesMessage := chatResult.ToResponsesToolMessage()
		if responsesMessage == nil {
			return nil, fmt.Errorf("failed to convert tool result to Responses format")
		}
		return &schemas.BifrostMCPResponse{
			ResponsesMessage: responsesMessage,
			ExtraFields:      extraFields,
		}, nil
	default:
		return nil, fmt.Errorf("invalid request type: %s", request.RequestType)
	}
}

// executeToolInternal is the internal tool executor that works with Chat format.
// This is used internally by ExecuteTool after format conversion.
// Returns: (message, clientName, originalToolName, error)
func (m *ToolsManager) executeToolInternal(ctx *schemas.BifrostContext, toolCall *schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, string, string, error) {
	toolName := *toolCall.Function.Name

	// Check if this is a code mode tool and delegate to CodeMode implementation
	if m.codeMode != nil && m.codeMode.IsCodeModeTool(toolName) {
		msg, err := m.codeMode.ExecuteTool(ctx, *toolCall)
		return msg, "", toolName, err
	}

	// Handle regular MCP tools
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
		return nil, "", "", fmt.Errorf("tool '%s' is not available or not permitted", toolName)
	}

	client := m.clientManager.GetClientForTool(toolName)
	if client == nil {
		return nil, "", "", fmt.Errorf("client not found for tool %s", toolName)
	}

	// Parse tool arguments
	var arguments map[string]interface{}
	if strings.TrimSpace(toolCall.Function.Arguments) == "" {
		arguments = map[string]interface{}{}
	} else {
		if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &arguments); err != nil {
			return nil, "", "", fmt.Errorf("failed to parse tool arguments for '%s': %v", toolName, err)
		}
	}

	// Strip the client name prefix from tool name before calling MCP server
	// The MCP server expects the original tool name (with hyphens), not the sanitized version
	sanitizedToolName := stripClientPrefix(toolName, client.ExecutionConfig.Name)
	originalMCPToolName := getOriginalToolName(sanitizedToolName, client)

	// Call the tool via MCP client -> MCP server
	callRequest := mcp.CallToolRequest{
		Request: mcp.Request{
			Method: string(mcp.MethodToolsCall),
		},
		Params: mcp.CallToolParams{
			Name:      originalMCPToolName,
			Arguments: arguments,
		},
		Header: utils.GetHeadersForToolExecution(ctx, client),
	}

	// Handle per-user OAuth: inject user-specific Authorization header
	if client.ExecutionConfig.AuthType == schemas.MCPAuthTypePerUserOauth {
		if m.oauth2Provider == nil {
			return nil, "", "", fmt.Errorf("per-user OAuth requires an OAuth2Provider but none is configured")
		}
		virtualKeyID, _ := ctx.Value(schemas.BifrostContextKeyGovernanceVirtualKeyID).(string)
		userID, _ := ctx.Value(schemas.BifrostContextKeyUserID).(string)
		sessionToken, _ := ctx.Value(schemas.BifrostContextKeyMCPUserSession).(string)

		// Optional X-Bf-User-Id header overrides user identity; if absent, falls back to virtual key
		if mcpUserID, _ := ctx.Value(schemas.BifrostContextKeyMCPUserID).(string); mcpUserID != "" {
			userID = mcpUserID
		}

		// Try identity-based token lookup first (works even without session token)
		accessToken, err := m.oauth2Provider.GetUserAccessTokenByIdentity(ctx, virtualKeyID, userID, sessionToken, client.ExecutionConfig.ID)
		if err != nil && !errors.Is(err, schemas.ErrOAuth2TokenNotFound) {
			// Had session but token lookup failed with a real error (not just "not found") — return error
			return nil, "", "", fmt.Errorf("failed to get user access token for MCP server %s: %w", client.ExecutionConfig.Name, err)
		}
		if err != nil {
			// No token found — user hasn't authenticated with this MCP server yet.
			// In LLM gateway mode with no identity, we can't track who this user is,
			// so an OAuth flow would produce an orphaned token. Return a clear error instead.
			isMCPGateway, _ := ctx.Value(schemas.BifrostContextKeyIsMCPGateway).(bool)
			if !isMCPGateway && userID == "" && virtualKeyID == "" {
				return nil, "", "", fmt.Errorf(
					"per-user OAuth for %s requires a user identity: include X-Bf-User-Id or a Virtual Key in your request so the token can be linked to you",
					client.ExecutionConfig.Name,
				)
			}

			// Initiate OAuth flow to get a proper authorize URL with session tracking.
			if client.ExecutionConfig.OauthConfigID == nil || *client.ExecutionConfig.OauthConfigID == "" {
				return nil, "", "", fmt.Errorf("per-user OAuth requires an OAuth config but MCP client %s has none", client.ExecutionConfig.Name)
			}
			redirectURI := buildRedirectURIFromContext(ctx)
			if redirectURI == "" {
				return nil, "", "", fmt.Errorf("per-user OAuth requires a redirect URI but none is available in context")
			}
			flowInitiation, sessionID, flowErr := m.oauth2Provider.InitiateUserOAuthFlow(ctx, *client.ExecutionConfig.OauthConfigID, client.ExecutionConfig.ID, redirectURI)
			if flowErr != nil {
				return nil, "", "", fmt.Errorf("failed to initiate per-user OAuth flow for %s: %w", client.ExecutionConfig.Name, flowErr)
			}
			return nil, "", "", &schemas.MCPUserOAuthRequiredError{
				MCPClientID:   client.ExecutionConfig.ID,
				MCPClientName: client.ExecutionConfig.Name,
				AuthorizeURL:  flowInitiation.AuthorizeURL,
				SessionID:     sessionID,
				Message:       fmt.Sprintf("Authentication required for %s. Please visit the authorize URL to connect your account.", client.ExecutionConfig.Name),
			}
		}

		if client.Conn == nil {
			// No persistent connection — create temporary connection with user's token
			toolExecutionTimeout := m.toolExecutionTimeout.Load().(time.Duration)
			toolCtx, cancel := context.WithTimeout(ctx, toolExecutionTimeout)
			defer cancel()

			toolResponse, callErr := executeToolWithUserToken(toolCtx, client.ExecutionConfig, originalMCPToolName, arguments, accessToken, m.logger)
			if callErr != nil {
				if toolCtx.Err() == context.DeadlineExceeded {
					return nil, "", "", fmt.Errorf("MCP tool call timed out after %v: %s", toolExecutionTimeout, toolName)
				}
				m.logger.Error("%s Tool execution failed for %s via client %s: %v", MCPLogPrefix, toolName, client.ExecutionConfig.Name, callErr)
				return nil, "", "", fmt.Errorf("MCP tool call failed: %v", callErr)
			}
			responseText := extractTextFromMCPResponse(toolResponse, toolName)
			return createToolResponseMessage(*toolCall, responseText), client.ExecutionConfig.Name, sanitizedToolName, nil
		}

		// Persistent connection exists — use per-call headers
		headers := make(http.Header)
		if client.ExecutionConfig.Headers != nil {
			for key, value := range client.ExecutionConfig.Headers {
				headers.Add(key, value.GetValue())
			}
		}
		headers.Set("Authorization", "Bearer "+accessToken)
		callRequest.Header = headers
	} else if client.ExecutionConfig.Headers != nil {
		headers := make(http.Header)
		for key, value := range client.ExecutionConfig.Headers {
			headers.Add(key, value.GetValue())
		}
		callRequest.Header = headers
	}

	// Create timeout context for tool execution
	toolExecutionTimeout := m.toolExecutionTimeout.Load().(time.Duration)
	toolCtx, cancel := context.WithTimeout(ctx, toolExecutionTimeout)
	defer cancel()

	toolResponse, callErr := client.Conn.CallTool(toolCtx, callRequest)
	if callErr != nil {
		// Check if it was a timeout error
		if toolCtx.Err() == context.DeadlineExceeded {
			return nil, "", "", fmt.Errorf("MCP tool call timed out after %v: %s", toolExecutionTimeout, toolName)
		}
		m.logger.Error("%s Tool execution failed for %s via client %s: %v", MCPLogPrefix, toolName, client.ExecutionConfig.Name, callErr)
		return nil, "", "", fmt.Errorf("MCP tool call failed: %v", callErr)
	}

	// Extract text from MCP response
	responseText := extractTextFromMCPResponse(toolResponse, toolName)

	// Create tool response message
	return createToolResponseMessage(*toolCall, responseText), client.ExecutionConfig.Name, sanitizedToolName, nil
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
	executeTool func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error),
) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	// Use provided executeTool function, or fall back to internal ExecuteTool
	executeToolFunc := executeTool
	if executeToolFunc == nil {
		executeToolFunc = m.ExecuteTool
	}
	return m.agentModeExecutor.ExecuteAgentForChatRequest(
		ctx,
		int(m.maxAgentDepth.Load()),
		req,
		resp,
		makeReq,
		m.fetchNewRequestIDFunc,
		executeToolFunc,
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
	executeTool func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error),
) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	// Use provided executeTool function, or fall back to internal ExecuteTool
	executeToolFunc := executeTool
	if executeToolFunc == nil {
		executeToolFunc = m.ExecuteTool
	}
	return m.agentModeExecutor.ExecuteAgentForResponsesRequest(
		ctx,
		int(m.maxAgentDepth.Load()),
		req,
		resp,
		makeReq,
		m.fetchNewRequestIDFunc,
		executeToolFunc,
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

	// Update CodeMode configuration — propagate whenever either field is set
	if m.codeMode != nil && (config.CodeModeBindingLevel != "" || config.ToolExecutionTimeout > 0) {
		m.codeMode.UpdateConfig(&CodeModeConfig{
			BindingLevel:         config.CodeModeBindingLevel,
			ToolExecutionTimeout: config.ToolExecutionTimeout,
		})
	}

	m.disableAutoToolInject.Store(config.DisableAutoToolInject)

	m.logger.Info("%s tool manager configuration updated with tool execution timeout: %v, max agent depth: %d, and code mode binding level: %s", MCPLogPrefix, config.ToolExecutionTimeout, config.MaxAgentDepth, config.CodeModeBindingLevel)
}

// executeToolWithUserToken creates a temporary MCP connection using the user's
// OAuth access token, calls the specified tool, and closes the connection.
// This is used for per_user_oauth clients which have no persistent connection —
// each tool call gets its own short-lived connection authenticated with the
// requesting user's token.
//
// Parameters:
//   - ctx: context with timeout for the entire operation
//   - config: MCP client configuration (connection URL, name)
//   - toolName: original MCP tool name to call
//   - arguments: tool call arguments
//   - accessToken: user's OAuth access token
//   - logger: logger instance
//
// Returns:
//   - *mcp.CallToolResult: tool execution result
//   - error: any error during connection or execution
func executeToolWithUserToken(ctx context.Context, config *schemas.MCPClientConfig, toolName string, arguments map[string]interface{}, accessToken string, logger schemas.Logger) (*mcp.CallToolResult, error) {
	if config.ConnectionString == nil || config.ConnectionString.GetValue() == "" {
		return nil, fmt.Errorf("connection URL is required for per-user OAuth tool execution")
	}

	// Create HTTP transport with the user's Bearer token, preserving configured headers
	headers := make(map[string]string)
	if config.Headers != nil {
		for key, value := range config.Headers {
			headers[key] = value.GetValue()
		}
	}
	headers["Authorization"] = "Bearer " + accessToken
	httpTransport, err := transport.NewStreamableHTTP(config.ConnectionString.GetValue(), transport.WithHTTPHeaders(headers))
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP transport: %w", err)
	}

	// Create temporary MCP client
	tempClient := client.NewClient(httpTransport)
	if err := tempClient.Start(ctx); err != nil {
		return nil, fmt.Errorf("failed to start temporary MCP connection: %w", err)
	}
	defer tempClient.Close()

	// Initialize MCP handshake
	initRequest := mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			Capabilities:    mcp.ClientCapabilities{},
			ClientInfo: mcp.Implementation{
				Name:    fmt.Sprintf("Bifrost-%s-user", config.Name),
				Version: "1.0.0",
			},
		},
	}
	if _, err := tempClient.Initialize(ctx, initRequest); err != nil {
		return nil, fmt.Errorf("failed to initialize temporary MCP connection: %w", err)
	}

	// Call the tool
	callRequest := mcp.CallToolRequest{
		Request: mcp.Request{
			Method: string(mcp.MethodToolsCall),
		},
		Params: mcp.CallToolParams{
			Name:      toolName,
			Arguments: arguments,
		},
	}
	return tempClient.CallTool(ctx, callRequest)
}

// buildRedirectURIFromContext extracts the OAuth redirect URI from context.
// The URI is set by the HTTP middleware from the request's host.
func buildRedirectURIFromContext(ctx *schemas.BifrostContext) string {
	if uri, ok := ctx.Value(schemas.BifrostContextKeyOAuthRedirectURI).(string); ok && uri != "" {
		return uri
	}
	// Fallback — should not happen if middleware is configured correctly
	return ""
}

// GetCodeModeBindingLevel returns the current code mode binding level.
// This method is safe to call concurrently from multiple goroutines.
func (m *ToolsManager) GetCodeModeBindingLevel() schemas.CodeModeBindingLevel {
	if m.codeMode != nil {
		return m.codeMode.GetBindingLevel()
	}
	return schemas.CodeModeBindingLevelServer
}
