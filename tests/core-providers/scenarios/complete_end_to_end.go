package scenarios

import (
	"context"
	config "core-providers-test/config"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// RunCompleteEnd2EndTest executes the complete end-to-end test scenario
func RunCompleteEnd2EndTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, config config.ComprehensiveTestConfig) {
	if !config.Scenarios.CompleteEnd2End {
		t.Logf("Complete end-to-end not supported for provider %s", config.Provider)
		return
	}

	t.Run("CompleteEnd2End", func(t *testing.T) {
		// Multi-step conversation with tools and images
		userMessage1 := CreateBasicChatMessage("Hi, I'm planning a trip. Can you help me get the weather in Paris?")

		request1 := &schemas.BifrostRequest{
			Provider: config.Provider,
			Model:    config.ChatModel,
			Input: schemas.RequestInput{
				ChatCompletionInput: &[]schemas.BifrostMessage{userMessage1},
			},
			Params: &schemas.ModelParameters{
				Tools:     &[]schemas.Tool{WeatherToolDefinition},
				MaxTokens: bifrost.Ptr(150),
			},
			Fallbacks: config.Fallbacks,
		}

		response1, err := client.ChatCompletionRequest(ctx, request1)
		require.Nilf(t, err, "First end-to-end request failed: %v", err)
		require.NotNil(t, response1)
		require.NotEmpty(t, response1.Choices)

		t.Logf("✅ First response: %s", GetResultContent(response1))

		// If tool was called, simulate result and continue conversation
		var conversationHistory []schemas.BifrostMessage
		conversationHistory = append(conversationHistory, userMessage1)
		conversationHistory = append(conversationHistory, response1.Choices[0].Message)

		message := response1.Choices[0].Message
		if message.AssistantMessage != nil && message.AssistantMessage.ToolCalls != nil {
			toolCalls := *message.AssistantMessage.ToolCalls
			if len(toolCalls) > 0 {
				// Simulate tool result
				toolResult := `{"temperature": "18", "unit": "celsius", "description": "Partly cloudy", "humidity": "70%"}`
				toolCallID := ""
				if toolCalls[0].ID != nil {
					toolCallID = *toolCalls[0].ID
				} else {
					toolCallID = *toolCalls[0].Function.Name
				}
				toolMessage := CreateToolMessage(toolResult, toolCallID)
				conversationHistory = append(conversationHistory, toolMessage)
			}
		}

		// Continue with follow-up
		followUpMessage := CreateBasicChatMessage("Thanks! Now can you tell me about this travel image?")
		if config.Scenarios.ImageURL {
			followUpMessage = CreateImageMessage("Thanks! Now can you tell me what you see in this travel-related image?", TestImageURL)
		}
		conversationHistory = append(conversationHistory, followUpMessage)

		finalRequest := &schemas.BifrostRequest{
			Provider: config.Provider,
			Model:    config.ChatModel,
			Input: schemas.RequestInput{
				ChatCompletionInput: &conversationHistory,
			},
			Params: &schemas.ModelParameters{
				MaxTokens: bifrost.Ptr(200),
			},
			Fallbacks: config.Fallbacks,
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
