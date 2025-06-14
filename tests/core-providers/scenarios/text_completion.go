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

// RunTextCompletionTest tests text completion functionality
func RunTextCompletionTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, config config.ComprehensiveTestConfig) {
	if !config.Scenarios.TextCompletion || config.TextModel == "" {
		t.Logf("⏭️ Text completion not supported for provider %s", config.Provider)
		return
	}

	t.Run("TextCompletion", func(t *testing.T) {
		request := &schemas.BifrostRequest{
			Provider: config.Provider,
			Model:    config.TextModel,
			Input: schemas.RequestInput{
				TextCompletionInput: bifrost.Ptr("The future of artificial intelligence is"),
			},
			Params: &schemas.ModelParameters{
				MaxTokens: bifrost.Ptr(50),
			},
			Fallbacks: config.Fallbacks,
		}

		response, err := client.TextCompletionRequest(ctx, request)
		require.Nilf(t, err, "Text completion failed: %v", err)
		require.NotNil(t, response)
		require.NotEmpty(t, response.Choices)

		content := GetResultContent(response)
		assert.NotEmpty(t, content, "Response content should not be empty")
		t.Logf("✅ Text completion result: %s", content)
	})
}
