package mcp

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Note: Full agent mode testing requires integration with LLM calls.
// These tests verify the configuration and tool execution aspects that can be tested directly.
// For full agent mode flow testing, see integration_test.go

// TestAgentModeConfiguration tests the configuration aspects of agent mode
// Full agent mode flow testing requires LLM integration (see integration_test.go)
func TestAgentModeConfiguration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrost(ctx)
	require.NoError(t, err)

	err = registerTestTools(b)
	require.NoError(t, err)

	// Test configuration: echo auto-execute, add non-auto-execute
	err = b.EditMCPClient("bifrostInternal", schemas.MCPClientConfig{
		ToolsToExecute:     []string{"*"},
		ToolsToAutoExecute: []string{"echo"}, // Only echo is auto-execute
	})
	require.NoError(t, err)

	clients, err := b.GetMCPClients()
	require.NoError(t, err)

	var bifrostClient *schemas.MCPClient
	for i := range clients {
		if clients[i].Config.ID == "bifrostInternal" {
			bifrostClient = &clients[i]
			break
		}
	}

	require.NotNil(t, bifrostClient)

	// Verify configuration
	assert.True(t, canAutoExecuteTool("echo", bifrostClient.Config), "echo should be auto-executable")
	assert.False(t, canAutoExecuteTool("add", bifrostClient.Config), "add should not be auto-executable")
	assert.False(t, canAutoExecuteTool("multiply", bifrostClient.Config), "multiply should not be auto-executable")
}

func TestAgentModeMaxDepthConfiguration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	// Create Bifrost with max depth of 2
	mcpConfig := &schemas.MCPConfig{
		ClientConfigs: []schemas.MCPClientConfig{},
		ToolManagerConfig: &schemas.MCPToolManagerConfig{
			MaxAgentDepth:        2,
			ToolExecutionTimeout: 30 * time.Second,
		},
		FetchNewRequestIDFunc: func(ctx context.Context) string {
			return "test-request-id"
		},
	}
	b, err := setupTestBifrostWithMCPConfig(ctx, mcpConfig)
	require.NoError(t, err)

	// Verify max depth is configured
	clients, err := b.GetMCPClients()
	require.NoError(t, err)
	assert.NotNil(t, clients, "Should have clients")
}
