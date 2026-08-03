package utils

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetModelCapabilities_CacheHit verifies the lookup returns the stored
// record for a known key.
func TestGetModelCapabilities_CacheHit(t *testing.T) {
	truePtr := true
	record := schemas.ModelCapabilities{
		SupportsCachePoint: &truePtr,
		ServerTools:        map[string]string{"web_search": "web_search_20260209"},
	}
	SetModelCapability("test/claude-opus-4-7", &record)
	t.Cleanup(func() { DeleteModelCapability("test/claude-opus-4-7") })

	got := GetModelCapabilities("test/claude-opus-4-7")
	require.NotNil(t, got)
	require.NotNil(t, got.SupportsCachePoint)
	assert.True(t, *got.SupportsCachePoint)
	assert.Equal(t, "web_search_20260209", got.ServerTools["web_search"])
}

// TestGetModelCapabilities_CacheMiss verifies missing keys return nil cleanly so
// callers can detect absence and fall back to hardcoded helpers.
func TestGetModelCapabilities_CacheMiss(t *testing.T) {
	got := GetModelCapabilities("test/nonexistent-model-12345")
	assert.Nil(t, got)
}

// TestCapabilitiesFor_ProviderIsKey verifies the same model on
// different providers resolves to distinct records — provider is part of the
// key (mirrors pricing), so there is no cross-provider collision.
func TestCapabilitiesFor_ProviderIsKey(t *testing.T) {
	yes, no := true, false
	anthKey := CapabilityCacheKey("claude-opus-4-8", schemas.Anthropic)
	vertexKey := CapabilityCacheKey("claude-opus-4-8", schemas.Vertex)
	SetModelCapability(anthKey, &schemas.ModelCapabilities{SupportsFastMode: &yes})
	SetModelCapability(vertexKey, &schemas.ModelCapabilities{SupportsFastMode: &no})
	t.Cleanup(func() { DeleteModelCapability(anthKey); DeleteModelCapability(vertexKey) })

	anth := CapabilitiesFor(schemas.Anthropic, "claude-opus-4-8")
	require.NotNil(t, anth)
	assert.True(t, *anth.SupportsFastMode)

	vertex := CapabilitiesFor(schemas.Vertex, "claude-opus-4-8")
	require.NotNil(t, vertex)
	assert.False(t, *vertex.SupportsFastMode)
}

// TestCapabilitiesFor_NoCrossProviderCollision verifies a bare
// model on a non-Anthropic provider does NOT borrow the Anthropic-native entry —
// it misses cleanly so the caller falls back to substring detection.
func TestCapabilitiesFor_NoCrossProviderCollision(t *testing.T) {
	yes := true
	anthKey := CapabilityCacheKey("claude-opus-4-8", schemas.Anthropic)
	SetModelCapability(anthKey, &schemas.ModelCapabilities{SupportsFastMode: &yes})
	t.Cleanup(func() {
		DeleteModelCapability(anthKey)
		for _, p := range []schemas.ModelProvider{schemas.Vertex, schemas.Bedrock, schemas.Azure} {
			DeleteModelCapability(CapabilityCacheKey("claude-opus-4-8", p))
		}
	})

	assert.Nil(t, CapabilitiesFor(schemas.Vertex, "claude-opus-4-8"))
	assert.Nil(t, CapabilitiesFor(schemas.Bedrock, "claude-opus-4-8"))
	assert.Nil(t, CapabilitiesFor(schemas.Azure, "claude-opus-4-8"))
}

// TestCapabilitiesFor_BedrockDotted verifies a Bedrock dotted
// runtime id ("us.anthropic.claude-...-v1:0") collapses to the bare base_model
// the datasheet keys records under. This is exactly the case plain
// BaseModelName cannot handle (it leaves the provider/region prefix and
// ":version" intact), so it proves the stronger read-side normalization.
func TestCapabilitiesFor_BedrockDotted(t *testing.T) {
	yes := true
	key := CapabilityCacheKey("claude-opus-4-7", schemas.Bedrock)
	dotted := "us.anthropic.claude-opus-4-7-20250101-v1:0"
	SetModelCapability(key, &schemas.ModelCapabilities{SupportsFastMode: &yes})
	t.Cleanup(func() {
		DeleteModelCapability(key)
		DeleteModelCapability(CapabilityCacheKey(dotted, schemas.Bedrock))
	})

	got := CapabilitiesFor(schemas.Bedrock, dotted)
	require.NotNil(t, got, "dotted Bedrock id should normalize to the base entry")
	assert.True(t, *got.SupportsFastMode)
}

// TestCapabilitiesFor_VertexAtVersion verifies a Vertex
// "@version" runtime id collapses to the bare base_model.
func TestCapabilitiesFor_VertexAtVersion(t *testing.T) {
	yes := true
	key := CapabilityCacheKey("claude-haiku-4-5", schemas.Vertex)
	versioned := "claude-haiku-4-5@20251001"
	SetModelCapability(key, &schemas.ModelCapabilities{SupportsFastMode: &yes})
	t.Cleanup(func() {
		DeleteModelCapability(key)
		DeleteModelCapability(CapabilityCacheKey(versioned, schemas.Vertex))
	})

	got := CapabilitiesFor(schemas.Vertex, versioned)
	require.NotNil(t, got, "@version Vertex id should normalize to the base entry")
	assert.True(t, *got.SupportsFastMode)
}

// TestCapabilitiesFor_BaseModelFallback verifies a dated model falls back to the
// base-model entry, and that the resolved record is then cached under the name
// that was actually asked for so the fallback walk runs once.
func TestCapabilitiesFor_BaseModelFallback(t *testing.T) {
	yes := true
	key := CapabilityCacheKey("claude-opus-4-7", schemas.Anthropic)
	dated := "claude-opus-4-7-20260401"
	SetModelCapability(key, &schemas.ModelCapabilities{SupportsFastMode: &yes})
	t.Cleanup(func() {
		DeleteModelCapability(key)
		DeleteModelCapability(CapabilityCacheKey(dated, schemas.Anthropic))
	})

	got := CapabilitiesFor(schemas.Anthropic, dated)
	require.NotNil(t, got, "dated model should fall back to base entry")
	assert.True(t, *got.SupportsFastMode)

	// Alias-on-hit: the dated name now resolves directly.
	assert.NotNil(t, GetModelCapabilities(CapabilityCacheKey(dated, schemas.Anthropic)))
}

// TestCapabilitiesFor_Miss verifies that an unknown
// (provider, model) pair returns nil cleanly.
func TestCapabilitiesFor_Miss(t *testing.T) {
	assert.Nil(t, CapabilitiesFor(schemas.Anthropic, "definitely-nonexistent-model-12345"))
}

// TestCapabilitiesFor_EmptyModel verifies short-circuit on empty
// model string.
func TestCapabilitiesFor_EmptyModel(t *testing.T) {
	assert.Nil(t, CapabilitiesFor(schemas.Anthropic, ""))
}

// TestCapabilitiesFor_TombstonesAbsentModel verifies a model with no datasheet
// row is queried once and then answered from cache. Without this, the ~10
// capability checks a single request makes would each hit the database.
func TestCapabilitiesFor_TombstonesAbsentModel(t *testing.T) {
	calls := 0
	SetCacheMissHandler(func(string) *schemas.ModelCapabilities {
		calls++
		return nil
	})
	t.Cleanup(func() {
		SetCacheMissHandler(nil)
		DeleteModelCapability(CapabilityCacheKey("absent-model-tombstone", schemas.OpenAI))
	})

	for range 5 {
		assert.Nil(t, CapabilitiesFor(schemas.OpenAI, "absent-model-tombstone"))
	}
	assert.Equal(t, 1, calls, "absent model should be queried once, then served from the tombstone")
}

// TestCapabilitiesFor_MissHandlerResultIsCached verifies a found record is
// fetched once and reused.
func TestCapabilitiesFor_MissHandlerResultIsCached(t *testing.T) {
	yes := true
	calls := 0
	SetCacheMissHandler(func(rowKey string) *schemas.ModelCapabilities {
		calls++
		if rowKey == "lazy-model" {
			return &schemas.ModelCapabilities{SupportsFastMode: &yes}
		}
		return nil
	})
	t.Cleanup(func() {
		SetCacheMissHandler(nil)
		DeleteModelCapability(CapabilityCacheKey("lazy-model", schemas.OpenAI))
	})

	for range 3 {
		got := CapabilitiesFor(schemas.OpenAI, "lazy-model")
		require.NotNil(t, got)
		assert.True(t, *got.SupportsFastMode)
	}
	assert.Equal(t, 1, calls)
}

// TestCapabilitiesFor_AliasIndexResolvesRowKey verifies a runtime name that is
// not itself a datasheet row key is resolved through the alias index, so the
// miss handler is asked for the row that actually carries the record.
func TestCapabilitiesFor_AliasIndexResolvesRowKey(t *testing.T) {
	yes := true
	var asked []string
	SetModelCapabilitiesAliases(map[string]string{
		CapabilityCacheKey("gpt-5.1-chat", schemas.Azure): "azure/gpt-5.1-chat",
	})
	SetCacheMissHandler(func(rowKey string) *schemas.ModelCapabilities {
		asked = append(asked, rowKey)
		if rowKey == "azure/gpt-5.1-chat" {
			return &schemas.ModelCapabilities{SupportsFastMode: &yes}
		}
		return nil
	})
	t.Cleanup(func() {
		SetCacheMissHandler(nil)
		SetModelCapabilitiesAliases(nil)
		DeleteModelCapability(CapabilityCacheKey("gpt-5.1-chat", schemas.Azure))
		DeleteModelCapability(CapabilityCacheKey("azure/gpt-5.1-chat", schemas.Azure))
	})

	got := CapabilitiesFor(schemas.Azure, "gpt-5.1-chat")
	require.NotNil(t, got)
	assert.Contains(t, asked, "azure/gpt-5.1-chat")
}

// TestInvalidateCapabilities_InvalidatesEverything verifies a datasheet
// reload invalidates both cached records and tombstones, so an updated sheet is
// picked up without walking the cache.
func TestInvalidateCapabilities_InvalidatesEverything(t *testing.T) {
	yes := true
	key := CapabilityCacheKey("regen-model", schemas.OpenAI)
	SetModelCapability(key, &schemas.ModelCapabilities{SupportsFastMode: &yes})
	require.NotNil(t, GetModelCapabilities(key))

	// A tombstone from an absent model must be invalidated too, otherwise a
	// model added by the sync would stay invisible.
	tombstoneKey := CapabilityCacheKey("regen-absent", schemas.OpenAI)
	require.Nil(t, CapabilitiesFor(schemas.OpenAI, "regen-absent"))

	InvalidateCapabilities()
	t.Cleanup(func() {
		DeleteModelCapability(key)
		DeleteModelCapability(tombstoneKey)
	})

	assert.Nil(t, GetModelCapabilities(key), "records must not survive a reload")

	calls := 0
	SetCacheMissHandler(func(string) *schemas.ModelCapabilities {
		calls++
		return nil
	})
	t.Cleanup(func() { SetCacheMissHandler(nil) })
	assert.Nil(t, CapabilitiesFor(schemas.OpenAI, "regen-absent"))
	assert.Equal(t, 1, calls, "tombstone must not survive a reload")
}

// TestMaxOutputTokensLivesOnTheRecord verifies the ceiling and the behaviour
// flags are one record — the merge that removed the separate model-params store.
func TestMaxOutputTokensLivesOnTheRecord(t *testing.T) {
	maxTokens := 8192
	truePtr := true
	key := CapabilityCacheKey("combo-model", schemas.OpenAI)
	SetModelCapability(key, &schemas.ModelCapabilities{
		MaxOutputTokens:    &maxTokens,
		SupportsCachePoint: &truePtr,
	})
	t.Cleanup(func() { DeleteModelCapability(key) })

	assert.Equal(t, 8192, GetMaxOutputTokensOrDefault(schemas.OpenAI, "combo-model", 1024))

	got := CapabilitiesFor(schemas.OpenAI, "combo-model")
	require.NotNil(t, got)
	require.NotNil(t, got.SupportsCachePoint)
	assert.True(t, *got.SupportsCachePoint)
}
