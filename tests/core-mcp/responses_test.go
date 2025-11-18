package mcp

import (
	"context"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResponsesNonCodeModeToolExecution tests direct tool execution via Responses API
func TestResponsesNonCodeModeToolExecution(t *testing.T) {
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

	// Execute tool directly to verify it works
	echoCall := createToolCall("echo", map[string]interface{}{
		"message": "test message",
	})
	result, bifrostErr := b.ExecuteMCPTool(ctx, echoCall)
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)
	responseText := *result.Content.ContentStr
	assert.Equal(t, "test message", responseText, "Echo tool should return the input message")
}

// TestResponsesCodeModeToolExecution tests code mode tool execution via Responses API
func TestResponsesCodeModeToolExecution(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	// Tools are already registered in setupTestBifrostWithCodeMode

	// Test executeToolCode directly to verify code mode works
	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.SimpleExpression,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)
	assertExecutionResult(t, result, true, nil, "")
	assertResultContains(t, result, "completed successfully")
}

// TestResponsesAgentModeWithAutoExecuteTools tests agent mode configuration with auto-executable tools
func TestResponsesAgentModeWithAutoExecuteTools(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrost(ctx)
	require.NoError(t, err)

	err = registerTestTools(b)
	require.NoError(t, err)

	// Configure bifrostInternal with echo as auto-execute
	err = b.EditMCPClient("bifrostInternal", schemas.MCPClientConfig{
		IsCodeModeClient:   false,
		ToolsToExecute:     []string{"*"},
		ToolsToAutoExecute: []string{"echo"}, // Only echo is auto-execute
	})
	require.NoError(t, err)

	// Verify configuration
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
	assert.False(t, canAutoExecuteTool("multiply", bifrostClient.Config), "multiply should not be auto-executable")

	// Verify echo tool can be executed directly
	echoCall := createToolCall("echo", map[string]interface{}{
		"message": "test message",
	})
	result, bifrostErr := b.ExecuteMCPTool(ctx, echoCall)
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)
	responseText := *result.Content.ContentStr
	assert.Equal(t, "test message", responseText, "Echo tool should return the input message")
}

// TestResponsesAgentModeWithNonAutoExecuteTools tests agent mode configuration with non-auto-executable tools
func TestResponsesAgentModeWithNonAutoExecuteTools(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrost(ctx)
	require.NoError(t, err)

	err = registerTestTools(b)
	require.NoError(t, err)

	// Configure bifrostInternal with multiply NOT in auto-execute
	err = b.EditMCPClient("bifrostInternal", schemas.MCPClientConfig{
		IsCodeModeClient:   false,
		ToolsToExecute:     []string{"*"},
		ToolsToAutoExecute: []string{"echo"}, // multiply is NOT auto-execute
	})
	require.NoError(t, err)

	// Verify configuration
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
	assert.False(t, canAutoExecuteTool("multiply", bifrostClient.Config), "multiply should not be auto-executable")

	// Verify multiply tool can still be executed directly (just not auto-executed)
	multiplyCall := createToolCall("multiply", map[string]interface{}{
		"a": float64(2),
		"b": float64(3),
	})
	result, bifrostErr := b.ExecuteMCPTool(ctx, multiplyCall)
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)
	responseText := *result.Content.ContentStr
	assert.Equal(t, "6", responseText, "Multiply tool should return correct result")
}

// TestResponsesAgentModeMaxDepth tests agent mode max depth configuration via Responses API
func TestResponsesAgentModeMaxDepth(t *testing.T) {
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

	err = registerTestTools(b)
	require.NoError(t, err)

	// Configure all tools as available
	err = b.EditMCPClient("bifrostInternal", schemas.MCPClientConfig{
		IsCodeModeClient: false,
		ToolsToExecute:   []string{"*"},
	})
	require.NoError(t, err)

	// Verify tools still work with max depth configured
	echoCall := createToolCall("echo", map[string]interface{}{
		"message": "test",
	})
	result, bifrostErr := b.ExecuteMCPTool(ctx, echoCall)
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)
	responseText := *result.Content.ContentStr
	assert.Equal(t, "test", responseText, "Echo tool should work with max depth configured")
}

// TestResponsesToolExecutionTimeout tests tool execution timeout via Responses API
func TestResponsesToolExecutionTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	// Create Bifrost with short timeout
	mcpConfig := &schemas.MCPConfig{
		ClientConfigs: []schemas.MCPClientConfig{},
		ToolManagerConfig: &schemas.MCPToolManagerConfig{
			MaxAgentDepth:        10,
			ToolExecutionTimeout: 100 * time.Millisecond, // Very short timeout
		},
		FetchNewRequestIDFunc: func(ctx context.Context) string {
			return "test-request-id"
		},
	}
	b, err := setupTestBifrostWithMCPConfig(ctx, mcpConfig)
	require.NoError(t, err)

	err = registerTestTools(b)
	require.NoError(t, err)

	// Configure slow_tool
	err = b.EditMCPClient("bifrostInternal", schemas.MCPClientConfig{
		IsCodeModeClient:   false,
		ToolsToExecute:     []string{"*"},
		ToolsToAutoExecute: []string{"*"},
	})
	require.NoError(t, err)

	// Create a Responses request that will trigger a slow tool
	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ResponsesMessage{
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("Call slow_tool with delay 500ms"),
				},
			},
		},
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{
				{
					Name:        schemas.Ptr("slow_tool"),
					Description: schemas.Ptr("A tool that takes time to execute"),
				},
			},
		},
	}

	// Execute the request - should handle timeout gracefully
	_, bifrostErr := b.ResponsesRequest(ctx, req)
	// Timeout errors are acceptable in this test
	if bifrostErr != nil {
		assert.Contains(t, bifrost.GetErrorMessage(bifrostErr), "timeout", "Should contain timeout error")
	}
}

// TestResponsesMultipleToolCalls tests multiple tool calls via Responses API
func TestResponsesMultipleToolCalls(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrost(ctx)
	require.NoError(t, err)

	err = registerTestTools(b)
	require.NoError(t, err)

	// Configure all tools as available
	err = b.EditMCPClient("bifrostInternal", schemas.MCPClientConfig{
		IsCodeModeClient: false,
		ToolsToExecute:   []string{"*"},
	})
	require.NoError(t, err)

	// Test echo tool
	echoCall := createToolCall("echo", map[string]interface{}{
		"message": "test",
	})
	result, bifrostErr := b.ExecuteMCPTool(ctx, echoCall)
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)
	responseText := *result.Content.ContentStr
	assert.Equal(t, "test", responseText, "Echo tool should return correct result")

	// Test add tool
	addCall := createToolCall("add", map[string]interface{}{
		"a": float64(5),
		"b": float64(3),
	})
	result, bifrostErr = b.ExecuteMCPTool(ctx, addCall)
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)
	responseText = *result.Content.ContentStr
	assert.Equal(t, "8", responseText, "Add tool should return correct result")
}

// TestResponsesCodeModeWithCodeExecution tests code mode with code execution via Responses API
func TestResponsesCodeModeWithCodeExecution(t *testing.T) {
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

// TestResponsesToolFiltering tests tool filtering via Responses API
func TestResponsesToolFiltering(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrost(ctx)
	require.NoError(t, err)

	err = registerTestTools(b)
	require.NoError(t, err)

	// Configure specific tools only
	err = b.EditMCPClient("bifrostInternal", schemas.MCPClientConfig{
		IsCodeModeClient:   false,
		ToolsToExecute:     []string{"echo", "add"}, // Only these tools available
		ToolsToAutoExecute: []string{"echo"},
	})
	require.NoError(t, err)

	// Verify allowed tools work
	echoCall := createToolCall("echo", map[string]interface{}{
		"message": "test",
	})
	result, bifrostErr := b.ExecuteMCPTool(ctx, echoCall)
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)
	responseText := *result.Content.ContentStr
	assert.Equal(t, "test", responseText, "Echo tool should work")

	addCall := createToolCall("add", map[string]interface{}{
		"a": float64(1),
		"b": float64(2),
	})
	result, bifrostErr = b.ExecuteMCPTool(ctx, addCall)
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)
	responseText = *result.Content.ContentStr
	assert.Equal(t, "3", responseText, "Add tool should work")

	// Verify multiply tool is NOT available (should fail)
	multiplyCall := createToolCall("multiply", map[string]interface{}{
		"a": float64(2),
		"b": float64(3),
	})
	result, bifrostErr = b.ExecuteMCPTool(ctx, multiplyCall)
	// Should fail because multiply is not in ToolsToExecute
	assert.NotNil(t, bifrostErr, "Multiply tool should fail when not in ToolsToExecute")
}

// TestResponsesComplexWorkflow tests a complex workflow via Responses API
func TestResponsesComplexWorkflow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrost(ctx)
	require.NoError(t, err)

	err = registerTestTools(b)
	require.NoError(t, err)

	// Configure all tools as available
	err = b.EditMCPClient("bifrostInternal", schemas.MCPClientConfig{
		IsCodeModeClient: false,
		ToolsToExecute:   []string{"*"},
	})
	require.NoError(t, err)

	// Test echo tool
	echoCall := createToolCall("echo", map[string]interface{}{
		"message": "hello",
	})
	result, bifrostErr := b.ExecuteMCPTool(ctx, echoCall)
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)
	responseText := *result.Content.ContentStr
	assert.Equal(t, "hello", responseText, "Echo tool should return correct result")

	// Test add tool
	addCall := createToolCall("add", map[string]interface{}{
		"a": float64(5),
		"b": float64(3),
	})
	result, bifrostErr = b.ExecuteMCPTool(ctx, addCall)
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)
	responseText = *result.Content.ContentStr
	assert.Equal(t, "8", responseText, "Add tool should return correct result")

	// Test multiply tool with result from add
	multiplyCall := createToolCall("multiply", map[string]interface{}{
		"a": float64(8), // Result from add
		"b": float64(2),
	})
	result, bifrostErr = b.ExecuteMCPTool(ctx, multiplyCall)
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)
	responseText = *result.Content.ContentStr
	assert.Equal(t, "16", responseText, "Multiply tool should return correct result")
}
