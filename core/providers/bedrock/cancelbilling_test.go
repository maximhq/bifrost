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

func TestAccumulateBedrockResponsesUsage_MirrorsCacheIntoBilledUsage(t *testing.T) {
	responseUsage := &schemas.ResponsesResponseUsage{}
	billedUsage := &schemas.BifrostLLMUsage{}
	cacheDetails := []BedrockCacheWriteDetails{
		{TTL: BedrockCacheWriteTTL1h, InputTokens: 700},
	}
	upstreamUsage := &BedrockTokenUsage{
		InputTokens:           10,
		OutputTokens:          5,
		TotalTokens:           1515,
		CacheReadInputTokens:  800,
		CacheWriteInputTokens: 700,
		CacheDetails:          &cacheDetails,
	}

	accumulateBedrockResponsesUsage(responseUsage, billedUsage, upstreamUsage)

	if responseUsage.InputTokens != 10 || responseUsage.OutputTokens != 5 || responseUsage.TotalTokens != 1515 {
		t.Fatalf("unexpected response usage: %+v", responseUsage)
	}
	if responseUsage.InputTokensDetails == nil {
		t.Fatal("expected response cache details")
	}
	if responseUsage.InputTokensDetails.CachedReadTokens != 800 {
		t.Fatalf("response cached read = %d, want 800", responseUsage.InputTokensDetails.CachedReadTokens)
	}
	if responseUsage.InputTokensDetails.CachedWriteTokens != 700 {
		t.Fatalf("response cached write = %d, want 700", responseUsage.InputTokensDetails.CachedWriteTokens)
	}
	if responseUsage.InputTokensDetails.CachedWriteTokenDetails == nil ||
		responseUsage.InputTokensDetails.CachedWriteTokenDetails.CachedWriteTokens1h != 700 {
		t.Fatalf("response cached write details = %+v, want 1h=700", responseUsage.InputTokensDetails.CachedWriteTokenDetails)
	}

	if billedUsage.PromptTokens != 10 || billedUsage.CompletionTokens != 5 || billedUsage.TotalTokens != 1515 {
		t.Fatalf("unexpected billed usage before normalization: %+v", billedUsage)
	}
	if billedUsage.PromptTokensDetails == nil {
		t.Fatal("expected billed cache details")
	}
	if billedUsage.PromptTokensDetails.CachedReadTokens != 800 {
		t.Fatalf("billed cached read = %d, want 800", billedUsage.PromptTokensDetails.CachedReadTokens)
	}
	if billedUsage.PromptTokensDetails.CachedWriteTokens != 700 {
		t.Fatalf("billed cached write = %d, want 700", billedUsage.PromptTokensDetails.CachedWriteTokens)
	}
	if billedUsage.PromptTokensDetails.CachedWriteTokenDetails == nil ||
		billedUsage.PromptTokensDetails.CachedWriteTokenDetails.CachedWriteTokens1h != 700 {
		t.Fatalf("billed cached write details = %+v, want 1h=700", billedUsage.PromptTokensDetails.CachedWriteTokenDetails)
	}

	normalizeCachedUsage(billedUsage)
	if billedUsage.PromptTokens != 1510 {
		t.Fatalf("normalized billed prompt = %d, want 1510", billedUsage.PromptTokens)
	}
	if billedUsage.TotalTokens != 1515 {
		t.Fatalf("normalized billed total = %d, want 1515", billedUsage.TotalTokens)
	}
}
