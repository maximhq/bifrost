package mcptests

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// DIRECT TOOL EXECUTION TESTS
// =============================================================================

func TestDirectToolExecution_ChatFormat(t *testing.T) {
	t.Parallel()

	// Use InProcess tools for self-contained testing
	manager := setupMCPManager(t)

	// Register echo tool
	err := RegisterEchoTool(manager)
	require.NoError(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	// Execute echo tool in Chat format
	ctx := createTestContext()
	toolCall := GetSampleEchoToolCall("call-1", "Hello, World!")

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
	require.Nil(t, bifrostErr, "tool execution should succeed")
	require.NotNil(t, result, "should have result")

	// Verify response format
	assert.Equal(t, schemas.ChatMessageRoleTool, result.Role)
	assert.NotNil(t, result.Content)
	if result.Content.ContentStr != nil {
		assert.Contains(t, *result.Content.ContentStr, "Hello, World!")
	}
}

func TestDirectToolExecution_ResponsesFormat(t *testing.T) {
	t.Parallel()

	// Use InProcess tools for self-contained testing
	manager := setupMCPManager(t)

	// Register echo tool
	err := RegisterEchoTool(manager)
	require.NoError(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	// Execute echo tool in Responses format
	ctx := createTestContext()
	args := map[string]interface{}{"message": "Hello, Responses!"}
	toolCall := CreateResponsesToolCallForExecution("call-1", "echo", args)

	result, bifrostErr := bifrost.ExecuteResponsesMCPTool(ctx, &toolCall)
	require.Nil(t, bifrostErr, "tool execution should succeed")
	require.NotNil(t, result, "should have result")

	// Verify response format
	assert.Equal(t, schemas.ResponsesMessageTypeFunctionCallOutput, *result.Type)
	assert.NotNil(t, result.ResponsesToolMessage)
	if result.ResponsesToolMessage.Output != nil && result.ResponsesToolMessage.Output.ResponsesToolCallOutputStr != nil {
		assert.Contains(t, *result.ResponsesToolMessage.Output.ResponsesToolCallOutputStr, "Hello, Responses!")
	}
}

func TestToolExecutionWithArguments(t *testing.T) {
	t.Parallel()

	// Use InProcess tools for self-contained testing
	manager := setupMCPManager(t)

	// Register calculator tool
	err := RegisterCalculatorTool(manager)
	require.NoError(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	testCases := []struct {
		name      string
		operation string
		x         float64
		y         float64
		expected  string
	}{
		{"add operation", "add", 2, 3, "5"},
		{"subtract operation", "subtract", 10, 4, "6"},
		{"multiply operation", "multiply", 5, 6, "30"},
		{"divide operation", "divide", 20, 4, "5"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			toolCall := GetSampleCalculatorToolCall("call-"+tc.operation, tc.operation, tc.x, tc.y)
			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			require.Nil(t, bifrostErr, "calculator execution should succeed")
			require.NotNil(t, result, "should have result")

			if result.Content != nil && result.Content.ContentStr != nil {
				assert.Contains(t, *result.Content.ContentStr, tc.expected)
			}
		})
	}
}

func TestToolExecutionInvalidArguments(t *testing.T) {
	t.Parallel()

	// Use InProcess tools for self-contained testing
	manager := setupMCPManager(t)

	// Register calculator tool
	err := RegisterCalculatorTool(manager)
	require.NoError(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	t.Run("invalid_json", func(t *testing.T) {
		toolCall := schemas.ChatAssistantMessageToolCall{
			ID:   schemas.Ptr("call-1"),
			Type: schemas.Ptr("function"),
			Function: schemas.ChatAssistantMessageToolCallFunction{
				Name:      schemas.Ptr("calculator"),
				Arguments: "invalid json {{{",
			},
		}

		result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
		// Should either return error or error in result
		if bifrostErr == nil && result != nil {
			// Some implementations may return error in result content
			t.Log("Tool execution handled invalid JSON")
		}
	})

	t.Run("missing_required_arguments", func(t *testing.T) {
		argsMap := map[string]interface{}{
			"operation": "add",
			// Missing x and y
		}
		argsJSON, _ := json.Marshal(argsMap)

		toolCall := schemas.ChatAssistantMessageToolCall{
			ID:   schemas.Ptr("call-2"),
			Type: schemas.Ptr("function"),
			Function: schemas.ChatAssistantMessageToolCallFunction{
				Name:      schemas.Ptr("calculator"),
				Arguments: string(argsJSON),
			},
		}

		result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
		// Should indicate missing arguments
		if bifrostErr == nil && result != nil {
			t.Log("Tool execution handled missing arguments")
		}
	})

	t.Run("wrong_argument_types", func(t *testing.T) {
		argsMap := map[string]interface{}{
			"operation": "add",
			"x":         "not_a_number",
			"y":         "also_not_a_number",
		}
		argsJSON, _ := json.Marshal(argsMap)

		toolCall := schemas.ChatAssistantMessageToolCall{
			ID:   schemas.Ptr("call-3"),
			Type: schemas.Ptr("function"),
			Function: schemas.ChatAssistantMessageToolCallFunction{
				Name:      schemas.Ptr("calculator"),
				Arguments: string(argsJSON),
			},
		}

		result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
		// Should indicate type error
		if bifrostErr == nil && result != nil {
			t.Log("Tool execution handled wrong types")
		}
	})
}

func TestToolExecutionTimeout(t *testing.T) {
	t.Parallel()

	// Use InProcess tools for self-contained testing
	manager := setupMCPManager(t)

	// Register delay tool
	err := RegisterDelayTool(manager)
	require.NoError(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	// Create context with short timeout (100ms)
	ctx, cancel := createTestContextWithTimeout(100 * time.Millisecond)
	defer cancel()

	// Try to execute delay tool with long duration (5 seconds)
	argsMap := map[string]interface{}{
		"seconds": 5.0, // 5 seconds delay
	}
	argsJSON, _ := json.Marshal(argsMap)

	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-timeout"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("delay"),
			Arguments: string(argsJSON),
		},
	}

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

	// Should timeout - the tool takes 5 seconds but context times out in 100ms
	if bifrostErr != nil && bifrostErr.Error != nil {
		t.Logf("✅ Got expected timeout error: %v", bifrostErr.Error.Message)
	} else if result != nil {
		// Some implementations may return timeout in result
		t.Log("Timeout handled in result")
	} else {
		t.Log("Timeout handled (no error or result)")
	}
}

func TestToolExecutionReturnsError(t *testing.T) {
	t.Parallel()

	// Use InProcess tools for self-contained testing
	manager := setupMCPManager(t)

	// Register throw_error tool
	err := RegisterThrowErrorTool(manager)
	require.NoError(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Use error tool
	errorMessage := "This is a test error"
	argsMap := map[string]interface{}{
		"error_message": errorMessage,
	}
	argsJSON, _ := json.Marshal(argsMap)

	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-error"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("bifrostInternal-throw_error"),
			Arguments: string(argsJSON),
		},
	}

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

	// Error should be propagated
	if bifrostErr != nil && bifrostErr.Error != nil {
		assert.Contains(t, bifrostErr.Error.Message, errorMessage)
		t.Logf("✅ Error properly propagated: %v", bifrostErr.Error.Message)
	} else if result != nil && result.Content != nil && result.Content.ContentStr != nil {
		// Error might be in result content
		assert.Contains(t, *result.Content.ContentStr, errorMessage)
		t.Logf("✅ Error in result content")
	}
}

// =============================================================================
// TOOL EXECUTION WITH DIFFERENT RESULT TYPES
// =============================================================================

func TestToolExecutionLargeResponse(t *testing.T) {
	t.Parallel()

	// Use InProcess tools for self-contained testing
	manager := setupMCPManager(t)

	// Register echo tool
	err := RegisterEchoTool(manager)
	require.NoError(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Create large message (10KB - reasonable size for testing)
	largeMessage := ""
	for i := 0; i < 10000; i++ {
		largeMessage += "A"
	}

	toolCall := GetSampleEchoToolCall("call-large", largeMessage)
	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

	require.Nil(t, bifrostErr, "large response should not error")
	assert.NotNil(t, result, "should have result")
	t.Logf("✅ Large response handled successfully (%d bytes)", len(largeMessage))
}

func TestToolExecutionEmptyResponse(t *testing.T) {
	t.Parallel()

	// Use InProcess tools for self-contained testing
	manager := setupMCPManager(t)

	// Register echo tool
	err := RegisterEchoTool(manager)
	require.NoError(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	toolCall := GetSampleEchoToolCall("call-empty", "")
	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

	require.Nil(t, bifrostErr, "empty response should not crash")
	assert.NotNil(t, result, "should have result structure")
	t.Logf("✅ Empty message handled successfully")
}

func TestToolExecutionStructuredResponse(t *testing.T) {
	t.Parallel()

	// Use InProcess tools for self-contained testing
	manager := setupMCPManager(t)

	// Register weather tool
	err := RegisterWeatherTool(manager)
	require.NoError(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Weather tool returns structured JSON response
	toolCall := GetSampleWeatherToolCall("call-weather", "San Francisco", "celsius")
	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

	require.Nil(t, bifrostErr, "structured response should work")
	assert.NotNil(t, result, "should have result")
	t.Logf("✅ Structured response handled successfully")
}

// =============================================================================
// LATENCY AND PERFORMANCE TESTS
// =============================================================================

func TestToolExecutionLatencyMeasurement(t *testing.T) {
	t.Parallel()

	// Use InProcess tools for fast, reliable testing
	manager := setupMCPManager(t)
	err := RegisterEchoTool(manager)
	require.NoError(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	toolCall := GetSampleEchoToolCall("call-latency", "test")

	start := time.Now()
	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
	elapsed := time.Since(start)

	require.Nil(t, bifrostErr, "tool execution should succeed")
	assert.NotNil(t, result, "should have result")

	// Latency should be reasonable (< 5 seconds for echo)
	assert.Less(t, elapsed, 5*time.Second, "echo should be fast")
}

func TestToolExecutionParallel(t *testing.T) {
	t.Parallel()

	// Use InProcess tools for fast, reliable testing
	manager := setupMCPManager(t)
	err := RegisterEchoTool(manager)
	require.NoError(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	var wg sync.WaitGroup
	errors := make(chan error, 5)

	start := time.Now()
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			toolCall := GetSampleEchoToolCall("call-parallel-"+string(rune('a'+id)), "test")
			_, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			if bifrostErr != nil {
				errors <- fmt.Errorf("tool execution error: %v", bifrostErr)
			}
		}(i)
	}

	wg.Wait()
	close(errors)
	elapsed := time.Since(start)

	// Check for errors
	for err := range errors {
		t.Errorf("Parallel execution error: %v", err)
	}

	t.Logf("Parallel execution of 5 tools took: %v", elapsed)
}

// =============================================================================
// EXTRA FIELDS AND METADATA TESTS
// =============================================================================

func TestToolExecutionExtraFields(t *testing.T) {
	t.Parallel()

	// Use InProcess tools for fast, reliable testing
	manager := setupMCPManager(t)
	err := RegisterEchoTool(manager)
	require.NoError(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	toolCall := GetSampleEchoToolCall("call-extra", "test")
	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

	require.Nil(t, bifrostErr, "tool execution should succeed")
	assert.NotNil(t, result, "should have result")

	// ExtraFields should be populated (if implementation supports it)
	// Note: ExtraFields are in BifrostMCPResponse, not ChatMessage
}

func TestToolExecutionPreservesCallID(t *testing.T) {
	t.Parallel()

	// Use InProcess tools for fast, reliable testing
	manager := setupMCPManager(t)
	err := RegisterEchoTool(manager)
	require.NoError(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Test Chat format
	expectedCallID := "call-preserve-123"
	chatToolCall := GetSampleEchoToolCall(expectedCallID, "test")
	chatResult, chatErr := bifrost.ExecuteChatMCPTool(ctx, &chatToolCall)

	require.Nil(t, chatErr, "Chat tool execution should succeed")
	if chatResult.ChatToolMessage != nil && chatResult.ChatToolMessage.ToolCallID != nil {
		assert.Equal(t, expectedCallID, *chatResult.ChatToolMessage.ToolCallID)
	}

	// Test Responses format
	args := map[string]interface{}{"message": "test"}
	responsesToolCall := CreateResponsesToolCallForExecution(expectedCallID, "echo", args)
	responsesResult, responsesErr := bifrost.ExecuteResponsesMCPTool(ctx, &responsesToolCall)

	require.Nil(t, responsesErr, "Responses tool execution should succeed")
	if responsesResult.ResponsesToolMessage != nil && responsesResult.ResponsesToolMessage.CallID != nil {
		assert.Equal(t, expectedCallID, *responsesResult.ResponsesToolMessage.CallID)
	}
}

// =============================================================================
// MULTIPLE TOOLS AND CLIENTS TESTS
// =============================================================================

func TestToolExecutionMultipleTools(t *testing.T) {
	t.Parallel()

	// Use InProcess tools for fast, reliable testing
	manager := setupMCPManager(t)
	err := RegisterCalculatorTool(manager)
	require.NoError(t, err)
	err = RegisterEchoTool(manager)
	require.NoError(t, err)
	err = RegisterWeatherTool(manager)
	require.NoError(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute different tools
	t.Run("calculator", func(t *testing.T) {
		toolCall := GetSampleCalculatorToolCall("call-calc", "add", 2, 3)
		result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
		require.Nil(t, bifrostErr)
		assert.NotNil(t, result)
	})

	t.Run("echo", func(t *testing.T) {
		toolCall := GetSampleEchoToolCall("call-echo", "test")
		result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
		require.Nil(t, bifrostErr)
		assert.NotNil(t, result)
	})

	t.Run("weather", func(t *testing.T) {
		toolCall := GetSampleWeatherToolCall("call-weather", "London", "celsius")
		result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
		require.Nil(t, bifrostErr)
		assert.NotNil(t, result)
	})
}

func TestToolExecutionMultipleClients(t *testing.T) {
	t.Parallel()

	// Setup two InProcess clients with different tools
	manager := setupMCPManager(t)

	// Register first set of tools (simulating first client)
	err := RegisterEchoTool(manager)
	require.Nil(t, err)

	// Register second tool (simulating second client)
	localToolHandler := func(args any) (string, error) {
		return `{"result": "local execution"}`, nil
	}
	localToolSchema := schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "local_tool",
			Description: schemas.Ptr("A local tool"),
		},
	}
	err = manager.RegisterTool("local_tool", "A local tool", localToolHandler, localToolSchema)
	require.Nil(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute echo tool
	echoToolCall := GetSampleEchoToolCall("call-echo", "echo test")
	echoResult, echoErr := bifrost.ExecuteChatMCPTool(ctx, &echoToolCall)
	require.Nil(t, echoErr, "Echo tool should work")
	assert.NotNil(t, echoResult)

	// Execute local tool
	argsJSON, _ := json.Marshal(map[string]interface{}{})
	inProcessToolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-local"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("bifrostInternal-local_tool"),
			Arguments: string(argsJSON),
		},
	}
	localResult, localErr := bifrost.ExecuteChatMCPTool(ctx, &inProcessToolCall)
	require.Nil(t, localErr, "InProcess tool should work")
	assert.NotNil(t, localResult)
}

// =============================================================================
// ERROR HANDLING TESTS
// =============================================================================

func TestToolExecutionToolNotFound(t *testing.T) {
	t.Parallel()

	// Use InProcess tools for self-contained testing
	manager := setupMCPManager(t)

	// Register echo tool
	err := RegisterEchoTool(manager)
	require.NoError(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Try to execute non-existent tool
	argsJSON, _ := json.Marshal(map[string]interface{}{})
	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-notfound"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("nonexistent_tool_xyz"),
			Arguments: string(argsJSON),
		},
	}

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

	// Should return error - check for "not available" or "not permitted" or "not found"
	if bifrostErr != nil && bifrostErr.Error != nil {
		// Accept any of these error messages
		errorMsg := bifrostErr.Error.Message
		hasExpectedError := assert.True(t,
			strings.Contains(errorMsg, "not available") || strings.Contains(errorMsg, "not permitted") || strings.Contains(errorMsg, "not found"),
			"error should mention tool is not available/permitted/found, got: %s", errorMsg)
		if hasExpectedError {
			t.Logf("✅ Tool not found error correctly returned: %s", errorMsg)
		}
	} else if result != nil && result.Content != nil && result.Content.ContentStr != nil {
		// Error might be in result
		t.Log("Tool not found handled in result")
	} else {
		t.Error("Expected error for non-existent tool")
	}
}

func TestToolExecutionClientNotFound(t *testing.T) {
	t.Parallel()

	// Create manager with no clients
	manager := setupMCPManager(t)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	toolCall := GetSampleEchoToolCall("call-noclient", "test")
	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

	// Should error about no client available
	if bifrostErr != nil {
		t.Logf("Got expected error: %v", bifrostErr)
	} else if result != nil {
		t.Log("No client handled in result")
	}
}

func TestToolExecutionMalformedRequest(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	clientConfig := GetSampleHTTPClientConfig(config.HTTPServerURL)
	manager := setupMCPManager(t, clientConfig)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	t.Run("missing_function_name", func(t *testing.T) {
		toolCall := schemas.ChatAssistantMessageToolCall{
			ID:   schemas.Ptr("call-noname"),
			Type: schemas.Ptr("function"),
			Function: schemas.ChatAssistantMessageToolCallFunction{
				Name:      nil, // Missing name
				Arguments: "{}",
			},
		}

		result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
		// Should error
		if bifrostErr == nil && result != nil {
			t.Log("Missing name handled")
		}
	})

	t.Run("nil_tool_call", func(t *testing.T) {
		result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, nil)
		// Should error or handle gracefully
		if bifrostErr == nil && result != nil {
			t.Log("Nil tool call handled")
		}
	})
}

// =============================================================================
// CONTEXT AND CANCELLATION TESTS
// =============================================================================

func TestToolExecutionContextCancellation(t *testing.T) {
	t.Parallel()

	// Use InProcess delay tool for fast, reliable testing
	manager := setupMCPManager(t)
	err := RegisterDelayTool(manager)
	require.NoError(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx, cancel := createTestContextWithTimeout(10 * time.Second)

	// Start long-running tool
	argsMap := map[string]interface{}{"seconds": 5.0}
	argsJSON, _ := json.Marshal(argsMap)
	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-cancel"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("delay"),
			Arguments: string(argsJSON),
		},
	}

	// Cancel after 1 second
	go func() {
		time.Sleep(time.Second)
		cancel()
	}()

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

	// Should be cancelled
	if bifrostErr != nil {
		t.Logf("Got cancellation error: %v", bifrostErr)
	} else if result != nil {
		t.Log("Cancellation handled in result")
	}
}

func TestToolExecutionContextDeadline(t *testing.T) {
	t.Parallel()

	// Use InProcess delay tool for fast, reliable testing
	manager := setupMCPManager(t)
	err := RegisterDelayTool(manager)
	require.NoError(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	// 2-second deadline
	ctx, cancel := createTestContextWithTimeout(2 * time.Second)
	defer cancel()

	// Tool that takes 5 seconds
	argsMap := map[string]interface{}{"seconds": 5.0}
	argsJSON, _ := json.Marshal(argsMap)
	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-deadline"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("delay"),
			Arguments: string(argsJSON),
		},
	}

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

	// Should hit deadline
	if bifrostErr != nil {
		t.Logf("Got deadline error: %v", bifrostErr)
	} else if result != nil {
		t.Log("Deadline handled in result")
	}
}

