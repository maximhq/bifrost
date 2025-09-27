package scenarios

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	"slices"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// getKeysFromMap returns the keys of a map[string]bool as a slice
func getKeysFromMap(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// RunMultipleToolCallsTest executes the multiple tool calls test scenario
func RunMultipleToolCallsTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig config.ComprehensiveTestConfig) {
	if !testConfig.Scenarios.MultipleToolCalls {
		t.Logf("Multiple tool calls not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("MultipleToolCalls", func(t *testing.T) {
		messages := []schemas.ChatMessage{
			CreateBasicChatMessage("I need to know the weather in London and also calculate 15 * 23. Can you help with both?"),
		}

		params := MergeModelParameters(&schemas.ModelParameters{
			Tools:     &[]schemas.Tool{WeatherToolDefinition, CalculatorToolDefinition},
			MaxTokens: bifrost.Ptr(200),
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
		require.Nilf(t, err, "Multiple tool calls failed: %v", err)
		require.NotNil(t, response)
		require.NotEmpty(t, response.Choices)

		// Find at least one choice with multiple valid tool calls
		expectedToolNames := []string{"get_weather", "calculate"}
		foundValidMultipleToolCalls := false
		for choiceIdx, choice := range response.Choices {
			message := choice.Message
			if message.AssistantMessage != nil && message.AssistantMessage.ToolCalls != nil {
				toolCalls := *message.AssistantMessage.ToolCalls
				if len(toolCalls) >= 2 {
					validToolCalls := 0
					foundToolNames := make(map[string]bool)

					for _, toolCall := range toolCalls {
						if toolCall.Function.Name != nil {
							toolName := *toolCall.Function.Name
							// Check if this is one of the expected tool names
							isExpected := false
							for _, expectedName := range expectedToolNames {
								if toolName == expectedName {
									isExpected = true
									foundToolNames[toolName] = true
									break
								}
							}
							if isExpected {
								validToolCalls++
							}
						}
					}

					// Require at least 2 valid tool calls with expected names
					if validToolCalls >= 2 {
						foundValidMultipleToolCalls = true
						t.Logf("✅ Number of tool calls for choice %d: %d", choiceIdx, len(toolCalls))
						t.Logf("✅ Found expected tools: %v", getKeysFromMap(foundToolNames))

						for i, toolCall := range toolCalls {
							if toolCall.Function.Name != nil {
								toolName := *toolCall.Function.Name
								// Validate that each tool name is expected
								isExpected := slices.Contains(expectedToolNames, toolName)
								require.True(t, isExpected, "Unexpected tool call '%s' - expected one of %v", toolName, expectedToolNames)
								t.Logf("✅ Tool call %d for choice %d: %s with args: %s", i+1, choiceIdx, toolName, toolCall.Function.Arguments)
							}
						}
						break // Found a valid choice with multiple tool calls
					}
				}
			}
		}

		require.True(t, foundValidMultipleToolCalls, "Expected at least one choice to have 2 or more valid tool calls. Response: %s", GetResultContent(response))
	})
}
