package mcptests

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// HTTP CONNECTION TESTS
// =============================================================================

func TestHTTPConnection(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	// Create client config
	clientConfig := GetSampleHTTPClientConfig(config.HTTPServerURL)
	// Apply headers from environment if set
	if len(config.HTTPHeaders) > 0 {
		clientConfig.Headers = config.HTTPHeaders
	}

	// Setup MCP manager with HTTP client
	manager := setupMCPManager(t, clientConfig)

	// Verify client was added
	clients := manager.GetClients()
	require.Len(t, clients, 1, "should have one client")
	assert.Equal(t, schemas.MCPConnectionTypeHTTP, clients[0].ConnectionInfo.Type)
	assert.Equal(t, schemas.MCPConnectionStateConnected, clients[0].State)
}

func TestHTTPConnectionInvalidURL(t *testing.T) {
	t.Parallel()

	// Create client config with invalid URL
	invalidURL := "http://invalid-url-that-does-not-exist:9999"
	clientConfig := GetSampleHTTPClientConfig(invalidURL)

	// This should fail or have client in disconnected state
	manager := setupMCPManager(t, clientConfig)
	clients := manager.GetClients()

	if len(clients) > 0 {
		// If client was added, it should eventually be disconnected
		time.Sleep(2 * time.Second)
		clients = manager.GetClients()
		if len(clients) > 0 {
			assert.Equal(t, schemas.MCPConnectionStateDisconnected, clients[0].State)
		}
	}
}

// =============================================================================
// SSE CONNECTION TESTS
// =============================================================================

func TestSSEConnection(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.SSEServerURL == "" {
		t.Skip("MCP_SSE_URL not set")
	}

	// Create client config
	clientConfig := GetSampleSSEClientConfig(config.SSEServerURL)
	// Apply headers from environment if set
	if len(config.SSEHeaders) > 0 {
		clientConfig.Headers = config.SSEHeaders
	}

	// Setup MCP manager with SSE client
	manager := setupMCPManager(t, clientConfig)

	// Verify client was added
	clients := manager.GetClients()
	require.Len(t, clients, 1, "should have one client")
	assert.Equal(t, schemas.MCPConnectionTypeSSE, clients[0].ConnectionInfo.Type)
	assert.Equal(t, schemas.MCPConnectionStateConnected, clients[0].State)
}

func TestSSEConnectionReconnect(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.SSEServerURL == "" {
		t.Skip("MCP_SSE_URL not set")
	}

	clientConfig := GetSampleSSEClientConfig(config.SSEServerURL)
	// Apply headers from environment if set
	if len(config.SSEHeaders) > 0 {
		clientConfig.Headers = config.SSEHeaders
	}
	manager := setupMCPManager(t, clientConfig)

	clients := manager.GetClients()
	require.Len(t, clients, 1, "should have one client")

	clientID := clients[0].ExecutionConfig.ID

	// Attempt to reconnect
	err := manager.ReconnectClient(clientID)
	assert.NoError(t, err, "reconnect should succeed")

	// Verify still connected
	clients = manager.GetClients()
	AssertClientState(t, clients, clientID, schemas.MCPConnectionStateConnected)
}

// =============================================================================
// STDIO CONNECTION TESTS
// =============================================================================

func TestSTDIOConnection(t *testing.T) {
	t.Parallel()

	// Create STDIO server
	stdioServer := NewSTDIOServerManager(t)
	err := stdioServer.Start()
	require.NoError(t, err, "should start STDIO server")
	defer stdioServer.Stop()

	// Wait for server to be ready
	time.Sleep(500 * time.Millisecond)

	// Note: For actual STDIO connection test, we need a compiled executable
	// This test verifies the server manager works
	assert.True(t, stdioServer.IsRunning(), "STDIO server should be running")
}

func TestSTDIOServerDoubleStart(t *testing.T) {
	t.Parallel()

	stdioServer := NewSTDIOServerManager(t)

	// Start server
	err := stdioServer.Start()
	require.NoError(t, err, "first start should succeed")

	// Try to start again
	err = stdioServer.Start()
	assert.Error(t, err, "second start should fail")
	assert.Contains(t, err.Error(), "already running")
}

func TestSTDIOConnectionTimeout(t *testing.T) {
	t.Parallel()

	// Create client config with non-existent command
	clientConfig := GetSampleSTDIOClientConfig("nonexistent-command", []string{})

	// This should fail during connection
	manager := setupMCPManager(t, clientConfig)

	// Wait a bit for connection attempt
	time.Sleep(2 * time.Second)

	clients := manager.GetClients()
	if len(clients) > 0 {
		// Client should be in disconnected or error state
		assert.NotEqual(t, schemas.MCPConnectionStateConnected, clients[0].State)
	}
}

// =============================================================================
// INPROCESS CONNECTION TESTS
// =============================================================================

func TestInProcessConnection(t *testing.T) {
	t.Parallel()

	// For in-process connections, we don't create a client config
	// Instead, the internal server is created automatically when we register tools
	manager := setupMCPManager(t)

	// Register a test tool
	toolSchema := schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "test_inprocess_tool",
			Description: schemas.Ptr("A test tool for in-process execution"),
			Parameters: &schemas.ToolFunctionParameters{
				Type: "object",
				Properties: schemas.NewOrderedMapFromPairs(
					schemas.KV("message", map[string]interface{}{
						"type":        "string",
						"description": "The message to process",
					}),
				),
				Required: []string{"message"},
			},
		},
	}
	err := manager.RegisterTool(
		"test_inprocess_tool",
		"A test tool for in-process execution",
		func(args any) (string, error) {
			argsMap, ok := args.(map[string]interface{})
			if !ok {
				return "", assert.AnError
			}
			message, ok := argsMap["message"].(string)
			if !ok {
				return "", assert.AnError
			}
			result := map[string]interface{}{
				"result": "processed: " + message,
			}
			resultJSON, _ := json.Marshal(result)
			return string(resultJSON), nil
		},
		toolSchema,
	)
	require.NoError(t, err, "should register tool")

	// Verify tools are available
	ctx := createTestContext()
	tools := manager.GetToolPerClient(ctx)
	assert.NotEmpty(t, tools, "should have registered tool")
}

func TestInProcessToolExecution(t *testing.T) {
	t.Parallel()

	// InProcess connections don't need a client config - the internal server is created automatically
	manager := setupMCPManager(t)

	// Register a simple echo tool
	echoToolSchema := schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "echo_inprocess",
			Description: schemas.Ptr("Echoes the input"),
			Parameters: &schemas.ToolFunctionParameters{
				Type: "object",
				Properties: schemas.NewOrderedMapFromPairs(
					schemas.KV("text", map[string]interface{}{
						"type": "string",
					}),
				),
			},
		},
	}
	err := manager.RegisterTool(
		"echo_inprocess",
		"Echoes the input",
		func(args any) (string, error) {
			argsMap, ok := args.(map[string]interface{})
			if !ok {
				return "", assert.AnError
			}
			resultJSON, _ := json.Marshal(argsMap)
			return string(resultJSON), nil
		},
		echoToolSchema,
	)
	require.NoError(t, err, "should register tool")

	// Execute the tool
	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()
	// Create a tool call for echo_inprocess (matching the registered tool name with prefix)
	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-1"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("bifrostInternal-echo_inprocess"),
			Arguments: `{"text":"test message"}`,
		},
	}

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
	require.Nil(t, bifrostErr, "tool execution should succeed")
	assert.NotNil(t, result, "should have result")
}

// =============================================================================
// MULTIPLE CONNECTION TYPES TEST
// =============================================================================

func TestMultipleConnectionTypes(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)

	var clientConfigs []schemas.MCPClientConfig

	// Add HTTP client if available
	if config.HTTPServerURL != "" {
		httpConfig := GetSampleHTTPClientConfig(config.HTTPServerURL)
		httpConfig.ID = "http-client"
		// Apply headers from environment if set
		if len(config.HTTPHeaders) > 0 {
			httpConfig.Headers = config.HTTPHeaders
		}
		clientConfigs = append(clientConfigs, httpConfig)
	}

	// Add SSE client if available
	if config.SSEServerURL != "" {
		sseConfig := GetSampleSSEClientConfig(config.SSEServerURL)
		sseConfig.ID = "sse-client"
		// Apply headers from environment if set
		if len(config.SSEHeaders) > 0 {
			sseConfig.Headers = config.SSEHeaders
		}
		clientConfigs = append(clientConfigs, sseConfig)
	}

	// Note: We don't add an InProcess client config here because InProcess connections
	// are created automatically when tools are registered via RegisterTool()

	if len(clientConfigs) == 0 {
		t.Skip("No MCP servers configured")
	}

	// Create manager with multiple clients
	manager := setupMCPManager(t, clientConfigs...)

	// Verify all clients were added
	clients := manager.GetClients()
	// We expect at least the configured clients (HTTP/SSE if available)
	assert.GreaterOrEqual(t, len(clients), len(clientConfigs), "should have all configured clients")

	// Verify different connection types
	connectionTypes := make(map[schemas.MCPConnectionType]bool)
	for _, client := range clients {
		connectionTypes[client.ConnectionInfo.Type] = true
	}
	assert.GreaterOrEqual(t, len(connectionTypes), 1, "should have at least one connection type")
}

// =============================================================================
// CONNECTION CONFIGURATION TESTS
// =============================================================================

func TestConnectionWithHeaders(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	// Create client config with custom headers
	clientConfig := GetSampleHTTPClientConfig(config.HTTPServerURL)
	clientConfig.Headers = map[string]schemas.EnvVar{
		"Authorization":   *schemas.NewEnvVar("Bearer test-token"),
		"X-Custom-Header": *schemas.NewEnvVar("test-value"),
	}

	manager := setupMCPManager(t, clientConfig)
	clients := manager.GetClients()

	require.Len(t, clients, 1, "should have one client")
	assert.Equal(t, schemas.MCPConnectionStateConnected, clients[0].State)
}

func TestConnectionWithEnvironmentVariables(t *testing.T) {
	t.Parallel()

	// Create STDIO config with environment variables
	clientConfig := GetSampleSTDIOClientConfig("echo", []string{"test"})
	if clientConfig.StdioConfig != nil {
		clientConfig.StdioConfig.Envs = []string{"TEST_VAR=test_value"}
	}

	// Manager creation should validate environment variables
	manager := setupMCPManager(t, clientConfig)
	assert.NotNil(t, manager, "should create manager")
}

func TestInvalidConnectionType(t *testing.T) {
	t.Parallel()

	// Create client config with invalid connection type
	clientConfig := schemas.MCPClientConfig{
		ID:             "invalid-client",
		Name:           "Invalid Client",
		ConnectionType: "invalid_type",
	}

	// This should fail validation
	manager := setupMCPManager(t, clientConfig)

	// Verify client was not added or is in error state
	clients := manager.GetClients()
	if len(clients) > 0 {
		assert.NotEqual(t, schemas.MCPConnectionStateConnected, clients[0].State)
	}
}

func TestConnectionWithMissingRequiredFields(t *testing.T) {
	t.Parallel()

	// HTTP connection without ConnectionString
	clientConfig := schemas.MCPClientConfig{
		ID:             "missing-url-client",
		Name:           "Missing URL Client",
		ConnectionType: schemas.MCPConnectionTypeHTTP,
		// ConnectionString is missing
	}

	manager := setupMCPManager(t, clientConfig)
	clients := manager.GetClients()

	// Client should not be connected
	if len(clients) > 0 {
		assert.NotEqual(t, schemas.MCPConnectionStateConnected, clients[0].State)
	}
}
