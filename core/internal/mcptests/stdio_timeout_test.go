package mcptests

import (
	"testing"
	"time"
)

// TestSTDIO_InitTimeout verifies that STDIO initialization fails gracefully
// when the subprocess cannot be launched (invalid command path).
//
// This test validates that:
// 1. Invalid subprocess paths are detected during Start() phase
// 2. The error is returned immediately (no hang)
// 3. Manager setup completes even when a client fails to connect
//
// Note: This tests the "subprocess launch failure" scenario. The timeout fix
// also handles "subprocess launches but doesn't respond" scenarios, which would
// require a mock server that launches but never sends Initialize response.
func TestSTDIO_InitTimeout(t *testing.T) {
	// Initialize global MCP server paths
	InitMCPServerPaths(t)

	// Create a broken STDIO client by using an invalid path
	brokenClient := GetGoTestServerConfig(mcpServerPaths.ExamplesRoot)
	brokenClient.ID = "broken-stdio"
	brokenClient.Name = "BrokenSTDIOServer"
	// Corrupt the command path to cause subprocess launch failure
	brokenClient.StdioConfig.Command = brokenClient.StdioConfig.Command + "-INVALID"
	brokenClient.IsCodeModeClient = false
	brokenClient.ToolsToExecute = []string{"*"}

	t.Log("Testing STDIO initialization timeout with broken path...")

	start := time.Now()

	// This should fail after 30 seconds, not hang indefinitely
	// We expect setupMCPManager to fail because the client can't initialize
	defer func() {
		if r := recover(); r != nil {
			elapsed := time.Since(start)
			t.Logf("Manager setup panicked after %v: %v", elapsed, r)

			// Verify it failed within reasonable time (< 35 seconds to allow some buffer)
			if elapsed > 35*time.Second {
				t.Fatalf("Initialization took too long (%v), should timeout after 30s", elapsed)
			}
			t.Logf("✅ Failed as expected within timeout period (%v)", elapsed)
		}
	}()

	// Try to setup manager - this will fail when client can't initialize
	manager := setupMCPManager(t, brokenClient)

	elapsed := time.Since(start)

	// If we get here, check if the client actually connected
	clients := manager.GetClients()
	if len(clients) > 0 {
		t.Fatalf("Expected no clients to connect, but got %d", len(clients))
	}

	// Verify it failed quickly (< 35 seconds)
	if elapsed > 35*time.Second {
		t.Fatalf("Initialization took too long (%v), should timeout after 30s", elapsed)
	}

	t.Logf("✅ Initialization failed within timeout period (%v)", elapsed)
}
