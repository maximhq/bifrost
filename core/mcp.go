package bifrost

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
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
	ctx           context.Context
	server        *server.MCPServer     // Local MCP server instance for hosting tools (STDIO-based)
	clientMap     map[string]*MCPClient // Map of MCP client names to their configurations
	mu            sync.RWMutex          // Read-write mutex for thread-safe operations
	serverRunning bool                  // Track whether local MCP server is running
	logger        schemas.Logger        // Logger instance for structured logging
}

// MCPClient represents a connected MCP client with its configuration and tools.
type MCPClient struct {
	// Name            string                      // Unique name for this client
	Conn            *client.Client              // Active MCP client connection
	ExecutionConfig schemas.MCPClientConfig     // Tool filtering settings
	ToolMap         map[string]schemas.ChatTool // Available tools mapped by name
	ConnectionInfo  MCPClientConnectionInfo     `json:"connection_info"` // Connection metadata for management
	cancelFunc      context.CancelFunc          `json:"-"`               // Cancel function for SSE connections (not serialized)
}

// MCPClientConnectionInfo stores metadata about how a client is connected.
type MCPClientConnectionInfo struct {
	Type               schemas.MCPConnectionType `json:"type"`                           // Connection type (HTTP, STDIO, SSE, or InProcess)
	ConnectionURL      *string                   `json:"connection_url,omitempty"`       // HTTP/SSE endpoint URL (for HTTP/SSE connections)
	StdioCommandString *string                   `json:"stdio_command_string,omitempty"` // Command string for display (for STDIO connections)
}

// MCPToolHandler is a generic function type for handling tool calls with typed arguments.
// T represents the expected argument structure for the tool.
type MCPToolHandler[T any] func(args T) (string, error)

// ============================================================================
// CONSTRUCTOR AND INITIALIZATION
// ============================================================================

// newMCPManager creates and initializes a new MCP manager instance.
//
// Parameters:
//   - config: MCP configuration including server port and client configs
//   - logger: Logger instance for structured logging (uses default if nil)
//
// Returns:
//   - *MCPManager: Initialized manager instance
//   - error: Any initialization error
func newMCPManager(ctx context.Context, config schemas.MCPConfig, logger schemas.Logger) (*MCPManager, error) {
	// Creating new instance
	manager := &MCPManager{
		ctx:       ctx,
		clientMap: make(map[string]*MCPClient),
		logger:    logger,
	}
	// Process client configs: create client map entries and establish connections
	for _, clientConfig := range config.ClientConfigs {
		if err := manager.AddClient(clientConfig); err != nil {
			manager.logger.Warn(fmt.Sprintf("%s Failed to add MCP client %s: %v", MCPLogPrefix, clientConfig.Name, err))
		}
	}
	manager.logger.Info(MCPLogPrefix + " MCP Manager initialized")
	return manager, nil
}

// GetClients returns all MCP clients managed by the manager.
//
// Returns:
//   - []*MCPClient: List of all MCP clients
//   - error: Any retrieval error
func (m *MCPManager) GetClients() ([]MCPClient, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	clients := make([]MCPClient, 0, len(m.clientMap))
	for _, client := range m.clientMap {
		clients = append(clients, *client)
	}

	return clients, nil
}

// ReconnectClient attempts to reconnect an MCP client if it is disconnected.
func (m *MCPManager) ReconnectClient(id string) error {
	m.mu.Lock()

	client, ok := m.clientMap[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("client %s not found", id)
	}

	m.mu.Unlock()

	// connectToMCPClient handles locking internally
	err := m.connectToMCPClient(client.ExecutionConfig)
	if err != nil {
		return fmt.Errorf("failed to connect to MCP client %s: %w", id, err)
	}

	return nil
}

// AddClient adds a new MCP client to the manager.
// It validates the client configuration and establishes a connection.
//
// Parameters:
//   - config: MCP client configuration
//
// Returns:
func (m *MCPManager) AddClient(config schemas.MCPClientConfig) error {
	if err := validateMCPClientConfig(&config); err != nil {
		return fmt.Errorf("invalid MCP client configuration: %w", err)
	}

	// Make a copy of the config to use after unlocking
	configCopy := config

	m.mu.Lock()

	if _, ok := m.clientMap[config.ID]; ok {
		m.mu.Unlock()
		return fmt.Errorf("client %s already exists", config.Name)
	}

	// Create placeholder entry
	m.clientMap[config.ID] = &MCPClient{
		ExecutionConfig: config,
		ToolMap:         make(map[string]schemas.ChatTool),
	}

	// Temporarily unlock for the connection attempt
	// This is to avoid deadlocks when the connection attempt is made
	m.mu.Unlock()

	// Connect using the copied config
	if err := m.connectToMCPClient(configCopy); err != nil {
		// Re-lock to clean up the failed entry
		m.mu.Lock()
		delete(m.clientMap, config.ID)
		m.mu.Unlock()
		return fmt.Errorf("failed to connect to MCP client %s: %w", config.Name, err)
	}

	return nil
}

// RemoveClient removes an MCP client from the manager.
// It handles cleanup for all transport types (HTTP, STDIO, SSE).
//
// Parameters:
//   - id: ID of the client to remove
func (m *MCPManager) RemoveClient(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.removeClientUnsafe(id)
}

func (m *MCPManager) removeClientUnsafe(id string) error {
	client, ok := m.clientMap[id]
	if !ok {
		return fmt.Errorf("client %s not found", id)
	}

	m.logger.Info(fmt.Sprintf("%s Disconnecting MCP client: %s", MCPLogPrefix, client.ExecutionConfig.Name))

	// Cancel SSE context if present (required for proper SSE cleanup)
	if client.cancelFunc != nil {
		client.cancelFunc()
		client.cancelFunc = nil
	}

	// Close the client transport connection
	// This handles cleanup for all transport types (HTTP, STDIO, SSE)
	if client.Conn != nil {
		if err := client.Conn.Close(); err != nil {
			m.logger.Error("%s Failed to close MCP client %s: %v", MCPLogPrefix, client.ExecutionConfig.Name, err)
		}
		client.Conn = nil
	}

	// Clear client tool map
	client.ToolMap = make(map[string]schemas.ChatTool)

	delete(m.clientMap, id)
	return nil
}

func (m *MCPManager) EditClient(id string, updatedConfig schemas.MCPClientConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	client, ok := m.clientMap[id]
	if !ok {
		return fmt.Errorf("client %s not found", id)
	}

	// Update the client's execution config with new tool filters
	config := client.ExecutionConfig
	config.Name = updatedConfig.Name
	config.Headers = updatedConfig.Headers
	config.ToolsToExecute = updatedConfig.ToolsToExecute

	// Store the updated config
	client.ExecutionConfig = config

	if client.Conn == nil {
		return nil // Client is not connected, so no tools to update
	}

	// Clear current tool map
	client.ToolMap = make(map[string]schemas.ChatTool)

	// Temporarily unlock for the network call
	m.mu.Unlock()

	// Retrieve tools with updated configuration
	tools, err := m.retrieveExternalTools(m.ctx, client.Conn, config)

	// Re-lock to update the tool map
	m.mu.Lock()

	// Verify client still exists
	if _, ok := m.clientMap[id]; !ok {
		return fmt.Errorf("client %s was removed during tool update", id)
	}

	if err != nil {
		return fmt.Errorf("failed to retrieve external tools: %w", err)
	}

	// Store discovered tools
	maps.Copy(client.ToolMap, tools)

	return nil
}

// ============================================================================
// TOOL REGISTRATION AND DISCOVERY
// ============================================================================

// getAvailableTools returns all tools from connected MCP clients.
// Applies client filtering if specified in the context.
func (m *MCPManager) getAvailableTools(ctx context.Context) []schemas.ChatTool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var includeClients []string

	// Extract client filtering from request context
	if existingIncludeClients, ok := ctx.Value(MCPContextKeyIncludeClients).([]string); ok && existingIncludeClients != nil {
		includeClients = existingIncludeClients
	}

	tools := make([]schemas.ChatTool, 0)
	for id, client := range m.clientMap {
		// Apply client filtering logic
		if !m.shouldIncludeClient(id, includeClients) {
			m.logger.Debug(fmt.Sprintf("%s Skipping MCP client %s: not in include clients list", MCPLogPrefix, id))
			continue
		}

		m.logger.Debug(fmt.Sprintf("Checking tools for MCP client %s with tools to execute: %v", id, client.ExecutionConfig.ToolsToExecute))

		// Add all tools from this client
		for toolName, tool := range client.ToolMap {
			// Check if tool should be skipped based on client configuration
			if m.shouldSkipToolForConfig(toolName, client.ExecutionConfig) {
				m.logger.Debug(fmt.Sprintf("%s Skipping MCP tool %s: not in tools to execute list", MCPLogPrefix, toolName))
				continue
			}

			// Check if tool should be skipped based on request context
			if m.shouldSkipToolForRequest(id, toolName, ctx) {
				m.logger.Debug(fmt.Sprintf("%s Skipping MCP tool %s: not in include tools list", MCPLogPrefix, toolName))
				continue
			}

			tools = append(tools, tool)
		}
	}
	return tools
}

// registerTool registers a typed tool handler with the local MCP server.
// This is a convenience function that handles the conversion between typed Go
// handlers and the MCP protocol.
//
// Type Parameters:
//   - T: The expected argument type for the tool (must be JSON-deserializable)
//
// Parameters:
//   - name: Unique tool name
//   - description: Human-readable tool description
//   - handler: Typed function that handles tool execution
//   - toolSchema: Bifrost tool schema for function calling
//
// Returns:
//   - error: Any registration error
//
// Example:
//
//	type EchoArgs struct {
//	    Message string `json:"message"`
//	}
//
//	err := bifrost.RegisterMCPTool("echo", "Echo a message",
//	    func(args EchoArgs) (string, error) {
//	        return args.Message, nil
//	    }, toolSchema)
func (m *MCPManager) registerTool(name, description string, handler MCPToolHandler[any], toolSchema schemas.ChatTool) error {
	// Ensure local server is set up
	if err := m.setupLocalHost(); err != nil {
		return fmt.Errorf("failed to setup local host: %w", err)
	}

	// Verify internal client exists
	if _, ok := m.clientMap[BifrostMCPClientKey]; !ok {
		return fmt.Errorf("bifrost client not found")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if tool name already exists to prevent silent overwrites
	if _, exists := m.clientMap[BifrostMCPClientKey].ToolMap[name]; exists {
		return fmt.Errorf("tool '%s' is already registered", name)
	}

	m.logger.Info(fmt.Sprintf("%s Registering typed tool: %s", MCPLogPrefix, name))

	// Create MCP handler wrapper that converts between typed and MCP interfaces
	mcpHandler := func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Extract arguments from the request using the request's methods
		args := request.GetArguments()
		result, err := handler(args)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Error: %s", err.Error())), nil
		}
		return mcp.NewToolResultText(result), nil
	}

	// Register the tool with the local MCP server using AddTool
	if m.server != nil {
		tool := mcp.NewTool(name, mcp.WithDescription(description))
		m.server.AddTool(tool, mcpHandler)
	}

	// Store tool definition for Bifrost integration
	m.clientMap[BifrostMCPClientKey].ToolMap[name] = toolSchema

	return nil
}

// setupLocalHost initializes the local MCP server and client if not already running.
// This creates a STDIO-based server for local tool hosting and a corresponding client.
// This is called automatically when tools are registered or when the server is needed.
//
// Returns:
//   - error: Any setup error
func (m *MCPManager) setupLocalHost() error {
	// Check if server is already running
	if m.server != nil && m.serverRunning {
		return nil
	}

	// Create and configure local MCP server (STDIO-based)
	server, err := m.createLocalMCPServer()
	if err != nil {
		return fmt.Errorf("failed to create local MCP server: %w", err)
	}
	m.server = server

	// Create and configure local MCP client (STDIO-based)
	client, err := m.createLocalMCPClient()
	if err != nil {
		return fmt.Errorf("failed to create local MCP client: %w", err)
	}
	m.clientMap[BifrostMCPClientKey] = client

	// Start the server and initialize client connection
	return m.startLocalMCPServer()
}

// createLocalMCPServer creates a new local MCP server instance with STDIO transport.
// This server will host tools registered via RegisterTool function.
//
// Returns:
//   - *server.MCPServer: Configured MCP server instance
//   - error: Any creation error
func (m *MCPManager) createLocalMCPServer() (*server.MCPServer, error) {
	// Create MCP server
	mcpServer := server.NewMCPServer(
		"Bifrost-MCP-Server",
		"1.0.0",
		server.WithToolCapabilities(true),
	)

	return mcpServer, nil
}

// createLocalMCPClient creates a placeholder client entry for the local MCP server.
// The actual in-process client connection will be established in startLocalMCPServer.
//
// Returns:
//   - *MCPClient: Placeholder client for local server
//   - error: Any creation error
func (m *MCPManager) createLocalMCPClient() (*MCPClient, error) {
	// Don't create the actual client connection here - it will be created
	// after the server is ready using NewInProcessClient
	return &MCPClient{
		ExecutionConfig: schemas.MCPClientConfig{
			Name: BifrostMCPClientName,
		},
		ToolMap: make(map[string]schemas.ChatTool),
		ConnectionInfo: MCPClientConnectionInfo{
			Type: schemas.MCPConnectionTypeInProcess, // Accurate: in-process (in-memory) transport
		},
	}, nil
}

// startLocalMCPServer creates an in-process connection between the local server and client.
//
// Returns:
//   - error: Any startup error
func (m *MCPManager) startLocalMCPServer() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if server is already running
	if m.server != nil && m.serverRunning {
		return nil
	}

	if m.server == nil {
		return fmt.Errorf("server not initialized")
	}

	// Create in-process client directly connected to the server
	inProcessClient, err := client.NewInProcessClient(m.server)
	if err != nil {
		return fmt.Errorf("failed to create in-process MCP client: %w", err)
	}

	// Update the client connection
	clientEntry, ok := m.clientMap[BifrostMCPClientKey]
	if !ok {
		return fmt.Errorf("bifrost client not found")
	}
	clientEntry.Conn = inProcessClient

	// Initialize the in-process client
	ctx, cancel := context.WithTimeout(m.ctx, MCPClientConnectionEstablishTimeout)
	defer cancel()

	// Create proper initialize request with correct structure
	initRequest := mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			Capabilities:    mcp.ClientCapabilities{},
			ClientInfo: mcp.Implementation{
				Name:    BifrostMCPClientName,
				Version: BifrostMCPVersion,
			},
		},
	}

	_, err = inProcessClient.Initialize(ctx, initRequest)
	if err != nil {
		return fmt.Errorf("failed to initialize MCP client: %w", err)
	}

	// Mark server as running
	m.serverRunning = true

	return nil
}

// executeTool executes a tool call and returns the result as a tool message.
//
// Parameters:
//   - ctx: Execution context
//   - toolCall: The tool call to execute (from assistant message)
//
// Returns:
//   - schemas.ChatMessage: Tool message with execution result
//   - error: Any execution error
func (m *MCPManager) executeTool(ctx context.Context, toolCall schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, error) {
	if toolCall.Function.Name == nil {
		return nil, fmt.Errorf("tool call missing function name")
	}
	toolName := *toolCall.Function.Name

	// Parse tool arguments
	var arguments map[string]interface{}
	if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &arguments); err != nil {
		return nil, fmt.Errorf("failed to parse tool arguments for '%s': %v", toolName, err)
	}

	// Find which client has this tool
	client := m.findMCPClientForTool(toolName)
	if client == nil {
		return nil, fmt.Errorf("tool '%s' not found in any connected MCP client", toolName)
	}

	if client.Conn == nil {
		return nil, fmt.Errorf("client '%s' has no active connection", client.ExecutionConfig.Name)
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

	m.logger.Debug(fmt.Sprintf("%s Starting tool execution: %s via client: %s", MCPLogPrefix, toolName, client.ExecutionConfig.Name))

	toolResponse, callErr := client.Conn.CallTool(ctx, callRequest)
	if callErr != nil {
		m.logger.Error("%s Tool execution failed for %s via client %s: %v", MCPLogPrefix, toolName, client.ExecutionConfig.Name, callErr)
		return nil, fmt.Errorf("MCP tool call failed: %v", callErr)
	}

	m.logger.Debug(fmt.Sprintf("%s Tool execution completed: %s", MCPLogPrefix, toolName))

	// Extract text from MCP response
	responseText := m.extractTextFromMCPResponse(toolResponse, toolName)

	// Create tool response message
	return m.createToolResponseMessage(toolCall, responseText), nil
}

// ============================================================================
// EXTERNAL MCP CONNECTION MANAGEMENT
// ============================================================================

// connectToMCPClient establishes a connection to an external MCP server and
// registers its available tools with the manager.
func (m *MCPManager) connectToMCPClient(config schemas.MCPClientConfig) error {
	// First lock: Initialize or validate client entry
	m.mu.Lock()

	// Initialize or validate client entry
	if existingClient, exists := m.clientMap[config.ID]; exists {
		// Client entry exists from config, check for existing connection, if it does then close
		if existingClient.cancelFunc != nil {
			existingClient.cancelFunc()
			existingClient.cancelFunc = nil
		}
		if existingClient.Conn != nil {
			existingClient.Conn.Close()
		}
		// Update connection type for this connection attempt
		existingClient.ConnectionInfo.Type = config.ConnectionType
	}
	// Create new client entry with configuration
	m.clientMap[config.ID] = &MCPClient{
		ExecutionConfig: config,
		ToolMap:         make(map[string]schemas.ChatTool),
		ConnectionInfo: MCPClientConnectionInfo{
			Type: config.ConnectionType,
		},
	}
	m.mu.Unlock()

	// Heavy operations performed outside lock
	var externalClient *client.Client
	var connectionInfo MCPClientConnectionInfo
	var err error

	// Create appropriate transport based on connection type
	switch config.ConnectionType {
	case schemas.MCPConnectionTypeHTTP:
		externalClient, connectionInfo, err = m.createHTTPConnection(config)
	case schemas.MCPConnectionTypeSTDIO:
		externalClient, connectionInfo, err = m.createSTDIOConnection(config)
	case schemas.MCPConnectionTypeSSE:
		externalClient, connectionInfo, err = m.createSSEConnection(config)
	case schemas.MCPConnectionTypeInProcess:
		externalClient, connectionInfo, err = m.createInProcessConnection(config)
	default:
		return fmt.Errorf("unknown connection type: %s", config.ConnectionType)
	}

	if err != nil {
		return fmt.Errorf("failed to create connection: %w", err)
	}

	// Initialize the external client with timeout
	// For SSE connections, we need a long-lived context, for others we can use timeout
	var ctx context.Context
	var cancel context.CancelFunc

	if config.ConnectionType == schemas.MCPConnectionTypeSSE {
		// SSE connections need a long-lived context for the persistent stream
		ctx, cancel = context.WithCancel(m.ctx)
		// Don't defer cancel here - SSE needs the context to remain active
	} else {
		// Other connection types can use timeout context
		ctx, cancel = context.WithTimeout(m.ctx, MCPClientConnectionEstablishTimeout)
		defer cancel()
	}

	// Start the transport first (required for STDIO and SSE clients)
	if err := externalClient.Start(ctx); err != nil {
		if config.ConnectionType == schemas.MCPConnectionTypeSSE {
			cancel() // Cancel SSE context only on error
		}
		return fmt.Errorf("failed to start MCP client transport %s: %v", config.Name, err)
	}

	// Create proper initialize request for external client
	extInitRequest := mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			Capabilities:    mcp.ClientCapabilities{},
			ClientInfo: mcp.Implementation{
				Name:    fmt.Sprintf("Bifrost-%s", config.Name),
				Version: "1.0.0",
			},
		},
	}

	_, err = externalClient.Initialize(ctx, extInitRequest)
	if err != nil {
		if config.ConnectionType == schemas.MCPConnectionTypeSSE {
			cancel() // Cancel SSE context only on error
		}
		return fmt.Errorf("failed to initialize MCP client %s: %v", config.Name, err)
	}

	// Retrieve tools from the external server (this also requires network I/O)
	tools, err := m.retrieveExternalTools(ctx, externalClient, config)
	if err != nil {
		m.logger.Warn(fmt.Sprintf("%s Failed to retrieve tools from %s: %v", MCPLogPrefix, config.Name, err))
		// Continue with connection even if tool retrieval fails
		tools = make(map[string]schemas.ChatTool)
	}

	// Second lock: Update client with final connection details and tools
	m.mu.Lock()
	defer m.mu.Unlock()

	// Verify client still exists (could have been cleaned up during heavy operations)
	if client, exists := m.clientMap[config.ID]; exists {
		// Store the external client connection and details
		client.Conn = externalClient
		client.ConnectionInfo = connectionInfo

		// Store cancel function for SSE connections to enable proper cleanup
		if config.ConnectionType == schemas.MCPConnectionTypeSSE {
			client.cancelFunc = cancel
		}

		// Store discovered tools
		for toolName, tool := range tools {
			client.ToolMap[toolName] = tool
		}

		m.logger.Info(fmt.Sprintf("%s Connected to MCP client: %s", MCPLogPrefix, config.Name))
	} else {
		return fmt.Errorf("client %s was removed during connection setup", config.Name)
	}

	return nil
}

// retrieveExternalTools retrieves and filters tools from an external MCP server without holding locks.
func (m *MCPManager) retrieveExternalTools(ctx context.Context, client *client.Client, config schemas.MCPClientConfig) (map[string]schemas.ChatTool, error) {
	// Get available tools from external server
	listRequest := mcp.ListToolsRequest{
		PaginatedRequest: mcp.PaginatedRequest{
			Request: mcp.Request{
				Method: string(mcp.MethodToolsList),
			},
		},
	}

	toolsResponse, err := client.ListTools(ctx, listRequest)
	if err != nil {
		return nil, fmt.Errorf("failed to list tools: %v", err)
	}

	if toolsResponse == nil {
		return make(map[string]schemas.ChatTool), nil // No tools available
	}

	m.logger.Debug(fmt.Sprintf("%s Retrieved %d tools from %s", MCPLogPrefix, len(toolsResponse.Tools), config.Name))

	tools := make(map[string]schemas.ChatTool)

	// toolsResponse is already a ListToolsResult
	for _, mcpTool := range toolsResponse.Tools {
		// Convert MCP tool schema to Bifrost format
		bifrostTool := m.convertMCPToolToBifrostSchema(&mcpTool)
		tools[mcpTool.Name] = bifrostTool
	}

	return tools, nil
}

// shouldSkipToolForConfig checks if a tool should be skipped based on client configuration (without accessing clientMap).
func (m *MCPManager) shouldSkipToolForConfig(toolName string, config schemas.MCPClientConfig) bool {
	// If ToolsToExecute is specified (not nil), apply filtering
	if config.ToolsToExecute != nil {
		// Handle empty array [] - means no tools are allowed
		if len(config.ToolsToExecute) == 0 {
			return true // No tools allowed
		}

		// Handle wildcard "*" - if present, all tools are allowed
		if slices.Contains(config.ToolsToExecute, "*") {
			return false // All tools allowed
		}

		// Check if specific tool is in the allowed list
		for _, allowedTool := range config.ToolsToExecute {
			if allowedTool == toolName {
				return false // Tool is allowed
			}
		}
		return true // Tool not in allowed list
	}

	return true // Tool is skipped (nil is treated as [] - no tools)
}

// shouldSkipToolForRequest checks if a tool should be skipped based on the request context.
func (m *MCPManager) shouldSkipToolForRequest(clientID, toolName string, ctx context.Context) bool {
	includeTools := ctx.Value(MCPContextKeyIncludeTools)

	if includeTools != nil {
		// Try []string first (preferred type)
		if includeToolsList, ok := includeTools.([]string); ok {
			// Handle empty array [] - means no tools are included
			if len(includeToolsList) == 0 {
				return true // No tools allowed
			}

			// Handle wildcard "clientName/*" - if present, all tools are included for this client
			if slices.Contains(includeToolsList, fmt.Sprintf("%s/*", clientID)) {
				return false // All tools allowed
			}

			// Check if specific tool is in the list (format: clientName/toolName)
			fullToolName := fmt.Sprintf("%s/%s", clientID, toolName)
			if slices.Contains(includeToolsList, fullToolName) {
				return false // Tool is explicitly allowed
			}

			// If includeTools is specified but this tool is not in it, skip it
			return true
		}
	}

	return false // Tool is allowed (default when no filtering specified)
}

// convertMCPToolToBifrostSchema converts an MCP tool definition to Bifrost format.
func (m *MCPManager) convertMCPToolToBifrostSchema(mcpTool *mcp.Tool) schemas.ChatTool {
	var properties *schemas.OrderedMap
	schemaType := mcpTool.InputSchema.Type

	// Ensure properties is always set (required by OpenAI API validation)
	if len(mcpTool.InputSchema.Properties) > 0 {
		orderedProps := make(schemas.OrderedMap, len(mcpTool.InputSchema.Properties))
		maps.Copy(orderedProps, mcpTool.InputSchema.Properties)
		properties = &orderedProps
	} else {
		// OpenAI function calling API always expects object schemas with properties field present
		emptyProps := make(schemas.OrderedMap)
		properties = &emptyProps
	}

	// Default to "object" type if empty (OpenAI function calling always uses object schemas)
	if schemaType == "" {
		schemaType = "object"
	}

	return schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        mcpTool.Name,
			Description: Ptr(mcpTool.Description),
			Parameters: &schemas.ToolFunctionParameters{
				Type:       schemaType,
				Properties: properties,
				Required:   mcpTool.InputSchema.Required,
			},
		},
	}
}

// extractTextFromMCPResponse extracts text content from an MCP tool response.
func (m *MCPManager) extractTextFromMCPResponse(toolResponse *mcp.CallToolResult, toolName string) string {
	if toolResponse == nil {
		return fmt.Sprintf("MCP tool '%s' executed successfully", toolName)
	}

	var result strings.Builder
	for _, contentBlock := range toolResponse.Content {
		// Handle typed content
		switch content := contentBlock.(type) {
		case mcp.TextContent:
			result.WriteString(content.Text)
		case mcp.ImageContent:
			result.WriteString(fmt.Sprintf("[Image Response: %s, MIME: %s]\n", content.Data, content.MIMEType))
		case mcp.AudioContent:
			result.WriteString(fmt.Sprintf("[Audio Response: %s, MIME: %s]\n", content.Data, content.MIMEType))
		case mcp.EmbeddedResource:
			result.WriteString(fmt.Sprintf("[Embedded Resource Response: %s]\n", content.Type))
		default:
			// Fallback: try to extract from map structure
			if jsonBytes, err := json.Marshal(contentBlock); err == nil {
				var contentMap map[string]interface{}
				if json.Unmarshal(jsonBytes, &contentMap) == nil {
					if text, ok := contentMap["text"].(string); ok {
						result.WriteString(fmt.Sprintf("[Text Response: %s]\n", text))
						continue
					}
				}
				// Final fallback: serialize as JSON
				result.WriteString(string(jsonBytes))
			}
		}
	}

	if result.Len() > 0 {
		return strings.TrimSpace(result.String())
	}
	return fmt.Sprintf("MCP tool '%s' executed successfully", toolName)
}

// createToolResponseMessage creates a tool response message with the execution result.
func (m *MCPManager) createToolResponseMessage(toolCall schemas.ChatAssistantMessageToolCall, responseText string) *schemas.ChatMessage {
	return &schemas.ChatMessage{
		Role: schemas.ChatMessageRoleTool,
		Content: &schemas.ChatMessageContent{
			ContentStr: &responseText,
		},
		ChatToolMessage: &schemas.ChatToolMessage{
			ToolCallID: toolCall.ID,
		},
	}
}

func (m *MCPManager) addMCPToolsToBifrostRequest(ctx context.Context, req *schemas.BifrostRequest) *schemas.BifrostRequest {
	mcpTools := m.getAvailableTools(ctx)
	if len(mcpTools) > 0 {
		m.logger.Debug(fmt.Sprintf("%s Adding %d MCP tools to request", MCPLogPrefix, len(mcpTools)))
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
			for _, mcpTool := range mcpTools {
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
			for _, mcpTool := range mcpTools {
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

func validateMCPClientConfig(config *schemas.MCPClientConfig) error {
	if strings.TrimSpace(config.ID) == "" {
		return fmt.Errorf("id is required for MCP client config")
	}

	if strings.TrimSpace(config.Name) == "" {
		return fmt.Errorf("name is required for MCP client config")
	}

	if config.ConnectionType == "" {
		return fmt.Errorf("connection type is required for MCP client config")
	}

	switch config.ConnectionType {
	case schemas.MCPConnectionTypeHTTP:
		if config.ConnectionString == nil {
			return fmt.Errorf("ConnectionString is required for HTTP connection type in client '%s'", config.Name)
		}
	case schemas.MCPConnectionTypeSSE:
		if config.ConnectionString == nil {
			return fmt.Errorf("ConnectionString is required for SSE connection type in client '%s'", config.Name)
		}
	case schemas.MCPConnectionTypeSTDIO:
		if config.StdioConfig == nil {
			return fmt.Errorf("StdioConfig is required for STDIO connection type in client '%s'", config.Name)
		}
	case schemas.MCPConnectionTypeInProcess:
		// InProcess requires a server instance to be provided programmatically
		// This cannot be validated from JSON config - the server must be set when using the Go package
		if config.InProcessServer == nil {
			return fmt.Errorf("InProcessServer is required for InProcess connection type in client '%s' (Go package only)", config.Name)
		}
	default:
		return fmt.Errorf("unknown connection type '%s' in client '%s'", config.ConnectionType, config.Name)
	}

	return nil
}

// ============================================================================
// HELPER METHODS
// ============================================================================

// findMCPClientForTool safely finds a client that has the specified tool.
func (m *MCPManager) findMCPClientForTool(toolName string) *MCPClient {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, client := range m.clientMap {
		if _, exists := client.ToolMap[toolName]; exists {
			return client
		}
	}
	return nil
}

// shouldIncludeClient determines if a client should be included based on filtering rules.
func (m *MCPManager) shouldIncludeClient(clientID string, includeClients []string) bool {
	// If includeClients is specified (not nil), apply whitelist filtering
	if includeClients != nil {
		// Handle empty array [] - means no clients are included
		if len(includeClients) == 0 {
			return false // No clients allowed
		}

		// Handle wildcard "*" - if present, all clients are included
		if slices.Contains(includeClients, "*") {
			return true // All clients allowed
		}

		// Check if specific client is in the list
		return slices.Contains(includeClients, clientID)
	}

	// Default: include all clients when no filtering specified (nil case)
	return true
}

// createHTTPConnection creates an HTTP-based MCP client connection without holding locks.
func (m *MCPManager) createHTTPConnection(config schemas.MCPClientConfig) (*client.Client, MCPClientConnectionInfo, error) {
	if config.ConnectionString == nil {
		return nil, MCPClientConnectionInfo{}, fmt.Errorf("HTTP connection string is required")
	}

	// Prepare connection info
	connectionInfo := MCPClientConnectionInfo{
		Type:          config.ConnectionType,
		ConnectionURL: config.ConnectionString,
	}

	// Create StreamableHTTP transport
	httpTransport, err := transport.NewStreamableHTTP(*config.ConnectionString, transport.WithHTTPHeaders(config.Headers))
	if err != nil {
		return nil, MCPClientConnectionInfo{}, fmt.Errorf("failed to create HTTP transport: %w", err)
	}

	client := client.NewClient(httpTransport)

	return client, connectionInfo, nil
}

// createSTDIOConnection creates a STDIO-based MCP client connection without holding locks.
func (m *MCPManager) createSTDIOConnection(config schemas.MCPClientConfig) (*client.Client, MCPClientConnectionInfo, error) {
	if config.StdioConfig == nil {
		return nil, MCPClientConnectionInfo{}, fmt.Errorf("stdio config is required")
	}

	// Prepare STDIO command info for display
	cmdString := fmt.Sprintf("%s %s", config.StdioConfig.Command, strings.Join(config.StdioConfig.Args, " "))

	// Check if environment variables are set
	for _, env := range config.StdioConfig.Envs {
		if os.Getenv(env) == "" {
			return nil, MCPClientConnectionInfo{}, fmt.Errorf("environment variable %s is not set for MCP client %s", env, config.Name)
		}
	}

	// Create STDIO transport
	stdioTransport := transport.NewStdio(
		config.StdioConfig.Command,
		config.StdioConfig.Envs,
		config.StdioConfig.Args...,
	)

	// Prepare connection info
	connectionInfo := MCPClientConnectionInfo{
		Type:               config.ConnectionType,
		StdioCommandString: &cmdString,
	}

	client := client.NewClient(stdioTransport)

	// Return nil for cmd since mark3labs/mcp-go manages the process internally
	return client, connectionInfo, nil
}

// createSSEConnection creates a SSE-based MCP client connection without holding locks.
func (m *MCPManager) createSSEConnection(config schemas.MCPClientConfig) (*client.Client, MCPClientConnectionInfo, error) {
	if config.ConnectionString == nil {
		return nil, MCPClientConnectionInfo{}, fmt.Errorf("SSE connection string is required")
	}

	// Prepare connection info
	connectionInfo := MCPClientConnectionInfo{
		Type:          config.ConnectionType,
		ConnectionURL: config.ConnectionString, // Reuse HTTPConnectionURL field for SSE URL display
	}

	// Create SSE transport
	sseTransport, err := transport.NewSSE(*config.ConnectionString, transport.WithHeaders(config.Headers))
	if err != nil {
		return nil, MCPClientConnectionInfo{}, fmt.Errorf("failed to create SSE transport: %w", err)
	}

	client := client.NewClient(sseTransport)

	return client, connectionInfo, nil
}

// createInProcessConnection creates an in-process MCP client connection without holding locks.
// This allows direct connection to an MCP server running in the same process, providing
// the lowest latency and highest performance for tool execution.
func (m *MCPManager) createInProcessConnection(config schemas.MCPClientConfig) (*client.Client, MCPClientConnectionInfo, error) {
	if config.InProcessServer == nil {
		return nil, MCPClientConnectionInfo{}, fmt.Errorf("InProcess connection requires a server instance")
	}
	// Create in-process client directly connected to the provided server
	inProcessClient, err := client.NewInProcessClient(config.InProcessServer)
	if err != nil {
		return nil, MCPClientConnectionInfo{}, fmt.Errorf("failed to create in-process client: %w", err)
	}

	// Prepare connection info
	connectionInfo := MCPClientConnectionInfo{
		Type: config.ConnectionType,
	}

	return inProcessClient, connectionInfo, nil
}

// cleanup performs cleanup of all MCP resources including clients and local server.
// This function safely disconnects all MCP clients (HTTP, STDIO, and SSE) and
// cleans up the local MCP server. It handles proper cancellation of SSE contexts
// and closes all transport connections.
//
// Returns:
//   - error: Always returns nil, but maintains error interface for consistency
func (m *MCPManager) cleanup() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Disconnect all external MCP clients
	for id := range m.clientMap {
		if err := m.removeClientUnsafe(id); err != nil {
			m.logger.Error("%s Failed to remove MCP client %s: %v", MCPLogPrefix, id, err)
		}
	}

	// Clear the client map
	m.clientMap = make(map[string]*MCPClient)

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
