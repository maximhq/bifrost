package mcp

import (
	"encoding/json"
	"fmt"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createToolCall creates a tool call message for testing
func createToolCall(toolName string, arguments map[string]interface{}) schemas.ChatAssistantMessageToolCall {
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

// assertExecutionResult validates execution results
func assertExecutionResult(t *testing.T, result *schemas.ChatMessage, expectedSuccess bool, expectedLogs []string, expectedErrorKind string) {
	require.NotNil(t, result)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)

	responseText := *result.Content.ContentStr

	if expectedSuccess {
		// Success case - should not contain error indicators
		assert.NotContains(t, responseText, "error", "Response should not contain error for successful execution")
		assert.NotContains(t, responseText, "Error:", "Response should not contain Error: for successful execution")

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

// assertErrorHints validates error hints in the response
func assertErrorHints(t *testing.T, result *schemas.ChatMessage, expectedHints []string) {
	require.NotNil(t, result)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)

	responseText := *result.Content.ContentStr

	for _, hint := range expectedHints {
		assert.Contains(t, responseText, hint, "Response should contain expected error hint")
	}
}

// assertLogsPresent validates that specific logs are present in the response
func assertLogsPresent(t *testing.T, result *schemas.ChatMessage, expectedLogs []string) {
	require.NotNil(t, result)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)

	responseText := *result.Content.ContentStr

	for _, expectedLog := range expectedLogs {
		assert.Contains(t, responseText, expectedLog, "Response should contain expected log")
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

// assertResultEquals validates that the result equals specific text
func assertResultEquals(t *testing.T, result *schemas.ChatMessage, expectedText string) {
	require.NotNil(t, result)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)

	responseText := *result.Content.ContentStr
	assert.Equal(t, expectedText, responseText, "Response should equal expected text")
}

// extractResultValue extracts a value from the execution result JSON
func extractResultValue(t *testing.T, result *schemas.ChatMessage, key string) interface{} {
	require.NotNil(t, result)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)

	responseText := *result.Content.ContentStr

	// Try to parse as JSON
	var resultMap map[string]interface{}
	if err := json.Unmarshal([]byte(responseText), &resultMap); err == nil {
		if val, ok := resultMap[key]; ok {
			return val
		}
	}

	// If not JSON, return nil
	return nil
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
