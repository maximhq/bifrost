package modelcatalog

import (
	"fmt"
	"slices"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/modelcatalog/keyconfig"
)

// providersWithPartialListModels enumerates providers whose /v1/models response
// is a strict subset of their callable catalog — Perplexity lists only
// responses-API models and omits the Sonar chat family, which is still callable
// via /chat/completions. For these, the datasheet (not the live list) is the
// authoritative superset, so live must be unioned with the full allowed
// datasheet set rather than only the deprecated backfill.
var providersWithPartialListModels = map[schemas.ModelProvider]bool{
	schemas.Perplexity: true,
	schemas.Vertex:     true,
}

// GetModelsForProvider returns the effective allowed model set for the
// provider across all of its keys. Filtered live entries are authoritative when
// present (they were pre-gated by ListModelsPipeline against the key's
// allow/block/aliases); otherwise the datasheet view is filtered by the
// keyconfig aggregates.
func (mc *ModelCatalog) GetModelsForProvider(provider schemas.ModelProvider) []string {
	return mc.modelsForProvider(provider, nil)
}

// modelsForProvider is GetModelsForProvider optionally narrowed to a subset of
// the provider's keys. An empty keyIDs means every key — the provider-wide view
// routing asks for. A non-empty keyIDs restricts all three inputs (live
// entries, the allow/block gate, and the per-key alias and allow additions) to
// those keys, so a caller already pinned to one key is never told about models
// only a sibling key may serve.
func (mc *ModelCatalog) modelsForProvider(provider schemas.ModelProvider, keyIDs []string) []string {
	allowed, blacklisted := mc.keyconf.GatesForKeys(provider, keyIDs)

	var out []string
	if liveModels := mc.liveModelIDs(provider, keyIDs, false); len(liveModels) > 0 {
		out = liveModels
		// Datasheet models to reconcile on top of the live list: normally just
		// deprecated ones (dropped from list-models but still callable). For
		// providers whose list-models is a partial subset, use the full
		// datasheet so callable-but-unlisted models (e.g. Perplexity's Sonar
		// chat family) aren't shadowed by the incomplete live list.
		datasheetModelsToAppend := mc.datasheet.DeprecatedDatasheetModelsForProvider(provider)
		if providersWithPartialListModels[provider] {
			datasheetModelsToAppend = mc.datasheet.DatasheetModelsForProvider(provider)
		}
		out = mc.appendAllowedDatasheetModels(out, datasheetModelsToAppend, allowed, blacklisted)
	} else if datasheetModels := mc.datasheet.DatasheetModelsForProvider(provider); len(datasheetModels) > 0 && allowed != nil {
		out = make([]string, 0, len(datasheetModels))
		for _, m := range datasheetModels {
			if blacklisted.IsBlocked(m) {
				continue
			}
			if allowed.IsAllowed(m) {
				out = append(out, m)
			}
		}
	} else {
		out = []string{}
	}

	seen := make(map[string]struct{}, len(out))
	for _, m := range out {
		seen[m] = struct{}{}
	}
	for _, e := range mc.keyEntries(provider, keyIDs) {
		if !e.Enabled {
			continue
		}
		for alias := range e.Aliases {
			if blacklisted.IsBlocked(alias) {
				continue
			}
			if allowed == nil || !allowed.IsAllowed(alias) {
				continue
			}
			if _, ok := seen[alias]; ok {
				continue
			}
			seen[alias] = struct{}{}
			out = append(out, alias)
		}
		for _, m := range e.Allowed {
			if m == "*" || blacklisted.IsBlocked(m) {
				continue
			}
			if _, ok := seen[m]; ok {
				continue
			}
			seen[m] = struct{}{}
			out = append(out, m)
		}
	}
	return out
}

func (mc *ModelCatalog) appendAllowedDatasheetModels(out []string, models []string, allowed schemas.WhiteList, blacklisted schemas.BlackList) []string {
	if len(models) == 0 {
		return out
	}
	seen := make(map[string]struct{}, len(out))
	for _, m := range out {
		seen[m] = struct{}{}
	}
	for _, m := range models {
		if _, ok := seen[m]; ok {
			continue
		}
		if blacklisted.IsBlocked(m) {
			continue
		}
		if allowed != nil && !allowed.IsAllowed(m) {
			continue
		}
		seen[m] = struct{}{}
		out = append(out, m)
	}
	slices.Sort(out)
	return out
}

// liveModelIDs returns the live routing view for the provider, narrowed to
// keyIDs when non-empty. Split out so the scoped and provider-wide compositions
// read the live store through one place.
func (mc *ModelCatalog) liveModelIDs(provider schemas.ModelProvider, keyIDs []string, unfiltered bool) []string {
	if len(keyIDs) == 0 {
		if unfiltered {
			return mc.live.UnfilteredModelsForProvider(provider)
		}
		return mc.live.ModelsForProvider(provider)
	}
	return mc.live.IDsForKeys(provider, keyIDs, unfiltered)
}

// keyEntries returns the provider's key entries, narrowed to keyIDs when
// non-empty. Disabled keys are left in: callers apply their own Enabled check,
// matching EntriesFor's contract.
func (mc *ModelCatalog) keyEntries(provider schemas.ModelProvider, keyIDs []string) []keyconfig.KeyEntry {
	entries := mc.keyconf.EntriesFor(provider)
	if len(keyIDs) == 0 || len(entries) == 0 {
		return entries
	}
	want := make(map[string]struct{}, len(keyIDs))
	for _, id := range keyIDs {
		want[id] = struct{}{}
	}
	out := make([]keyconfig.KeyEntry, 0, len(keyIDs))
	for _, e := range entries {
		if _, ok := want[e.KeyID]; ok {
			out = append(out, e)
		}
	}
	return out
}

// GetUnfilteredModelsForProvider returns the raw catalog view (no gate
// applied): union of live unfiltered entries and the datasheet view.
func (mc *ModelCatalog) GetUnfilteredModelsForProvider(provider schemas.ModelProvider) []string {
	return mc.unfilteredModelsForProvider(provider, nil)
}

// unfilteredModelsForProvider is GetUnfilteredModelsForProvider optionally
// narrowed to a subset of keys. Only the live half narrows: the datasheet is
// not key-derived, and "unfiltered" means exactly that no key gate is applied.
// Scoping still matters here because two keys on the same provider can point at
// different upstreams (different base_url, region, or deployment), so their raw
// catalogs are not interchangeable.
func (mc *ModelCatalog) unfilteredModelsForProvider(provider schemas.ModelProvider, keyIDs []string) []string {
	liveModels := mc.liveModelIDs(provider, keyIDs, true)
	datasheetModels := mc.datasheet.DatasheetModelsForProvider(provider)
	if len(liveModels) == 0 {
		return datasheetModels
	}
	if len(datasheetModels) == 0 {
		return liveModels
	}
	seen := make(map[string]struct{}, len(liveModels)+len(datasheetModels))
	out := make([]string, 0, len(liveModels)+len(datasheetModels))
	for _, m := range liveModels {
		if _, ok := seen[m]; !ok {
			seen[m] = struct{}{}
			out = append(out, m)
		}
	}
	for _, m := range datasheetModels {
		if _, ok := seen[m]; !ok {
			seen[m] = struct{}{}
			out = append(out, m)
		}
	}
	slices.Sort(out)
	return out
}

// GetDistinctBaseModelNames returns all unique base model names from the
// datasheet. Used by governance for cross-provider model selection.
func (mc *ModelCatalog) GetDistinctBaseModelNames() []string {
	return mc.datasheet.DistinctBaseModelNames()
}

// GetProvidersForModel returns every provider that can serve the model.
// Composes across stores and applies the cross-provider special cases
// (openrouter / vertex / groq-gpt / bedrock-claude) preserved verbatim from
// the pre-refactor implementation.
func (mc *ModelCatalog) GetProvidersForModel(model string) []schemas.ModelProvider {
	baseModel := mc.datasheet.BaseModelName(model)

	providers := make([]schemas.ModelProvider, 0)
	seen := make(map[schemas.ModelProvider]struct{})
	for _, p := range mc.knownProviders() {
		models := mc.GetModelsForProvider(p)
		matched := false
		for _, m := range models {
			if m == model || mc.datasheet.BaseModelName(m) == baseModel {
				matched = true
				break
			}
		}
		if matched {
			if _, ok := seen[p]; !ok {
				providers = append(providers, p)
				seen[p] = struct{}{}
			}
		}
	}

	// Cross-provider special cases
	if _, ok := seen[schemas.OpenRouter]; !ok {
		openRouterModels := mc.GetModelsForProvider(schemas.OpenRouter)
		for _, p := range providers {
			if slices.Contains(openRouterModels, string(p)+"/"+model) {
				providers = append(providers, schemas.OpenRouter)
				seen[schemas.OpenRouter] = struct{}{}
				break
			}
		}
	}
	if _, ok := seen[schemas.Vertex]; !ok {
		vertexModels := mc.GetModelsForProvider(schemas.Vertex)
		for _, p := range providers {
			if slices.Contains(vertexModels, string(p)+"/"+model) {
				providers = append(providers, schemas.Vertex)
				seen[schemas.Vertex] = struct{}{}
				break
			}
		}
	}
	if _, ok := seen[schemas.Groq]; !ok && strings.Contains(model, "gpt-") {
		if slices.Contains(mc.GetModelsForProvider(schemas.Groq), "openai/"+model) {
			providers = append(providers, schemas.Groq)
		}
	}
	if _, ok := seen[schemas.Bedrock]; !ok && strings.Contains(model, "claude") {
		for _, bedrockModel := range mc.GetModelsForProvider(schemas.Bedrock) {
			if strings.Contains(bedrockModel, model) {
				providers = append(providers, schemas.Bedrock)
				break
			}
		}
	}

	for _, p := range mc.keyconf.Providers() {
		if _, ok := seen[p]; ok {
			continue
		}
		if mc.keyconf.BlacklistedFor(p).IsBlocked(model) {
			continue
		}
		allowed := mc.keyconf.AllowedFor(p)
		matched := false
		if _, hit := mc.keyconf.ResolveAlias(p, model); hit && allowed.IsAllowed(model) {
			matched = true
		} else if allowed.Contains(model) {
			matched = true
		} else if allowed.IsUnrestricted() &&
			len(mc.datasheet.DatasheetModelsForProvider(p)) == 0 &&
			len(mc.live.UnfilteredModelsForProvider(p)) == 0 {
			matched = true
		}
		if matched {
			providers = append(providers, p)
			seen[p] = struct{}{}
		}
	}

	return providers
}

// IsModelAllowedForProvider checks whether the model is allowed for the
// provider given an explicit allowedModels list (used by VK governance
// checks, not by the static keyconfig allow set).
//
//   - allowedModels=["*"]: defer to GetProvidersForModel (with custom-provider
//     fast path when list-models is disabled).
//   - allowedModels=[]: deny-by-default.
//   - explicit allowedModels: direct or provider-prefixed match against the
//     provider's catalog.
func (mc *ModelCatalog) IsModelAllowedForProvider(provider schemas.ModelProvider, model string, providerConfig *configstore.ProviderConfig, allowedModels schemas.WhiteList) bool {
	isCustomProvider := false
	hasListModelsEndpointDisabled := false
	if providerConfig != nil && providerConfig.CustomProviderConfig != nil {
		isCustomProvider = true
		hasListModelsEndpointDisabled = !providerConfig.CustomProviderConfig.IsOperationAllowed(schemas.ListModelsRequest)
	}

	if allowedModels.IsUnrestricted() {
		if isCustomProvider && hasListModelsEndpointDisabled {
			return true
		}
		return slices.Contains(mc.GetProvidersForModel(model), provider)
	}
	if allowedModels.IsEmpty() {
		return false
	}

	providerCatalogModels := mc.GetModelsForProvider(provider)
	for _, allowedModel := range allowedModels {
		if allowedModel == model {
			return true
		}
		if strings.Contains(allowedModel, "/") {
			if slices.Contains(providerCatalogModels, allowedModel) {
				_, modelPart := schemas.ParseModelString(allowedModel, "")
				if modelPart == model {
					return true
				}
			}
		}
	}
	return false
}

func (mc *ModelCatalog) GetBaseModelName(model string) string {
	return mc.datasheet.BaseModelName(model)
}

func (mc *ModelCatalog) IsSameModel(model1, model2 string) bool {
	return mc.datasheet.IsSameModel(model1, model2)
}

// RefineModelForProvider refines a model identifier for providers whose
// catalog names carry a leading "provider/" segment (Groq, Replicate,
// Perplexity, OpenRouter), resolving a bare request like "gpt-oss-120b" to
// the provider's catalog slug ("openai/gpt-oss-120b"). Returns the original
// model unchanged when no refinement applies, or an error when multiple
// catalog candidates match ambiguously.
//
// Idempotent: a model that already carries a known provider prefix is the
// refined form itself — routing plugins may refine the same request more
// than once, so it is returned unchanged without a catalog scan. The one
// exception is a model carrying the TARGET provider's own prefix (e.g. a
// fallback entry built from another provider's refined form, or a
// double-prefixed input): the prefix is stripped and the bare remainder
// re-refined, so canonical own-prefixed names ("perplexity/sonar",
// "openrouter/auto") round-trip through the catalog scan unchanged.
func (mc *ModelCatalog) RefineModelForProvider(provider schemas.ModelProvider, model string) (string, error) {
	if prefixProvider, modelPart := schemas.ParseModelString(model, ""); prefixProvider != "" {
		if prefixProvider == provider {
			return mc.RefineModelForProvider(provider, modelPart)
		}
		return model, nil
	}
	switch provider {
	case schemas.Groq, schemas.Replicate, schemas.Perplexity, schemas.OpenRouter:
		return mc.refineNestedProviderModel(provider, model)
	}
	return model, nil
}

// refineNestedProviderModel resolves provider-native model slugs such as
// "openai/gpt-5-nano" from a base model request like "gpt-5-nano". Only
// considers catalog entries whose leading segment is a known Bifrost
// provider so Replicate owner/model identifiers like "meta/llama-3-8b" are
// left untouched.
func (mc *ModelCatalog) refineNestedProviderModel(provider schemas.ModelProvider, model string) (string, error) {
	models := mc.GetModelsForProvider(provider)
	if len(models) == 0 {
		return model, nil
	}

	candidateModels := make([]string, 0)
	seenCandidates := make(map[string]struct{})
	for _, poolModel := range models {
		providerPart, modelPart := schemas.ParseModelString(poolModel, "")
		if providerPart == "" || model != modelPart {
			continue
		}
		candidate := string(providerPart) + "/" + modelPart
		if _, seen := seenCandidates[candidate]; seen {
			continue
		}
		seenCandidates[candidate] = struct{}{}
		candidateModels = append(candidateModels, candidate)
	}

	switch len(candidateModels) {
	case 0:
		return model, nil
	case 1:
		return candidateModels[0], nil
	default:
		return "", fmt.Errorf("multiple compatible models found for model %s: %v", model, candidateModels)
	}
}
