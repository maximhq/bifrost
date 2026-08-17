package modelcatalog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
	"github.com/maximhq/bifrost/framework/modelcatalog/keyconfig"
	"github.com/maximhq/bifrost/framework/modelcatalog/live"
)

// benchCatalog builds a catalog at roughly production scale: nProviders each
// serving nModels, with versioned model ids so BaseModelName does real work.
// Every provider gets an unrestricted ["*"] allowlist so IsModelAllowedForProvider
// takes the GetProvidersForModel path (the hot path in governance/loadbalancer).
func benchCatalog(tb testing.TB, nProviders, nModels int) (*ModelCatalog, []string) {
	tb.Helper()

	var sb strings.Builder
	sb.WriteString("{\n")
	var models []string
	first := true
	kcSnapshot := map[schemas.ModelProvider][]schemas.Key{}
	for p := 0; p < nProviders; p++ {
		provider := fmt.Sprintf("provider%02d", p)
		for m := 0; m < nModels; m++ {
			base := fmt.Sprintf("model-%02d-%03d", p, m)
			// versioned id so BaseModelName -> SplitModelAndVersion does work
			id := base + "-2024-08-06"
			if !first {
				sb.WriteString(",\n")
			}
			first = false
			sb.WriteString(fmt.Sprintf(`  %q: {"provider":%q,"mode":"chat","base_model":%q}`, id, provider, base))
			models = append(models, id)
		}
		kcSnapshot[schemas.ModelProvider(provider)] = []schemas.Key{
			{ID: "k1", Enabled: ptrBool(true), Models: schemas.WhiteList{"*"}},
		}
	}
	sb.WriteString("\n}\n")

	path := filepath.Join(tb.TempDir(), "pricing.json")
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		tb.Fatalf("write pricing: %v", err)
	}
	ds := datasheet.New(nil, nil, datasheet.Config{URL: "file://" + path})
	if err := ds.LoadFromURLIntoMemory(tb.Context()); err != nil {
		tb.Fatalf("load pricing: %v", err)
	}

	kc := keyconfig.New(nil)
	kc.Replace(kcSnapshot)
	mc := &ModelCatalog{
		datasheet: ds,
		live:      live.New(nil),
		keyconf:   kc,
		done:      make(chan struct{}),
	}
	mc.initCaches()
	return mc, models
}

// rotate returns model ids to query: a small hot set (what a real workload hits
// repeatedly) rather than every distinct model once.
func benchQueryModels(all []string, n int) []string {
	if n > len(all) {
		n = len(all)
	}
	out := make([]string, n)
	// spread the picks across providers
	step := len(all) / n
	if step == 0 {
		step = 1
	}
	for i := 0; i < n; i++ {
		out[i] = all[(i*step)%len(all)]
	}
	return out
}

func BenchmarkProvidersForModel_Uncached(b *testing.B) {
	mc, all := benchCatalog(b, 15, 50)
	q := benchQueryModels(all, 8)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// computeProvidersForModel -> GetModelsForProvider, so flush modelsForProvider
		// each iteration or it stays warm and this stops measuring the pre-cache path.
		mc.modelsForProvider.Flush()
		_ = mc.computeProvidersForModel(q[i%len(q)])
	}
}

// scale sweep: uncached cost is ~O(providers × models), so it grows with the
// datasheet. Brackets where a real catalog sits.
func BenchmarkProvidersForModel_UncachedScale(b *testing.B) {
	for _, sz := range []struct{ p, m int }{{15, 50}, {20, 100}, {30, 150}, {40, 250}} {
		b.Run(fmt.Sprintf("%dx%d", sz.p, sz.m), func(b *testing.B) {
			mc, all := benchCatalog(b, sz.p, sz.m)
			q := benchQueryModels(all, 8)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// flush modelsForProvider each iteration so the compute path stays cold
				// (computeProvidersForModel -> GetModelsForProvider is otherwise memoised).
				mc.modelsForProvider.Flush()
				_ = mc.computeProvidersForModel(q[i%len(q)])
			}
		})
	}
}

func BenchmarkProvidersForModel_Cached(b *testing.B) {
	mc, all := benchCatalog(b, 15, 50)
	q := benchQueryModels(all, 8)
	// warm the memo
	for _, m := range q {
		_ = mc.GetProvidersForModel(m)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = mc.GetProvidersForModel(q[i%len(q)])
	}
}

// IsModelAllowedForProvider with ["*"] is the exact call governance and the
// loadbalancer make per request; measure it uncached vs cached.
func BenchmarkIsModelAllowed_Uncached(b *testing.B) {
	mc, all := benchCatalog(b, 15, 50)
	q := benchQueryModels(all, 8)
	unrestricted := schemas.WhiteList{"*"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		model := q[i%len(q)]
		// bust both memos each iteration to simulate the pre-cache cost:
		// IsModelAllowedForProvider -> computeProvidersForModel -> GetModelsForProvider,
		// so modelsForProvider must be flushed too or it stays warm after iteration 1.
		mc.providersForModel.Flush()
		mc.modelsForProvider.Flush()
		_ = mc.IsModelAllowedForProvider(schemas.ModelProvider("provider00"), model, nil, unrestricted)
	}
}

// GetModelsForProvider is the single-provider rebuild the explicit-list branch
// pays per call (uncached today).
func BenchmarkGetModelsForProvider(b *testing.B) {
	mc, _ := benchCatalog(b, 15, 50)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = mc.GetModelsForProvider(schemas.ModelProvider("provider00"))
	}
}

// Explicit-list IsModelAllowedForProvider: allowedModels holds a specific set,
// not ["*"]. bare-name direct match: option 1 resolves this without touching the
// catalog at all.
func BenchmarkIsModelAllowed_ExplicitList(b *testing.B) {
	mc, all := benchCatalog(b, 15, 50)
	q := benchQueryModels(all, 8)
	// realistic explicit allowlist for provider00: a handful of its own models,
	// including the ones we query.
	explicit := schemas.WhiteList(append([]string{}, q...))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = mc.IsModelAllowedForProvider(schemas.ModelProvider("provider00"), q[i%len(q)], nil, explicit)
	}
}

// bare-name allowlist, model NOT present (deny). No "/" entries, so option 1
// still never touches the catalog.
func BenchmarkIsModelAllowed_ExplicitList_Deny(b *testing.B) {
	mc, all := benchCatalog(b, 15, 50)
	explicit := schemas.WhiteList{"nope-a", "nope-b", "nope-c"}
	q := benchQueryModels(all, 8)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = mc.IsModelAllowedForProvider(schemas.ModelProvider("provider00"), q[i%len(q)], nil, explicit)
	}
}

// provider-prefixed allowlist ("provider00/model..."). This is the one explicit
// shape option 1 cannot make free: it must build the provider catalog once to
// resolve the prefix. Shows the residual cost option 2 would target.
func BenchmarkIsModelAllowed_ExplicitList_Prefixed(b *testing.B) {
	mc, all := benchCatalog(b, 15, 50)
	q := benchQueryModels(all, 8)
	prefixed := make(schemas.WhiteList, len(q))
	for i, m := range q {
		prefixed[i] = "provider00/" + m
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = mc.IsModelAllowedForProvider(schemas.ModelProvider("provider00"), q[i%len(q)], nil, prefixed)
	}
}

func BenchmarkIsModelAllowed_Cached(b *testing.B) {
	mc, all := benchCatalog(b, 15, 50)
	q := benchQueryModels(all, 8)
	unrestricted := schemas.WhiteList{"*"}
	for _, m := range q {
		_ = mc.IsModelAllowedForProvider(schemas.ModelProvider("provider00"), m, nil, unrestricted)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		model := q[i%len(q)]
		_ = mc.IsModelAllowedForProvider(schemas.ModelProvider("provider00"), model, nil, unrestricted)
	}
}
