package scenarios

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// RunToolCallsTest executes the tool calls test scenario
func RunToolCallsTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig config.ComprehensiveTestConfig) {
	if !testConfig.Scenarios.ToolCalls {
		t.Logf("Tool calls not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("ToolCalls", func(t *testing.T) {
		messages := []schemas.BifrostMessage{
			CreateBasicChatMessage("What's the weather like in New York? answer in celsius"),
		}

		params := MergeModelParameters(&schemas.ModelParameters{
			Tools:     &[]schemas.Tool{WeatherToolDefinition},
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
		require.Nilf(t, err, "Tool calls failed: %v", err)
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
					if toolCall.Function.Name != nil && *toolCall.Function.Name == "get_weather" {
						// Verify arguments contain location
						var args map[string]interface{}
						err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args)
						if err == nil {
							if _, hasLocation := args["location"]; hasLocation {
								foundValidToolCall = true
								t.Logf("✅ Tool call arguments for choice %d, tool call %d: %s", i, j, toolCall.Function.Arguments)
								break // Found valid tool call, can break from this inner loop
							}
						}
					}
				}
				if foundValidToolCall {
					break // Found valid tool call, can break from choices loop
				}
			}
		}

		if !foundValidToolCall {
			t.Logf("❌ No valid tool calls found in any choice, response: %s", GetResultContent(response))
		}
		require.True(t, foundValidToolCall, "Expected at least one choice to have valid tool call for 'get_weather' with 'location' argument")
	})
}
