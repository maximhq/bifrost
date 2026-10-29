package gemini

import (
	"encoding/json"
	"testing"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPayloadOrdering_GeminiGenerationRequest(t *testing.T) {
	req := &GeminiGenerationRequest{
		Model: "gemini-2.0-flash",
		Contents: []Content{
			{
				Parts: []*Part{{Text: "hello"}},
				Role:  "user",
			},
		},
		GenerationConfig: GenerationConfig{
			Temperature: schemas.Ptr(float64(0.7)),
		},
		Tools: []Tool{
			{
				FunctionDeclarations: []*FunctionDeclaration{
					{
						Name:        "get_weather",
						Description: "Get weather",
						Parameters: &Schema{
							Type: "OBJECT",
							Properties: map[string]*Schema{
								"location": {Type: "STRING"},
							},
							Required: []string{"location"},
						},
					},
				},
			},
		},
	}

	result, err := providerUtils.MarshalSorted(req)
	require.NoError(t, err)

	golden := `{"model":"gemini-2.0-flash","contents":[{"parts":[{"text":"hello"}],"role":"user"}],"generationConfig":{"temperature":0.7},"tools":[{"functionDeclarations":[{"description":"Get weather","name":"get_weather","parameters":{"properties":{"location":{"type":"STRING"}},"required":["location"],"type":"OBJECT"}}]}]}`

	assert.Equal(t, golden, string(result), "payload field ordering changed — if intentional, update the golden string")

	// Determinism: 100 iterations must produce identical bytes
	for i := 0; i < 100; i++ {
		iter, err := providerUtils.MarshalSorted(req)
		require.NoError(t, err)
		assert.Equal(t, string(result), string(iter), "non-deterministic marshal output on iteration %d", i)
	}
}

func TestNormalizeRawGenerateContentRequestForCompatibility(t *testing.T) {
	t.Run("keeps valid audio and logprobs, removes penalties", func(t *testing.T) {
		raw := []byte(`{"contents":[{"parts":[{"text":"Transcribe"},{"inlineData":{"mimeType":"audio/wav","data":"UklGRiQAAABXQVZFZm10IBAAAAABAAEAQB8AAIA+AAACABAAZGF0YQAAAAA="}}]}],"generationConfig":{"responseLogprobs":true,"logprobs":3,"presencePenalty":0.5,"frequencyPenalty":0.5,"temperature":0.2}}`)

		got := NormalizeRawGenerateContentRequestForCompatibility(nil, raw, schemas.Gemini, "")

		assert.Equal(t, `{"contents":[{"parts":[{"text":"Transcribe"},{"inlineData":{"mimeType":"audio/wav","data":"UklGRiQAAABXQVZFZm10IBAAAAABAAEAQB8AAIA+AAACABAAZGF0YQAAAAA="}}]}],"generationConfig":{"responseLogprobs":true,"logprobs":3,"temperature":0.2}}`, string(got))
	})

	t.Run("accepts url safe base64 audio", func(t *testing.T) {
		raw := []byte(`{"contents":[{"parts":[{"inlineData":{"mimeType":"audio/wav","data":"AA-_"}}]}]}`)

		got := NormalizeRawGenerateContentRequestForCompatibility(nil, raw, schemas.Gemini, "")

		assert.Equal(t, string(raw), string(got))
	})

	t.Run("removes invalid audio-only content entries", func(t *testing.T) {
		raw := []byte(`{"contents":[{"parts":[{"inlineData":{"mimeType":"audio/wav","data":"***"}}]},{"parts":[{"text":"keep"}]}]}`)

		got := NormalizeRawGenerateContentRequestForCompatibility(nil, raw, schemas.Gemini, "")

		assert.Equal(t, `{"contents":[{"parts":[{"text":"keep"}]}]}`, string(got))
	})
}

// TestWrapGeminiCountTokensBody covers the countTokens envelope. The endpoint rejects
// systemInstruction/tools/toolConfig/generationConfig at the top level and silently
// ignores a top-level contents/model once generateContentRequest is set, so the body
// must carry the envelope and nothing beside it.
func TestWrapGeminiCountTokensBody(t *testing.T) {
	t.Run("wraps a flat body and keeps every counted field", func(t *testing.T) {
		raw := []byte(`{"model":"gemini-3.6-flash","contents":[{"role":"user","parts":[{"text":"hi"}]}],"systemInstruction":{"parts":[{"text":"be terse"}]},"tools":[{"functionDeclarations":[{"name":"probe"}]}],"toolConfig":{"functionCallingConfig":{"mode":"AUTO"}},"generationConfig":{"temperature":0.2}}`)

		got := wrapGeminiCountTokensBody(raw, "gemini-3.6-flash")

		assert.JSONEq(t, `{"generateContentRequest":{"model":"models/gemini-3.6-flash","contents":[{"role":"user","parts":[{"text":"hi"}]}],"systemInstruction":{"parts":[{"text":"be terse"}]},"tools":[{"functionDeclarations":[{"name":"probe"}]}],"toolConfig":{"functionCallingConfig":{"mode":"AUTO"}},"generationConfig":{"temperature":0.2}}}`, string(got))
	})

	t.Run("leaves nothing at the top level besides the envelope", func(t *testing.T) {
		got := wrapGeminiCountTokensBody([]byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`), "gemini-3.6-flash")

		require.True(t, providerUtils.JSONFieldExists(got, "generateContentRequest"))
		assert.False(t, providerUtils.JSONFieldExists(got, "contents"))
		assert.False(t, providerUtils.JSONFieldExists(got, "model"))
	})

	t.Run("does not double wrap an already enveloped body", func(t *testing.T) {
		raw := []byte(`{"generateContentRequest":{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"systemInstruction":{"parts":[{"text":"be terse"}]}}}`)

		got := wrapGeminiCountTokensBody(raw, "gemini-3.6-flash")

		assert.JSONEq(t, `{"generateContentRequest":{"model":"models/gemini-3.6-flash","contents":[{"role":"user","parts":[{"text":"hi"}]}],"systemInstruction":{"parts":[{"text":"be terse"}]}}}`, string(got))
		assert.False(t, providerUtils.JSONFieldExists(got, "generateContentRequest.generateContentRequest"))
	})

	t.Run("qualifies the model without doubling the prefix", func(t *testing.T) {
		got := wrapGeminiCountTokensBody([]byte(`{"contents":[]}`), "models/gemini-3.6-flash")

		assert.Equal(t, "models/gemini-3.6-flash", providerUtils.GetJSONField(got, "generateContentRequest.model").String())
	})

	t.Run("strips fields GenerateContentRequest does not define", func(t *testing.T) {
		raw := []byte(`{"contents":[],"labels":{"a":"b"},"fallbacks":["gemini/other"]}`)

		got := wrapGeminiCountTokensBody(raw, "gemini-3.6-flash")

		assert.False(t, providerUtils.JSONFieldExists(got, "generateContentRequest.labels"))
		assert.False(t, providerUtils.JSONFieldExists(got, "generateContentRequest.fallbacks"))
	})

	t.Run("handles empty body", func(t *testing.T) {
		assert.Empty(t, wrapGeminiCountTokensBody(nil, "gemini-3.6-flash"))
	})
}

// TestGeminiCountTokensRequestToGenerationRequest covers the genai ingress. Clients post
// either the generateContentRequest envelope the Gemini API requires or the flat body
// Vertex accepts, so both must survive unmarshalling with every counted field intact.
//
// These go through json.Unmarshal rather than struct literals on purpose: a literal sets
// fields the decoder would never have populated, so it cannot catch a field that the DTO
// forgot to declare — which is exactly how top-level systemInstruction, tools and
// fallbacks were once dropped here.
func TestGeminiCountTokensRequestToGenerationRequest(t *testing.T) {
	flatten := func(t *testing.T, body string) *GeminiGenerationRequest {
		t.Helper()
		var req GeminiCountTokensRequest
		require.NoError(t, json.Unmarshal([]byte(body), &req))
		req.Model = "gemini-3.6-flash" // the genai route resolves the model from the path
		return req.ToGeminiGenerationRequest()
	}

	t.Run("unwraps the envelope and keeps the system instruction", func(t *testing.T) {
		got := flatten(t, `{"generateContentRequest":{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"systemInstruction":{"parts":[{"text":"be terse"}]}}}`)

		require.NotNil(t, got.SystemInstruction)
		assert.Equal(t, "be terse", got.SystemInstruction.Parts[0].Text)
		assert.Equal(t, "gemini-3.6-flash", got.Model)
		assert.True(t, got.IsCountTokens)
	})

	t.Run("keeps every counted field of a flat body", func(t *testing.T) {
		got := flatten(t, `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"systemInstruction":{"parts":[{"text":"be terse"}]},"tools":[{"functionDeclarations":[{"name":"probe"}]}],"generationConfig":{"temperature":0.2}}`)

		require.Len(t, got.Contents, 1)
		assert.Equal(t, "hi", got.Contents[0].Parts[0].Text)
		require.NotNil(t, got.SystemInstruction)
		assert.Equal(t, "be terse", got.SystemInstruction.Parts[0].Text)
		require.Len(t, got.Tools, 1)
		require.NotNil(t, got.GenerationConfig.Temperature)
	})

	t.Run("path model wins over the envelope model", func(t *testing.T) {
		got := flatten(t, `{"generateContentRequest":{"model":"models/stale","contents":[]}}`)

		assert.Equal(t, "gemini-3.6-flash", got.Model)
	})

	t.Run("retains the top-level fallback chain", func(t *testing.T) {
		got := flatten(t, `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"fallbacks":["vertex/gemini-3.6-flash","openai/gpt-4o"]}`)

		assert.Equal(t, []string{"vertex/gemini-3.6-flash", "openai/gpt-4o"}, got.Fallbacks)
		require.Len(t, got.ToBifrostResponsesRequest(nil).Fallbacks, 2)
	})

	t.Run("top-level fallbacks win over the envelope", func(t *testing.T) {
		got := flatten(t, `{"fallbacks":["vertex/gemini-3.6-flash"],"generateContentRequest":{"contents":[],"fallbacks":["openai/stale"]}}`)

		assert.Equal(t, []string{"vertex/gemini-3.6-flash"}, got.Fallbacks)
	})

	t.Run("keeps envelope fallbacks when none are set at the top level", func(t *testing.T) {
		got := flatten(t, `{"generateContentRequest":{"contents":[],"fallbacks":["openai/gpt-4o"]}}`)

		assert.Equal(t, []string{"openai/gpt-4o"}, got.Fallbacks)
	})

	// Converters are pure transformations, so the parsed request must survive untouched.
	t.Run("leaves the parsed request unmutated", func(t *testing.T) {
		for _, body := range []string{
			`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			`{"generateContentRequest":{"model":"models/stale","contents":[],"fallbacks":["openai/stale"]}}`,
		} {
			var req GeminiCountTokensRequest
			require.NoError(t, json.Unmarshal([]byte(body), &req))
			req.Model = "gemini-3.6-flash"
			req.Fallbacks = []string{"vertex/gemini-3.6-flash"}
			before, err := json.Marshal(req)
			require.NoError(t, err)

			require.NotNil(t, req.ToGeminiGenerationRequest())

			after, err := json.Marshal(req)
			require.NoError(t, err)
			assert.JSONEq(t, string(before), string(after), "converter mutated the request it was given")
			assert.False(t, req.IsCountTokens, "converter set IsCountTokens on the source request")
		}
	})
}

// TestToBifrostCountTokensResponseCachedContent locks the cached-token accounting
// against the real API shape. Verified live: a 7202-token cache plus 9 tokens of
// contents returns totalTokens 7211 with promptTokensDetails already at 7211 and
// cacheTokensDetails at 7202 — the cache is a subset of the prompt, not an addition,
// so its modalities must never be folded into the breakdown a second time.
func TestToBifrostCountTokensResponseCachedContent(t *testing.T) {
	t.Run("cached modalities do not inflate the breakdown", func(t *testing.T) {
		resp := &GeminiCountTokensResponse{
			TotalTokens:             7211,
			CachedContentTokenCount: 7202,
			PromptTokensDetails:     []*ModalityTokenCount{{Modality: ModalityText, TokenCount: 7211}},
			CacheTokensDetails:      []*ModalityTokenCount{{Modality: ModalityText, TokenCount: 7202}},
		}

		got := resp.ToBifrostCountTokensResponse("gemini-2.5-flash")

		assert.Equal(t, 7211, got.InputTokens)
		assert.Equal(t, 7211, got.InputTokensDetails.TextTokens)
		assert.Equal(t, 7202, got.InputTokensDetails.CachedReadTokens)
	})

	t.Run("falls back to summing cache details for the cached total", func(t *testing.T) {
		resp := &GeminiCountTokensResponse{
			TotalTokens:         100,
			PromptTokensDetails: []*ModalityTokenCount{{Modality: ModalityText, TokenCount: 100}},
			CacheTokensDetails: []*ModalityTokenCount{
				{Modality: ModalityText, TokenCount: 60},
				{Modality: ModalityAudio, TokenCount: 15},
			},
		}

		got := resp.ToBifrostCountTokensResponse("gemini-2.5-flash")

		assert.Equal(t, 75, got.InputTokensDetails.CachedReadTokens)
		assert.Equal(t, 100, got.InputTokensDetails.TextTokens)
		assert.Zero(t, got.InputTokensDetails.AudioTokens)
	})
}

// BenchmarkNormalizeRawGenerateContentRequestForCompatibility measures the raw /genai
// normalizer on the request path: a body without a thinking budget (the common case,
// which must only pay the field probes), a Gemini 3 budget that is kept, and a Gemini 4
// budget that is rewritten to a level.
func BenchmarkNormalizeRawGenerateContentRequestForCompatibility(b *testing.B) {
	const contents = `"contents":[{"role":"user","parts":[{"text":"Summarise the attached design notes in three bullet points."}]},` +
		`{"role":"model","parts":[{"text":"Here are the three points you asked for."}]},` +
		`{"role":"user","parts":[{"text":"Now list the open questions."}]}]`
	cases := []struct {
		name  string
		model string
		body  []byte
	}{
		{"no thinking budget", "gemini-4-argon", []byte(`{` + contents + `,"generationConfig":{"temperature":1,"maxOutputTokens":8192}}`)},
		{"gemini 3 budget kept", "gemini-3.8-flash", []byte(`{` + contents + `,"generationConfig":{"temperature":1,"thinkingConfig":{"includeThoughts":true,"thinkingBudget":4096},"maxOutputTokens":8192}}`)},
		{"gemini 4 budget rewritten", "gemini-4-argon", []byte(`{` + contents + `,"generationConfig":{"temperature":1,"thinkingConfig":{"includeThoughts":true,"thinkingBudget":4096},"maxOutputTokens":8192}}`)},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = NormalizeRawGenerateContentRequestForCompatibility(nil, tc.body, schemas.Gemini, tc.model)
			}
		})
	}
}

// A batch whose items need no rewrite must cost what it cost before the batch rewrite
// existed: an empty model skips the rewrite entirely, so it is the baseline.
func TestRawBatchThinkingRewriteAllocatesNothingWhenUnchanged(t *testing.T) {
	cases := []struct {
		name  string
		model string
		body  []byte
	}{
		{"gemini 4 batch without a budget", "gemini-4-argon", []byte(`{"batch":{"displayName":"b","inputConfig":{"requests":{"requests":[` +
			`{"request":{"contents":[{"parts":[{"text":"a"}]}],"generationConfig":{"temperature":1}},"metadata":{"key":"1"}},` +
			`{"request":{"contents":[{"parts":[{"text":"b"}]}]},"metadata":{"key":"2"}}]}}}}`)},
		{"gemini 3 batch keeps its budget", "gemini-3.8-flash", []byte(`{"batch":{"displayName":"b","inputConfig":{"requests":{"requests":[` +
			`{"request":{"contents":[{"parts":[{"text":"a"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":4096}}},"metadata":{"key":"1"}},` +
			`{"request":{"contents":[{"parts":[{"text":"b"}]}]},"metadata":{"key":"2"}}]}}}}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			baseline := testing.AllocsPerRun(200, func() {
				_ = NormalizeRawGenerateContentRequestForCompatibility(nil, tc.body, schemas.Gemini, "")
			})
			got := testing.AllocsPerRun(200, func() {
				_ = NormalizeRawGenerateContentRequestForCompatibility(nil, tc.body, schemas.Gemini, tc.model)
			})
			assert.Equal(t, baseline, got, "an unchanged batch must not allocate for the thinking rewrite")
			assert.Equal(t, string(tc.body), string(NormalizeRawGenerateContentRequestForCompatibility(nil, tc.body, schemas.Gemini, tc.model)))
		})
	}
}

// The items before the first rewritten one are carried over as one slice of the body,
// so the commas and the untouched items around a rewritten item must survive byte for byte.
func TestRawBatchThinkingRewriteKeepsItemsAroundARewrittenOne(t *testing.T) {
	body := []byte(`{"batch":{"inputConfig":{"requests":{"requests":[` +
		`{"request":{"contents":[{"parts":[{"text":"a"}]}]},"metadata":{"key":"1"}}, ` +
		`{"request":{"contents":[{"parts":[{"text":"b"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":20000}}},"metadata":{"key":"2"}},` +
		`{"request":{"contents":[{"parts":[{"text":"c"}]}],"generationConfig":{"temperature":1}},"metadata":{"key":"3"}}]}}}}`)
	want := `{"batch":{"inputConfig":{"requests":{"requests":[` +
		`{"request":{"contents":[{"parts":[{"text":"a"}]}]},"metadata":{"key":"1"}}, ` +
		`{"request":{"contents":[{"parts":[{"text":"b"}]}],"generationConfig":{"thinkingConfig":{"thinkingLevel":"high"}}},"metadata":{"key":"2"}},` +
		`{"request":{"contents":[{"parts":[{"text":"c"}]}],"generationConfig":{"temperature":1}},"metadata":{"key":"3"}}]}}}}`
	got := NormalizeRawGenerateContentRequestForCompatibility(nil, body, schemas.Gemini, "gemini-4-argon")
	assert.Equal(t, want, string(got))
	require.True(t, json.Valid(got))
}
