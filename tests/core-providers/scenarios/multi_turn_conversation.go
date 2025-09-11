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

// RunMultiTurnConversationTest executes the multi-turn conversation test scenario
func RunMultiTurnConversationTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig config.ComprehensiveTestConfig) {
	if !testConfig.Scenarios.MultiTurnConversation {
		t.Logf("Multi-turn conversation not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("MultiTurnConversation", func(t *testing.T) {
		// First message
		userMessage1 := CreateBasicChatMessage("My name is Alice. Remember this.")
		messages1 := []schemas.ChatMessage{
			userMessage1,
		}

		firstRequest := &schemas.BifrostRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ChatModel,
			Input: schemas.RequestInput{
				ChatCompletionInput: &messages1,
			},
			Params: MergeModelParameters(&schemas.ModelParameters{
				MaxTokens: bifrost.Ptr(150),
			}, testConfig.CustomParams),
			Fallbacks: testConfig.Fallbacks,
		}

		response1, err := client.ChatCompletionRequest(ctx, firstRequest)
		require.Nilf(t, err, "First conversation turn failed: %v", err)
		require.NotNil(t, response1)
		require.NotEmpty(t, response1.Choices)

		// Second message with conversation history
		// Build conversation history with all choice messages
		messages2 := []schemas.ChatMessage{
			userMessage1,
		}

		// Add all choice messages from the first response
		for _, choice := range response1.Choices {
			messages2 = append(messages2, choice.Message)
		}

		// Add the follow-up question
		messages2 = append(messages2, CreateBasicChatMessage("What's my name?"))

		secondRequest := &schemas.BifrostRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ChatModel,
			Input: schemas.RequestInput{
				ChatCompletionInput: &messages2,
			},
			Params: MergeModelParameters(&schemas.ModelParameters{
				MaxTokens: bifrost.Ptr(150),
			}, testConfig.CustomParams),
			Fallbacks: testConfig.Fallbacks,
		}

		response2, err := client.ChatCompletionRequest(ctx, secondRequest)
		require.Nilf(t, err, "Second conversation turn failed: %v", err)
		require.NotNil(t, response2)
		require.NotEmpty(t, response2.Choices)

		content := GetResultContent(response2)
		assert.NotEmpty(t, content, "Response content should not be empty")
		// Check if the model remembered the name
		assert.Contains(t, strings.ToLower(content), "alice", "Model should remember the name Alice")
		t.Logf("✅ Multi-turn conversation result: %s", content)
	})
}
