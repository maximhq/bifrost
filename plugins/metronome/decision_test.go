package metronome

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func decisionResponse(provider schemas.ModelProvider, model string, usage *schemas.BifrostLLMUsage) *schemas.BifrostResponse {
	response := &schemas.BifrostResponse{DecisionResponse: &schemas.BifrostDecisionResponse{Model: model, Usage: usage}}
	response.PopulateExtraFields(schemas.DecisionRequest, provider, model, model)
	return response
}

func exportDecision(t *testing.T, ctx *schemas.BifrostContext, response *schemas.BifrostResponse) (Event[TokenUsage], bool) {
	t.Helper()
	p := &Plugin{logger: testLogger{}, queue: make(chan Event[TokenUsage], 1), ctx: context.Background()}
	p.PreLLMHook(ctx, nil)
	if _, _, err := p.PostLLMHook(ctx, response, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-p.queue:
		return event, true
	default:
		return Event[TokenUsage]{}, false
	}
}

// TestDecisionUsageIsExportedAsInputOnly pins that a TypeSafe decision is billed
// on its input tokens through the token-billing event, with its free output
// tokens not reported as generated output and the event marked as a decision.
func TestDecisionUsageIsExportedAsInputOnly(t *testing.T) {
	ctx := audioContext("vk-1", "request")
	event, ok := exportDecision(t, ctx, decisionResponse(schemas.Typesafe, "jev-1.13.0", &schemas.BifrostLLMUsage{PromptTokens: 296, CompletionTokens: 20, TotalTokens: 316}))
	if !ok {
		t.Fatal("decision usage was not exported")
	}
	u := event.Properties
	if event.EventType != "token-billing" || event.CustomerID != "vk-1" || u.Provider != "typesafe" || u.Model != "typesafe/jev-1.13.0" ||
		u.InputTokens != 296 || u.OutputTokens != 0 || u.CachedInputTokens != 0 || u.CachedWriteTokens != 0 || u.RequestType != "decisions" {
		t.Fatalf("unexpected decision event: %+v", event)
	}

	// A TypeSafe-based custom provider is recognised through its base type.
	custom := audioContext("vk-1", "request")
	custom.SetValue(schemas.BifrostContextKeyBaseProviderType, schemas.Typesafe)
	event, ok = exportDecision(t, custom, decisionResponse(schemas.ModelProvider("system-one"), "jev-latest", &schemas.BifrostLLMUsage{PromptTokens: 50, CompletionTokens: 7}))
	if !ok || event.Properties.InputTokens != 50 || event.Properties.OutputTokens != 0 {
		t.Fatalf("custom TypeSafe decision event: %+v (queued %v)", event, ok)
	}
}

// TestEmulatedDecisionKeepsGeneratedOutput pins that a decision emulated
// through an LLM still bills the output tokens that LLM generated.
func TestEmulatedDecisionKeepsGeneratedOutput(t *testing.T) {
	event, ok := exportDecision(t, audioContext("vk-1", "request"), decisionResponse(schemas.OpenAI, "gpt-test", &schemas.BifrostLLMUsage{PromptTokens: 120, CompletionTokens: 30}))
	if !ok || event.Properties.InputTokens != 120 || event.Properties.OutputTokens != 30 || event.Properties.RequestType != "decisions" {
		t.Fatalf("emulated decision event: %+v (queued %v)", event, ok)
	}
}

// idemContext is a decision context for a virtual key carrying a client
// Idempotency-Key; each call stands for a fresh attempt.
func idemContext(vk, key string) *schemas.BifrostContext {
	ctx := audioContext(vk, "request")
	if key != "" {
		ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{"idempotency-key": {key}})
	}
	return ctx
}

// TestDecisionIdempotentReplayIsBilledReplaySafely pins that a replay keeps its
// usage and shares the original transaction ID, so Metronome deduplicates it
// when the first event arrived and bills it when the first response was lost.
func TestDecisionIdempotentReplayIsBilledReplaySafely(t *testing.T) {
	usage := &schemas.BifrostLLMUsage{PromptTokens: 10, CompletionTokens: 2}
	first, ok1 := exportDecision(t, idemContext("vk-1", "key-1"), decisionResponse(schemas.Typesafe, "jev-1.13.0", usage))
	replay, ok2 := exportDecision(t, idemContext("vk-1", "key-1"), decisionResponse(schemas.Typesafe, "jev-1.13.0", usage))
	if !ok1 || !ok2 {
		t.Fatalf("first and replay must both be exported: %v %v", ok1, ok2)
	}
	if first.TransactionID != replay.TransactionID || replay.Properties.InputTokens != 10 {
		t.Fatalf("replay must reuse the transaction ID and usage: %q vs %q", first.TransactionID, replay.TransactionID)
	}
	// A lost first response leaves only the replay, which is still exported.
	other, _ := exportDecision(t, idemContext("vk-2", "key-1"), decisionResponse(schemas.Typesafe, "jev-1.13.0", usage))
	diffKey, _ := exportDecision(t, idemContext("vk-1", "key-2"), decisionResponse(schemas.Typesafe, "jev-1.13.0", usage))
	if other.TransactionID == first.TransactionID || diffKey.TransactionID == first.TransactionID {
		t.Fatal("tenants and keys must not share a transaction ID")
	}
}

// TestDecisionWithoutIdempotencyKeyKeepsPerAttemptID pins that no key leaves
// each attempt on its own UUID-based ID, and that other request types ignore
// the header.
func TestDecisionWithoutIdempotencyKeyKeepsPerAttemptID(t *testing.T) {
	usage := &schemas.BifrostLLMUsage{PromptTokens: 10}
	a, _ := exportDecision(t, idemContext("vk-1", ""), decisionResponse(schemas.Typesafe, "jev-1.13.0", usage))
	b, _ := exportDecision(t, idemContext("vk-1", ""), decisionResponse(schemas.Typesafe, "jev-1.13.0", usage))
	if a.TransactionID == b.TransactionID {
		t.Fatal("attempts without a key must not share an ID")
	}
	c1, _ := exportDecision(t, idemContext("vk-1", "key-1"), tokenIdentityResponse())
	c2, _ := exportDecision(t, idemContext("vk-1", "key-1"), tokenIdentityResponse())
	if c1.TransactionID == c2.TransactionID || strings.HasPrefix(c1.TransactionID, "bifrost-idem-") {
		t.Fatal("chat events must stay per-attempt")
	}
}

// TestGenerationEventShapeUnchanged pins that generation events do not gain
// the decision marker, so existing billable metrics see the same payload.
func TestGenerationEventShapeUnchanged(t *testing.T) {
	event, ok := exportDecision(t, audioContext("vk-1", "request"), tokenIdentityResponse())
	if !ok {
		t.Fatal("chat usage was not exported")
	}
	payload, err := json.Marshal(event.Properties)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "request_type") {
		t.Fatalf("generation event gained request_type: %s", payload)
	}
}

func creditDecisionUsage() *schemas.BifrostLLMUsage {
	return &schemas.BifrostLLMUsage{
		PromptTokens: 296, CompletionTokens: 20, CreditCostUSD: "0.031",
		Cost: &schemas.BifrostCost{TotalCost: 0.031},
	}
}

// TestCreditPricedDecisionCarriesUSDSpend pins that a hosted decision priced
// from credits is exported in the existing event with its exact USD spend and
// no billable token counts, and that a replay still yields one transaction.
func TestCreditPricedDecisionCarriesUSDSpend(t *testing.T) {
	first, ok1 := exportDecision(t, idemContext("vk-1", "key-1"), decisionResponse(schemas.Typesafe, "jev-1.13.0", creditDecisionUsage()))
	replay, ok2 := exportDecision(t, idemContext("vk-1", "key-1"), decisionResponse(schemas.Typesafe, "jev-1.13.0", creditDecisionUsage()))
	if !ok1 || !ok2 {
		t.Fatal("credit-priced decision was not exported")
	}
	if first.EventType != "token-billing" || first.Properties.CostUSD != "0.031" || first.Properties.InputTokens != 0 || first.Properties.OutputTokens != 0 {
		t.Fatalf("event = %+v", first)
	}
	if first.TransactionID != replay.TransactionID || !strings.HasPrefix(first.TransactionID, "bifrost-idem-") {
		t.Fatalf("replay must share the transaction ID: %q vs %q", first.TransactionID, replay.TransactionID)
	}
}

// TestNativeDecisionAndChatEventsHaveNoCostUSD pins that events without credit
// pricing keep their existing shape.
func TestNativeDecisionAndChatEventsHaveNoCostUSD(t *testing.T) {
	native, _ := exportDecision(t, audioContext("vk-1", "request"), decisionResponse(schemas.Typesafe, "jev-1.13.0", &schemas.BifrostLLMUsage{PromptTokens: 10}))
	chat, _ := exportDecision(t, audioContext("vk-1", "request"), tokenIdentityResponse())
	for _, e := range []Event[TokenUsage]{native, chat} {
		payload, _ := json.Marshal(e.Properties)
		if strings.Contains(string(payload), "cost_usd") {
			t.Fatalf("unexpected cost_usd: %s", payload)
		}
	}
	if native.Properties.InputTokens != 10 {
		t.Fatalf("native decision tokens changed: %+v", native.Properties)
	}
}
