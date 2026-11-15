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
