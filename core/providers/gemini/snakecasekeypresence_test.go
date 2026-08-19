package gemini

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Gemini REST surface is protobuf-derived, so protobuf JSON accepts both the lowerCamelCase
// name and the original snake_case one. The alias decoders in types.go exist to honour both, with
// camelCase winning when both are present.
//
// The rule they actually implemented was "snake_case wins whenever the camelCase VALUE is falsy",
// which is a different rule. A caller who explicitly sends false, 0, "", [] or null decodes to the
// same zero value as one who omits the key, so their explicit choice was silently overwritten by a
// snake_case sibling. These tests pin presence-based precedence.

func TestGenerationConfig_ExplicitCamelCaseValueBeatsSnakeCase(t *testing.T) {
	for _, tt := range []struct {
		name   string
		body   string
		assert func(t *testing.T, g *GenerationConfig)
	}{
		{
			name: "explicit false is not treated as absent",
			body: `{"responseLogprobs":false,"response_logprobs":true}`,
			assert: func(t *testing.T, g *GenerationConfig) {
				assert.False(t, g.ResponseLogprobs,
					"the caller explicitly turned logprobs off; a snake_case sibling must not turn them on")
			},
		},
		{
			name: "explicit zero is not treated as absent",
			body: `{"candidateCount":0,"candidate_count":5}`,
			assert: func(t *testing.T, g *GenerationConfig) {
				assert.Equal(t, int32(0), g.CandidateCount)
			},
		},
		{
			name: "explicit empty string is not treated as absent",
			body: `{"responseMimeType":"","response_mime_type":"application/json"}`,
			assert: func(t *testing.T, g *GenerationConfig) {
				assert.Equal(t, "", g.ResponseMIMEType)
			},
		},
		{
			name: "explicit null is not treated as absent",
			body: `{"topP":null,"top_p":0.5}`,
			assert: func(t *testing.T, g *GenerationConfig) {
				assert.Nil(t, g.TopP, "an explicit null is a choice, not a missing key")
			},
		},
		{
			name: "explicit empty slice is not treated as absent",
			body: `{"stopSequences":[],"stop_sequences":["STOP"]}`,
			assert: func(t *testing.T, g *GenerationConfig) {
				assert.Empty(t, g.StopSequences,
					"an explicitly empty stop list must not be repopulated from snake_case")
			},
		},
		{
			name: "absent camelCase still falls back to snake_case",
			body: `{"response_logprobs":true,"candidate_count":5,"top_p":0.5}`,
			assert: func(t *testing.T, g *GenerationConfig) {
				assert.True(t, g.ResponseLogprobs, "the fallback must still work when camelCase is genuinely absent")
				assert.Equal(t, int32(5), g.CandidateCount)
				require.NotNil(t, g.TopP)
				assert.InDelta(t, 0.5, *g.TopP, 1e-9)
			},
		},
		{
			name: "camelCase wins when both carry real values",
			body: `{"maxOutputTokens":100,"max_output_tokens":200}`,
			assert: func(t *testing.T, g *GenerationConfig) {
				assert.Equal(t, int32(100), g.MaxOutputTokens)
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var g GenerationConfig
			require.NoError(t, g.UnmarshalJSON([]byte(tt.body)))
			tt.assert(t, &g)
		})
	}
}

// Same rule one level up, on the request itself.
func TestGeminiGenerationRequest_ExplicitCamelCaseValueBeatsSnakeCase(t *testing.T) {
	t.Run("an explicitly empty generationConfig is not replaced", func(t *testing.T) {
		var r GeminiGenerationRequest
		require.NoError(t, r.UnmarshalJSON([]byte(
			`{"generationConfig":{},"generation_config":{"temperature":1.5}}`)))
		assert.Nil(t, r.GenerationConfig.Temperature,
			"the caller sent an empty generationConfig; snake_case must not fill it in")
	})

	t.Run("an explicitly empty safetySettings list is not repopulated", func(t *testing.T) {
		var r GeminiGenerationRequest
		require.NoError(t, r.UnmarshalJSON([]byte(
			`{"safetySettings":[],"safety_settings":[{"category":"HARM_CATEGORY_HARASSMENT"}]}`)))
		assert.Empty(t, r.SafetySettings)
	})

	t.Run("an explicitly empty cachedContent is not replaced", func(t *testing.T) {
		var r GeminiGenerationRequest
		require.NoError(t, r.UnmarshalJSON([]byte(
			`{"cachedContent":"","cached_content":"caches/abc"}`)))
		assert.Equal(t, "", r.CachedContent)
	})

	t.Run("absent camelCase still falls back to snake_case", func(t *testing.T) {
		var r GeminiGenerationRequest
		require.NoError(t, r.UnmarshalJSON([]byte(
			`{"generation_config":{"temperature":1.5},"cached_content":"caches/abc"}`)))
		require.NotNil(t, r.GenerationConfig.Temperature)
		assert.InDelta(t, 1.5, *r.GenerationConfig.Temperature, 1e-9)
		assert.Equal(t, "caches/abc", r.CachedContent)
	})
}

// The count-tokens envelope has the same two spellings. Decoding only the camelCase one dropped the
// envelope entirely and then read the enveloped body as a plain generation request, so the count was
// computed against an essentially empty request rather than failing loudly.
func TestGeminiCountTokensRequest_AcceptsSnakeCaseEnvelope(t *testing.T) {
	t.Run("snake_case envelope binds", func(t *testing.T) {
		var r GeminiCountTokensRequest
		require.NoError(t, r.UnmarshalJSON([]byte(
			`{"generate_content_request":{"model":"models/gemini-pro","contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`)))
		require.NotNil(t, r.GenerateContentRequest, "a protobuf-JSON client's envelope must not be dropped")
		assert.Equal(t, "models/gemini-pro", r.GenerateContentRequest.Model)
		require.Len(t, r.GenerateContentRequest.Contents, 1)
	})

	t.Run("camelCase envelope still binds", func(t *testing.T) {
		var r GeminiCountTokensRequest
		require.NoError(t, r.UnmarshalJSON([]byte(
			`{"generateContentRequest":{"model":"models/gemini-pro"}}`)))
		require.NotNil(t, r.GenerateContentRequest)
		assert.Equal(t, "models/gemini-pro", r.GenerateContentRequest.Model)
	})

	t.Run("camelCase wins when both spellings are present", func(t *testing.T) {
		var r GeminiCountTokensRequest
		require.NoError(t, r.UnmarshalJSON([]byte(
			`{"generateContentRequest":{"model":"camel"},"generate_content_request":{"model":"snake"}}`)))
		require.NotNil(t, r.GenerateContentRequest)
		assert.Equal(t, "camel", r.GenerateContentRequest.Model)
	})

	t.Run("the un-enveloped form is unaffected", func(t *testing.T) {
		var r GeminiCountTokensRequest
		require.NoError(t, r.UnmarshalJSON([]byte(
			`{"model":"models/gemini-pro","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)))
		assert.Nil(t, r.GenerateContentRequest, "no envelope was sent, so none should be synthesised")
		assert.Equal(t, "models/gemini-pro", r.Model)
	})
}
