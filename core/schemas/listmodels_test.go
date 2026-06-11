package schemas

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListModelsResponseMarshal_PreservesEnvelopeAndEnrichedPricing(t *testing.T) {
	t.Parallel()

	model := Model{
		ID:           "openai/gpt-5.5",
		OwnedBy:      Ptr("openai"),
		RawModelJSON: json.RawMessage(`{"id":"gpt-5.5","object":"model","description":"Rich metadata model","supported_parameters":["tools"],"knowledge_cutoff":"2025-01"}`),
		Pricing: &Pricing{
			Prompt:     Ptr("0.000001"),
			Completion: Ptr("0.000004"),
		},
	}

	resp := BifrostListModelsResponse{
		Data: []Model{model},
		ExtraFields: BifrostResponseExtraFields{
			Provider: OpenAI,
			Latency:  12,
		},
		KeyStatuses: []KeyStatus{{
			KeyID:    "key-1",
			Status:   KeyStatusSuccess,
			Provider: OpenAI,
		}},
	}

	payload, err := json.Marshal(resp)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(payload, &decoded))
	assert.Contains(t, decoded, "data")
	assert.Contains(t, decoded, "extra_fields")
	assert.Contains(t, decoded, "key_statuses")

	modelMap := decoded["data"].([]any)[0].(map[string]any)
	assert.Equal(t, "openai/gpt-5.5", modelMap["id"])
	assert.Equal(t, "model", modelMap["object"])
	assert.Equal(t, "Rich metadata model", modelMap["description"])
	assert.Equal(t, "2025-01", modelMap["knowledge_cutoff"])

	pricing := modelMap["pricing"].(map[string]any)
	assert.Equal(t, "0.000001", pricing["prompt"])
	assert.Equal(t, "0.000004", pricing["completion"])

	extraFields := decoded["extra_fields"].(map[string]any)
	assert.Equal(t, "openai", extraFields["provider"])
	assert.Equal(t, float64(12), extraFields["latency"])

	keyStatus := decoded["key_statuses"].([]any)[0].(map[string]any)
	assert.Equal(t, "key-1", keyStatus["key_id"])
	assert.Equal(t, "success", keyStatus["status"])
	assert.Equal(t, "openai", keyStatus["provider"])
}

func TestParseListModelString_NormalizesKnownProviderCasing(t *testing.T) {
	t.Parallel()

	provider, model := ParseListModelString("OpenAI/gpt-4o", "")
	assert.Equal(t, OpenAI, provider)
	assert.Equal(t, "gpt-4o", model)
}

func TestModelUnmarshalJSON_NormalizesEmptyNestedStructsToNil(t *testing.T) {
	t.Parallel()

	var model Model
	err := json.Unmarshal([]byte(`{
		"id":"openai/gpt-5.5",
		"pricing":{},
		"architecture":{},
		"top_provider":{},
		"per_request_limits":{},
		"default_parameters":{}
	}`), &model)
	require.NoError(t, err)
	assert.Nil(t, model.Pricing)
	assert.Nil(t, model.Architecture)
	assert.Nil(t, model.TopProvider)
	assert.Nil(t, model.PerRequestLimits)
	assert.Nil(t, model.DefaultParameters)
}

func TestModelMarshalJSON_DeepMergesNestedMetadata(t *testing.T) {
	t.Parallel()

	model := Model{
		ID: "openai/gpt-5.5",
		RawModelJSON: json.RawMessage(`{
			"id":"gpt-5.5",
			"pricing":{"prompt":"0.1","input_cache_read":"0.02"},
			"top_provider":{"is_moderated":true,"provider_name":"openrouter"}
		}`),
		Pricing: &Pricing{
			Prompt:     Ptr("0.3"),
			Completion: Ptr("0.4"),
		},
		TopProvider: &TopProvider{
			ContextLength: Ptr(4096),
		},
	}

	payload, err := json.Marshal(model)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(payload, &decoded))

	pricing := decoded["pricing"].(map[string]any)
	assert.Equal(t, "0.3", pricing["prompt"])
	assert.Equal(t, "0.4", pricing["completion"])
	assert.Equal(t, "0.02", pricing["input_cache_read"])

	topProvider := decoded["top_provider"].(map[string]any)
	assert.Equal(t, true, topProvider["is_moderated"])
	assert.Equal(t, float64(4096), topProvider["context_length"])
	assert.Equal(t, "openrouter", topProvider["provider_name"])
}
