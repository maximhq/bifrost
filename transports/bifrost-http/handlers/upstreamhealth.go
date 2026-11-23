package handlers

import (
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

// UpstreamHealthSource returns the gateway's current per provider/model upstream health
// (bifrost.Bifrost.UpstreamHealthSnapshot).
type UpstreamHealthSource func(now time.Time) []schemas.UpstreamHealth

// SetUpstreamHealthSource wires the snapshot the virtual-key quota endpoint reports as
// upstream_health. Without it the field is reported empty.
func (h *GovernanceHandler) SetUpstreamHealthSource(source UpstreamHealthSource) {
	h.upstreamHealth = source
}

// vkUpstreamHealth returns the upstream health of the provider/model pairs the virtual key
// can reach (its provider configs, their allowed and blacklisted models), with each pair's
// EffectiveRemainingFraction narrowed by the static budgets and rate limits the key has on
// that provider and on that model under it.
func vkUpstreamHealth(snapshot []schemas.UpstreamHealth, providerConfigs []configstoreTables.TableVirtualKeyProviderConfig) []schemas.UpstreamHealth {
	out := make([]schemas.UpstreamHealth, 0, len(snapshot))
	for _, entry := range snapshot {
		for i := range providerConfigs {
			pc := &providerConfigs[i]
			if schemas.ModelProvider(pc.Provider) != entry.Provider ||
				!pc.AllowedModels.IsAllowed(entry.Model) || pc.BlacklistedModels.IsBlocked(entry.Model) {
				continue
			}
			if entry.Available {
				entry.EffectiveRemainingFraction = min(entry.EffectiveRemainingFraction, staticRemainingFraction(pc.Budgets, pc.RateLimit))
				for j := range pc.ModelBudgets {
					if mb := &pc.ModelBudgets[j]; mb.ModelName == entry.Model {
						entry.EffectiveRemainingFraction = min(entry.EffectiveRemainingFraction, staticRemainingFraction(mb.Budgets, mb.RateLimit))
					}
				}
			}
			out = append(out, entry)
			break
		}
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
