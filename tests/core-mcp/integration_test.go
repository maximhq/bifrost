package mcp

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFullWorkflowListToolFilesReadToolFileExecuteToolCode(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	// Tools are already registered in setupTestBifrostWithCodeMode

	// Step 1: List tool files
	listCall := createToolCall("listToolFiles", map[string]interface{}{})
	result, bifrostErr := b.ExecuteChatMCPTool(ctx, listCall)
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)

	responseText := *result.Content.ContentStr
	assert.Contains(t, responseText, "BifrostClient.d.ts", "Should list BifrostClient")

	// Step 2: Read tool file
	readCall := createToolCall("readToolFile", map[string]interface{}{
		"fileName": "BifrostClient.d.ts",
	})
	result, bifrostErr = b.ExecuteChatMCPTool(ctx, readCall)
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)

	responseText = *result.Content.ContentStr
	assert.Contains(t, responseText, "interface", "Should contain interface definitions")
	assert.Contains(t, responseText, "echo", "Should contain echo tool")

	// Step 3: Execute code using the discovered tools
	executeCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.CodeCallingCodeModeTool,
	})
	result, bifrostErr = b.ExecuteChatMCPTool(ctx, executeCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, nil, "")
}

func TestMultipleCodeModeClientsWithDifferentAutoExecuteConfigs(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrost(ctx)
	require.NoError(t, err)

	err = registerTestTools(b)
	require.NoError(t, err)

	// Configure bifrostInternal with mixed auto-execute
	err = b.EditMCPClient("bifrostInternal", schemas.MCPClientConfig{
		IsCodeModeClient:   true,
		ToolsToExecute:     []string{"*"},
		ToolsToAutoExecute: []string{"echo", "add"}, // multiply not auto-execute
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
	assert.True(t, canAutoExecuteTool("echo", bifrostClient.Config))
	assert.True(t, canAutoExecuteTool("add", bifrostClient.Config))
	assert.False(t, canAutoExecuteTool("multiply", bifrostClient.Config))
}

func TestToolFilteringWithCodeMode(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrost(ctx)
	require.NoError(t, err)

	err = registerTestTools(b)
	require.NoError(t, err)

	// Configure specific tools only
	err = b.EditMCPClient("bifrostInternal", schemas.MCPClientConfig{
		IsCodeModeClient:   true,
		ToolsToExecute:     []string{"echo", "add"}, // Only these tools available
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
	assert.Contains(t, bifrostClient.Config.ToolsToExecute, "add")
	assert.NotContains(t, bifrostClient.Config.ToolsToExecute, "multiply")
}

func TestCodeModeAndNonCodeModeToolsInSameRequest(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrost(ctx)
	require.NoError(t, err)

	err = registerTestTools(b)
	require.NoError(t, err)

	// Set bifrostInternal to code mode
	err = b.EditMCPClient("bifrostInternal", schemas.MCPClientConfig{
		IsCodeModeClient:   true,
		ToolsToExecute:     []string{"*"},
		ToolsToAutoExecute: []string{"*"},
	})
	require.NoError(t, err)

	// Code mode tools should be available
	listCall := createToolCall("listToolFiles", map[string]interface{}{})
	result, bifrostErr := b.ExecuteChatMCPTool(ctx, listCall)
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result)

	// Verify direct tools are not exposed for code-mode clients
	// Code mode clients expose tools via executeToolCode, not as direct tool calls
	echoCall := createToolCall("echo", map[string]interface{}{
		"message": "test",
	})
	_, bifrostErr = b.ExecuteChatMCPTool(ctx, echoCall)
	require.NotNil(t, bifrostErr, "Direct tool call should fail for code-mode client")
	assert.Contains(t, bifrostErr.Error.Message, "not available", "Error should indicate tool is not available")
}

func TestComplexCodeExecutionWithMultipleToolCalls(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	// Tools are already registered in setupTestBifrostWithCodeMode

	// Test complex code with multiple tool calls
	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.ToolCallChain,
	})

	result, bifrostErr := b.ExecuteChatMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, nil, "")
}

func TestCodeExecutionWithErrorHandling(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	// Tools are already registered in setupTestBifrostWithCodeMode

	// Test code with error handling
	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.ToolCallErrorHandling,
	})

	result, bifrostErr := b.ExecuteChatMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, nil, "")
	assertResultContains(t, result, "handled")
}

func TestCodeExecutionWithAsyncAwait(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	// Tools are already registered in setupTestBifrostWithCodeMode

	// Test async/await syntax
	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.AsyncAwaitTest,
	})

	result, bifrostErr := b.ExecuteChatMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, nil, "")
}

func TestLongCodeExecution(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	// Tools are already registered in setupTestBifrostWithCodeMode

	// Test long and complex code execution
	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.LongCodeExecution,
	})

	result, bifrostErr := b.ExecuteChatMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, nil, "")
}
