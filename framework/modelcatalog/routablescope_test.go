package modelcatalog

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
)

// The reconciliation path asks the catalog what a *specific key* can route to.
// These tests pin the three inputs that have to narrow together: the live
// entries, the allow/block gate derived from the keys, and the per-key alias
// and explicit-allow additions. Any one of them left provider-wide reopens the
// leak, because a list-models reply reconciled against a wider set is then
// cached under the narrower key's entry.

func routableScopeCatalog(t *testing.T) *ModelCatalog {
	t.Helper()
	mc := NewTestCatalog(nil)
	mc.SetKeyConfigForProvider(schemas.OpenAI, []schemas.Key{
		{ID: "k1", Models: schemas.WhiteList{"gpt-4o"}},
		{ID: "k2", Models: schemas.WhiteList{"o1"}},
	})
	return mc
}

// scopeDatasheetCatalog returns a catalog whose datasheet lists gpt-4o and o1
// for openai and whose live store is empty, so the composition falls through to
// the datasheet and the allow/block gate is what decides the answer.
func scopeDatasheetCatalog(t *testing.T, keys ...schemas.Key) *ModelCatalog {
	t.Helper()
	pricingPath := filepath.Join(t.TempDir(), "pricing.json")
	pricingJSON := []byte(`{
		"gpt-4o": {"provider":"openai","mode":"chat","base_model":"gpt-4o"},
		"o1": {"provider":"openai","mode":"chat","base_model":"o1"}
	}`)
	if err := os.WriteFile(pricingPath, pricingJSON, 0o600); err != nil {
		t.Fatalf("write pricing testdata: %v", err)
	}
	ds := datasheet.New(nil, nil, datasheet.Config{URL: "file://" + pricingPath})
	if err := ds.LoadFromURLIntoMemory(t.Context()); err != nil {
		t.Fatalf("load pricing testdata: %v", err)
	}
	mc := NewTestCatalogWithDatasheet(ds)
	mc.SetKeyConfigForProvider(schemas.OpenAI, keys)
	return mc
}

// TestRoutableModelsForProvider_ScopesLiveEntriesToKey is the base case: two
// keys, each with its own fetched catalog, must not see each other's models.
func TestRoutableModelsForProvider_ScopesLiveEntriesToKey(t *testing.T) {
	mc := routableScopeCatalog(t)
	mc.UpsertLive(schemas.OpenAI, "k1", false, []string{"gpt-4o"})
	mc.UpsertLive(schemas.OpenAI, "k2", false, []string{"o1"})

	if got := mc.RoutableModelsForProvider(schemas.OpenAI, []string{"k1"}, false); !slices.Equal(got, []string{"gpt-4o"}) {
		t.Errorf("scoped to k1 = %v, want [gpt-4o]", got)
	}
	if got := mc.RoutableModelsForProvider(schemas.OpenAI, []string{"k2"}, false); !slices.Equal(got, []string{"o1"}) {
		t.Errorf("scoped to k2 = %v, want [o1]", got)
	}
}

// An unscoped read still returns the union. Routing may pick any key, and the
// provider-level list-models call is answered from every key, so narrowing here
// would under-report rather than fix anything.
func TestRoutableModelsForProvider_UnscopedIsTheUnion(t *testing.T) {
	mc := routableScopeCatalog(t)
	mc.UpsertLive(schemas.OpenAI, "k1", false, []string{"gpt-4o"})
	mc.UpsertLive(schemas.OpenAI, "k2", false, []string{"o1"})

	got := mc.RoutableModelsForProvider(schemas.OpenAI, nil, false)
	slices.Sort(got)
	if !slices.Equal(got, []string{"gpt-4o", "o1"}) {
		t.Errorf("unscoped = %v, want the union [gpt-4o o1]", got)
	}
	// And it must still agree with the provider-wide accessor routing uses.
	if want := mc.GetModelsForProvider(schemas.OpenAI); !slices.Equal(got, want) {
		t.Errorf("unscoped = %v, want to match GetModelsForProvider %v", got, want)
	}
}

// TestRoutableModelsForProvider_ScopesTheAllowGate is the half a live-only
// narrowing would miss. With no live entry the composition falls through to the
// datasheet, gated by the allow-list — which is a *union* across keys, so
// reading it provider-wide would let k2's allowance admit o1 into k1's answer.
func TestRoutableModelsForProvider_ScopesTheAllowGate(t *testing.T) {
	mc := scopeDatasheetCatalog(t,
		schemas.Key{ID: "k1", Models: schemas.WhiteList{"gpt-4o"}},
		schemas.Key{ID: "k2", Models: schemas.WhiteList{"o1"}},
	)

	if got := mc.RoutableModelsForProvider(schemas.OpenAI, []string{"k1"}, false); !slices.Equal(got, []string{"gpt-4o"}) {
		t.Errorf("scoped to k1 = %v, want [gpt-4o]: k2's allow-list must not admit o1", got)
	}
}

// TestRoutableModelsForProvider_ScopesTheBlacklist is the mirror image. The
// provider-wide blacklist is an *intersection* — a model counts as blocked only
// when every key blocks it — so reading it provider-wide would unblock a model
// the scoped key does blacklist.
func TestRoutableModelsForProvider_ScopesTheBlacklist(t *testing.T) {
	mc := scopeDatasheetCatalog(t,
		schemas.Key{ID: "k1", Models: schemas.WhiteList{"*"}, BlacklistedModels: schemas.BlackList{"o1"}},
		schemas.Key{ID: "k2", Models: schemas.WhiteList{"*"}},
	)

	if got := mc.RoutableModelsForProvider(schemas.OpenAI, []string{"k1"}, false); slices.Contains(got, "o1") {
		t.Errorf("scoped to k1 = %v, want o1 excluded: k1 blacklists it even though k2 does not", got)
	}
	// Provider-wide, o1 is routable via k2, so the union must keep it.
	if got := mc.RoutableModelsForProvider(schemas.OpenAI, nil, false); !slices.Contains(got, "o1") {
		t.Errorf("unscoped = %v, want o1 present: k2 can still serve it", got)
	}
}

// Aliases are introduced by key configuration and exist only for the key that
// declares them, so they have to narrow with everything else.
func TestRoutableModelsForProvider_ScopesAliases(t *testing.T) {
	mc := NewTestCatalog(nil)
	mc.SetKeyConfigForProvider(schemas.OpenAI, []schemas.Key{
		{ID: "k1", Models: schemas.WhiteList{"*"}, Aliases: schemas.KeyAliases{"fast": {}}},
		{ID: "k2", Models: schemas.WhiteList{"*"}, Aliases: schemas.KeyAliases{"cheap": {}}},
	})

	got := mc.RoutableModelsForProvider(schemas.OpenAI, []string{"k1"}, false)
	if !slices.Contains(got, "fast") {
		t.Errorf("scoped to k1 = %v, want its own alias [fast]", got)
	}
	if slices.Contains(got, "cheap") {
		t.Errorf("scoped to k1 = %v, want k2's alias [cheap] excluded", got)
	}
}

// The unfiltered view applies no key gate by definition, but two keys on one
// provider can point at different upstreams (base_url, region, deployment), so
// their raw catalogs are not interchangeable and the live half still narrows.
func TestRoutableModelsForProvider_UnfilteredStillScopesLiveEntries(t *testing.T) {
	mc := routableScopeCatalog(t)
	mc.UpsertLive(schemas.OpenAI, "k1", true, []string{"gpt-4o", "gpt-4o-mini"})
	mc.UpsertLive(schemas.OpenAI, "k2", true, []string{"o1"})

	if got := mc.RoutableModelsForProvider(schemas.OpenAI, []string{"k1"}, true); !slices.Equal(got, []string{"gpt-4o", "gpt-4o-mini"}) {
		t.Errorf("unfiltered scoped to k1 = %v, want [gpt-4o gpt-4o-mini]", got)
	}
}

// A key with no cached entry must come back empty rather than inheriting the
// provider's other keys. This is the failure mode that would silently repopulate
// a just-invalidated key from its siblings.
func TestRoutableModelsForProvider_UnknownKeyDoesNotInherit(t *testing.T) {
	mc := routableScopeCatalog(t)
	mc.UpsertLive(schemas.OpenAI, "k1", false, []string{"gpt-4o"})

	if got := mc.RoutableModelsForProvider(schemas.OpenAI, []string{"never-fetched"}, false); len(got) != 0 {
		t.Errorf("scoped to an unfetched key = %v, want empty", got)
	}
}
