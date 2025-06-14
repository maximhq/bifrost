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

// RunSimpleChatTest executes the simple chat test scenario
func RunSimpleChatTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, config config.ComprehensiveTestConfig) {
	if !config.Scenarios.SimpleChat {
		t.Logf("Simple chat not supported for provider %s", config.Provider)
		return
	}

	t.Run("SimpleChat", func(t *testing.T) {
		messages := []schemas.BifrostMessage{
			CreateBasicChatMessage("Hello! What's the capital of France?"),
		}

		request := &schemas.BifrostRequest{
			Provider: config.Provider,
			Model:    config.ChatModel,
			Input: schemas.RequestInput{
				ChatCompletionInput: &messages,
			},
			Params: &schemas.ModelParameters{
				MaxTokens: bifrost.Ptr(100),
			},
			Fallbacks: config.Fallbacks,
		}

		response, err := client.ChatCompletionRequest(ctx, request)
		require.Nilf(t, err, "Simple chat failed: %v", err)
		require.NotNil(t, response)
		require.NotEmpty(t, response.Choices)

		content := GetResultContent(response)
		assert.NotEmpty(t, content, "Response content should not be empty")
		t.Logf("✅ Simple chat result: %s", content)
	})
}
