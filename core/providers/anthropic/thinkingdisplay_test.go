package anthropic

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// Regression coverage for GH #5185: Bifrost forced thinking.display:"summarized"
// for adaptive-only models (Opus 4.7+, Sonnet 5+, Fable/Mythos) even on
// Bifrost's own native /anthropic/v1/messages surface, silently overriding
// Anthropic's real (opaque) default. Fixed by gating the implicit default on
// IsAnthropicNativeSurface, and by round-tripping an explicit display value
// verbatim via ExtraParams["thinking_display"] so it survives the
// inbound->outbound conversion even once the implicit default is gone.

func nativeSurfaceCtx() *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyIntegrationType, "anthropic")
	return ctx
}

func TestIsAnthropicNativeSurface(t *testing.T) {
	native := nativeSurfaceCtx()
	if !IsAnthropicNativeSurface(native) {
		t.Error("expected native surface ctx to report true")
	}

	other := schemas.NewBifrostContext(context.Background(), time.Time{})
	other.SetValue(schemas.BifrostContextKeyIntegrationType, "openai")
	if IsAnthropicNativeSurface(other) {
		t.Error("expected non-anthropic integration type to report false")
	}

	unset := schemas.NewBifrostContext(context.Background(), time.Time{})
	if IsAnthropicNativeSurface(unset) {
		t.Error("expected unset integration type to report false")
	}

	if IsAnthropicNativeSurface(nil) {
		t.Error("expected nil ctx to report false")
	}
}

// TestToAnthropicChatRequest_NativeSurface_NoImplicitDisplayDefault covers
// the Chat-Completions outbound converter: on the native surface, an unset
// display must stay unset for an adaptive-only model.
func TestToAnthropicChatRequest_NativeSurface_NoImplicitDisplayDefault(t *testing.T) {
	maxTok := 2048
	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-opus-4-7-20260401",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: new(string)}},
		},
		Params: &schemas.ChatParameters{
			MaxCompletionTokens: schemas.Ptr(8192),
			Reasoning:           &schemas.ChatReasoning{MaxTokens: &maxTok},
		},
	}

	result, err := ToAnthropicChatRequest(nativeSurfaceCtx(), bifrostReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Thinking == nil {
		t.Fatal("expected Thinking to be set")
	}
	if result.Thinking.Display != nil {
		t.Errorf("expected Display to stay unset on the native surface, got %v", *result.Thinking.Display)
	}
}

// TestNativeSurfaceExplicitSummarizedRoundTrips covers the Responses path,
// which is what /anthropic/v1/messages always uses internally regardless of
// destination provider: an explicit display value (both "summarized" and
// "omitted") must survive an inbound->outbound round trip on the native
// surface, and must never leak into ExtraParams on the outbound wire body.
func TestNativeSurfaceExplicitSummarizedRoundTrips(t *testing.T) {
	for _, display := range []string{"summarized", "omitted"} {
		t.Run(display, func(t *testing.T) {
			ctx := nativeSurfaceCtx()
			inbound := &AnthropicMessageRequest{
				Model:     "claude-sonnet-5",
				MaxTokens: 2048,
				Messages: []AnthropicMessage{
					{Role: "user", Content: AnthropicContent{ContentStr: schemas.Ptr("think")}},
				},
				Thinking: &AnthropicThinking{
					Type:    "adaptive",
					Display: schemas.Ptr(display),
				},
			}

			bifrostReq := inbound.ToBifrostResponsesRequest(ctx)
			if bifrostReq.Params.ExtraParams["thinking_display"] != display {
				t.Fatalf("expected inbound ExtraParams[thinking_display]=%q, got %v", display, bifrostReq.Params.ExtraParams["thinking_display"])
			}

			outbound, err := ToAnthropicResponsesRequest(ctx, bifrostReq)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if outbound.Thinking == nil || outbound.Thinking.Display == nil || *outbound.Thinking.Display != display {
				t.Fatalf("expected outbound Display=%q, got %v", display, outbound.Thinking)
			}
			if _, leaked := outbound.ExtraParams["thinking_display"]; leaked {
				t.Error("thinking_display must not leak into outbound ExtraParams")
			}
		})
	}
}

// TestNativeSurfaceUnsetDisplayStaysUnset is the direct repro of the reported
// bug on the Responses path: an unset display on the native surface must not
// pick up Bifrost's "summarized" default for an adaptive-only model.
func TestNativeSurfaceUnsetDisplayStaysUnset(t *testing.T) {
	ctx := nativeSurfaceCtx()
	inbound := &AnthropicMessageRequest{
		Model:     "claude-sonnet-5",
		MaxTokens: 2048,
		Messages: []AnthropicMessage{
			{Role: "user", Content: AnthropicContent{ContentStr: schemas.Ptr("think")}},
		},
		Thinking: &AnthropicThinking{Type: "adaptive"},
	}

	bifrostReq := inbound.ToBifrostResponsesRequest(ctx)
	if _, exists := bifrostReq.Params.ExtraParams["thinking_display"]; exists {
		t.Fatal("expected no thinking_display captured when caller left display unset")
	}

	outbound, err := ToAnthropicResponsesRequest(ctx, bifrostReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outbound.Thinking != nil && outbound.Thinking.Display != nil {
		t.Errorf("expected Display to stay unset on the native surface, got %v", *outbound.Thinking.Display)
	}
}

// TestNonNativeSurfaceStillDefaultsToSummarized locks in that every other
// surface (no IntegrationType set on ctx, e.g. direct Go SDK usage or a
// non-anthropic integration) is unaffected by this fix and keeps defaulting
// to "summarized" for adaptive-only models.
func TestNonNativeSurfaceStillDefaultsToSummarized(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	inbound := &AnthropicMessageRequest{
		Model:     "claude-sonnet-5",
		MaxTokens: 2048,
		Messages: []AnthropicMessage{
			{Role: "user", Content: AnthropicContent{ContentStr: schemas.Ptr("think")}},
		},
		Thinking: &AnthropicThinking{Type: "adaptive"},
	}

	bifrostReq := inbound.ToBifrostResponsesRequest(ctx)
	outbound, err := ToAnthropicResponsesRequest(ctx, bifrostReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outbound.Thinking == nil || outbound.Thinking.Display == nil || *outbound.Thinking.Display != "summarized" {
		t.Errorf("expected Display to default to 'summarized' off the native surface, got %v", outbound.Thinking)
	}
}
