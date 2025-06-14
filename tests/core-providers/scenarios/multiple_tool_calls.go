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

// RunMultipleToolCallsTest executes the multiple tool calls test scenario
func RunMultipleToolCallsTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, config config.ComprehensiveTestConfig) {
	if !config.Scenarios.MultipleToolCalls {
		t.Logf("Multiple tool calls not supported for provider %s", config.Provider)
		return
	}

	t.Run("MultipleToolCalls", func(t *testing.T) {
		messages := []schemas.BifrostMessage{
			CreateBasicChatMessage("I need to know the weather in London and also calculate 15 * 23. Can you help with both?"),
		}

		params := schemas.ModelParameters{
			Tools:     &[]schemas.Tool{WeatherToolDefinition, WeatherToolDefinition},
			MaxTokens: bifrost.Ptr(200),
		}

		request := &schemas.BifrostRequest{
			Provider: config.Provider,
			Model:    config.ChatModel,
			Input: schemas.RequestInput{
				ChatCompletionInput: &messages,
			},
			Params:    &params,
			Fallbacks: config.Fallbacks,
		}

		response, err := client.ChatCompletionRequest(ctx, request)
		require.Nilf(t, err, "Multiple tool calls failed: %v", err)
		require.NotNil(t, response)
		require.NotEmpty(t, response.Choices)

		message := response.Choices[0].Message
		if message.AssistantMessage != nil && message.AssistantMessage.ToolCalls != nil {
			toolCalls := *message.AssistantMessage.ToolCalls
			t.Logf("✅ Number of tool calls: %d", len(toolCalls))

			for i, toolCall := range toolCalls {
				assert.NotNil(t, toolCall.Function.Name)
				t.Logf("✅ Tool call %d: %s with args: %s", i+1, *toolCall.Function.Name, toolCall.Function.Arguments)
			}
		} else {
			t.Logf("No tool calls found, response: %s", GetResultContent(response))
		}
	})
}
