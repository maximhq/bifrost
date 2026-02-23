package modelcatalog

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

type compiledPricingOverride struct {
	override         schemas.ProviderPricingOverride
	scope            configstoreTables.PricingOverrideScope
	scopeID          string
	id               string
	regex            *regexp.Regexp
	requestModes     map[string]struct{}
	hasRequestFilter bool
	literalChars     int
	order            int
}

type modeBuckets struct {
	byMode  map[string][]compiledPricingOverride // normalized mode -> candidates
	generic []compiledPricingOverride            // no request-type filter
}

type compiledOverrides struct {
	virtualKey  map[string]*scopeOverrideIndex // vkID -> index
	providerKey map[string]*scopeOverrideIndex // pkID -> index
	provider    map[string]*scopeOverrideIndex // provider name -> index
	global      *scopeOverrideIndex            // single global index (nil if none)
}

type scopeOverrideIndex struct {
	exact    exactIndex  // model -> modeBuckets
	wildcard modeBuckets // wildcard overrides
	regex    modeBuckets // regex overrides
}

type exactIndex struct {
	byModel map[string]*modeBuckets
}

func (idx *scopeOverrideIndex) addOverride(c compiledPricingOverride) {
	switch c.override.MatchType {
	case schemas.PricingOverrideMatchExact:
		model := c.override.ModelPattern
		mb := idx.exact.byModel[model]
		if mb == nil {
			mb = &modeBuckets{byMode: make(map[string][]compiledPricingOverride)}
			idx.exact.byModel[model] = mb
		}
		addToModeBuckets(mb, c)
	case schemas.PricingOverrideMatchWildcard:
		addToModeBuckets(&idx.wildcard, c)
	case schemas.PricingOverrideMatchRegex:
		addToModeBuckets(&idx.regex, c)
	}
}

func addToModeBuckets(mb *modeBuckets, c compiledPricingOverride) {
	if c.hasRequestFilter {
		for mode := range c.requestModes {
			mb.byMode[mode] = append(mb.byMode[mode], c)
		}
	} else {
		mb.generic = append(mb.generic, c)
	}
}

func newScopeOverrideIndex() *scopeOverrideIndex {
	return &scopeOverrideIndex{
		exact:    exactIndex{byModel: make(map[string]*modeBuckets)},
		wildcard: modeBuckets{byMode: make(map[string][]compiledPricingOverride)},
		regex:    modeBuckets{byMode: make(map[string][]compiledPricingOverride)},
	}
}

func scopeKey(scope configstoreTables.PricingOverrideScope, scopeID string) string {
	return string(scope) + ":" + scopeID
}

func normalizeScopeID(scope configstoreTables.PricingOverrideScope, scopeID *string) string {
	if scope == configstoreTables.PricingOverrideScopeGlobal || scopeID == nil {
		return ""
	}
	return strings.TrimSpace(*scopeID)
}

func (mc *ModelCatalog) SetPricingOverrides(overrides []configstoreTables.TablePricingOverride) error {
	raw := make(map[string]configstoreTables.TablePricingOverride, len(overrides))
	for i := range overrides {
		raw[overrides[i].ID] = overrides[i]
	}
	compiled, err := compilePricingOverrides(raw)
	if err != nil {
		return err
	}

	mc.overridesMu.Lock()
	mc.rawPricingOverrides = raw
	mc.compiledPricingOverrides = compiled
	mc.overridesMu.Unlock()
	return nil
}

func (mc *ModelCatalog) UpsertPricingOverride(override configstoreTables.TablePricingOverride) error {
	mc.overridesMu.Lock()
	defer mc.overridesMu.Unlock()

	raw := make(map[string]configstoreTables.TablePricingOverride, len(mc.rawPricingOverrides)+1)
	for id, value := range mc.rawPricingOverrides {
		raw[id] = value
	}
	raw[override.ID] = override

	compiled, err := compilePricingOverrides(raw)
	if err != nil {
		return err
	}
	mc.rawPricingOverrides = raw
	mc.compiledPricingOverrides = compiled
	return nil
}

func (mc *ModelCatalog) DeletePricingOverride(id string) {
	mc.overridesMu.Lock()
	defer mc.overridesMu.Unlock()
	if len(mc.rawPricingOverrides) == 0 {
		return
	}
	raw := make(map[string]configstoreTables.TablePricingOverride, len(mc.rawPricingOverrides))
	for key, value := range mc.rawPricingOverrides {
		if key == id {
			continue
		}
		raw[key] = value
	}
	compiled, err := compilePricingOverrides(raw)
	if err != nil {
		// Best effort delete: keep existing cache untouched on compilation errors.
		return
	}
	mc.rawPricingOverrides = raw
	mc.compiledPricingOverrides = compiled
}

func (mc *ModelCatalog) GetPricingOverridesSnapshot() []configstoreTables.TablePricingOverride {
	mc.overridesMu.RLock()
	defer mc.overridesMu.RUnlock()
	if len(mc.rawPricingOverrides) == 0 {
		return nil
	}
	result := make([]configstoreTables.TablePricingOverride, 0, len(mc.rawPricingOverrides))
	for _, item := range mc.rawPricingOverrides {
		result = append(result, item)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})
	return result
}

func compilePricingOverrides(raw map[string]configstoreTables.TablePricingOverride) (*compiledOverrides, error) {
	if len(raw) == 0 {
		return nil, nil
	}

	overrides := make([]configstoreTables.TablePricingOverride, 0, len(raw))
	for _, item := range raw {
		if !item.Enabled {
			continue
		}
		overrides = append(overrides, item)
	}
	if len(overrides) == 0 {
		return nil, nil
	}

	sort.SliceStable(overrides, func(i, j int) bool {
		if overrides[i].CreatedAt.Equal(overrides[j].CreatedAt) {
			return overrides[i].ID < overrides[j].ID
		}
		return overrides[i].CreatedAt.Before(overrides[j].CreatedAt)
	})

	result := &compiledOverrides{
		virtualKey:  make(map[string]*scopeOverrideIndex),
		providerKey: make(map[string]*scopeOverrideIndex),
		provider:    make(map[string]*scopeOverrideIndex),
	}

	scopeOrder := make(map[string]int)
	for i := range overrides {
		item := overrides[i]
		compiled, err := compilePricingOverride(item)
		if err != nil {
			return nil, fmt.Errorf("invalid pricing override %s: %w", item.ID, err)
		}
		key := scopeKey(compiled.scope, compiled.scopeID)
		compiled.order = scopeOrder[key]
		scopeOrder[key]++

		switch compiled.scope {
		case configstoreTables.PricingOverrideScopeGlobal:
			if result.global == nil {
				result.global = newScopeOverrideIndex()
			}
			result.global.addOverride(compiled)
		case configstoreTables.PricingOverrideScopeProvider:
			idx := result.provider[compiled.scopeID]
			if idx == nil {
				idx = newScopeOverrideIndex()
				result.provider[compiled.scopeID] = idx
			}
			idx.addOverride(compiled)
		case configstoreTables.PricingOverrideScopeProviderKey:
			idx := result.providerKey[compiled.scopeID]
			if idx == nil {
				idx = newScopeOverrideIndex()
				result.providerKey[compiled.scopeID] = idx
			}
			idx.addOverride(compiled)
		case configstoreTables.PricingOverrideScopeVirtualKey:
			idx := result.virtualKey[compiled.scopeID]
			if idx == nil {
				idx = newScopeOverrideIndex()
				result.virtualKey[compiled.scopeID] = idx
			}
			idx.addOverride(compiled)
		}
	}
	return result, nil
}

func (mc *ModelCatalog) applyPricingOverrides(provider schemas.ModelProvider, selectedKeyID string, virtualKeyID string, model string, requestType schemas.RequestType, pricing configstoreTables.TableModelPricing) configstoreTables.TableModelPricing {
	mc.overridesMu.RLock()
	overrides := mc.compiledPricingOverrides
	mc.overridesMu.RUnlock()
	if overrides == nil {
		return pricing
	}

	mode := normalizeRequestType(requestType)

	if virtualKeyID != "" {
		if idx := overrides.virtualKey[virtualKeyID]; idx != nil {
			if best := selectBestFromIndex(idx, model, mode); best != nil {
				return patchPricing(pricing, best.override)
			}
		}
	}
	if selectedKeyID != "" {
		if idx := overrides.providerKey[selectedKeyID]; idx != nil {
			if best := selectBestFromIndex(idx, model, mode); best != nil {
				return patchPricing(pricing, best.override)
			}
		}
	}
	if idx := overrides.provider[string(provider)]; idx != nil {
		if best := selectBestFromIndex(idx, model, mode); best != nil {
			return patchPricing(pricing, best.override)
		}
	}
	if overrides.global != nil {
		if best := selectBestFromIndex(overrides.global, model, mode); best != nil {
			return patchPricing(pricing, best.override)
		}
	}

	return pricing
}

func selectBestFromIndex(idx *scopeOverrideIndex, model string, mode string) *compiledPricingOverride {
	if mb := idx.exact.byModel[model]; mb != nil {
		if best := selectBestFromBuckets(mb, mode); best != nil {
			return best
		}
	}

	if best := selectBestFromBucketsMatching(&idx.wildcard, model, mode); best != nil {
		return best
	}

	if best := selectBestFromBucketsMatching(&idx.regex, model, mode); best != nil {
		return best
	}

	return nil
}

func selectBestFromBuckets(mb *modeBuckets, mode string) *compiledPricingOverride {
	var best *compiledPricingOverride

	if candidates, ok := mb.byMode[mode]; ok {
		for i := range candidates {
			if isBetterOverride(&candidates[i], best) {
				best = &candidates[i]
			}
		}
	}

	for i := range mb.generic {
		if isBetterOverride(&mb.generic[i], best) {
			best = &mb.generic[i]
		}
	}

	return best
}

func selectBestFromBucketsMatching(mb *modeBuckets, model string, mode string) *compiledPricingOverride {
	var best *compiledPricingOverride

	if candidates, ok := mb.byMode[mode]; ok {
		for i := range candidates {
			if matchesModel(&candidates[i], model) && isBetterOverride(&candidates[i], best) {
				best = &candidates[i]
			}
		}
	}

	for i := range mb.generic {
		if matchesModel(&mb.generic[i], model) && isBetterOverride(&mb.generic[i], best) {
			best = &mb.generic[i]
		}
	}

	return best
}

func compilePricingOverride(override configstoreTables.TablePricingOverride) (compiledPricingOverride, error) {
	pattern := strings.TrimSpace(override.ModelPattern)
	if pattern == "" {
		return compiledPricingOverride{}, fmt.Errorf("model_pattern cannot be empty")
	}

	result := compiledPricingOverride{
		id:               override.ID,
		scope:            override.Scope,
		scopeID:          normalizeScopeID(override.Scope, override.ScopeID),
		override:         override.ToProviderPricingOverride(),
		requestModes:     make(map[string]struct{}),
		hasRequestFilter: false,
	}
	result.override.ModelPattern = pattern

	switch override.MatchType {
	case schemas.PricingOverrideMatchExact:
		result.literalChars = len(pattern)
	case schemas.PricingOverrideMatchWildcard:
		if !strings.Contains(pattern, "*") {
			return compiledPricingOverride{}, fmt.Errorf("wildcard model_pattern must contain '*'")
		}
		result.literalChars = len(strings.ReplaceAll(pattern, "*", ""))
	case schemas.PricingOverrideMatchRegex:
		re, err := regexp.Compile(pattern)
		if err != nil {
			return compiledPricingOverride{}, fmt.Errorf("invalid regex model_pattern: %w", err)
		}
		result.regex = re
		result.literalChars = len(pattern)
	default:
		return compiledPricingOverride{}, fmt.Errorf("unsupported match_type: %s", override.MatchType)
	}

	if len(override.RequestTypes) > 0 {
		result.hasRequestFilter = true
		for _, requestType := range override.RequestTypes {
			mode := normalizeRequestType(requestType)
			if mode == "unknown" {
				return compiledPricingOverride{}, fmt.Errorf("unsupported request_type: %s", requestType)
			}
			result.requestModes[mode] = struct{}{}
		}
	}

	return result, nil
}

func matchesModel(override *compiledPricingOverride, model string) bool {
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

func isBetterOverride(candidate, best *compiledPricingOverride) bool {
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
	if override.InputCostPerCharacter != nil {
		patched.InputCostPerCharacter = override.InputCostPerCharacter
	}
	if override.OutputCostPerCharacter != nil {
		patched.OutputCostPerCharacter = override.OutputCostPerCharacter
	}
	if override.InputCostPerTokenAbove128kTokens != nil {
		patched.InputCostPerTokenAbove128kTokens = override.InputCostPerTokenAbove128kTokens
	}
	if override.InputCostPerCharacterAbove128kTokens != nil {
		patched.InputCostPerCharacterAbove128kTokens = override.InputCostPerCharacterAbove128kTokens
	}
	if override.InputCostPerImageAbove128kTokens != nil {
		patched.InputCostPerImageAbove128kTokens = override.InputCostPerImageAbove128kTokens
	}
	if override.InputCostPerVideoPerSecondAbove128kTokens != nil {
		patched.InputCostPerVideoPerSecondAbove128kTokens = override.InputCostPerVideoPerSecondAbove128kTokens
	}
	if override.InputCostPerAudioPerSecondAbove128kTokens != nil {
		patched.InputCostPerAudioPerSecondAbove128kTokens = override.InputCostPerAudioPerSecondAbove128kTokens
	}
	if override.OutputCostPerTokenAbove128kTokens != nil {
		patched.OutputCostPerTokenAbove128kTokens = override.OutputCostPerTokenAbove128kTokens
	}
	if override.OutputCostPerCharacterAbove128kTokens != nil {
		patched.OutputCostPerCharacterAbove128kTokens = override.OutputCostPerCharacterAbove128kTokens
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
	if override.InputCostPerImageToken != nil {
		patched.InputCostPerImageToken = override.InputCostPerImageToken
	}
	if override.OutputCostPerImageToken != nil {
		patched.OutputCostPerImageToken = override.OutputCostPerImageToken
	}
	if override.InputCostPerImage != nil {
		patched.InputCostPerImage = override.InputCostPerImage
	}
	if override.OutputCostPerImage != nil {
		patched.OutputCostPerImage = override.OutputCostPerImage
	}
	if override.CacheReadInputImageTokenCost != nil {
		patched.CacheReadInputImageTokenCost = override.CacheReadInputImageTokenCost
	}

	return patched
}
