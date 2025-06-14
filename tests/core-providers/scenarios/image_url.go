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

// RunImageURLTest executes the image URL test scenario
func RunImageURLTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, config config.ComprehensiveTestConfig) {
	if !config.Scenarios.ImageURL {
		t.Logf("Image URL not supported for provider %s", config.Provider)
		return
	}

	t.Run("ImageURL", func(t *testing.T) {
		messages := []schemas.BifrostMessage{
			CreateImageMessage("What do you see in this image?", TestImageURL),
		}

		request := &schemas.BifrostRequest{
			Provider: config.Provider,
			Model:    config.ChatModel,
			Input: schemas.RequestInput{
				ChatCompletionInput: &messages,
			},
			Params: &schemas.ModelParameters{
				MaxTokens: bifrost.Ptr(200),
			},
			Fallbacks: config.Fallbacks,
		}

		response, err := client.ChatCompletionRequest(ctx, request)
		require.Nilf(t, err, "Image URL test failed: %v", err)
		require.NotNil(t, response)
		require.NotEmpty(t, response.Choices)

		content := GetResultContent(response)
		assert.NotEmpty(t, content, "Response content should not be empty")
		// Should mention something about the ant in the image
		assert.True(t, strings.Contains(strings.ToLower(content), "ant") ||
			strings.Contains(strings.ToLower(content), "insect"),
			"Response should identify the ant/insect in the image")

		t.Logf("✅ Image URL result: %s", content)
	})
}
