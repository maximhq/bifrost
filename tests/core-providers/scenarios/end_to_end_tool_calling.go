package scenarios

import (
	"context"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// RunEnd2EndToolCallingTest executes the end-to-end tool calling test scenario
func RunEnd2EndToolCallingTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig config.ComprehensiveTestConfig) {
	if !testConfig.Scenarios.End2EndToolCalling {
		t.Logf("End-to-end tool calling not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("End2EndToolCalling", func(t *testing.T) {
		// Step 1: User asks for weather
		userMessage := CreateBasicChatMessage("What's the weather in San Francisco?")

		params := MergeModelParameters(&schemas.ModelParameters{
			Tools:     &[]schemas.Tool{WeatherToolDefinition},
			MaxTokens: bifrost.Ptr(150),
		}, testConfig.CustomParams)

		request := &schemas.BifrostRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ChatModel,
			Input: schemas.RequestInput{
				ChatCompletionInput: &[]schemas.ChatMessage{userMessage},
			},
			Params:    params,
			Fallbacks: testConfig.Fallbacks,
		}

		// Execute first request
		firstResponse, err := client.ChatCompletionRequest(ctx, request)
		require.Nilf(t, err, "First request failed: %v", err)
		require.NotNil(t, firstResponse)
		require.NotEmpty(t, firstResponse.Choices)

		// Find a choice with valid tool calls
		var toolCall schemas.ToolCall
		foundValidChoice := false

		for _, choice := range firstResponse.Choices {
			if choice.Message.AssistantMessage != nil &&
				choice.Message.AssistantMessage.ToolCalls != nil &&
				len(*choice.Message.AssistantMessage.ToolCalls) > 0 {

				firstToolCall := (*choice.Message.AssistantMessage.ToolCalls)[0]
				if firstToolCall.Function.Name != nil && *firstToolCall.Function.Name == "get_weather" {
					toolCall = firstToolCall
					foundValidChoice = true
					break
				}
			}
		}

		require.True(t, foundValidChoice, "Expected at least one choice to have valid tool call for 'get_weather'")

		// Step 2: Simulate tool execution and provide result
		toolResult := `{"temperature": "22", "unit": "celsius", "description": "Sunny with light clouds", "humidity": "65%"}`

		toolCallID := ""
		if toolCall.ID != nil {
			toolCallID = *toolCall.ID
		} else {
			toolCallID = *toolCall.Function.Name
		}

		require.NotEmpty(t, toolCallID, "toolCallID must not be empty")

		// Build conversation history with all choice messages from first response
		conversationMessages := []schemas.ChatMessage{
			userMessage,
		}

		// Add all choice messages from the first response
		for _, choice := range firstResponse.Choices {
			conversationMessages = append(conversationMessages, choice.Message)
		}

		// Add the tool result message
		conversationMessages = append(conversationMessages, CreateToolMessage(toolResult, toolCallID))

		secondRequest := &schemas.BifrostRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ChatModel,
			Input: schemas.RequestInput{
				ChatCompletionInput: &conversationMessages,
			},
			Params: MergeModelParameters(&schemas.ModelParameters{
				MaxTokens: bifrost.Ptr(200),
			}, testConfig.CustomParams),
			Fallbacks: testConfig.Fallbacks,
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
