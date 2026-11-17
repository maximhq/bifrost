package modelcatalog

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
)

// TestProviderMemoInvalidatesOnWrite pins the correctness of the generation
// stamp: a store write must make GetProvidersForModel recompute, never serve a
// cached membership from before the write.
func TestProviderMemoInvalidatesOnWrite(t *testing.T) {
	liveModels, kc := stateStoresForTest(t)
	kc.Replace(map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI: {{ID: "k1", Enabled: ptrBool(true), Models: schemas.WhiteList{"gpt-4o"}}},
	})
	mc := &ModelCatalog{
		datasheet: datasheet.NewTestStore(map[string]string{"gpt-4o": "gpt-4o"}),
		live:      liveModels,
		keyconf:   kc,
		done:      make(chan struct{}),
	}
	withTestCaches(t, mc)

	// Prime the memo.
	got := mc.GetProvidersForModel("gpt-4o")
	if !slices.Contains(got, schemas.OpenAI) {
		t.Fatalf("expected OpenAI before write, got %v", got)
	}
	if !slices.Contains(mc.GetProvidersForModel("gpt-4o"), schemas.OpenAI) {
		t.Fatalf("cached read lost OpenAI")
	}

	// Add a second provider that serves the same model. This is a keyconfig
	// write, which must invalidate the memo.
	kc.SetProvider(schemas.Anthropic, []schemas.Key{
		{ID: "k2", Enabled: ptrBool(true), Models: schemas.WhiteList{"gpt-4o"}},
	})

	got = mc.GetProvidersForModel("gpt-4o")
	if !slices.Contains(got, schemas.Anthropic) {
		t.Fatalf("memo served stale membership after write: got %v, want Anthropic included", got)
	}
}

// TestProviderMemoSkipsEmptyResults pins that unknown models are never cached:
// model strings are client-controlled, so caching misses would let junk names
// grow the memo.
func TestProviderMemoSkipsEmptyResults(t *testing.T) {
	liveModels, kc := stateStoresForTest(t)
	kc.Replace(map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI: {{ID: "k1", Enabled: ptrBool(true), Models: schemas.WhiteList{"gpt-4o"}}},
	})
	mc := &ModelCatalog{
		datasheet: datasheet.NewTestStore(map[string]string{"gpt-4o": "gpt-4o"}),
		live:      liveModels,
		keyconf:   kc,
		done:      make(chan struct{}),
	}
	withTestCaches(t, mc)

	if got := mc.GetProvidersForModel("junk-model-xyz"); len(got) != 0 {
		t.Fatalf("expected no providers for junk model, got %v", got)
	}
	if _, cached := mc.providersForModel.Get("junk-model-xyz"); cached {
		t.Fatalf("empty result was cached")
	}
}

// TestProviderMemoReturnsClone pins that callers can mutate the returned slice
// (the resolver sorts it in place) without corrupting the cached entry.
func TestProviderMemoReturnsClone(t *testing.T) {
	liveModels, kc := stateStoresForTest(t)
	kc.Replace(map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI:    {{ID: "k1", Enabled: ptrBool(true), Models: schemas.WhiteList{"gpt-4o"}}},
		schemas.Anthropic: {{ID: "k2", Enabled: ptrBool(true), Models: schemas.WhiteList{"gpt-4o"}}},
	})
	mc := &ModelCatalog{
		datasheet: datasheet.NewTestStore(map[string]string{"gpt-4o": "gpt-4o"}),
		live:      liveModels,
		keyconf:   kc,
		done:      make(chan struct{}),
	}
	withTestCaches(t, mc)

	first := mc.GetProvidersForModel("gpt-4o")
	if len(first) < 2 {
		t.Fatalf("expected >=2 providers, got %v", first)
	}
	// Corrupt the returned slice.
	for i := range first {
		first[i] = "MUTATED"
	}

	second := mc.GetProvidersForModel("gpt-4o")
	for _, p := range second {
		if p == "MUTATED" {
			t.Fatalf("caller mutation leaked into cached entry: %v", second)
		}
	}
}

// TestMaxOutputTokensMemo pins the output-token cap memo: a hit and a miss are
// both cached (a miss costs a full-sheet capability scan, which is too slow to
// repeat per request), and a datasheet write drops every entry so a sync that
// adds or changes a cap is seen on the next request.
func TestMaxOutputTokensMemo(t *testing.T) {
	ds := datasheet.NewTestStore(nil)
	ds.SetPricingRowsForTest([]configstoreTables.TableModelPricing{
		{Model: "gemini-test-flash", Provider: string(schemas.Gemini), Mode: "chat", MaxOutputTokens: new(65536)},
	})
	mc := withTestCaches(t, catalogWithDatasheetForTest(t, ds))

	if got := mc.GetMaxOutputTokens("gemini-test-flash", schemas.Gemini); got != 65536 {
		t.Fatalf("GetMaxOutputTokens(hit) = %d, want 65536", got)
	}
	if got := mc.GetMaxOutputTokens("gemini-new-flash", schemas.Gemini); got != 0 {
		t.Fatalf("GetMaxOutputTokens(miss) = %d, want 0", got)
	}
	for _, model := range []string{"gemini-test-flash", "gemini-new-flash"} {
		if _, cached := mc.maxOutputTokens.Get(livestore.EncodeKey(string(schemas.Gemini), model)); !cached {
			t.Errorf("%s was not cached", model)
		}
	}

	// A sync adds the missing model and changes the known cap.
	ds.SetPricingRowsForTest([]configstoreTables.TableModelPricing{
		{Model: "gemini-test-flash", Provider: string(schemas.Gemini), Mode: "chat", MaxOutputTokens: new(32768)},
		{Model: "gemini-new-flash", Provider: string(schemas.Gemini), Mode: "chat", MaxOutputTokens: new(8192)},
	})
	if got := mc.GetMaxOutputTokens("gemini-test-flash", schemas.Gemini); got != 32768 {
		t.Errorf("after sync GetMaxOutputTokens(gemini-test-flash) = %d, want 32768 (stale cap served)", got)
	}
	if got := mc.GetMaxOutputTokens("gemini-new-flash", schemas.Gemini); got != 8192 {
		t.Errorf("after sync GetMaxOutputTokens(gemini-new-flash) = %d, want 8192 (stale miss served)", got)
	}
}

// TestCatalogMemosKeepUnlistedSpellingsBriefly pins the bound on the memos keyed by a request's
// model name. A version suffix strips to the listed base model, so every gpt-4o-v<N> gets gpt-4o's
// providers, output-token cap and capabilities; kept for the generation, a caller inventing
// spellings would grow memory without bound. Each such answer must expire within
// catalogNegativeTTL, while gpt-4o, which the catalog lists, keeps its answers with no expiry, and
// every spelling still gets the same answers.
func TestCatalogMemosKeepUnlistedSpellingsBriefly(t *testing.T) {
	ds := datasheet.NewTestStore(nil)
	ds.SetPricingRowsForTest([]configstoreTables.TableModelPricing{
		{Model: "gpt-4o", BaseModel: "gpt-4o", Provider: string(schemas.OpenAI), Mode: "chat", MaxOutputTokens: new(16384)},
	})
	mc := withTestCaches(t, catalogWithDatasheetForTest(t, ds))
	mc.SetKeyConfigForProvider(schemas.OpenAI, []schemas.Key{{ID: "k1", Enabled: ptrBool(true), Models: schemas.WhiteList{"gpt-4o"}}})
	yes := true
	mc.loadCapabilities = func(p schemas.ModelProvider, m string) (*schemas.ModelCapabilities, error) {
		// The datasheet loader also falls back to the base model's row.
		if p == schemas.OpenAI && mc.GetBaseModelName(m) == "gpt-4o" {
			return &schemas.ModelCapabilities{SupportsFastMode: &yes}, nil
		}
		return nil, nil
	}

	// resolve checks every memo answers name as it answers gpt-4o, and returns, per memo, whether
	// its entry for name is kept for the generation (true) or expires within catalogNegativeTTL
	// (false).
	resolve := func(name string) [3]bool {
		t.Helper()
		if got := mc.GetProvidersForModel(name); !slices.Equal(got, []schemas.ModelProvider{schemas.OpenAI}) {
			t.Fatalf("GetProvidersForModel(%s) = %v, want [openai]", name, got)
		}
		if got := mc.GetMaxOutputTokens(name, schemas.OpenAI); got != 16384 {
			t.Fatalf("GetMaxOutputTokens(%s) = %d, want 16384", name, got)
		}
		if got := mc.GetModelCapabilities(schemas.OpenAI, name); got == nil || got.SupportsFastMode == nil || !*got.SupportsFastMode {
			t.Fatalf("GetModelCapabilities(%s) = %+v, want the gpt-4o record", name, got)
		}
		deadline := time.Now().Add(catalogNegativeTTL)
		key := livestore.EncodeKey(string(schemas.OpenAI), name)
		var kept [3]bool
		for i, expiry := range []func() (time.Time, bool){
			func() (time.Time, bool) { return mc.providersForModel.Expiry(name) },
			func() (time.Time, bool) { return mc.maxOutputTokens.Expiry(key) },
			func() (time.Time, bool) { return mc.capabilities.Expiry(key) },
		} {
			at, expires := expiry()
			if expires && at.After(deadline) {
				t.Fatalf("memo %d keeps %s until %v, past catalogNegativeTTL", i, name, at)
			}
			kept[i] = !expires
		}
		_, inProviders := mc.providersForModel.Get(name)
		_, inTokens := mc.maxOutputTokens.Get(key)
		_, inCapabilities := mc.capabilities.Get(key)
		if !inProviders || !inTokens || !inCapabilities {
			t.Fatalf("an answer for %s was not kept at all: providers %v, tokens %v, capabilities %v", name, inProviders, inTokens, inCapabilities)
		}
		return kept
	}

	memos := [3]string{"providers-for-model", "max output tokens", "capabilities"}
	var forever [3]int
	for i := 1; i <= 100; i++ {
		for m, kept := range resolve(fmt.Sprintf("gpt-4o-v%d", i)) {
			if kept {
				forever[m]++
			}
		}
	}
	for m, n := range forever {
		if n != 0 {
			t.Errorf("%s memo keeps %d of 100 gpt-4o-v<N> spellings with no expiry; want each to expire within catalogNegativeTTL", memos[m], n)
		}
	}
	for m, kept := range resolve("gpt-4o") {
		if !kept {
			t.Errorf("%s memo gives gpt-4o, which the catalog lists, an expiry; want it kept for the generation", memos[m])
		}
	}
}

// clearTable drops every entry of tb, so a benchmark or test starts from a cold catalog table.
func clearTable[K comparable, V any](tb *livestore.Table[K, V]) {
	tb.Range(func(k K, _ V) bool {
		tb.Delete(k)
		return true
	})
}
