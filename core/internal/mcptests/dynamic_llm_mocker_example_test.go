package mcptests

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// EXAMPLE TESTS FOR DYNAMIC LLM MOCKER
// =============================================================================

// TestDynamicLLMMocker_BasicValidation demonstrates basic validation pattern
func TestDynamicLLMMocker_BasicValidation(t *testing.T) {
	t.Parallel()
	t.Skip("Example test - demonstrates usage pattern, requires completion of mock setup")

	// Setup: Create a mocker with a validating response
	mocker := NewDynamicLLMMocker()

	// Add a response that validates tool results
	mocker.AddChatResponse(
		CreateValidatingChatResponse(
			"call-1",
			[]string{"sunny", "temperature"},
			"The weather looks good!",
			"Could not understand weather data",
		),
	)

	// Simulate message history with a tool result
	history := []schemas.ChatMessage{
		GetSampleUserMessage("What's the weather?"),
		GetSampleToolCallMessage([]schemas.ChatAssistantMessageToolCall{
			GetSampleWeatherToolCall("call-1", "London", "celsius"),
		}),
		GetSampleToolResultMessage("call-1", `{"location":"London","temperature":22,"units":"celsius","conditions":"sunny"}`),
	}

	// Create a mock request
	req := &schemas.BifrostChatRequest{
		Input: history,
	}

	// Execute
	ctx := createTestContext()
	response, err := mocker.MakeChatRequest(ctx, req)

	// Verify
	require.Nil(t, err, "should not error")
	require.NotNil(t, response, "should return response")
	require.NotEmpty(t, response.Choices, "should have choices")

	content := response.Choices[0].ChatNonStreamResponseChoice.Message.Content.ContentStr
	assert.Contains(t, *content, "looks good", "should validate successfully")
}

// TestDynamicLLMMocker_ConditionalResponse demonstrates conditional response based on history
func TestDynamicLLMMocker_ConditionalResponse(t *testing.T) {
	t.Parallel()
	t.Skip("Example test - demonstrates usage pattern, requires completion of mock setup")

	mocker := NewDynamicLLMMocker()

	// Add a conditional response
	mocker.AddChatResponse(
		CreateConditionalChatResponse(
			func(history []schemas.ChatMessage) bool {
				return HasToolCallInChatHistory(history, "get_weather")
			},
			CreateChatResponseWithText("I see you requested weather data"),
			CreateChatResponseWithText("No weather request found"),
		),
	)

	// Test with weather tool call
	historyWithWeather := []schemas.ChatMessage{
		GetSampleUserMessage("What's the weather?"),
		GetSampleToolCallMessage([]schemas.ChatAssistantMessageToolCall{
			GetSampleWeatherToolCall("call-1", "Tokyo", "celsius"),
		}),
	}

	req := &schemas.BifrostChatRequest{Input: historyWithWeather}
	ctx := createTestContext()
	response, err := mocker.MakeChatRequest(ctx, req)

	require.Nil(t, err)
	require.NotNil(t, response)
	content := response.Choices[0].ChatNonStreamResponseChoice.Message.Content.ContentStr
	assert.Contains(t, *content, "weather data", "should detect weather call")
}

// TestDynamicLLMMocker_MultiTurnScenario demonstrates multi-turn agent scenario
func TestDynamicLLMMocker_MultiTurnScenario(t *testing.T) {
	t.Parallel()
	t.Skip("Example test - demonstrates usage pattern, requires completion of mock setup")

	mocker := NewDynamicLLMMocker()

	// Turn 1: Request weather
	mocker.AddChatResponse(CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
		// Check that we got a user message asking about weather
		lastMsg, found := GetLastUserMessageFromChatHistory(history)
		if found && containsString(lastMsg, "weather") {
			return CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleWeatherToolCall("call-1", "Paris", "celsius"),
			})
		}
		return CreateChatResponseWithText("I don't understand the request")
	}))

	// Turn 2: Validate result and respond
	mocker.AddChatResponse(
		CreateValidatingChatResponse(
			"call-1",
			[]string{"Paris", "temperature"},
			"The weather in Paris is lovely!",
			"Could not get Paris weather",
		),
	)

	// Turn 1: User asks about weather
	req1 := &schemas.BifrostChatRequest{
		Input: []schemas.ChatMessage{
			GetSampleUserMessage("What's the weather in Paris?"),
		},
	}

	ctx := createTestContext()
	resp1, err1 := mocker.MakeChatRequest(ctx, req1)

	require.Nil(t, err1)
	require.NotNil(t, resp1)
	assert.NotEmpty(t, resp1.Choices[0].ChatNonStreamResponseChoice.Message.ChatAssistantMessage.ToolCalls)

	// Turn 2: System returns tool result
	toolCall := resp1.Choices[0].ChatNonStreamResponseChoice.Message.ChatAssistantMessage.ToolCalls[0]
	req2 := &schemas.BifrostChatRequest{
		Input: []schemas.ChatMessage{
			GetSampleUserMessage("What's the weather in Paris?"),
			*resp1.Choices[0].ChatNonStreamResponseChoice.Message,
			GetSampleToolResultMessage(*toolCall.ID, `{"location":"Paris","temperature":18,"units":"celsius","conditions":"sunny"}`),
		},
	}

	resp2, err2 := mocker.MakeChatRequest(ctx, req2)

	require.Nil(t, err2)
	require.NotNil(t, resp2)
	content := resp2.Choices[0].ChatNonStreamResponseChoice.Message.Content.ContentStr
	assert.Contains(t, *content, "lovely", "should validate Paris weather successfully")
}

// TestDynamicLLMMocker_HelperFunctions tests the helper functions
func TestDynamicLLMMocker_HelperFunctions(t *testing.T) {
	t.Parallel()
	t.Skip("Example test - demonstrates usage pattern, requires completion of mock setup")

	history := []schemas.ChatMessage{
		GetSampleUserMessage("Calculate 10 + 20"),
		GetSampleToolCallMessage([]schemas.ChatAssistantMessageToolCall{
			GetSampleCalculatorToolCall("calc-1", "add", 10, 20),
		}),
		GetSampleToolResultMessage("calc-1", `{"result": 30}`),
		GetSampleToolCallMessage([]schemas.ChatAssistantMessageToolCall{
			GetSampleCalculatorToolCall("calc-2", "multiply", 5, 6),
		}),
		GetSampleToolResultMessage("calc-2", `{"result": 30}`),
	}

	// Test GetToolResultFromChatHistory
	result1, found1 := GetToolResultFromChatHistory(history, "calc-1")
	assert.True(t, found1, "should find calc-1 result")
	assert.Contains(t, result1, "30", "should contain result")

	// Test GetAllToolResultsFromChatHistory
	allResults := GetAllToolResultsFromChatHistory(history)
	assert.Len(t, allResults, 2, "should have 2 results")
	assert.Contains(t, allResults["calc-1"], "30")
	assert.Contains(t, allResults["calc-2"], "30")

	// Test CountToolCallsInChatHistory
	count := CountToolCallsInChatHistory(history)
	assert.Equal(t, 2, count, "should count 2 tool calls")

	// Test HasToolCallInChatHistory
	// Tool names may include client prefix, so check with HasToolCallInChatHistory which handles both
	hasCalc := HasToolCallInChatHistory(history, "calculator")
	assert.True(t, hasCalc, "should detect calculator calls")

	hasWeather := HasToolCallInChatHistory(history, "get_weather")
	assert.False(t, hasWeather, "should not detect weather calls")

	// Test GetLastUserMessageFromChatHistory
	lastMsg, foundMsg := GetLastUserMessageFromChatHistory(history)
	assert.True(t, foundMsg, "should find user message")
	assert.Contains(t, lastMsg, "Calculate", "should get correct message")
}

// TestDynamicLLMMocker_CallCount tests that call counts are tracked correctly
func TestDynamicLLMMocker_CallCount(t *testing.T) {
	t.Parallel()
	t.Skip("Example test - demonstrates usage pattern, requires completion of mock setup")

	mocker := NewDynamicLLMMocker()

	// Add 3 responses
	mocker.AddStaticChatResponse(CreateChatResponseWithText("Response 1"))
	mocker.AddStaticChatResponse(CreateChatResponseWithText("Response 2"))
	mocker.AddStaticChatResponse(CreateChatResponseWithText("Response 3"))

	ctx := createTestContext()
	req := &schemas.BifrostChatRequest{
		Input: []schemas.ChatMessage{GetSampleUserMessage("Test")},
	}

	// Make 3 calls
	assert.Equal(t, 0, mocker.GetChatCallCount(), "should start at 0")

	_, _ = mocker.MakeChatRequest(ctx, req)
	assert.Equal(t, 1, mocker.GetChatCallCount(), "should be 1 after first call")

	_, _ = mocker.MakeChatRequest(ctx, req)
	assert.Equal(t, 2, mocker.GetChatCallCount(), "should be 2 after second call")

	_, _ = mocker.MakeChatRequest(ctx, req)
	assert.Equal(t, 3, mocker.GetChatCallCount(), "should be 3 after third call")

	// 4th call should error (no more responses)
	_, err := mocker.MakeChatRequest(ctx, req)
	assert.NotNil(t, err, "should error when out of responses")
	assert.Equal(t, 3, mocker.GetChatCallCount(), "should still be 3")
}

// TestDynamicLLMMocker_HistoryTracking tests that history is tracked correctly
func TestDynamicLLMMocker_HistoryTracking(t *testing.T) {
	t.Parallel()

	mocker := NewDynamicLLMMocker()

	// Add responses
	mocker.AddStaticChatResponse(CreateChatResponseWithText("Response 1"))
	mocker.AddStaticChatResponse(CreateChatResponseWithText("Response 2"))

	ctx := createTestContext()

	// First call with 1 message
	req1 := &schemas.BifrostChatRequest{
		Input: []schemas.ChatMessage{GetSampleUserMessage("First message")},
	}
	_, _ = mocker.MakeChatRequest(ctx, req1)

	// Second call with 2 messages
	req2 := &schemas.BifrostChatRequest{
		Input: []schemas.ChatMessage{
			GetSampleUserMessage("First message"),
			GetSampleAssistantMessage("Response"),
		},
	}
	_, _ = mocker.MakeChatRequest(ctx, req2)

	// Check history
	history := mocker.GetChatHistory()
	assert.Len(t, history, 2, "should have 2 history entries")
	assert.Len(t, history[0], 1, "first call should have 1 message")
	assert.Len(t, history[1], 2, "second call should have 2 messages")
}
