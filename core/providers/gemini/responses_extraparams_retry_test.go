package gemini_test

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/providers/gemini"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression: the Responses conversion runs once per retry/fallback attempt on the
// same Bifrost request. It used to delete the ExtraParams it mapped into
// generationConfig, so the second attempt was sent without mediaResolution
// (and without topK, penalties and stop sequences).
func TestToGeminiResponsesRequest_GenerationConfigExtraParamsSurviveRetries(t *testing.T) {
	bifrostReq := &schemas.BifrostResponsesRequest{
		Provider: schemas.Vertex,
		Model:    "gemini-3.1-flash-lite",
		Params: &schemas.ResponsesParameters{
			ExtraParams: map[string]interface{}{
				"media_resolution":   "MEDIA_RESOLUTION_HIGH",
				"top_k":              40,
				"frequency_penalty":  0.5,
				"presence_penalty":   0.25,
				"stop_sequences":     []string{"END"},
				"custom_passthrough": "keep-me",
			},
		},
	}

	for attempt := 1; attempt <= 3; attempt++ {
		geminiReq, err := gemini.ToGeminiResponsesRequest(nil, bifrostReq)
		require.NoError(t, err, "attempt %d", attempt)
		require.NotNil(t, geminiReq, "attempt %d", attempt)

		config := geminiReq.GenerationConfig
		assert.Equal(t, "MEDIA_RESOLUTION_HIGH", config.MediaResolution, "attempt %d", attempt)
		require.NotNil(t, config.TopK, "attempt %d", attempt)
		assert.Equal(t, 40, *config.TopK, "attempt %d", attempt)
		require.NotNil(t, config.FrequencyPenalty, "attempt %d", attempt)
		assert.InDelta(t, 0.5, *config.FrequencyPenalty, 1e-9, "attempt %d", attempt)
		require.NotNil(t, config.PresencePenalty, "attempt %d", attempt)
		assert.InDelta(t, 0.25, *config.PresencePenalty, 1e-9, "attempt %d", attempt)
		assert.Equal(t, []string{"END"}, config.StopSequences, "attempt %d", attempt)

		// Consumed keys must not leak onto the wire as unknown snake_case fields,
		// while unrelated passthrough params are still forwarded.
		wire := geminiReq.GetExtraParams()
		assert.Equal(t, map[string]interface{}{"custom_passthrough": "keep-me"}, wire, "attempt %d", attempt)
	}

	// The Bifrost request itself must be left intact for the next attempt.
	assert.Equal(t, "MEDIA_RESOLUTION_HIGH", bifrostReq.Params.ExtraParams["media_resolution"])
	assert.Equal(t, 40, bifrostReq.Params.ExtraParams["top_k"])
	assert.Len(t, bifrostReq.Params.ExtraParams, 6)
}

// The GenAI inbound path stores mediaResolution in ExtraParams; a retried request
// must still carry it in the outbound generationConfig.
func TestGenAIMediaResolution_PreservedOnSecondConversion(t *testing.T) {
	geminiReq := &gemini.GeminiGenerationRequest{
		Model: "gemini-2.5-flash",
		GenerationConfig: gemini.GenerationConfig{
			MediaResolution: "MEDIA_RESOLUTION_LOW",
		},
	}

	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bifrostReq := geminiReq.ToBifrostResponsesRequest(bifrostCtx)
	require.NotNil(t, bifrostReq.Params)

	first, err := gemini.ToGeminiResponsesRequest(nil, bifrostReq)
	require.NoError(t, err)
	second, err := gemini.ToGeminiResponsesRequest(nil, bifrostReq)
	require.NoError(t, err)

	assert.Equal(t, "MEDIA_RESOLUTION_LOW", first.GenerationConfig.MediaResolution)
	assert.Equal(t, "MEDIA_RESOLUTION_LOW", second.GenerationConfig.MediaResolution,
		"mediaResolution must survive a second conversion of the same request (retry)")
	assert.NotContains(t, second.GetExtraParams(), "media_resolution")
}

// safety_settings and cached_content are not generationConfig keys, but they are
// removed from the outbound ExtraParams after being mapped to dedicated fields.
// Without a copy, that removal hit the Bifrost request and a retry lost them.
func TestToGeminiResponsesRequest_SafetySettingsAndCachedContentSurviveRetries(t *testing.T) {
	bifrostReq := &schemas.BifrostResponsesRequest{
		Provider: schemas.Gemini,
		Model:    "gemini-2.5-flash",
		Params: &schemas.ResponsesParameters{
			ExtraParams: map[string]interface{}{
				"safety_settings": []interface{}{
					map[string]interface{}{
						"category":  "HARM_CATEGORY_HARASSMENT",
						"threshold": "BLOCK_NONE",
					},
				},
				"cached_content":     "cachedContents/abc123",
				"custom_passthrough": "keep-me",
			},
		},
	}

	for attempt := 1; attempt <= 3; attempt++ {
		geminiReq, err := gemini.ToGeminiResponsesRequest(nil, bifrostReq)
		require.NoError(t, err, "attempt %d", attempt)
		require.NotNil(t, geminiReq, "attempt %d", attempt)

		require.Len(t, geminiReq.SafetySettings, 1, "attempt %d", attempt)
		assert.Equal(t, "HARM_CATEGORY_HARASSMENT", geminiReq.SafetySettings[0].Category, "attempt %d", attempt)
		assert.Equal(t, "BLOCK_NONE", geminiReq.SafetySettings[0].Threshold, "attempt %d", attempt)
		assert.Equal(t, "cachedContents/abc123", geminiReq.CachedContent, "attempt %d", attempt)
		assert.Equal(t, map[string]interface{}{"custom_passthrough": "keep-me"}, geminiReq.GetExtraParams(), "attempt %d", attempt)
	}

	assert.Contains(t, bifrostReq.Params.ExtraParams, "safety_settings")
	assert.Contains(t, bifrostReq.Params.ExtraParams, "cached_content")
	assert.Len(t, bifrostReq.Params.ExtraParams, 3)
}

// The GenAI inbound path maps candidateCount and responseLogprobs into ExtraParams and
// logprobs into TopLogProbs. The outbound Responses converter used to read none of them,
// so a bare-model /genai call silently lost all three. They must land back on
// generationConfig unclamped (an out-of-range logprobs is Google's to reject), and the
// snake_case keys must not leak onto the wire as unknown top-level fields.
func TestGenAICandidateCountAndLogprobs_ReachGenerationConfig(t *testing.T) {
	geminiReq := &gemini.GeminiGenerationRequest{
		Model: "gemini-2.5-flash",
		GenerationConfig: gemini.GenerationConfig{
			CandidateCount:   2,
			ResponseLogprobs: true,
			Logprobs:         schemas.Ptr(int32(99)),
			Seed:             schemas.Ptr(int32(7)),
		},
	}

	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bifrostReq := geminiReq.ToBifrostResponsesRequest(bifrostCtx)
	require.NotNil(t, bifrostReq.Params)

	for attempt := 1; attempt <= 2; attempt++ {
		out, err := gemini.ToGeminiResponsesRequest(nil, bifrostReq)
		require.NoError(t, err, "attempt %d", attempt)

		config := out.GenerationConfig
		assert.Equal(t, int32(2), config.CandidateCount, "attempt %d", attempt)
		assert.True(t, config.ResponseLogprobs, "attempt %d", attempt)
		require.NotNil(t, config.Logprobs, "attempt %d", attempt)
		assert.Equal(t, int32(99), *config.Logprobs, "logprobs must not be clamped (attempt %d)", attempt)
		require.NotNil(t, config.Seed, "attempt %d", attempt)
		assert.Equal(t, int32(7), *config.Seed, "attempt %d", attempt)

		wire := out.GetExtraParams()
		assert.NotContains(t, wire, "candidate_count", "attempt %d", attempt)
		assert.NotContains(t, wire, "response_logprobs", "attempt %d", attempt)
		assert.NotContains(t, wire, "seed", "attempt %d", attempt)
	}
}

// A /v1/responses top_logprobs request reaches Gemini as logprobs plus the
// responseLogprobs switch Gemini requires alongside it.
func TestToGeminiResponsesRequest_TopLogProbsMapsToLogprobs(t *testing.T) {
	out, err := gemini.ToGeminiResponsesRequest(nil, &schemas.BifrostResponsesRequest{
		Provider: schemas.Gemini,
		Model:    "gemini-2.5-flash",
		Params:   &schemas.ResponsesParameters{TopLogProbs: schemas.Ptr(5)},
	})
	require.NoError(t, err)
	assert.True(t, out.GenerationConfig.ResponseLogprobs)
	require.NotNil(t, out.GenerationConfig.Logprobs)
	assert.Equal(t, int32(5), *out.GenerationConfig.Logprobs)
}
