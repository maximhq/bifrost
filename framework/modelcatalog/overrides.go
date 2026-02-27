package modelcatalog

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

type PricingLookupScopes struct {
	VirtualKeyID  string
	ProviderKeyID string
	ProviderID    string
}

func normalizeScopeIDPointer(id *string) *string {
	if id == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*id)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

type compiledPricingOverride struct {
	override         schemas.PricingOverride
	pricingPatch     schemas.PricingOverridePatch
	wildcardPrefix   string
	requestModes     map[string]struct{}
	hasRequestFilter bool
	order            int
}

type pricingOverrideScopeBucket struct {
	exact                 map[string][]compiledPricingOverride
	wildcard              map[string][]compiledPricingOverride
	wildcardPrefixLengths []int
}

type compiledScopedOverrides struct {
	buckets map[string]*pricingOverrideScopeBucket
	byID    map[string]schemas.PricingOverride
}

func normalizedScopeKey(scopeKind schemas.PricingOverrideScopeKind, virtualKeyID, providerID, providerKeyID string) string {
	return string(scopeKind) + "|" + virtualKeyID + "|" + providerID + "|" + providerKeyID
}

func (mc *ModelCatalog) SetPricingOverrides(overrides []schemas.PricingOverride) error {
	compiled, err := compileScopedOverrides(overrides)
	if err != nil {
		return err
	}

	mc.overridesMu.Lock()
	mc.scopedOverrides = compiled
	mc.overridesMu.Unlock()
	return nil
}

func (mc *ModelCatalog) UpsertPricingOverride(override schemas.PricingOverride) error {
	mc.overridesMu.Lock()
	defer mc.overridesMu.Unlock()
	current := mc.scopedOverrides

	overrides := make([]schemas.PricingOverride, 0)
	if current != nil {
		for _, ov := range current.byID {
			if ov.ID == override.ID {
				continue
			}
			overrides = append(overrides, ov)
		}
	}
	overrides = append(overrides, override)
	slices.SortFunc(overrides, func(a, b schemas.PricingOverride) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	compiled, err := compileScopedOverrides(overrides)
	if err != nil {
		return err
	}
	mc.scopedOverrides = compiled
	return nil
}

func (mc *ModelCatalog) DeletePricingOverride(id string) {
	mc.overridesMu.Lock()
	defer mc.overridesMu.Unlock()
	current := mc.scopedOverrides
	if current == nil {
		return
	}
	overrides := make([]schemas.PricingOverride, 0, len(current.byID))
	for _, ov := range current.byID {
		if ov.ID == id {
			continue
		}
		overrides = append(overrides, ov)
	}
	slices.SortFunc(overrides, func(a, b schemas.PricingOverride) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	compiled, err := compileScopedOverrides(overrides)
	if err != nil {
		mc.logger.Warn("failed to recompile overrides after delete: %v", err)
		return
	}
	mc.scopedOverrides = compiled
}

func compileScopedOverrides(overrides []schemas.PricingOverride) (*compiledScopedOverrides, error) {
	compiled := &compiledScopedOverrides{
		buckets: make(map[string]*pricingOverrideScopeBucket),
		byID:    make(map[string]schemas.PricingOverride, len(overrides)),
	}

	for i := range overrides {
		item, err := compilePricingOverride(i, overrides[i])
		if err != nil {
			return nil, err
		}
		virtualKeyID := ""
		if item.override.VirtualKeyID != nil {
			virtualKeyID = *item.override.VirtualKeyID
		}
		providerID := ""
		if item.override.ProviderID != nil {
			providerID = *item.override.ProviderID
		}
		providerKeyID := ""
		if item.override.ProviderKeyID != nil {
			providerKeyID = *item.override.ProviderKeyID
		}
		key := normalizedScopeKey(item.override.ScopeKind, virtualKeyID, providerID, providerKeyID)
		bucket := compiled.buckets[key]
		if bucket == nil {
			bucket = &pricingOverrideScopeBucket{
				exact:    make(map[string][]compiledPricingOverride),
				wildcard: make(map[string][]compiledPricingOverride),
			}
			compiled.buckets[key] = bucket
		}
		switch item.override.MatchType {
		case schemas.PricingOverrideMatchExact:
			bucket.exact[item.override.Pattern] = append(bucket.exact[item.override.Pattern], item)
		case schemas.PricingOverrideMatchWildcard:
			if _, exists := bucket.wildcard[item.wildcardPrefix]; !exists {
				bucket.wildcardPrefixLengths = append(bucket.wildcardPrefixLengths, len(item.wildcardPrefix))
			}
			bucket.wildcard[item.wildcardPrefix] = append(bucket.wildcard[item.wildcardPrefix], item)
		}
		compiled.byID[item.override.ID] = item.override
	}
	for key := range compiled.buckets {
		bucket := compiled.buckets[key]
		slices.SortFunc(bucket.wildcardPrefixLengths, func(a, b int) int {
			if a > b {
				return -1
			}
			if a < b {
				return 1
			}
			return 0
		})
	}

	return compiled, nil
}

func (mc *ModelCatalog) applyScopedPricingOverrides(model string, requestType schemas.RequestType, pricing configstoreTables.TableModelPricing, scopes PricingLookupScopes) configstoreTables.TableModelPricing {
	mc.overridesMu.RLock()
	compiled := mc.scopedOverrides
	mc.overridesMu.RUnlock()
	if compiled == nil {
		return pricing
	}

	mode := normalizeRequestType(requestType)
	if mode == "unknown" {
		return pricing
	}

	if override := resolveScopedOverride(compiled, model, mode, scopes); override != nil {
		return patchPricing(pricing, override.pricingPatch)
	}
	return pricing
}

func resolveScopedOverride(compiled *compiledScopedOverrides, model, mode string, scopes PricingLookupScopes) *compiledPricingOverride {
	scopeOrder := make([]string, 0, 6)
	if scopes.VirtualKeyID != "" && scopes.ProviderID != "" && scopes.ProviderKeyID != "" {
		scopeOrder = append(scopeOrder, normalizedScopeKey(schemas.PricingOverrideScopeKindVirtualKeyProviderKey, scopes.VirtualKeyID, scopes.ProviderID, scopes.ProviderKeyID))
	}
	if scopes.VirtualKeyID != "" && scopes.ProviderID != "" {
		scopeOrder = append(scopeOrder, normalizedScopeKey(schemas.PricingOverrideScopeKindVirtualKeyProvider, scopes.VirtualKeyID, scopes.ProviderID, ""))
	}
	if scopes.VirtualKeyID != "" {
		scopeOrder = append(scopeOrder, normalizedScopeKey(schemas.PricingOverrideScopeKindVirtualKey, scopes.VirtualKeyID, "", ""))
	}
	if scopes.ProviderKeyID != "" {
		scopeOrder = append(scopeOrder, normalizedScopeKey(schemas.PricingOverrideScopeKindProviderKey, "", "", scopes.ProviderKeyID))
	}
	if scopes.ProviderID != "" {
		scopeOrder = append(scopeOrder, normalizedScopeKey(schemas.PricingOverrideScopeKindProvider, "", scopes.ProviderID, ""))
	}
	scopeOrder = append(scopeOrder, normalizedScopeKey(schemas.PricingOverrideScopeKindGlobal, "", "", ""))

	for _, key := range scopeOrder {
		bucket := compiled.buckets[key]
		if bucket == nil {
			continue
		}
		if best := selectBestOverride(bucket.exact[model], mode); best != nil {
			return best
		}
		for _, prefixLength := range bucket.wildcardPrefixLengths {
			if prefixLength > len(model) {
				continue
			}
			prefix := model[:prefixLength]
			if best := selectBestOverride(bucket.wildcard[prefix], mode); best != nil {
				return best
			}
		}
	}
	return nil
}

func selectBestOverride(candidates []compiledPricingOverride, mode string) *compiledPricingOverride {
	if len(candidates) == 0 {
		return nil
	}
	var bestSpecific *compiledPricingOverride
	var bestGeneric *compiledPricingOverride
	for i := range candidates {
		candidate := &candidates[i]
		if candidate.hasRequestFilter {
			if _, ok := candidate.requestModes[mode]; !ok {
				continue
			}
			if bestSpecific == nil || isBetterOverrideCandidate(candidate, bestSpecific) {
				bestSpecific = candidate
			}
			continue
		}
		if bestGeneric == nil || isBetterOverrideCandidate(candidate, bestGeneric) {
			bestGeneric = candidate
		}
	}
	if bestSpecific != nil {
		return bestSpecific
	}
	return bestGeneric
}

func isBetterOverrideCandidate(candidate, current *compiledPricingOverride) bool {
	if candidate.override.UpdatedAt.After(current.override.UpdatedAt) {
		return true
	}
	if candidate.override.UpdatedAt.Before(current.override.UpdatedAt) {
		return false
	}

	if candidate.override.ID < current.override.ID {
		return true
	}
	if candidate.override.ID > current.override.ID {
		return false
	}

	return candidate.order < current.order
}

func compilePricingOverride(order int, override schemas.PricingOverride) (compiledPricingOverride, error) {
	override.VirtualKeyID = normalizeScopeIDPointer(override.VirtualKeyID)
	override.ProviderID = normalizeScopeIDPointer(override.ProviderID)
	override.ProviderKeyID = normalizeScopeIDPointer(override.ProviderKeyID)

	if err := schemas.ValidatePricingOverrideScopeKind(override.ScopeKind, override.VirtualKeyID, override.ProviderID, override.ProviderKeyID); err != nil {
		return compiledPricingOverride{}, err
	}

	pattern, err := schemas.ValidatePricingOverridePattern(override.MatchType, override.Pattern)
	if err != nil {
		return compiledPricingOverride{}, err
	}
	override.Pattern = pattern

	compiled := compiledPricingOverride{
		override:     override,
		pricingPatch: override.Patch,
		requestModes: make(map[string]struct{}),
		order:        order,
	}

	switch override.MatchType {
	case schemas.PricingOverrideMatchExact:
	case schemas.PricingOverrideMatchWildcard:
		compiled.wildcardPrefix = strings.TrimSuffix(override.Pattern, "*")
	default:
		return compiledPricingOverride{}, fmt.Errorf("unsupported match_type: %s", override.MatchType)
	}

	if len(override.RequestTypes) > 0 {
		if err := schemas.ValidatePricingOverrideRequestTypes(override.RequestTypes); err != nil {
			return compiledPricingOverride{}, err
		}
		compiled.hasRequestFilter = true
		for _, requestType := range override.RequestTypes {
			compiled.requestModes[normalizeRequestType(requestType)] = struct{}{}
		}
	}

	return compiled, nil
}

func patchPricing(pricing configstoreTables.TableModelPricing, override schemas.PricingOverridePatch) configstoreTables.TableModelPricing {
	patched := pricing

	for _, field := range []struct {
		dst *float64
		src *float64
	}{
		{dst: &patched.InputCostPerToken, src: override.InputCostPerToken},
		{dst: &patched.OutputCostPerToken, src: override.OutputCostPerToken},
	} {
		setFloatValue(field.dst, field.src)
	}

	for _, field := range []struct {
		dst **float64
		src *float64
	}{
		{dst: &patched.InputCostPerTokenPriority, src: override.InputCostPerTokenPriority},
		{dst: &patched.OutputCostPerTokenPriority, src: override.OutputCostPerTokenPriority},
		{dst: &patched.InputCostPerVideoPerSecond, src: override.InputCostPerVideoPerSecond},
		{dst: &patched.OutputCostPerVideoPerSecond, src: override.OutputCostPerVideoPerSecond},
		{dst: &patched.OutputCostPerSecond, src: override.OutputCostPerSecond},
		{dst: &patched.InputCostPerAudioPerSecond, src: override.InputCostPerAudioPerSecond},
		{dst: &patched.InputCostPerSecond, src: override.InputCostPerSecond},
		{dst: &patched.InputCostPerAudioToken, src: override.InputCostPerAudioToken},
		{dst: &patched.OutputCostPerAudioToken, src: override.OutputCostPerAudioToken},
		{dst: &patched.InputCostPerCharacter, src: override.InputCostPerCharacter},
		{dst: &patched.OutputCostPerCharacter, src: override.OutputCostPerCharacter},
		{dst: &patched.InputCostPerTokenAbove128kTokens, src: override.InputCostPerTokenAbove128kTokens},
		{dst: &patched.InputCostPerCharacterAbove128kTokens, src: override.InputCostPerCharacterAbove128kTokens},
		{dst: &patched.InputCostPerImageAbove128kTokens, src: override.InputCostPerImageAbove128kTokens},
		{dst: &patched.InputCostPerVideoPerSecondAbove128kTokens, src: override.InputCostPerVideoPerSecondAbove128kTokens},
		{dst: &patched.InputCostPerAudioPerSecondAbove128kTokens, src: override.InputCostPerAudioPerSecondAbove128kTokens},
		{dst: &patched.OutputCostPerTokenAbove128kTokens, src: override.OutputCostPerTokenAbove128kTokens},
		{dst: &patched.OutputCostPerCharacterAbove128kTokens, src: override.OutputCostPerCharacterAbove128kTokens},
		{dst: &patched.InputCostPerTokenAbove200kTokens, src: override.InputCostPerTokenAbove200kTokens},
		{dst: &patched.OutputCostPerTokenAbove200kTokens, src: override.OutputCostPerTokenAbove200kTokens},
		{dst: &patched.CacheCreationInputTokenCostAbove200kTokens, src: override.CacheCreationInputTokenCostAbove200kTokens},
		{dst: &patched.CacheReadInputTokenCostAbove200kTokens, src: override.CacheReadInputTokenCostAbove200kTokens},
		{dst: &patched.CacheReadInputTokenCost, src: override.CacheReadInputTokenCost},
		{dst: &patched.CacheCreationInputTokenCost, src: override.CacheCreationInputTokenCost},
		{dst: &patched.CacheCreationInputTokenCostAbove1hr, src: override.CacheCreationInputTokenCostAbove1hr},
		{dst: &patched.CacheCreationInputTokenCostAbove1hrAbove200kTokens, src: override.CacheCreationInputTokenCostAbove1hrAbove200kTokens},
		{dst: &patched.CacheCreationInputAudioTokenCost, src: override.CacheCreationInputAudioTokenCost},
		{dst: &patched.CacheReadInputTokenCostPriority, src: override.CacheReadInputTokenCostPriority},
		{dst: &patched.InputCostPerTokenBatches, src: override.InputCostPerTokenBatches},
		{dst: &patched.OutputCostPerTokenBatches, src: override.OutputCostPerTokenBatches},
		{dst: &patched.InputCostPerImageToken, src: override.InputCostPerImageToken},
		{dst: &patched.OutputCostPerImageToken, src: override.OutputCostPerImageToken},
		{dst: &patched.InputCostPerImage, src: override.InputCostPerImage},
		{dst: &patched.OutputCostPerImage, src: override.OutputCostPerImage},
		{dst: &patched.InputCostPerPixel, src: override.InputCostPerPixel},
		{dst: &patched.OutputCostPerPixel, src: override.OutputCostPerPixel},
		{dst: &patched.OutputCostPerImagePremiumImage, src: override.OutputCostPerImagePremiumImage},
		{dst: &patched.OutputCostPerImageAbove512x512Pixels, src: override.OutputCostPerImageAbove512x512Pixels},
		{dst: &patched.OutputCostPerImageAbove512x512PixelsPremium, src: override.OutputCostPerImageAbove512x512PixelsPremium},
		{dst: &patched.OutputCostPerImageAbove1024x1024Pixels, src: override.OutputCostPerImageAbove1024x1024Pixels},
		{dst: &patched.OutputCostPerImageAbove1024x1024PixelsPremium, src: override.OutputCostPerImageAbove1024x1024PixelsPremium},
		{dst: &patched.CacheReadInputImageTokenCost, src: override.CacheReadInputImageTokenCost},
		{dst: &patched.SearchContextCostPerQuery, src: override.SearchContextCostPerQuery},
		{dst: &patched.CodeInterpreterCostPerSession, src: override.CodeInterpreterCostPerSession},
	} {
		setOptionalFloatValue(field.dst, field.src)
	}
	return patched
}

func setFloatValue(dst *float64, src *float64) {
	if src != nil {
		*dst = *src
	}
}

func setOptionalFloatValue(dst **float64, src *float64) {
	if src != nil {
		*dst = src
	}
}

func (mc *ModelCatalog) loadPricingOverridesFromStore(ctx context.Context) error {
	if mc.configStore == nil {
		return nil
	}
	rows, err := mc.configStore.GetPricingOverrides(ctx, configstore.PricingOverrideFilter{})
	if err != nil {
		return err
	}
	overrides := make([]schemas.PricingOverride, 0, len(rows))
	for i := range rows {
		overrides = append(overrides, schemas.PricingOverride{
			ID:            rows[i].ID,
			Name:          rows[i].Name,
			ScopeKind:     rows[i].ScopeKind,
			VirtualKeyID:  rows[i].VirtualKeyID,
			ProviderID:    rows[i].ProviderID,
			ProviderKeyID: rows[i].ProviderKeyID,
			MatchType:     rows[i].MatchType,
			Pattern:       rows[i].Pattern,
			RequestTypes:  rows[i].RequestTypes,
			Patch:         rows[i].Patch,
			ConfigHash:    rows[i].ConfigHash,
			CreatedAt:     rows[i].CreatedAt,
			UpdatedAt:     rows[i].UpdatedAt,
		})
	}
	return mc.SetPricingOverrides(overrides)
}
