package mcp

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSingleCodeModeClient(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	// Tools are already registered in setupTestBifrostWithCodeMode

	clients, err := b.GetMCPClients()
	require.NoError(t, err)
	require.NotEmpty(t, clients)

	// Find bifrostInternal client
	var bifrostClient *schemas.MCPClient
	for i := range clients {
		if clients[i].Config.ID == "bifrostInternal" {
			bifrostClient = &clients[i]
			break
		}
	}

	require.NotNil(t, bifrostClient, "bifrostInternal client should exist")
	assert.True(t, bifrostClient.Config.IsCodeModeClient, "bifrostInternal should be code mode client")
	assert.Equal(t, schemas.MCPConnectionStateConnected, bifrostClient.State)
}

func TestSingleNonCodeModeClient(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrost(ctx)
	require.NoError(t, err)

	// Note: For in-process clients, we need to register tools first
	err = registerTestTools(b)
	require.NoError(t, err)

	// Update bifrostInternal to be non-code mode
	err = b.EditMCPClient("bifrostInternal", schemas.MCPClientConfig{
		IsCodeModeClient: false,
	})
	require.NoError(t, err)

	clients, err := b.GetMCPClients()
	require.NoError(t, err)
	require.NotEmpty(t, clients)

	var bifrostClient *schemas.MCPClient
	for i := range clients {
		if clients[i].Config.ID == "bifrostInternal" {
			bifrostClient = &clients[i]
			break
		}
	}

	require.NotNil(t, bifrostClient)
	assert.False(t, bifrostClient.Config.IsCodeModeClient, "bifrostInternal should be non-code mode client")
}

func TestMultipleCodeModeClients(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrost(ctx)
	require.NoError(t, err)

	err = registerTestTools(b)
	require.NoError(t, err)

	// Set bifrostInternal to code mode
	err = b.EditMCPClient("bifrostInternal", schemas.MCPClientConfig{
		IsCodeModeClient: true,
	})
	require.NoError(t, err)

	clients, err := b.GetMCPClients()
	require.NoError(t, err)

	codeModeCount := 0
	for _, client := range clients {
		if client.Config.IsCodeModeClient {
			codeModeCount++
		}
	}

	assert.GreaterOrEqual(t, codeModeCount, 1, "Should have at least one code mode client")
}

func TestMultipleNonCodeModeClients(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrost(ctx)
	require.NoError(t, err)

	err = registerTestTools(b)
	require.NoError(t, err)

	// Set bifrostInternal to non-code mode
	err = b.EditMCPClient("bifrostInternal", schemas.MCPClientConfig{
		IsCodeModeClient: false,
	})
	require.NoError(t, err)

	clients, err := b.GetMCPClients()
	require.NoError(t, err)

	nonCodeModeCount := 0
	for _, client := range clients {
		if !client.Config.IsCodeModeClient {
			nonCodeModeCount++
		}
	}

	assert.GreaterOrEqual(t, nonCodeModeCount, 1, "Should have at least one non-code mode client")
}

func TestMixedCodeModeAndNonCodeModeClients(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrost(ctx)
	require.NoError(t, err)

	err = registerTestTools(b)
	require.NoError(t, err)

	// Set bifrostInternal to code mode
	err = b.EditMCPClient("bifrostInternal", schemas.MCPClientConfig{
		IsCodeModeClient: true,
	})
	require.NoError(t, err)

	clients, err := b.GetMCPClients()
	require.NoError(t, err)

	codeModeCount := 0

	for _, client := range clients {
		if client.Config.IsCodeModeClient {
			codeModeCount++
		}
	}

	// At minimum, we should have bifrostInternal as code mode
	assert.GreaterOrEqual(t, codeModeCount, 1, "Should have at least one code mode client")
}

func TestClientConnectionStates(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	// Tools are already registered in setupTestBifrostWithCodeMode

	clients, err := b.GetMCPClients()
	require.NoError(t, err)
	require.NotEmpty(t, clients)

	// All clients should be connected
	for _, client := range clients {
		assert.Equal(t, schemas.MCPConnectionStateConnected, client.State, "Client %s should be connected", client.Config.ID)
	}
}

func TestClientWithNoTools(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrost(ctx)
	require.NoError(t, err)

	// Don't register any tools - bifrostInternal client should still exist but with no tools
	clients, err := b.GetMCPClients()
	require.NoError(t, err)

	// bifrostInternal client is created when MCP is initialized, but won't have tools until registered
	// This test verifies the client exists even without tools
	assert.NotNil(t, clients, "Clients list should exist")

	// Find bifrostInternal client
	var bifrostClient *schemas.MCPClient
	for i := range clients {
		if clients[i].Config.ID == "bifrostInternal" {
			bifrostClient = &clients[i]
			break
		}
	}

	require.NotNil(t, bifrostClient, "bifrostInternal client should exist")
	assert.Empty(t, bifrostClient.Tools, "bifrostInternal client should have no tools")
}

func TestClientWithEmptyToolLists(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrost(ctx)
	require.NoError(t, err)

	err = registerTestTools(b)
	require.NoError(t, err)

	// Set ToolsToExecute to empty list
	err = b.EditMCPClient("bifrostInternal", schemas.MCPClientConfig{
		ToolsToExecute: []string{},
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
	assert.Equal(t, []string{}, bifrostClient.Config.ToolsToExecute, "ToolsToExecute should be empty")
}

func TestClientConfigUpdate(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrost(ctx)
	require.NoError(t, err)

	err = registerTestTools(b)
	require.NoError(t, err)

	// Initially, bifrostInternal should not be code mode (default)
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
	initialIsCodeMode := bifrostClient.Config.IsCodeModeClient

	// Update to code mode
	err = b.EditMCPClient("bifrostInternal", schemas.MCPClientConfig{
		IsCodeModeClient: true,
	})
	require.NoError(t, err)

	// Verify update
	clients, err = b.GetMCPClients()
	require.NoError(t, err)

	for i := range clients {
		if clients[i].Config.ID == "bifrostInternal" {
			bifrostClient = &clients[i]
			break
		}
	}

	require.NotNil(t, bifrostClient)
	assert.NotEqual(t, initialIsCodeMode, bifrostClient.Config.IsCodeModeClient, "IsCodeModeClient should have changed")
	assert.True(t, bifrostClient.Config.IsCodeModeClient, "Should now be code mode")
}

func TestClientWithToolsToExecuteWildcard(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrost(ctx)
	require.NoError(t, err)

	err = registerTestTools(b)
	require.NoError(t, err)

	// Set ToolsToExecute to wildcard
	err = b.EditMCPClient("bifrostInternal", schemas.MCPClientConfig{
		ToolsToExecute: []string{"*"},
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
	assert.Contains(t, bifrostClient.Config.ToolsToExecute, "*", "Should contain wildcard")
}

func TestClientWithSpecificToolsToExecute(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrost(ctx)
	require.NoError(t, err)

	err = registerTestTools(b)
	require.NoError(t, err)

	// Set ToolsToExecute to specific tools
	err = b.EditMCPClient("bifrostInternal", schemas.MCPClientConfig{
		ToolsToExecute: []string{"echo", "add"},
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
	assert.Contains(t, bifrostClient.Config.ToolsToExecute, "echo")
	assert.Contains(t, bifrostClient.Config.ToolsToExecute, "add")
	assert.Len(t, bifrostClient.Config.ToolsToExecute, 2)
}
