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

// RunImageBase64Test executes the image base64 test scenario
func RunImageBase64Test(t *testing.T, client *bifrost.Bifrost, ctx context.Context, config config.ComprehensiveTestConfig) {
	if !config.Scenarios.ImageBase64 {
		t.Logf("Image base64 not supported for provider %s", config.Provider)
		return
	}

	t.Run("ImageBase64", func(t *testing.T) {
		messages := []schemas.BifrostMessage{
			CreateImageMessage("Describe this image briefly", TestImageBase64),
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
		require.Nilf(t, err, "Image base64 test failed: %v", err)
		require.NotNil(t, response)
		require.NotEmpty(t, response.Choices)

		content := GetResultContent(response)
		assert.NotEmpty(t, content, "Response content should not be empty")

		t.Logf("✅ Image base64 result: %s", content)
	})
}
