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
		AllowAllKeys:      true,
		BlacklistedModels: schemas.BlackList{"claude-blocked"},
		Budgets:           []configstoreTables.TableBudget{{MaxLimit: 10, CurrentUsage: 2}},
		ModelBudgets: []configstoreTables.VKProviderModelBudget{{
			ModelName: "claude-b",
			RateLimit: &configstoreTables.TableRateLimit{RequestMaxLimit: &maxRequests, RequestCurrentUsage: 75},
		}},
	}}

	got := vkUpstreamHealth(snapshot, configs, false, nil, canonicalModelConfigMatch, nil)
	if len(got) != 2 || got[0].Model != "claude-a" || got[1].Model != "claude-b" {
		t.Fatalf("expected only the reachable anthropic pairs, got %+v", got)
	}
	if got[0].EffectiveRemainingFraction != 0 {
		t.Fatalf("a cooling-down pair must report 0 remaining, got %v", got[0].EffectiveRemainingFraction)
	}
	if got[1].EffectiveRemainingFraction != 0.25 {
		t.Fatalf("expected the per-model request limit (25%% left) to bound the pair, got %v", got[1].EffectiveRemainingFraction)
	}
	if len(vkUpstreamHealth(snapshot, nil, false, nil, canonicalModelConfigMatch, nil)) != 0 {
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
		AllowAllKeys:      true,
		BlacklistedModels: schemas.BlackList{"claude-blocked"},
	}}
	got := vkUpstreamHealth(snapshot, configs, true, nil, canonicalModelConfigMatch, nil)
	if len(got) != 2 || got[0].Model != "gpt-x" || got[1].Model != "claude-a" {
		t.Fatalf("expected the unnamed provider and the named provider's allowed model, got %+v", got)
	}
	if got := vkUpstreamHealth(snapshot, nil, true, nil, canonicalModelConfigMatch, nil); len(got) != 3 {
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
		AllowAllKeys:  true,
		ModelBudgets: []configstoreTables.VKProviderModelBudget{{
			ModelName: "gpt-4o",
			Budgets:   []configstoreTables.TableBudget{{MaxLimit: 10, CurrentUsage: 10}},
		}},
	}}
	models := []quotaModelUsage{{ModelName: "gpt-4o", Budgets: []configstoreTables.TableBudget{{MaxLimit: 10, CurrentUsage: 5}}}}
	match := func(configModel string, _ *string, _ schemas.ModelProvider, model string, _ schemas.WhiteList) bool {
		return configModel == "gpt-4o" && model == "gpt-4o-2024-08-06"
	}
	got := vkUpstreamHealth(snapshot, configs, false, models, match, nil)
	if len(got) != 2 || got[0].EffectiveRemainingFraction != 0 || got[1].EffectiveRemainingFraction != 1 {
		t.Fatalf("expected the exhausted gpt-4o budget to bound only its dated model, got %+v", got)
	}
}

// The matcher resolves a pair through the keys the virtual key's provider config allows, so an
// alias another key maps to an exhausted model does not take capacity from this key's route.
func TestVKUpstreamHealthMatchesThroughTheConfigsKeys(t *testing.T) {
	snapshot := []schemas.UpstreamHealth{
		{Provider: schemas.Bedrock, Model: "claude-sonnet", Status: schemas.UpstreamHealthDegraded, Available: true, EffectiveRemainingFraction: 1},
		{Provider: schemas.OpenAI, Model: "gpt-x", Status: schemas.UpstreamHealthDegraded, Available: true, EffectiveRemainingFraction: 1},
	}
	configs := []configstoreTables.TableVirtualKeyProviderConfig{{
		Provider:      "bedrock",
		AllowedModels: schemas.WhiteList{"*"},
		Keys:          []configstoreTables.TableKey{{KeyID: "key-sonnet"}},
	}}
	models := []quotaModelUsage{{ModelName: "opus", Budgets: []configstoreTables.TableBudget{{MaxLimit: 10, CurrentUsage: 10}}}}
	var seen []schemas.WhiteList
	match := func(configModel string, _ *string, provider schemas.ModelProvider, _ string, keyIDs schemas.WhiteList) bool {
		seen = append(seen, keyIDs)
		// key-opus maps claude-sonnet to opus; key-sonnet does not.
		return configModel == "opus" && provider == schemas.Bedrock && keyIDs.IsAllowed("key-opus")
	}
	got := vkUpstreamHealth(snapshot, configs, true, models, match, nil)
	if len(got) != 2 || got[0].EffectiveRemainingFraction != 1 {
		t.Fatalf("expected the key-sonnet route to keep its capacity, got %+v", got)
	}
	if len(seen) != 2 || len(seen[0]) != 1 || seen[0][0] != "key-sonnet" || !seen[1].IsUnrestricted() {
		t.Fatalf("expected the config's keys for bedrock and every key for the allow-all provider, got %v", seen)
	}
}

// A provider-scoped model config needs no provider config: on a provider allow_all_providers
// grants, an exhausted openai/gpt-4o limit still bounds the pair, and it leaves other providers'
// pairs alone.
func TestVKUpstreamHealthFoldsProviderScopedLimitWithoutProviderConfig(t *testing.T) {
	snapshot := []schemas.UpstreamHealth{
		{Provider: schemas.OpenAI, Model: "gpt-4o", Status: schemas.UpstreamHealthDegraded, Available: true, EffectiveRemainingFraction: 1},
		{Provider: schemas.Azure, Model: "gpt-4o", Status: schemas.UpstreamHealthDegraded, Available: true, EffectiveRemainingFraction: 1},
	}
	models := []quotaModelUsage{{ModelName: "gpt-4o", Provider: schemas.Ptr("openai"), Budgets: []configstoreTables.TableBudget{{MaxLimit: 10, CurrentUsage: 10}}}}
	got := vkUpstreamHealth(snapshot, nil, true, models, canonicalModelConfigMatch, nil)
	if len(got) != 2 || got[0].EffectiveRemainingFraction != 0 || got[1].EffectiveRemainingFraction != 1 {
		t.Fatalf("expected the exhausted openai/gpt-4o limit to bound only the openai pair, got %+v", got)
	}
}

// A provider config that allows no keys (deny-by-default) serves nothing, so neither an available
// nor a cooling-down pair on its provider is reachable, even though its models allow them.
func TestVKUpstreamHealthSkipsDenyAllProviderConfig(t *testing.T) {
	snapshot := []schemas.UpstreamHealth{
		{Provider: schemas.OpenAI, Model: "gpt-a", Status: schemas.UpstreamHealthDegraded, Available: true, EffectiveRemainingFraction: 1},
		{Provider: schemas.OpenAI, Model: "gpt-b", Status: schemas.UpstreamHealthCoolingDown},
		{Provider: schemas.Anthropic, Model: "claude-a", Status: schemas.UpstreamHealthCoolingDown},
	}
	configs := []configstoreTables.TableVirtualKeyProviderConfig{
		{Provider: "openai", AllowedModels: schemas.WhiteList{"*"}},
		{Provider: "anthropic", AllowedModels: schemas.WhiteList{"*"}, Keys: []configstoreTables.TableKey{{KeyID: "key-1"}}},
	}
	got := vkUpstreamHealth(snapshot, configs, true, nil, canonicalModelConfigMatch, nil)
	if len(got) != 1 || got[0].Provider != schemas.Anthropic {
		t.Fatalf("expected only the provider whose config allows a key, got %+v", got)
	}
}

// A keyless provider skips key selection, so a config that allows no keys still reaches its pairs;
// the same config on a provider that needs keys reaches none.
func TestVKUpstreamHealthKeylessProviderIgnoresKeyRestriction(t *testing.T) {
	const keylessProvider = schemas.ModelProvider("my-local")
	snapshot := []schemas.UpstreamHealth{
		{Provider: keylessProvider, Model: "local-a", Status: schemas.UpstreamHealthCoolingDown},
		{Provider: schemas.OpenAI, Model: "gpt-a", Status: schemas.UpstreamHealthCoolingDown},
	}
	configs := []configstoreTables.TableVirtualKeyProviderConfig{
		{Provider: string(keylessProvider), AllowedModels: schemas.WhiteList{"*"}},
		{Provider: "openai", AllowedModels: schemas.WhiteList{"*"}},
	}
	keyless := func(provider schemas.ModelProvider) bool { return provider == keylessProvider }
	got := vkUpstreamHealth(snapshot, configs, false, nil, canonicalModelConfigMatch, keyless)
	if len(got) != 1 || got[0].Provider != keylessProvider {
		t.Fatalf("expected only the keyless provider's pair, got %+v", got)
	}
}
