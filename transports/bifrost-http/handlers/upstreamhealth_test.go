package handlers

import (
	"context"
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

	got := vkUpstreamHealth(context.Background(), snapshot, configs, false, nil, canonicalModelConfigMatch)
	if len(got) != 2 || got[0].Model != "claude-a" || got[1].Model != "claude-b" {
		t.Fatalf("expected only the reachable anthropic pairs, got %+v", got)
	}
	if got[0].EffectiveRemainingFraction != 0 {
		t.Fatalf("a cooling-down pair must report 0 remaining, got %v", got[0].EffectiveRemainingFraction)
	}
	if got[1].EffectiveRemainingFraction != 0.25 {
		t.Fatalf("expected the per-model request limit (25%% left) to bound the pair, got %v", got[1].EffectiveRemainingFraction)
	}
	if len(vkUpstreamHealth(context.Background(), snapshot, nil, false, nil, canonicalModelConfigMatch)) != 0 {
		t.Fatal("a key with no provider configs reaches nothing")
	}
}

// A key that allows all providers reaches every configured provider it names no config for, with
// every model, as inference grants them. A provider it does name keeps that config's model rules.
func TestVKUpstreamHealthAllowAllProvidersReachesUnnamedProviders(t *testing.T) {
	snapshot := []schemas.UpstreamHealth{
		{Provider: schemas.OpenAI, Model: "gpt-x", Status: schemas.UpstreamHealthCoolingDown},
		{Provider: schemas.Anthropic, Model: "claude-blocked", Status: schemas.UpstreamHealthCoolingDown},
		{Provider: schemas.Anthropic, Model: "claude-a", Status: schemas.UpstreamHealthCoolingDown},
	}
	configs := []configstoreTables.TableVirtualKeyProviderConfig{{
		Provider:          "anthropic",
		AllowedModels:     schemas.WhiteList{"*"},
		BlacklistedModels: schemas.BlackList{"claude-blocked"},
	}}
	got := vkUpstreamHealth(context.Background(), snapshot, configs, true, nil, canonicalModelConfigMatch)
	if len(got) != 2 || got[0].Model != "gpt-x" || got[1].Model != "claude-a" {
		t.Fatalf("expected the unnamed provider and the named provider's allowed model, got %+v", got)
	}
	if got := vkUpstreamHealth(context.Background(), snapshot, nil, true, nil, canonicalModelConfigMatch); len(got) != 3 {
		t.Fatalf("a key allowing all providers with no configs reaches every pair, got %+v", got)
	}
}

// A per-model limit reaches every pair the matcher says it governs, not only the model it is
// spelled as: inference applies a gpt-4o limit to gpt-4o-2024-08-06 and to a key alias for it.
func TestVKUpstreamHealthFoldsLimitsTheMatcherGoverns(t *testing.T) {
	snapshot := []schemas.UpstreamHealth{
		{Provider: schemas.OpenAI, Model: "gpt-4o-2024-08-06", Status: schemas.UpstreamHealthDegraded, Available: true, EffectiveRemainingFraction: 1},
		{Provider: schemas.OpenAI, Model: "gpt-4o-mini", Status: schemas.UpstreamHealthDegraded, Available: true, EffectiveRemainingFraction: 1},
	}
	configs := []configstoreTables.TableVirtualKeyProviderConfig{{
		Provider:      "openai",
		AllowedModels: schemas.WhiteList{"*"},
		ModelBudgets: []configstoreTables.VKProviderModelBudget{{
			ModelName: "gpt-4o",
			Budgets:   []configstoreTables.TableBudget{{MaxLimit: 10, CurrentUsage: 10}},
		}},
	}}
	models := []quotaModelUsage{{ModelName: "gpt-4o", Budgets: []configstoreTables.TableBudget{{MaxLimit: 10, CurrentUsage: 5}}}}
	match := func(_ context.Context, configModel string, _ *string, _ schemas.ModelProvider, model string) bool {
		return configModel == "gpt-4o" && model == "gpt-4o-2024-08-06"
	}
	got := vkUpstreamHealth(context.Background(), snapshot, configs, false, models, match)
	if len(got) != 2 || got[0].EffectiveRemainingFraction != 0 || got[1].EffectiveRemainingFraction != 1 {
		t.Fatalf("expected the exhausted gpt-4o budget to bound only its dated model, got %+v", got)
	}
}
