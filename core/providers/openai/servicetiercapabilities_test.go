package openai

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestServiceTierForModel(t *testing.T) {
	ultrafast := schemas.BifrostServiceTierUltrafast
	priority := schemas.BifrostServiceTierPriority

	schemas.SetCapabilityResolver(func(provider schemas.ModelProvider, model string) *schemas.ModelCapabilities {
		if provider != schemas.OpenAI {
			return nil
		}
		switch model {
		case "gpt-5.6-sol":
			return &schemas.ModelCapabilities{ServiceTiers: []string{"default", "flex", "priority", "ultrafast"}}
		case "standard-model":
			return &schemas.ModelCapabilities{ServiceTiers: []string{"default", "flex", "priority"}}
		default:
			return nil
		}
	})
	t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })

	tests := []struct {
		name  string
		model string
		tier  *schemas.BifrostServiceTier
		want  *schemas.BifrostServiceTier
	}{
		{name: "explicit ultrafast support", model: "gpt-5.6-sol", tier: &ultrafast, want: &ultrafast},
		{name: "unsupported ultrafast", model: "standard-model", tier: &ultrafast, want: nil},
		{name: "missing capabilities preserve tier", model: "unknown-model", tier: &ultrafast, want: &ultrafast},
		{name: "missing capabilities preserve ordinary tier", model: "unknown-model", tier: &priority, want: &priority},
		{name: "explicit tier list rejects omitted ordinary tier", model: "standard-model", tier: schemas.Ptr(schemas.BifrostServiceTierProvisioned), want: nil},
		{name: "nil tier", model: "standard-model", tier: nil, want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			caps := schemas.ResolveModelCaps(schemas.OpenAI, tt.model)
			got := serviceTierForModel(caps, tt.tier)
			if tt.want == nil {
				if got != nil {
					t.Fatalf("service tier = %q, want nil", *got)
				}
				return
			}
			if got == nil || *got != *tt.want {
				t.Fatalf("service tier = %v, want %q", got, *tt.want)
			}
		})
	}
}

func TestOpenAIConvertersFilterUltrafastByModelCapability(t *testing.T) {
	ultrafast := schemas.BifrostServiceTierUltrafast
	schemas.SetCapabilityResolver(func(provider schemas.ModelProvider, model string) *schemas.ModelCapabilities {
		if provider == schemas.OpenAI && model == "gpt-5.6-sol" {
			return &schemas.ModelCapabilities{ServiceTiers: []string{"ultrafast"}}
		}
		if provider == schemas.OpenRouter && model == "compatible-fast-model" {
			return &schemas.ModelCapabilities{ServiceTiers: []string{"ultrafast"}}
		}
		if provider == schemas.OpenRouter && model == "compatible-standard-model" {
			return &schemas.ModelCapabilities{ServiceTiers: []string{"default", "priority"}}
		}
		return nil
	})
	t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })

	tests := []struct {
		name     string
		provider schemas.ModelProvider
		model    string
		wantTier bool
	}{
		{name: "OpenAI supported model", provider: schemas.OpenAI, model: "gpt-5.6-sol", wantTier: true},
		{name: "OpenAI model without tier metadata", provider: schemas.OpenAI, model: "other-model", wantTier: true},
		{name: "compatible provider without tier metadata", provider: schemas.OpenRouter, model: "other-model", wantTier: true},
		{name: "compatible provider explicitly unsupported tier", provider: schemas.OpenRouter, model: "compatible-standard-model", wantTier: false},
		{name: "compatible provider supported model", provider: schemas.OpenRouter, model: "compatible-fast-model", wantTier: true},
		// Mantle's OpenAI-compatible surface rejects service_tier outright, and its
		// verbatim model ids carry no datasheet row — so the "no metadata means keep
		// the tier" fallback above must not apply to it.
		{name: "bedrock mantle drops tier without metadata", provider: schemas.BedrockMantle, model: "openai.gpt-5.6-terra", wantTier: false},
		{name: "bedrock mantle drops tier for bare name", provider: schemas.BedrockMantle, model: "gpt-5.6-terra", wantTier: false},
		// Provider bedrock reaches these converters only via the deprecated
		// in-provider Mantle routing, so it means the same endpoint.
		{name: "legacy in-bedrock mantle drops tier", provider: schemas.Bedrock, model: "openai.gpt-5.4", wantTier: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			chat := ToOpenAIChatRequest(ctx, &schemas.BifrostChatRequest{
				Provider: tt.provider,
				Model:    tt.model,
				Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser}},
				Params:   &schemas.ChatParameters{ServiceTier: &ultrafast},
			})
			if chat == nil {
				t.Fatal("chat conversion returned nil")
			}
			if (chat.ServiceTier != nil) != tt.wantTier {
				t.Fatalf("chat service tier = %v, want present=%v", chat.ServiceTier, tt.wantTier)
			}

			responses := ToOpenAIResponsesRequest(ctx, &schemas.BifrostResponsesRequest{
				Provider: tt.provider,
				Model:    tt.model,
				Input: []schemas.ResponsesMessage{{
					Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
					Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hello")},
				}},
				Params: &schemas.ResponsesParameters{ServiceTier: &ultrafast},
			})
			if responses == nil {
				t.Fatal("responses conversion returned nil")
			}
			if (responses.ServiceTier != nil) != tt.wantTier {
				t.Fatalf("responses service tier = %v, want present=%v", responses.ServiceTier, tt.wantTier)
			}
		})
	}
}
