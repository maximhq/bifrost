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

func TestAccumulateAnthropicResponsesUsage_MirrorsCacheIntoBilledUsage(t *testing.T) {
	responseUsage := &schemas.ResponsesResponseUsage{}
	billedUsage := &schemas.BifrostLLMUsage{}
	upstreamUsage := &AnthropicUsage{
		InputTokens:              2,
		CacheReadInputTokens:     1300,
		CacheCreationInputTokens: 78456,
		CacheCreation: AnthropicUsageCacheCreation{
			Ephemeral1hInputTokens: 78456,
		},
		OutputTokens: 3,
	}

	accumulateAnthropicResponsesUsage(responseUsage, billedUsage, upstreamUsage)

	if responseUsage.InputTokens != 2 || responseUsage.OutputTokens != 3 || responseUsage.TotalTokens != 5 {
		t.Fatalf("unexpected response usage before final normalization: %+v", responseUsage)
	}
	if responseUsage.InputTokensDetails == nil {
		t.Fatal("expected response cache details")
	}
	if responseUsage.InputTokensDetails.CachedReadTokens != 1300 {
		t.Fatalf("response cached read = %d, want 1300", responseUsage.InputTokensDetails.CachedReadTokens)
	}
	if responseUsage.InputTokensDetails.CachedWriteTokens != 78456 {
		t.Fatalf("response cached write = %d, want 78456", responseUsage.InputTokensDetails.CachedWriteTokens)
	}
	if responseUsage.InputTokensDetails.CachedWriteTokenDetails == nil ||
		responseUsage.InputTokensDetails.CachedWriteTokenDetails.CachedWriteTokens1h != 78456 {
		t.Fatalf("response cached write details = %+v, want 1h=78456", responseUsage.InputTokensDetails.CachedWriteTokenDetails)
	}

	if billedUsage.PromptTokens != 2 || billedUsage.CompletionTokens != 3 || billedUsage.TotalTokens != 5 {
		t.Fatalf("unexpected billed usage before normalization: %+v", billedUsage)
	}
	if billedUsage.PromptTokensDetails == nil {
		t.Fatal("expected billed cache details")
	}
	if billedUsage.PromptTokensDetails.CachedReadTokens != 1300 {
		t.Fatalf("billed cached read = %d, want 1300", billedUsage.PromptTokensDetails.CachedReadTokens)
	}
	if billedUsage.PromptTokensDetails.CachedWriteTokens != 78456 {
		t.Fatalf("billed cached write = %d, want 78456", billedUsage.PromptTokensDetails.CachedWriteTokens)
	}
	if billedUsage.PromptTokensDetails.CachedWriteTokenDetails == nil ||
		billedUsage.PromptTokensDetails.CachedWriteTokenDetails.CachedWriteTokens1h != 78456 {
		t.Fatalf("billed cached write details = %+v, want 1h=78456", billedUsage.PromptTokensDetails.CachedWriteTokenDetails)
	}

	normalizeCachedUsage(billedUsage)
	if billedUsage.PromptTokens != 79758 {
		t.Fatalf("normalized billed prompt = %d, want 79758", billedUsage.PromptTokens)
	}
	if billedUsage.TotalTokens != 79761 {
		t.Fatalf("normalized billed total = %d, want 79761", billedUsage.TotalTokens)
	}
}
