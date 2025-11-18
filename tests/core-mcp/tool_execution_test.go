package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNonCodeModeToolExecution(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrost(ctx)
	require.NoError(t, err)

	err = registerTestTools(b)
	require.NoError(t, err)

	// Set bifrostInternal to non-code mode and ensure tools are available
	err = b.EditMCPClient("bifrostInternal", schemas.MCPClientConfig{
		IsCodeModeClient: false,
		ToolsToExecute:   []string{"*"}, // Allow all tools
	})
	require.NoError(t, err)

	// Test direct tool execution
	echoCall := createToolCall("echo", map[string]interface{}{
		"message": "test message",
	})
	result, bifrostErr := b.ExecuteMCPTool(ctx, echoCall)
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)

	responseText := *result.Content.ContentStr
	assert.Equal(t, "test message", responseText)
}

func TestCodeModeToolExecution(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	// Tools are already registered in setupTestBifrostWithCodeMode

	// Test executeToolCode
	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.SimpleExpression,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)
	assertExecutionResult(t, result, true, nil, "")
	assertResultContains(t, result, "completed successfully")
}

func TestCodeModeCallingCodeModeClientTools(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	// Tools are already registered in setupTestBifrostWithCodeMode

	// Test code calling code mode client tools
	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.CodeCallingCodeModeTool,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)
	assertExecutionResult(t, result, true, nil, "")
	assertResultContains(t, result, "test")
}

func TestCodeModeCallingMultipleCodeModeClients(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	// Tools are already registered in setupTestBifrostWithCodeMode

	// Test code calling tools from multiple code mode clients
	// Since we only have bifrostInternal, we'll test calling multiple tools from the same client
	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.MultipleServerToolCalls, // This calls echo and add from BifrostClient
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)
	assertExecutionResult(t, result, true, nil, "")
}

func TestListToolFilesWithNoClients(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrost(ctx)
	require.NoError(t, err)

	// Don't register tools or set code mode - should have no code mode clients
	toolCall := createToolCall("listToolFiles", map[string]interface{}{})
	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)

	// listToolFiles should still work but return empty/no servers message
	if bifrostErr == nil && result != nil {
		responseText := *result.Content.ContentStr
		assert.Contains(t, responseText, "No servers", "Should indicate no servers")
	}
}

func TestListToolFilesWithOnlyNonCodeModeClients(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
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

	// listToolFiles should not be available when no code mode clients exist
	// But if it is called, it should return empty
	toolCall := createToolCall("listToolFiles", map[string]interface{}{})
	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)

	if bifrostErr == nil && result != nil {
		responseText := *result.Content.ContentStr
		// Should indicate no servers or empty list
		assert.True(t,
			len(responseText) == 0 ||
				strings.Contains(responseText, "No servers") || strings.Contains(responseText, "servers/"),
			"Should return empty or no servers message")
	}
}

func TestListToolFilesWithCodeModeClients(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	// Tools are already registered in setupTestBifrostWithCodeMode

	toolCall := createToolCall("listToolFiles", map[string]interface{}{})
	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)

	responseText := *result.Content.ContentStr
	assert.Contains(t, responseText, "servers/", "Should list servers")
	assert.Contains(t, responseText, "BifrostClient.d.ts", "Should list BifrostClient server")
}

func TestReadToolFileForNonExistentClient(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	// Tools are already registered in setupTestBifrostWithCodeMode

	toolCall := createToolCall("readToolFile", map[string]interface{}{
		"fileName": "NonExistentClient.d.ts",
	})
	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)

	responseText := *result.Content.ContentStr
	assert.Contains(t, responseText, "No server found", "Should indicate server not found")
}

func TestReadToolFileForCodeModeClient(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	// Tools are already registered in setupTestBifrostWithCodeMode

	toolCall := createToolCall("readToolFile", map[string]interface{}{
		"fileName": "BifrostClient.d.ts",
	})
	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)

	responseText := *result.Content.ContentStr
	assert.Contains(t, responseText, "interface", "Should contain TypeScript interface declarations")
	assert.Contains(t, responseText, "echo", "Should contain echo tool definition")
}

func TestReadToolFileWithLineRange(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	// Tools are already registered in setupTestBifrostWithCodeMode

	toolCall := createToolCall("readToolFile", map[string]interface{}{
		"fileName":  "BifrostClient.d.ts",
		"startLine": float64(1),
		"endLine":   float64(10),
	})
	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)

	responseText := *result.Content.ContentStr
	assert.NotEmpty(t, responseText, "Should return content")
}
