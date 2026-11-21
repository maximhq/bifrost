package requestguard

import (
	"sort"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

// RecommendModels finds catalogued alternatives that can satisfy the same
// request features. Candidates are evaluated and priced synchronously; the
// request pointer is never accepted or retained here.
func RecommendModels(
	catalog Catalog,
	capCfg CapabilityGuardConfig,
	budgetCfg BudgetGuardConfig,
	recCfg RecommenderConfig,
	features RequestFeatures,
	estimate CostEstimate,
	usage *schemas.BifrostLLMUsage,
	scopes *modelcatalog.PricingLookupScopes,
	baselineCost float64,
	forCapabilityFailure bool,
) []ModelRecommendation {
	if isNilCatalog(catalog) || !recCfg.Enabled || !features.Supported || recCfg.MaxRecommendations <= 0 {
		return nil
	}
	if usage == nil {
		usage = &schemas.BifrostLLMUsage{
			Estimation:       &schemas.TokenUsageEstimation{Prompt: true, Completion: true, Tokenizer: "requestguard-estimate"},
			PromptTokens:     estimate.InputTokens,
			CompletionTokens: estimate.OutputTokens,
			TotalTokens:      estimate.TotalTokens,
		}
	}
	if baselineCost <= 0 && estimate.PricingAvailable {
		baselineCost = estimate.EstimatedCostUSD
	}

	providers := candidateProviders(catalog, features, recCfg.SameProviderOnly)
	allowedProviders := stringSet(recCfg.AllowedProviders)
	allowedModels := stringSet(recCfg.AllowedModels)
	excludedModels := stringSet(recCfg.ExcludedModels)
	seen := make(map[string]struct{})
	var candidates []rankedRecommendation

	for _, provider := range providers {
		if len(allowedProviders) > 0 && !setHas(allowedProviders, string(provider)) {
			continue
		}
		models := catalog.GetModelsForProvider(provider)
		for _, model := range models {
			model = strings.TrimSpace(model)
			if model == "" || model == features.Model {
				continue
			}
			key := string(provider) + "\x00" + model
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			if (len(allowedModels) > 0 && !modelAllowed(allowedModels, provider, model)) || modelAllowed(excludedModels, provider, model) {
				continue
			}

			modelInfo := catalog.GetModelInfo(provider, model)
			entry := catalog.GetPricingEntryForModel(model, provider)
			if (modelInfo != nil && modelInfo.IsDeprecated) || (entry != nil && entry.IsDeprecated) {
				continue
			}

			candidateFeatures := features
			candidateFeatures.Provider = provider
			candidateFeatures.Model = model
			capability := EvaluateCapabilities(catalog, capCfg, candidateFeatures)
			if !capability.Pass || capability.Blocked || capability.NeedsDowngrade || len(capability.Issues) > 0 {
				continue
			}
			if issue := EvaluateContextLimit(catalog, capCfg, candidateFeatures, estimate); issue != nil {
				continue
			}

			cost, pricingAvailable := PriceEstimate(catalog, usage, provider, model, features.RequestType, scopes)
			if !pricingAvailable {
				continue
			}
			savings := baselineCost - cost
			savingsPct := 0.0
			if baselineCost > 0 {
				savingsPct = savings / baselineCost * 100
			}
			if !forCapabilityFailure && (cost >= baselineCost || savingsPct < recCfg.MinSavingsPct) {
				continue
			}

			reason := "catalogued alternative supports the requested capabilities"
			if baselineCost > 0 && savings > 0 {
				reason = "estimated lower cost while preserving requested capabilities"
			}
			recommendation := ModelRecommendation{
				Provider:            provider,
				Model:               model,
				EstimatedCostUSD:    cost,
				EstimatedSavingsUSD: savings,
				SavingsPct:          savingsPct,
				Reason:              reason,
			}
			candidates = append(candidates, rankedRecommendation{
				recommendation: recommendation,
				withinBudget:   withinBudget(budgetCfg, estimate, cost),
			})
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if left.withinBudget != right.withinBudget {
			return left.withinBudget
		}
		if left.recommendation.EstimatedCostUSD != right.recommendation.EstimatedCostUSD {
			return left.recommendation.EstimatedCostUSD < right.recommendation.EstimatedCostUSD
		}
		if left.recommendation.Provider != right.recommendation.Provider {
			return left.recommendation.Provider < right.recommendation.Provider
		}
		return left.recommendation.Model < right.recommendation.Model
	})
	if len(candidates) > recCfg.MaxRecommendations {
		candidates = candidates[:recCfg.MaxRecommendations]
	}
	result := make([]ModelRecommendation, len(candidates))
	for i := range candidates {
		result[i] = candidates[i].recommendation
	}
	return result
}

type rankedRecommendation struct {
	recommendation ModelRecommendation
	withinBudget   bool
}

func candidateProviders(catalog Catalog, features RequestFeatures, sameProviderOnly bool) []schemas.ModelProvider {
	if sameProviderOnly {
		if features.Provider == "" {
			return nil
		}
		return []schemas.ModelProvider{features.Provider}
	}
	providers := []schemas.ModelProvider{features.Provider}
	providers = append(providers, catalog.GetProvidersForModel(features.Model)...)
	seen := make(map[schemas.ModelProvider]struct{}, len(providers))
	result := make([]schemas.ModelProvider, 0, len(providers))
	for _, provider := range providers {
		if provider == "" {
			continue
		}
		if _, ok := seen[provider]; ok {
			continue
		}
		seen[provider] = struct{}{}
		result = append(result, provider)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func stringSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" {
			set[value] = struct{}{}
		}
	}
	return set
}

func setHas(set map[string]struct{}, value string) bool {
	_, ok := set[strings.ToLower(strings.TrimSpace(value))]
	return ok
}

func modelAllowed(set map[string]struct{}, provider schemas.ModelProvider, model string) bool {
	if len(set) == 0 {
		return false
	}
	return setHas(set, model) || setHas(set, string(provider)+"/"+model)
}

func withinBudget(cfg BudgetGuardConfig, estimate CostEstimate, cost float64) bool {
	if cfg.MaxInputTokens > 0 && estimate.InputTokens > cfg.MaxInputTokens {
		return false
	}
	if cfg.MaxTotalTokens > 0 && estimate.TotalTokens > cfg.MaxTotalTokens {
		return false
	}
	if cfg.MaxEstimatedCostUSD > 0 && cost > cfg.MaxEstimatedCostUSD {
		return false
	}
	return true
}
