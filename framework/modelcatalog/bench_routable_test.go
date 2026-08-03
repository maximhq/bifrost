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
