package gemini

import (
	"encoding/json"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChatCompletionRequestExtractsTranslationEnhancedCivicResponseFormat(t *testing.T) {
	result, err := ToGeminiChatCompletionRequest(nil, &schemas.BifrostChatRequest{
		Model: "gemini-2.5-flash",
		Input: []schemas.ChatMessage{
			{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hola")},
			},
		},
		Params: &schemas.ChatParameters{
			ExtraParams: map[string]interface{}{
				"translation_config": map[string]interface{}{
					"targetLanguageCode": "es",
				},
				"enable_enhanced_civic_answers": true,
				"response_format": map[string]interface{}{
					"text": map[string]interface{}{
						"mimeType": "TEXT_PLAIN",
					},
				},
				"unrelated_passthrough_key": "keep-me",
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	require.NotNil(t, result.GenerationConfig.TranslationConfig)
	assert.Equal(t, "es", result.GenerationConfig.TranslationConfig.TargetLanguageCode)

	require.NotNil(t, result.GenerationConfig.EnableEnhancedCivicAnswers)
	assert.True(t, *result.GenerationConfig.EnableEnhancedCivicAnswers)

	require.NotNil(t, result.GenerationConfig.ResponseFormat)
	require.NotNil(t, result.GenerationConfig.ResponseFormat.Text)
	assert.Equal(t, "TEXT_PLAIN", result.GenerationConfig.ResponseFormat.Text.MimeType)

	// Consumed keys must be removed from the passthrough ExtraParams so they aren't
	// double-applied by the generic extra_params -> JSON merge at send time.
	_, hasTranslation := result.ExtraParams["translation_config"]
	assert.False(t, hasTranslation)
	_, hasCivic := result.ExtraParams["enable_enhanced_civic_answers"]
	assert.False(t, hasCivic)
	_, hasResponseFormat := result.ExtraParams["response_format"]
	assert.False(t, hasResponseFormat)

	// Untouched keys must survive.
	assert.Equal(t, "keep-me", result.ExtraParams["unrelated_passthrough_key"])
}

func TestResponsesRequestExtractsTranslationEnhancedCivicResponseFormat(t *testing.T) {
	result, err := ToGeminiResponsesRequest(nil, &schemas.BifrostResponsesRequest{
		Model: "gemini-2.5-flash",
		Input: []schemas.ResponsesMessage{
			{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("hola"),
				},
			},
		},
		Params: &schemas.ResponsesParameters{
			ExtraParams: map[string]interface{}{
				"translation_config": map[string]interface{}{
					"targetLanguageCode": "fr",
					"echoTargetLanguage": true,
				},
				"enable_enhanced_civic_answers": false,
				"response_format": map[string]interface{}{
					"text": map[string]interface{}{
						"mimeType": "TEXT_PLAIN",
					},
				},
				"unrelated_passthrough_key": "keep-me",
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	require.NotNil(t, result.GenerationConfig.TranslationConfig)
	assert.Equal(t, "fr", result.GenerationConfig.TranslationConfig.TargetLanguageCode)

	require.NotNil(t, result.GenerationConfig.EnableEnhancedCivicAnswers)
	assert.False(t, *result.GenerationConfig.EnableEnhancedCivicAnswers)

	require.NotNil(t, result.GenerationConfig.ResponseFormat)
	require.NotNil(t, result.GenerationConfig.ResponseFormat.Text)
	assert.Equal(t, "TEXT_PLAIN", result.GenerationConfig.ResponseFormat.Text.MimeType)

	// Consumed keys must be removed from the passthrough ExtraParams so they aren't
	// double-applied by the generic extra_params -> JSON merge at send time.
	_, hasTranslation := result.ExtraParams["translation_config"]
	assert.False(t, hasTranslation)
	_, hasCivic := result.ExtraParams["enable_enhanced_civic_answers"]
	assert.False(t, hasCivic)
	_, hasResponseFormat := result.ExtraParams["response_format"]
	assert.False(t, hasResponseFormat)

	// Untouched keys must survive.
	assert.Equal(t, "keep-me", result.ExtraParams["unrelated_passthrough_key"])
}

func TestChatCompletionRequestExtractsResponseFormatFromCanonicalField(t *testing.T) {
	// On /v1/chat/completions, "response_format" is a known top-level field, so
	// the HTTP layer parses it into Params.ResponseFormat (canonical field),
	// never into Params.ExtraParams. Gemini's native per-modality shape (no
	// OpenAI "type" discriminator) must still be picked up from there.
	rf := interface{}(map[string]interface{}{
		"text": map[string]interface{}{"mimeType": "text/plain"},
	})
	result, err := ToGeminiChatCompletionRequest(nil, &schemas.BifrostChatRequest{
		Model: "gemini-2.5-flash",
		Input: []schemas.ChatMessage{
			{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hola")},
			},
		},
		Params: &schemas.ChatParameters{
			ResponseFormat: &rf,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	require.NotNil(t, result.GenerationConfig.ResponseFormat)
	require.NotNil(t, result.GenerationConfig.ResponseFormat.Text)
	assert.Equal(t, "text/plain", result.GenerationConfig.ResponseFormat.Text.MimeType)
}

func TestChatCompletionRequestCanonicalResponseFormatStillMapsJSONSchema(t *testing.T) {
	// OpenAI's canonical json_schema shape (with a "type" discriminator) must
	// keep mapping to ResponseSchema/ResponseMIMEType, not be misread as the
	// Gemini-native per-modality shape.
	rf := interface{}(map[string]interface{}{
		"type": "json_schema",
		"json_schema": map[string]interface{}{
			"name": "test_schema",
			"schema": map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{"a": map[string]interface{}{"type": "string"}},
			},
		},
	})
	result, err := ToGeminiChatCompletionRequest(nil, &schemas.BifrostChatRequest{
		Model: "gemini-2.5-flash",
		Input: []schemas.ChatMessage{
			{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hola")},
			},
		},
		Params: &schemas.ChatParameters{
			ResponseFormat: &rf,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, "application/json", result.GenerationConfig.ResponseMIMEType)
	assert.NotNil(t, result.GenerationConfig.ResponseJSONSchema)
	assert.Nil(t, result.GenerationConfig.ResponseFormat)
}

func TestGoogleSearchSearchTypesRoundTrip(t *testing.T) {
	t.Run("camelCase", func(t *testing.T) {
		raw := []byte(`{"searchTypes":{"webSearch":{},"imageSearch":{}}}`)
		var gs GoogleSearch
		require.NoError(t, json.Unmarshal(raw, &gs))
		require.NotNil(t, gs.SearchTypes)
		assert.NotNil(t, gs.SearchTypes.WebSearch)
		assert.NotNil(t, gs.SearchTypes.ImageSearch)
	})

	t.Run("snake_case", func(t *testing.T) {
		raw := []byte(`{"search_types":{"web_search":{},"image_search":{}}}`)
		var gs GoogleSearch
		require.NoError(t, json.Unmarshal(raw, &gs))
		require.NotNil(t, gs.SearchTypes)
		assert.NotNil(t, gs.SearchTypes.WebSearch)
		assert.NotNil(t, gs.SearchTypes.ImageSearch)
	})
}

func TestTranslationConfigSnakeCase(t *testing.T) {
	raw := []byte(`{"target_language_code":"es","echo_target_language":true}`)
	var tc TranslationConfig
	require.NoError(t, json.Unmarshal(raw, &tc))
	assert.Equal(t, "es", tc.TargetLanguageCode)
	require.NotNil(t, tc.EchoTargetLanguage)
	assert.True(t, *tc.EchoTargetLanguage)
}

func TestResponseFormatConfigSnakeCase(t *testing.T) {
	raw := []byte(`{"text":{"mime_type":"APPLICATION_JSON"},"image":{"aspect_ratio":"ASPECT_RATIO_ONE_BY_ONE"},"audio":{"mime_type":"AUDIO_MP3","bit_rate":128000,"sample_rate":24000}}`)
	var rf ResponseFormatConfig
	require.NoError(t, json.Unmarshal(raw, &rf))

	require.NotNil(t, rf.Text)
	assert.Equal(t, "APPLICATION_JSON", rf.Text.MimeType)

	require.NotNil(t, rf.Image)
	assert.Equal(t, "ASPECT_RATIO_ONE_BY_ONE", rf.Image.AspectRatio)

	require.NotNil(t, rf.Audio)
	assert.Equal(t, "AUDIO_MP3", rf.Audio.MimeType)
	require.NotNil(t, rf.Audio.BitRate)
	assert.Equal(t, int32(128000), *rf.Audio.BitRate)
	require.NotNil(t, rf.Audio.SampleRate)
	assert.Equal(t, int32(24000), *rf.Audio.SampleRate)
}

func TestGenerateContentResponseCapturesModelStatus(t *testing.T) {
	raw := []byte(`{"modelVersion":"gemini-2.5-flash","modelStatus":{"message":"deprecated soon","modelStage":"DEPRECATED"},"candidates":[]}`)
	var resp GenerateContentResponse
	require.NoError(t, json.Unmarshal(raw, &resp))
	require.NotNil(t, resp.ModelStatus)
	assert.Equal(t, "deprecated soon", resp.ModelStatus.Message)
	assert.Equal(t, "DEPRECATED", resp.ModelStatus.ModelStage)
}
