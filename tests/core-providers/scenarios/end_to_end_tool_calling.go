package scenarios

import (
	"context"
	config "core-providers-test/config"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// RunEnd2EndToolCallingTest executes the end-to-end tool calling test scenario
func RunEnd2EndToolCallingTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, config config.ComprehensiveTestConfig) {
	if !config.Scenarios.End2EndToolCalling {
		t.Logf("End-to-end tool calling not supported for provider %s", config.Provider)
		return
	}

	t.Run("End2EndToolCalling", func(t *testing.T) {
		// Step 1: User asks for weather
		userMessage := CreateBasicChatMessage("What's the weather in San Francisco?")

		params := schemas.ModelParameters{
			Tools:     &[]schemas.Tool{WeatherToolDefinition},
			MaxTokens: bifrost.Ptr(150),
		}

		request := &schemas.BifrostRequest{
			Provider: config.Provider,
			Model:    config.ChatModel,
			Input: schemas.RequestInput{
				ChatCompletionInput: &[]schemas.BifrostMessage{userMessage},
			},
			Params:    &params,
			Fallbacks: config.Fallbacks,
		}

		// Execute first request
		firstResponse, err := client.ChatCompletionRequest(ctx, request)
		require.Nilf(t, err, "First request failed: %v", err)
		require.NotNil(t, firstResponse)
		require.NotEmpty(t, firstResponse.Choices)

		message := firstResponse.Choices[0].Message
		require.NotNil(t, message.AssistantMessage)
		require.NotNil(t, message.AssistantMessage.ToolCalls)
		require.NotEmpty(t, *message.AssistantMessage.ToolCalls)

		toolCall := (*message.AssistantMessage.ToolCalls)[0]
		require.NotNil(t, toolCall.Function.Name)
		assert.Equal(t, "get_weather", *toolCall.Function.Name)

		// Step 2: Simulate tool execution and provide result
		toolResult := `{"temperature": "22", "unit": "celsius", "description": "Sunny with light clouds", "humidity": "65%"}`

		toolCallID := ""
		if toolCall.ID != nil {
			toolCallID = *toolCall.ID
		} else {
			toolCallID = *toolCall.Function.Name
		}

		conversationMessages := []schemas.BifrostMessage{
			userMessage,
			message,
			CreateToolMessage(toolResult, toolCallID),
		}

		secondRequest := &schemas.BifrostRequest{
			Provider: config.Provider,
			Model:    config.ChatModel,
			Input: schemas.RequestInput{
				ChatCompletionInput: &conversationMessages,
			},
			Params: &schemas.ModelParameters{
				MaxTokens: bifrost.Ptr(200),
			},
			Fallbacks: config.Fallbacks,
		}

		// Execute second request
		finalResponse, err := client.ChatCompletionRequest(ctx, secondRequest)
		require.Nilf(t, err, "Second request failed: %v", err)
		require.NotNil(t, finalResponse)
		require.NotEmpty(t, finalResponse.Choices)

		content := GetResultContent(finalResponse)
		require.NotEmpty(t, content, "Response content should not be empty")

		// Verify response contains expected information
		assert.Contains(t, strings.ToLower(content), "san francisco", "Response should mention San Francisco")
		assert.Contains(t, content, "22", "Response should mention temperature")
		assert.Contains(t, strings.ToLower(content), "sunny", "Response should mention weather description")

		t.Logf("✅ End-to-end tool calling result: %s", content)
	})
}
