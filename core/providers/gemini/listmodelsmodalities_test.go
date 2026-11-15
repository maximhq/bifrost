package gemini

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// geminiModalitiesListFixture follows the models.list Model resource shape
// (https://ai.google.dev/api/models). It has no modality fields, and the TTS
// and image models list the same generation methods as the chat model.
const geminiModalitiesListFixture = `{
  "models": [
    {
      "name": "models/gemini-2.5-flash",
      "version": "001",
      "displayName": "Gemini 2.5 Flash",
      "inputTokenLimit": 1048576,
      "outputTokenLimit": 65536,
      "supportedGenerationMethods": ["generateContent", "countTokens", "createCachedContent", "batchGenerateContent"],
      "thinking": true
    },
    {
      "name": "models/gemini-2.5-flash-preview-tts",
      "version": "gemini-2.5-flash-exp-tts-2025-05-19",
      "displayName": "Gemini 2.5 Flash Preview TTS",
      "inputTokenLimit": 8192,
      "outputTokenLimit": 16384,
      "supportedGenerationMethods": ["countTokens", "generateContent", "batchGenerateContent"]
    },
    {
      "name": "models/gemini-2.5-flash-image",
      "version": "2.0",
      "displayName": "Nano Banana",
      "inputTokenLimit": 32768,
      "outputTokenLimit": 32768,
      "supportedGenerationMethods": ["generateContent", "countTokens", "batchGenerateContent"]
    },
    {
      "name": "models/gemini-embedding-001",
      "version": "001",
      "displayName": "Gemini Embedding 001",
      "inputTokenLimit": 2048,
      "outputTokenLimit": 1,
      "supportedGenerationMethods": ["embedContent", "countTextTokens", "countTokens", "asyncBatchEmbedContent"]
    },
    {
      "name": "models/imagen-4.0-generate-001",
      "version": "001",
      "displayName": "Imagen 4",
      "inputTokenLimit": 480,
      "outputTokenLimit": 8192,
      "supportedGenerationMethods": ["predict"]
    },
    {
      "name": "models/veo-3.0-generate-001",
      "version": "3.0",
      "displayName": "Veo 3",
      "inputTokenLimit": 480,
      "outputTokenLimit": 8192,
      "supportedGenerationMethods": ["predictLongRunning"]
    },
    {
      "name": "models/gemini-2.5-flash-native-audio-latest",
      "version": "Gemini 2.5 Flash Native Audio Latest",
      "displayName": "Gemini 2.5 Flash Native Audio Latest",
      "inputTokenLimit": 131072,
      "outputTokenLimit": 8192,
      "supportedGenerationMethods": ["countTokens", "bidiGenerateContent"]
    }
  ]
}`

func TestToBifrostListModelsResponse_ReportsModalities(t *testing.T) {
	var listing GeminiListModelsResponse
	require.NoError(t, json.Unmarshal([]byte(geminiModalitiesListFixture), &listing))

	resp := listing.ToBifrostListModelsResponse(schemas.Gemini, nil, nil, nil, true)
	require.NotNil(t, resp)

	rows := make(map[string]schemas.Model, len(resp.Data))
	for _, row := range resp.Data {
		rows[row.ID] = row
	}
	require.Len(t, rows, 7)

	// A plain chat model keeps a nil architecture so the catalog datasheet can fill it.
	assert.Nil(t, rows["gemini/gemini-2.5-flash"].Architecture)

	want := map[string]schemas.Architecture{
		"gemini/gemini-2.5-flash-preview-tts":         {InputModalities: []string{"text"}, OutputModalities: []string{"audio"}},
		"gemini/gemini-2.5-flash-image":               {InputModalities: []string{"image", "text"}, OutputModalities: []string{"image", "text"}},
		"gemini/gemini-embedding-001":                 {InputModalities: []string{"text"}, OutputModalities: []string{"embeddings"}},
		"gemini/imagen-4.0-generate-001":              {InputModalities: []string{"text"}, OutputModalities: []string{"image"}},
		"gemini/veo-3.0-generate-001":                 {InputModalities: []string{"image", "text"}, OutputModalities: []string{"video"}},
		"gemini/gemini-2.5-flash-native-audio-latest": {InputModalities: []string{"audio", "text", "video"}, OutputModalities: []string{"audio", "text"}},
	}
	for id, arch := range want {
		row, ok := rows[id]
		require.True(t, ok, id)
		require.NotNil(t, row.Architecture, id)
		assert.Equal(t, arch.InputModalities, row.Architecture.InputModalities, id)
		assert.Equal(t, arch.OutputModalities, row.Architecture.OutputModalities, id)
	}

	// The TTS row must carry the marker on the /v1/models wire shape.
	raw, err := json.Marshal(rows["gemini/gemini-2.5-flash-preview-tts"])
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"architecture":{"input_modalities":["text"],"output_modalities":["audio"]}`)
}

// TestGeminiModelArchitecture_DocumentedFamilies follows the Inputs/Output rows of
// Google's per-model pages (https://ai.google.dev/gemini-api/docs/models).
func TestGeminiModelArchitecture_DocumentedFamilies(t *testing.T) {
	generate := []string{"generateContent", "countTokens"}
	tests := []struct {
		name    string
		methods []string
		in, out []string
	}{
		{"models/gemini-3.8-flash-tts", generate, []string{"text"}, []string{"audio"}},
		{"models/gemini-3.8-flash-lite-tts", generate, []string{"text"}, []string{"audio"}},
		{"models/gemini-3.8-live", []string{"bidiGenerateContent"}, []string{"audio", "image", "text"}, []string{"audio", "text"}},
		{"models/gemini-3.5-live-translate-preview", []string{"bidiGenerateContent"}, []string{"audio"}, []string{"audio", "text"}},
		{"models/gemini-2.5-flash-native-audio-preview-12-2025", []string{"bidiGenerateContent"}, []string{"audio", "text", "video"}, []string{"audio", "text"}},
		{"models/gemini-3.5-transcribe", generate, []string{"audio"}, []string{"text"}},
		{"models/gemini-3.5-transcribe-live", []string{"bidiGenerateContent"}, []string{"audio"}, []string{"text"}},
		{"models/imagen-4.0-ultra-generate-001", []string{"predict"}, []string{"text"}, []string{"image"}},
		{"models/veo-3.1-generate-preview", []string{"predictLongRunning"}, []string{"image", "text"}, []string{"video"}},
		{"models/gemini-omni-flash", generate, []string{"image", "text", "video"}, []string{"text", "video"}},
		{"models/lyria-realtime-exp", []string{"bidiGenerateMusic"}, []string{"text"}, []string{"audio"}},
		{"models/lyria-3.5", generate, []string{"image", "text"}, []string{"audio", "text"}},
		{"models/gemini-3-pro-image", generate, []string{"image", "text"}, []string{"image", "text"}},
		{"models/gemini-3.1-flash-image", generate, []string{"file", "image", "text", "video"}, []string{"image", "text"}},
		{"models/gemini-nano-banana-2.1", generate, []string{"file", "image", "text", "video"}, []string{"image", "text"}},
		{"models/nano-banana-pro-preview", generate, []string{"image", "text"}, []string{"image", "text"}},
		{"models/gemini-omni-1.2-flash", generate, []string{"image", "text", "video"}, []string{"text", "video"}},
		{"models/gemini-embedding-2", []string{"embedContent"}, []string{"audio", "file", "image", "text", "video"}, []string{"embeddings"}},
		{"models/text-embedding-004", []string{"embedContent"}, []string{"text"}, []string{"embeddings"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			arch := geminiModelArchitecture(tc.name, tc.methods)
			require.NotNil(t, arch)
			assert.Equal(t, tc.in, arch.InputModalities)
			assert.Equal(t, tc.out, arch.OutputModalities)
		})
	}

	for _, chat := range []string{"models/gemini-3.8-flash", "models/gemini-4-carbon", "models/gemini-3.1-flash-lite", "models/gemma-4-31b-it", "models/gemini-robotics-er-2-preview", "models/deep-research-preview-04-2026", "models/gemini-decision-1"} {
		assert.Nil(t, geminiModelArchitecture(chat, generate), chat)
	}
}

func TestToBifrostModelRetrieveResponse_ReportsTTSModality(t *testing.T) {
	model := &GeminiModel{
		Name:                       "models/gemini-2.5-pro-preview-tts",
		SupportedGenerationMethods: []string{"countTokens", "generateContent"},
	}
	resp := model.ToBifrostModelRetrieveResponse(schemas.Gemini)
	require.NotNil(t, resp)
	require.NotNil(t, resp.Model.Architecture)
	assert.Equal(t, []string{"audio"}, resp.Model.Architecture.OutputModalities)
}
