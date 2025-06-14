package scenarios

import (
	"context"
	config "core-providers-test/config"
	"encoding/json"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// RunToolCallsTest executes the tool calls test scenario
func RunToolCallsTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, config config.ComprehensiveTestConfig) {
	if !config.Scenarios.ToolCalls {
		t.Logf("Tool calls not supported for provider %s", config.Provider)
		return
	}

	t.Run("ToolCalls", func(t *testing.T) {
		messages := []schemas.BifrostMessage{
			CreateBasicChatMessage("What's the weather like in New York?"),
		}

		params := schemas.ModelParameters{
			Tools:     &[]schemas.Tool{WeatherToolDefinition},
			MaxTokens: bifrost.Ptr(150),
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
		require.Nilf(t, err, "Tool calls failed: %v", err)
		require.NotNil(t, response)
		require.NotEmpty(t, response.Choices)

		message := response.Choices[0].Message
		if message.AssistantMessage != nil && message.AssistantMessage.ToolCalls != nil {
			toolCalls := *message.AssistantMessage.ToolCalls
			require.NotEmpty(t, toolCalls, "Expected at least one tool call")

			toolCall := toolCalls[0]
			assert.NotNil(t, toolCall.Function.Name)
			assert.Equal(t, "get_weather", *toolCall.Function.Name)

			// Verify arguments contain location
			var args map[string]interface{}
			err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args)
			require.NoError(t, err)
			assert.Contains(t, args, "location")

			t.Logf("✅ Tool call arguments: %s", toolCall.Function.Arguments)
		} else {
			t.Logf("❌ No tool calls found, response: %s", GetResultContent(response))
		}
	})
}
