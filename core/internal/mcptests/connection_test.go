package mcptests

import (
	"encoding/json"
	"net"
	"net/url"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/network"
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
	assert.Equal(t, schemas.MCPConnectionStateHealthy, clients[0].State)
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
			assert.Equal(t, schemas.MCPConnectionStateUnstable, clients[0].State)
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
	assert.Equal(t, schemas.MCPConnectionStateHealthy, clients[0].State)
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
	AssertClientState(t, clients, clientID, schemas.MCPConnectionStateHealthy)
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
		assert.NotEqual(t, schemas.MCPConnectionStateHealthy, clients[0].State)
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
	clientConfig.Headers = map[string]schemas.SecretVar{
		"Authorization":   *schemas.NewSecretVar("Bearer test-token"),
		"X-Custom-Header": *schemas.NewSecretVar("test-value"),
	}

	manager := setupMCPManager(t, clientConfig)
	clients := manager.GetClients()

	require.Len(t, clients, 1, "should have one client")
	assert.Equal(t, schemas.MCPConnectionStateHealthy, clients[0].State)
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
		assert.NotEqual(t, schemas.MCPConnectionStateHealthy, clients[0].State)
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
		assert.NotEqual(t, schemas.MCPConnectionStateHealthy, clients[0].State)
	}
}

// =============================================================================
// REQUIRE-PUBLIC-TARGET CONNECTION TESTS
// =============================================================================

// RequirePublicTarget is server-set when a client is registered over the management
// API with no admin credential check, and is never cleared. The dial policy it selects
// is unit-tested in core/mcp; what these pin is the effect a caller can observe — a
// flagged client pointed at a private address serves nothing.
//
// The pair matters more than either half: the refusal alone would also pass with the
// fixture server down, so the second test connects the same URL unflagged.

func TestRequirePublicTargetRefusesPrivateUpstream(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}
	// The default fixture is loopback, but MCP_USE_REMOTE=1 lets an operator point
	// MCP_HTTP_URL at any host. Against a public one the policy correctly ALLOWS the
	// connection, which would fail the assertions below and read as a broken guard.
	requirePrivateUpstream(t, config.HTTPServerURL)

	flagged := GetSampleHTTPClientConfig(config.HTTPServerURL)
	flagged.ID = "require-public-target-client"
	flagged.Name = "unauthenticatedRegistration"
	flagged.RequirePublicTarget = true

	manager := setupMCPManager(t, flagged)

	ctx := createTestContext()
	assert.Empty(t, manager.GetToolPerClient(ctx)[flagged.Name],
		"a RequirePublicTarget client must expose no tools from a private upstream")

	for _, client := range manager.GetClients() {
		if client.ExecutionConfig.Name == flagged.Name {
			assert.NotEqual(t, schemas.MCPConnectionStateHealthy, client.State,
				"a RequirePublicTarget client must not reach healthy against a private upstream")
		}
	}
}

// requirePrivateUpstream skips unless rawURL resolves to a private address, using the
// same classifier the production dial policy uses rather than a second definition of
// "private" that could drift from it.
func requirePrivateUpstream(t *testing.T, rawURL string) {
	t.Helper()

	parsed, err := url.Parse(rawURL)
	require.NoError(t, err, "MCP_HTTP_URL must be a valid URL")

	// Resolver rather than net.LookupIP: the lookup unblocks when the test's context is
	// cancelled, instead of outliving the test on a hung resolver.
	addrs, err := net.DefaultResolver.LookupIPAddr(t.Context(), parsed.Hostname())
	if err != nil || len(addrs) == 0 {
		t.Skipf("cannot resolve %q to decide whether it is private: %v", parsed.Hostname(), err)
	}
	for _, addr := range addrs {
		if network.IsPublicIP(addr.IP) {
			t.Skipf("MCP_HTTP_URL resolves to the public address %s; RequirePublicTarget permits it by design", addr.IP)
		}
	}
}

func TestRequirePublicTargetUnflaggedClientReachesSameUpstream(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	unflagged := GetSampleHTTPClientConfig(config.HTTPServerURL)
	unflagged.ID = "admin-registration-client"
	unflagged.Name = "adminRegistration"

	manager := setupMCPManager(t, unflagged)

	ctx := createTestContext()
	require.NotEmpty(t, manager.GetToolPerClient(ctx)[unflagged.Name],
		"the same upstream must be reachable without RequirePublicTarget")
}

// TestConnectionRefusesCloudMetadataEndpoint pins the pre-proxy destination guard that
// keeps MCP from being used as an SSRF primitive against the cloud metadata service.
// 169.254.169.254 is the canonical target; the guard blocks link-local and unspecified
// destinations regardless of whether a proxy is configured.
func TestConnectionRefusesCloudMetadataEndpoint(t *testing.T) {
	t.Parallel()

	for _, target := range []string{
		"http://169.254.169.254/mcp",
		"http://[fe80::1]/mcp",
		"http://0.0.0.0/mcp",
	} {
		clientConfig := GetSampleHTTPClientConfig(target)
		clientConfig.ID = "metadata-probe"
		clientConfig.Name = "MetadataProbe"

		manager := setupMCPManager(t, clientConfig)

		ctx := createTestContext()
		assert.Empty(t, manager.GetToolPerClient(ctx)[clientConfig.Name],
			"%s must expose no tools", target)

		for _, client := range manager.GetClients() {
			if client.ExecutionConfig.Name == clientConfig.Name {
				assert.NotEqual(t, schemas.MCPConnectionStateHealthy, client.State,
					"%s must not reach healthy", target)
			}
		}
	}
}
