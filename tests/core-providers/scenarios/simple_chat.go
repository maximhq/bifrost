package scenarios

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// RunSimpleChatTest executes the simple chat test scenario
func RunSimpleChatTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig config.ComprehensiveTestConfig) {
	if !testConfig.Scenarios.SimpleChat {
		t.Logf("Simple chat not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("SimpleChat", func(t *testing.T) {
		messages := []schemas.ChatMessage{
			CreateBasicChatMessage("Hello! What's the capital of France?"),
		}

		request := &schemas.BifrostRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ChatModel,
			Input: schemas.RequestInput{
				ChatCompletionInput: &messages,
			},
			Params: MergeModelParameters(&schemas.ModelParameters{
				MaxTokens: bifrost.Ptr(150),
			}, testConfig.CustomParams),
			Fallbacks: testConfig.Fallbacks,
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
