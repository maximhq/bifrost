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

// Allowlist entries prefixed with the "auto" policy must not be re-prefixed
// with an inference provider: "auto" is owned by no provider pass (the listing
// loop iterates INFERENCE_PROVIDERS, which excludes it). To keep the entry from
// being dropped entirely, it is emitted exactly once during the canonical first
// pass and skipped in every other pass, so it surfaces in the listing without
// being duplicated once per provider. The "auto/" prefix is preserved, so it
// stays routable: splitIntoModelProvider recognizes "auto" as a valid policy.
func TestToBifrostListModelsResponse_AllowlistWithAutoPolicySegment(t *testing.T) {
	t.Parallel()

	allowlist := schemas.WhiteList{"auto/deepseek-ai/DeepSeek-V4-Pro"}

	t.Run("canonical first pass emits the auto-policy entry exactly once", func(t *testing.T) {
		t.Parallel()
		response := &HuggingFaceListModelsResponse{Models: nil}
		result := response.ToBifrostListModelsResponse(schemas.HuggingFace, INFERENCE_PROVIDERS[0], allowlist, nil, nil, false)
		require.NotNil(t, result)
		require.Len(t, result.Data, 1)
		assert.Equal(t, "huggingface/auto/deepseek-ai/DeepSeek-V4-Pro", result.Data[0].ID,
			"the auto-policy entry keeps its prefix and is not re-wrapped with an inference provider")
	})

	t.Run("non-canonical passes do not duplicate the auto-policy entry", func(t *testing.T) {
		t.Parallel()
		require.Greater(t, len(INFERENCE_PROVIDERS), 1)
		nonCanonicalProvider := INFERENCE_PROVIDERS[1]

		response := &HuggingFaceListModelsResponse{Models: nil}
		result := response.ToBifrostListModelsResponse(schemas.HuggingFace, nonCanonicalProvider, allowlist, nil, nil, false)
		require.NotNil(t, result)
		assert.Empty(t, result.Data, "a non-canonical pass must not re-emit the auto-policy entry")
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
	// No matching entry in response.Models, so there is nothing to enrich from.
	assert.Nil(t, result.Data[0].HuggingFaceID)
	assert.Empty(t, result.Data[0].SupportedMethods)
}

// A backfilled entry whose model is actually present in response.Models (e.g.
// its provider-prefixed allowlist entry didn't string-match model.ModelID
// during the main filter pass) should still surface HuggingFaceID and
// SupportedMethods, not just ID/Name, since the data is available right there
// in the same response.
func TestToBifrostListModelsResponse_BackfillEnrichesHuggingFaceIDAndSupportedMethods(t *testing.T) {
	t.Parallel()

	allowlist := schemas.WhiteList{"featherless-ai/deepseek-ai/DeepSeek-V4-Pro"}
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

	got := result.Data[0]
	assert.Equal(t, "huggingface/featherless-ai/deepseek-ai/DeepSeek-V4-Pro", got.ID)
	require.NotNil(t, got.HuggingFaceID)
	assert.Equal(t, "abc123", *got.HuggingFaceID)
	assert.ElementsMatch(t, []string{
		string(schemas.ChatCompletionRequest),
		string(schemas.ChatCompletionStreamRequest),
		string(schemas.ResponsesRequest),
		string(schemas.ResponsesStreamRequest),
	}, got.SupportedMethods)
}

// If the backfilled model is present in response.Models but has no
// recognizable pipeline tag/tags, HuggingFaceID is still recovered but
// SupportedMethods stays unset rather than being forced to an empty slice.
func TestToBifrostListModelsResponse_BackfillEnrichesHuggingFaceIDOnlyWhenMethodsUnknown(t *testing.T) {
	t.Parallel()

	allowlist := schemas.WhiteList{"deepseek-ai/DeepSeek-V4-Pro"}
	response := &HuggingFaceListModelsResponse{
		Models: []HuggingFaceModel{
			{
				ID:          "abc123",
				ModelID:     "deepseek-ai/DeepSeek-V4-Pro",
				PipelineTag: "some-unrecognized-pipeline",
			},
		},
	}

	result := response.ToBifrostListModelsResponse(schemas.HuggingFace, featherlessAI, allowlist, nil, nil, false)
	require.NotNil(t, result)
	require.Len(t, result.Data, 1)

	got := result.Data[0]
	require.NotNil(t, got.HuggingFaceID)
	assert.Equal(t, "abc123", *got.HuggingFaceID)
	assert.Nil(t, got.SupportedMethods)
}
