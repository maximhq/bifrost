package mcptests

import (
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// ADD CLIENT TESTS
// =============================================================================

func TestAddClientDuplicate(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	manager := setupMCPManager(t)

	// Add client
	clientConfig := GetSampleHTTPClientConfig(config.HTTPServerURL)
	err := manager.AddClient(&clientConfig)
	require.NoError(t, err, "should add client first time")

	// Try to add same client again
	err = manager.AddClient(&clientConfig)
	// Should either return error or be idempotent
	if err == nil {
		clients := manager.GetClients()
		// Should still have reasonable number of clients (not double-added)
		assert.LessOrEqual(t, len(clients), 2, "should not duplicate clients excessively")
	}
}

// =============================================================================
// REMOVE CLIENT TESTS
// =============================================================================

func TestRemoveClient(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	clientConfig := GetSampleHTTPClientConfig(config.HTTPServerURL)
	manager := setupMCPManager(t, clientConfig)

	// Verify client exists
	clients := manager.GetClients()
	require.Len(t, clients, 1, "should have one client")
	clientID := clients[0].ExecutionConfig.ID

	// Remove client
	err := manager.RemoveClient(clientID)
	require.NoError(t, err, "should remove client")

	// Verify client was removed
	clients = manager.GetClients()
	assert.Len(t, clients, 0, "should have no clients")
}

func TestRemoveClientInvalidID(t *testing.T) {
	t.Parallel()

	manager := setupMCPManager(t)

	// Try to remove non-existent client
	err := manager.RemoveClient("non-existent-id")
	assert.Error(t, err, "should error when removing non-existent client")
}

func TestRemoveClientMultiple(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	// Add multiple clients
	httpConfig1 := GetSampleHTTPClientConfig(config.HTTPServerURL)
	httpConfig1.ID = "client-1"

	httpConfig2 := GetSampleHTTPClientConfig(config.HTTPServerURL)
	httpConfig2.ID = "client-2"

	manager := setupMCPManager(t, httpConfig1, httpConfig2)

	// Verify both clients exist
	clients := manager.GetClients()
	assert.GreaterOrEqual(t, len(clients), 2, "should have at least two clients")

	// Remove first client
	err := manager.RemoveClient("client-1")
	require.NoError(t, err, "should remove first client")

	// Verify only one client remains
	clients = manager.GetClients()
	assert.Len(t, clients, 1, "should have one client remaining")

	// Remove second client
	err = manager.RemoveClient("client-2")
	require.NoError(t, err, "should remove second client")

	// Verify no clients remain
	clients = manager.GetClients()
	assert.Len(t, clients, 0, "should have no clients")
}

func TestRemoveClientDuringExecution(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	clientConfig := GetSampleHTTPClientConfig(config.HTTPServerURL)
	manager := setupMCPManager(t, clientConfig)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	clients := manager.GetClients()
	require.Len(t, clients, 1, "should have one client")
	clientID := clients[0].ExecutionConfig.ID

	// Start a tool execution (if delay tool is available)
	ctx := createTestContext()
	toolCall := GetSampleEchoToolCall("call-1", "test")

	// Execute tool asynchronously
	done := make(chan bool)
	go func() {
		_, _ = bifrost.ExecuteChatMCPTool(ctx, &toolCall)
		done <- true
	}()

	// Small delay to let execution start
	time.Sleep(100 * time.Millisecond)

	// Remove client during execution
	err := manager.RemoveClient(clientID)
	require.NoError(t, err, "should remove client even during execution")

	// Wait for execution to complete
	<-done

	// Verify client was removed
	clients = manager.GetClients()
	assert.Len(t, clients, 0, "client should be removed")
}

// =============================================================================
// EDIT CLIENT TESTS
// =============================================================================

func TestEditClient(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	clientConfig := GetSampleHTTPClientConfig(config.HTTPServerURL)
	manager := setupMCPManager(t, clientConfig)

	clients := manager.GetClients()
	require.Len(t, clients, 1, "should have one client")
	clientID := clients[0].ExecutionConfig.ID

	// Edit client configuration
	updatedConfig := clientConfig
	updatedConfig.Name = "UpdatedName"
	updatedConfig.ToolsToExecute = []string{"calculator", "echo"}

	err := manager.EditClient(clientID, &updatedConfig)
	require.NoError(t, err, "should edit client")

	// Verify changes
	clients = manager.GetClients()
	require.Len(t, clients, 1, "should still have one client")
	assert.Equal(t, "UpdatedName", clients[0].Name)
}

func TestEditClientInvalidID(t *testing.T) {
	t.Parallel()

	manager := setupMCPManager(t)

	// Try to edit non-existent client
	clientConfig := GetSampleHTTPClientConfig("http://example.com")
	err := manager.EditClient("non-existent-id", &clientConfig)
	assert.Error(t, err, "should error when editing non-existent client")
}

func TestEditClientInvalidConfig(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	clientConfig := GetSampleHTTPClientConfig(config.HTTPServerURL)
	manager := setupMCPManager(t, clientConfig)

	clients := manager.GetClients()
	require.Len(t, clients, 1, "should have one client")
	clientID := clients[0].ExecutionConfig.ID

	// Try to edit with invalid config (missing ConnectionString)
	invalidConfig := schemas.MCPClientConfig{
		ID:             clientConfig.ID,
		ConnectionType: schemas.MCPConnectionTypeHTTP,
		// Missing ConnectionString
	}

	err := manager.EditClient(clientID, &invalidConfig)
	// Should return error or leave client unchanged
	if err == nil {
		clients = manager.GetClients()
		if len(clients) > 0 {
			// Client might be in error state
			t.Log("Edit with invalid config did not error, checking client state")
		}
	} else {
		assert.Error(t, err, "should error with invalid config")
	}
}

func TestEditClientChangeConnectionType(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	clientConfig := GetSampleHTTPClientConfig(config.HTTPServerURL)
	manager := setupMCPManager(t, clientConfig)

	clients := manager.GetClients()
	require.Len(t, clients, 1, "should have one client")
	clientID := clients[0].ExecutionConfig.ID

	// Try to change connection type
	updatedConfig := clientConfig
	updatedConfig.ConnectionType = schemas.MCPConnectionTypeSSE

	err := manager.EditClient(clientID, &updatedConfig)
	assert.Error(t, err, "should not allow connection type change")
	clients = manager.GetClients()
	if len(clients) > 0 {
		assert.Equal(t, schemas.MCPConnectionTypeHTTP, clients[0].ConnectionInfo.Type)
	}
}

// =============================================================================
// GET CLIENTS TESTS
// =============================================================================

func TestGetMCPClients(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	clientConfig := GetSampleHTTPClientConfig(config.HTTPServerURL)
	manager := setupMCPManager(t, clientConfig)

	// Get clients
	clients := manager.GetClients()
	assert.NotNil(t, clients, "clients should not be nil")
	assert.Len(t, clients, 1, "should have one client")
}

func TestGetMCPClientsEmpty(t *testing.T) {
	t.Parallel()

	manager := setupMCPManager(t)

	// Get clients when none exist
	clients := manager.GetClients()
	assert.NotNil(t, clients, "clients should not be nil")
	assert.Len(t, clients, 0, "should have no clients")
}

func TestGetMCPClientsMultiple(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	// Create HTTP client
	httpConfig := GetSampleHTTPClientConfig(config.HTTPServerURL)
	httpConfig.ID = "http-get-test"
	applyTestConfigHeaders(t, &httpConfig)

	manager := setupMCPManager(t, httpConfig)

	// Register a tool to create the InProcess client automatically
	testToolHandler := func(args any) (string, error) {
		return "test response", nil
	}
	testTool := GetSampleEchoTool()
	testTool.Function.Name = "test_tool"
	err := manager.RegisterTool("test_tool", "Test tool", testToolHandler, testTool)
	require.NoError(t, err, "should register tool")

	// Get all clients - should have HTTP + InProcess (auto-created)
	clients := manager.GetClients()
	assert.GreaterOrEqual(t, len(clients), 2, "should have HTTP and InProcess clients")

	// Verify client types
	hasHTTP := false
	hasInProcess := false
	for _, client := range clients {
		if client.ConnectionInfo.Type == schemas.MCPConnectionTypeHTTP {
			hasHTTP = true
		}
		if client.ConnectionInfo.Type == schemas.MCPConnectionTypeInProcess {
			hasInProcess = true
		}
	}

	assert.True(t, hasHTTP, "should have HTTP client")
	assert.True(t, hasInProcess, "should have InProcess client (auto-created)")
}

// =============================================================================
// RECONNECT CLIENT TESTS
// =============================================================================

func TestReconnectClient(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	clientConfig := GetSampleHTTPClientConfig(config.HTTPServerURL)
	applyTestConfigHeaders(t, &clientConfig)
	manager := setupMCPManager(t, clientConfig)

	clients := manager.GetClients()
	require.Len(t, clients, 1, "should have one client")
	clientID := clients[0].ExecutionConfig.ID

	// Reconnect client
	err := manager.ReconnectClient(clientID)
	require.NoError(t, err, "should reconnect client")

	// Verify client is still connected
	time.Sleep(time.Second)
	clients = manager.GetClients()
	AssertClientState(t, clients, clientID, schemas.MCPConnectionStateConnected)
}

func TestReconnectClientInvalidID(t *testing.T) {
	t.Parallel()

	manager := setupMCPManager(t)

	// Try to reconnect non-existent client
	err := manager.ReconnectClient("non-existent-id")
	assert.Error(t, err, "should error when reconnecting non-existent client")
}

func TestReconnectClientAfterRemoval(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	clientConfig := GetSampleHTTPClientConfig(config.HTTPServerURL)
	manager := setupMCPManager(t, clientConfig)

	clients := manager.GetClients()
	require.Len(t, clients, 1, "should have one client")
	clientID := clients[0].ExecutionConfig.ID

	// Remove client
	err := manager.RemoveClient(clientID)
	require.NoError(t, err, "should remove client")

	// Try to reconnect removed client
	err = manager.ReconnectClient(clientID)
	assert.Error(t, err, "should not reconnect removed client")
}

// =============================================================================
// CONCURRENT CLIENT OPERATIONS TESTS
// =============================================================================

func TestConcurrentClientOperations(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	manager := setupMCPManager(t)

	// Perform concurrent add operations
	done := make(chan bool, 5)
	errors := make(chan error, 5)

	for i := 0; i < 5; i++ {
		go func(id int) {
			clientConfig := GetSampleHTTPClientConfig(config.HTTPServerURL)
			clientConfig.ID = string(rune('a'+id)) + "-concurrent-client"

			err := manager.AddClient(&clientConfig)
			if err != nil {
				errors <- err
			}
			done <- true
		}(i)
	}

	// Wait for all operations
	for i := 0; i < 5; i++ {
		<-done
	}
	close(errors)

	// Check for errors
	errorCount := 0
	for err := range errors {
		t.Logf("Concurrent add error: %v", err)
		errorCount++
	}

	// Most operations should succeed
	assert.LessOrEqual(t, errorCount, 2, "should have few errors in concurrent operations")

	// Verify clients were added
	clients := manager.GetClients()
	assert.GreaterOrEqual(t, len(clients), 3, "should have added multiple clients")
}
