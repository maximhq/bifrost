// Compatibility shims preserved to keep server.go and the enterprise
// transport compiling without code changes in the same commit as the
// internal refactor. Each shim mirrors the pre-refactor API by aggregating
// into a single live entry keyed by "" (empty keyID).
//
// The follow-up PR replaces the call sites with per-key fanout via
// BifrostListModelsRequest.KeyID and then DELETES THIS WHOLE FILE.
package modelcatalog

import (
	"time"

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
	mc.live.Upsert(provider, "", false, models, time.Now())
}

// UpsertUnfilteredModelDataForProvider stores the unfiltered provider
// response in a single aggregated entry.
//
// Deprecated: shim. See UpsertModelDataForProvider.
func (mc *ModelCatalog) UpsertUnfilteredModelDataForProvider(provider schemas.ModelProvider, modelData *schemas.BifrostListModelsResponse) {
	models := extractModelIDs(modelData, provider)
	mc.live.Upsert(provider, "", true, models, time.Now())
}

// DeleteModelDataForProvider drops every live entry for the provider.
//
// Deprecated: shim. Use InvalidateLiveProvider.
func (mc *ModelCatalog) DeleteModelDataForProvider(provider schemas.ModelProvider) {
	mc.live.InvalidateProvider(provider)
}

// extractModelIDs flattens a list-models response into the bare model
// identifiers the live store expects, filtering entries whose ID prefix
// doesn't match the requested provider.
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
