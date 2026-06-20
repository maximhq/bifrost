package bedrock

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// normalizeCachedUsage must fold cached read/write tokens into PromptTokens only.
// Bedrock's stream reports TotalTokens directly, so the total must NOT be
// re-summed here. This is the value billed when a stream is cancelled mid-flight.
func TestNormalizeCachedUsage_FoldsCacheIntoPromptOnly(t *testing.T) {
	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     10,
		CompletionTokens: 5,
		TotalTokens:      18, // provider-reported, already includes cache
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedReadTokens:  3,
			CachedWriteTokens: 7,
		},
	}
	normalizeCachedUsage(usage)
	if usage.PromptTokens != 20 {
		t.Fatalf("PromptTokens = %d, want 20 (10 + 3 + 7)", usage.PromptTokens)
	}
	if usage.TotalTokens != 18 {
		t.Fatalf("TotalTokens = %d, want 18 (provider-reported, unchanged)", usage.TotalTokens)
	}
}

func TestNormalizeCachedUsage_NoCacheDetailsIsNoOp(t *testing.T) {
	usage := &schemas.BifrostLLMUsage{PromptTokens: 10, TotalTokens: 15}
	normalizeCachedUsage(usage)
	if usage.PromptTokens != 10 || usage.TotalTokens != 15 {
		t.Fatalf("unexpected mutation: prompt=%d total=%d", usage.PromptTokens, usage.TotalTokens)
	}
}

func TestNormalizeCachedUsage_NilSafe(t *testing.T) {
	normalizeCachedUsage(nil) // must not panic
}
