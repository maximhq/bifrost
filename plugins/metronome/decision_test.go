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

// TestDecisionReplayWithoutUsageIsNotExported pins that a free idempotent
// replay, which the provider reports without usage, creates no billable event.
func TestDecisionReplayWithoutUsageIsNotExported(t *testing.T) {
	if event, ok := exportDecision(t, audioContext("vk-1", "request"), decisionResponse(schemas.Typesafe, "jev-1.13.0", nil)); ok {
		t.Fatalf("replay without usage was exported: %+v", event)
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
