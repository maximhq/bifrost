package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
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

// createMCPClientConfig creates an MCPClientConfig with the specified parameters
func createMCPClientConfig(id, name string, isCodeMode bool, toolsToExecute, toolsToAutoExecute []string) schemas.MCPClientConfig {
	return schemas.MCPClientConfig{
		ID:                 id,
		Name:               name,
		IsCodeModeClient:   isCodeMode,
		ConnectionType:     schemas.MCPConnectionTypeInProcess,
		ToolsToExecute:     toolsToExecute,
		ToolsToAutoExecute: toolsToAutoExecute,
	}
}

// assertAgentModeResponse validates agent mode response structure
func assertAgentModeResponse(t *testing.T, response *schemas.BifrostChatResponse, expectedExecutedCount int, expectedNonAutoExecuteCount int) {
	require.NotNil(t, response)
	require.NotNil(t, response.Choices)
	require.Greater(t, len(response.Choices), 0)

	choice := response.Choices[0]
	require.NotNil(t, choice.ChatNonStreamResponseChoice)
	require.NotNil(t, choice.ChatNonStreamResponseChoice.Message)

	message := choice.ChatNonStreamResponseChoice.Message

	// Check content contains executed tool results if any were executed
	if expectedExecutedCount > 0 {
		require.NotNil(t, message.Content)
		require.NotNil(t, message.Content.ContentStr)
		content := *message.Content.ContentStr
		assert.Contains(t, content, "The Output from allowed tools calls is", "Should contain executed tool results")
	}

	// Check tool calls if non-auto-execute tools are present
	if expectedNonAutoExecuteCount > 0 {
		require.NotNil(t, message.ChatAssistantMessage)
		require.NotNil(t, message.ChatAssistantMessage.ToolCalls)
		assert.Equal(t, expectedNonAutoExecuteCount, len(message.ChatAssistantMessage.ToolCalls), "Should have correct number of non-auto-execute tool calls")

		// Verify finish_reason is "stop" when non-auto-execute tools are present
		require.NotNil(t, choice.FinishReason)
		assert.Equal(t, "stop", *choice.FinishReason, "Finish reason should be 'stop' when non-auto-execute tools present")
	} else {
		// When all tools are auto-execute, finish_reason should be "tool_calls" (continues loop)
		if choice.FinishReason != nil {
			assert.Equal(t, "tool_calls", *choice.FinishReason, "Finish reason should be 'tool_calls' when all tools are auto-execute")
		}
	}
}

// assertToolCallInResponse checks if a specific tool call is present in the response
func assertToolCallInResponse(t *testing.T, response *schemas.BifrostChatResponse, toolName string) bool {
	require.NotNil(t, response)
	require.NotNil(t, response.Choices)
	require.Greater(t, len(response.Choices), 0)

	choice := response.Choices[0]
	if choice.ChatNonStreamResponseChoice == nil || choice.ChatNonStreamResponseChoice.Message == nil {
		return false
	}

	message := choice.ChatNonStreamResponseChoice.Message
	if message.ChatAssistantMessage == nil {
		return false
	}

	for _, toolCall := range message.ChatAssistantMessage.ToolCalls {
		if toolCall.Function.Name != nil && *toolCall.Function.Name == toolName {
			return true
		}
	}

	return false
}

// extractExecutedToolResults extracts executed tool results from agent mode response content
func extractExecutedToolResults(t *testing.T, response *schemas.BifrostChatResponse) map[string]interface{} {
	require.NotNil(t, response)
	require.NotNil(t, response.Choices)
	require.Greater(t, len(response.Choices), 0)

	choice := response.Choices[0]
	if choice.ChatNonStreamResponseChoice == nil || choice.ChatNonStreamResponseChoice.Message == nil {
		return nil
	}

	message := choice.ChatNonStreamResponseChoice.Message
	if message.Content == nil || message.Content.ContentStr == nil {
		return nil
	}

	content := *message.Content.ContentStr

	// Try to extract JSON from content
	// Content format: "The Output from allowed tools calls is - {...}\n\nNow I shall call these tools next..."
	jsonStart := strings.Index(content, "{")
	if jsonStart == -1 {
		return nil
	}

	jsonEnd := strings.LastIndex(content, "}")
	if jsonEnd == -1 || jsonEnd < jsonStart {
		return nil
	}

	jsonStr := content[jsonStart : jsonEnd+1]
	var resultMap map[string]interface{}
	if err := json.Unmarshal([]byte(jsonStr), &resultMap); err != nil {
		return nil
	}

	return resultMap
}

// canAutoExecuteTool checks if a tool can be auto-executed based on client config
func canAutoExecuteTool(toolName string, config schemas.MCPClientConfig) bool {
	// First check if tool is in ToolsToExecute
	if config.ToolsToExecute != nil {
		if len(config.ToolsToExecute) == 0 {
			return false // Empty list means no tools allowed
		}
		if !contains(config.ToolsToExecute, "*") && !contains(config.ToolsToExecute, toolName) {
			return false // Tool not in allowed list
		}
	} else {
		return false // nil means no tools allowed
	}

	// Then check if tool is in ToolsToAutoExecute
	if len(config.ToolsToAutoExecute) == 0 {
		return false // No auto-execute tools configured
	}

	return contains(config.ToolsToAutoExecute, "*") || contains(config.ToolsToAutoExecute, toolName)
}

// contains checks if a slice contains a string
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
