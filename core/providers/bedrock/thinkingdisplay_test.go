package bedrock

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// Regression coverage for the Bedrock side of GH #5185's cross-provider gap:
// an Anthropic-native request (/anthropic/v1/messages) that gets routed to a
// Bedrock-hosted Claude model (via model-catalog aliasing) must still (a) not
// pick up Bifrost's implicit "summarized" default when the caller left
// display unset, and (b) forward an explicit display value verbatim, instead
// of silently dropping it because Bedrock's Converse wire shape has no
// first-class field for it.

func bedrockAnthropicNativeCtx() *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyIntegrationType, "anthropic")
	return ctx
}

func TestToBedrockResponsesRequest_AnthropicNativeSurface_NoImplicitDisplayDefault(t *testing.T) {
	ctx := bedrockAnthropicNativeCtx()
	req := &schemas.BifrostResponsesRequest{
		Model: "anthropic.claude-sonnet-5",
		Input: []schemas.ResponsesMessage{glmUserMessage("think")},
		Params: &schemas.ResponsesParameters{
			MaxOutputTokens: schemas.Ptr(2048),
			Reasoning:       &schemas.ResponsesParametersReasoning{Effort: schemas.Ptr("high")},
		},
	}

	bedrockReq, err := ToBedrockResponsesRequest(ctx, req)
	if err != nil {
		t.Fatalf("ToBedrockResponsesRequest failed: %v", err)
	}
	thinkingRaw, ok := bedrockReq.AdditionalModelRequestFields.Get("thinking")
	if !ok {
		t.Fatal("expected thinking field to be set")
	}
	thinking, ok := thinkingRaw.(map[string]any)
	if !ok {
		t.Fatalf("expected thinking to be a map, got %T", thinkingRaw)
	}
	if display, exists := thinking["display"]; exists {
		t.Errorf("expected no implicit display default on the Anthropic-native surface, got %v", display)
	}
}

func TestToBedrockResponsesRequest_ExplicitThinkingDisplayRoundTrips(t *testing.T) {
	for _, display := range []string{"summarized", "omitted"} {
		t.Run(display, func(t *testing.T) {
			ctx := bedrockAnthropicNativeCtx()
			req := &schemas.BifrostResponsesRequest{
				Model: "anthropic.claude-sonnet-5",
				Input: []schemas.ResponsesMessage{glmUserMessage("think")},
				Params: &schemas.ResponsesParameters{
					MaxOutputTokens: schemas.Ptr(2048),
					Reasoning:       &schemas.ResponsesParametersReasoning{Effort: schemas.Ptr("high")},
					ExtraParams:     map[string]interface{}{"thinking_display": display},
				},
			}

			bedrockReq, err := ToBedrockResponsesRequest(ctx, req)
			if err != nil {
				t.Fatalf("ToBedrockResponsesRequest failed: %v", err)
			}
			thinkingRaw, ok := bedrockReq.AdditionalModelRequestFields.Get("thinking")
			if !ok {
				t.Fatal("expected thinking field to be set")
			}
			thinking, ok := thinkingRaw.(map[string]any)
			if !ok {
				t.Fatalf("expected thinking to be a map, got %T", thinkingRaw)
			}
			if thinking["display"] != display {
				t.Errorf("expected display=%q, got %v", display, thinking["display"])
			}
			if bedrockReq.ExtraParams != nil {
				if _, leaked := bedrockReq.ExtraParams["thinking_display"]; leaked {
					t.Error("thinking_display must not leak into outbound Bedrock ExtraParams")
				}
			}
		})
	}
}

// TestToBedrockResponsesRequest_DoesNotMutateSharedExtraParams guards against
// a fallback-safety regression: core/bifrost.go's prepareFallbackRequest
// shallow-copies BifrostResponsesRequest for each fallback attempt, keeping
// the same *Params pointer (and therefore the same ExtraParams map) across
// attempts. If ToBedrockResponsesRequest deleted "thinking_display" from that
// shared map in place, a subsequent fallback attempt (e.g. to a different
// Anthropic-family model) would lose the caller's explicit display choice.
func TestToBedrockResponsesRequest_DoesNotMutateSharedExtraParams(t *testing.T) {
	ctx := bedrockAnthropicNativeCtx()
	sharedExtraParams := map[string]interface{}{"thinking_display": "summarized"}
	req := &schemas.BifrostResponsesRequest{
		Model: "anthropic.claude-sonnet-5",
		Input: []schemas.ResponsesMessage{glmUserMessage("think")},
		Params: &schemas.ResponsesParameters{
			MaxOutputTokens: schemas.Ptr(2048),
			Reasoning:       &schemas.ResponsesParametersReasoning{Effort: schemas.Ptr("high")},
			ExtraParams:     sharedExtraParams,
		},
	}

	if _, err := ToBedrockResponsesRequest(ctx, req); err != nil {
		t.Fatalf("ToBedrockResponsesRequest failed: %v", err)
	}

	if _, exists := sharedExtraParams["thinking_display"]; !exists {
		t.Error("expected the caller's original ExtraParams map to be left untouched (simulating a shared map across fallback attempts)")
	}
}

// TestToBedrockResponsesRequest_NonNativeSurfaceStillDefaultsToSummarized
// locks in that traffic not originating from the Anthropic-native surface
// (e.g. OpenAI-compat callers routed to a Bedrock-hosted Claude model) is
// unaffected and keeps the "summarized" default for adaptive-only models.
func TestToBedrockResponsesRequest_NonNativeSurfaceStillDefaultsToSummarized(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := &schemas.BifrostResponsesRequest{
		Model: "anthropic.claude-sonnet-5",
		Input: []schemas.ResponsesMessage{glmUserMessage("think")},
		Params: &schemas.ResponsesParameters{
			MaxOutputTokens: schemas.Ptr(2048),
			Reasoning:       &schemas.ResponsesParametersReasoning{Effort: schemas.Ptr("high")},
		},
	}

	bedrockReq, err := ToBedrockResponsesRequest(ctx, req)
	if err != nil {
		t.Fatalf("ToBedrockResponsesRequest failed: %v", err)
	}
	thinkingRaw, ok := bedrockReq.AdditionalModelRequestFields.Get("thinking")
	if !ok {
		t.Fatal("expected thinking field to be set")
	}
	thinking, ok := thinkingRaw.(map[string]any)
	if !ok {
		t.Fatalf("expected thinking to be a map, got %T", thinkingRaw)
	}
	if thinking["display"] != "summarized" {
		t.Errorf("expected display to default to 'summarized' off the native surface, got %v", thinking["display"])
	}
}
