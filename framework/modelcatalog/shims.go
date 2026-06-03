// Compatibility shims preserved to keep server.go and the enterprise
// transport compiling without code changes in the same commit as the
// internal refactor. Each shim mirrors the pre-refactor API by aggregating
// into a single live entry keyed by "" (empty keyID).
//
// The follow-up PR replaces the call sites with per-key fanout via
// BifrostListModelsRequest.KeyID and then DELETES THIS WHOLE FILE.
package modelcatalog

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// UpsertModelDataForProvider stores the merged filtered response for the
// provider in a single aggregated live entry. modelsInKeys is retained as a
// fallback when modelData is empty (provider list-models failed or no keys
// configured) — matches the pre-refactor "trust the user-allowed list" path.
//
// Deprecated: shim. Use UpsertLive per key once the call site adopts
// BifrostListModelsRequest.KeyID.
func (mc *ModelCatalog) UpsertModelDataForProvider(provider schemas.ModelProvider, modelData *schemas.BifrostListModelsResponse, modelsInKeys []schemas.Model) {
	models := extractModelIDs(modelData, provider)
	if len(models) == 0 {
		models = extractModelIDs(&schemas.BifrostListModelsResponse{Data: modelsInKeys}, provider)
	}
	mc.live.Upsert(provider, "", false, models)
}

// UpsertUnfilteredModelDataForProvider stores the unfiltered provider
// response in a single aggregated entry.
//
// Deprecated: shim. See UpsertModelDataForProvider.
func (mc *ModelCatalog) UpsertUnfilteredModelDataForProvider(provider schemas.ModelProvider, modelData *schemas.BifrostListModelsResponse) {
	models := extractModelIDs(modelData, provider)
	mc.live.Upsert(provider, "", true, models)
}

// DeleteModelDataForProvider drops every live entry for the provider.
//
// Deprecated: shim. Use InvalidateLiveProvider.
func (mc *ModelCatalog) DeleteModelDataForProvider(provider schemas.ModelProvider) {
	mc.live.InvalidateProvider(provider)
}
