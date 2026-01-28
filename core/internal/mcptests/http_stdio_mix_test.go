package mcptests

import (
	"testing"
)

// TestHTTP_STDIO_Mix tests HTTP and STDIO clients together to verify
// whether parallel initialization causes a deadlock
func TestHTTP_STDIO_Mix(t *testing.T) {
	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	// Initialize global MCP server paths
	InitMCPServerPaths(t)

	// Create HTTP client
	httpClient := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)

	// Create one STDIO client (GoTest)
	goTestClient := GetGoTestServerConfig(mcpServerPaths.ExamplesRoot)
	goTestClient.ID = "gotest"
	goTestClient.IsCodeModeClient = true
	goTestClient.ToolsToExecute = []string{"*"}

	t.Log("Setting up manager with HTTP + STDIO clients...")

	// This should trigger the deadlock if the hypothesis is correct
	manager := setupMCPManager(t, httpClient, goTestClient)

	// Verify both connected
	clients := manager.GetClients()
	t.Logf("Connected clients: %d", len(clients))

	for _, client := range clients {
		t.Logf("  - %s (%s)", client.Name, client.ExecutionConfig.ConnectionType)
	}

	if len(clients) != 2 {
		t.Fatalf("Expected 2 clients, got %d", len(clients))
	}

	t.Log("✅ HTTP + STDIO clients connected successfully")
}
