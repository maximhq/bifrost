package mcp

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"

	"github.com/mark3labs/mcp-go/server"
)

// ============================================================================
// CONSTANTS
// ============================================================================

const (
	// MCP defaults and identifiers
	BifrostMCPVersion                   = "1.0.0"           // Version identifier for Bifrost
	BifrostMCPClientName                = "BifrostClient"   // Name for internal Bifrost MCP client
	BifrostMCPClientKey                 = "bifrostInternal" // Key for internal Bifrost client in clientMap
	MCPLogPrefix                        = "[Bifrost MCP]"   // Consistent logging prefix
	MCPClientConnectionEstablishTimeout = 30 * time.Second  // Timeout for MCP client connection establishment

	// Context keys for client filtering in requests
	// NOTE: []string is used for both keys, and by default all clients/tools are included (when nil).
	// If "*" is present, all clients/tools are included, and [] means no clients/tools are included.
	// Request context filtering takes priority over client config - context can override client exclusions.
	MCPContextKeyIncludeClients schemas.BifrostContextKey = "mcp-include-clients" // Context key for whitelist client filtering
	MCPContextKeyIncludeTools   schemas.BifrostContextKey = "mcp-include-tools"   // Context key for whitelist tool filtering (Note: toolName should be in "clientName/toolName" format)
)

// ============================================================================
// TYPE DEFINITIONS
// ============================================================================

// MCPManager manages MCP integration for Bifrost core.
// It provides a bridge between Bifrost and various MCP servers, supporting
// both local tool hosting and external MCP server connections.
type MCPManager struct {
	ctx           context.Context
	toolsHandler  *ToolsManager                      // Handler for MCP tools
	server        *server.MCPServer                  // Local MCP server instance for hosting tools (STDIO-based)
	clientMap     map[string]*schemas.MCPClientState // Map of MCP client names to their configurations
	mu            sync.RWMutex                       // Read-write mutex for thread-safe operations
	serverRunning bool                               // Track whether local MCP server is running
}

// MCPToolFunction is a generic function type for handling tool calls with typed arguments.
// T represents the expected argument structure for the tool.
type MCPToolFunction[T any] func(args T) (string, error)

// ============================================================================
// CONSTRUCTOR AND INITIALIZATION
// ============================================================================

// NewMCPManager creates and initializes a new MCP manager instance.
//
// Parameters:
//   - config: MCP configuration including server port and client configs
//   - logger: Logger instance for structured logging (uses default if nil)
//
// Returns:
//   - *MCPManager: Initialized manager instance
//   - error: Any initialization error
func NewMCPManager(ctx context.Context, config schemas.MCPConfig, logger schemas.Logger) *MCPManager {
	SetLogger(logger)
	// Set default values
	if config.ToolManagerConfig == nil {
		config.ToolManagerConfig = &schemas.MCPToolManagerConfig{
			ToolExecutionTimeout: schemas.DefaultToolExecutionTimeout,
			MaxAgentDepth:        schemas.DefaultMaxAgentDepth,
		}
	}
	// Creating new instance
	manager := &MCPManager{
		ctx:       ctx,
		clientMap: make(map[string]*schemas.MCPClientState),
	}
	manager.toolsHandler = NewToolsManager(config.ToolManagerConfig, manager, config.FetchNewRequestIDFunc)
	// Process client configs: create client map entries and establish connections
	for _, clientConfig := range config.ClientConfigs {
		if err := manager.AddClient(clientConfig); err != nil {
			logger.Warn(fmt.Sprintf("%s Failed to add MCP client %s: %v", MCPLogPrefix, clientConfig.Name, err))
		}
	}
	logger.Info(MCPLogPrefix + " MCP Manager initialized")
	return manager
}

// AddToolsToRequest parses available MCP tools from the context and adds them to the request.
// It respects context-based filtering for clients and tools, and returns the modified request
// with tools attached.
//
// Parameters:
//   - ctx: Context containing optional client/tool filtering keys
//   - req: The Bifrost request to add tools to
//
// Returns:
//   - *schemas.BifrostRequest: The request with tools added
func (m *MCPManager) AddToolsToRequest(ctx context.Context, req *schemas.BifrostRequest) *schemas.BifrostRequest {
	return m.toolsHandler.ParseAndAddToolsToRequest(ctx, req)
}

// ExecuteTool executes a single tool call from a chat assistant message.
// It handles tool execution, error handling, and returns the result as a chat message.
//
// Parameters:
//   - ctx: Context for the tool execution
//   - toolCall: The tool call to execute, containing tool name and arguments
//
// Returns:
//   - *schemas.ChatMessage: The result message containing tool execution output
//   - error: Any error that occurred during tool execution
func (m *MCPManager) ExecuteTool(ctx context.Context, toolCall schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, error) {
	return m.toolsHandler.ExecuteTool(ctx, toolCall)
}

// UpdateToolManagerConfig updates the configuration for the tool manager.
// This allows runtime updates to settings like execution timeout and max agent depth.
//
// Parameters:
//   - config: The new tool manager configuration to apply
func (m *MCPManager) UpdateToolManagerConfig(config *schemas.MCPToolManagerConfig) {
	m.toolsHandler.UpdateConfig(config)
}

// CheckAndExecuteAgentForChatRequest checks if the chat response contains tool calls,
// and if so, executes agent mode to handle the tool calls iteratively. If no tool calls
// are present, it returns the original response unchanged.
//
// Parameters:
//   - ctx: Context for the agent execution
//   - req: The original chat request
//   - response: The initial chat response that may contain tool calls
//   - makeReq: Function to make subsequent chat requests during agent execution
//
// Returns:
//   - *schemas.BifrostChatResponse: The final response after agent execution (or original if no tool calls)
//   - *schemas.BifrostError: Any error that occurred during agent execution
func (m *MCPManager) CheckAndExecuteAgentForChatRequest(
	ctx *context.Context,
	req *schemas.BifrostChatRequest,
	response *schemas.BifrostChatResponse,
	makeReq func(ctx context.Context, req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError),
) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	if makeReq == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: "makeReq is required to execute agent mode",
			},
		}
	}
	// Check if initial response has tool calls
	if !hasToolCallsForChatResponse(response) {
		logger.Debug("No tool calls detected, returning response")
		return response, nil
	}
	// Execute agent mode
	return m.toolsHandler.ExecuteAgentForChatRequest(ctx, req, response, makeReq)
}

// CheckAndExecuteAgentForResponsesRequest checks if the responses response contains tool calls,
// and if so, executes agent mode to handle the tool calls iteratively. If no tool calls
// are present, it returns the original response unchanged.
//
// Parameters:
//   - ctx: Context for the agent execution
//   - req: The original responses request
//   - response: The initial responses response that may contain tool calls
//   - makeReq: Function to make subsequent responses requests during agent execution
//
// Returns:
//   - *schemas.BifrostResponsesResponse: The final response after agent execution (or original if no tool calls)
//   - *schemas.BifrostError: Any error that occurred during agent execution
func (m *MCPManager) CheckAndExecuteAgentForResponsesRequest(
	ctx *context.Context,
	req *schemas.BifrostResponsesRequest,
	response *schemas.BifrostResponsesResponse,
	makeReq func(ctx context.Context, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError),
) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	if makeReq == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: "makeReq is required to execute agent mode",
			},
		}
	}
	// Check if initial response has tool calls
	if !hasToolCallsForResponsesResponse(response) {
		logger.Debug("No tool calls detected, returning response")
		return response, nil
	}
	// Execute agent mode
	return m.toolsHandler.ExecuteAgentForResponsesRequest(ctx, req, response, makeReq)
}

// Cleanup performs cleanup of all MCP resources including clients and local server.
// This function safely disconnects all MCP clients (HTTP, STDIO, and SSE) and
// cleans up the local MCP server. It handles proper cancellation of SSE contexts
// and closes all transport connections.
//
// Returns:
//   - error: Always returns nil, but maintains error interface for consistency
func (m *MCPManager) Cleanup() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Disconnect all external MCP clients
	for id := range m.clientMap {
		if err := m.removeClientUnsafe(id); err != nil {
			logger.Error("%s Failed to remove MCP client %s: %v", MCPLogPrefix, id, err)
		}
	}

	// Clear the client map
	m.clientMap = make(map[string]*schemas.MCPClientState)

	// Clear local server reference
	// Note: mark3labs/mcp-go STDIO server cleanup is handled automatically
	if m.server != nil {
		logger.Info(MCPLogPrefix + " Clearing local MCP server reference")
		m.server = nil
		m.serverRunning = false
	}

	logger.Info(MCPLogPrefix + " MCP cleanup completed")
	return nil
}
