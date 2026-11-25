package handlers

import (
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/plugins/governance"
)

// UpstreamHealthSource returns the gateway's current per provider/model upstream health
// (bifrost.Bifrost.UpstreamHealthSnapshot).
type UpstreamHealthSource func(now time.Time) []schemas.UpstreamHealth

// ModelConfigMatcher reports whether a model config for (configModel, configProvider) governs a
// request for model on provider sent through the keys keyIDs allows, the way inference resolves a
// request's model limits: by the model's canonical and base names and the models those keys'
// aliases map it to.
type ModelConfigMatcher func(configModel string, configProvider *string, provider schemas.ModelProvider, model string, keyIDs schemas.WhiteList) bool

// SetUpstreamHealthSource wires the snapshot the virtual-key quota endpoint reports as
// upstream_health. Without it the field is reported empty.
func (h *GovernanceHandler) SetUpstreamHealthSource(source UpstreamHealthSource) {
	h.upstreamHealth = source
}

// SetModelConfigMatcher wires how upstream_health matches the key's per-model limits to a pair.
// Without it a limit applies only to the model's catalog-independent canonical spelling.
func (h *GovernanceHandler) SetModelConfigMatcher(match ModelConfigMatcher) {
	h.modelConfigMatcher = match
}

// canonicalModelConfigMatch is the matcher used without the governance store: a limit applies to
// the model it names, in any spelling that canonicalises to it.
func canonicalModelConfigMatch(configModel string, configProvider *string, _ schemas.ModelProvider, model string, _ schemas.WhiteList) bool {
	return governance.CanonicalModelConfigName(configModel, configProvider) == governance.CanonicalModelConfigName(model, configProvider)
}

// allProviderKeys is the key restriction on a provider granted by allow_all_providers: none.
var allProviderKeys = schemas.WhiteList{"*"}

// vkUpstreamHealth returns the upstream health of the provider/model pairs the virtual key
// can reach, with each pair's EffectiveRemainingFraction narrowed by the static budgets and rate
// limits the key has on that provider, on that model under it (with or without a provider
// config), and on that model across all providers (a model config with no provider, which
// governance enforces on every provider). A pair is reachable through a provider config that allows the model, or, when the key allows all
// providers, through any provider it names no config for: inference grants those with every
// model. Every pair in the snapshot was dispatched to, so its provider is configured. match
// decides which per-model limits govern a pair, through the keys the key's provider config allows
// (every key on a provider granted by allow_all_providers).
func vkUpstreamHealth(snapshot []schemas.UpstreamHealth, providerConfigs []configstoreTables.TableVirtualKeyProviderConfig, allowAllProviders bool, models []quotaModelUsage, match ModelConfigMatcher) []schemas.UpstreamHealth {
	out := make([]schemas.UpstreamHealth, 0, len(snapshot))
	for _, entry := range snapshot {
		var pc *configstoreTables.TableVirtualKeyProviderConfig
		named := false
		for i := range providerConfigs {
			if schemas.ModelProvider(providerConfigs[i].Provider) == entry.Provider {
				pc, named = &providerConfigs[i], true
				break
			}
		}
		if pc != nil && (!pc.AllowedModels.IsAllowed(entry.Model) || pc.BlacklistedModels.IsBlocked(entry.Model)) {
			continue
		}
		if !named && !allowAllProviders {
			continue
		}
		if entry.Available {
			keyIDs := allProviderKeys
			if pc != nil {
				keyIDs = pc.KeyIDs()
			}
			if pc != nil {
				entry.EffectiveRemainingFraction = min(entry.EffectiveRemainingFraction, staticRemainingFraction(pc.Budgets, pc.RateLimit))
				for j := range pc.ModelBudgets {
					if mb := &pc.ModelBudgets[j]; match(mb.ModelName, &pc.Provider, entry.Provider, entry.Model, keyIDs) {
						entry.EffectiveRemainingFraction = min(entry.EffectiveRemainingFraction, staticRemainingFraction(mb.Budgets, mb.RateLimit))
					}
				}
			}
			for j := range models {
				// A provider-scoped config can exist without a provider config (on a provider
				// allow_all_providers grants); one that has a provider config is also in
				// pc.ModelBudgets, and folding it twice changes nothing.
				if m := &models[j]; (m.Provider == nil || *m.Provider == string(entry.Provider)) && match(m.ModelName, m.Provider, entry.Provider, entry.Model, keyIDs) {
					entry.EffectiveRemainingFraction = min(entry.EffectiveRemainingFraction, staticRemainingFraction(m.Budgets, m.RateLimit))
				}
			}
		}
		out = append(out, entry)
	}
	return out
}

// staticRemainingFraction is the smallest remaining fraction across budgets and the token and
// request dimensions of a rate limit; 1 when none is capped.
func staticRemainingFraction(budgets []configstoreTables.TableBudget, rateLimit *configstoreTables.TableRateLimit) float64 {
	remaining := 1.0
	capped := func(used, limit float64) {
		if limit <= 0 {
			remaining = 0
			return
		}
		remaining = min(remaining, max(0, 1-used/limit))
	}
	for i := range budgets {
		capped(budgets[i].CurrentUsage, budgets[i].EffectiveMaxLimit())
	}
	if rateLimit != nil {
		if rateLimit.TokenMaxLimit != nil {
			capped(float64(rateLimit.TokenCurrentUsage), float64(*rateLimit.TokenMaxLimit))
		}
		if rateLimit.RequestMaxLimit != nil {
			capped(float64(rateLimit.RequestCurrentUsage), float64(*rateLimit.RequestMaxLimit))
		}
	}
	return remaining
}
