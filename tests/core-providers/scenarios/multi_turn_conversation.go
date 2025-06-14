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

// RunMultiTurnConversationTest executes the multi-turn conversation test scenario
func RunMultiTurnConversationTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, config config.ComprehensiveTestConfig) {
	if !config.Scenarios.MultiTurnConversation {
		t.Logf("Multi-turn conversation not supported for provider %s", config.Provider)
		return
	}

	t.Run("MultiTurnConversation", func(t *testing.T) {
		// First message
		messages1 := []schemas.BifrostMessage{
			CreateBasicChatMessage("My name is Alice. Remember this."),
		}

		request1 := &schemas.BifrostRequest{
			Provider: config.Provider,
			Model:    config.ChatModel,
			Input: schemas.RequestInput{
				ChatCompletionInput: &messages1,
			},
			Params: &schemas.ModelParameters{
				MaxTokens: bifrost.Ptr(100),
			},
			Fallbacks: config.Fallbacks,
		}

		response1, err := client.ChatCompletionRequest(ctx, request1)
		require.Nilf(t, err, "First conversation turn failed: %v", err)
		require.NotNil(t, response1)
		require.NotEmpty(t, response1.Choices)

		// Second message with conversation history
		messages2 := []schemas.BifrostMessage{
			CreateBasicChatMessage("My name is Alice. Remember this."),
			response1.Choices[0].Message,
			CreateBasicChatMessage("What's my name?"),
		}

		request2 := &schemas.BifrostRequest{
			Provider: config.Provider,
			Model:    config.ChatModel,
			Input: schemas.RequestInput{
				ChatCompletionInput: &messages2,
			},
			Params: &schemas.ModelParameters{
				MaxTokens: bifrost.Ptr(100),
			},
			Fallbacks: config.Fallbacks,
		}

		response2, err := client.ChatCompletionRequest(ctx, request2)
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
