package bedrock

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestBedrockConvertersGateServiceTierByModelCapability(t *testing.T) {
	schemas.SetCapabilityResolver(func(provider schemas.ModelProvider, model string) *schemas.ModelCapabilities {
		if provider != schemas.Bedrock {
			return nil
		}
		switch model {
		case "priority-model":
			return &schemas.ModelCapabilities{ServiceTiers: []string{"priority", "flex"}}
		case "flex-only-model":
			return &schemas.ModelCapabilities{ServiceTiers: []string{"flex"}}
		default:
			return nil
		}
	})
	t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })

	tests := []struct {
		name           string
		model          string
		canonicalModel string
		tier           schemas.BifrostServiceTier
		wantTier       *BedrockServiceTierType
	}{
		{name: "supported priority", model: "priority-model", tier: schemas.BifrostServiceTierPriority, wantTier: schemas.Ptr(BedrockServiceTierTypePriority)},
		{name: "alias resolves supported priority", model: "friendly-alias", canonicalModel: "priority-model", tier: schemas.BifrostServiceTierPriority, wantTier: schemas.Ptr(BedrockServiceTierTypePriority)},
		{name: "supported flex", model: "priority-model", tier: schemas.BifrostServiceTierFlex, wantTier: schemas.Ptr(BedrockServiceTierTypeFlex)},
		{name: "tier omitted from explicit list", model: "flex-only-model", tier: schemas.BifrostServiceTierPriority},
		{name: "missing capabilities fail closed", model: "unknown-model", tier: schemas.BifrostServiceTierPriority},
		{name: "default uses omission", model: "priority-model", tier: schemas.BifrostServiceTierDefault},
		{name: "auto uses omission", model: "priority-model", tier: schemas.BifrostServiceTierAuto},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			if tt.canonicalModel != "" {
				ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{
					Key: tt.model,
					Config: &schemas.AliasConfig{
						ModelID:   tt.model,
						ModelName: &tt.canonicalModel,
					},
				})
			}
			chat, err := ToBedrockChatCompletionRequest(ctx, &schemas.BifrostChatRequest{
				Provider: schemas.Bedrock,
				Model:    tt.model,
				Input: []schemas.ChatMessage{{
					Role:    schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")},
				}},
				Params: &schemas.ChatParameters{ServiceTier: &tt.tier},
			})
			if err != nil {
				t.Fatalf("chat conversion failed: %v", err)
			}

			responses, err := ToBedrockResponsesRequest(ctx, &schemas.BifrostResponsesRequest{
				Provider: schemas.Bedrock,
				Model:    tt.model,
				Params:   &schemas.ResponsesParameters{ServiceTier: &tt.tier},
			})
			if err != nil {
				t.Fatalf("responses conversion failed: %v", err)
			}

			for name, request := range map[string]*BedrockConverseRequest{"chat": chat, "responses": responses} {
				if tt.wantTier == nil {
					if request.ServiceTier != nil {
						t.Errorf("%s service tier = %q, want omitted", name, request.ServiceTier.Type)
					}
					continue
				}
				if request.ServiceTier == nil || request.ServiceTier.Type != *tt.wantTier {
					t.Errorf("%s service tier = %v, want %q", name, request.ServiceTier, *tt.wantTier)
				}
			}
		})
	}
}
