package anthropic

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// streamUsageEvent pairs an Anthropic stream event type with its usage snapshot,
// for the synthetic-fixture tests below.
type streamUsageEvent struct {
	eventType AnthropicStreamEventType
	usage     *AnthropicUsage
}

// accumulateStreamUsage mirrors the usage-accumulation pipeline inside
// HandleAnthropicChatCompletionStreaming: it feeds a synthetic event sequence
// through applyStreamUsageEvent (capturing message_start as the start-side
// snapshot and tracking whether message_delta overwrote it) and applies the
// end-of-stream normalizeCachedUsage fold only when no message_delta arrived.
// This is the smallest extraction that lets the fixture tests exercise the
// exact production code path without a live SSE harness.
func accumulateStreamUsage(events []streamUsageEvent) *schemas.BifrostLLMUsage {
	usage := &schemas.BifrostLLMUsage{}
	var startSnapshot *AnthropicUsage
	deltaProcessed := false
	for _, ev := range events {
		if ev.eventType == AnthropicStreamEventTypeMessageStart && ev.usage != nil {
			startSnapshot = ev.usage
		}
		if applyStreamUsageEvent(usage, ev.eventType, ev.usage, startSnapshot) {
			deltaProcessed = true
		}
	}
	if !deltaProcessed {
		normalizeCachedUsage(usage)
	}
	return usage
}

// TestAnthropicStreamCacheReadNotDoubleCounted reproduces the operator's turn-2
// production evidence: an Anthropic-protocol stream where the provider emits
// cache-read tokens only in the final message_delta (not at message_start).
//
// Per the Anthropic Messages API spec, message_delta.usage is the AUTHORITATIVE
// post-cache breakdown: input_tokens is the uncached tail, and
// cache_read_input_tokens decomposes the cached portion of the same total.
// message_delta.usage OVERWRITES message_start.usage wholesale — it does NOT
// merge-with or add-to. The authoritative prompt total is therefore
// delta.input_tokens + delta.cache_creation_input_tokens + delta.cache_read_input_tokens
// = 308 + 0 + 20224 = 20532.
//
// The prior accumulator used a max-keep guard on input_tokens for BOTH events,
// which retained message_start.input_tokens (the full 20532 prompt) and then
// folded cache_read on top at stream end via normalizeCachedUsage, producing
// 20532 + 20224 = 40756 — a ~2x double-count of the cached portion. This test
// asserts the post-fix correct value (20532); pre-fix it failed at 40756.
func TestAnthropicStreamCacheReadNotDoubleCounted(t *testing.T) {
	usage := accumulateStreamUsage([]streamUsageEvent{
		{
			eventType: AnthropicStreamEventTypeMessageStart,
			usage: &AnthropicUsage{
				InputTokens:              20532,
				CacheReadInputTokens:     0,
				CacheCreationInputTokens: 0,
				OutputTokens:             1,
			},
		},
		{
			eventType: AnthropicStreamEventTypeMessageDelta,
			usage: &AnthropicUsage{
				InputTokens:              308,
				CacheReadInputTokens:     20224,
				CacheCreationInputTokens: 0,
				OutputTokens:             300,
			},
		},
	})

	if usage.PromptTokens != 20532 {
		t.Fatalf("prompt_tokens = %d, want 20532 (delta.input 308 + cache_read 20224 + cache_creation 0)", usage.PromptTokens)
	}
	if usage.PromptTokensDetails == nil || usage.PromptTokensDetails.CachedReadTokens != 20224 {
		got := 0
		if usage.PromptTokensDetails != nil {
			got = usage.PromptTokensDetails.CachedReadTokens
		}
		t.Fatalf("cached_read_tokens = %d, want 20224 (cache breakdown preserved, not dropped)", got)
	}
}

// TestAnthropicStreamNoCacheReadUnchanged is the no-cache non-regression guard
// (operator's turn-1 evidence): when no cache read occurs, the delta's
// input_tokens equals the start's input_tokens, and the correct prompt_tokens
// is unchanged at 20458. This test MUST pass both pre- and post-fix — it
// proves the overwrite fix does not break the no-cache path.
func TestAnthropicStreamNoCacheReadUnchanged(t *testing.T) {
	usage := accumulateStreamUsage([]streamUsageEvent{
		{
			eventType: AnthropicStreamEventTypeMessageStart,
			usage: &AnthropicUsage{
				InputTokens:              20458,
				CacheReadInputTokens:     0,
				CacheCreationInputTokens: 0,
				OutputTokens:             1,
			},
		},
		{
			eventType: AnthropicStreamEventTypeMessageDelta,
			usage: &AnthropicUsage{
				InputTokens:              20458,
				CacheReadInputTokens:     0,
				CacheCreationInputTokens: 0,
				OutputTokens:             200,
			},
		},
	})

	if usage.PromptTokens != 20458 {
		t.Fatalf("prompt_tokens = %d, want 20458 (no-cache path unchanged)", usage.PromptTokens)
	}
}

// TestAnthropicStreamUsage_NonConformantDeltaOmitsCacheFields proves the
// impossible-zero guard is load-bearing (plan consensus arch-002). A
// non-conformant Anthropic-compatible provider may emit a partial-usage
// message_delta with input_tokens=0 AND OMIT cache_creation_input_tokens +
// cache_read_input_tokens entirely; for non-pointer Go int, omitted fields
// deserialize to 0, so the unconditional event-level overwrite would
// reconstruct PromptTokens = 0+0+0 = 0 — an impossible value for a real
// Anthropic prompt (input_tokens >= 1 always). The guard detects this
// (PromptTokens == 0 && startSnapshot.InputTokens > 0) and restores the
// start-side snapshot as the best available approximation.
//
// This test synthesizes the non-conformant case: message_start carries the
// full breakdown (input=20532, cache_read=20224), then message_delta is
// degenerate (input=0, cache fields omitted). Pre-fix (overwrite without
// guard) prompt_tokens = 0; post-fix (overwrite + guard) prompt_tokens = 20532.
// The test asserts 20532, so it fails pre-fix at 0 — proving the guard fires.
//
// DISCRIMINATOR from TestAnthropicStreamCacheReadNotDoubleCounted: both expect
// 20532 post-fix, but via different code paths — turn-2 reconstructs
// 308+0+20224=20532 from the delta; turn-3 restores start-side 20532 via the
// impossible-zero guard. The distinct test names disambiguate.
func TestAnthropicStreamUsage_NonConformantDeltaOmitsCacheFields(t *testing.T) {
	usage := accumulateStreamUsage([]streamUsageEvent{
		{
			eventType: AnthropicStreamEventTypeMessageStart,
			usage: &AnthropicUsage{
				InputTokens:              20532,
				CacheReadInputTokens:     20224,
				CacheCreationInputTokens: 0,
				OutputTokens:             1,
			},
		},
		{
			eventType: AnthropicStreamEventTypeMessageDelta,
			usage: &AnthropicUsage{
				// Non-conformant: input_tokens=0 AND cache fields omitted
				// (deserialize to 0 for non-pointer Go int).
				InputTokens:              0,
				CacheReadInputTokens:     0,
				CacheCreationInputTokens: 0,
				OutputTokens:             300,
			},
		},
	})

	if usage.PromptTokens != 20532 {
		t.Fatalf("prompt_tokens = %d, want 20532 (impossible-zero guard must restore start-side when non-conformant delta omits cache fields)", usage.PromptTokens)
	}
	if usage.PromptTokensDetails == nil || usage.PromptTokensDetails.CachedReadTokens != 20224 {
		got := 0
		if usage.PromptTokensDetails != nil {
			got = usage.PromptTokensDetails.CachedReadTokens
		}
		t.Fatalf("cached_read_tokens = %d, want 20224 (guard must restore start-side cache breakdown)", got)
	}
}
