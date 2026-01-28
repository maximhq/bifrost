package mcptests

import (
	"testing"
)

func TestHTTP_Only_Connection(t *testing.T) {
	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	// Only HTTP client
	httpClient := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)

	manager := setupMCPManager(t, httpClient)

	// Verify connected
	clients := manager.GetClients()
	if len(clients) != 1 {
		t.Fatalf("Expected 1 client, got %d", len(clients))
	}

	t.Log("✅ HTTP-only client connected successfully")
}
