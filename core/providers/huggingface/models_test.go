package huggingface

import (
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression tests for https://github.com/maximhq/bifrost/issues/4215.
//
// Allowlist entries selected from a previous ListModels response carry an
// inference-provider segment (e.g. "featherless-ai/org/model"). The backfill
// re-wrap used to blindly prepend the current inference provider, producing
// IDs like "huggingface/cohere/featherless-ai/org/model" that duplicate the
// provider segment and fail request routing.
func TestToBifrostListModelsResponse_AllowlistWithInferenceProviderSegment(t *testing.T) {
	t.Parallel()

	allowlist := schemas.WhiteList{"featherless-ai/deepseek-ai/DeepSeek-V4-Pro"}

	t.Run("matching provider emits single correctly-prefixed entry", func(t *testing.T) {
		t.Parallel()
		response := &HuggingFaceListModelsResponse{
			Models: []HuggingFaceModel{
				{
					ID:          "abc123",
					ModelID:     "deepseek-ai/DeepSeek-V4-Pro",
					PipelineTag: "conversational",
				},
			},
		}

		result := response.ToBifrostListModelsResponse(schemas.HuggingFace, featherlessAI, allowlist, nil, nil, false)
		require.NotNil(t, result)
		require.Len(t, result.Data, 1)
		assert.Equal(t, "huggingface/featherless-ai/deepseek-ai/DeepSeek-V4-Pro", result.Data[0].ID)
	})

	t.Run("other providers do not duplicate the entry", func(t *testing.T) {
		t.Parallel()
		response := &HuggingFaceListModelsResponse{
			Models: []HuggingFaceModel{
				{
					ID:          "def456",
					ModelID:     "CohereLabs/aya-vision-32b",
					PipelineTag: "conversational",
				},
			},
		}

		result := response.ToBifrostListModelsResponse(schemas.HuggingFace, cohere, allowlist, nil, nil, false)
		require.NotNil(t, result)
		assert.Empty(t, result.Data, "an allowlist entry pinned to featherless-ai must not be backfilled under cohere")
	})
}

// Entries without an inference-provider segment keep the existing backfill
// behavior: the current inference provider is prepended.
func TestToBifrostListModelsResponse_BackfillWithoutInferenceProviderSegment(t *testing.T) {
	t.Parallel()

	response := &HuggingFaceListModelsResponse{Models: nil}
	allowlist := schemas.WhiteList{"deepseek-ai/DeepSeek-V4-Pro"}

	result := response.ToBifrostListModelsResponse(schemas.HuggingFace, featherlessAI, allowlist, nil, nil, false)
	require.NotNil(t, result)
	require.Len(t, result.Data, 1)
	assert.Equal(t, "huggingface/featherless-ai/deepseek-ai/DeepSeek-V4-Pro", result.Data[0].ID)
}
