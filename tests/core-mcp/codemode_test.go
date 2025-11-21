package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// A. Basic Execution Tests
// ============================================================================

func TestSimpleExpression(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.SimpleExpression,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, nil, "")
	assertResultContains(t, result, "completed successfully")
}

func TestSimpleString(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.SimpleString,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, nil, "")
}

func TestVariableAssignment(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.VariableAssignment,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, nil, "")
}

func TestConsoleLogging(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.ConsoleLogging,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, []string{"test"}, "")
	assertResultContains(t, result, "Console output")
}

func TestReturnValues(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.ExplicitReturn,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, nil, "")
}

func TestAutoReturn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.AutoReturnExpression,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, nil, "")
}

// ============================================================================
// B. MCP Tool Call Tests
// ============================================================================

func TestSingleToolCall(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": `BifrostClient.echo({message: "hello"}).then(result => { console.log(result); return result; })`,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, []string{"hello"}, "")
}

func TestToolCallWithPromise(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.ToolCallWithPromise,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, []string{"test"}, "")
}

func TestToolCallChain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.ToolCallChain,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, nil, "")
}

func TestToolCallErrorHandling(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.ToolCallErrorHandling,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, []string{"handled"}, "")
}

func TestToolCallWithComplexArgs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.ToolCallWithComplexArgs,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, nil, "")
}

// ============================================================================
// C. Import/Export Stripping Tests
// ============================================================================

func TestImportStatementStripping(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.ImportStatement,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, nil, "")
	assertResultContains(t, result, "Imports stripped: Yes")
}

func TestExportStatementStripping(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.ExportStatement,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, nil, "")
	assertResultContains(t, result, "Imports stripped: Yes")
}

func TestMultipleImportExportStripping(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.MultipleImportExport,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, nil, "")
	assertResultContains(t, result, "Imports stripped: Yes")
}

func TestImportExportWithComments(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.ImportExportWithComments,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, nil, "")
}

// ============================================================================
// D. Expression Analysis & Auto-Return Tests
// ============================================================================

func TestFunctionCallAutoReturn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.FunctionCallExpression,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, nil, "")
}

func TestPromiseChainAutoReturn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.PromiseChainExpression,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, nil, "")
}

func TestObjectLiteralAutoReturn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.ObjectLiteralExpression,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, nil, "")
}

func TestAssignmentNoAutoReturn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.AssignmentStatement,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	// Assignment statements don't return values, so result should be nil or undefined
	assertExecutionResult(t, result, true, nil, "")
}

func TestControlFlowNoAutoReturn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.ControlFlowStatement,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, nil, "")
}

func TestTopLevelReturnPreserved(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.TopLevelReturn,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, nil, "")
}

// ============================================================================
// E. Error Handling Tests
// ============================================================================

func TestUndefinedVariableError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.UndefinedVariable,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr) // Tool execution succeeds, but code has runtime error
	assertExecutionResult(t, result, false, nil, "runtime")
	assertErrorHints(t, result, []string{"is not defined"})
}

func TestUndefinedServerError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.UndefinedServer,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, false, nil, "runtime")
	assertErrorHints(t, result, []string{"is not defined"})
}

func TestUndefinedToolError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.UndefinedTool,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, false, nil, "runtime")
	// Check for either error message format
	require.NotNil(t, result)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)
	responseText := *result.Content.ContentStr
	assert.True(t, strings.Contains(responseText, "nonexistentTool") || strings.Contains(responseText, "not a function") || strings.Contains(responseText, "no member"), "Response should mention the undefined tool")
}

func TestToolCallErrorPropagation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": `BifrostClient.error_tool({}).then(result => result).catch(err => { throw err; })`,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, false, nil, "runtime")
}

func TestSyntaxError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.SyntaxError,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, false, nil, "runtime")
}

func TestRuntimeError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.RuntimeError,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, false, nil, "runtime")
}

// ============================================================================
// F. Edge Cases & Complex Scenarios
// ============================================================================

func TestNestedPromiseChains(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.NestedPromiseChains,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, nil, "")
}

func TestPromiseErrorHandling(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.PromiseErrorHandling,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, nil, "")
}

func TestComplexDataStructures(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.ComplexDataStructures,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, nil, "")
}

func TestMultiLineExpressions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.MultiLineExpression,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, nil, "")
}

func TestEmptyCode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.EmptyCode,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	// Empty code should return an error now that we require non-empty strings
	require.NotNil(t, bifrostErr)
	require.Nil(t, result)
	assert.Contains(t, bifrost.GetErrorMessage(bifrostErr), "code parameter is required and must be a non-empty string")
}

func TestCommentsOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.CommentsOnly,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, nil, "")
}

func TestFunctionDefinition(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.FunctionDefinition,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, nil, "")
}

// ============================================================================
// G. Environment & Sandbox Tests
// ============================================================================

func TestNoBrowserAPIs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": `typeof fetch === "undefined"`,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, nil, "")
	// Should return true (fetch is undefined)
}

func TestNoNodeAPIs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": `typeof require === "undefined"`,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, nil, "")
	// Should return true (require is undefined)
}

func TestNoAsyncAwait(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	// async/await syntax should cause a syntax error in goja
	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.AsyncAwaitTest,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	// Should fail with syntax error
	assertExecutionResult(t, result, false, nil, "runtime")
}

func TestEnvironmentObject(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": CodeFixtures.EnvironmentTest,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, nil, "")
	assertResultContains(t, result, "BifrostClient")
}

func TestServerKeysAvailable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": `typeof BifrostClient !== "undefined"`,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, nil, "")
	// Should return true (BifrostClient exists)
}

// ============================================================================
// H. Long-Running & Performance Tests
// ============================================================================

func TestLongRunningCode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	// Use slow_tool with reasonable delay
	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": `BifrostClient.slow_tool({delay_ms: 50}).then(result => result)`,
	})

	start := time.Now()
	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	duration := time.Since(start)

	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, nil, "")
	assert.GreaterOrEqual(t, duration, 50*time.Millisecond, "Should take at least 50ms")
}

func TestToolCallWithUnderscores(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	// Test tool with underscores (get_data)
	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": `BifrostClient.get_data({}).then(result => result)`,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, nil, "")
}

func TestMCPToolCallAutoReturn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	// MCP tool calls should get auto-return even without explicit return
	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": `BifrostClient.echo({message: "test"}).then(result => result)`,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, nil, "")
}

func TestErrorHintsForFetch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": `fetch("https://example.com")`,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, false, nil, "runtime")
	assertErrorHints(t, result, []string{"fetch", "not available", "MCP tools"})
}

func TestErrorHintsForSetTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": `setTimeout(() => {}, 100)`,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, false, nil, "runtime")
	assertErrorHints(t, result, []string{"setTimeout", "not available"})
}

func TestStringManipulation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": `"hello".toUpperCase() + " " + "world".toLowerCase()`,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, nil, "")
}

func TestArithmeticOperations(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": `(10 + 5) * 2 - 3`,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, nil, "")
}

func TestConditionalLogic(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": `true ? "yes" : "no"`,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, nil, "")
}

func TestLoops(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	err = registerTestTools(b)
	require.NoError(t, err)

	toolCall := createToolCall("executeToolCode", map[string]interface{}{
		"code": `var sum = 0; for (var i = 0; i < 5; i++) { sum += i; } sum`,
	})

	result, bifrostErr := b.ExecuteMCPTool(ctx, toolCall)
	requireNoBifrostError(t, bifrostErr)
	assertExecutionResult(t, result, true, nil, "")
}
