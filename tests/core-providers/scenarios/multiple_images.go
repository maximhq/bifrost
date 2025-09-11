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

// RunMultipleImagesTest executes the multiple images test scenario
func RunMultipleImagesTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig config.ComprehensiveTestConfig) {
	if !testConfig.Scenarios.MultipleImages {
		t.Logf("Multiple images not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("MultipleImages", func(t *testing.T) {
		messages := []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: schemas.ChatMessageContent{
					ContentBlocks: &[]schemas.ContentBlock{
						{
							Type: schemas.ContentBlockTypeText,
							Text: bifrost.Ptr("Compare these two images - what are the similarities and differences?"),
						},
						{
							Type: schemas.ContentBlockTypeImage,
							ImageURL: &schemas.ImageURLStruct{
								URL: TestImageURL,
							},
						},
						{
							Type: schemas.ContentBlockTypeImage,
							ImageURL: &schemas.ImageURLStruct{
								URL: TestImageBase64,
							},
						},
					},
				},
			},
		}

		request := &schemas.BifrostRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ChatModel,
			Input: schemas.RequestInput{
				ChatCompletionInput: &messages,
			},
			Params: MergeModelParameters(&schemas.ModelParameters{
				MaxTokens: bifrost.Ptr(300),
			}, testConfig.CustomParams),
			Fallbacks: testConfig.Fallbacks,
		}

		response, err := client.ChatCompletionRequest(ctx, request)
		require.Nilf(t, err, "Multiple images test failed: %v", err)
		require.NotNil(t, response)
		require.NotEmpty(t, response.Choices)

		content := GetResultContent(response)
		assert.NotEmpty(t, content, "Response content should not be empty")

		t.Logf("✅ Multiple images result: %s", content)
	})
}
