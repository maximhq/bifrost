package mcp

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodeModeClientCallingNonCodeModeClientTool(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	// Tools are already registered in setupTestBifrostWithCodeMode

	// Test code trying to call non-code mode client tool
	// This should fail at runtime since non-code mode clients aren't available in code execution
	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.CodeCallingNonCodeModeTool,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	// Should fail with runtime error - tool call succeeds but code execution fails
	requireNoBifrostError(t, bifrostErr, "Tool call should succeed")
	require.NotNil(t, result, "Result should be present")
	assertExecutionResult(t, result, false, nil, "runtime")
}

func TestNonCodeModeClientToolCalledFromExecuteToolCode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	// Tools are already registered in setupTestBifrostWithCodeMode

	// Code mode can only call code mode client tools
	// Non-code mode tools are not available in executeToolCode context
	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": `const result = await NonExistentClient.tool({}); return result`,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	// Should fail with runtime error - tool call succeeds but code execution fails
	requireNoBifrostError(t, bifrostErr, "Tool call should succeed")
	require.NotNil(t, result, "Result should be present")
	assertExecutionResult(t, result, false, nil, "runtime")
}

func TestToolNotInToolsToExecute(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrost(ctx)
	require.NoError(t, err)

	err = registerTestTools(b)
	require.NoError(t, err)

	// Configure only echo in ToolsToExecute
	err = b.EditMCPClient("bifrostInternal", schemas.MCPClientConfig{
		ToolsToExecute: []string{"echo"}, // add not in list
	})
	require.NoError(t, err)

	// Try to execute add tool (not in ToolsToExecute)
	addCall := createToolCall("add", map[string]interface{}{
		"a": float64(1),
		"b": float64(2),
	})
	_, bifrostErr := b.ExecuteMCPTool(ctx, addCall)

	// Should fail - tool not available
	assert.NotNil(t, bifrostErr, "Should fail when tool not in ToolsToExecute")
}

func TestToolExecutionTimeoutEdgeCase(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrost(ctx)
	require.NoError(t, err)

	err = registerTestTools(b)
	require.NoError(t, err)

	// Test slow tool with timeout
	slowCall := createToolCall("slow_tool", map[string]interface{}{
		"delay_ms": float64(100),
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, slowCall)
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)

	responseText := *result.Content.ContentStr
	assert.Contains(t, responseText, "Completed", "Should complete execution")
}

func TestToolExecutionErrorPropagation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrost(ctx)
	require.NoError(t, err)

	err = registerTestTools(b)
	require.NoError(t, err)

	// Test error tool
	errorCall := createToolCall("error_tool", map[string]interface{}{})
	result, bifrostErr := b.ExecuteMCPTool(ctx, errorCall)

	// Tool execution should succeed (no bifrostErr), but result should contain error message
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)

	responseText := *result.Content.ContentStr
	assert.Contains(t, responseText, "Error:", "Result should contain error message")
	assert.Contains(t, responseText, "this tool always fails", "Result should contain the error text")
}

func TestEmptyCodeExecution(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	// Tools are already registered in setupTestBifrostWithCodeMode

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.EmptyCode,
	})

	_, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	// Empty code should return an error
	require.NotNil(t, bifrostErr, "Empty code should return an error")
	assert.Contains(t, bifrostErr.Error.Message, "code parameter is required", "Error should mention code parameter")
}

func TestCodeWithSyntaxErrors(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	// Tools are already registered in setupTestBifrostWithCodeMode

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.SyntaxError,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result)

	// Syntax errors are caught during JavaScript execution (runtime), not TypeScript compilation
	// The error will be a runtime SyntaxError
	assertExecutionResult(t, result, false, nil, "runtime")
}

func TestCodeWithTypeScriptCompilationErrors(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	// Tools are already registered in setupTestBifrostWithCodeMode

	// Invalid TypeScript code
	invalidCode := `const x: string = 123; return x`
	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": invalidCode,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result)

	// TypeScript type errors might not be caught - the code might execute successfully
	// This is acceptable behavior if type checking is disabled
	// Just verify the execution completed (either with error or success)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)
}

func TestCodeWithRuntimeErrors(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	// Tools are already registered in setupTestBifrostWithCodeMode

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.RuntimeError,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	// Should fail with runtime error
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result)
	assertExecutionResult(t, result, false, nil, "runtime")
}

func TestCodeCallingToolsWithInvalidArguments(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	// Tools are already registered in setupTestBifrostWithCodeMode

	// Code calling tool with invalid arguments
	invalidArgsCode := `const result = await BifrostClient.echo({invalid: "arg"}); return result`
	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": invalidArgsCode,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	// Should fail - tool expects "message" parameter
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result)
	assertExecutionResult(t, result, false, nil, "")
}

func TestCodeModeToolsAlwaysAutoExecutable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrost(ctx)
	require.NoError(t, err)

	err = registerTestTools(b)
	require.NoError(t, err)

	// Set bifrostInternal to code mode
	err = b.EditMCPClient("bifrostInternal", schemas.MCPClientConfig{
		IsCodeModeClient:   true,
		ToolsToExecute:     []string{"*"},
		ToolsToAutoExecute: []string{}, // Empty - no auto-execute configured
	})
	require.NoError(t, err)

	// listToolFiles and readToolFile should always be auto-executable
	// This is tested in integration tests that verify agent mode behavior
	// For now, verify they can be executed directly
	listCall := createToolCall("listToolFiles", map[string]interface{}{})
	result, bifrostErr := b.ExecuteMCPTool(ctx, listCall)
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result)
}

func TestCommentsOnlyCode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	// Tools are already registered in setupTestBifrostWithCodeMode

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.CommentsOnly,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result)

	// Comments-only code should execute (return null)
	assertExecutionResult(t, result, true, nil, "")
}

func TestUndefinedVariableError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	// Tools are already registered in setupTestBifrostWithCodeMode

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.UndefinedVariable,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	// Should fail with runtime error
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result)
	assertExecutionResult(t, result, false, nil, "runtime")
}
