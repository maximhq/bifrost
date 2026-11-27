package schemas

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCacheMetadataContextReturnsOwnedSnapshot verifies semantic cache metadata survives without aliases.
func TestCacheMetadataContextReturnsOwnedSnapshot(t *testing.T) {
	ctx := NewBifrostContext(nil, NoDeadline)
	provider := "openai"
	model := "text-embedding-3-small"
	inputTokens := 12
	require.True(t, SetCacheMetadataOnContext(ctx, &BifrostCacheMetadata{
		ProviderUsed: &provider,
		ModelUsed:    &model,
		InputTokens:  &inputTokens,
	}))

	first, ok := CacheMetadataFromContext(ctx)
	require.True(t, ok)
	*first.InputTokens = 999

	second, ok := CacheMetadataFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, 12, *second.InputTokens)
}

func TestLegacyCacheDebugAPIsRemainCompatible(t *testing.T) {
	ctx := NewBifrostContext(nil, NoDeadline)
	provider, model, inputTokens := "openai", "text-embedding-3-small", 12
	require.True(t, SetCacheDebugOnContext(ctx, &BifrostCacheDebug{
		ProviderUsed: &provider,
		ModelUsed:    &model,
		InputTokens:  &inputTokens,
	}))

	metadata, ok := CacheDebugFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, inputTokens, *metadata.InputTokens)
}

func TestMetadataContextKeysPreserveLegacyValues(t *testing.T) {
	assert.Equal(t, BifrostContextKey("bifrost-cache-debug"), BifrostContextKeyCacheMetadata)
	assert.Equal(t, BifrostContextKey("bifrost-guardrail-debug"), BifrostContextKeyGuardrailMetadata)
	assert.Equal(t, BifrostContextKey("bifrost-routing-metadata"), BifrostContextKeyRoutingMetadata)
	assert.Equal(t, BifrostContextKeyCacheMetadata, BifrostContextKeyCacheDebug)
	assert.Equal(t, BifrostContextKeyGuardrailMetadata, BifrostContextKeyGuardrailDebug)
}

// TestCacheLookupContextReturnsOwnedSnapshot pins the lookup outcome as a value a reader owns: a
// direct hit carries no embedding fields and is still recorded, and later edits on either side
// never reach the other.
func TestCacheLookupContextReturnsOwnedSnapshot(t *testing.T) {
	ctx := NewBifrostContext(nil, NoDeadline)
	_, ok := CacheLookupFromContext(ctx)
	assert.False(t, ok, "no lookup recorded yet")
	assert.False(t, SetCacheLookupOnContext(ctx, nil))

	cacheID := "entry-1"
	hitType := "direct"
	recorded := &BifrostCacheMetadata{CacheHit: true, CacheID: &cacheID, HitType: &hitType}
	require.True(t, SetCacheLookupOnContext(ctx, recorded))
	cacheID = "changed-after-recording"

	got, ok := CacheLookupFromContext(ctx)
	require.True(t, ok)
	assert.True(t, got.CacheHit)
	assert.Equal(t, "entry-1", *got.CacheID)
	assert.Equal(t, "direct", *got.HitType)

	*got.CacheID = "changed-by-reader"
	again, _ := CacheLookupFromContext(ctx)
	assert.Equal(t, "entry-1", *again.CacheID)
}
