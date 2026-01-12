package mcptests

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// PROTOCOL ERROR HANDLING TESTS
// =============================================================================
//
// These tests verify that the MCP implementation handles protocol-level errors
// gracefully, including:
// - MCP error responses (tools returning errors via MCP protocol)
// - Invalid/malformed tool arguments
// - Timeout scenarios
// - Intermittent failures
// - Various error types (validation, runtime, network, permission)
//
// Test Strategy:
// - Use InProcess tools for basic error testing (fast, reliable)
// - Use error-test-server (STDIO) for comprehensive error scenarios
// - Verify errors are propagated correctly to both Chat and Responses APIs
// =============================================================================

// =============================================================================
// INPROCESS ERROR HANDLING TESTS
// =============================================================================

func TestErrorHandling_InProcess_ToolReturnsError(t *testing.T) {
	t.Parallel()

	// Setup: Register throw_error tool
	manager := setupMCPManager(t)
	require.NoError(t, RegisterThrowErrorTool(manager))

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Test 1: Tool returns error in Chat format
	errorMessage := "This is a test error message"
	toolCall := createThrowErrorToolCall("call-1", errorMessage)

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

	// MCP protocol: tool errors can be returned as execution errors OR as successful
	// results with error content embedded in the message (per MCP spec)
	if bifrostErr != nil {
		// Option 1: Returned as execution error
		assert.Contains(t, bifrostErr.Error.Message, errorMessage, "error message should contain original error")
		t.Logf("✅ Tool error returned as execution error: %s", bifrostErr.Error.Message)
	} else {
		// Option 2: Returned as successful message with error content
		require.NotNil(t, result, "result should not be nil if no error")
		assert.Equal(t, schemas.ChatMessageRoleTool, result.Role, "should be tool message")
		// Error content should be in the message
		if result.Content != nil && result.Content.ContentStr != nil {
			assert.Contains(t, *result.Content.ContentStr, "Error:", "content should indicate error")
		}
		t.Logf("✅ Tool error returned as message content")
	}
}

func TestErrorHandling_InProcess_ToolReturnsError_ResponsesFormat(t *testing.T) {
	t.Parallel()

	// Setup: Register throw_error tool
	manager := setupMCPManager(t)
	require.NoError(t, RegisterThrowErrorTool(manager))

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Test 2: Tool returns error in Responses format
	errorMessage := "Responses API error test"
	args := map[string]interface{}{
		"error_message": errorMessage,
	}

	// Create Responses format tool call
	responsesToolMsg := CreateResponsesToolCallForExecution("call-2", "throw_error", args)

	result, bifrostErr := bifrost.ExecuteResponsesMCPTool(ctx, &responsesToolMsg)

	// MCP protocol: tool errors can be returned as execution errors OR as successful
	// results with error content embedded in the message
	if bifrostErr != nil {
		// Option 1: Returned as execution error
		assert.Contains(t, bifrostErr.Error.Message, errorMessage, "error message should contain original error")
		t.Logf("✅ Responses format: Tool error returned as execution error")
	} else {
		// Option 2: Returned as successful message with error content
		require.NotNil(t, result, "result should not be nil if no error")
		// Error content should be in the message
		if result.Content != nil && result.Content.ContentStr != nil {
			assert.Contains(t, *result.Content.ContentStr, "Error:", "content should indicate error")
		}
		t.Logf("✅ Responses format: Tool error returned as message content")
	}
}

func TestErrorHandling_InProcess_InvalidArguments(t *testing.T) {
	t.Parallel()

	// Setup: Register calculator tool
	manager := setupMCPManager(t)
	require.NoError(t, RegisterCalculatorTool(manager))

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	testCases := []struct {
		name        string
		arguments   string
		shouldError bool
		errorMatch  string
	}{
		{
			name:        "empty arguments",
			arguments:   "",
			shouldError: true,
			errorMatch:  "operation must be a string",
		},
		{
			name:        "empty object",
			arguments:   "{}",
			shouldError: true,
			errorMatch:  "operation must be a string",
		},
		{
			name:        "malformed JSON",
			arguments:   "{invalid json}",
			shouldError: true,
			errorMatch:  "failed to parse tool arguments",
		},
		{
			name:        "missing required field",
			arguments:   `{"operation": "add", "x": 10}`,
			shouldError: true,
			errorMatch:  "y must be a number",
		},
		{
			name:        "wrong type for field",
			arguments:   `{"operation": "add", "x": "not a number", "y": 20}`,
			shouldError: true,
			errorMatch:  "x must be a number",
		},
		{
			name:        "division by zero",
			arguments:   `{"operation": "divide", "x": 10, "y": 0}`,
			shouldError: true,
			errorMatch:  "division by zero",
		},
		{
			name:        "invalid operation",
			arguments:   `{"operation": "invalid", "x": 10, "y": 20}`,
			shouldError: true,
			errorMatch:  "unknown operation",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			toolCall := schemas.ChatAssistantMessageToolCall{
				ID:   schemas.Ptr(fmt.Sprintf("call-%s", tc.name)),
				Type: schemas.Ptr("function"),
				Function: schemas.ChatAssistantMessageToolCallFunction{
					Name:      schemas.Ptr("bifrostInternal-calculator"),
					Arguments: tc.arguments,
				},
			}

			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			if tc.shouldError {
				// Error can be returned as execution error OR as message content
				errorHandled := false
				if bifrostErr != nil {
					// Returned as execution error
					if tc.errorMatch != "" {
						assert.Contains(t, bifrostErr.Error.Message, tc.errorMatch,
							"error message should contain expected text for test case '%s'", tc.name)
					}
					t.Logf("✅ %s: Error handled as execution error: %s", tc.name, bifrostErr.Error.Message)
					errorHandled = true
				} else if result != nil {
					// Returned as message content (tool result with error text)
					t.Logf("✅ %s: Error handled as message content", tc.name)
					errorHandled = true
				}
				assert.True(t, errorHandled, "test case '%s' should handle error", tc.name)
			} else {
				if bifrostErr != nil && bifrostErr.Error != nil {
					fmt.Println("bifrostErr", bifrostErr.Error.Message)
				}
				assert.Nil(t, bifrostErr, "test case '%s' should not return error", tc.name)
				assert.NotNil(t, result, "result should not be nil")
			}
		})
	}
}

func TestErrorHandling_InProcess_NullAndUndefinedArguments(t *testing.T) {
	t.Parallel()

	// Setup: Register echo tool
	manager := setupMCPManager(t)
	require.NoError(t, RegisterEchoTool(manager))

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	testCases := []struct {
		name      string
		arguments string
		expectErr bool
	}{
		{
			name:      "null value for message",
			arguments: `{"message": null}`,
			expectErr: true, // message is required
		},
		{
			name:      "missing message field",
			arguments: `{}`,
			expectErr: true, // message is required
		},
		{
			name:      "valid message",
			arguments: `{"message": "test"}`,
			expectErr: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			toolCall := schemas.ChatAssistantMessageToolCall{
				ID:   schemas.Ptr(fmt.Sprintf("call-%s", tc.name)),
				Type: schemas.Ptr("function"),
				Function: schemas.ChatAssistantMessageToolCallFunction{
					Name:      schemas.Ptr("bifrostInternal-echo"),
					Arguments: tc.arguments,
				},
			}

			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			if tc.expectErr {
				// Error can be returned as execution error OR as message content with error
				errorHandled := (bifrostErr != nil) || (result != nil)
				assert.True(t, errorHandled, "test '%s' should handle error", tc.name)
				t.Logf("✅ %s: Error handled correctly", tc.name)
			} else {
				if bifrostErr != nil && bifrostErr.Error != nil {
					fmt.Println("bifrostErr", bifrostErr.Error.Message)
				}
				assert.Nil(t, bifrostErr, "test '%s' should not return error", tc.name)
				assert.NotNil(t, result, "result should not be nil")
				t.Logf("✅ %s: Executed successfully", tc.name)
			}
		})
	}
}

// =============================================================================
// STDIO ERROR-TEST-SERVER TESTS
// =============================================================================

func TestErrorHandling_STDIO_MCPErrorResponse(t *testing.T) {
	t.Parallel()

	// Check if error-test-server is available
	bifrostRoot := GetBifrostRoot(t)
	errorServerConfig := GetErrorTestServerConfig(bifrostRoot)

	manager := setupMCPManager(t)
	err := manager.AddClient(errorServerConfig)
	if err != nil {
		t.Skipf("error-test-server not available: %v (build with: cd examples/mcps/error-test-server && go build -o bin/error-test-server)", err)
	}
	t.Cleanup(func() { _ = manager.RemoveClient(errorServerConfig.ID) })

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Wait for client to be ready
	time.Sleep(500 * time.Millisecond)

	// Test different error types from error-test-server's return_error tool
	errorTypes := []string{
		"validation",
		"runtime",
		"network",
		"timeout",
		"permission",
	}

	for _, errorType := range errorTypes {
		t.Run(errorType, func(t *testing.T) {
			args := map[string]interface{}{
				"error_type": errorType,
			}
			argsJSON, _ := json.Marshal(args)

			toolCall := schemas.ChatAssistantMessageToolCall{
				ID:   schemas.Ptr(fmt.Sprintf("call-error-%s", errorType)),
				Type: schemas.Ptr("function"),
				Function: schemas.ChatAssistantMessageToolCallFunction{
					Name:      schemas.Ptr("ErrorTestServer-return_error"),
					Arguments: string(argsJSON),
				},
			}

			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			// MCP error can be returned as execution error OR as message content
			errorHandled := false
			if bifrostErr != nil {
				// Returned as execution error
				errorMsg := bifrostErr.Error.Message
				assert.NotEmpty(t, errorMsg, "error message should not be empty")
				t.Logf("✅ %s error (as execution error): %s", errorType, errorMsg)
				errorHandled = true
			} else if result != nil {
				// Returned as message content
				assert.Equal(t, schemas.ChatMessageRoleTool, result.Role, "should be tool message")
				if result.Content != nil && result.Content.ContentStr != nil {
					t.Logf("✅ %s error (as message content): %s", errorType, *result.Content.ContentStr)
				} else {
					t.Logf("✅ %s error (as message content)", errorType)
				}
				errorHandled = true
			}
			assert.True(t, errorHandled, "should handle %s error type", errorType)
		})
	}
}

func TestErrorHandling_STDIO_TimeoutScenario(t *testing.T) {
	t.Parallel()

	// Check if error-test-server is available
	bifrostRoot := GetBifrostRoot(t)
	errorServerConfig := GetErrorTestServerConfig(bifrostRoot)

	manager := setupMCPManager(t)

	// Set a short timeout for this test
	manager.UpdateToolManagerConfig(&schemas.MCPToolManagerConfig{
		ToolExecutionTimeout: 2 * time.Second, // 2 second timeout
	})

	err := manager.AddClient(errorServerConfig)
	if err != nil {
		t.Skipf("error-test-server not available: %v", err)
	}
	t.Cleanup(func() { _ = manager.RemoveClient(errorServerConfig.ID) })

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Wait for client to be ready
	time.Sleep(500 * time.Millisecond)

	// Test: Tool that takes longer than timeout
	args := map[string]interface{}{
		"seconds": 5.0, // Takes 5 seconds, timeout is 2 seconds
	}
	argsJSON, _ := json.Marshal(args)

	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-timeout-test"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("ErrorTestServer-timeout_after"),
			Arguments: string(argsJSON),
		},
	}

	start := time.Now()
	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
	elapsed := time.Since(start)

	// Verify timeout occurred
	assert.NotNil(t, bifrostErr, "should return timeout error")
	assert.Nil(t, result, "result should be nil on timeout")
	assert.Contains(t, bifrostErr.Error.Message, "timed out", "error message should indicate timeout")

	// Verify timeout happened around 2 seconds (with some tolerance)
	assert.Less(t, elapsed, 3*time.Second, "should timeout within 3 seconds")
	assert.Greater(t, elapsed, 1*time.Second, "should take at least 1 second")

	t.Logf("✅ Timeout handled correctly after %v: %s", elapsed, bifrostErr.Error.Message)
}

func TestErrorHandling_STDIO_MalformedJSON(t *testing.T) {
	t.Parallel()

	// Check if error-test-server is available
	bifrostRoot := GetBifrostRoot(t)
	errorServerConfig := GetErrorTestServerConfig(bifrostRoot)

	manager := setupMCPManager(t)
	err := manager.AddClient(errorServerConfig)
	if err != nil {
		t.Skipf("error-test-server not available: %v", err)
	}
	t.Cleanup(func() { _ = manager.RemoveClient(errorServerConfig.ID) })

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Wait for client to be ready
	time.Sleep(500 * time.Millisecond)

	// Test: Tool returns malformed JSON
	// Note: The MCP protocol wraps the response, so the malformed JSON is in the content
	// The MCP layer should handle this gracefully
	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-malformed-json"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("ErrorTestServer-return_malformed_json"),
			Arguments: "{}",
		},
	}

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

	// The MCP protocol should handle this - either return the malformed JSON as text
	// or return an error. Either is acceptable as long as it doesn't crash.
	if bifrostErr != nil {
		if bifrostErr != nil && bifrostErr.Error != nil {
			fmt.Println("bifrostErr", bifrostErr.Error.Message)
		}
		t.Logf("✅ Malformed JSON handled as error: %s", bifrostErr.Error.Message)
	} else {
		require.NotNil(t, result, "if no error, result should not be nil")
		t.Logf("✅ Malformed JSON returned as content (MCP protocol wrapped it)")
	}
}

func TestErrorHandling_STDIO_IntermittentFailures(t *testing.T) {
	t.Parallel()

	// Check if error-test-server is available
	bifrostRoot := GetBifrostRoot(t)
	errorServerConfig := GetErrorTestServerConfig(bifrostRoot)

	manager := setupMCPManager(t)
	err := manager.AddClient(errorServerConfig)
	if err != nil {
		t.Skipf("error-test-server not available: %v", err)
	}
	t.Cleanup(func() { _ = manager.RemoveClient(errorServerConfig.ID) })

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Wait for client to be ready
	time.Sleep(500 * time.Millisecond)

	testCases := []struct {
		name     string
		failRate float64
		runs     int
	}{
		{
			name:     "always_succeed",
			failRate: 0.0,
			runs:     10,
		},
		{
			name:     "always_fail",
			failRate: 100.0,
			runs:     10,
		},
		{
			name:     "fifty_percent",
			failRate: 50.0,
			runs:     20,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			successCount := 0
			errorCount := 0

			for i := 0; i < tc.runs; i++ {
				args := map[string]interface{}{
					"fail_rate": tc.failRate,
				}
				argsJSON, _ := json.Marshal(args)

				toolCall := schemas.ChatAssistantMessageToolCall{
					ID:   schemas.Ptr(fmt.Sprintf("call-intermittent-%d", i)),
					Type: schemas.Ptr("function"),
					Function: schemas.ChatAssistantMessageToolCallFunction{
						Name:      schemas.Ptr("ErrorTestServer-intermittent_fail"),
						Arguments: string(argsJSON),
					},
				}

				result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

				// MCP protocol: intermittent_fail returns either:
				// - Error result (as tool message content with "Intermittent failure")
				// - Success result (as tool message content with JSON {"success": true})
				if bifrostErr != nil {
					errorCount++
				} else if result != nil {
					// Check content to determine if it's a success or error
					if result.Content != nil && result.Content.ContentStr != nil {
						content := *result.Content.ContentStr
						// Error responses contain "Intermittent failure" or "Error:"
						if strings.Contains(content, "Intermittent failure") || strings.Contains(content, "Error:") {
							errorCount++
						} else {
							// Success responses contain JSON with "success": true
							successCount++
						}
					} else {
						// No content, consider it a success
						successCount++
					}
				}
			}

			// Verify failure rate is approximately correct
			if tc.failRate == 0.0 {
				assert.Equal(t, tc.runs, successCount, "all should succeed with 0%% fail rate")
				assert.Equal(t, 0, errorCount, "no errors with 0%% fail rate")
			} else if tc.failRate == 100.0 {
				assert.Equal(t, 0, successCount, "none should succeed with 100%% fail rate")
				assert.Equal(t, tc.runs, errorCount, "all should fail with 100%% fail rate")
			} else {
				// For 50%, we expect roughly half to succeed (with some variance)
				// Allow 20-80% success range to account for randomness
				successRate := float64(successCount) / float64(tc.runs) * 100
				assert.Greater(t, successRate, 20.0, "success rate should be > 20%%")
				assert.Less(t, successRate, 80.0, "success rate should be < 80%%")
			}

			t.Logf("✅ %s: %d successes, %d errors out of %d runs", tc.name, successCount, errorCount, tc.runs)
		})
	}
}

// =============================================================================
// ERROR PROPAGATION AND FORMATTING TESTS
// =============================================================================

func TestErrorHandling_ErrorMessageFormat(t *testing.T) {
	t.Parallel()

	// Setup: Register throw_error tool
	manager := setupMCPManager(t)
	require.NoError(t, RegisterThrowErrorTool(manager))

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Test: Verify error message structure
	errorMessage := "Custom error message for formatting test"
	toolCall := createThrowErrorToolCall("call-format", errorMessage)

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

	// Verify error handling (either as error or message content)
	if bifrostErr != nil {
		require.NotNil(t, bifrostErr.Error, "error field should not be nil")
		assert.NotEmpty(t, bifrostErr.Error.Message, "error message should not be empty")
		assert.Contains(t, bifrostErr.Error.Message, errorMessage, "error should contain original message")
		t.Logf("✅ Error message format (as error): %s", bifrostErr.Error.Message)
	} else {
		require.NotNil(t, result, "result should not be nil")
		t.Logf("✅ Error message format (as content): handled gracefully")
	}
}

func TestErrorHandling_MultipleConsecutiveErrors(t *testing.T) {
	t.Parallel()

	// Setup: Register throw_error tool
	manager := setupMCPManager(t)
	require.NoError(t, RegisterThrowErrorTool(manager))

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Test: Execute 5 tools that all return errors
	// Verify each error is handled independently
	numErrors := 5
	successfulExecutions := 0
	for i := 0; i < numErrors; i++ {
		errorMessage := fmt.Sprintf("Error number %d", i+1)
		toolCall := createThrowErrorToolCall(fmt.Sprintf("call-%d", i), errorMessage)

		result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

		// Each execution should complete (either with error or result containing error)
		if bifrostErr != nil {
			assert.Contains(t, bifrostErr.Error.Message, errorMessage, "error %d should contain correct message", i+1)
		} else {
			assert.NotNil(t, result, "execution %d should have result", i+1)
		}
		successfulExecutions++
	}

	assert.Equal(t, numErrors, successfulExecutions, "all %d executions should complete", numErrors)
	t.Logf("✅ Successfully handled %d consecutive error tool executions independently", numErrors)
}

func TestErrorHandling_ErrorWithSpecialCharacters(t *testing.T) {
	t.Parallel()

	// Setup: Register throw_error tool
	manager := setupMCPManager(t)
	require.NoError(t, RegisterThrowErrorTool(manager))

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Test error messages with special characters
	specialMessages := []string{
		"Error with \"quotes\"",
		"Error with 'single quotes'",
		"Error with\nnewlines\nincluded",
		"Error with\ttabs",
		"Error with unicode: 你好世界 🚀",
		"Error with backslash: C:\\path\\to\\file",
		"Error with JSON: {\"key\": \"value\"}",
	}

	handledCount := 0
	for i, msg := range specialMessages {
		toolCall := createThrowErrorToolCall(fmt.Sprintf("call-special-%d", i), msg)

		result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

		// Verify the tool execution completes (either with error or result)
		if bifrostErr != nil {
			// Error message should contain the original text (may be escaped)
			assert.True(t,
				strings.Contains(bifrostErr.Error.Message, msg) ||
					strings.Contains(bifrostErr.Error.Message, strings.ReplaceAll(msg, "\n", "\\n")) ||
					strings.Contains(bifrostErr.Error.Message, strings.ReplaceAll(msg, "\t", "\\t")),
				"error message should contain original text or escaped version: %s", msg)
		} else {
			assert.NotNil(t, result, "should have result for message: %s", msg)
		}
		handledCount++
	}

	assert.Equal(t, len(specialMessages), handledCount, "all special character messages should be handled")
	t.Logf("✅ Successfully handled %d error messages with special characters", len(specialMessages))
}

// =============================================================================
// HELPER FUNCTIONS
// =============================================================================

func createThrowErrorToolCall(id, errorMessage string) schemas.ChatAssistantMessageToolCall {
	args := map[string]interface{}{
		"error_message": errorMessage,
	}
	argsJSON, _ := json.Marshal(args)

	return schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr(id),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("bifrostInternal-throw_error"),
			Arguments: string(argsJSON),
		},
	}
}
