package modelcatalog

import (
	"fmt"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// buildCatalog mimics the deployment shape from issue #5552: providers x keys,
// each key holding a provider catalog of modelsPerKey entries.
func buildCatalog(providers, keysPerProvider, modelsPerKey int) (*ModelCatalog, []schemas.ModelProvider) {
	mc := NewTestCatalog(nil)
	names := make([]schemas.ModelProvider, providers)
	for p := range providers {
		provider := schemas.ModelProvider(fmt.Sprintf("provider-%d", p))
		names[p] = provider
		keys := make([]schemas.Key, keysPerProvider)
		models := make([]string, modelsPerKey)
		for m := range modelsPerKey {
			models[m] = fmt.Sprintf("model-%d-%d", p, m)
		}
		for k := range keysPerProvider {
			keyID := fmt.Sprintf("key-%d-%d", p, k)
			keys[k] = schemas.Key{ID: keyID, Models: schemas.WhiteList{"*"}}
			mc.UpsertLive(provider, keyID, false, models)
			mc.UpsertLive(provider, keyID, true, models)
		}
		mc.SetKeyConfigForProvider(provider, keys)
	}
	return mc, names
}

// One provider's routable lookup, which is what reconciliation costs per
// provider on a /v1/models request.
func BenchmarkRoutableModelsForProvider(b *testing.B) {
	for _, sz := range []struct{ providers, keys, models int }{
		{4, 2, 50},
		{16, 2, 100},
		{16, 30, 100},
		{40, 5, 300},
	} {
		name := fmt.Sprintf("providers=%d/keys=%d/models=%d", sz.providers, sz.keys, sz.models)
		b.Run(name, func(b *testing.B) {
			mc, names := buildCatalog(sz.providers, sz.keys, sz.models)
			b.ResetTimer()
			for range b.N {
				_ = mc.RoutableModelsForProvider(names[0], nil, false)
			}
		})
	}
}

// GetModelInfo backs ctx.GetModelInfo, which a plugin may call per request
// rather than per /v1/models call, so its cost matters at a different rate to
// everything else here.
//
// The live half is a scan, and its worst case is a miss - which is exactly the
// hit case for the datasheet. "miss" below is therefore the number to watch: it
// walks every entry of every provider before falling through. If it ever gets
// expensive, the fix is a per-provider model index on live.Store, not a
// reordering, since the merge order is what keeps this agreeing with /v1/models.
func BenchmarkGetModelInfo(b *testing.B) {
	for _, sz := range []struct{ providers, keys, models int }{
		{16, 2, 100},
		{16, 30, 100},
		{40, 5, 300},
	} {
		mc, names := buildCatalog(sz.providers, sz.keys, sz.models)
		provider := names[len(names)-1]
		cases := []struct{ name, model string }{
			// Cached by this provider: the scan exits on a match.
			{"live-hit", fmt.Sprintf("model-%d-0", len(names)-1)},
			// Nowhere in the catalog: the full scan, then a datasheet miss.
			{"miss", "no-such-model"},
		}
		for _, tc := range cases {
			name := fmt.Sprintf("providers=%d/keys=%d/models=%d/%s", sz.providers, sz.keys, sz.models, tc.name)
			b.Run(name, func(b *testing.B) {
				b.ResetTimer()
				for range b.N {
					_ = mc.GetModelInfo(provider, tc.model)
				}
			})
		}
	}
}

// The whole fan-out: one RoutableModels call per provider, which is what a
// single GET /v1/models with no provider param performs.
func BenchmarkRoutableModelsFullFanout(b *testing.B) {
	for _, sz := range []struct{ providers, keys, models int }{
		{16, 2, 100},
		{16, 30, 100},
		{40, 5, 300},
	} {
		name := fmt.Sprintf("providers=%d/keys=%d/models=%d", sz.providers, sz.keys, sz.models)
		b.Run(name, func(b *testing.B) {
			mc, names := buildCatalog(sz.providers, sz.keys, sz.models)
			b.ResetTimer()
			for range b.N {
				for _, p := range names {
					_ = mc.RoutableModelsForProvider(p, nil, false)
				}
			}
		})
	}
}
