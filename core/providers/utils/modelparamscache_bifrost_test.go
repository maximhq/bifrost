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

// TestGetBifrostOverridesForRequest_ProviderIsKey verifies the same model on
// different providers resolves to distinct overrides — provider is part of the
// cache key (mirrors pricing), so there is no cross-provider collision.
func TestGetBifrostOverridesForRequest_ProviderIsKey(t *testing.T) {
	yes, no := true, false
	anthKey := OverrideCacheKey("claude-opus-4-8", schemas.Anthropic)
	vertexKey := OverrideCacheKey("claude-opus-4-8", schemas.Vertex)
	SetModelParams(anthKey, ModelParams{BifrostOverrides: &schemas.BifrostOverrides{SupportsFastMode: &yes}})
	SetModelParams(vertexKey, ModelParams{BifrostOverrides: &schemas.BifrostOverrides{SupportsFastMode: &no}})
	t.Cleanup(func() { DeleteModelParams(anthKey); DeleteModelParams(vertexKey) })

	anth := GetBifrostOverridesForRequest(schemas.Anthropic, "claude-opus-4-8")
	require.NotNil(t, anth)
	assert.True(t, *anth.SupportsFastMode)

	vertex := GetBifrostOverridesForRequest(schemas.Vertex, "claude-opus-4-8")
	require.NotNil(t, vertex)
	assert.False(t, *vertex.SupportsFastMode)
}

// TestGetBifrostOverridesForRequest_NoCrossProviderCollision verifies a bare
// model on a non-Anthropic provider does NOT borrow the Anthropic-native entry
// — it misses cleanly so the caller falls back to substring detection.
func TestGetBifrostOverridesForRequest_NoCrossProviderCollision(t *testing.T) {
	yes := true
	anthKey := OverrideCacheKey("claude-opus-4-8", schemas.Anthropic)
	SetModelParams(anthKey, ModelParams{BifrostOverrides: &schemas.BifrostOverrides{SupportsFastMode: &yes}})
	t.Cleanup(func() { DeleteModelParams(anthKey) })

	assert.Nil(t, GetBifrostOverridesForRequest(schemas.Vertex, "claude-opus-4-8"))
	assert.Nil(t, GetBifrostOverridesForRequest(schemas.Bedrock, "claude-opus-4-8"))
	assert.Nil(t, GetBifrostOverridesForRequest(schemas.Azure, "claude-opus-4-8"))
}

// TestGetBifrostOverridesForRequest_BedrockDotted verifies Bedrock's dotted
// runtime model (which equals the datasheet key form) matches under its
// composite key.
func TestGetBifrostOverridesForRequest_BedrockDotted(t *testing.T) {
	yes := true
	key := OverrideCacheKey("global.anthropic.claude-opus-4-7", schemas.Bedrock)
	SetModelParams(key, ModelParams{BifrostOverrides: &schemas.BifrostOverrides{SupportsFastMode: &yes}})
	t.Cleanup(func() { DeleteModelParams(key) })

	got := GetBifrostOverridesForRequest(schemas.Bedrock, "global.anthropic.claude-opus-4-7")
	require.NotNil(t, got)
	assert.True(t, *got.SupportsFastMode)
}

// TestGetBifrostOverridesForRequest_BaseModelFallback verifies a dated model
// falls back to the base-model entry, matching the pricing capability lookup.
func TestGetBifrostOverridesForRequest_BaseModelFallback(t *testing.T) {
	yes := true
	key := OverrideCacheKey("claude-opus-4-7", schemas.Anthropic)
	SetModelParams(key, ModelParams{BifrostOverrides: &schemas.BifrostOverrides{SupportsFastMode: &yes}})
	t.Cleanup(func() { DeleteModelParams(key) })

	got := GetBifrostOverridesForRequest(schemas.Anthropic, "claude-opus-4-7-20260401")
	require.NotNil(t, got, "dated model should fall back to base entry")
	assert.True(t, *got.SupportsFastMode)
}

// TestGetBifrostOverridesForRequest_Miss verifies that an unknown
// (provider, model) pair returns nil cleanly.
func TestGetBifrostOverridesForRequest_Miss(t *testing.T) {
	assert.Nil(t, GetBifrostOverridesForRequest(schemas.Anthropic, "definitely-nonexistent-model-12345"))
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
