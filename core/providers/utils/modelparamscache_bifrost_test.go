package utils

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetBifrostOverrides_CacheHit verifies the lookup returns the cached
// override pointer for a known model key.
func TestGetBifrostOverrides_CacheHit(t *testing.T) {
	truePtr := true
	override := schemas.BifrostOverrides{
		SupportsCachePoint: &truePtr,
		ServerTools:        map[string]string{"web_search": "web_search_20260209"},
	}
	SetModelParams("test/claude-opus-4-7", ModelParams{
		BifrostOverrides: &override,
	})
	t.Cleanup(func() { DeleteModelParams("test/claude-opus-4-7") })

	got := GetBifrostOverrides("test/claude-opus-4-7")
	require.NotNil(t, got)
	require.NotNil(t, got.SupportsCachePoint)
	assert.True(t, *got.SupportsCachePoint)
	assert.Equal(t, "web_search_20260209", got.ServerTools["web_search"])
}

// TestGetBifrostOverrides_CacheMiss verifies that missing entries return nil
// cleanly (not a zero-valued struct), so callers can detect the absence and
// fall back to hardcoded helpers.
func TestGetBifrostOverrides_CacheMiss(t *testing.T) {
	got := GetBifrostOverrides("test/nonexistent-model-12345")
	assert.Nil(t, got)
}

// TestGetBifrostOverrides_PresentButEmpty verifies that an entry with no
// override pointer (only max_output_tokens) returns nil for the override
// lookup.
func TestGetBifrostOverrides_PresentButEmpty(t *testing.T) {
	maxTokens := 4096
	SetModelParams("test/no-overrides-just-max-tokens", ModelParams{
		MaxOutputTokens: &maxTokens,
	})
	t.Cleanup(func() { DeleteModelParams("test/no-overrides-just-max-tokens") })

	got := GetBifrostOverrides("test/no-overrides-just-max-tokens")
	assert.Nil(t, got)
}

// TestGetBifrostOverridesForRequest_Verbatim covers the (1) precedence path:
// callers passing already-prefixed datasheet keys (Bedrock canonical IDs,
// Vertex "@date" suffixes, Azure "azure/..." keys) match without any
// per-provider prefix construction.
func TestGetBifrostOverridesForRequest_Verbatim(t *testing.T) {
	truePtr := true
	cases := []struct {
		name     string
		key      string
		provider schemas.ModelProvider
	}{
		{"bedrock canonical", "anthropic.claude-opus-4-7-20251101-v1:0", schemas.Bedrock},
		{"bedrock regional", "us.anthropic.claude-opus-4-7-20251101-v1:0", schemas.Bedrock},
		{"vertex with date", "claude-opus-4-7@20251101", schemas.Vertex},
		{"azure prefixed", "azure/claude-opus-4-7", schemas.Azure},
		{"anthropic native", "claude-opus-4-7", schemas.Anthropic},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ov := schemas.BifrostOverrides{SupportsCachePoint: &truePtr}
			SetModelParams(tc.key, ModelParams{BifrostOverrides: &ov})
			t.Cleanup(func() { DeleteModelParams(tc.key) })

			got := GetBifrostOverridesForRequest(tc.provider, tc.key)
			require.NotNil(t, got, "verbatim lookup should hit")
			assert.True(t, *got.SupportsCachePoint)
		})
	}
}

// TestGetBifrostOverridesForRequest_BedrockFamilyPrefix verifies that bare
// model names route to the right Bedrock family-stamped prefix (anthropic./
// meta./mistral./amazon.) when the datasheet stores them under that prefix.
func TestGetBifrostOverridesForRequest_BedrockFamilyPrefix(t *testing.T) {
	truePtr := true
	cases := []struct {
		name      string
		bareModel string
		storedKey string
	}{
		{"claude → anthropic.", "claude-opus-4-7", "anthropic.claude-opus-4-7"},
		{"llama → meta.", "llama3-1-70b-instruct", "meta.llama3-1-70b-instruct"},
		{"mistral → mistral.", "mistral-large-2407-v1:0", "mistral.mistral-large-2407-v1:0"},
		{"nova → amazon.", "nova-pro-v1:0", "amazon.nova-pro-v1:0"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ov := schemas.BifrostOverrides{SupportsCachePoint: &truePtr}
			SetModelParams(tc.storedKey, ModelParams{BifrostOverrides: &ov})
			t.Cleanup(func() { DeleteModelParams(tc.storedKey) })

			got := GetBifrostOverridesForRequest(schemas.Bedrock, tc.bareModel)
			require.NotNil(t, got, "family-prefix lookup should hit %s", tc.storedKey)
			assert.True(t, *got.SupportsCachePoint)
		})
	}
}

// TestGetBifrostOverridesForRequest_VertexPrefix verifies bare Gemini /
// Mistral model names route to vertex_ai/<model>.
func TestGetBifrostOverridesForRequest_VertexPrefix(t *testing.T) {
	truePtr := true
	cases := []string{"gemini-2.5-pro", "gemini-2.5-flash"}

	for _, model := range cases {
		t.Run(model, func(t *testing.T) {
			storedKey := "vertex_ai/" + model
			ov := schemas.BifrostOverrides{SupportsCachePoint: &truePtr}
			SetModelParams(storedKey, ModelParams{BifrostOverrides: &ov})
			t.Cleanup(func() { DeleteModelParams(storedKey) })

			got := GetBifrostOverridesForRequest(schemas.Vertex, model)
			require.NotNil(t, got)
			assert.True(t, *got.SupportsCachePoint)
		})
	}
}

// TestGetBifrostOverridesForRequest_AzurePrefix verifies bare Azure model
// names route to azure/<model>.
func TestGetBifrostOverridesForRequest_AzurePrefix(t *testing.T) {
	truePtr := true
	storedKey := "azure/claude-opus-4-7"
	ov := schemas.BifrostOverrides{SupportsCachePoint: &truePtr}
	SetModelParams(storedKey, ModelParams{BifrostOverrides: &ov})
	t.Cleanup(func() { DeleteModelParams(storedKey) })

	got := GetBifrostOverridesForRequest(schemas.Azure, "claude-opus-4-7")
	require.NotNil(t, got)
	assert.True(t, *got.SupportsCachePoint)
}

// TestGetBifrostOverridesForRequest_Miss verifies that an unknown
// (provider, model) pair returns nil cleanly.
func TestGetBifrostOverridesForRequest_Miss(t *testing.T) {
	got := GetBifrostOverridesForRequest(schemas.Anthropic, "definitely-nonexistent-model-12345")
	assert.Nil(t, got)
}

// TestGetBifrostOverridesForRequest_EmptyModel verifies short-circuit on
// empty model string.
func TestGetBifrostOverridesForRequest_EmptyModel(t *testing.T) {
	assert.Nil(t, GetBifrostOverridesForRequest(schemas.Anthropic, ""))
}

// TestModelParams_BothFieldsCoexist verifies a single cache entry can carry
// both max_output_tokens and bifrost_overrides — important because
// populateModelParamsFromPricing writes both under the same datasheet key
// when the key is unprefixed (Anthropic native).
func TestModelParams_BothFieldsCoexist(t *testing.T) {
	maxTokens := 8192
	truePtr := true
	override := schemas.BifrostOverrides{SupportsCachePoint: &truePtr}
	SetModelParams("test/combo-model", ModelParams{
		MaxOutputTokens:  &maxTokens,
		BifrostOverrides: &override,
	})
	t.Cleanup(func() { DeleteModelParams("test/combo-model") })

	gotMax, ok := GetMaxOutputTokens("test/combo-model")
	require.True(t, ok)
	assert.Equal(t, 8192, gotMax)

	gotOv := GetBifrostOverrides("test/combo-model")
	require.NotNil(t, gotOv)
	require.NotNil(t, gotOv.SupportsCachePoint)
	assert.True(t, *gotOv.SupportsCachePoint)
}
