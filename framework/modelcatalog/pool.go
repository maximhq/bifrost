// Editing the model pool. These methods are the push surface server.go
// orchestrates against — fetched list-models responses go into live,
// configstore key edits go into keyconfig, and the composed pool is what
// the read methods in models.go return.
package modelcatalog

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// UpsertLive caches one (provider, keyID, unfiltered) entry from bare model
// names. Convenience wrapper for callers that have no provider metadata to
// store; an entry written this way carries identifiers only, so serving it back
// through the list-models cache yields models with just an ID set.
func (mc *ModelCatalog) UpsertLive(provider schemas.ModelProvider, keyID string, unfiltered bool, models []string) {
	full := make([]schemas.Model, len(models))
	for i, m := range models {
		full[i] = schemas.Model{ID: m}
	}
	// Identifiers pass through untouched: callers of this form already hold
	// routing-shaped names, and re-parsing would strip foreign prefixes that
	// cross-provider resolution depends on.
	mc.live.Upsert(provider, keyID, unfiltered, full, models)
}

// UpsertLiveFromResponse narrows a BifrostListModelsResponse to the provider's
// own models (parsing "provider/model" prefixes, filtering by provider match,
// deduplicating) and pushes them into the live cache. A nil resp is a no-op
// so callers can't accidentally clear an existing cache entry by handing in
// a missing response.
func (mc *ModelCatalog) UpsertLiveFromResponse(provider schemas.ModelProvider, keyID string, unfiltered bool, resp *schemas.BifrostListModelsResponse) {
	if resp == nil {
		return
	}
	mc.live.Upsert(provider, keyID, unfiltered, extractProviderModels(resp, provider), extractModelIDs(resp, provider))
}

// LookupLiveModels returns the cached list-models result for the provider
// across the given keys, and whether the lookup hit. It is the read side of
// schemas.ListModelsCatalog, letting the core pipeline answer a list-models
// call from cache instead of the network.
func (mc *ModelCatalog) LookupLiveModels(provider schemas.ModelProvider, keyIDs []string, unfiltered bool) ([]schemas.Model, bool) {
	return mc.live.FullModelsFor(provider, keyIDs, unfiltered)
}

// RoutableModelsForProvider returns every model the gateway would route to the
// provider through the given keys: the same composition routing resolves
// against, so a list-models response reconciled with it advertises exactly what
// will be accepted.
//
// Deliberately the composed view rather than the live entries alone. It also
// covers delisted-but-callable models, providers whose list-models is a known
// partial view, and identifiers that only key configuration knows about.
//
// keyIDs is read-only and mirrors the scoping of the call being reconciled: a
// request pinned to one key must be reconciled against that key alone, or the
// reply advertises models a sibling key's allow-list permits and this one does
// not. Empty keyIDs means the provider-wide view, which is what an unscoped
// request and the list-models failure fallback both want.
func (mc *ModelCatalog) RoutableModelsForProvider(provider schemas.ModelProvider, keyIDs []string, unfiltered bool) []string {
	if unfiltered {
		return mc.unfilteredModelsForProvider(provider, keyIDs)
	}
	return mc.modelsForProvider(provider, keyIDs)
}

// LiveGeneration returns the live cache's invalidation counter for the
// provider. Read it before issuing a list-models fetch and pass it back to
// UpsertLiveFromResponseIfCurrent when the response lands.
func (mc *ModelCatalog) LiveGeneration(provider schemas.ModelProvider) uint64 {
	return mc.live.Generation(provider)
}

// UpsertLiveFromResponseIfCurrent is UpsertLiveFromResponse with a staleness
// guard: the write is dropped, and false returned, if the provider was
// invalidated (key deleted or disabled, provider removed, entries pruned)
// after gen was read. A nil resp is a no-op and also reports false, matching
// UpsertLiveFromResponse's "never clear a cache entry with a missing
// response" contract.
func (mc *ModelCatalog) UpsertLiveFromResponseIfCurrent(provider schemas.ModelProvider, keyID string, unfiltered bool, resp *schemas.BifrostListModelsResponse, gen uint64) bool {
	if resp == nil {
		return false
	}
	return mc.live.UpsertIfCurrent(provider, keyID, unfiltered, extractProviderModels(resp, provider), extractModelIDs(resp, provider), gen)
}

// InvalidateLive drops both filtered + unfiltered live entries for one key.
func (mc *ModelCatalog) InvalidateLive(provider schemas.ModelProvider, keyID string) {
	mc.live.Invalidate(provider, keyID)
}

// InvalidateLiveProvider drops all live entries for a provider.
func (mc *ModelCatalog) InvalidateLiveProvider(provider schemas.ModelProvider) {
	mc.live.InvalidateProvider(provider)
}

// RetainLiveKeys prunes the provider's live entries down to the keys in keep,
// leaving those keys' cached models in place. Used before a re-fetch so a
// failed list-models call cannot empty a catalog that was valid a moment ago.
func (mc *ModelCatalog) RetainLiveKeys(provider schemas.ModelProvider, keep map[string]struct{}) {
	mc.live.RetainKeys(provider, keep)
}

// SetKeyConfigForProvider replaces the keyconfig snapshot for one provider.
func (mc *ModelCatalog) SetKeyConfigForProvider(provider schemas.ModelProvider, keys []schemas.Key) {
	mc.keyconf.SetProvider(provider, keys)
}

// ReplaceKeyConfig atomically resets the keyconfig snapshot for all providers.
func (mc *ModelCatalog) ReplaceKeyConfig(snapshot map[schemas.ModelProvider][]schemas.Key) {
	mc.keyconf.Replace(snapshot)
}

// RemoveKeyConfigForProvider drops keyconfig state for the provider.
func (mc *ModelCatalog) RemoveKeyConfigForProvider(provider schemas.ModelProvider) {
	mc.keyconf.RemoveProvider(provider)
}

// KeyConfigEntries returns the per-key entries for one provider (used by
// orchestration to know which keys to fan list-models calls across).
func (mc *ModelCatalog) KeyConfigEntries(provider schemas.ModelProvider) []KeyConfigEntry {
	return mc.keyconf.EntriesFor(provider)
}

// ResolveAlias returns which key owns an alias on the provider and its
// AliasConfig.
func (mc *ModelCatalog) ResolveAlias(provider schemas.ModelProvider, model string) (AliasOwner, bool) {
	return mc.keyconf.ResolveAlias(provider, model)
}

// KeysAllowingModel returns the IDs of enabled keys that can serve the model.
func (mc *ModelCatalog) KeysAllowingModel(provider schemas.ModelProvider, model string) []string {
	return mc.keyconf.KeysAllowingModel(provider, model)
}

// AllowedModelsForProvider returns the aggregated whitelist for the
// provider (union of enabled keys' Models minus per-key blacklists, or
// ["*"] when any key is unrestricted). Used by the load balancer to know
// what each provider can serve without re-walking the configstore.
func (mc *ModelCatalog) AllowedModelsForProvider(provider schemas.ModelProvider) schemas.WhiteList {
	return mc.keyconf.AllowedFor(provider)
}

// BlacklistedModelsForProvider returns the intersection of enabled keys'
// BlacklistedModels for the provider — a model is included only when every
// enabled key blacklists it.
func (mc *ModelCatalog) BlacklistedModelsForProvider(provider schemas.ModelProvider) schemas.BlackList {
	return mc.keyconf.BlacklistedFor(provider)
}

// ConfiguredProviders returns every provider with at least one entry in
// keyconfig. Used by the load balancer's provider selection where the
// configured-provider set is the routing-eligible universe.
func (mc *ModelCatalog) ConfiguredProviders() []schemas.ModelProvider {
	return mc.keyconf.Providers()
}

// extractProviderModels narrows a list-models response to the models the
// requested provider owns, dropping entries whose ID prefix names a different
// provider and collapsing duplicates by bare model name.
//
// Models are returned whole rather than as identifiers: the live store keeps
// them so a cache hit can reproduce a live /v1/models response field for
// field, and dropping to identifiers here would discard alias, owned_by,
// context_length, pricing and everything else the provider reported.
func extractProviderModels(resp *schemas.BifrostListModelsResponse, provider schemas.ModelProvider) []schemas.Model {
	if resp == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(resp.Data))
	out := make([]schemas.Model, 0, len(resp.Data))
	for _, m := range resp.Data {
		parsedProvider, parsedModel := schemas.ParseModelString(m.ID, "")
		if parsedProvider != "" && parsedProvider != provider {
			continue
		}
		if _, ok := seen[parsedModel]; ok {
			continue
		}
		seen[parsedModel] = struct{}{}
		out = append(out, m)
	}
	return out
}

// extractModelIDs is the bare-identifier projection of extractProviderModels,
// kept so callers that only need names do not have to repeat the parse.
func extractModelIDs(resp *schemas.BifrostListModelsResponse, provider schemas.ModelProvider) []string {
	models := extractProviderModels(resp, provider)
	if models == nil {
		return nil
	}
	out := make([]string, len(models))
	for i, m := range models {
		_, out[i] = schemas.ParseModelString(m.ID, "")
	}
	return out
}
