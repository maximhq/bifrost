package gemini

import (
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Gemini bills the prompt even when it returns no transcript (for example a
// speechless clip), so usage must survive an empty-text response.
func TestToBifrostTranscriptionResponseKeepsUsageWhenTextEmpty(t *testing.T) {
	resp := &GenerateContentResponse{
		Candidates: []*Candidate{{Content: &Content{Role: string(RoleModel), Parts: []*Part{{Text: ""}}}}},
		UsageMetadata: &GenerateContentResponseUsageMetadata{
			PromptTokenCount: 12,
			TotalTokenCount:  12,
		},
	}

	got := resp.ToBifrostTranscriptionResponse()
	if got.Text != "" {
		t.Fatalf("text = %q, want empty", got.Text)
	}
	if got.Usage == nil {
		t.Fatal("usage dropped for an empty transcript")
	}
	if got.Usage.InputTokens == nil || *got.Usage.InputTokens != 12 {
		t.Errorf("input tokens = %v, want 12", got.Usage.InputTokens)
	}

	genai := ToGeminiTranscriptionResponse(got)
	if genai.UsageMetadata == nil || genai.UsageMetadata.PromptTokenCount != 12 {
		t.Errorf("genai usageMetadata = %+v, want promptTokenCount 12", genai.UsageMetadata)
	}
}

// The transcription converter runs once per retry/fallback attempt on the same
// Bifrost request and removes safety_settings, cached_content and labels from the
// outbound ExtraParams. It used to alias the source map, so the second attempt was
// sent without any of them. Regression for issue #7826.
func TestToGeminiTranscriptionRequest_ExtraParamsSurviveRetries(t *testing.T) {
	bifrostReq := &schemas.BifrostTranscriptionRequest{
		Provider: schemas.Gemini,
		Model:    "gemini-2.5-flash",
		Input:    &schemas.TranscriptionInput{File: []byte("not-really-audio"), Filename: "sample.mp3"},
		Params: &schemas.TranscriptionParameters{
			ExtraParams: map[string]interface{}{
				"safety_settings": []interface{}{
					map[string]interface{}{
						"category":  "HARM_CATEGORY_HARASSMENT",
						"threshold": "BLOCK_NONE",
					},
				},
				"cached_content":     "cachedContents/abc123",
				"labels":             map[string]interface{}{"team": "platform"},
				"custom_passthrough": "keep-me",
			},
		},
	}

	for attempt := 1; attempt <= 3; attempt++ {
		geminiReq := ToGeminiTranscriptionRequest(bifrostReq)
		require.NotNil(t, geminiReq, "attempt %d", attempt)

		require.Len(t, geminiReq.SafetySettings, 1, "attempt %d: safetySettings", attempt)
		assert.Equal(t, "HARM_CATEGORY_HARASSMENT", geminiReq.SafetySettings[0].Category, "attempt %d", attempt)
		assert.Equal(t, "BLOCK_NONE", geminiReq.SafetySettings[0].Threshold, "attempt %d", attempt)
		assert.Equal(t, "cachedContents/abc123", geminiReq.CachedContent, "attempt %d: cachedContent", attempt)
		assert.Equal(t, map[string]string{"team": "platform"}, geminiReq.Labels, "attempt %d: labels", attempt)
		assert.Equal(t, map[string]interface{}{"custom_passthrough": "keep-me"}, geminiReq.GetExtraParams(), "attempt %d: wire extra params", attempt)
	}

	// The Bifrost request itself must be left intact for the next attempt.
	assert.Contains(t, bifrostReq.Params.ExtraParams, "safety_settings")
	assert.Contains(t, bifrostReq.Params.ExtraParams, "cached_content")
	assert.Contains(t, bifrostReq.Params.ExtraParams, "labels")
	assert.Len(t, bifrostReq.Params.ExtraParams, 4)
}

// geminiTranscribeResponse is a gemini-3.5-transcribe :generateContent response as
// returned by the Gemini Developer API: the transcript sits in
// parts[].audioTranscription.text and there is no text part.
const geminiTranscribeResponse = `{
  "candidates": [
    {
      "content": {
        "parts": [
          {
            "audioTranscription": {
              "text": "The quick brown fox jumps over the lazy dog."
            }
          }
        ],
        "role": "model"
      },
      "finishReason": "STOP",
      "index": 0
    }
  ],
  "usageMetadata": {
    "promptTokenCount": 79,
    "candidatesTokenCount": 10,
    "totalTokenCount": 89,
    "promptTokensDetails": [
      {"modality": "TEXT", "tokenCount": 7},
      {"modality": "AUDIO", "tokenCount": 72}
    ],
    "serviceTier": "standard"
  },
  "modelVersion": "gemini-3.5-transcribe",
  "responseId": "-0T3aub7Mei8kdUPqtjb2QQ"
}`

func TestToBifrostTranscriptionResponseReadsAudioTranscriptionPart(t *testing.T) {
	var resp GenerateContentResponse
	require.NoError(t, sonic.Unmarshal([]byte(geminiTranscribeResponse), &resp))

	got := resp.ToBifrostTranscriptionResponse()
	assert.Equal(t, "The quick brown fox jumps over the lazy dog.", got.Text)
	require.NotNil(t, got.Task)
	assert.Equal(t, "transcribe", *got.Task)
	require.NotNil(t, got.Usage)
	require.NotNil(t, got.Usage.InputTokens)
	assert.Equal(t, 79, *got.Usage.InputTokens)
}

func TestToBifrostTranscriptionResponseFallsBackToTextParts(t *testing.T) {
	resp := &GenerateContentResponse{
		Candidates: []*Candidate{{Content: &Content{Role: string(RoleModel), Parts: []*Part{
			{Text: "Hello "},
			nil,
			{AudioTranscription: &PartAudioTranscription{}},
			{Text: "world."},
		}}}},
	}
	assert.Equal(t, "Hello world.", resp.ToBifrostTranscriptionResponse().Text)
}

// The streaming endpoint sends the transcript in an audioTranscription chunk and
// then a closing chunk with an empty text part and finishReason STOP.
func TestTranscriptionStreamChunksReadAudioTranscriptionPart(t *testing.T) {
	chunks := []string{
		`{"candidates": [{"content": {"parts": [{"audioTranscription": {"text": "The quick brown fox jumps over the lazy dog."}}],"role": "model"},"index": 0}],"usageMetadata": {"promptTokenCount": 79,"candidatesTokenCount": 10,"totalTokenCount": 89},"modelVersion": "gemini-3.5-transcribe"}`,
		`{"candidates": [{"content": {"parts": [{"text": ""}],"role": "model"},"finishReason": "STOP","index": 0}],"usageMetadata": {"promptTokenCount": 79,"candidatesTokenCount": 10,"totalTokenCount": 89},"modelVersion": "gemini-3.5-transcribe"}`,
	}
	var full string
	for _, c := range chunks {
		resp, err := processGeminiStreamChunk([]byte(c))
		require.NoError(t, err)
		full += resp.transcriptText()
	}
	assert.Equal(t, "The quick brown fox jumps over the lazy dog.", full)
}

func TestPartAudioTranscriptionRoundTrips(t *testing.T) {
	in := Part{AudioTranscription: &PartAudioTranscription{Text: "hi"}}
	data, err := sonic.Marshal(in)
	require.NoError(t, err)
	assert.JSONEq(t, `{"audioTranscription":{"text":"hi"}}`, string(data))

	var out Part
	require.NoError(t, sonic.Unmarshal(data, &out))
	require.NotNil(t, out.AudioTranscription)
	assert.Equal(t, "hi", out.AudioTranscription.Text)
}
