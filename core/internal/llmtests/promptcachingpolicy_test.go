package llmtests

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
)

// TestCacheReadPolicyFor pins the per-provider cache-read rules used by the
// multi-turn prompt-caching scenarios. The rules are asserted here rather than
// only through the live scenarios because a provider that silently falls into
// the generic branch fails intermittently, and only against a real endpoint.
func TestCacheReadPolicyFor(t *testing.T) {
	t.Parallel()

	t.Run("OpenAI is aggregate only", func(t *testing.T) {
		t.Parallel()

		policy := cacheReadPolicyFor(schemas.OpenAI)
		assert.True(t, policy.aggregateOnly,
			"OpenAI caches automatically and best-effort, so individual turns must not be asserted")
		assert.False(t, policy.requireStableRead,
			"an individual OpenAI turn may miss, so turn-over-turn stability must not be asserted")
	})

	t.Run("OpenRouter requires only a non-zero read", func(t *testing.T) {
		t.Parallel()

		policy := cacheReadPolicyFor(schemas.OpenRouter)
		assert.False(t, policy.aggregateOnly,
			"OpenRouter forwards explicit breakpoints, so every turn should read from the cache")
		assert.Zero(t, policy.minReadPercentage,
			"OpenRouter converts only input_text blocks into prompt_cache_breakpoint, so the "+
				"surviving prefix collapses to the system breakpoint alone on function_call and "+
				"assistant turns and can be well under half of a growing request")
		assert.False(t, policy.requireStableRead,
			"the same oscillation makes a turn-over-turn halving check read a documented "+
				"conversion rule as a prefix mismatch")
	})

	t.Run("explicit caching providers keep the strict rules", func(t *testing.T) {
		t.Parallel()

		for _, provider := range []schemas.ModelProvider{schemas.Anthropic, schemas.Vertex, schemas.Bedrock} {
			policy := cacheReadPolicyFor(provider)
			assert.False(t, policy.aggregateOnly, "%s caches explicitly", provider)
			assert.Equal(t, 0.50, policy.minReadPercentage,
				"%s keeps a stable cacheable prefix, so a drop below half signals broken key ordering", provider)
			assert.True(t, policy.requireStableRead,
				"%s should not lose its cached prefix as the conversation grows", provider)
		}
	})
}
