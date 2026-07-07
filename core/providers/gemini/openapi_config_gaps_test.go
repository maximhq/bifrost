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
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	require.NotNil(t, result.GenerationConfig.TranslationConfig)
	assert.Equal(t, "fr", result.GenerationConfig.TranslationConfig.TargetLanguageCode)

	require.NotNil(t, result.GenerationConfig.EnableEnhancedCivicAnswers)
	assert.False(t, *result.GenerationConfig.EnableEnhancedCivicAnswers)
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

func TestGenerateContentResponseCapturesModelStatus(t *testing.T) {
	raw := []byte(`{"modelVersion":"gemini-2.5-flash","modelStatus":{"message":"deprecated soon","modelStage":"DEPRECATED"},"candidates":[]}`)
	var resp GenerateContentResponse
	require.NoError(t, json.Unmarshal(raw, &resp))
	require.NotNil(t, resp.ModelStatus)
	assert.Equal(t, "deprecated soon", resp.ModelStatus.Message)
	assert.Equal(t, "DEPRECATED", resp.ModelStatus.ModelStage)
}
