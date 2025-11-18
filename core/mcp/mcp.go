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
	BifrostMCPVersion                   = "1.0.0"            // Version identifier for Bifrost
	BifrostMCPClientName                = "BifrostClient"    // Name for internal Bifrost MCP client
	BifrostMCPClientKey                 = "bifrost-internal" // Key for internal Bifrost client in clientMap
	MCPLogPrefix                        = "[Bifrost MCP]"    // Consistent logging prefix
	MCPClientConnectionEstablishTimeout = 30 * time.Second   // Timeout for MCP client connection establishment

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
	ctx                  context.Context
	toolsHandler         schemas.MCPToolHandler             // Handler for MCP tools
	server               *server.MCPServer                  // Local MCP server instance for hosting tools (STDIO-based)
	clientMap            map[string]*schemas.MCPClientState // Map of MCP client names to their configurations
	mu                   sync.RWMutex                       // Read-write mutex for thread-safe operations
	serverRunning        bool                               // Track whether local MCP server is running
	maxAgentDepth        int                                // Maximum depth of agent mode tool calls
	toolExecutionTimeout time.Duration                      // Timeout for individual tool execution

	// Function to fetch a new request ID for each tool call result message in agent mode,
	// this is used to ensure that the tool call result messages are unique and can be tracked in plugins or by the user.
	// This id is attached to ctx.Value(schemas.BifrostContextKeyRequestID) in the agent mode.
	// If not provider, same request ID is used for all tool call result messages without any overrides.
	fetchNewRequestIDFunc func(ctx context.Context) string
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
func NewMCPManager(ctx context.Context, config schemas.MCPConfig, logger schemas.Logger) (*MCPManager, error) {
	SetLogger(logger)
	// Set default values
	maxAgentDepth := config.MaxAgentDepth
	if maxAgentDepth <= 0 {
		maxAgentDepth = 10 // Default max depth
	}
	toolExecutionTimeout := time.Duration(config.ToolExecutionTimeout) * time.Second
	if toolExecutionTimeout <= 0 {
		toolExecutionTimeout = 30 * time.Second // Default timeout
	}
	// Creating new instance
	manager := &MCPManager{
		ctx:                   ctx,
		clientMap:             make(map[string]*schemas.MCPClientState),
		maxAgentDepth:         maxAgentDepth,
		toolExecutionTimeout:  toolExecutionTimeout,
		fetchNewRequestIDFunc: config.FetchNewRequestIDFunc,
	}
	toolsHandler, err := NewDefaultToolsHandler(&DefaultHandlerConfig{
		toolExecutionTimeout: toolExecutionTimeout,
		maxAgentDepth:        maxAgentDepth,
	})
	if err != nil {
		return nil, err
	}
	toolsHandler.SetToolsFetcherFunc(manager.getAvailableTools)
	toolsHandler.SetClientForToolFetcherFunc(manager.findMCPClientForTool)
	toolsHandler.SetFetchNewRequestIDFunc(config.FetchNewRequestIDFunc)
	manager.toolsHandler = toolsHandler
	// Process client configs: create client map entries and establish connections
	for _, clientConfig := range config.ClientConfigs {
		if err := manager.AddClient(clientConfig); err != nil {
			logger.Warn(fmt.Sprintf("%s Failed to add MCP client %s: %v", MCPLogPrefix, clientConfig.Name, err))
		}
	}
	logger.Info(MCPLogPrefix + " MCP Manager initialized")
	return manager, nil
}

func (m *MCPManager) AddToolsToRequest(ctx context.Context, req *schemas.BifrostRequest) *schemas.BifrostRequest {
	if !m.toolsHandler.SetupCompleted() {
		logger.Error("%s Tools handler is not fully setup", MCPLogPrefix)
		return req
	}
	return m.toolsHandler.ParseAndAddToolsToRequest(ctx, req)
}

func (m *MCPManager) ExecuteTool(ctx context.Context, toolCall schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, error) {
	if !m.toolsHandler.SetupCompleted() {
		logger.Error("%s Tools handler is not fully setup", MCPLogPrefix)
		return nil, fmt.Errorf("tools handler is not fully setup")
	}
	// Check if the user has permission to execute the tool call
	availableTools := m.getAvailableTools(ctx)
	toolName := toolCall.Function.Name
	if toolName == nil {
		return nil, fmt.Errorf("tool call missing function name")
	}

	// Check if the tool is available
	toolFound := false
	for _, mcpTool := range availableTools {
		if mcpTool.Function != nil && mcpTool.Function.Name == *toolName {
			toolFound = true
			break
		}
	}

	if !toolFound {
		return nil, fmt.Errorf("tool '%s' is not available or not permitted", *toolName)
	}

	return m.toolsHandler.ExecuteTool(ctx, toolCall)
}

func (m *MCPManager) CheckAndExecuteAgent(ctx context.Context, req *schemas.BifrostChatRequest, response *schemas.BifrostChatResponse, llmCaller schemas.BifrostLLMCaller) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	if !m.toolsHandler.SetupCompleted() {
		logger.Error("%s Tools handler is not fully setup", MCPLogPrefix)
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: "tools handler is not fully setup",
			},
		}
	}
	if llmCaller == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: "llmCaller is required to execute agent mode",
			},
		}
	}
	// Check if initial response has tool calls
	if !hasToolCalls(response) {
		logger.Debug("No tool calls detected, returning response")
		return response, nil
	}
	return m.toolsHandler.ExecuteAgent(ctx, req, response, llmCaller)
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
