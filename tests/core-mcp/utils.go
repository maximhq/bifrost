package mcp

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createToolCall creates a tool call message for testing
func createToolCall(toolName string, arguments schemas.OrderedMap) schemas.ChatAssistantMessageToolCall {
	argsJSON, _ := json.Marshal(arguments)
	argsStr := string(argsJSON)
	id := fmt.Sprintf("test-tool-call-%d", len(argsStr))
	toolType := "function"

	return schemas.ChatAssistantMessageToolCall{
		ID:   &id,
		Type: &toolType,
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      &toolName,
			Arguments: argsStr,
		},
	}
}

// createResponsesToolCall creates a tool call message for testing
func createResponsesToolCall(toolName string, arguments schemas.OrderedMap) *schemas.ResponsesToolMessage {
	argsJSON, _ := json.Marshal(arguments)
	argsStr := string(argsJSON)
	id := fmt.Sprintf("test-tool-call-%d", len(argsStr))

	return &schemas.ResponsesToolMessage{
		CallID:    &id,
		Name:      &toolName,
		Arguments: &argsStr,
	}
}

// assertResponsesExecutionResult validates execution results
func assertResponsesExecutionResult(t *testing.T, result *schemas.ResponsesMessage, expectedSuccess bool, expectedLogs []string, expectedErrorKind string) {
	require.NotNil(t, result)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)

	responseText := *result.Content.ContentStr

	if expectedSuccess {
		// Success case - should not contain error indicators (but allow console.error output)
		assert.NotContains(t, responseText, "Execution runtime error", "Response should not contain execution runtime error for successful execution")
		assert.NotContains(t, responseText, "Execution typescript error", "Response should not contain execution typescript error for successful execution")
		assert.NotContains(t, responseText, "Error:", "Response should not contain Error: prefix for successful execution")
	} else {
		// Error case - should contain error information
		assert.Contains(t, responseText, "error", "Response should contain error for failed execution")

		if expectedErrorKind != "" {
			assert.Contains(t, responseText, expectedErrorKind, "Response should contain expected error kind")
		}
	}
}

// assertExecutionResult validates execution results
func assertExecutionResult(t *testing.T, result *schemas.ChatMessage, expectedSuccess bool, expectedLogs []string, expectedErrorKind string) {
	require.NotNil(t, result)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)

	responseText := *result.Content.ContentStr

	if expectedSuccess {
		// Success case - should not contain error indicators (but allow console.error output)
		assert.NotContains(t, responseText, "Execution runtime error", "Response should not contain execution runtime error for successful execution")
		assert.NotContains(t, responseText, "Execution typescript error", "Response should not contain execution typescript error for successful execution")
		assert.NotContains(t, responseText, "Error:", "Response should not contain Error: prefix for successful execution")

		// Check logs if expected
		if len(expectedLogs) > 0 {
			for _, expectedLog := range expectedLogs {
				assert.Contains(t, responseText, expectedLog, "Response should contain expected log")
			}
		}
	} else {
		// Error case - should contain error information
		assert.Contains(t, responseText, "error", "Response should contain error for failed execution")

		if expectedErrorKind != "" {
			assert.Contains(t, responseText, expectedErrorKind, "Response should contain expected error kind")
		}
	}
}

// assertResultContains validates that the result contains specific text
func assertResultContains(t *testing.T, result *schemas.ChatMessage, expectedText string) {
	require.NotNil(t, result)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)

	responseText := *result.Content.ContentStr
	assert.Contains(t, responseText, expectedText, "Response should contain expected text")
}

// assertResponsesResultContains validates that the result contains specific text
func assertResponsesResultContains(t *testing.T, result *schemas.ResponsesMessage, expectedText string) {
	require.NotNil(t, result)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)

	responseText := *result.Content.ContentStr
	assert.Contains(t, responseText, expectedText, "Response should contain expected text")
}

// requireNoBifrostError asserts that bifrostErr is nil, using GetErrorMessage for better error reporting
func requireNoBifrostError(t *testing.T, bifrostErr *schemas.BifrostError, msgAndArgs ...interface{}) {
	if bifrostErr != nil {
		errorMsg := bifrost.GetErrorMessage(bifrostErr)
		if len(msgAndArgs) > 0 {
			require.Fail(t, fmt.Sprintf("Expected no error but got: %s", errorMsg), msgAndArgs...)
		} else {
			require.Fail(t, fmt.Sprintf("Expected no error but got: %s", errorMsg))
		}
	}
}

// canAutoExecuteTool checks if a tool can be auto-executed based on client config
func canAutoExecuteTool(toolName string, config schemas.MCPClientConfig) bool {
	// First check if tool is in ToolsToExecute
	if config.ToolsToExecute != nil {
		if len(config.ToolsToExecute) == 0 {
			return false // Empty list means no tools allowed
		}
		if !slices.Contains(config.ToolsToExecute, "*") && !slices.Contains(config.ToolsToExecute, toolName) {
			return false // Tool not in allowed list
		}
	} else {
		return false // nil means no tools allowed
	}

	// Then check if tool is in ToolsToAutoExecute
	if len(config.ToolsToAutoExecute) == 0 {
		return false // No auto-execute tools configured
	}

	return slices.Contains(config.ToolsToAutoExecute, "*") || slices.Contains(config.ToolsToAutoExecute, toolName)
}
