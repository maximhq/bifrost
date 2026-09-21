package openai

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestServiceTierForModel(t *testing.T) {
	ultrafast := schemas.BifrostServiceTierUltrafast
	priority := schemas.BifrostServiceTierPriority
	defaultTier := schemas.BifrostServiceTierDefault

	schemas.SetCapabilityResolver(func(provider schemas.ModelProvider, model string) *schemas.ModelCapabilities {
		switch provider {
		case schemas.OpenAI:
			switch model {
			case "gpt-5.6-sol":
				return &schemas.ModelCapabilities{ServiceTiers: []string{"default", "flex", "priority", "ultrafast"}}
			case "standard-model":
				return &schemas.ModelCapabilities{ServiceTiers: []string{"default", "flex", "priority"}}
			}
		case schemas.BedrockMantle:
			if model == "xai.grok-4.6" {
				return &schemas.ModelCapabilities{ServiceTiers: []string{"priority", "flex"}}
			}
		}
		return nil
	})
	t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })

	tests := []struct {
		name     string
		provider schemas.ModelProvider
		model    string
		tier     *schemas.BifrostServiceTier
		want     *schemas.BifrostServiceTier
	}{
		{name: "explicit ultrafast support", provider: schemas.OpenAI, model: "gpt-5.6-sol", tier: &ultrafast, want: &ultrafast},
		{name: "unsupported ultrafast", provider: schemas.OpenAI, model: "standard-model", tier: &ultrafast, want: nil},
		{name: "missing capabilities preserve tier", provider: schemas.OpenAI, model: "unknown-model", tier: &ultrafast, want: &ultrafast},
		{name: "missing capabilities preserve ordinary tier", provider: schemas.OpenAI, model: "unknown-model", tier: &priority, want: &priority},
		{name: "explicit tier list rejects omitted ordinary tier", provider: schemas.OpenAI, model: "standard-model", tier: schemas.Ptr(schemas.BifrostServiceTierProvisioned), want: nil},
		{name: "bedrock mantle keeps explicitly supported tier", provider: schemas.BedrockMantle, model: "xai.grok-4.6", tier: &priority, want: &priority},
		{name: "bedrock mantle drops unknown non-standard tier", provider: schemas.BedrockMantle, model: "openai.gpt-5.6-luna", tier: &priority, want: nil},
		{name: "bedrock mantle omits default", provider: schemas.BedrockMantle, model: "xai.grok-4.6", tier: &defaultTier, want: nil},
		{name: "nil tier", provider: schemas.OpenAI, model: "standard-model", tier: nil, want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			caps := schemas.ResolveModelCaps(tt.provider, tt.model)
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

func TestOpenAIConvertersFilterServiceTierByModelCapability(t *testing.T) {
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
		if provider == schemas.BedrockMantle && model == "xai.grok-4.6" {
			return &schemas.ModelCapabilities{ServiceTiers: []string{"priority", "flex"}}
		}
		return nil
	})
	t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })

	tests := []struct {
		name     string
		provider schemas.ModelProvider
		model    string
		tier     schemas.BifrostServiceTier
		wantTier bool
	}{
		{name: "OpenAI supported model", provider: schemas.OpenAI, model: "gpt-5.6-sol", tier: ultrafast, wantTier: true},
		{name: "OpenAI model without tier metadata", provider: schemas.OpenAI, model: "other-model", tier: ultrafast, wantTier: true},
		{name: "compatible provider without tier metadata", provider: schemas.OpenRouter, model: "other-model", tier: ultrafast, wantTier: true},
		{name: "compatible provider explicitly unsupported tier", provider: schemas.OpenRouter, model: "compatible-standard-model", tier: ultrafast, wantTier: false},
		{name: "compatible provider supported model", provider: schemas.OpenRouter, model: "compatible-fast-model", tier: ultrafast, wantTier: true},
		{name: "bedrock mantle keeps explicitly supported tier", provider: schemas.BedrockMantle, model: "xai.grok-4.6", tier: schemas.BifrostServiceTierPriority, wantTier: true},
		{name: "bedrock mantle drops tier without metadata", provider: schemas.BedrockMantle, model: "openai.gpt-5.6-terra", tier: ultrafast, wantTier: false},
		{name: "bedrock mantle drops tier for bare name", provider: schemas.BedrockMantle, model: "gpt-5.6-terra", tier: ultrafast, wantTier: false},
		{name: "legacy in-bedrock mantle drops tier", provider: schemas.Bedrock, model: "openai.gpt-5.4", tier: ultrafast, wantTier: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tier := tt.tier
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			chat := ToOpenAIChatRequest(ctx, &schemas.BifrostChatRequest{
				Provider: tt.provider,
				Model:    tt.model,
				Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser}},
				Params:   &schemas.ChatParameters{ServiceTier: &tier},
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
				Params: &schemas.ResponsesParameters{ServiceTier: &tier},
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
