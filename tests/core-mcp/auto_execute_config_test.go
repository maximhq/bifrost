package mcp

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToolInToolsToExecuteButNotInToolsToAutoExecute(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrost(ctx)
	require.NoError(t, err)

	err = registerTestTools(b)
	require.NoError(t, err)

	// Configure echo in ToolsToExecute but not in ToolsToAutoExecute
	err = b.EditMCPClient("bifrostInternal", schemas.MCPClientConfig{
		ToolsToExecute:     []string{"echo"},
		ToolsToAutoExecute: []string{}, // Empty - no auto-execute
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
	assert.Empty(t, bifrostClient.Config.ToolsToAutoExecute)
	assert.False(t, canAutoExecuteTool("echo", bifrostClient.Config), "echo should not be auto-executable")
}

func TestToolInBothToolsToExecuteAndToolsToAutoExecute(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrost(ctx)
	require.NoError(t, err)

	err = registerTestTools(b)
	require.NoError(t, err)

	// Configure echo in both lists
	err = b.EditMCPClient("bifrostInternal", schemas.MCPClientConfig{
		ToolsToExecute:     []string{"echo"},
		ToolsToAutoExecute: []string{"echo"},
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
	assert.Contains(t, bifrostClient.Config.ToolsToAutoExecute, "echo")
	assert.True(t, canAutoExecuteTool("echo", bifrostClient.Config), "echo should be auto-executable")
}

func TestToolInToolsToAutoExecuteButNotInToolsToExecute(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrost(ctx)
	require.NoError(t, err)

	err = registerTestTools(b)
	require.NoError(t, err)

	// Configure echo in ToolsToAutoExecute but not in ToolsToExecute
	err = b.EditMCPClient("bifrostInternal", schemas.MCPClientConfig{
		ToolsToExecute:     []string{"add"}, // echo not in this list
		ToolsToAutoExecute: []string{"echo"},
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
	// echo should not be auto-executable because it's not in ToolsToExecute
	assert.False(t, canAutoExecuteTool("echo", bifrostClient.Config), "echo should not be auto-executable (not in ToolsToExecute)")
}

func TestWildcardInToolsToAutoExecute(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrost(ctx)
	require.NoError(t, err)

	err = registerTestTools(b)
	require.NoError(t, err)

	// Configure wildcard in ToolsToAutoExecute
	err = b.EditMCPClient("bifrostInternal", schemas.MCPClientConfig{
		ToolsToExecute:     []string{"*"},
		ToolsToAutoExecute: []string{"*"},
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
	assert.Contains(t, bifrostClient.Config.ToolsToAutoExecute, "*")
	assert.True(t, canAutoExecuteTool("echo", bifrostClient.Config), "echo should be auto-executable with wildcard")
	assert.True(t, canAutoExecuteTool("add", bifrostClient.Config), "add should be auto-executable with wildcard")
}

func TestEmptyToolsToAutoExecute(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrost(ctx)
	require.NoError(t, err)

	err = registerTestTools(b)
	require.NoError(t, err)

	// Configure empty ToolsToAutoExecute
	err = b.EditMCPClient("bifrostInternal", schemas.MCPClientConfig{
		ToolsToExecute:     []string{"*"},
		ToolsToAutoExecute: []string{}, // Empty - no auto-execute
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
	assert.Empty(t, bifrostClient.Config.ToolsToAutoExecute)
	assert.False(t, canAutoExecuteTool("echo", bifrostClient.Config), "echo should not be auto-executable")
}

func TestNilToolsToAutoExecute(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrost(ctx)
	require.NoError(t, err)

	err = registerTestTools(b)
	require.NoError(t, err)

	// Configure nil ToolsToAutoExecute (omitted)
	err = b.EditMCPClient("bifrostInternal", schemas.MCPClientConfig{
		ToolsToExecute: []string{"*"},
		// ToolsToAutoExecute omitted (nil)
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
	// nil should be treated as empty
	if bifrostClient.Config.ToolsToAutoExecute == nil {
		assert.False(t, canAutoExecuteTool("echo", bifrostClient.Config), "echo should not be auto-executable (nil treated as empty)")
	} else {
		assert.Empty(t, bifrostClient.Config.ToolsToAutoExecute)
		assert.False(t, canAutoExecuteTool("echo", bifrostClient.Config), "echo should not be auto-executable")
	}
}

func TestMultipleToolsWithMixedAutoExecuteConfigs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrost(ctx)
	require.NoError(t, err)

	err = registerTestTools(b)
	require.NoError(t, err)

	// Configure mixed: echo auto-execute, add non-auto-execute
	err = b.EditMCPClient("bifrostInternal", schemas.MCPClientConfig{
		ToolsToExecute:     []string{"echo", "add", "multiply"},
		ToolsToAutoExecute: []string{"echo", "multiply"}, // add not in auto-execute
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
	assert.True(t, canAutoExecuteTool("echo", bifrostClient.Config), "echo should be auto-executable")
	assert.False(t, canAutoExecuteTool("add", bifrostClient.Config), "add should not be auto-executable")
	assert.True(t, canAutoExecuteTool("multiply", bifrostClient.Config), "multiply should be auto-executable")
}

func TestToolsToExecuteEmptyList(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrost(ctx)
	require.NoError(t, err)

	err = registerTestTools(b)
	require.NoError(t, err)

	// Configure empty ToolsToExecute
	err = b.EditMCPClient("bifrostInternal", schemas.MCPClientConfig{
		ToolsToExecute:     []string{}, // Empty - no tools allowed
		ToolsToAutoExecute: []string{"*"},
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
	assert.Empty(t, bifrostClient.Config.ToolsToExecute)
	// Even with wildcard in ToolsToAutoExecute, tools not in ToolsToExecute should not be auto-executable
	assert.False(t, canAutoExecuteTool("echo", bifrostClient.Config), "echo should not be auto-executable (not in ToolsToExecute)")
}

func TestToolsToExecuteNil(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrost(ctx)
	require.NoError(t, err)

	err = registerTestTools(b)
	require.NoError(t, err)

	// Configure nil ToolsToExecute (omitted)
	err = b.EditMCPClient("bifrostInternal", schemas.MCPClientConfig{
		// ToolsToExecute omitted (nil)
		ToolsToAutoExecute: []string{"*"},
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
	// nil ToolsToExecute should be treated as empty
	if bifrostClient.Config.ToolsToExecute == nil {
		assert.False(t, canAutoExecuteTool("echo", bifrostClient.Config), "echo should not be auto-executable (nil ToolsToExecute treated as empty)")
	} else {
		assert.Empty(t, bifrostClient.Config.ToolsToExecute)
		assert.False(t, canAutoExecuteTool("echo", bifrostClient.Config), "echo should not be auto-executable")
	}
}
