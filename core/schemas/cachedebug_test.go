package schemas

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCacheDebugContextReturnsOwnedSnapshot verifies semantic cache metadata survives without aliases.
func TestCacheDebugContextReturnsOwnedSnapshot(t *testing.T) {
	ctx := NewBifrostContext(nil, NoDeadline)
	provider := "openai"
	model := "text-embedding-3-small"
	inputTokens := 12
	require.True(t, SetCacheDebugOnContext(ctx, &BifrostCacheDebug{
		ProviderUsed: &provider,
		ModelUsed:    &model,
		InputTokens:  &inputTokens,
	}))

	first, ok := CacheDebugFromContext(ctx)
	require.True(t, ok)
	*first.InputTokens = 999

	second, ok := CacheDebugFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, 12, *second.InputTokens)
}
