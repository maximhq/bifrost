package modelcatalog

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

// PricingLookupScopes carries the runtime identifiers used to resolve scoped
// pricing overrides during cost calculation.
type PricingLookupScopes struct {
	VirtualKeyID  string
	SelectedKeyID string
	Provider      string
}

// PricingLookupScopesFromContext builds a PricingLookupScopes from a BifrostContext.
// It reads the governance virtual key ID (not the raw VK token) and the selected key ID.
// provider should be the provider name string (e.g. "openai"), pass "" if unavailable.
// Returns nil only when ctx is nil. An empty scopes value is still returned when all fields
// are empty so that global-scope overrides are always evaluated.
// DO NOT USE THIS FUNCTION IN A GO ROUTINE. This is because it reads from ctx which is cancelled when the request ends.
// Better to call it in PostHooks synchronously and then pass the scopes object to the pricing manager.
// Only use this in go routines when you know for sure that the request will not end before the go routine completes.
func PricingLookupScopesFromContext(ctx *schemas.BifrostContext, provider string) *PricingLookupScopes {
	if ctx == nil {
		return nil
	}
	virtualKeyID, _ := ctx.Value(schemas.BifrostContextKeyGovernanceVirtualKeyID).(string)
	selectedKeyID, _ := ctx.Value(schemas.BifrostContextKeySelectedKeyID).(string)
	return &PricingLookupScopes{
		VirtualKeyID:  virtualKeyID,
		SelectedKeyID: selectedKeyID,
		Provider:      provider,
	}
}

// ScopeKind identifies which governance scope an override applies to.
type ScopeKind string

const (
	ScopeKindGlobal                ScopeKind = "global"
	ScopeKindProvider              ScopeKind = "provider"
	ScopeKindProviderKey           ScopeKind = "provider_key"
	ScopeKindVirtualKey            ScopeKind = "virtual_key"
	ScopeKindVirtualKeyProvider    ScopeKind = "virtual_key_provider"
	ScopeKindVirtualKeyProviderKey ScopeKind = "virtual_key_provider_key"
)

// MatchType controls how an override pattern is matched against model names.
type MatchType string

const (
	MatchTypeExact    MatchType = "exact"
	MatchTypeWildcard MatchType = "wildcard"
)

// PricingOverride describes a scoped pricing override shared across config storage,
// model catalog compilation, and governance APIs.
type PricingOverride struct {
	ID            string                `json:"id"`
	Name          string                `json:"name"`
	ScopeKind     ScopeKind             `json:"scope_kind"`
	VirtualKeyID  *string               `json:"virtual_key_id,omitempty"`
	ProviderID    *string               `json:"provider_id,omitempty"`
	ProviderKeyID *string               `json:"provider_key_id,omitempty"`
	MatchType     MatchType             `json:"match_type"`
	Pattern       string                `json:"pattern"`
	RequestTypes  []schemas.RequestType `json:"request_types,omitempty"`
	Options       PricingOptions        `json:"options"`
}

// customPricingEntry is a single flattened override ready for lookup.
type customPricingEntry struct {
	id            string
	scopeKind     ScopeKind
	virtualKeyID  string
	providerID    string
	providerKeyID string
	pattern       string // exact model name, or wildcard prefix (trailing * stripped)
	wildcard      bool
	requestModes  map[string]struct{} // always non-nil for valid overrides
	options       PricingOptions
}

// customPricingData is the in-memory lookup structure for pricing overrides.
// Exact matches are indexed by model name; wildcards are a flat slice.
type customPricingData struct {
	exact    map[string][]customPricingEntry
	wildcard []customPricingEntry
}

// IsValid validates the shared pricing override contract before persistence or runtime use.
//
// Input:  override — the PricingOverride to validate (receiver).
// Output: error — non-nil if any scope, pattern, or request-type constraint is violated.
func (override *PricingOverride) IsValid() error {
	if err := override.validateScopeKind(); err != nil {
		return err
	}
	if err := override.validatePattern(); err != nil {
		return err
	}
	return override.validateRequestTypes()
}

// validateScopeKind validates the scope identifiers required by override.ScopeKind.
//
// Input:  override — receiver; ScopeKind and the three optional ID fields are inspected.
// Output: error — non-nil when required identifiers are absent or forbidden ones are present.
func (override *PricingOverride) validateScopeKind() error {
	switch override.ScopeKind {
	case ScopeKindGlobal:
		if override.VirtualKeyID != nil || override.ProviderID != nil || override.ProviderKeyID != nil {
			return fmt.Errorf("global scope_kind must not include scope identifiers")
		}
	case ScopeKindProvider:
		if override.ProviderID == nil {
			return fmt.Errorf("provider_id is required for provider scope_kind")
		}
		if override.VirtualKeyID != nil || override.ProviderKeyID != nil {
			return fmt.Errorf("provider scope_kind only supports provider_id")
		}
	case ScopeKindProviderKey:
		if override.ProviderKeyID == nil {
			return fmt.Errorf("provider_key_id is required for provider_key scope_kind")
		}
		if override.VirtualKeyID != nil || override.ProviderID != nil {
			return fmt.Errorf("provider_key scope_kind only supports provider_key_id")
		}
	case ScopeKindVirtualKey:
		if override.VirtualKeyID == nil {
			return fmt.Errorf("virtual_key_id is required for virtual_key scope_kind")
		}
		if override.ProviderID != nil || override.ProviderKeyID != nil {
			return fmt.Errorf("virtual_key scope_kind only supports virtual_key_id")
		}
	case ScopeKindVirtualKeyProvider:
		if override.VirtualKeyID == nil || override.ProviderID == nil {
			return fmt.Errorf("virtual_key_id and provider_id are required for virtual_key_provider scope_kind")
		}
		if override.ProviderKeyID != nil {
			return fmt.Errorf("virtual_key_provider scope_kind does not support provider_key_id")
		}
	case ScopeKindVirtualKeyProviderKey:
		if override.VirtualKeyID == nil || override.ProviderID == nil || override.ProviderKeyID == nil {
			return fmt.Errorf("virtual_key_id, provider_id, and provider_key_id are required for virtual_key_provider_key scope_kind")
		}
	default:
		return fmt.Errorf("unsupported scope_kind %q", override.ScopeKind)
	}
	return nil
}

// validatePattern checks that Pattern is non-empty and consistent with MatchType.
//
// Input:  override — receiver; Pattern and MatchType are inspected.
// Output: error — non-nil when the pattern is empty, contains a wildcard for exact mode,
//
//	or does not end with a single trailing "*" for wildcard mode.
func (override *PricingOverride) validatePattern() error {
	pattern := strings.TrimSpace(override.Pattern)
	if pattern == "" {
		return fmt.Errorf("pattern is required")
	}
	switch override.MatchType {
	case MatchTypeExact:
		if strings.Contains(pattern, "*") {
			return fmt.Errorf("exact match pattern must not contain wildcards")
		}
	case MatchTypeWildcard:
		if !strings.HasSuffix(pattern, "*") {
			return fmt.Errorf("wildcard pattern must end with *")
		}
		if strings.Count(pattern, "*") != 1 {
			return fmt.Errorf("wildcard pattern must contain exactly one trailing *")
		}
	default:
		return fmt.Errorf("unsupported match_type %q", override.MatchType)
	}
	return nil
}

// validateRequestTypes checks that RequestTypes is non-empty and that every entry is a
// supported base request type. Stream variants (e.g. chat_completion_stream) are rejected —
// the base type (chat_completion) already covers both streaming and non-streaming requests.
//
// Input:  override — receiver; RequestTypes slice is inspected.
// Output: error — non-nil if RequestTypes is empty, or contains an unsupported or stream variant.
func (override *PricingOverride) validateRequestTypes() error {
	if len(override.RequestTypes) == 0 {
		return fmt.Errorf("request_types is required and must contain at least one value")
	}
	for _, rt := range override.RequestTypes {
		if normalizeStreamRequestType(rt) != rt {
			return fmt.Errorf("unsupported request_type %q: use the base type (e.g. %q covers both streaming and non-streaming)", rt, normalizeStreamRequestType(rt))
		}
		if normalizeRequestType(rt) == "unknown" {
			return fmt.Errorf("unsupported request_type %q", rt)
		}
	}
	return nil
}

// matchesScope reports whether the entry's governance scope matches the runtime identifiers.
//
// Input:  scopes — runtime VirtualKeyID, SelectedKeyID, and Provider to match against.
// Output: bool — true when the entry's scope kind and stored IDs align with scopes.
func (e *customPricingEntry) matchesScope(scopes PricingLookupScopes) bool {
	switch e.scopeKind {
	case ScopeKindGlobal:
		return true
	case ScopeKindProvider:
		return e.providerID == scopes.Provider
	case ScopeKindProviderKey:
		return e.providerKeyID == scopes.SelectedKeyID
	case ScopeKindVirtualKey:
		return e.virtualKeyID == scopes.VirtualKeyID
	case ScopeKindVirtualKeyProvider:
		return e.virtualKeyID == scopes.VirtualKeyID && e.providerID == scopes.Provider
	case ScopeKindVirtualKeyProviderKey:
		return e.virtualKeyID == scopes.VirtualKeyID && e.providerID == scopes.Provider && e.providerKeyID == scopes.SelectedKeyID
	}
	return false
}

// matchesMode reports whether the entry applies to the given normalized request mode.
//
// Input:  mode — normalized request type string (e.g. "chat", "embedding").
// Output: bool — true when requestModes contains mode.
func (e *customPricingEntry) matchesMode(mode string) bool {
	_, ok := e.requestModes[mode]
	return ok
}

// resolve walks the 6-scope priority hierarchy and returns the first matching
// pricing patch for the given model, request mode, and runtime scopes.
//
// Input:  model  — exact model name being priced.
//
//	mode   — normalized request type string (e.g. "chat", "embedding").
//	scopes — runtime governance identifiers used to narrow the scope search.
//
// Output: *PricingOptions — pointer to the first matching override's options, or nil if none match.
func (c *customPricingData) resolve(model, mode string, scopes PricingLookupScopes) *PricingOptions {
	for _, scopeKind := range scopePriorityOrder(scopes) {
		for i := range c.exact[model] {
			e := &c.exact[model][i]
			if e.scopeKind == scopeKind && e.matchesScope(scopes) && e.matchesMode(mode) {
				return &e.options
			}
		}
		for i := range c.wildcard {
			e := &c.wildcard[i]
			if e.scopeKind == scopeKind && e.matchesScope(scopes) && strings.HasPrefix(model, e.pattern) && e.matchesMode(mode) {
				return &e.options
			}
		}
	}
	return nil
}

// scopePriorityOrder returns scope kinds in most-specific-first order,
// skipping scopes that can't match given the available runtime identifiers.
//
// Input:  scopes — runtime governance identifiers; empty fields cause the corresponding scope kinds to be omitted.
// Output: []ScopeKind — ordered list from most-specific (VirtualKeyProviderKey) to least-specific (Global).
func scopePriorityOrder(scopes PricingLookupScopes) []ScopeKind {
	order := make([]ScopeKind, 0, 6)
	if scopes.VirtualKeyID != "" && scopes.Provider != "" && scopes.SelectedKeyID != "" {
		order = append(order, ScopeKindVirtualKeyProviderKey)
	}
	if scopes.VirtualKeyID != "" && scopes.Provider != "" {
		order = append(order, ScopeKindVirtualKeyProvider)
	}
	if scopes.VirtualKeyID != "" {
		order = append(order, ScopeKindVirtualKey)
	}
	if scopes.SelectedKeyID != "" {
		order = append(order, ScopeKindProviderKey)
	}
	if scopes.Provider != "" {
		order = append(order, ScopeKindProvider)
	}
	order = append(order, ScopeKindGlobal)
	return order
}

// buildCustomPricingData constructs a customPricingData lookup structure from a raw override slice.
//
// Input:  overrides — slice of validated PricingOverride records loaded from the config store.
// Output: *customPricingData — ready-to-query structure with exact and wildcard indexes populated.
func buildCustomPricingData(overrides []PricingOverride) *customPricingData {
	data := &customPricingData{
		exact: make(map[string][]customPricingEntry, len(overrides)),
	}
	for _, o := range overrides {
		entry := customPricingEntry{
			id:        o.ID,
			scopeKind: o.ScopeKind,
			options:   o.Options,
		}
		if o.VirtualKeyID != nil {
			entry.virtualKeyID = *o.VirtualKeyID
		}
		if o.ProviderID != nil {
			entry.providerID = *o.ProviderID
		}
		if o.ProviderKeyID != nil {
			entry.providerKeyID = *o.ProviderKeyID
		}
		entry.requestModes = make(map[string]struct{}, len(o.RequestTypes))
		for _, rt := range o.RequestTypes {
			entry.requestModes[normalizeRequestType(rt)] = struct{}{}
		}
		pattern := strings.TrimSpace(o.Pattern)
		switch o.MatchType {
		case MatchTypeExact:
			entry.pattern = pattern
			data.exact[pattern] = append(data.exact[pattern], entry)
		case MatchTypeWildcard:
			entry.pattern = strings.TrimSuffix(pattern, "*")
			entry.wildcard = true
			data.wildcard = append(data.wildcard, entry)
		}
	}
	// Sort wildcards by descending prefix length so more-specific patterns (e.g. "gpt-4*")
	// are checked before broader ones (e.g. "gpt-*"), making precedence deterministic.
	sort.Slice(data.wildcard, func(i, j int) bool {
		return len(data.wildcard[i].pattern) > len(data.wildcard[j].pattern)
	})
	return data
}

// applyPricingOverrides resolves any active scoped pricing override for the given model
// and request type, then patches the catalog base pricing with the override values.
// It returns the original pricing unchanged when no custom pricing tree is loaded or
// when the request type cannot be mapped to a known pricing mode.
//
// Input:  model       — exact model name being priced.
//
//	requestType — the request type used to derive the pricing mode.
//	pricing     — base pricing row from the catalog to patch.
//	scopes      — runtime governance identifiers used to narrow the override scope.
//
// Output: TableModelPricing — patched pricing row, or pricing unchanged if no override matches.
// bool — true when an override was applied, false otherwise.
func (mc *ModelCatalog) applyPricingOverrides(model string, requestType schemas.RequestType, pricing configstoreTables.TableModelPricing, scopes PricingLookupScopes) (configstoreTables.TableModelPricing, bool) {
	mc.overridesMu.RLock()
	custom := mc.customPricing
	mc.overridesMu.RUnlock()

	if custom == nil {
		return pricing, false
	}

	mode := normalizeRequestType(requestType)
	if mode == "unknown" {
		return pricing, false
	}

	if patch := custom.resolve(model, mode, scopes); patch != nil {
		return patchPricing(pricing, *patch), true
	}
	return pricing, false
}

// patchPricing applies override values onto a copy of the base pricing row.
// For all fields, a non-nil override pointer replaces the corresponding destination value;
// a nil override leaves the base value intact.
// The original pricing row is never modified; a patched copy is always returned.
//
// Input:  pricing  — base pricing row from the catalog.
//
//	override — pricing options sourced from the matched override entry.
//
// Output: TableModelPricing — shallow copy of pricing with override fields applied.
func patchPricing(pricing configstoreTables.TableModelPricing, override PricingOptions) configstoreTables.TableModelPricing {
	patched := pricing

	for _, field := range []struct {
		dst **float64
		src *float64
	}{
		{dst: &patched.InputCostPerToken, src: override.InputCostPerToken},
		{dst: &patched.OutputCostPerToken, src: override.OutputCostPerToken},
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
		{dst: &patched.InputCostPerTokenAbove128kTokens, src: override.InputCostPerTokenAbove128kTokens},
		{dst: &patched.InputCostPerImageAbove128kTokens, src: override.InputCostPerImageAbove128kTokens},
		{dst: &patched.InputCostPerVideoPerSecondAbove128kTokens, src: override.InputCostPerVideoPerSecondAbove128kTokens},
		{dst: &patched.InputCostPerAudioPerSecondAbove128kTokens, src: override.InputCostPerAudioPerSecondAbove128kTokens},
		{dst: &patched.OutputCostPerTokenAbove128kTokens, src: override.OutputCostPerTokenAbove128kTokens},
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
		{dst: &patched.OutputCostPerImageAbove2048x2048Pixels, src: override.OutputCostPerImageAbove2048x2048Pixels},
		{dst: &patched.OutputCostPerImageAbove4096x4096Pixels, src: override.OutputCostPerImageAbove4096x4096Pixels},
		{dst: &patched.CacheReadInputImageTokenCost, src: override.CacheReadInputImageTokenCost},
		{dst: &patched.SearchContextCostPerQuery, src: override.SearchContextCostPerQuery},
		{dst: &patched.CodeInterpreterCostPerSession, src: override.CodeInterpreterCostPerSession},
		{dst: &patched.OutputCostPerImageLowQuality, src: override.OutputCostPerImageLowQuality},
		{dst: &patched.OutputCostPerImageMediumQuality, src: override.OutputCostPerImageMediumQuality},
		{dst: &patched.OutputCostPerImageHighQuality, src: override.OutputCostPerImageHighQuality},
		{dst: &patched.OutputCostPerImageAutoQuality, src: override.OutputCostPerImageAutoQuality},
	} {
		if field.src != nil {
			*field.dst = field.src
		}
	}
	return patched
}

func (mc *ModelCatalog) loadPricingOverridesFromStore(ctx context.Context) error {
	if mc.configStore == nil {
		return nil
	}
	rows, err := mc.configStore.GetPricingOverrides(ctx, configstore.PricingOverrideFilters{})
	if err != nil {
		return err
	}
	return mc.SetPricingOverrides(rows)
}
