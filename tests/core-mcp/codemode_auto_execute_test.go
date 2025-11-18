package mcp

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

func TestExecuteToolCodeWithAutoExecuteTool(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	// Tools are already registered in setupTestBifrostWithCodeMode

	// Configure echo as auto-execute - preserve existing config
	clients, err := b.GetMCPClients()
	require.NoError(t, err)
	var currentConfig *schemas.MCPClientConfig
	for _, client := range clients {
		if client.Config.ID == "bifrostInternal" {
			currentConfig = &client.Config
			break
		}
	}
	require.NotNil(t, currentConfig)

	err = b.EditMCPClient("bifrostInternal", schemas.MCPClientConfig{
		ID:                 currentConfig.ID,
		Name:               currentConfig.Name,
		ConnectionType:     currentConfig.ConnectionType,
		IsCodeModeClient:   currentConfig.IsCodeModeClient,
		ToolsToExecute:     []string{"*"},
		ToolsToAutoExecute: []string{"echo"},
	})
	require.NoError(t, err)

	// Test executeToolCode with code calling auto-execute tool
	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.CodeWithAutoExecuteTool,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, nil, "")
}

func TestExecuteToolCodeWithNonAutoExecuteTool(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	// Tools are already registered in setupTestBifrostWithCodeMode

	// Configure multiply as non-auto-execute - preserve existing config
	clients, err := b.GetMCPClients()
	require.NoError(t, err)
	var currentConfig *schemas.MCPClientConfig
	for _, client := range clients {
		if client.Config.ID == "bifrostInternal" {
			currentConfig = &client.Config
			break
		}
	}
	require.NotNil(t, currentConfig)

	err = b.EditMCPClient("bifrostInternal", schemas.MCPClientConfig{
		ID:                 currentConfig.ID,
		Name:               currentConfig.Name,
		ConnectionType:     currentConfig.ConnectionType,
		IsCodeModeClient:   currentConfig.IsCodeModeClient,
		ToolsToExecute:     []string{"*"},
		ToolsToAutoExecute: []string{"echo"}, // multiply not in auto-execute
	})
	require.NoError(t, err)

	// Test executeToolCode with code calling non-auto-execute tool
	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.CodeWithNonAutoExecuteTool,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, nil, "")
}

func TestExecuteToolCodeWithMixedAutoExecute(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	// Tools are already registered in setupTestBifrostWithCodeMode

	// Configure echo as auto-execute, multiply as non-auto-execute - preserve existing config
	clients, err := b.GetMCPClients()
	require.NoError(t, err)
	var currentConfig *schemas.MCPClientConfig
	for _, client := range clients {
		if client.Config.ID == "bifrostInternal" {
			currentConfig = &client.Config
			break
		}
	}
	require.NotNil(t, currentConfig)

	err = b.EditMCPClient("bifrostInternal", schemas.MCPClientConfig{
		ID:                 currentConfig.ID,
		Name:               currentConfig.Name,
		ConnectionType:     currentConfig.ConnectionType,
		IsCodeModeClient:   currentConfig.IsCodeModeClient,
		ToolsToExecute:     []string{"*"},
		ToolsToAutoExecute: []string{"echo"}, // multiply not in auto-execute
	})
	require.NoError(t, err)

	// Test executeToolCode with code calling mixed tools
	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.CodeWithMixedAutoExecute,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, nil, "")
}

func TestExecuteToolCodeWithNoToolCalls(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	// Tools are already registered in setupTestBifrostWithCodeMode

	// Test executeToolCode with no tool calls
	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.CodeWithNoToolCalls,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, nil, "")
}

func TestExecuteToolCodeWithListToolFiles(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	// Tools are already registered in setupTestBifrostWithCodeMode

	// listToolFiles should always be auto-executable
	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.CodeWithListToolFiles,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result)

	// listToolFiles and readToolFile are code mode meta-tools and cannot be called from within executeToolCode
	// They're only available as direct tool calls, not from within code execution
	// So this will fail with a runtime error
	assertExecutionResult(t, result, false, nil, "runtime")
}

func TestExecuteToolCodeWithReadToolFile(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	// Tools are already registered in setupTestBifrostWithCodeMode

	// readToolFile should always be auto-executable
	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.CodeWithReadToolFile,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result)

	// listToolFiles and readToolFile are code mode meta-tools and cannot be called from within executeToolCode
	// They're only available as direct tool calls, not from within code execution
	// So this will fail with a runtime error
	assertExecutionResult(t, result, false, nil, "runtime")
}

func TestExecuteToolCodeWithUndefinedServer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	// Tools are already registered in setupTestBifrostWithCodeMode

	// Test executeToolCode with undefined server
	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.CodeWithUndefinedServer,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	// Should fail with runtime error
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result)
	assertExecutionResult(t, result, false, nil, "runtime")
}

func TestExecuteToolCodeWithUndefinedTool(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	// Tools are already registered in setupTestBifrostWithCodeMode

	// Test executeToolCode with undefined tool
	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.CodeWithUndefinedTool,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	// Should fail with runtime error
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result)
	assertExecutionResult(t, result, false, nil, "runtime")
}
