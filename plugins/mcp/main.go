package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	mcp_golang "github.com/metoro-io/mcp-golang"
	httpTransport "github.com/metoro-io/mcp-golang/transport/http"
	"github.com/metoro-io/mcp-golang/transport/stdio"
)

// ============================================================================
// CONSTANTS
// ============================================================================

const (
	// Plugin identification and defaults
	PluginName        = "MCPHost"              // Name identifier for the MCP plugin
	DefaultServerPort = ":8181"                // Default port for local MCP server
	BifrostVersion    = "1.0.0"                // Version identifier for Bifrost
	BifrostClientName = "BifrostClient"        // Name for internal Bifrost MCP client
	BifrostClientKey  = "bifrost-internal"     // Key for internal Bifrost client in clientMap
	LogPrefix         = "[Bifrost MCP Plugin]" // Consistent logging prefix

	// Context keys for client filtering in requests
	ContextKeyIncludeClients = "mcp_include_clients" // Context key for whitelist client filtering
	ContextKeyExcludeClients = "mcp_exclude_clients" // Context key for blacklist client filtering
)

// ConnectionType defines the communication protocol for MCP connections
type ConnectionType string

const (
	ConnectionTypeHTTP  ConnectionType = "http"  // HTTP-based MCP connection
	ConnectionTypeSTDIO ConnectionType = "stdio" // STDIO-based MCP connection
)

// ToolExecutionPolicy defines how tools should be executed
type ToolExecutionPolicy string

const (
	ToolExecutionPolicyRequireApproval ToolExecutionPolicy = "require_approval" // Tool requires user approval before execution
	ToolExecutionPolicyAutoExecute     ToolExecutionPolicy = "auto_execute"     // Tool executes automatically without approval
)

// ============================================================================
// TYPE DEFINITIONS
// ============================================================================

// MCPPlugin implements schemas.Plugin for hosting and managing MCP tools.
// It provides a bridge between Bifrost and various MCP servers, supporting
// both local tool hosting and external MCP server connections.
type MCPPlugin struct {
	server        *mcp_golang.Server       // Local MCP server instance for hosting tools
	clientMap     map[string]*PluginClient // Map of MCP client names to their configurations
	serverPort    string                   // Port for local MCP server
	mu            sync.RWMutex             // Read-write mutex for thread-safe operations
	agenticMode   bool                     // Enable agentic flow (tool results sent back to LLM)
	bifrostClient *bifrost.Bifrost         // Bifrost client instance for agentic mode
	serverRunning bool                     // Track whether local MCP server is running
	logger        schemas.Logger           // Logger instance for structured logging
}

// PluginClient represents a connected MCP client with its configuration and tools.
type PluginClient struct {
	Name            string                  // Unique name for this client
	Conn            *mcp_golang.Client      // Active MCP client connection
	ExecutionConfig ClientExecutionConfig   // Tool execution policies and settings
	ToolMap         map[string]schemas.Tool // Available tools mapped by name
	StdioCommand    *exec.Cmd               `json:"-"`               // STDIO process command (not serialized)
	ConnectionInfo  ClientConnectionInfo    `json:"connection_info"` // Connection metadata for management
}

// ClientExecutionConfig defines execution policies and tool filtering for a client.
type ClientExecutionConfig struct {
	Name           string                         // Client name
	DefaultPolicy  ToolExecutionPolicy            `json:"default_policy"`          // Default execution policy for all tools
	ToolPolicies   map[string]ToolExecutionPolicy `json:"tool_policies,omitempty"` // Per-tool execution policies
	ToolsToSkip    []string                       `json:"tools_to_skip,omitempty"` // Tools to exclude from this client
	ToolsToExecute []string                       // Tools to include from this client (if specified, only these are used)
}

// ClientConnectionInfo stores metadata about how a client is connected.
type ClientConnectionInfo struct {
	Type               ConnectionType `json:"type"`                           // Connection type (HTTP or STDIO)
	HTTPConnectionURL  *string        `json:"http_connection_url,omitempty"`  // HTTP endpoint URL (for HTTP connections)
	StdioCommandString *string        `json:"stdio_command_string,omitempty"` // Command string for display (for STDIO connections)
	ProcessID          *int           `json:"process_id,omitempty"`           // Process ID of STDIO command
}

// MCPPluginConfig holds configuration options for initializing the MCP plugin.
type MCPPluginConfig struct {
	ServerPort    string                  `json:"server_port,omitempty"`    // Port for local MCP server (defaults to :8181)
	AgenticMode   bool                    `json:"agentic_mode,omitempty"`   // Enable agentic flow for tool results
	ClientConfigs []ClientExecutionConfig `json:"client_configs,omitempty"` // Per-client execution configurations
}

// ExternalMCPConfig defines configuration for connecting to an external MCP server.
type ExternalMCPConfig struct {
	Name                 string         // Unique name for this external MCP connection
	ConnectionType       ConnectionType // How to connect (HTTP or STDIO)
	HTTPConnectionString *string        // HTTP URL (required for HTTP connections)
	StdioConfig          *StdioConfig   // STDIO configuration (required for STDIO connections)
}

// StdioConfig defines how to launch a STDIO-based MCP server.
type StdioConfig struct {
	Command string   // Executable command to run
	Args    []string // Command line arguments
}

// ToolHandler is a generic function type for handling tool calls with typed arguments.
// T represents the expected argument structure for the tool.
type ToolHandler[T any] func(args T) (string, error)

// ============================================================================
// CONSTRUCTOR AND INITIALIZATION
// ============================================================================

// NewMCPPlugin creates and initializes a new MCP plugin instance.
//
// Parameters:
//   - config: Plugin configuration including server port, agentic mode, and client configs
//   - logger: Logger instance for structured logging (uses default if nil)
//
// Returns:
//   - *MCPPlugin: Initialized plugin instance
//   - error: Any initialization error
//
// The plugin will pre-create client entries for any configured clients but won't
// establish connections until ConnectToExternalMCP is called.
func NewMCPPlugin(config MCPPluginConfig, logger schemas.Logger) (*MCPPlugin, error) {
	// Convert client configs to map for faster lookup during operations
	clientMap := make(map[string]*PluginClient)
	for _, clientConfig := range config.ClientConfigs {
		clientMap[clientConfig.Name] = &PluginClient{
			Name:            clientConfig.Name,
			ExecutionConfig: clientConfig,
			ToolMap:         make(map[string]schemas.Tool),
		}
	}

	// Use provided logger or create default logger with info level
	if logger == nil {
		logger = bifrost.NewDefaultLogger(schemas.LogLevelInfo)
	}

	plugin := &MCPPlugin{
		serverPort:  config.ServerPort,
		agenticMode: config.AgenticMode,
		clientMap:   clientMap,
		logger:      logger,
	}

	plugin.logger.Info(LogPrefix + " MCP Plugin initialized")
	if config.AgenticMode {
		plugin.logger.Info(LogPrefix + " Agentic mode enabled")
	}

	return plugin, nil
}

// ============================================================================
// LOCAL MCP SERVER MANAGEMENT
// ============================================================================

// createLocalMCPServer creates a new local MCP server instance with HTTP transport.
// This server will host tools registered via RegisterTool function.
//
// Parameters:
//   - config: Plugin configuration containing server port
//
// Returns:
//   - *mcp_golang.Server: Configured MCP server instance
//   - error: Any creation error
func (p *MCPPlugin) createLocalMCPServer(config MCPPluginConfig) (*mcp_golang.Server, error) {
	// Use configured port or default
	serverPort := config.ServerPort
	if serverPort == "" {
		serverPort = DefaultServerPort
	}

	// Create HTTP transport for the MCP server
	serverTransport := httpTransport.NewHTTPTransport("/mcp")
	serverTransport.WithAddr(serverPort)
	server := mcp_golang.NewServer(serverTransport)

	return server, nil
}

// createLocalMCPClient creates a client that connects to the local MCP server.
// This client is used internally by Bifrost to access locally hosted tools.
//
// Parameters:
//   - config: Plugin configuration containing server port
//
// Returns:
//   - *PluginClient: Configured client for local server
//   - error: Any creation error
func (p *MCPPlugin) createLocalMCPClient(config MCPPluginConfig) (*PluginClient, error) {
	// Use configured port or default
	serverPort := config.ServerPort
	if serverPort == "" {
		serverPort = DefaultServerPort
	}

	// Create HTTP client transport pointing to local server
	clientTransport := httpTransport.NewHTTPClientTransport("/mcp")
	clientTransport.WithBaseURL(fmt.Sprintf("http://localhost%s", serverPort))
	client := mcp_golang.NewClientWithInfo(clientTransport, mcp_golang.ClientInfo{
		Name:    BifrostClientName,
		Version: BifrostVersion,
	})

	return &PluginClient{
		Name: BifrostClientName,
		Conn: client,
		ExecutionConfig: ClientExecutionConfig{
			Name:          BifrostClientName,
			DefaultPolicy: ToolExecutionPolicyRequireApproval,
			ToolPolicies:  make(map[string]ToolExecutionPolicy),
		},
		ToolMap: make(map[string]schemas.Tool),
	}, nil
}

// setupLocalHost initializes the local MCP server and client if not already running.
// This is called automatically when tools are registered or when the server is needed.
//
// Returns:
//   - error: Any setup error
func (p *MCPPlugin) setupLocalHost() error {
	// Check if server is already running
	if p.server != nil && p.serverRunning {
		return nil
	}

	// Create and configure local MCP server
	server, err := p.createLocalMCPServer(MCPPluginConfig{ServerPort: p.serverPort})
	if err != nil {
		return fmt.Errorf("failed to create local MCP server: %w", err)
	}
	p.server = server

	// Create and configure local MCP client
	client, err := p.createLocalMCPClient(MCPPluginConfig{ServerPort: p.serverPort})
	if err != nil {
		return fmt.Errorf("failed to create local MCP client: %w", err)
	}
	p.clientMap[BifrostClientKey] = client

	// Start the server and initialize client connection
	return p.startLocalMCPServer()
}

// startLocalMCPServer starts the HTTP server and initializes the client connection.
// The server runs in a separate goroutine to avoid blocking.
//
// Returns:
//   - error: Any startup error
func (p *MCPPlugin) startLocalMCPServer() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Check if server is already running
	if p.server != nil && p.serverRunning {
		return nil
	}

	if p.server == nil {
		return fmt.Errorf("server not initialized")
	}

	// Start the HTTP server in background goroutine
	go func() {
		if err := p.server.Serve(); err != nil && err != http.ErrServerClosed {
			p.logger.Error(fmt.Errorf(LogPrefix+" MCP server error: %w", err))
			p.mu.Lock()
			p.serverRunning = false
			p.mu.Unlock()
		}
	}()

	// Mark server as running
	p.serverRunning = true

	// Initialize the client connection to the server
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, ok := p.clientMap[BifrostClientKey]; !ok {
		return fmt.Errorf("bifrost client not found")
	}

	_, err := p.clientMap[BifrostClientKey].Conn.Initialize(ctx)
	if err != nil {
		p.serverRunning = false
		return fmt.Errorf("failed to initialize MCP client: %v", err)
	}

	return nil
}

// ============================================================================
// TOOL REGISTRATION
// ============================================================================

// RegisterTool registers a typed tool handler with the local MCP server.
// This is a convenience function that handles the conversion between typed Go
// handlers and the MCP protocol.
//
// Type Parameters:
//   - T: The expected argument type for the tool (must be JSON-deserializable)
//
// Parameters:
//   - plugin: The MCP plugin instance
//   - name: Unique tool name
//   - description: Human-readable tool description
//   - handler: Typed function that handles tool execution
//   - toolSchema: Bifrost tool schema for function calling
//   - policy: Execution policy for this tool
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
//	RegisterTool(plugin, "echo", "Echo a message",
//	    func(args EchoArgs) (string, error) {
//	        return args.Message, nil
//	    }, toolSchema, ToolExecutionPolicyAutoExecute)
func RegisterTool[T any](plugin *MCPPlugin, name, description string, handler ToolHandler[T], toolSchema schemas.Tool, policy ToolExecutionPolicy) error {
	// Ensure local server is set up
	if err := plugin.setupLocalHost(); err != nil {
		return fmt.Errorf("failed to setup local host: %w", err)
	}

	// Verify internal client exists
	if _, ok := plugin.clientMap[BifrostClientKey]; !ok {
		return fmt.Errorf("bifrost client not found")
	}

	plugin.mu.Lock()
	defer plugin.mu.Unlock()

	plugin.logger.Info(fmt.Sprintf(LogPrefix+" Registering typed tool: %s", name))

	// Create MCP handler wrapper that converts between typed and MCP interfaces
	mcpHandler := func(args T) (*mcp_golang.ToolResponse, error) {
		result, err := handler(args)
		if err != nil {
			return mcp_golang.NewToolResponse(mcp_golang.NewTextContent(fmt.Sprintf("Error: %s", err.Error()))), nil
		}
		return mcp_golang.NewToolResponse(mcp_golang.NewTextContent(result)), nil
	}

	// Register with the underlying mcp-golang server
	err := plugin.server.RegisterTool(name, description, mcpHandler)
	if err != nil {
		return fmt.Errorf("failed to register tool with MCP server: %w", err)
	}

	// Store tool definition and policy for Bifrost integration
	plugin.clientMap[BifrostClientKey].ToolMap[name] = toolSchema
	plugin.clientMap[BifrostClientKey].ExecutionConfig.ToolPolicies[name] = policy

	return nil
}

// ============================================================================
// EXTERNAL MCP CONNECTION MANAGEMENT
// ============================================================================

// ConnectToExternalMCP establishes a connection to an external MCP server and
// registers its available tools with the plugin.
//
// Supported connection types:
//   - HTTP: Connects to an HTTP-based MCP server
//   - STDIO: Launches and connects to a command-line MCP server
//
// Parameters:
//   - config: External MCP connection configuration
//
// Returns:
//   - error: Any connection or registration error
//
// The function will:
//  1. Create or update the client entry in clientMap
//  2. Establish the connection based on the specified type
//  3. Initialize the connection and retrieve available tools
//  4. Register tools with the plugin (subject to filtering rules)
func (p *MCPPlugin) ConnectToExternalMCP(config ExternalMCPConfig) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Initialize or validate client entry
	if existingClient, exists := p.clientMap[config.Name]; exists {
		// Client entry exists from config, check for existing connection
		if existingClient.Conn != nil {
			return fmt.Errorf("client %s already has an active connection", config.Name)
		}
		// Update connection type for this connection attempt
		existingClient.ConnectionInfo.Type = config.ConnectionType
	} else {
		// Create new client entry with default configuration
		p.clientMap[config.Name] = &PluginClient{
			Name: config.Name,
			ExecutionConfig: ClientExecutionConfig{
				Name:          config.Name,
				DefaultPolicy: ToolExecutionPolicyRequireApproval,
				ToolPolicies:  make(map[string]ToolExecutionPolicy),
				ToolsToSkip:   make([]string, 0),
			},
			ToolMap: make(map[string]schemas.Tool),
			ConnectionInfo: ClientConnectionInfo{
				Type: config.ConnectionType,
			},
		}
	}

	var externalClient *mcp_golang.Client
	var err error

	// Create appropriate transport based on connection type
	switch config.ConnectionType {
	case ConnectionTypeHTTP:
		externalClient, err = p.createHTTPConnection(config)
	case ConnectionTypeSTDIO:
		externalClient, err = p.createSTDIOConnection(config)
	default:
		return fmt.Errorf("unknown connection type: %s", config.ConnectionType)
	}

	if err != nil {
		return fmt.Errorf("failed to create connection: %w", err)
	}

	// Initialize the external client with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err = externalClient.Initialize(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize external MCP client %s: %v", config.Name, err)
	}

	// Retrieve and register available tools from the external server
	err = p.registerExternalTools(ctx, externalClient, config.Name)
	if err != nil {
		p.logger.Warn(fmt.Sprintf(LogPrefix+" Failed to register tools from %s: %v", config.Name, err))
		// Continue with connection even if tool registration fails
	}

	// Store the external client connection
	p.clientMap[config.Name].Conn = externalClient

	return nil
}

// createHTTPConnection creates an HTTP-based MCP client connection.
func (p *MCPPlugin) createHTTPConnection(config ExternalMCPConfig) (*mcp_golang.Client, error) {
	if config.HTTPConnectionString == nil {
		return nil, fmt.Errorf("HTTP connection string is required")
	}

	// Store HTTP connection info
	p.clientMap[config.Name].ConnectionInfo.HTTPConnectionURL = config.HTTPConnectionString

	// Create HTTP transport
	clientTransport := httpTransport.NewHTTPClientTransport("/mcp")
	clientTransport.WithBaseURL(*config.HTTPConnectionString)

	return mcp_golang.NewClientWithInfo(clientTransport, mcp_golang.ClientInfo{
		Name:    fmt.Sprintf("Bifrost-%s", config.Name),
		Version: "1.0.0",
	}), nil
}

// createSTDIOConnection creates a STDIO-based MCP client connection.
func (p *MCPPlugin) createSTDIOConnection(config ExternalMCPConfig) (*mcp_golang.Client, error) {
	if config.StdioConfig == nil {
		return nil, fmt.Errorf("stdio config is required")
	}

	// Store STDIO command info for display
	cmdString := fmt.Sprintf("%s %s", config.StdioConfig.Command, strings.Join(config.StdioConfig.Args, " "))
	p.clientMap[config.Name].ConnectionInfo.StdioCommandString = &cmdString

	// Create and start the STDIO command
	cmd := exec.Command(config.StdioConfig.Command, config.StdioConfig.Args...)

	// Get stdin/stdout pipes before starting
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to get stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close() // Clean up stdin if stdout fails
		return nil, fmt.Errorf("failed to get stdout pipe: %v", err)
	}

	// Start the command
	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		return nil, fmt.Errorf("failed to start command '%s %v': %v", config.StdioConfig.Command, config.StdioConfig.Args, err)
	}

	// Track the command and process ID for cleanup
	p.clientMap[config.Name].StdioCommand = cmd
	if cmd.Process != nil {
		pid := cmd.Process.Pid
		p.clientMap[config.Name].ConnectionInfo.ProcessID = &pid
	}

	// Create stdio transport with the command's stdout as our stdin, and stdin as our stdout
	stdioTransport := stdio.NewStdioServerTransportWithIO(stdout, stdin)

	return mcp_golang.NewClientWithInfo(stdioTransport, mcp_golang.ClientInfo{
		Name:    fmt.Sprintf("Bifrost-%s", config.Name),
		Version: "1.0.0",
	}), nil
}

// registerExternalTools retrieves and registers tools from an external MCP server.
func (p *MCPPlugin) registerExternalTools(ctx context.Context, client *mcp_golang.Client, clientName string) error {
	// Get available tools from external server
	// Pass empty string instead of nil to avoid "Expected string, received null" error
	toolsResponse, err := client.ListTools(ctx, bifrost.Ptr(""))
	if err != nil {
		return fmt.Errorf("failed to list tools: %v", err)
	}

	if toolsResponse == nil {
		return nil // No tools available
	}

	// Convert and register each tool
	for _, mcpTool := range toolsResponse.Tools {
		// Skip tools that are configured to be skipped
		if p.shouldSkipTool(mcpTool.Name, clientName) {
			continue
		}

		// Convert MCP tool schema to Bifrost format
		bifrostTool := convertMCPToolToBifrostSchema(&mcpTool)
		p.clientMap[clientName].ToolMap[mcpTool.Name] = bifrostTool
	}

	return nil
}

// ============================================================================
// PLUGIN INTERFACE IMPLEMENTATION
// ============================================================================

// GetName returns the plugin's name identifier.
// This implements the schemas.Plugin interface.
func (p *MCPPlugin) GetName() string {
	return PluginName
}

// SetBifrostClient sets the Bifrost client instance for agentic mode.
// This client is used to make follow-up requests when agentic mode is enabled.
//
// Parameters:
//   - client: Bifrost client instance
func (p *MCPPlugin) SetBifrostClient(client *bifrost.Bifrost) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bifrostClient = client
}

// PreHook is called before request processing to add available MCP tools.
// This implements the schemas.Plugin interface.
//
// The function:
//  1. Handles approved tool execution (from user approval flow)
//  2. Adds available MCP tools to the request for normal flow
//  3. Applies client filtering based on request context
//
// Parameters:
//   - ctx: Request context (may contain client filtering preferences)
//   - req: Incoming Bifrost request
//
// Returns:
//   - *schemas.BifrostRequest: Modified request with MCP tools
//   - *schemas.BifrostResponse: Response if tools were executed (short-circuit)
//   - error: Any processing error
func (p *MCPPlugin) PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.BifrostResponse, error) {
	// Check if this is an approved tool execution request
	if req.MCPTools != nil && len(*req.MCPTools) > 0 && req.Input.ChatCompletionInput != nil {
		return p.handleApprovedTools(ctx, req)
	}

	// Normal flow: Add available tools to request
	availableTools := p.getFilteredAvailableTools(ctx)

	// Initialize tools array if needed
	if req.Params == nil {
		req.Params = &schemas.ModelParameters{}
	}
	if req.Params.Tools == nil {
		req.Params.Tools = &[]schemas.Tool{}
	}
	tools := *req.Params.Tools

	// Add MCP tools, avoiding duplicates
	for _, mcpTool := range availableTools {
		isDuplicate := false
		for _, tool := range tools {
			if tool.Function.Name == mcpTool.Function.Name {
				isDuplicate = true
				break
			}
		}
		if !isDuplicate {
			tools = append(tools, mcpTool)
		}
	}

	req.Params.Tools = &tools
	return req, nil, nil
}

// PostHook is called after response generation to handle tool calls.
// This implements the schemas.Plugin interface.
//
// The function:
//  1. Detects tool calls in the response
//  2. Applies execution policies (auto-execute vs require approval)
//  3. Executes approved tools or returns pending tools for user approval
//  4. Handles agentic flow if enabled
//
// Parameters:
//   - ctx: Request context
//   - res: Generated response from LLM
//   - err: Any error from response generation
//
// Returns:
//   - *schemas.BifrostResponse: Modified response with tool results or pending tools
//   - *schemas.BifrostError: Any processing error
//   - error: Any fatal error
func (p *MCPPlugin) PostHook(ctx *context.Context, res *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if res == nil || res.Choices == nil {
		return res, err, nil
	}

	// Check each choice for tool calls
	for i, choice := range res.Choices {
		if choice.Message.ToolCalls != nil && len(*choice.Message.ToolCalls) > 0 {
			return p.handleToolCallsWithPolicy(ctx, res, i, choice)
		}
	}

	return res, err, nil
}

// Cleanup performs cleanup of all resources when the plugin is being destroyed.
// This implements the schemas.Plugin interface.
//
// The function:
//  1. Terminates all STDIO processes
//  2. Disconnects all MCP clients
//  3. Clears server references
//
// Returns:
//   - error: Any cleanup error
func (p *MCPPlugin) Cleanup() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Clean up STDIO processes
	for _, client := range p.clientMap {
		if client.StdioCommand != nil && client.StdioCommand.Process != nil {
			p.logger.Info(fmt.Sprintf(LogPrefix+" Terminating STDIO process: %d", client.StdioCommand.Process.Pid))
			client.StdioCommand.Process.Kill()
			client.StdioCommand.Wait() // Wait for cleanup
		}
	}

	// Disconnect all clients
	for name := range p.clientMap {
		p.logger.Info(fmt.Sprintf(LogPrefix+" Disconnecting MCP client: %s", name))
	}
	p.clientMap = make(map[string]*PluginClient)

	// Clear server reference
	if p.server != nil {
		p.logger.Info(LogPrefix + " Clearing MCP server reference")
		p.server = nil
		p.serverRunning = false
	}

	return nil
}

// ============================================================================
// TOOL EXECUTION AND FLOW MANAGEMENT
// ============================================================================

// getFilteredAvailableTools returns tools filtered by request-level client inclusion/exclusion.
// Client filtering allows requests to specify which MCP clients' tools should be included.
//
// Parameters:
//   - ctx: Request context containing potential filtering directives
//
// Returns:
//   - []schemas.Tool: List of available tools after applying filters
func (p *MCPPlugin) getFilteredAvailableTools(ctx *context.Context) []schemas.Tool {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var includeClients []string
	var excludeClients []string

	// Extract client filtering from request context
	if ctx != nil {
		if existingIncludeClients, ok := (*ctx).Value(ContextKeyIncludeClients).([]string); ok && existingIncludeClients != nil {
			includeClients = existingIncludeClients
		}
		if existingExcludeClients, ok := (*ctx).Value(ContextKeyExcludeClients).([]string); ok && existingExcludeClients != nil {
			excludeClients = existingExcludeClients
		}
	}

	tools := make([]schemas.Tool, 0)
	for clientName, client := range p.clientMap {
		// Apply client filtering logic
		if !p.shouldIncludeClient(clientName, includeClients, excludeClients) {
			continue
		}

		// Add all tools from this client
		for _, tool := range client.ToolMap {
			tools = append(tools, tool)
		}
	}
	return tools
}

// callTool executes a tool by finding the appropriate MCP client and invoking the tool.
//
// Parameters:
//   - ctx: Execution context
//   - toolName: Name of the tool to execute
//   - arguments: Tool arguments as key-value pairs
//
// Returns:
//   - *mcp_golang.ToolResponse: Tool execution result
//   - error: Any execution error
func (p *MCPPlugin) callTool(ctx context.Context, toolName string, arguments map[string]interface{}) (*mcp_golang.ToolResponse, error) {
	// Find which client has this tool
	client := p.findMCPClientForTool(toolName)
	if client == nil {
		return nil, fmt.Errorf("tool '%s' not found in any connected MCP client", toolName)
	}

	if client.Conn == nil {
		return nil, fmt.Errorf("client '%s' has no active connection", client.Name)
	}

	return client.Conn.CallTool(ctx, toolName, arguments)
}

// handleApprovedTools executes MCP tools that have been approved by the user.
// This is called when a request contains pre-approved tools.
//
// Parameters:
//   - ctx: Request context
//   - req: Request containing approved tools
//
// Returns:
//   - *schemas.BifrostRequest: Modified request for agentic flow
//   - *schemas.BifrostResponse: Response with tool results (non-agentic)
//   - error: Any execution error
func (p *MCPPlugin) handleApprovedTools(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.BifrostResponse, error) {
	// Validate request has conversation history
	if req.Input.ChatCompletionInput == nil || len(*req.Input.ChatCompletionInput) == 0 {
		return req, nil, nil
	}

	messages := *req.Input.ChatCompletionInput

	// Find the assistant message with tool calls
	var assistantMessageWithToolCalls *schemas.BifrostMessage
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == schemas.ModelChatMessageRoleAssistant && messages[i].ToolCalls != nil && len(*messages[i].ToolCalls) > 0 {
			assistantMessageWithToolCalls = &messages[i]
			break
		}
	}

	if assistantMessageWithToolCalls == nil {
		return req, nil, nil
	}

	// Create approved tools lookup map
	approvedTools := make(map[string]schemas.Tool)
	for _, tool := range *req.MCPTools {
		approvedTools[tool.Function.Name] = tool
	}

	toolCallResults := make([]schemas.BifrostMessage, 0)

	// Execute each approved tool
	for _, toolCall := range *assistantMessageWithToolCalls.ToolCalls {
		if toolCall.Function.Name == nil {
			continue
		}
		toolName := *toolCall.Function.Name

		// Verify tool is approved and is an MCP tool
		if _, isApproved := approvedTools[toolName]; !isApproved {
			continue
		}

		client := p.findMCPClientForTool(toolName)
		if client == nil {
			continue
		}

		// Execute the tool
		toolMsg, err := p.executeSingleTool(context.Background(), toolCall)
		if err != nil {
			return nil, &schemas.BifrostResponse{}, err
		}

		toolCallResults = append(toolCallResults, toolMsg)
	}

	// Handle results based on agentic mode
	if len(toolCallResults) == 0 {
		return req, nil, nil
	}

	if p.checkAgenticModeAvailable() {
		// Agentic mode: Add tool results to conversation and continue to LLM
		return p.prepareAgenticRequest(req, toolCallResults), nil, nil
	}

	// Non-agentic mode: Return tool results directly
	response := &schemas.BifrostResponse{
		Choices: []schemas.BifrostResponseChoice{
			{
				Index:   0,
				Message: toolCallResults[0], // Return first tool result for backwards compatibility
			},
		},
	}
	return nil, response, nil
}

// prepareAgenticRequest prepares a request for agentic flow by adding tool results to conversation.
func (p *MCPPlugin) prepareAgenticRequest(req *schemas.BifrostRequest, toolCallResults []schemas.BifrostMessage) *schemas.BifrostRequest {
	conversationHistory := *req.Input.ChatCompletionInput

	// Remove placeholder tool messages to avoid duplicates
	cleanedHistory := make([]schemas.BifrostMessage, 0)
	for _, msg := range conversationHistory {
		// Skip placeholder tool messages
		if msg.Role == schemas.ModelChatMessageRoleTool &&
			msg.Content.ContentStr != nil &&
			strings.Contains(*msg.Content.ContentStr, "Tool execution approved") {
			continue
		}
		cleanedHistory = append(cleanedHistory, msg)
	}

	// Add actual tool results
	cleanedHistory = append(cleanedHistory, toolCallResults...)

	// Add synthesis prompt
	synthesisPrompt := schemas.BifrostMessage{
		Role: schemas.ModelChatMessageRoleUser,
		Content: schemas.MessageContent{
			ContentStr: bifrost.Ptr("Please provide a comprehensive response based on the tool results above."),
		},
	}
	cleanedHistory = append(cleanedHistory, synthesisPrompt)

	// Update request
	req.Input.ChatCompletionInput = &cleanedHistory
	req.MCPTools = nil // Clear MCP tools since we're not using them in the next turn

	return req
}

// handleToolCallsWithPolicy processes tool calls based on their execution policies.
// Tools are categorized as either requiring approval or auto-executing.
//
// Parameters:
//   - ctx: Request context
//   - res: Response containing tool calls
//   - choiceIndex: Index of the choice being processed
//   - choice: The specific choice containing tool calls
//
// Returns:
//   - *schemas.BifrostResponse: Modified response
//   - *schemas.BifrostError: Any processing error
//   - error: Any fatal error
func (p *MCPPlugin) handleToolCallsWithPolicy(ctx *context.Context, res *schemas.BifrostResponse, choiceIndex int, choice schemas.BifrostResponseChoice) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	pendingTools := make([]schemas.PendingMCPTool, 0)
	autoExecuteTools := make([]schemas.ToolCall, 0)

	// Categorize tools based on execution policies
	for _, toolCall := range *choice.Message.ToolCalls {
		if toolCall.Function.Name == nil {
			continue
		}
		toolName := *toolCall.Function.Name

		client := p.findMCPClientForTool(toolName)
		if client == nil {
			continue // Skip tools not found in any MCP client
		}

		if p.shouldRequireApproval(toolName, client.Name) {
			// Tool requires user approval
			var arguments map[string]interface{}
			if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &arguments); err != nil {
				return nil, &schemas.BifrostError{
					Error: schemas.ErrorField{
						Message: fmt.Sprintf("Failed to parse tool arguments: %v", err),
					},
				}, nil
			}

			pendingTool := schemas.PendingMCPTool{
				ClientName: client.Name,
				Tool:       client.ToolMap[toolName],
				ToolCall:   toolCall,
			}
			pendingTools = append(pendingTools, pendingTool)
		} else {
			// Tool can be auto-executed
			autoExecuteTools = append(autoExecuteTools, toolCall)
		}
	}

	// Handle pending tools (require user approval)
	if len(pendingTools) > 0 {
		res.ExtraFields.PendingMCPTools = &pendingTools
		return res, nil, nil
	}

	// Handle auto-execute tools
	if len(autoExecuteTools) > 0 {
		return p.executeToolsImmediately(ctx, res, choiceIndex, choice, autoExecuteTools)
	}

	return res, nil, nil
}

// executeToolsImmediately executes tools that are configured for automatic execution.
//
// Parameters:
//   - ctx: Request context
//   - res: Response to modify
//   - choiceIndex: Index of choice being processed
//   - choice: Choice containing tool calls
//   - toolsToExecute: List of tools to execute
//
// Returns:
//   - *schemas.BifrostResponse: Modified response
//   - *schemas.BifrostError: Any processing error
//   - error: Any fatal error
func (p *MCPPlugin) executeToolsImmediately(ctx *context.Context, res *schemas.BifrostResponse, choiceIndex int, choice schemas.BifrostResponseChoice, toolsToExecute []schemas.ToolCall) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	toolCallResults := make([]schemas.BifrostMessage, 0)
	assistantMessage := choice.Message // Preserve original assistant message

	// Execute each tool
	for _, toolCall := range toolsToExecute {
		toolMsg, err := p.executeSingleTool(*ctx, toolCall)
		if err != nil {
			return nil, &schemas.BifrostError{
				Error: schemas.ErrorField{
					Message: err.Error(),
				},
			}, nil
		}
		toolCallResults = append(toolCallResults, toolMsg)
	}

	// Handle results based on agentic mode
	if len(toolCallResults) > 0 {
		if p.checkAgenticModeAvailable() {
			// Agentic mode: Send conversation back to LLM for synthesis
			return p.handleAgenticFlow(ctx, res, choiceIndex, assistantMessage, toolCallResults)
		} else {
			// Non-agentic mode: Replace with tool result
			res.Choices[choiceIndex].Message = toolCallResults[0]
		}
	}

	return res, nil, nil
}

// handleAgenticFlow processes tool results in agentic mode by sending the conversation
// back to the LLM for synthesis and natural language response generation.
//
// Parameters:
//   - ctx: Request context
//   - res: Original response
//   - choiceIndex: Index of choice being processed
//   - assistantMessage: Original assistant message with tool calls
//   - toolCallResults: Results from tool execution
//
// Returns:
//   - *schemas.BifrostResponse: Response with synthesized content
//   - *schemas.BifrostError: Any processing error
//   - error: Any fatal error
func (p *MCPPlugin) handleAgenticFlow(ctx *context.Context, res *schemas.BifrostResponse, choiceIndex int, assistantMessage schemas.BifrostMessage, toolCallResults []schemas.BifrostMessage) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	// Verify agentic mode is properly configured
	if !p.checkAgenticModeAvailable() {
		// Fallback to non-agentic mode
		if len(toolCallResults) > 0 {
			res.Choices[choiceIndex].Message = toolCallResults[0]
		}
		return res, nil, nil
	}

	// Reconstruct conversation history
	var conversationHistory []schemas.BifrostMessage
	if res.ExtraFields.ChatHistory != nil {
		conversationHistory = *res.ExtraFields.ChatHistory
	}

	// Add assistant message with tool calls
	conversationHistory = append(conversationHistory, assistantMessage)

	// Add all tool results
	conversationHistory = append(conversationHistory, toolCallResults...)

	// Add synthesis prompt
	synthesisPrompt := schemas.BifrostMessage{
		Role: schemas.ModelChatMessageRoleUser,
		Content: schemas.MessageContent{
			ContentStr: bifrost.Ptr("Please provide a comprehensive response based on the tool results above."),
		},
	}
	conversationHistory = append(conversationHistory, synthesisPrompt)

	// Create agentic request
	agenticRequest := &schemas.BifrostRequest{
		Provider: res.ExtraFields.Provider,
		Model:    "gpt-4o-mini",
		Input: schemas.RequestInput{
			ChatCompletionInput: &conversationHistory,
		},
		Params: &res.ExtraFields.Params,
	}

	// Make agentic call
	agenticResponse, bifrostErr := p.bifrostClient.ChatCompletionRequest(context.Background(), agenticRequest)
	if bifrostErr != nil {
		p.logger.Warn(fmt.Sprintf(LogPrefix+" Agentic call failed: %v. Falling back to normal execution.", bifrostErr.Error.Message))
		// Fallback to non-agentic mode
		if len(toolCallResults) > 0 {
			res.Choices[choiceIndex].Message = toolCallResults[0]
		}
		return res, nil, nil
	}

	// Replace original choice with synthesized response
	if agenticResponse != nil && len(agenticResponse.Choices) > 0 {
		res.Choices[choiceIndex] = agenticResponse.Choices[0]
		res.ExtraFields.ChatHistory = &conversationHistory
	}

	return res, nil, nil
}
