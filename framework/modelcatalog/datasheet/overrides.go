package datasheet

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

// IsValid validates the shared override contract before persistence or runtime use.
func (override *Override) IsValid() error {
	if err := override.validateScopeKind(); err != nil {
		return err
	}
	if err := override.validatePattern(); err != nil {
		return err
	}
	return override.validateRequestTypes()
}

func (override *Override) validateScopeKind() error {
	switch override.ScopeKind {
	case ScopeKindGlobal:
		if override.UserID != nil || override.VirtualKeyID != nil || override.ProviderID != nil || override.ProviderKeyID != nil {
			return fmt.Errorf("global scope_kind must not include scope identifiers")
		}
	case ScopeKindUser:
		if override.UserID == nil {
			return fmt.Errorf("user_id is required for user scope_kind")
		}
		if override.VirtualKeyID != nil || override.ProviderID != nil || override.ProviderKeyID != nil {
			return fmt.Errorf("user scope_kind only supports user_id")
		}
	case ScopeKindUserProvider:
		if override.UserID == nil || override.ProviderID == nil {
			return fmt.Errorf("user_id and provider_id are required for user_provider scope_kind")
		}
		if override.VirtualKeyID != nil || override.ProviderKeyID != nil {
			return fmt.Errorf("user_provider scope_kind does not support virtual_key_id or provider_key_id")
		}
	case ScopeKindUserProviderKey:
		if override.UserID == nil || override.ProviderID == nil || override.ProviderKeyID == nil {
			return fmt.Errorf("user_id, provider_id, and provider_key_id are required for user_provider_key scope_kind")
		}
		if override.VirtualKeyID != nil {
			return fmt.Errorf("user_provider_key scope_kind does not support virtual_key_id")
		}
	case ScopeKindProvider:
		if override.ProviderID == nil {
			return fmt.Errorf("provider_id is required for provider scope_kind")
		}
		if override.UserID != nil || override.VirtualKeyID != nil || override.ProviderKeyID != nil {
			return fmt.Errorf("provider scope_kind only supports provider_id")
		}
	case ScopeKindProviderKey:
		if override.ProviderKeyID == nil {
			return fmt.Errorf("provider_key_id is required for provider_key scope_kind")
		}
		if override.UserID != nil || override.VirtualKeyID != nil || override.ProviderID != nil {
			return fmt.Errorf("provider_key scope_kind only supports provider_key_id")
		}
	case ScopeKindVirtualKey:
		if override.VirtualKeyID == nil {
			return fmt.Errorf("virtual_key_id is required for virtual_key scope_kind")
		}
		if override.UserID != nil || override.ProviderID != nil || override.ProviderKeyID != nil {
			return fmt.Errorf("virtual_key scope_kind only supports virtual_key_id")
		}
	case ScopeKindVirtualKeyProvider:
		if override.VirtualKeyID == nil || override.ProviderID == nil {
			return fmt.Errorf("virtual_key_id and provider_id are required for virtual_key_provider scope_kind")
		}
		if override.UserID != nil || override.ProviderKeyID != nil {
			return fmt.Errorf("virtual_key_provider scope_kind does not support provider_key_id or user_id")
		}
	case ScopeKindVirtualKeyProviderKey:
		if override.VirtualKeyID == nil || override.ProviderID == nil || override.ProviderKeyID == nil {
			return fmt.Errorf("virtual_key_id, provider_id, and provider_key_id are required for virtual_key_provider_key scope_kind")
		}
		if override.UserID != nil {
			return fmt.Errorf("virtual_key_provider_key scope_kind does not support user_id")
		}
	default:
		return fmt.Errorf("unsupported scope_kind %q", override.ScopeKind)
	}
	return nil
}

func (override *Override) validatePattern() error {
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

// validateRequestTypes checks that RequestTypes is non-empty and that every
// entry is a supported base request type. Stream variants are rejected — the
// base type already covers both streaming and non-streaming requests.
func (override *Override) validateRequestTypes() error {
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

// matchesScope reports whether the entry's governance scope matches the
// runtime identifiers.
func (e *customPricingEntry) matchesScope(scopes LookupScopes) bool {
	switch e.scopeKind {
	case ScopeKindGlobal:
		return true
	case ScopeKindUser:
		return e.userID == scopes.UserID
	case ScopeKindUserProvider:
		return e.userID == scopes.UserID && e.providerID == scopes.Provider
	case ScopeKindUserProviderKey:
		return e.userID == scopes.UserID && e.providerID == scopes.Provider && e.providerKeyID == scopes.SelectedKeyID
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

func (e *customPricingEntry) matchesMode(mode string) bool {
	_, ok := e.requestModes[mode]
	return ok
}

// matchesCatalogProvider reports whether the entry could ever apply to a model
// served by provider. Weaker than matchesScope on purpose: the management
// catalog has no virtual-key/user/selected-key context, so those scopes can
// never satisfy matchesScope there, yet they still deserve to be listed
// informationally. Entries whose scope pins a provider must match it;
// provider_key-scoped entries carry no provider_id, so they always pass.
func (e *customPricingEntry) matchesCatalogProvider(provider string) bool {
	return e.providerID == "" || e.providerID == provider
}

// matchesModel reports whether the entry's pattern covers model.
func (e *customPricingEntry) matchesModel(model string) bool {
	if e.wildcard {
		return strings.HasPrefix(model, e.pattern)
	}
	return e.pattern == model
}

// catalogScopeRank orders scope kinds most-specific-first for display. Mirrors
// scopePriorityOrder's ranking but is independent of runtime identifiers,
// which the management catalog does not have.
func catalogScopeRank(k ScopeKind) int {
	switch k {
	case ScopeKindVirtualKeyProviderKey:
		return 0
	case ScopeKindVirtualKeyProvider:
		return 1
	case ScopeKindVirtualKey:
		return 2
	case ScopeKindUserProviderKey:
		return 3
	case ScopeKindUserProvider:
		return 4
	case ScopeKindUser:
		return 5
	case ScopeKindProviderKey:
		return 6
	case ScopeKindProvider:
		return 7
	case ScopeKindGlobal:
		return 8
	}
	return 9
}

// resolve walks the 9-scope priority hierarchy and returns the first matching
// pricing patch for the given model, mode, and runtime scopes.
func (c *customPricingData) resolve(model, mode string, scopes LookupScopes) *Options {
	if e := c.resolveEntry(model, mode, scopes); e != nil {
		return &e.options
	}
	return nil
}

// resolveEntry is resolve's implementation, returning the winning entry itself
// so callers that need the override's identity (not just its patch) can
// recover it without duplicating the precedence walk.
func (c *customPricingData) resolveEntry(model, mode string, scopes LookupScopes) *customPricingEntry {
	for _, scopeKind := range scopePriorityOrder(scopes) {
		for i := range c.exact[model] {
			e := &c.exact[model][i]
			if e.scopeKind == scopeKind && e.matchesScope(scopes) && e.matchesMode(mode) {
				return e
			}
		}
		for i := range c.wildcard {
			e := &c.wildcard[i]
			if e.scopeKind == scopeKind && e.matchesScope(scopes) && strings.HasPrefix(model, e.pattern) && e.matchesMode(mode) {
				return e
			}
		}
	}
	return nil
}

// scopePriorityOrder returns scope kinds in most-specific-first order,
// skipping scopes that can't match given the available runtime identifiers.
// The virtual-key family is checked before the user family: pricing pinned
// to the credential wins over pricing that follows the person. Within each
// family, more identifiers rank higher.
func scopePriorityOrder(scopes LookupScopes) []ScopeKind {
	order := make([]ScopeKind, 0, 9)
	if scopes.VirtualKeyID != "" && scopes.Provider != "" && scopes.SelectedKeyID != "" {
		order = append(order, ScopeKindVirtualKeyProviderKey)
	}
	if scopes.VirtualKeyID != "" && scopes.Provider != "" {
		order = append(order, ScopeKindVirtualKeyProvider)
	}
	if scopes.VirtualKeyID != "" {
		order = append(order, ScopeKindVirtualKey)
	}
	if scopes.UserID != "" && scopes.Provider != "" && scopes.SelectedKeyID != "" {
		order = append(order, ScopeKindUserProviderKey)
	}
	if scopes.UserID != "" && scopes.Provider != "" {
		order = append(order, ScopeKindUserProvider)
	}
	if scopes.UserID != "" {
		order = append(order, ScopeKindUser)
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

// buildCustomPricingData constructs the lookup structure from a raw override
// slice. Wildcards are sorted longest-prefix-first so more specific patterns
// (e.g. "gpt-4*") win over broader ones ("gpt-*") deterministically.
func buildCustomPricingData(overrides []Override) *customPricingData {
	data := &customPricingData{
		exact: make(map[string][]customPricingEntry, len(overrides)),
	}
	for _, o := range overrides {
		entry := customPricingEntry{
			id:        o.ID,
			scopeKind: o.ScopeKind,
			options:   o.Options,
		}
		if o.UserID != nil {
			entry.userID = *o.UserID
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
	sort.Slice(data.wildcard, func(i, j int) bool {
		return len(data.wildcard[i].pattern) > len(data.wildcard[j].pattern)
	})
	return data
}

// applyPricingOverrides resolves any active scoped override for (model,
// requestType) and patches the catalog base pricing. Returns the original
// pricing unchanged when no override matches or the request type can't be
// mapped to a known pricing mode.
func (s *Store) applyPricingOverrides(model string, requestType schemas.RequestType, pricing configstoreTables.TableModelPricing, scopes LookupScopes) (configstoreTables.TableModelPricing, bool) {
	s.overridesMu.RLock()
	custom := s.customPricing
	s.overridesMu.RUnlock()

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

// cloneOptions returns a copy of o in which every non-nil pointer field
// addresses a fresh value.
//
// buildCustomPricingData stores Options by value, so a runtime entry shares
// every pointer with the override it came from. Options is nothing but pointer
// fields, so copying the struct alone still lets a caller reach through and
// repricing live requests. Reflection rather than field-by-field assignment
// keeps this correct as Options gains fields — a missed field would fail
// silently. Only the management catalog path pays for this; the request path
// dereferences through resolve() and never hands pointers out.
func cloneOptions(o Options) Options {
	out := o
	v := reflect.ValueOf(&out).Elem()
	for _, f := range v.Fields() {
		if f.Kind() != reflect.Pointer || f.IsNil() || !f.CanSet() {
			continue
		}
		fresh := reflect.New(f.Type().Elem())
		fresh.Elem().Set(f.Elem())
		f.Set(fresh)
	}
	return out
}

// cloneOverride returns a copy of o sharing no mutable state with it.
func cloneOverride(o Override) Override {
	out := o
	out.UserID = cloneStringPtr(o.UserID)
	out.VirtualKeyID = cloneStringPtr(o.VirtualKeyID)
	out.ProviderID = cloneStringPtr(o.ProviderID)
	out.ProviderKeyID = cloneStringPtr(o.ProviderKeyID)
	out.RequestTypes = slices.Clone(o.RequestTypes)
	out.Options = cloneOptions(o.Options)
	return out
}

func cloneStringPtr(s *string) *string {
	if s == nil {
		return nil
	}
	v := *s
	return &v
}

// CatalogPricingOverrides summarizes how scoped overrides affect one
// (model, provider) row of the management model catalog.
type CatalogPricingOverrides struct {
	// AppliedID is the override that wins under the global/provider scopes
	// only — the scopes that apply to every request regardless of caller.
	// Empty when nothing global/provider-scoped matches.
	AppliedID string
	// AppliedPatch is that winner's option patch; nil when AppliedID is empty.
	AppliedPatch *Options
	// Matching lists every override matching (model, provider) regardless of
	// scope or mode, most-specific-first, for informational display. Includes
	// the applied one.
	Matching []Override
}

// CatalogPricingOverrides returns the overrides relevant to one management
// catalog row. mode is the pricing mode of the base row being displayed
// ("chat", "embedding", …).
//
// The management catalog has no virtual-key/user/selected-key context, so
// AppliedPatch is resolved with only the provider set — scopePriorityOrder
// then naturally yields [provider, global], the two scopes that hold for
// every caller. Overrides in the virtual-key/user/provider-key families are
// still returned in Matching so the UI can show them as informational.
func (s *Store) CatalogPricingOverrides(model string, provider schemas.ModelProvider, mode string) CatalogPricingOverrides {
	s.overridesMu.RLock()
	custom := s.customPricing
	raw := s.rawOverrides
	s.overridesMu.RUnlock()

	if custom == nil || len(raw) == 0 {
		return CatalogPricingOverrides{}
	}

	var out CatalogPricingOverrides
	if e := custom.resolveEntry(model, mode, LookupScopes{Provider: string(provider)}); e != nil {
		patch := cloneOptions(e.options)
		out.AppliedID = e.id
		out.AppliedPatch = &patch
	}

	matched := make(map[string]struct{})
	collect := func(entries []customPricingEntry) {
		for i := range entries {
			e := &entries[i]
			if e.matchesModel(model) && e.matchesCatalogProvider(string(provider)) {
				matched[e.id] = struct{}{}
			}
		}
	}
	collect(custom.exact[model])
	collect(custom.wildcard)
	if len(matched) == 0 {
		return out
	}

	out.Matching = make([]Override, 0, len(matched))
	for i := range raw {
		if _, ok := matched[raw[i].ID]; ok {
			out.Matching = append(out.Matching, cloneOverride(raw[i]))
		}
	}
	sort.SliceStable(out.Matching, func(i, j int) bool {
		a, b := out.Matching[i], out.Matching[j]
		if ra, rb := catalogScopeRank(a.ScopeKind), catalogScopeRank(b.ScopeKind); ra != rb {
			return ra < rb
		}
		// Exact patterns are more specific than wildcards; among wildcards the
		// longer prefix wins, matching buildCustomPricingData's ordering.
		if av, bv := a.MatchType == MatchTypeWildcard, b.MatchType == MatchTypeWildcard; av != bv {
			return !av
		}
		if len(a.Pattern) != len(b.Pattern) {
			return len(a.Pattern) > len(b.Pattern)
		}
		return a.ID < b.ID
	})
	return out
}

// patchPricing returns a copy of pricing with override fields applied. Nil
// fields in override leave the corresponding base values intact.
func patchPricing(pricing configstoreTables.TableModelPricing, override Options) configstoreTables.TableModelPricing {
	patched := pricing
	for _, field := range []struct {
		dst **float64
		src *float64
	}{
		{dst: &patched.InputCostPerToken, src: override.InputCostPerToken},
		{dst: &patched.OutputCostPerToken, src: override.OutputCostPerToken},
		{dst: &patched.InputCostPerTokenPriority, src: override.InputCostPerTokenPriority},
		{dst: &patched.OutputCostPerTokenPriority, src: override.OutputCostPerTokenPriority},
		{dst: &patched.InputCostPerTokenFlex, src: override.InputCostPerTokenFlex},
		{dst: &patched.OutputCostPerTokenFlex, src: override.OutputCostPerTokenFlex},
		{dst: &patched.InputCostPerTokenFast, src: override.InputCostPerTokenFast},
		{dst: &patched.OutputCostPerTokenFast, src: override.OutputCostPerTokenFast},
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
		{dst: &patched.InputCostPerTokenAbove200kTokensPriority, src: override.InputCostPerTokenAbove200kTokensPriority},
		{dst: &patched.OutputCostPerTokenAbove200kTokens, src: override.OutputCostPerTokenAbove200kTokens},
		{dst: &patched.OutputCostPerTokenAbove200kTokensPriority, src: override.OutputCostPerTokenAbove200kTokensPriority},
		{dst: &patched.InputCostPerTokenAbove272kTokens, src: override.InputCostPerTokenAbove272kTokens},
		{dst: &patched.InputCostPerTokenAbove272kTokensPriority, src: override.InputCostPerTokenAbove272kTokensPriority},
		{dst: &patched.InputCostPerTokenFlexAbove272kTokens, src: override.InputCostPerTokenFlexAbove272kTokens},
		{dst: &patched.OutputCostPerTokenAbove272kTokens, src: override.OutputCostPerTokenAbove272kTokens},
		{dst: &patched.OutputCostPerTokenAbove272kTokensPriority, src: override.OutputCostPerTokenAbove272kTokensPriority},
		{dst: &patched.OutputCostPerTokenFlexAbove272kTokens, src: override.OutputCostPerTokenFlexAbove272kTokens},
		{dst: &patched.CacheCreationInputTokenCostAbove200kTokens, src: override.CacheCreationInputTokenCostAbove200kTokens},
		{dst: &patched.CacheReadInputTokenCostAbove200kTokens, src: override.CacheReadInputTokenCostAbove200kTokens},
		{dst: &patched.CacheReadInputTokenCost, src: override.CacheReadInputTokenCost},
		{dst: &patched.CacheCreationInputTokenCost, src: override.CacheCreationInputTokenCost},
		{dst: &patched.CacheCreationInputTokenCostAbove1hr, src: override.CacheCreationInputTokenCostAbove1hr},
		{dst: &patched.CacheCreationInputTokenCostAbove1hrAbove200kTokens, src: override.CacheCreationInputTokenCostAbove1hrAbove200kTokens},
		{dst: &patched.CacheCreationInputAudioTokenCost, src: override.CacheCreationInputAudioTokenCost},
		{dst: &patched.CacheReadInputTokenCostPriority, src: override.CacheReadInputTokenCostPriority},
		{dst: &patched.CacheReadInputTokenCostFlex, src: override.CacheReadInputTokenCostFlex},
		{dst: &patched.CacheReadInputTokenCostAbove200kTokensPriority, src: override.CacheReadInputTokenCostAbove200kTokensPriority},
		{dst: &patched.CacheReadInputTokenCostAbove272kTokens, src: override.CacheReadInputTokenCostAbove272kTokens},
		{dst: &patched.CacheReadInputTokenCostAbove272kTokensPriority, src: override.CacheReadInputTokenCostAbove272kTokensPriority},

		{dst: &patched.CacheReadInputTokenCostFlexAbove272kTokens, src: override.CacheReadInputTokenCostFlexAbove272kTokens},
		{dst: &patched.CacheCreationInputTokenCostAbove272kTokens, src: override.CacheCreationInputTokenCostAbove272kTokens},
		{dst: &patched.CacheCreationInputTokenCostFlex, src: override.CacheCreationInputTokenCostFlex},
		{dst: &patched.CacheCreationInputTokenCostFlexAbove272kTokens, src: override.CacheCreationInputTokenCostFlexAbove272kTokens},
		{dst: &patched.CacheCreationInputTokenCostPriority, src: override.CacheCreationInputTokenCostPriority},
		{dst: &patched.CacheCreationInputTokenCostFast, src: override.CacheCreationInputTokenCostFast},
		{dst: &patched.CacheCreationInputTokenCostAbove1hrFast, src: override.CacheCreationInputTokenCostAbove1hrFast},
		{dst: &patched.CacheReadInputTokenCostFast, src: override.CacheReadInputTokenCostFast},
		{dst: &patched.InferenceGeoUSMultiplier, src: override.InferenceGeoUSMultiplier},

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
		{dst: &patched.CostPerRequest, src: override.CostPerRequest},
		{dst: &patched.OutputCostPerImageLowQuality, src: override.OutputCostPerImageLowQuality},
		{dst: &patched.OutputCostPerImageMediumQuality, src: override.OutputCostPerImageMediumQuality},
		{dst: &patched.OutputCostPerImageHighQuality, src: override.OutputCostPerImageHighQuality},
		{dst: &patched.OutputCostPerImageAutoQuality, src: override.OutputCostPerImageAutoQuality},
		{dst: &patched.OCRCostPerPage, src: override.OCRCostPerPage},
		{dst: &patched.AnnotationCostPerPage, src: override.AnnotationCostPerPage},
	} {
		if field.src != nil {
			*field.dst = field.src
		}
	}
	return patched
}

// LoadOverridesFromStore reloads all overrides from the config store. Called
// at bootstrap and after force-reload paths.
func (s *Store) LoadOverridesFromStore(ctx context.Context) error {
	if s.configStore == nil {
		return nil
	}
	rows, err := s.configStore.GetPricingOverrides(ctx, configstore.PricingOverrideFilters{})
	if err != nil {
		return err
	}
	return s.SetOverrides(rows)
}

// SetOverrides replaces the full in-memory override set. Duplicate IDs in
// the input keep the last-seen entry (matching today's behavior).
func (s *Store) SetOverrides(rows []configstoreTables.TablePricingOverride) error {
	seen := make(map[string]int, len(rows))
	overrides := make([]Override, 0, len(rows))
	for i := range rows {
		o, err := convertTableOverride(&rows[i])
		if err != nil {
			return err
		}
		if idx, exists := seen[o.ID]; exists {
			overrides[idx] = o
		} else {
			seen[o.ID] = len(overrides)
			overrides = append(overrides, o)
		}
	}
	s.overridesMu.Lock()
	s.rawOverrides = overrides
	s.customPricing = buildCustomPricingData(overrides)
	s.overridesMu.Unlock()
	return nil
}

// UpsertOverrides inserts or replaces one or more overrides, rebuilding the
// lookup map exactly once at the end.
func (s *Store) UpsertOverrides(rows ...*configstoreTables.TablePricingOverride) error {
	seenIncoming := make(map[string]int, len(rows))
	overrides := make([]Override, 0, len(rows))
	for _, row := range rows {
		o, err := convertTableOverride(row)
		if err != nil {
			return err
		}
		if idx, exists := seenIncoming[o.ID]; exists {
			overrides[idx] = o
		} else {
			seenIncoming[o.ID] = len(overrides)
			overrides = append(overrides, o)
		}
	}

	s.overridesMu.Lock()
	defer s.overridesMu.Unlock()

	updated := make([]Override, 0, len(s.rawOverrides)+len(overrides))
	for _, o := range s.rawOverrides {
		if _, replacing := seenIncoming[o.ID]; !replacing {
			updated = append(updated, o)
		}
	}
	updated = append(updated, overrides...)
	s.rawOverrides = updated
	s.customPricing = buildCustomPricingData(updated)
	return nil
}

// DeleteOverride removes an override by ID.
func (s *Store) DeleteOverride(id string) {
	s.overridesMu.Lock()
	defer s.overridesMu.Unlock()
	updated := make([]Override, 0, len(s.rawOverrides))
	for _, o := range s.rawOverrides {
		if o.ID != id {
			updated = append(updated, o)
		}
	}
	s.rawOverrides = updated
	s.customPricing = buildCustomPricingData(updated)
}
