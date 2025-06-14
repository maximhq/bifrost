package scenarios

import (
	"context"
	"testing"

	config "core-providers-test/config"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// RunAutomaticFunctionCallingTest executes the automatic function calling test scenario
func RunAutomaticFunctionCallingTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, config config.ComprehensiveTestConfig) {
	if !config.Scenarios.AutomaticFunctionCall {
		t.Logf("Automatic function calling not supported for provider %s", config.Provider)
		return
	}

	t.Run("AutomaticFunctionCalling", func(t *testing.T) {
		messages := []schemas.BifrostMessage{
			CreateBasicChatMessage("Get the current time in UTC timezone"),
		}

		params := schemas.ModelParameters{
			Tools: &[]schemas.Tool{TimeToolDefinition},
			ToolChoice: &schemas.ToolChoice{
				Type: schemas.ToolChoiceTypeFunction,
				Function: schemas.ToolChoiceFunction{
					Name: "get_current_time",
				},
			},
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
		require.Nilf(t, err, "Automatic function calling failed: %v", err)
		require.NotNil(t, response)
		require.NotEmpty(t, response.Choices)

		message := response.Choices[0].Message
		if message.AssistantMessage != nil && message.AssistantMessage.ToolCalls != nil {
			toolCalls := *message.AssistantMessage.ToolCalls
			require.NotEmpty(t, toolCalls, "Expected automatic tool call")

			toolCall := toolCalls[0]
			assert.NotNil(t, toolCall.Function.Name)
			assert.Equal(t, "get_current_time", *toolCall.Function.Name)

			t.Logf("✅ Automatic function call: %s", toolCall.Function.Arguments)
		} else {
			t.Logf("❌ No automatic tool calls, response: %s", GetResultContent(response))
		}
	})
}
