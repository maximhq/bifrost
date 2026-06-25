package mcp

import (
	"context"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/mcp/credstore"
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
)

// ============================================================================
// TYPE DEFINITIONS
// ============================================================================

// MCPManager manages MCP integration for Bifrost core.
// It provides a bridge between Bifrost and various MCP servers, supporting
// both local tool hosting and external MCP server connections.
type MCPManager struct {
	ctx                  context.Context
	logger               schemas.Logger                     // Logger instance for this manager
	credStore            schemas.MCPCredentialStore         // Resolves credentials per-call for MCP tool execution
	toolsManager         *ToolsManager                      // Handler for MCP tools
	server               *server.MCPServer                  // Local MCP server instance for hosting tools (STDIO-based)
	clientMap            map[string]*schemas.MCPClientState // Map of MCP client names to their configurations
	mu                   sync.RWMutex                       // Read-write mutex for thread-safe operations
	serverRunning        bool                               // Track whether local MCP server is running
	healthMonitorManager *HealthMonitorManager              // Manager for client health monitors
	toolSyncManager      *ToolSyncManager                   // Manager for periodic tool synchronization
	reconnectingClients  sync.Map                           // Tracks in-flight reconnect attempts per client ID (map[string]bool)
	bootClientConfigs    []*schemas.MCPClientConfig         // Client configs supplied at construction, dialed by ConnectConfiguredClients
	connectOnce          sync.Once                          // Ensures ConnectConfiguredClients dials the boot configs exactly once

	// Plugin pipeline access for connect/ping/list_tools hooks. nil-safe — gates short-circuit
	// to the underlying op when no pipeline is configured. Also used by ToolsManager for the
	// existing execute-tool hooks.
	pluginPipelineProvider func() PluginPipeline
	releasePluginPipeline  func(pipeline PluginPipeline)
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
//   - ctx: Context for the MCP manager
//   - config: MCP configuration including server port and client configs
//   - credStore: CredentialStore that resolves per-call credentials (Bearer
//     tokens, static headers, user-submitted headers) and signals whether
//     each client requires an ephemeral upstream connection. Pass nil only
//     in tests where credential resolution is irrelevant.
//   - logger: Logger instance for structured logging (uses default if nil)
//   - codeMode: Optional CodeMode implementation for code execution (e.g., Starlark).
//     Pass nil if code mode is not needed. The CodeMode's dependencies will be
//     injected automatically via SetDependencies after the manager is created.
//
// Returns:
//   - *MCPManager: Initialized manager instance
func NewMCPManager(ctx context.Context, config schemas.MCPConfig, credStore schemas.MCPCredentialStore, logger schemas.Logger, codeMode CodeMode) *MCPManager {
	if logger == nil {
		logger = defaultLogger
	}
	// Default to a provider-less CredentialStore so tests (and callers that
	// don't wire OAuth / per-user-headers) get a working store: static /
	// headers / none resolvers stay functional, per-user resolvers cleanly
	// error on use.
	if credStore == nil {
		credStore = credstore.NewCredStore(nil, nil, logger)
	}
	// Set default values
	if config.ToolManagerConfig == nil {
		config.ToolManagerConfig = &schemas.MCPToolManagerConfig{
			ToolExecutionTimeout: schemas.Duration(schemas.DefaultToolExecutionTimeout),
			MaxAgentDepth:        schemas.DefaultMaxAgentDepth,
		}
	}
	// Creating new instance
	manager := &MCPManager{
		ctx:                  ctx,
		logger:               logger,
		clientMap:            make(map[string]*schemas.MCPClientState),
		healthMonitorManager: NewHealthMonitorManager(),
		toolSyncManager:      NewToolSyncManager(config.ToolSyncInterval),
		credStore:            credStore,
	}
	// Convert plugin pipeline provider functions to the interface expected by ToolsManager
	var pluginPipelineProvider func() PluginPipeline
	var releasePluginPipeline func(pipeline PluginPipeline)

	if config.PluginPipelineProvider != nil && config.ReleasePluginPipeline != nil {
		pluginPipelineProvider = func() PluginPipeline {
			if pipeline := config.PluginPipelineProvider(); pipeline != nil {
				if pp, ok := pipeline.(PluginPipeline); ok {
					return pp
				}
			}
			return nil
		}
		releasePluginPipeline = func(pipeline PluginPipeline) {
			config.ReleasePluginPipeline(pipeline)
		}
	}

	manager.pluginPipelineProvider = pluginPipelineProvider
	manager.releasePluginPipeline = releasePluginPipeline
	manager.toolsManager = NewToolsManager(config.ToolManagerConfig, manager, config.FetchNewRequestIDFunc, credStore, logger)

	// Set up CodeMode if provided - inject dependencies after manager is created
	if codeMode != nil {
		deps := manager.toolsManager.GetCodeModeDependencies()
		codeMode.SetDependencies(deps)
		manager.toolsManager.SetCodeMode(codeMode)
	}

	// Retain client configs for an explicit dial via ConnectConfiguredClients.
	// Construction no longer connects: callers dial after every plugin is
	// registered so PreMCPConnectionHook sees the full plugin set (otherwise a
	// connect issued during Init would run against the point-in-time plugin
	// snapshot and silently skip plugins registered afterwards).
	manager.bootClientConfigs = config.ClientConfigs
	manager.logger.Info(MCPLogPrefix + " MCP Manager initialized")
	return manager
}

// ConnectConfiguredClients dials the MCP clients supplied at construction time
// (MCPConfig.ClientConfigs). It is separated from NewMCPManager so the caller can
// run it only after all plugins are registered, ensuring every PreMCPConnectionHook
// participates in the connection. Safe to call once after construction; clients are
// dialed in parallel and a failed client is retained in the Disconnected state with
// a health monitor that recovers it automatically.
func (m *MCPManager) ConnectConfiguredClients(ctx context.Context) {
	m.connectOnce.Do(func() {
		m.connectConfiguredClients(ctx)
	})
}

// connectConfiguredClients performs the actual dial. It is invoked exactly once via
// m.connectOnce, guarding against accidental repeat invocations (e.g. a double-Bootstrap
// or a future code path) that would otherwise re-dial every boot config.
func (m *MCPManager) connectConfiguredClients(ctx context.Context) {
	if len(m.bootClientConfigs) == 0 {
		return
	}
	if ctx == nil {
		ctx = m.ctx
	}
	// Add clients in parallel
	wg := sync.WaitGroup{}
	wg.Add(len(m.bootClientConfigs))
	for _, clientConfig := range m.bootClientConfigs {
		go func(clientConfig *schemas.MCPClientConfig) {
			defer wg.Done()
			if err := m.AddClient(ctx, clientConfig); err != nil {
				m.logger.Warn("%s Failed to register MCP client %s: %v", MCPLogPrefix, clientConfig.Name, err)
				// Retain the entry in Disconnected state and start a health monitor to
				// recover it automatically. On startup, a connection failure is likely
				// transient (e.g. autoscaling cold start) — the client was previously
				// configured and should be recovered without user intervention.
				m.mu.Lock()
				if _, exists := m.clientMap[clientConfig.ID]; !exists {
					m.clientMap[clientConfig.ID] = &schemas.MCPClientState{
						Name:            clientConfig.Name,
						ExecutionConfig: clientConfig,
						State:           schemas.MCPConnectionStateDisconnected,
						ToolMap:         make(map[string]schemas.ChatTool),
						ToolNameMapping: make(map[string]string),
						ConnectionInfo: &schemas.MCPClientConnectionInfo{
							Type: clientConfig.ConnectionType,
						},
					}
				} else {
					m.clientMap[clientConfig.ID].State = schemas.MCPConnectionStateDisconnected
				}
				m.mu.Unlock()
				isPingAvailable := true
				if clientConfig.IsPingAvailable != nil {
					isPingAvailable = *clientConfig.IsPingAvailable
				}
				monitor := NewClientHealthMonitor(m, clientConfig.ID, DefaultHealthCheckInterval, isPingAvailable, m.logger)
				m.healthMonitorManager.StartMonitoring(monitor)
			}
		}(clientConfig)
	}
	wg.Wait()
}

// SetPluginPipeline updates the plugin pipeline provider and release function on the manager's
// ToolsManager and CodeMode. Call this after attaching an externally-created MCPManager to a Bifrost
// instance so that nested tool calls in code mode can run through Bifrost's plugin hooks.
func (manager *MCPManager) SetPluginPipeline(provider func() PluginPipeline, release func(PluginPipeline)) {
	manager.pluginPipelineProvider = provider
	manager.releasePluginPipeline = release
}

// GetPluginPipeline returns a plugin pipeline from the provider, or nil if no provider is configured.
func (manager *MCPManager) GetPluginPipeline() PluginPipeline {
	if manager.pluginPipelineProvider != nil {
		return manager.pluginPipelineProvider()
	}
	return nil
}

// ReleasePluginPipeline releases a plugin pipeline back to the pool via the configured release function.
func (manager *MCPManager) ReleasePluginPipeline(pipeline PluginPipeline) {
	if manager.releasePluginPipeline != nil {
		manager.releasePluginPipeline(pipeline)
	}
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
func (m *MCPManager) AddToolsToRequest(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) *schemas.BifrostRequest {
	return m.toolsManager.ParseAndAddToolsToRequest(ctx, req)
}

func (m *MCPManager) GetAvailableTools(ctx *schemas.BifrostContext) []schemas.ChatTool {
	return m.toolsManager.GetAvailableTools(ctx)
}

// UpdateToolManagerConfig updates the configuration for the tool manager.
// This allows runtime updates to settings like execution timeout and max agent depth.
//
// Parameters:
//   - config: The new tool manager configuration to apply
func (m *MCPManager) UpdateToolManagerConfig(config *schemas.MCPToolManagerConfig) {
	m.toolsManager.UpdateConfig(config)
}

// CheckAndExecuteAgentForChatRequest checks if the chat response contains tool calls,
// and if so, executes agent mode to handle the tool calls iteratively. If no tool calls
// are present, it returns the original response unchanged.
//
// Agent mode enables autonomous tool execution where:
//  1. Tool calls are automatically executed
//  2. Results are fed back to the LLM
//  3. The loop continues until no more tool calls are made or max depth is reached
//  4. Non-auto-executable tools are returned to the caller
//
// This method is available for both Chat Completions and Responses APIs.
// For Responses API, use CheckAndExecuteAgentForResponsesRequest().
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
	ctx *schemas.BifrostContext,
	req *schemas.BifrostChatRequest,
	response *schemas.BifrostChatResponse,
	makeReq func(ctx *schemas.BifrostContext, req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError),
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
		m.logger.Debug("No tool calls detected, returning response")
		return response, nil
	}
	// Execute agent mode. The agent's tool executions go through the plugin gate
	// internally via m.executeToolForAgent — no external callback injection needed.
	return m.toolsManager.ExecuteAgentForChatRequest(ctx, req, response, makeReq, m.executeToolForAgent)
}

// CheckAndExecuteAgentForResponsesRequest checks if the responses response contains tool calls,
// and if so, executes agent mode to handle the tool calls iteratively. If no tool calls
// are present, it returns the original response unchanged.
//
// Agent mode for Responses API works identically to Chat API:
//  1. Detects tool calls in the response (function_call messages)
//  2. Automatically executes tools in parallel when possible
//  3. Feeds results back to the LLM in Responses API format
//  4. Continues the loop until no more tool calls or max depth reached
//  5. Returns non-auto-executable tools to the caller
//
// Format Handling:
// This method automatically handles format conversions:
//   - Responses tool calls (ResponsesToolMessage) are converted to Chat format for execution
//   - Tool execution results are converted back to Responses format (ResponsesMessage)
//   - All conversions use the adapters in agent_adaptors.go and converters in schemas/mux.go
//
// This provides full feature parity between Chat Completions and Responses APIs for tool execution.
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
	ctx *schemas.BifrostContext,
	req *schemas.BifrostResponsesRequest,
	response *schemas.BifrostResponsesResponse,
	makeReq func(ctx *schemas.BifrostContext, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError),
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
		m.logger.Debug("No tool calls detected, returning response")
		return response, nil
	}
	// Execute agent mode. Tool executions go through the plugin gate internally.
	return m.toolsManager.ExecuteAgentForResponsesRequest(ctx, req, response, makeReq, m.executeToolForAgent)
}

// Cleanup performs cleanup of all MCP resources including clients and local server.
// This function safely disconnects all MCP clients (HTTP, STDIO, and SSE) and
// cleans up the local MCP server. It handles proper cancellation of SSE contexts
// and closes all transport connections.
//
// Returns:
//   - error: Always returns nil, but maintains error interface for consistency
func (m *MCPManager) Cleanup() error {
	// Stop all health monitors first
	m.healthMonitorManager.StopAll()

	// Stop all tool syncers
	m.toolSyncManager.StopAll()

	m.mu.Lock()
	defer m.mu.Unlock()

	// Disconnect all external MCP clients
	for id := range m.clientMap {
		if err := m.removeClientUnsafe(id); err != nil {
			m.logger.Error("%s Failed to remove MCP client %s: %v", MCPLogPrefix, id, err)
		}
	}

	// Clear the client map
	m.clientMap = make(map[string]*schemas.MCPClientState)

	// Clear local server reference
	// Note: mark3labs/mcp-go STDIO server cleanup is handled automatically
	if m.server != nil {
		m.logger.Info(MCPLogPrefix + " Clearing local MCP server reference")
		m.server = nil
		m.serverRunning = false
	}

	m.logger.Info(MCPLogPrefix + " MCP cleanup completed")
	return nil
}
