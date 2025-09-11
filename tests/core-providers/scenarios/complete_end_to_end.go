package scenarios

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// RunCompleteEnd2EndTest executes the complete end-to-end test scenario
func RunCompleteEnd2EndTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig config.ComprehensiveTestConfig) {
	if !testConfig.Scenarios.CompleteEnd2End {
		t.Logf("Complete end-to-end not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("CompleteEnd2End", func(t *testing.T) {
		// Multi-step conversation with tools and images
		userMessage1 := CreateBasicChatMessage("Hi, I'm planning a trip. Can you help me get the weather in Paris?")

		request1 := &schemas.BifrostRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ChatModel,
			Input: schemas.RequestInput{
				ChatCompletionInput: &[]schemas.ChatMessage{userMessage1},
			},
			Params: MergeModelParameters(&schemas.ModelParameters{
				Tools:     &[]schemas.Tool{WeatherToolDefinition},
				MaxTokens: bifrost.Ptr(150),
			}, testConfig.CustomParams),
			Fallbacks: testConfig.Fallbacks,
		}

		response1, err := client.ChatCompletionRequest(ctx, request1)
		require.Nilf(t, err, "First end-to-end request failed: %v", err)
		require.NotNil(t, response1)
		require.NotEmpty(t, response1.Choices)

		t.Logf("✅ First response: %s", GetResultContent(response1))

		// If tool was called, simulate result and continue conversation
		var conversationHistory []schemas.ChatMessage
		conversationHistory = append(conversationHistory, userMessage1)

		// Add all choice messages to conversation history
		for _, choice := range response1.Choices {
			conversationHistory = append(conversationHistory, choice.Message)
		}

		// Find any choice with tool calls for processing
		var selectedToolCall *schemas.ToolCall
		for _, choice := range response1.Choices {
			message := choice.Message
			if message.AssistantMessage != nil && message.AssistantMessage.ToolCalls != nil {
				toolCalls := *message.AssistantMessage.ToolCalls
				// Look for a valid weather tool call
				for _, toolCall := range toolCalls {
					if toolCall.Function.Name != nil && *toolCall.Function.Name == "get_weather" {
						selectedToolCall = &toolCall
						break
					}
				}
				if selectedToolCall != nil {
					break
				}
			}
		}

		// If a tool call was found, simulate the result
		if selectedToolCall != nil {
			// Simulate tool result
			toolResult := `{"temperature": "18", "unit": "celsius", "description": "Partly cloudy", "humidity": "70%"}`
			toolCallID := ""
			if selectedToolCall.ID != nil {
				toolCallID = *selectedToolCall.ID
			} else if selectedToolCall.Function.Name != nil {
				toolCallID = *selectedToolCall.Function.Name
			}
			require.NotEmpty(t, toolCallID, "toolCallID must not be empty – provider did not return ID or Function.Name")
			toolMessage := CreateToolMessage(toolResult, toolCallID)
			conversationHistory = append(conversationHistory, toolMessage)
		}

		// Continue with follow-up
		followUpMessage := CreateBasicChatMessage("Thanks! Now can you tell me about this travel image?")
		if testConfig.Scenarios.ImageURL {
			followUpMessage = CreateImageMessage("Thanks! Now can you tell me what you see in this travel-related image?", TestImageURL)
		}
		conversationHistory = append(conversationHistory, followUpMessage)

		finalRequest := &schemas.BifrostRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ChatModel,
			Input: schemas.RequestInput{
				ChatCompletionInput: &conversationHistory,
			},
			Params: MergeModelParameters(&schemas.ModelParameters{
				MaxTokens: bifrost.Ptr(200),
			}, testConfig.CustomParams),
			Fallbacks: testConfig.Fallbacks,
		}

		finalResponse, err := client.ChatCompletionRequest(ctx, finalRequest)
		require.Nilf(t, err, "Final end-to-end request failed: %v", err)
		require.NotNil(t, finalResponse)
		require.NotEmpty(t, finalResponse.Choices)

		finalContent := GetResultContent(finalResponse)
		assert.NotEmpty(t, finalContent, "Final response content should not be empty")

		t.Logf("✅ Complete end-to-end result: %s", finalContent)
	})
}
