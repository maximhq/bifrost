package openai

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToBifrostListModelsResponse_PreservesRichMetadata(t *testing.T) {
	t.Parallel()

	raw := `{
		"object": "list",
		"data": [{
			"id": "gpt-5.5",
			"object": "model",
			"created": 1754587413,
			"owned_by": "openai",
			"name": "GPT 5.5",
			"description": "Rich metadata model",
			"canonical_slug": "openai/gpt-5.5",
			"context_length": 1050000,
			"architecture": {
				"modality": "text+image->text",
				"input_modalities": ["text", "image"],
				"output_modalities": ["text"]
			},
			"default_parameters": {
				"temperature": 0.7,
				"top_p": 0.95
			},
			"supported_parameters": ["tools", "response_format"],
			"top_provider": {
				"is_moderated": true,
				"context_length": 1050000,
				"max_completion_tokens": 64000
			},
			"pricing": {
				"prompt": "0.000001",
				"completion": "0.000004",
				"input_cache_read": "0.0000001"
			},
			"knowledge_cutoff": "2025-01",
			"expiration_date": "2026-12-31",
			"aliases": ["gpt-5.5-latest"]
		}]
	}`

	var upstream OpenAIListModelsResponse
	require.NoError(t, json.Unmarshal([]byte(raw), &upstream))

	resp := upstream.ToBifrostListModelsResponse(schemas.OpenAI, nil, nil, nil, true)
	require.Len(t, resp.Data, 1)
	require.Equal(t, "openai/gpt-5.5", resp.Data[0].ID)

	payload, err := json.Marshal(resp)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(payload, &decoded))

	data := decoded["data"].([]any)
	model := data[0].(map[string]any)
	assert.Equal(t, "openai/gpt-5.5", model["id"])
	assert.Equal(t, "model", model["object"])
	assert.Equal(t, "openai", model["owned_by"])
	assert.Equal(t, "GPT 5.5", model["name"])
	assert.Equal(t, "Rich metadata model", model["description"])
	assert.Equal(t, "openai/gpt-5.5", model["canonical_slug"])
	assert.Equal(t, "2025-01", model["knowledge_cutoff"])
	assert.Equal(t, "2026-12-31", model["expiration_date"])
	assert.Equal(t, float64(1050000), model["context_length"])

	architecture := model["architecture"].(map[string]any)
	assert.Equal(t, "text+image->text", architecture["modality"])
	assert.Equal(t, []any{"text", "image"}, architecture["input_modalities"])

	pricing := model["pricing"].(map[string]any)
	assert.Equal(t, "0.000001", pricing["prompt"])
	assert.Equal(t, "0.000004", pricing["completion"])
	assert.Equal(t, "0.0000001", pricing["input_cache_read"])

	supportedParameters := model["supported_parameters"].([]any)
	assert.Equal(t, []any{"tools", "response_format"}, supportedParameters)
}

func TestToBifrostListModelsResponse_MinimalModelStillWorks(t *testing.T) {
	t.Parallel()

	raw := `{"object":"list","data":[{"id":"gpt-4o-mini","object":"model","created":123,"owned_by":"openai"}]}`
	var upstream OpenAIListModelsResponse
	require.NoError(t, json.Unmarshal([]byte(raw), &upstream))

	resp := upstream.ToBifrostListModelsResponse(schemas.OpenAI, nil, nil, nil, true)
	require.Len(t, resp.Data, 1)

	payload, err := json.Marshal(resp)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(payload, &decoded))
	model := decoded["data"].([]any)[0].(map[string]any)
	assert.Equal(t, "openai/gpt-4o-mini", model["id"])
	assert.Equal(t, "model", model["object"])
	assert.Equal(t, float64(123), model["created"])
	assert.Equal(t, "openai", model["owned_by"])
}

func TestToBifrostListModelsResponse_StripsCaseInsensitiveProviderPrefix(t *testing.T) {
	t.Parallel()

	raw := `{"object":"list","data":[{"id":"OpenAI/gpt-4o","object":"model","owned_by":"openai"}]}`
	var upstream OpenAIListModelsResponse
	require.NoError(t, json.Unmarshal([]byte(raw), &upstream))

	resp := upstream.ToBifrostListModelsResponse(schemas.OpenAI, nil, nil, nil, true)
	require.Len(t, resp.Data, 1)
	assert.Equal(t, "openai/gpt-4o", resp.Data[0].ID)
}
