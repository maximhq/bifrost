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

// RunImageURLTest executes the image URL test scenario
func RunImageURLTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig config.ComprehensiveTestConfig) {
	if !testConfig.Scenarios.ImageURL {
		t.Logf("Image URL not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("ImageURL", func(t *testing.T) {
		messages := []schemas.ChatMessage{
			CreateImageMessage("What do you see in this image?", TestImageURL),
		}

		request := &schemas.BifrostRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ChatModel,
			Input: schemas.RequestInput{
				ChatCompletionInput: &messages,
			},
			Params: MergeModelParameters(&schemas.ModelParameters{
				MaxTokens: bifrost.Ptr(200),
			}, testConfig.CustomParams),
			Fallbacks: testConfig.Fallbacks,
		}

		response, err := client.ChatCompletionRequest(ctx, request)
		require.Nilf(t, err, "Image URL test failed: %v", err)
		require.NotNil(t, response)
		require.NotEmpty(t, response.Choices)

		content := GetResultContent(response)
		assert.NotEmpty(t, content, "Response content should not be empty")
		// Should mention something about the ant in the image
		lowerContent := strings.ToLower(content)
		assert.True(t, strings.Contains(lowerContent, "ant") ||
			strings.Contains(lowerContent, "insect"),
			"Response should identify the ant/insect in the image")

		t.Logf("✅ Image URL result: %s", content)
	})
}
