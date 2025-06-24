package bifrost

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strconv"
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
	server        *server.MCPServer     // Local MCP server instance for hosting tools (STDIO-based)
	clientMap     map[string]*MCPClient // Map of MCP client names to their configurations
	mu            sync.RWMutex          // Read-write mutex for thread-safe operations
	serverRunning bool                  // Track whether local MCP server is running
	logger        schemas.Logger        // Logger instance for structured logging
}

// MCPClient represents a connected MCP client with its configuration and tools.
type MCPClient struct {
	Name            string                  // Unique name for this client
	Conn            *client.Client          // Active MCP client connection
	ExecutionConfig schemas.MCPClientConfig // Tool filtering settings
	ToolMap         map[string]schemas.Tool // Available tools mapped by name
	ConnectionInfo  MCPClientConnectionInfo `json:"connection_info"` // Connection metadata for management
	cancelFunc      context.CancelFunc      `json:"-"`               // Cancel function for SSE connections (not serialized)
}

// MCPClientConnectionInfo stores metadata about how a client is connected.
type MCPClientConnectionInfo struct {
	Type               schemas.MCPConnectionType `json:"type"`                           // Connection type (HTTP, STDIO, or SSE)
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
func newMCPManager(config schemas.MCPConfig, logger schemas.Logger) (*MCPManager, error) {
	// Use provided logger or create default logger with info level
	if logger == nil {
		logger = NewDefaultLogger(schemas.LogLevelInfo)
	}

	manager := &MCPManager{
		clientMap: make(map[string]*MCPClient),
		logger:    logger,
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

// createLocalMCPClient creates a client that connects to the local MCP server via STDIO.
// This client is used internally by Bifrost to access locally hosted tools.
//
// Returns:
//   - *MCPClient: Configured client for local server
//   - error: Any creation error
func (m *MCPManager) createLocalMCPClient() (*MCPClient, error) {
	// For local STDIO communication, we'll use the same process
	// Create a STDIO transport that communicates with our local server
	// This creates an in-process communication channel
	stdioTransport := transport.NewStdio(
		"",  // Empty command means in-process
		nil, // No environment variables needed
	)

	// Create the MCP client
	mcpClient := client.NewClient(stdioTransport)

	return &MCPClient{
		Name: BifrostMCPClientName,
		Conn: mcpClient,
		ExecutionConfig: schemas.MCPClientConfig{
			Name: BifrostMCPClientName,
		},
		ToolMap: make(map[string]schemas.Tool),
		ConnectionInfo: MCPClientConnectionInfo{
			Type: schemas.MCPConnectionTypeSTDIO,
		},
	}, nil
}

// startLocalMCPServer starts the STDIO server in a background goroutine.
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

	// Start the STDIO server in background goroutine
	go func() {
		if err := server.ServeStdio(m.server); err != nil {
			m.logger.Error(fmt.Errorf("MCP STDIO server error: %w", err))
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

	// Start the local client transport first
	if err := m.clientMap[BifrostMCPClientKey].Conn.Start(ctx); err != nil {
		m.serverRunning = false
		return fmt.Errorf("failed to start local MCP client transport: %v", err)
	}

	// Create proper initialize request
	initRequest := mcp.InitializeRequest{
		Request: mcp.Request{
			Method: string(mcp.MethodInitialize),
		},
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			Capabilities:    mcp.ClientCapabilities{},
			ClientInfo: mcp.Implementation{
				Name:    BifrostMCPClientName,
				Version: BifrostMCPVersion,
			},
		},
	}

	_, err := m.clientMap[BifrostMCPClientKey].Conn.Initialize(ctx, initRequest)
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
	callRequest := mcp.CallToolRequest{
		Request: mcp.Request{
			Method: string(mcp.MethodToolsCall),
		},
		Params: mcp.CallToolParams{
			Name:      toolName,
			Arguments: arguments,
		},
	}

	m.logger.Info(fmt.Sprintf("%s Starting tool execution: %s via client: %s", MCPLogPrefix, toolName, client.Name))

	toolResponse, callErr := client.Conn.CallTool(ctx, callRequest)
	if callErr != nil {
		m.logger.Error(fmt.Errorf("%s Tool execution failed for %s via client %s: %v", MCPLogPrefix, toolName, client.Name, callErr))
		return nil, fmt.Errorf("MCP tool call failed: %v", callErr)
	}

	m.logger.Info(fmt.Sprintf("%s Tool execution completed: %s", MCPLogPrefix, toolName))

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
		ctx, cancel = context.WithCancel(context.Background())
		// Don't defer cancel here - SSE needs the context to remain active
	} else {
		// Other connection types can use timeout context
		ctx, cancel = context.WithTimeout(context.Background(), MCPClientConnectionEstablishTimeout)
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
		Request: mcp.Request{
			Method: string(mcp.MethodInitialize),
		},
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
func (m *MCPManager) retrieveExternalTools(ctx context.Context, client *client.Client, config schemas.MCPClientConfig) (map[string]schemas.Tool, error) {
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
		return make(map[string]schemas.Tool), nil // No tools available
	}

	tools := make(map[string]schemas.Tool)

	// toolsResponse is already a ListToolsResult
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
func (m *MCPManager) convertMCPToolToBifrostSchema(mcpTool *mcp.Tool) schemas.Tool {
	// Convert MCP tool schema to Bifrost tool schema
	properties := make(map[string]interface{})
	required := []string{}

	// Handle the InputSchema - it's a struct, not a pointer
	inputSchema := mcpTool.InputSchema
	// Convert to map for processing (this may need adjustment based on actual structure)
	if schemaBytes, err := json.Marshal(inputSchema); err == nil {
		var schemaMap map[string]interface{}
		if json.Unmarshal(schemaBytes, &schemaMap) == nil {
			if props, ok := schemaMap["properties"].(map[string]interface{}); ok {
				// Sanitize properties to handle type mismatches
				properties = m.sanitizeProperties(props)
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

	// Description is a string, not a pointer
	description := mcpTool.Description

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
func (m *MCPManager) extractTextFromMCPResponse(toolResponse *mcp.CallToolResult, toolName string) string {
	if toolResponse == nil {
		return fmt.Sprintf("MCP tool '%s' executed successfully", toolName)
	}

	var responseTextBuilder strings.Builder
	if len(toolResponse.Content) > 0 {
		for _, contentBlock := range toolResponse.Content {
			if textContent, ok := contentBlock.(*mcp.TextContent); ok && textContent.Text != "" {
				responseTextBuilder.WriteString(textContent.Text)
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

// ============================================================================
// HELPER METHODS
// ============================================================================

// Schema field type definitions for validation
type schemaFieldType int

const (
	schemaString schemaFieldType = iota
	schemaNumber
	schemaBoolean
	schemaArray
)

var (
	// Define expected types for JSON Schema fields
	schemaFieldTypes = map[string]schemaFieldType{
		// String fields
		"type": schemaString, "format": schemaString, "pattern": schemaString,
		"$id": schemaString, "$schema": schemaString, "title": schemaString,
		"description": schemaString, "contentMediaType": schemaString,
		"contentEncoding": schemaString, "$ref": schemaString, "$comment": schemaString,

		// Number fields
		"minimum": schemaNumber, "maximum": schemaNumber, "minLength": schemaNumber,
		"maxLength": schemaNumber, "minItems": schemaNumber, "maxItems": schemaNumber,
		"multipleOf": schemaNumber, "exclusiveMinimum": schemaNumber, "exclusiveMaximum": schemaNumber,

		// Boolean fields
		"additionalProperties": schemaBoolean, "additionalItems": schemaBoolean,
		"uniqueItems": schemaBoolean, "readOnly": schemaBoolean, "writeOnly": schemaBoolean,

		// Array fields
		"required": schemaArray, "enum": schemaArray, "examples": schemaArray,
		"allOf": schemaArray, "anyOf": schemaArray, "oneOf": schemaArray,
	}
)

// convertToExpectedType converts a value to the expected type for a schema field
func convertToExpectedType(value interface{}, expectedType schemaFieldType) interface{} {
	switch expectedType {
	case schemaString:
		switch v := value.(type) {
		case string:
			return v
		case bool:
			if v {
				return "boolean"
			}
			return "string"
		case float64:
			return fmt.Sprintf("%.0f", v)
		case int:
			return fmt.Sprintf("%d", v)
		default:
			return fmt.Sprintf("%v", v)
		}

	case schemaNumber:
		switch v := value.(type) {
		case float64, int:
			return v
		case string:
			if num, err := strconv.ParseFloat(v, 64); err == nil {
				return num
			}
			return 0
		case bool:
			if v {
				return 1
			}
			return 0
		default:
			return 0
		}

	case schemaBoolean:
		switch v := value.(type) {
		case bool:
			return v
		case string:
			switch v {
			case "true", "1":
				return true
			case "false", "0":
				return false
			default:
				return false
			}
		case float64, int:
			return v != 0
		default:
			return false
		}

	case schemaArray:
		switch v := value.(type) {
		case []interface{}:
			return v
		case bool:
			if v {
				return []interface{}{"true"}
			}
			return []interface{}{}
		case string:
			if v != "" {
				return []interface{}{v}
			}
			return []interface{}{}
		default:
			return []interface{}{v}
		}
	}

	return value
}

// sanitizeProperties recursively sanitizes property values to ensure they are valid for JSON Schema.
// It handles common schema validation issues from external MCP servers.
func (m *MCPManager) sanitizeProperties(properties map[string]interface{}) map[string]interface{} {
	sanitized := make(map[string]interface{})

	for key, value := range properties {
		switch v := value.(type) {
		case map[string]interface{}:
			// Handle nested property objects
			nestedProperty := m.sanitizeProperties(v)

			// Fix array types missing items specification
			if propType, hasType := nestedProperty["type"].(string); hasType && propType == "array" {
				if _, hasItems := nestedProperty["items"]; !hasItems {
					// Default to string items if not specified
					nestedProperty["items"] = map[string]interface{}{
						"type": "string",
					}
				}
			}

			// Fix object types without properties
			if propType, hasType := nestedProperty["type"].(string); hasType && propType == "object" {
				if _, hasProps := nestedProperty["properties"]; !hasProps {
					if _, hasAdditional := nestedProperty["additionalProperties"]; !hasAdditional {
						// Allow any additional properties for objects without defined properties
						nestedProperty["additionalProperties"] = true
					}
				}
			}

			// Sanitize field types based on JSON Schema expectations
			for propKey, propValue := range nestedProperty {
				if expectedType, hasExpectedType := schemaFieldTypes[propKey]; hasExpectedType {
					nestedProperty[propKey] = convertToExpectedType(propValue, expectedType)
				}
			}

			// Sanitize nested schemas in special fields
			for specialField, specialValue := range nestedProperty {
				switch specialField {
				case "items":
					if itemsMap, ok := specialValue.(map[string]interface{}); ok {
						nestedProperty["items"] = m.sanitizeProperties(itemsMap)
					}
				case "additionalProperties":
					if addPropsMap, ok := specialValue.(map[string]interface{}); ok {
						nestedProperty["additionalProperties"] = m.sanitizeProperties(addPropsMap)
					}
				case "patternProperties":
					if patternPropsMap, ok := specialValue.(map[string]interface{}); ok {
						sanitizedPatternProps := make(map[string]interface{})
						for pattern, schema := range patternPropsMap {
							if schemaMap, ok := schema.(map[string]interface{}); ok {
								sanitizedPatternProps[pattern] = m.sanitizeProperties(schemaMap)
							} else {
								sanitizedPatternProps[pattern] = schema
							}
						}
						nestedProperty["patternProperties"] = sanitizedPatternProps
					}
				}
			}

			sanitized[key] = nestedProperty
		default:
			// Use data-driven type conversion for all value types
			if expectedType, hasExpectedType := schemaFieldTypes[key]; hasExpectedType {
				sanitized[key] = convertToExpectedType(value, expectedType)
			} else {
				sanitized[key] = value
			}
		}
	}

	return sanitized
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
		return slices.Contains(includeClients, clientName)
	}

	// If excludeClients is specified, exclude those clients (blacklist mode)
	if len(excludeClients) > 0 {
		return !slices.Contains(excludeClients, clientName)
	}

	// Default: include all clients
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
	httpTransport, err := transport.NewStreamableHTTP(*config.ConnectionString)
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
	sseTransport, err := transport.NewSSE(*config.ConnectionString)
	if err != nil {
		return nil, MCPClientConnectionInfo{}, fmt.Errorf("failed to create SSE transport: %w", err)
	}

	client := client.NewClient(sseTransport)

	return client, connectionInfo, nil
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
	for name, client := range m.clientMap {
		m.logger.Info(fmt.Sprintf("%s Disconnecting MCP client: %s", MCPLogPrefix, name))

		// Cancel SSE context if present (required for proper SSE cleanup)
		if client.cancelFunc != nil {
			client.cancelFunc()
			client.cancelFunc = nil
		}

		// Close the client transport connection
		// This handles cleanup for all transport types (HTTP, STDIO, SSE)
		if client.Conn != nil {
			if err := client.Conn.Close(); err != nil {
				m.logger.Error(fmt.Errorf("%s Failed to close MCP client %s: %w", MCPLogPrefix, name, err))
			}
			client.Conn = nil
		}

		// Clear client tool map
		client.ToolMap = make(map[string]schemas.Tool)
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
