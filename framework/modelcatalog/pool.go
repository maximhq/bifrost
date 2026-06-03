// Editing the model pool. These methods are the push surface server.go
// orchestrates against — fetched list-models responses go into live,
// configstore key edits go into keyconfig, and the composed pool is what
// the read methods in models.go return.
package modelcatalog

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// UpsertLive caches one (provider, keyID, unfiltered) list-models response.
func (mc *ModelCatalog) UpsertLive(provider schemas.ModelProvider, keyID string, unfiltered bool, models []string) {
	mc.live.Upsert(provider, keyID, unfiltered, models)
}

// UpsertLiveFromResponse extracts model IDs from a BifrostListModelsResponse
// (parsing "provider/model" prefixes, filtering by provider match,
// deduplicating) and pushes them into the live cache. A nil resp is a no-op
// so callers can't accidentally clear an existing cache entry by handing in
// a missing response.
func (mc *ModelCatalog) UpsertLiveFromResponse(provider schemas.ModelProvider, keyID string, unfiltered bool, resp *schemas.BifrostListModelsResponse) {
	if resp == nil {
		return
	}
	mc.live.Upsert(provider, keyID, unfiltered, extractModelIDs(resp, provider))
}

// InvalidateLive drops both filtered + unfiltered live entries for one key.
func (mc *ModelCatalog) InvalidateLive(provider schemas.ModelProvider, keyID string) {
	mc.live.Invalidate(provider, keyID)
}

// InvalidateLiveProvider drops all live entries for a provider.
func (mc *ModelCatalog) InvalidateLiveProvider(provider schemas.ModelProvider) {
	mc.live.InvalidateProvider(provider)
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

// extractModelIDs flattens a list-models response into bare model
// identifiers, filtering entries whose ID prefix doesn't match the
// requested provider.
func extractModelIDs(resp *schemas.BifrostListModelsResponse, provider schemas.ModelProvider) []string {
	if resp == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(resp.Data))
	out := make([]string, 0, len(resp.Data))
	for _, m := range resp.Data {
		parsedProvider, parsedModel := schemas.ParseModelString(m.ID, "")
		if parsedProvider != "" && parsedProvider != provider {
			continue
		}
		if _, ok := seen[parsedModel]; ok {
			continue
		}
		seen[parsedModel] = struct{}{}
		out = append(out, parsedModel)
	}
	return out
}
