package handlers

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

func TestVKUpstreamHealthFiltersReachablePairsAndFoldsStaticLimits(t *testing.T) {
	maxRequests := int64(100)
	snapshot := []schemas.UpstreamHealth{
		{Provider: schemas.Anthropic, Model: "claude-a", Status: schemas.UpstreamHealthCoolingDown},
		{Provider: schemas.Anthropic, Model: "claude-b", Status: schemas.UpstreamHealthDegraded, Available: true, EffectiveRemainingFraction: 1},
		{Provider: schemas.Anthropic, Model: "claude-blocked", Status: schemas.UpstreamHealthCoolingDown},
		{Provider: schemas.OpenAI, Model: "gpt-x", Status: schemas.UpstreamHealthCoolingDown},
	}
	configs := []configstoreTables.TableVirtualKeyProviderConfig{{
		Provider:          "anthropic",
		AllowedModels:     schemas.WhiteList{"*"},
		BlacklistedModels: schemas.BlackList{"claude-blocked"},
		Budgets:           []configstoreTables.TableBudget{{MaxLimit: 10, CurrentUsage: 2}},
		ModelBudgets: []configstoreTables.VKProviderModelBudget{{
			ModelName: "claude-b",
			RateLimit: &configstoreTables.TableRateLimit{RequestMaxLimit: &maxRequests, RequestCurrentUsage: 75},
		}},
	}}

	got := vkUpstreamHealth(snapshot, configs)
	if len(got) != 2 || got[0].Model != "claude-a" || got[1].Model != "claude-b" {
		t.Fatalf("expected only the reachable anthropic pairs, got %+v", got)
	}
	if got[0].EffectiveRemainingFraction != 0 {
		t.Fatalf("a cooling-down pair must report 0 remaining, got %v", got[0].EffectiveRemainingFraction)
	}
	if got[1].EffectiveRemainingFraction != 0.25 {
		t.Fatalf("expected the per-model request limit (25%% left) to bound the pair, got %v", got[1].EffectiveRemainingFraction)
	}
	if len(vkUpstreamHealth(snapshot, nil)) != 0 {
		t.Fatal("a key with no provider configs reaches nothing")
	}
}
