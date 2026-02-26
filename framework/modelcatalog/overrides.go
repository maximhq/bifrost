package modelcatalog

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

type compiledProviderPricingOverride struct {
	override         schemas.ProviderPricingOverride
	regex            *regexp.Regexp
	requestModes     map[string]struct{}
	hasRequestFilter bool
	literalChars     int
	order            int
}

func (mc *ModelCatalog) SetProviderPricingOverrides(provider schemas.ModelProvider, overrides []schemas.ProviderPricingOverride) error {
	compiled := make([]compiledProviderPricingOverride, 0, len(overrides))
	for i := range overrides {
		item, err := compileProviderPricingOverride(i, overrides[i])
		if err != nil {
			return fmt.Errorf("invalid pricing override for provider %s at index %d: %w", provider, i, err)
		}
		compiled = append(compiled, item)
	}

	mc.overridesMu.Lock()
	defer mc.overridesMu.Unlock()
	if len(compiled) == 0 {
		delete(mc.compiledOverrides, provider)
		return nil
	}
	mc.compiledOverrides[provider] = compiled
	return nil
}

func (mc *ModelCatalog) DeleteProviderPricingOverrides(provider schemas.ModelProvider) {
	mc.overridesMu.Lock()
	defer mc.overridesMu.Unlock()
	delete(mc.compiledOverrides, provider)
}

func (mc *ModelCatalog) applyPricingOverrides(provider schemas.ModelProvider, model string, requestType schemas.RequestType, pricing configstoreTables.TableModelPricing) configstoreTables.TableModelPricing {
	mc.overridesMu.RLock()
	overrides := mc.compiledOverrides[provider]
	mc.overridesMu.RUnlock()
	if len(overrides) == 0 {
		return pricing
	}

	modelCandidates := []string{model}
	mode := normalizeRequestType(requestType)
	best := selectBestOverride(overrides, modelCandidates, mode)
	if best == nil {
		return pricing
	}

	return patchPricing(pricing, best.override)
}

func compileProviderPricingOverride(order int, override schemas.ProviderPricingOverride) (compiledProviderPricingOverride, error) {
	pattern := strings.TrimSpace(override.ModelPattern)
	if pattern == "" {
		return compiledProviderPricingOverride{}, fmt.Errorf("model_pattern cannot be empty")
	}

	result := compiledProviderPricingOverride{
		override:     override,
		requestModes: make(map[string]struct{}),
		order:        order,
	}
	result.override.ModelPattern = pattern

	switch override.MatchType {
	case schemas.PricingOverrideMatchExact:
		result.literalChars = len(pattern)
	case schemas.PricingOverrideMatchWildcard:
		if !strings.Contains(pattern, "*") {
			return compiledProviderPricingOverride{}, fmt.Errorf("wildcard model_pattern must contain '*'")
		}
		result.literalChars = len(strings.ReplaceAll(pattern, "*", ""))
	case schemas.PricingOverrideMatchRegex:
		re, err := regexp.Compile(pattern)
		if err != nil {
			return compiledProviderPricingOverride{}, fmt.Errorf("invalid regex model_pattern: %w", err)
		}
		result.regex = re
		result.literalChars = len(pattern)
	default:
		return compiledProviderPricingOverride{}, fmt.Errorf("unsupported match_type: %s", override.MatchType)
	}

	if len(override.RequestTypes) > 0 {
		result.hasRequestFilter = true
		for _, requestType := range override.RequestTypes {
			mode := normalizeRequestType(requestType)
			if mode == "unknown" {
				return compiledProviderPricingOverride{}, fmt.Errorf("unsupported request_type: %s", requestType)
			}
			result.requestModes[mode] = struct{}{}
		}
	}

	return result, nil
}

func selectBestOverride(overrides []compiledProviderPricingOverride, modelCandidates []string, mode string) *compiledProviderPricingOverride {
	var best *compiledProviderPricingOverride
	for i := range overrides {
		candidate := &overrides[i]
		if candidate.hasRequestFilter {
			if _, ok := candidate.requestModes[mode]; !ok {
				continue
			}
		}
		if !matchesAnyModel(candidate, modelCandidates) {
			continue
		}
		if isBetterOverride(candidate, best) {
			best = candidate
		}
	}
	return best
}

func matchesAnyModel(override *compiledProviderPricingOverride, modelCandidates []string) bool {
	for _, model := range modelCandidates {
		if matchesModel(override, model) {
			return true
		}
	}
	return false
}

func matchesModel(override *compiledProviderPricingOverride, model string) bool {
	switch override.override.MatchType {
	case schemas.PricingOverrideMatchExact:
		return model == override.override.ModelPattern
	case schemas.PricingOverrideMatchWildcard:
		return wildcardMatch(override.override.ModelPattern, model)
	case schemas.PricingOverrideMatchRegex:
		return override.regex != nil && override.regex.MatchString(model)
	default:
		return false
	}
}

func overridePriority(matchType schemas.PricingOverrideMatchType) int {
	switch matchType {
	case schemas.PricingOverrideMatchExact:
		return 0
	case schemas.PricingOverrideMatchWildcard:
		return 1
	case schemas.PricingOverrideMatchRegex:
		return 2
	default:
		return 3
	}
}

func isBetterOverride(candidate, best *compiledProviderPricingOverride) bool {
	if best == nil {
		return true
	}

	candidatePriority := overridePriority(candidate.override.MatchType)
	bestPriority := overridePriority(best.override.MatchType)
	if candidatePriority != bestPriority {
		return candidatePriority < bestPriority
	}

	if candidate.hasRequestFilter != best.hasRequestFilter {
		return candidate.hasRequestFilter
	}

	if candidate.literalChars != best.literalChars {
		return candidate.literalChars > best.literalChars
	}

	return candidate.order < best.order
}

func wildcardMatch(pattern, model string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return model == pattern
	}

	remaining := model
	if parts[0] != "" {
		if !strings.HasPrefix(remaining, parts[0]) {
			return false
		}
		remaining = remaining[len(parts[0]):]
	}

	for i := 1; i < len(parts)-1; i++ {
		part := parts[i]
		if part == "" {
			continue
		}
		index := strings.Index(remaining, part)
		if index < 0 {
			return false
		}
		remaining = remaining[index+len(part):]
	}

	last := parts[len(parts)-1]
	if last == "" {
		return true
	}
	return strings.HasSuffix(remaining, last)
}

func patchPricing(pricing configstoreTables.TableModelPricing, override schemas.ProviderPricingOverride) configstoreTables.TableModelPricing {
	patched := pricing

	if override.InputCostPerToken != nil {
		patched.InputCostPerToken = *override.InputCostPerToken
	}
	if override.OutputCostPerToken != nil {
		patched.OutputCostPerToken = *override.OutputCostPerToken
	}
	if override.InputCostPerVideoPerSecond != nil {
		patched.InputCostPerVideoPerSecond = override.InputCostPerVideoPerSecond
	}
	if override.InputCostPerAudioPerSecond != nil {
		patched.InputCostPerAudioPerSecond = override.InputCostPerAudioPerSecond
	}
	if override.InputCostPerTokenAbove200kTokens != nil {
		patched.InputCostPerTokenAbove200kTokens = override.InputCostPerTokenAbove200kTokens
	}
	if override.OutputCostPerTokenAbove200kTokens != nil {
		patched.OutputCostPerTokenAbove200kTokens = override.OutputCostPerTokenAbove200kTokens
	}
	if override.CacheCreationInputTokenCostAbove200kTokens != nil {
		patched.CacheCreationInputTokenCostAbove200kTokens = override.CacheCreationInputTokenCostAbove200kTokens
	}
	if override.CacheReadInputTokenCostAbove200kTokens != nil {
		patched.CacheReadInputTokenCostAbove200kTokens = override.CacheReadInputTokenCostAbove200kTokens
	}
	if override.CacheReadInputTokenCost != nil {
		patched.CacheReadInputTokenCost = override.CacheReadInputTokenCost
	}
	if override.CacheCreationInputTokenCost != nil {
		patched.CacheCreationInputTokenCost = override.CacheCreationInputTokenCost
	}
	if override.InputCostPerTokenBatches != nil {
		patched.InputCostPerTokenBatches = override.InputCostPerTokenBatches
	}
	if override.OutputCostPerTokenBatches != nil {
		patched.OutputCostPerTokenBatches = override.OutputCostPerTokenBatches
	}
	if override.InputCostPerImage != nil {
		patched.InputCostPerImage = override.InputCostPerImage
	}
	if override.OutputCostPerImage != nil {
		patched.OutputCostPerImage = override.OutputCostPerImage
	}

	return patched
}
