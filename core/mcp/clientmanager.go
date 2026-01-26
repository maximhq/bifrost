package mcp

import (
	"context"
	"fmt"
	"maps"
	"os"
	"strings"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/maximhq/bifrost/core/schemas"
)

// GetClients returns all MCP clients managed by the manager.
//
// Returns:
//   - []*schemas.MCPClientState: List of all MCP clients
func (m *MCPManager) GetClients() []schemas.MCPClientState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	clients := make([]schemas.MCPClientState, 0, len(m.clientMap))
	for _, client := range m.clientMap {
		snapshot := *client
		if client.ToolMap != nil {
			snapshot.ToolMap = make(map[string]schemas.ChatTool, len(client.ToolMap))
			maps.Copy(snapshot.ToolMap, client.ToolMap)
		}
		clients = append(clients, snapshot)
	}

	return clients
}

// ReconnectClient attempts to reconnect an MCP client if it is disconnected.
// It validates that the client exists and then establishes a new connection using
// the client's existing configuration.
//
// Parameters:
//   - id: ID of the client to reconnect
//
// Returns:
//   - error: Any error that occurred during reconnection
func (m *MCPManager) ReconnectClient(id string) error {
	m.mu.Lock()
	client, ok := m.clientMap[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("client %s not found", id)
	}
	config := client.ExecutionConfig
	m.mu.Unlock()

	// connectToMCPClient handles locking internally
	err := m.connectToMCPClient(config)
	if err != nil {
		return fmt.Errorf("failed to connect to MCP client %s: %w", id, err)
	}

	return nil
}

// AddClient adds a new MCP client to the manager.
// It validates the client configuration and establishes a connection.
// If connection fails, the client entry is automatically cleaned up.
//
// Parameters:
//   - config: MCP client configuration
//
// Returns:
//   - error: Any error that occurred during client addition or connection
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
	m.clientMap[config.ID] = &schemas.MCPClientState{
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

// removeClientUnsafe removes an MCP client from the manager without acquiring locks.
// This is an internal method that should only be called when the caller already holds
// the appropriate lock. It handles cleanup for all transport types including cancellation
// of SSE contexts and closing of transport connections.
//
// Parameters:
//   - id: ID of the client to remove
//
// Returns:
//   - error: Any error that occurred during client removal
func (m *MCPManager) removeClientUnsafe(id string) error {
	client, ok := m.clientMap[id]
	if !ok {
		return fmt.Errorf("client %s not found", id)
	}

	logger.Info(fmt.Sprintf("%s Disconnecting MCP server '%s'", MCPLogPrefix, client.ExecutionConfig.Name))

	// Stop health monitoring for this client
	m.healthMonitorManager.StopMonitoring(id)

	// Cancel SSE context if present (required for proper SSE cleanup)
	if client.CancelFunc != nil {
		client.CancelFunc()
		client.CancelFunc = nil
	}

	// Close the client transport connection
	// This handles cleanup for all transport types (HTTP, STDIO, SSE)
	if client.Conn != nil {
		if err := client.Conn.Close(); err != nil {
			logger.Error("%s Failed to close MCP server '%s': %v", MCPLogPrefix, client.ExecutionConfig.Name, err)
		}
		client.Conn = nil
	}

	// Clear client tool map
	client.ToolMap = make(map[string]schemas.ChatTool)

	delete(m.clientMap, id)
	return nil
}

// EditClient updates an existing MCP client's configuration and refreshes its tool list.
// It updates the client's execution config with new settings and retrieves updated tools
// from the MCP server if the client is connected.
// This method does not refresh the client's tool list.
// To refresh the client's tool list, use the ReconnectClient method.
//
// Parameters:
//   - id: ID of the client to edit
//   - updatedConfig: Updated client configuration with new settings
//
// Returns:
//   - error: Any error that occurred during client update or tool retrieval
func (m *MCPManager) EditClient(id string, updatedConfig schemas.MCPClientConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	client, ok := m.clientMap[id]
	if !ok {
		return fmt.Errorf("client %s not found", id)
	}

	if err := validateMCPClientName(updatedConfig.Name); err != nil {
		return fmt.Errorf("invalid MCP client configuration: %w", err)
	}

	// Check if is_ping_available changed
	isPingAvailableChanged := client.ExecutionConfig.IsPingAvailable != updatedConfig.IsPingAvailable

	// Update the client's execution config with new tool filters
	config := client.ExecutionConfig
	config.Name = updatedConfig.Name
	config.IsCodeModeClient = updatedConfig.IsCodeModeClient
	config.Headers = updatedConfig.Headers
	config.ToolsToExecute = updatedConfig.ToolsToExecute
	config.ToolsToAutoExecute = updatedConfig.ToolsToAutoExecute
	config.IsPingAvailable = updatedConfig.IsPingAvailable

	// Store the updated config
	client.ExecutionConfig = config

	// If is_ping_available changed, update the health monitor
	if isPingAvailableChanged {
		// Stop and restart the health monitor with the new is_ping_available setting
		m.healthMonitorManager.StopMonitoring(id)
		monitor := NewClientHealthMonitor(m, id, DefaultHealthCheckInterval, config.IsPingAvailable)
		m.healthMonitorManager.StartMonitoring(monitor)
	}

	return nil
}

// RegisterTool registers a typed tool handler with the local MCP server.
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
func (m *MCPManager) RegisterTool(name, description string, toolFunction MCPToolFunction[any], toolSchema schemas.ChatTool) error {
	// Ensure local server is set up
	if err := m.setupLocalHost(); err != nil {
		return fmt.Errorf("failed to setup local host: %w", err)
	}

	// Validate tool name
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("tool name is required")
	}
	if strings.Contains(name, "-") {
		return fmt.Errorf("tool name cannot contain hyphens")
	}
	if strings.Contains(name, " ") {
		return fmt.Errorf("tool name cannot contain spaces")
	}
	if len(name) > 0 && name[0] >= '0' && name[0] <= '9' {
		return fmt.Errorf("tool name cannot start with a number")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Verify internal client exists
	internalClient, ok := m.clientMap[BifrostMCPClientKey]
	if !ok {
		return fmt.Errorf("bifrost client not found")
	}

	// Check if tool name already exists to prevent silent overwrites
	if _, exists := internalClient.ToolMap[name]; exists {
		return fmt.Errorf("tool '%s' is already registered", name)
	}

	logger.Info(fmt.Sprintf("%s Registering typed tool: %s", MCPLogPrefix, name))

	// Create MCP handler wrapper that converts between typed and MCP interfaces
	mcpHandler := func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Extract arguments from the request using the request's methods
		args := request.GetArguments()
		result, err := toolFunction(args)
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
	internalClient.ToolMap[name] = toolSchema

	return nil
}

// ============================================================================
// CONNECTION HELPER METHODS
// ============================================================================

// connectToMCPClient establishes a connection to an external MCP server and
// registers its available tools with the manager.
func (m *MCPManager) connectToMCPClient(config schemas.MCPClientConfig) error {
	// First lock: Initialize or validate client entry
	m.mu.Lock()

	// Initialize or validate client entry
	if existingClient, exists := m.clientMap[config.ID]; exists {
		// Client entry exists from config, check for existing connection, if it does then close
		if existingClient.CancelFunc != nil {
			existingClient.CancelFunc()
			existingClient.CancelFunc = nil
		}
		if existingClient.Conn != nil {
			existingClient.Conn.Close()
		}
		// Update connection type for this connection attempt
		existingClient.ConnectionInfo.Type = config.ConnectionType
	}
	// Create new client entry with configuration
	m.clientMap[config.ID] = &schemas.MCPClientState{
		ExecutionConfig: config,
		ToolMap:         make(map[string]schemas.ChatTool),
		ConnectionInfo: schemas.MCPClientConnectionInfo{
			Type: config.ConnectionType,
		},
	}
	m.mu.Unlock()

	// Heavy operations performed outside lock
	var externalClient *client.Client
	var connectionInfo schemas.MCPClientConnectionInfo
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
	tools, err := retrieveExternalTools(ctx, externalClient, config.Name)
	if err != nil {
		logger.Warn("%s Failed to retrieve tools from %s: %v", MCPLogPrefix, config.Name, err)
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
		client.State = schemas.MCPConnectionStateConnected

		// Store cancel function for SSE connections to enable proper cleanup
		if config.ConnectionType == schemas.MCPConnectionTypeSSE {
			client.CancelFunc = cancel
		}

		// Store discovered tools
		for toolName, tool := range tools {
			client.ToolMap[toolName] = tool
		}

		logger.Info(fmt.Sprintf("%s Connected to MCP server '%s'", MCPLogPrefix, config.Name))
	} else {
		// Clean up resources before returning error: client was removed during connection setup
		// Cancel SSE context if it was created
		if config.ConnectionType == schemas.MCPConnectionTypeSSE && cancel != nil {
			cancel()
		}
		// Close external client connection to prevent transport/goroutine leaks
		if externalClient != nil {
			if err := externalClient.Close(); err != nil {
				logger.Warn("%s Failed to close external client during cleanup: %v", MCPLogPrefix, err)
			}
		}
		return fmt.Errorf("client %s was removed during connection setup", config.Name)
	}

	// Register OnConnectionLost hook for SSE connections to detect idle timeouts
	if config.ConnectionType == schemas.MCPConnectionTypeSSE && externalClient != nil {
		externalClient.OnConnectionLost(func(err error) {
			logger.Warn("%s SSE connection lost for MCP server '%s': %v", MCPLogPrefix, config.Name, err)
			// Update state to disconnected
			m.mu.Lock()
			if client, exists := m.clientMap[config.ID]; exists {
				client.State = schemas.MCPConnectionStateDisconnected
			}
			m.mu.Unlock()
		})
	}

	// Start health monitoring for the client
	monitor := NewClientHealthMonitor(m, config.ID, DefaultHealthCheckInterval, config.IsPingAvailable)
	m.healthMonitorManager.StartMonitoring(monitor)

	return nil
}

// createHTTPConnection creates an HTTP-based MCP client connection without holding locks.
func (m *MCPManager) createHTTPConnection(config schemas.MCPClientConfig) (*client.Client, schemas.MCPClientConnectionInfo, error) {
	if config.ConnectionString == nil {
		return nil, schemas.MCPClientConnectionInfo{}, fmt.Errorf("HTTP connection string is required")
	}

	// Prepare connection info
	connectionInfo := schemas.MCPClientConnectionInfo{
		Type:          config.ConnectionType,
		ConnectionURL: config.ConnectionString.GetValuePtr(),
	}

	// Create StreamableHTTP transport
	httpTransport, err := transport.NewStreamableHTTP(config.ConnectionString.GetValue(), transport.WithHTTPHeaders(config.HttpHeaders()))
	if err != nil {
		return nil, schemas.MCPClientConnectionInfo{}, fmt.Errorf("failed to create HTTP transport: %w", err)
	}

	client := client.NewClient(httpTransport)

	return client, connectionInfo, nil
}

// createSTDIOConnection creates a STDIO-based MCP client connection without holding locks.
func (m *MCPManager) createSTDIOConnection(config schemas.MCPClientConfig) (*client.Client, schemas.MCPClientConnectionInfo, error) {
	if config.StdioConfig == nil {
		return nil, schemas.MCPClientConnectionInfo{}, fmt.Errorf("stdio config is required")
	}

	// Prepare STDIO command info for display
	cmdString := fmt.Sprintf("%s %s", config.StdioConfig.Command, strings.Join(config.StdioConfig.Args, " "))

	// Check if environment variables are set
	for _, env := range config.StdioConfig.Envs {
		if os.Getenv(env) == "" {
			return nil, schemas.MCPClientConnectionInfo{}, fmt.Errorf("environment variable %s is not set for MCP client %s", env, config.Name)
		}
	}

	// Create STDIO transport
	stdioTransport := transport.NewStdio(
		config.StdioConfig.Command,
		config.StdioConfig.Envs,
		config.StdioConfig.Args...,
	)

	// Prepare connection info
	connectionInfo := schemas.MCPClientConnectionInfo{
		Type:               config.ConnectionType,
		StdioCommandString: &cmdString,
	}

	client := client.NewClient(stdioTransport)

	// Return nil for cmd since mark3labs/mcp-go manages the process internally
	return client, connectionInfo, nil
}

// createSSEConnection creates a SSE-based MCP client connection without holding locks.
func (m *MCPManager) createSSEConnection(config schemas.MCPClientConfig) (*client.Client, schemas.MCPClientConnectionInfo, error) {
	if config.ConnectionString == nil {
		return nil, schemas.MCPClientConnectionInfo{}, fmt.Errorf("SSE connection string is required")
	}

	// Prepare connection info
	connectionInfo := schemas.MCPClientConnectionInfo{
		Type:          config.ConnectionType,
		ConnectionURL: config.ConnectionString.GetValuePtr(), // Reuse HTTPConnectionURL field for SSE URL display
	}

	// Create SSE transport
	sseTransport, err := transport.NewSSE(config.ConnectionString.GetValue(), transport.WithHeaders(config.HttpHeaders()))
	if err != nil {
		return nil, schemas.MCPClientConnectionInfo{}, fmt.Errorf("failed to create SSE transport: %w", err)
	}

	client := client.NewClient(sseTransport)

	return client, connectionInfo, nil
}

// createInProcessConnection creates an in-process MCP client connection without holding locks.
// This allows direct connection to an MCP server running in the same process, providing
// the lowest latency and highest performance for tool execution.
func (m *MCPManager) createInProcessConnection(config schemas.MCPClientConfig) (*client.Client, schemas.MCPClientConnectionInfo, error) {
	if config.InProcessServer == nil {
		return nil, schemas.MCPClientConnectionInfo{}, fmt.Errorf("InProcess connection requires a server instance")
	}

	// Create in-process client directly connected to the provided server
	inProcessClient, err := client.NewInProcessClient(config.InProcessServer)
	if err != nil {
		return nil, schemas.MCPClientConnectionInfo{}, fmt.Errorf("failed to create in-process client: %w", err)
	}

	// Prepare connection info
	connectionInfo := schemas.MCPClientConnectionInfo{
		Type: config.ConnectionType,
	}

	return inProcessClient, connectionInfo, nil
}

// ============================================================================
// LOCAL MCP SERVER AND CLIENT MANAGEMENT
// ============================================================================

// setupLocalHost initializes the local MCP server and client if not already running.
// This creates a STDIO-based server for local tool hosting and a corresponding client.
// This is called automatically when tools are registered or when the server is needed.
//
// Returns:
//   - error: Any setup error
func (m *MCPManager) setupLocalHost() error {
	// First check: fast path if already initialized
	m.mu.Lock()
	if m.server != nil && m.serverRunning {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	// Create server and client into local variables (outside lock to avoid
	// holding lock during object creation, even though it's lightweight)
	server, err := m.createLocalMCPServer()
	if err != nil {
		return fmt.Errorf("failed to create local MCP server: %w", err)
	}

	client, err := m.createLocalMCPClient()
	if err != nil {
		return fmt.Errorf("failed to create local MCP client: %w", err)
	}

	// Second check and assignment: hold lock for atomic check-and-set
	m.mu.Lock()
	// Double-check: another goroutine might have initialized while we were creating
	if m.server != nil && m.serverRunning {
		m.mu.Unlock()
		return nil
	}

	// Assign server and client atomically while holding the lock
	m.server = server
	m.clientMap[BifrostMCPClientKey] = client
	m.mu.Unlock()

	// Start the server and initialize client connection
	// (startLocalMCPServer already locks internally)
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
//   - *schemas.MCPClientState: Placeholder client for local server
//   - error: Any creation error
func (m *MCPManager) createLocalMCPClient() (*schemas.MCPClientState, error) {
	// Don't create the actual client connection here - it will be created
	// after the server is ready using NewInProcessClient
	return &schemas.MCPClientState{
		ExecutionConfig: schemas.MCPClientConfig{
			ID:             BifrostMCPClientKey,
			Name:           BifrostMCPClientName,
			ToolsToExecute: []string{"*"}, // Allow all tools for internal client
		},
		ToolMap: make(map[string]schemas.ChatTool),
		ConnectionInfo: schemas.MCPClientConnectionInfo{
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
