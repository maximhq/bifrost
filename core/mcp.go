package bifrost

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	mcp_golang "github.com/metoro-io/mcp-golang"
	httpTransport "github.com/metoro-io/mcp-golang/transport/http"
	"github.com/metoro-io/mcp-golang/transport/stdio"
)

// ============================================================================
// CONSTANTS
// ============================================================================

const (
	// MCP defaults and identifiers
	DefaultMCPServerPort                = 8181               // Default port for local MCP server
	BifrostMCPVersion                   = "1.0.0"            // Version identifier for Bifrost
	BifrostMCPClientName                = "BifrostClient"    // Name for internal Bifrost MCP client
	BifrostMCPClientKey                 = "bifrost-internal" // Key for internal Bifrost client in clientMap
	MCPLogPrefix                        = "[Bifrost MCP]"    // Consistent logging prefix
	MCPClientConnectionEstablishTimeout = 30 * time.Second   // Timeout for MCP client connection establishment

	// Context keys for client filtering in requests
	MCPContextKeyIncludeClients = "mcp_include_clients" // Context key for whitelist client filtering
	MCPContextKeyExcludeClients = "mcp_exclude_clients" // Context key for blacklist client filtering
)

// ============================================================================
// TYPE DEFINITIONS
// ============================================================================

// MCPManager manages MCP integration for Bifrost core.
// It provides a bridge between Bifrost and various MCP servers, supporting
// both local tool hosting and external MCP server connections.
type MCPManager struct {
	server        *mcp_golang.Server    // Local MCP server instance for hosting tools
	clientMap     map[string]*MCPClient // Map of MCP client names to their configurations
	serverPort    *int                  // Port for local MCP server (only required for local server)
	mu            sync.RWMutex          // Read-write mutex for thread-safe operations
	serverRunning bool                  // Track whether local MCP server is running
	logger        schemas.Logger        // Logger instance for structured logging
}

// MCPClient represents a connected MCP client with its configuration and tools.
type MCPClient struct {
	Name            string                  // Unique name for this client
	Conn            *mcp_golang.Client      // Active MCP client connection
	ExecutionConfig schemas.MCPClientConfig // Tool filtering settings
	ToolMap         map[string]schemas.Tool // Available tools mapped by name
	StdioCommand    *exec.Cmd               `json:"-"`               // STDIO process command (not serialized)
	ConnectionInfo  MCPClientConnectionInfo `json:"connection_info"` // Connection metadata for management
}

// MCPClientConnectionInfo stores metadata about how a client is connected.
type MCPClientConnectionInfo struct {
	Type               schemas.MCPConnectionType `json:"type"`                           // Connection type (HTTP or STDIO)
	HTTPConnectionURL  *string                   `json:"http_connection_url,omitempty"`  // HTTP endpoint URL (for HTTP connections)
	StdioCommandString *string                   `json:"stdio_command_string,omitempty"` // Command string for display (for STDIO connections)
	ProcessID          *int                      `json:"process_id,omitempty"`           // Process ID of STDIO command
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
func newMCPManager(config schemas.MCPConfig, logger schemas.Logger) (*MCPManager, error) {
	// Use provided logger or create default logger with info level
	if logger == nil {
		logger = NewDefaultLogger(schemas.LogLevelInfo)
	}

	manager := &MCPManager{
		serverPort: config.ServerPort,
		clientMap:  make(map[string]*MCPClient),
		logger:     logger,
	}

	// Process client configs: create client map entries and establish connections
	for _, clientConfig := range config.ClientConfigs {
		// Validate client configuration
		if err := validateMCPClientConfig(&clientConfig); err != nil {
			return nil, fmt.Errorf("invalid MCP client configuration: %w", err)
		}

		// Create client map entry
		manager.clientMap[clientConfig.Name] = &MCPClient{
			Name:            clientConfig.Name,
			ExecutionConfig: clientConfig,
			ToolMap:         make(map[string]schemas.Tool),
		}

		// Attempt to establish connection
		err := manager.connectToMCPClient(clientConfig)
		if err != nil {
			logger.Warn(fmt.Sprintf("%s Failed to connect to MCP client %s: %v", MCPLogPrefix, clientConfig.Name, err))
			// Continue with other connections even if one fails
		}
	}

	manager.logger.Info(MCPLogPrefix + " MCP Manager initialized")

	return manager, nil
}

// ============================================================================
// TOOL REGISTRATION AND DISCOVERY
// ============================================================================

// getAvailableTools returns all tools from connected MCP clients.
// Applies client filtering if specified in the context.
func (m *MCPManager) getAvailableTools(ctx context.Context) []schemas.Tool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var includeClients []string
	var excludeClients []string

	// Extract client filtering from request context
	if existingIncludeClients, ok := ctx.Value(MCPContextKeyIncludeClients).([]string); ok && existingIncludeClients != nil {
		includeClients = existingIncludeClients
	}
	if existingExcludeClients, ok := ctx.Value(MCPContextKeyExcludeClients).([]string); ok && existingExcludeClients != nil {
		excludeClients = existingExcludeClients
	}

	tools := make([]schemas.Tool, 0)
	for clientName, client := range m.clientMap {
		// Apply client filtering logic
		if !m.shouldIncludeClient(clientName, includeClients, excludeClients) {
			continue
		}

		// Add all tools from this client
		for _, tool := range client.ToolMap {
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
func (m *MCPManager) registerTool(name, description string, handler MCPToolHandler[any], toolSchema schemas.Tool) error {
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
	mcpHandler := func(args any) (*mcp_golang.ToolResponse, error) {
		result, err := handler(args)
		if err != nil {
			return mcp_golang.NewToolResponse(mcp_golang.NewTextContent(fmt.Sprintf("Error: %s", err.Error()))), nil
		}
		return mcp_golang.NewToolResponse(mcp_golang.NewTextContent(result)), nil
	}

	// Register with the underlying mcp-golang server
	err := m.server.RegisterTool(name, description, mcpHandler)
	if err != nil {
		return fmt.Errorf("failed to register tool with MCP server: %w", err)
	}

	// Store tool definition for Bifrost integration
	m.clientMap[BifrostMCPClientKey].ToolMap[name] = toolSchema

	return nil
}

// setupLocalHost initializes the local MCP server and client if not already running.
// This is called automatically when tools are registered or when the server is needed.
//
// Returns:
//   - error: Any setup error
func (m *MCPManager) setupLocalHost() error {
	// Check if server is already running
	if m.server != nil && m.serverRunning {
		return nil
	}

	// Create and configure local MCP server
	server, err := m.createLocalMCPServer()
	if err != nil {
		return fmt.Errorf("failed to create local MCP server: %w", err)
	}
	m.server = server

	// Create and configure local MCP client
	client, err := m.createLocalMCPClient()
	if err != nil {
		return fmt.Errorf("failed to create local MCP client: %w", err)
	}
	m.clientMap[BifrostMCPClientKey] = client

	// Start the server and initialize client connection
	return m.startLocalMCPServer()
}

// createLocalMCPServer creates a new local MCP server instance with HTTP transport.
// This server will host tools registered via RegisterTool function.
//
// Returns:
//   - *mcp_golang.Server: Configured MCP server instance
//   - error: Any creation error
func (m *MCPManager) createLocalMCPServer() (*mcp_golang.Server, error) {
	// Use configured port or default
	serverPort := m.serverPort
	if serverPort == nil {
		serverPort = Ptr(DefaultMCPServerPort)
	}

	// Create HTTP transport for the MCP server
	serverTransport := httpTransport.NewHTTPTransport("/mcp")
	serverTransport.WithAddr(fmt.Sprintf(":%d", *serverPort))
	server := mcp_golang.NewServer(serverTransport)

	return server, nil
}

// createLocalMCPClient creates a client that connects to the local MCP server.
// This client is used internally by Bifrost to access locally hosted tools.
//
// Returns:
//   - *MCPClient: Configured client for local server
//   - error: Any creation error
func (m *MCPManager) createLocalMCPClient() (*MCPClient, error) {
	// Use configured port or default
	serverPort := m.serverPort
	if serverPort == nil {
		serverPort = Ptr(DefaultMCPServerPort)
	}

	// Create HTTP client transport pointing to local server
	clientTransport := httpTransport.NewHTTPClientTransport("/mcp")
	clientTransport.WithBaseURL(fmt.Sprintf("http://localhost:%d", *serverPort))
	client := mcp_golang.NewClientWithInfo(clientTransport, mcp_golang.ClientInfo{
		Name:    BifrostMCPClientName,
		Version: BifrostMCPVersion,
	})

	return &MCPClient{
		Name: BifrostMCPClientName,
		Conn: client,
		ExecutionConfig: schemas.MCPClientConfig{
			Name: BifrostMCPClientName,
		},
		ToolMap: make(map[string]schemas.Tool),
	}, nil
}

// startLocalMCPServer starts the HTTP server and initializes the client connection.
// The server runs in a separate goroutine to avoid blocking.
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

	// Start the HTTP server in background goroutine
	go func() {
		if err := m.server.Serve(); err != nil && err != http.ErrServerClosed {
			m.logger.Error(fmt.Errorf("%s MCP server error: %w", MCPLogPrefix, err))
			m.mu.Lock()
			m.serverRunning = false
			m.mu.Unlock()
		}
	}()

	// Mark server as running
	m.serverRunning = true

	// Initialize the client connection to the server
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, ok := m.clientMap[BifrostMCPClientKey]; !ok {
		return fmt.Errorf("bifrost client not found")
	}

	_, err := m.clientMap[BifrostMCPClientKey].Conn.Initialize(ctx)
	if err != nil {
		m.serverRunning = false
		return fmt.Errorf("failed to initialize MCP client: %v", err)
	}

	return nil
}

// executeTool executes a tool call and returns the result as a tool message.
//
// Parameters:
//   - ctx: Execution context
//   - toolCall: The tool call to execute (from assistant message)
//
// Returns:
//   - schemas.BifrostMessage: Tool message with execution result
//   - error: Any execution error
func (m *MCPManager) executeTool(ctx context.Context, toolCall schemas.ToolCall) (*schemas.BifrostMessage, error) {
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
		return nil, fmt.Errorf("client '%s' has no active connection", client.Name)
	}

	// Call the tool via MCP client -> MCP server
	toolResponse, callErr := client.Conn.CallTool(ctx, toolName, arguments)
	if callErr != nil {
		return nil, fmt.Errorf("MCP tool call failed: %v", callErr)
	}

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
	if existingClient, exists := m.clientMap[config.Name]; exists {
		// Client entry exists from config, check for existing connection
		if existingClient.Conn != nil {
			m.mu.Unlock()
			return fmt.Errorf("client %s already has an active connection", config.Name)
		}
		// Update connection type for this connection attempt
		existingClient.ConnectionInfo.Type = config.ConnectionType
	} else {
		// Create new client entry with configuration
		m.clientMap[config.Name] = &MCPClient{
			Name:            config.Name,
			ExecutionConfig: config,
			ToolMap:         make(map[string]schemas.Tool),
			ConnectionInfo: MCPClientConnectionInfo{
				Type: config.ConnectionType,
			},
		}
	}
	m.mu.Unlock()

	// Heavy operations performed outside lock
	var externalClient *mcp_golang.Client
	var connectionInfo MCPClientConnectionInfo
	var stdioCommand *exec.Cmd
	var err error

	// Create appropriate transport based on connection type
	switch config.ConnectionType {
	case schemas.MCPConnectionTypeHTTP:
		externalClient, connectionInfo, err = m.createHTTPConnection(config)
	case schemas.MCPConnectionTypeSTDIO:
		externalClient, connectionInfo, stdioCommand, err = m.createSTDIOConnection(config)
	default:
		return fmt.Errorf("unknown connection type: %s", config.ConnectionType)
	}

	if err != nil {
		return fmt.Errorf("failed to create connection: %w", err)
	}

	// Initialize the external client with timeout
	ctx, cancel := context.WithTimeout(context.Background(), MCPClientConnectionEstablishTimeout)
	defer cancel()

	_, err = externalClient.Initialize(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize MCP client %s: %v", config.Name, err)
	}

	// Retrieve tools from the external server (this also requires network I/O)
	tools, err := m.retrieveExternalTools(ctx, externalClient, config)
	if err != nil {
		m.logger.Warn(fmt.Sprintf("%s Failed to retrieve tools from %s: %v", MCPLogPrefix, config.Name, err))
		// Continue with connection even if tool retrieval fails
		tools = make(map[string]schemas.Tool)
	}

	// Second lock: Update client with final connection details and tools
	m.mu.Lock()
	defer m.mu.Unlock()

	// Verify client still exists (could have been cleaned up during heavy operations)
	if client, exists := m.clientMap[config.Name]; exists {
		// Store the external client connection and details
		client.Conn = externalClient
		client.ConnectionInfo = connectionInfo
		client.StdioCommand = stdioCommand

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

// ============================================================================
// CLEANUP
// ============================================================================

// cleanup performs cleanup of all MCP resources.
func (m *MCPManager) cleanup() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Clean up STDIO processes
	for _, client := range m.clientMap {
		if client.StdioCommand != nil && client.StdioCommand.Process != nil {
			m.logger.Info(fmt.Sprintf("%s Terminating STDIO process: %d", MCPLogPrefix, client.StdioCommand.Process.Pid))

			// Attempt to kill the process and log any errors
			if err := client.StdioCommand.Process.Kill(); err != nil {
				m.logger.Error(fmt.Errorf("%s Failed to kill STDIO process %d: %w", MCPLogPrefix, client.StdioCommand.Process.Pid, err))
			}

			// Wait for the process to exit with a timeout to prevent blocking cleanup
			done := make(chan error, 1)
			go func() {
				done <- client.StdioCommand.Wait()
			}()

			select {
			case err := <-done:
				// Process exited within timeout
				if err != nil && !isExpectedKillError(err) {
					m.logger.Warn(fmt.Sprintf("%s STDIO process %d exited with unexpected error: %v", MCPLogPrefix, client.StdioCommand.Process.Pid, err))
				}
				m.logger.Info(fmt.Sprintf("%s STDIO process %d terminated successfully", MCPLogPrefix, client.StdioCommand.Process.Pid))
			case <-time.After(10 * time.Second):
				// Process didn't exit within timeout - this is concerning but we can't wait forever
				m.logger.Warn(fmt.Sprintf("%s STDIO process %d did not terminate within 10 seconds after kill signal", MCPLogPrefix, client.StdioCommand.Process.Pid))
			}
		}
	}

	// Disconnect all clients
	for name := range m.clientMap {
		m.logger.Info(fmt.Sprintf("%s Disconnecting MCP client: %s", MCPLogPrefix, name))
	}
	m.clientMap = make(map[string]*MCPClient)

	// Clear server reference
	if m.server != nil {
		m.logger.Info(MCPLogPrefix + " Clearing MCP server reference")
		m.server = nil
		m.serverRunning = false
	}

	return nil
}

// ============================================================================
// HELPER METHODS
// ============================================================================

// isExpectedKillError checks if an error from Wait() is expected after killing a process.
func isExpectedKillError(err error) bool {
	if err == nil {
		return true
	}
	// Check if this is a typical "killed by signal" error which is expected after Process.Kill()
	errStr := err.Error()
	return strings.Contains(errStr, "signal:") || strings.Contains(errStr, "killed")
}

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
func (m *MCPManager) shouldIncludeClient(clientName string, includeClients, excludeClients []string) bool {
	// If includeClients is specified, only include those clients (whitelist mode)
	if len(includeClients) > 0 {
		for _, includeName := range includeClients {
			if clientName == includeName {
				return true
			}
		}
		return false // Not in include list
	}

	// If excludeClients is specified, exclude those clients (blacklist mode)
	if len(excludeClients) > 0 {
		for _, excludeName := range excludeClients {
			if clientName == excludeName {
				return false
			}
		}
	}

	// Default: include all clients
	return true
}

// createHTTPConnection creates an HTTP-based MCP client connection without holding locks.
func (m *MCPManager) createHTTPConnection(config schemas.MCPClientConfig) (*mcp_golang.Client, MCPClientConnectionInfo, error) {
	if config.HTTPConnectionString == nil {
		return nil, MCPClientConnectionInfo{}, fmt.Errorf("HTTP connection string is required")
	}

	// Prepare connection info
	connectionInfo := MCPClientConnectionInfo{
		Type:              config.ConnectionType,
		HTTPConnectionURL: config.HTTPConnectionString,
	}

	// Create HTTP transport
	clientTransport := httpTransport.NewHTTPClientTransport("/mcp")
	clientTransport.WithBaseURL(*config.HTTPConnectionString)

	client := mcp_golang.NewClientWithInfo(clientTransport, mcp_golang.ClientInfo{
		Name:    fmt.Sprintf("Bifrost-%s", config.Name),
		Version: "1.0.0",
	})

	return client, connectionInfo, nil
}

// createSTDIOConnection creates a STDIO-based MCP client connection without holding locks.
func (m *MCPManager) createSTDIOConnection(config schemas.MCPClientConfig) (*mcp_golang.Client, MCPClientConnectionInfo, *exec.Cmd, error) {
	if config.StdioConfig == nil {
		return nil, MCPClientConnectionInfo{}, nil, fmt.Errorf("stdio config is required")
	}

	// Prepare STDIO command info for display
	cmdString := fmt.Sprintf("%s %s", config.StdioConfig.Command, strings.Join(config.StdioConfig.Args, " "))

	// Create and start the STDIO command
	cmd := exec.Command(config.StdioConfig.Command, config.StdioConfig.Args...)

	// Get stdin/stdout pipes before starting
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, MCPClientConnectionInfo{}, nil, fmt.Errorf("failed to get stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close() // Clean up stdin if stdout fails
		return nil, MCPClientConnectionInfo{}, nil, fmt.Errorf("failed to get stdout pipe: %v", err)
	}

	// Check if environment variables are set
	for _, env := range config.StdioConfig.Envs {
		if os.Getenv(env) == "" {
			return nil, MCPClientConnectionInfo{}, nil, fmt.Errorf("environment variable %s is not set for MCP client %s", env, config.Name)
		}
	}

	// Start the command
	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		wd, _ := os.Getwd()
		formattedError := fmt.Errorf("failed to start MCP client '%s': command '%s %s' in directory '%s': %v",
			config.Name,
			config.StdioConfig.Command,
			strings.Join(config.StdioConfig.Args, " "),
			wd,
			err)

		return nil, MCPClientConnectionInfo{}, nil, formattedError
	}

	// Prepare connection info
	connectionInfo := MCPClientConnectionInfo{
		Type:               config.ConnectionType,
		StdioCommandString: &cmdString,
	}
	if cmd.Process != nil {
		pid := cmd.Process.Pid
		connectionInfo.ProcessID = &pid
	}

	// Create stdio transport with the command's stdout as our stdin, and stdin as our stdout
	stdioTransport := stdio.NewStdioServerTransportWithIO(stdout, stdin)

	client := mcp_golang.NewClientWithInfo(stdioTransport, mcp_golang.ClientInfo{
		Name:    fmt.Sprintf("Bifrost-%s", config.Name),
		Version: "1.0.0",
	})

	return client, connectionInfo, cmd, nil
}

// retrieveExternalTools retrieves and filters tools from an external MCP server without holding locks.
func (m *MCPManager) retrieveExternalTools(ctx context.Context, client *mcp_golang.Client, config schemas.MCPClientConfig) (map[string]schemas.Tool, error) {
	// Get available tools from external server
	toolsResponse, err := client.ListTools(ctx, Ptr(""))
	if err != nil {
		return nil, fmt.Errorf("failed to list tools: %v", err)
	}

	if toolsResponse == nil {
		return make(map[string]schemas.Tool), nil // No tools available
	}

	tools := make(map[string]schemas.Tool)

	// Convert and filter each tool
	for _, mcpTool := range toolsResponse.Tools {
		// Check if tool should be skipped based on configuration
		if m.shouldSkipToolForConfig(mcpTool.Name, config) {
			continue
		}

		// Convert MCP tool schema to Bifrost format
		bifrostTool := m.convertMCPToolToBifrostSchema(&mcpTool)
		tools[mcpTool.Name] = bifrostTool
	}

	return tools, nil
}

// shouldSkipToolForConfig checks if a tool should be skipped based on client configuration (without accessing clientMap).
func (m *MCPManager) shouldSkipToolForConfig(toolName string, config schemas.MCPClientConfig) bool {
	// If ToolsToExecute is specified, only execute tools in that list
	if len(config.ToolsToExecute) > 0 {
		for _, allowedTool := range config.ToolsToExecute {
			if allowedTool == toolName {
				return false // Tool is allowed
			}
		}
		return true // Tool not in allowed list
	}

	// Check if tool is in skip list
	for _, skipTool := range config.ToolsToSkip {
		if skipTool == toolName {
			return true // Tool should be skipped
		}
	}

	return false // Tool is allowed
}

// convertMCPToolToBifrostSchema converts an MCP tool definition to Bifrost format.
func (m *MCPManager) convertMCPToolToBifrostSchema(mcpTool *mcp_golang.ToolRetType) schemas.Tool {
	// Convert MCP tool schema to Bifrost tool schema
	properties := make(map[string]interface{})
	required := []string{}

	if mcpTool.InputSchema != nil {
		if schemaMap, ok := mcpTool.InputSchema.(map[string]interface{}); ok {
			if props, ok := schemaMap["properties"].(map[string]interface{}); ok {
				properties = props
			}
			if req, ok := schemaMap["required"].([]interface{}); ok {
				for _, r := range req {
					if reqStr, ok := r.(string); ok {
						required = append(required, reqStr)
					}
				}
			}
		}
	}

	// If no properties are defined, create an empty properties object
	// This is required by OpenAI's function calling schema
	if properties == nil {
		properties = make(map[string]interface{})
	}

	description := ""
	if mcpTool.Description != nil {
		description = *mcpTool.Description
	}

	return schemas.Tool{
		Type: "function",
		Function: schemas.Function{
			Name:        mcpTool.Name,
			Description: description,
			Parameters: schemas.FunctionParameters{
				Type:       "object",
				Properties: properties,
				Required:   required,
			},
		},
	}
}

// extractTextFromMCPResponse extracts text content from an MCP tool response.
func (m *MCPManager) extractTextFromMCPResponse(toolResponse *mcp_golang.ToolResponse, toolName string) string {
	if toolResponse == nil {
		return fmt.Sprintf("MCP tool '%s' executed successfully", toolName)
	}

	var responseTextBuilder strings.Builder
	if len(toolResponse.Content) > 0 {
		for _, contentBlock := range toolResponse.Content {
			if contentBlock.TextContent != nil && contentBlock.TextContent.Text != "" {
				responseTextBuilder.WriteString(contentBlock.TextContent.Text)
				responseTextBuilder.WriteString("\n")
			}
		}
	}

	if responseTextBuilder.Len() > 0 {
		return strings.TrimSpace(responseTextBuilder.String())
	}
	return fmt.Sprintf("MCP tool '%s' executed successfully", toolName)
}

// createToolResponseMessage creates a tool response message with the execution result.
func (m *MCPManager) createToolResponseMessage(toolCall schemas.ToolCall, responseText string) *schemas.BifrostMessage {
	return &schemas.BifrostMessage{
		Role: schemas.ModelChatMessageRoleTool,
		Content: schemas.MessageContent{
			ContentStr: &responseText,
		},
		ToolMessage: &schemas.ToolMessage{
			ToolCallID: toolCall.ID,
		},
	}
}

func (m *MCPManager) addMCPToolsToBifrostRequest(ctx context.Context, req *schemas.BifrostRequest) *schemas.BifrostRequest {
	mcpTools := m.getAvailableTools(ctx)
	if len(mcpTools) > 0 {
		// Initialize tools array if needed
		if req.Params == nil {
			req.Params = &schemas.ModelParameters{}
		}
		if req.Params.Tools == nil {
			req.Params.Tools = &[]schemas.Tool{}
		}
		tools := *req.Params.Tools

		// Create a map of existing tool names for O(1) lookup
		existingToolsMap := make(map[string]bool)
		for _, tool := range tools {
			existingToolsMap[tool.Function.Name] = true
		}

		// Add MCP tools that are not already present
		for _, mcpTool := range mcpTools {
			if !existingToolsMap[mcpTool.Function.Name] {
				tools = append(tools, mcpTool)
				// Update the map to prevent duplicates within MCP tools as well
				existingToolsMap[mcpTool.Function.Name] = true
			}
		}
		req.Params.Tools = &tools

	}
	return req
}

func validateMCPClientConfig(config *schemas.MCPClientConfig) error {
	if strings.TrimSpace(config.Name) == "" {
		return fmt.Errorf("name is required for MCP client config")
	}

	if config.ConnectionType == "" {
		return fmt.Errorf("connection type is required for MCP client config")
	}

	switch config.ConnectionType {
	case schemas.MCPConnectionTypeHTTP:
		if config.HTTPConnectionString == nil {
			return fmt.Errorf("HTTPConnectionString is required for HTTP connection type in client '%s'", config.Name)
		}
	case schemas.MCPConnectionTypeSTDIO:
		if config.StdioConfig == nil {
			return fmt.Errorf("StdioConfig is required for STDIO connection type in client '%s'", config.Name)
		}
	default:
		return fmt.Errorf("unknown connection type '%s' in client '%s'", config.ConnectionType, config.Name)
	}

	// Check for overlapping tools between ToolsToSkip and ToolsToExecute
	if len(config.ToolsToSkip) > 0 && len(config.ToolsToExecute) > 0 {
		skipMap := make(map[string]bool)
		for _, tool := range config.ToolsToSkip {
			skipMap[tool] = true
		}

		var overlapping []string
		for _, tool := range config.ToolsToExecute {
			if skipMap[tool] {
				overlapping = append(overlapping, tool)
			}
		}

		if len(overlapping) > 0 {
			return fmt.Errorf("tools cannot be both included and excluded in client '%s': %v", config.Name, overlapping)
		}
	}

	return nil
}
