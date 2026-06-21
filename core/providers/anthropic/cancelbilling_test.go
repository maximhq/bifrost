package anthropic

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// normalizeCachedUsage must fold cached read/write tokens into the top-level
// prompt + total counters, matching the per-request totals Anthropic reports
// only at the end of a stream. This is the value billed when a stream is
// cancelled mid-flight.
func TestNormalizeCachedUsage_FoldsCacheIntoPromptAndTotal(t *testing.T) {
	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     10,
		CompletionTokens: 5,
		TotalTokens:      15,
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedReadTokens:  3,
			CachedWriteTokens: 7,
		},
	}
	normalizeCachedUsage(usage)
	if usage.PromptTokens != 20 {
		t.Fatalf("PromptTokens = %d, want 20 (10 + 3 + 7)", usage.PromptTokens)
	}
	if usage.TotalTokens != 25 {
		t.Fatalf("TotalTokens = %d, want 25 (15 + 3 + 7)", usage.TotalTokens)
	}
	if usage.CompletionTokens != 5 {
		t.Fatalf("CompletionTokens = %d, want 5 (unchanged)", usage.CompletionTokens)
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
