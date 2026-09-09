package anthropic

import (
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// realThinkingUsage is a verbatim usage block from a Claude extended-thinking
// response. Note thinking_tokens (39) is a SUBSET of output_tokens (87) — the two
// must never be summed.
const realThinkingUsage = `{
  "input_tokens": 23,
  "cache_creation_input_tokens": 0,
  "cache_read_input_tokens": 0,
  "cache_creation": {"ephemeral_5m_input_tokens": 0, "ephemeral_1h_input_tokens": 0},
  "output_tokens": 87,
  "output_tokens_details": {"thinking_tokens": 39}
}`

func mustParseUsage(t *testing.T, raw string) *AnthropicUsage {
	t.Helper()
	var u AnthropicUsage
	if err := sonic.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatalf("unmarshal usage: %v", err)
	}
	return &u
}

func TestAnthropicUsage_UnmarshalsThinkingTokens(t *testing.T) {
	u := mustParseUsage(t, realThinkingUsage)
	if u.OutputTokensDetails == nil {
		t.Fatal("OutputTokensDetails is nil; thinking_tokens was dropped at unmarshal")
	}
	if got := u.OutputTokensDetails.ThinkingTokens; got != 39 {
		t.Errorf("ThinkingTokens = %d, want 39", got)
	}
}

func TestChatConversion_ThinkingTokensAreSubsetOfCompletion(t *testing.T) {
	resp := &AnthropicMessageResponse{Usage: mustParseUsage(t, realThinkingUsage)}
	got := resp.ToBifrostChatResponse(nil)

	if got.Usage == nil || got.Usage.CompletionTokensDetails == nil {
		t.Fatal("CompletionTokensDetails is nil; reasoning tokens were dropped")
	}
	if r := got.Usage.CompletionTokensDetails.ReasoningTokens; r != 39 {
		t.Errorf("ReasoningTokens = %d, want 39", r)
	}
	// The invariant: reasoning is inside completion, not added to it.
	if got.Usage.CompletionTokens != 87 {
		t.Errorf("CompletionTokens = %d, want 87 (thinking must not be added)", got.Usage.CompletionTokens)
	}
	if got.Usage.CompletionTokensDetails.ReasoningTokens > got.Usage.CompletionTokens {
		t.Error("invariant violated: ReasoningTokens > CompletionTokens")
	}
	if got.Usage.TotalTokens != got.Usage.PromptTokens+got.Usage.CompletionTokens {
		t.Errorf("TotalTokens = %d, want PromptTokens+CompletionTokens = %d",
			got.Usage.TotalTokens, got.Usage.PromptTokens+got.Usage.CompletionTokens)
	}
}

func TestChatConversion_NoThinkingLeavesReasoningZero(t *testing.T) {
	resp := &AnthropicMessageResponse{
		Usage: mustParseUsage(t, `{"input_tokens": 10, "output_tokens": 5}`),
	}
	got := resp.ToBifrostChatResponse(nil)
	if got.Usage.CompletionTokensDetails != nil &&
		got.Usage.CompletionTokensDetails.ReasoningTokens != 0 {
		t.Errorf("ReasoningTokens = %d on a non-thinking response, want 0",
			got.Usage.CompletionTokensDetails.ReasoningTokens)
	}
}

func TestResponsesConversion_CarriesThinkingTokens(t *testing.T) {
	got := ConvertAnthropicUsageToBifrostUsage(mustParseUsage(t, realThinkingUsage))
	if got == nil || got.OutputTokensDetails == nil {
		t.Fatal("OutputTokensDetails is nil; reasoning tokens were dropped")
	}
	if r := got.OutputTokensDetails.ReasoningTokens; r != 39 {
		t.Errorf("ReasoningTokens = %d, want 39", r)
	}
	if got.OutputTokens != 87 {
		t.Errorf("OutputTokens = %d, want 87 (thinking must not be added)", got.OutputTokens)
	}
}

func TestUsageRoundTrip_PreservesThinkingTokens(t *testing.T) {
	original := mustParseUsage(t, realThinkingUsage)
	back := ConvertBifrostUsageToAnthropicUsage(ConvertAnthropicUsageToBifrostUsage(original))

	if back == nil || back.OutputTokensDetails == nil {
		t.Fatal("thinking tokens lost on Anthropic->Bifrost->Anthropic round trip")
	}
	if back.OutputTokensDetails.ThinkingTokens != original.OutputTokensDetails.ThinkingTokens {
		t.Errorf("ThinkingTokens = %d after round trip, want %d",
			back.OutputTokensDetails.ThinkingTokens, original.OutputTokensDetails.ThinkingTokens)
	}
	if back.OutputTokens != original.OutputTokens {
		t.Errorf("OutputTokens = %d after round trip, want %d", back.OutputTokens, original.OutputTokens)
	}
}

// Anthropic reports thinking tokens as a running per-request total across
// message_start and message_delta. The accumulator must max-merge, not sum.
func TestResponsesAccumulator_MaxMergesRatherThanSums(t *testing.T) {
	usage := &schemas.ResponsesResponseUsage{}
	billed := &schemas.BifrostLLMUsage{}

	for _, raw := range []string{
		`{"input_tokens": 23, "output_tokens": 20, "output_tokens_details": {"thinking_tokens": 20}}`,
		`{"input_tokens": 23, "output_tokens": 87, "output_tokens_details": {"thinking_tokens": 39}}`,
		// Out-of-order/stale event must not regress the accumulated value.
		`{"input_tokens": 23, "output_tokens": 50, "output_tokens_details": {"thinking_tokens": 30}}`,
	} {
		accumulateAnthropicResponsesUsage(usage, billed, mustParseUsage(t, raw))
	}

	if usage.OutputTokensDetails == nil {
		t.Fatal("OutputTokensDetails is nil after accumulation")
	}
	if r := usage.OutputTokensDetails.ReasoningTokens; r != 39 {
		t.Errorf("ReasoningTokens = %d, want 39 (max, not sum of 20+39+30=89)", r)
	}
	if billed.CompletionTokensDetails == nil {
		t.Fatal("billed usage did not receive reasoning tokens")
	}
	if r := billed.CompletionTokensDetails.ReasoningTokens; r != 39 {
		t.Errorf("billed ReasoningTokens = %d, want 39", r)
	}
	if usage.OutputTokensDetails.ReasoningTokens > usage.OutputTokens {
		t.Error("invariant violated: ReasoningTokens > OutputTokens")
	}
}

func TestPassthroughStream_MergesThinkingTokensAcrossEvents(t *testing.T) {
	var acc AnthropicPassthroughStreamUsage

	// message_start nests usage under message.usage and has no output counts yet.
	acc.ObserveEvent([]byte(`{"type":"message_start","message":{"usage":{"input_tokens":23,"output_tokens":0}}}`))
	// message_delta carries usage at the top level, including the thinking breakdown.
	got := acc.ObserveEvent([]byte(`{"type":"message_delta","usage":{"input_tokens":23,"output_tokens":87,"output_tokens_details":{"thinking_tokens":39}}}`))

	if got == nil || got.LLMUsage == nil {
		t.Fatal("no usage assembled from stream")
	}
	if got.LLMUsage.CompletionTokensDetails == nil {
		t.Fatal("CompletionTokensDetails is nil; reasoning tokens were dropped in streaming passthrough")
	}
	if r := got.LLMUsage.CompletionTokensDetails.ReasoningTokens; r != 39 {
		t.Errorf("ReasoningTokens = %d, want 39", r)
	}
	if got.LLMUsage.CompletionTokens != 87 {
		t.Errorf("CompletionTokens = %d, want 87", got.LLMUsage.CompletionTokens)
	}
}
