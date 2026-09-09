package modelcatalog

import (
	"slices"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
	"github.com/maximhq/bifrost/framework/modelcatalog/keyconfig"
	"github.com/maximhq/bifrost/framework/modelcatalog/live"
)

// TestProviderMemoInvalidatesOnWrite pins the correctness of the generation
// stamp: a store write must make GetProvidersForModel recompute, never serve a
// cached membership from before the write.
func TestProviderMemoInvalidatesOnWrite(t *testing.T) {
	kc := keyconfig.New(nil)
	kc.Replace(map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI: {{ID: "k1", Enabled: ptrBool(true), Models: schemas.WhiteList{"gpt-4o"}}},
	})
	mc := &ModelCatalog{
		datasheet: datasheet.NewTestStore(map[string]string{"gpt-4o": "gpt-4o"}),
		live:      live.New(nil),
		keyconf:   kc,
		done:      make(chan struct{}),
	}
	mc.initCaches()

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
	kc := keyconfig.New(nil)
	kc.Replace(map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI: {{ID: "k1", Enabled: ptrBool(true), Models: schemas.WhiteList{"gpt-4o"}}},
	})
	mc := &ModelCatalog{
		datasheet: datasheet.NewTestStore(map[string]string{"gpt-4o": "gpt-4o"}),
		live:      live.New(nil),
		keyconf:   kc,
		done:      make(chan struct{}),
	}
	mc.initCaches()

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
	kc := keyconfig.New(nil)
	kc.Replace(map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI:    {{ID: "k1", Enabled: ptrBool(true), Models: schemas.WhiteList{"gpt-4o"}}},
		schemas.Anthropic: {{ID: "k2", Enabled: ptrBool(true), Models: schemas.WhiteList{"gpt-4o"}}},
	})
	mc := &ModelCatalog{
		datasheet: datasheet.NewTestStore(map[string]string{"gpt-4o": "gpt-4o"}),
		live:      live.New(nil),
		keyconf:   kc,
		done:      make(chan struct{}),
	}
	mc.initCaches()

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
