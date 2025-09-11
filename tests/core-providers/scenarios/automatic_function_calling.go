package scenarios

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// RunAutomaticFunctionCallingTest executes the automatic function calling test scenario
func RunAutomaticFunctionCallingTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig config.ComprehensiveTestConfig) {
	if !testConfig.Scenarios.AutomaticFunctionCall {
		t.Logf("Automatic function calling not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("AutomaticFunctionCalling", func(t *testing.T) {
		messages := []schemas.ChatMessage{
			CreateBasicChatMessage("Get the current time in UTC timezone"),
		}

		params := MergeModelParameters(&schemas.ModelParameters{
			Tools: &[]schemas.Tool{TimeToolDefinition},
			ToolChoice: &schemas.ToolChoice{
				ToolChoiceStruct: &schemas.ToolChoiceStruct{
					Type: schemas.ToolChoiceTypeFunction,
					Function: schemas.ToolChoiceFunction{
						Name: "get_current_time",
					},
				},
			},
			MaxTokens: bifrost.Ptr(150),
		}, testConfig.CustomParams)

		request := &schemas.BifrostRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ChatModel,
			Input: schemas.RequestInput{
				ChatCompletionInput: &messages,
			},
			Params:    params,
			Fallbacks: testConfig.Fallbacks,
		}

		response, err := client.ChatCompletionRequest(ctx, request)
		require.Nilf(t, err, "Automatic function calling failed: %v", err)
		require.NotNil(t, response)
		require.NotEmpty(t, response.Choices)

		// Find at least one choice with valid tool calls
		foundValidToolCall := false
		for i, choice := range response.Choices {
			message := choice.Message
			if message.AssistantMessage != nil && message.AssistantMessage.ToolCalls != nil {
				toolCalls := *message.AssistantMessage.ToolCalls
				// Iterate through all tool calls, not just the first one
				for j, toolCall := range toolCalls {
					if toolCall.Function.Name != nil && *toolCall.Function.Name == "get_current_time" {
						foundValidToolCall = true
						t.Logf("✅ Automatic function call for choice %d, tool call %d: %s", i, j, toolCall.Function.Arguments)
						break // Found valid tool call, can break from this inner loop
					}
				}
				if foundValidToolCall {
					break // Found valid tool call, can break from choices loop
				}
			}
		}

		require.True(t, foundValidToolCall, "Expected at least one choice to have automatic tool call for 'get_current_time'. Response: %s", GetResultContent(response))
	})
}
