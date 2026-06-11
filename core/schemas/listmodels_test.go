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
