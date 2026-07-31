package bifrost

import (
	"slices"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// fakeModelDirectory records what it was asked for and returns canned data.
type fakeModelDirectory struct {
	cached      []schemas.Model
	hit         bool
	routable    []string
	cachedCalls int
	gotKeyIDs   []string
	gotUnfilt   bool
	// gotRoutableKeyIDs is recorded separately from gotKeyIDs: the two reads are
	// scoped by different inputs (the resolved key set vs. the request's KeyID),
	// and conflating them would hide a reconciliation that widened past the key
	// the response was actually produced from.
	gotRoutableKeyIDs  []string
	routableKeyIDsSeen bool
}

func (f *fakeModelDirectory) CachedModels(provider schemas.ModelProvider, keyIDs []string, unfiltered bool) ([]schemas.Model, bool) {
	f.cachedCalls++
	f.gotKeyIDs = append([]string(nil), keyIDs...)
	f.gotUnfilt = unfiltered
	if !f.hit {
		return nil, false
	}
	return f.cached, true
}

func (f *fakeModelDirectory) RoutableModels(provider schemas.ModelProvider, keyIDs []string, unfiltered bool) []string {
	f.gotRoutableKeyIDs = append([]string(nil), keyIDs...)
	f.routableKeyIDsSeen = true
	return f.routable
}

// The lookup half of schemas.ModelDirectory. Inert here: these tests exercise
// list-models, and the two halves share no state.
func (f *fakeModelDirectory) GetModelInfo(schemas.ModelProvider, string) *schemas.Model { return nil }

func (f *fakeModelDirectory) CalculateRequestCost(*schemas.BifrostContext, *schemas.BifrostResponse) float64 {
	return 0
}

func modelsFixture(ids ...string) []schemas.Model {
	out := make([]schemas.Model, len(ids))
	for i, id := range ids {
		out[i] = schemas.Model{ID: id}
	}
	return out
}

func modelIDs(models []schemas.Model) []string {
	out := make([]string, len(models))
	for i, m := range models {
		out[i] = m.ID
	}
	return out
}

func TestSetModelDirectory_InstallAndWithdraw(t *testing.T) {
	b := &Bifrost{}

	if b.HasModelDirectory() {
		t.Fatal("expected no catalog before install")
	}

	b.SetModelDirectory(&fakeModelDirectory{}, true)
	if !b.HasModelDirectory() || !b.ServesListModelsFromCatalog() {
		t.Fatal("expected the catalog to be installed and servable")
	}

	// Installed but not servable is the disabled-refresher state: still used to
	// reconcile and to stand in for a failing provider, never to answer alone.
	b.SetModelDirectory(&fakeModelDirectory{}, false)
	if !b.HasModelDirectory() {
		t.Fatal("expected the catalog to stay installed when serving is off")
	}
	if b.ServesListModelsFromCatalog() {
		t.Fatal("expected serving to be off")
	}

	b.SetModelDirectory(nil, true)
	if b.HasModelDirectory() {
		t.Fatal("expected a nil catalog to be withdrawn entirely")
	}
}

func TestLookupCachedListModels_MissWhenNoCatalog(t *testing.T) {
	b := &Bifrost{}
	req := &schemas.BifrostListModelsRequest{Provider: schemas.OpenAI}

	if _, ok := b.lookupCachedListModels(nil, schemas.OpenAI, nil, req); ok {
		t.Fatal("expected a miss with no catalog installed")
	}
}

// With serving disabled the catalog must never answer a request outright, even
// though it stays installed for reconciliation.
func TestLookupCachedListModels_NotServedWhenServingDisabled(t *testing.T) {
	catalog := &fakeModelDirectory{cached: modelsFixture("gpt-4o"), hit: true}
	b := &Bifrost{}
	b.SetModelDirectory(catalog, false)

	req := &schemas.BifrostListModelsRequest{Provider: schemas.OpenAI}
	if _, ok := b.lookupCachedListModels(nil, schemas.OpenAI, nil, req); ok {
		t.Fatal("expected no cache hit while serving from the catalog is disabled")
	}
	if catalog.cachedCalls != 0 {
		t.Errorf("expected the catalog not to be consulted at all, got %d call(s)", catalog.cachedCalls)
	}
}

func TestLookupCachedListModels_HitReturnsCachedModels(t *testing.T) {
	catalog := &fakeModelDirectory{cached: modelsFixture("gpt-4o", "o1"), hit: true}
	b := &Bifrost{}
	b.SetModelDirectory(catalog, true)

	keys := []schemas.Key{{ID: "k1"}, {ID: "k2"}}
	req := &schemas.BifrostListModelsRequest{Provider: schemas.OpenAI}

	resp, ok := b.lookupCachedListModels(nil, schemas.OpenAI, keys, req)
	if !ok {
		t.Fatal("expected a cache hit")
	}
	if !slices.Equal(modelIDs(resp.Data), []string{"gpt-4o", "o1"}) {
		t.Fatalf("unexpected models: %v", modelIDs(resp.Data))
	}
	if resp.ExtraFields.Provider != schemas.OpenAI {
		t.Errorf("Provider = %q, want %q", resp.ExtraFields.Provider, schemas.OpenAI)
	}
	// A hit probes nothing, so it has learned nothing about key health.
	if len(resp.KeyStatuses) != 0 {
		t.Errorf("expected no key statuses on a cache hit, got %+v", resp.KeyStatuses)
	}
	if got := catalog.gotKeyIDs; !slices.Equal(got, []string{"k1", "k2"}) {
		t.Errorf("catalog received keyIDs %v, want [k1 k2]", got)
	}
}

// The refresher calls ListModelsRequest to fill the catalog. If that call could
// read it back, the refresher would be handed its own previous result, store it
// unchanged, and the catalog would never move again.
func TestLookupCachedListModels_SkipFlagBypassesCache(t *testing.T) {
	catalog := &fakeModelDirectory{cached: modelsFixture("gpt-4o"), hit: true}
	b := &Bifrost{}
	b.SetModelDirectory(catalog, true)

	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeySkipListModelsCache, true)

	req := &schemas.BifrostListModelsRequest{Provider: schemas.OpenAI}
	if _, ok := b.lookupCachedListModels(ctx, schemas.OpenAI, nil, req); ok {
		t.Fatal("expected the skip flag to force a miss")
	}
	if catalog.cachedCalls != 0 {
		t.Errorf("expected the catalog not to be consulted, got %d call(s)", catalog.cachedCalls)
	}
}

func TestLookupCachedListModels_PassesUnfilteredThrough(t *testing.T) {
	catalog := &fakeModelDirectory{cached: modelsFixture("gpt-4o"), hit: true}
	b := &Bifrost{}
	b.SetModelDirectory(catalog, true)

	req := &schemas.BifrostListModelsRequest{Provider: schemas.OpenAI, Unfiltered: true}
	if _, ok := b.lookupCachedListModels(nil, schemas.OpenAI, nil, req); !ok {
		t.Fatal("expected a cache hit")
	}
	// Filtered and unfiltered are separate views; conflating them would serve
	// the ungated catalog to a caller asking for the gated one.
	if !catalog.gotUnfilt {
		t.Error("expected the unfiltered flag to reach the catalog")
	}
}

// TestReconcileListModelsWithRoutable_AddsMissingModels is the point of the
// whole reconciliation: what list-models advertises must match what routing
// accepts, so models the provider under-reported have to be added back.
func TestReconcileListModelsWithRoutable_AddsMissingModels(t *testing.T) {
	catalog := &fakeModelDirectory{routable: []string{"gpt-4o", "gpt-4o-deprecated", "o1"}}
	b := &Bifrost{}
	b.SetModelDirectory(catalog, true)

	// The provider listed only one of the three it will actually serve.
	resp := &schemas.BifrostListModelsResponse{Data: modelsFixture("openai/gpt-4o")}
	req := &schemas.BifrostListModelsRequest{Provider: schemas.OpenAI}

	got := modelIDs(b.reconcileListModelsWithRoutable(resp, schemas.OpenAI, nil, req).Data)
	want := []string{"openai/gpt-4o", "openai/gpt-4o-deprecated", "openai/o1"}
	if !slices.Equal(got, want) {
		t.Fatalf("reconciled = %v, want %v", got, want)
	}
}

// Matching is on the bare name, because the provider may or may not prefix its
// own IDs. Comparing raw strings would re-add every model under a second ID.
func TestReconcileListModelsWithRoutable_NoDuplicatesAcrossIDForms(t *testing.T) {
	catalog := &fakeModelDirectory{routable: []string{"gpt-4o", "o1"}}
	b := &Bifrost{}
	b.SetModelDirectory(catalog, true)

	// Provider prefixed one and left the other bare; the routable view has both
	// bare. Neither should be duplicated.
	resp := &schemas.BifrostListModelsResponse{Data: modelsFixture("openai/gpt-4o", "o1")}
	req := &schemas.BifrostListModelsRequest{Provider: schemas.OpenAI}

	got := modelIDs(b.reconcileListModelsWithRoutable(resp, schemas.OpenAI, nil, req).Data)
	if !slices.Equal(got, []string{"openai/gpt-4o", "o1"}) {
		t.Fatalf("reconciled = %v, want no additions", got)
	}
}

// A provider listing another provider's model keeps the foreign prefix, since
// that prefix is what cross-provider resolution keys off.
// Every synthesized ID is namespaced to the provider that will serve it, which
// is the convention providers already follow: OpenRouter returns
// "openrouter/ai21/jamba-large-1.7", not "ai21/jamba-large-1.7". A resold model
// keeps its upstream name inside that namespace.
//
// Emitting the bare upstream name instead would make the model look like it
// came from the upstream provider, and it would still appear in the listing
// after that provider had been turned off.
func TestReconcileListModelsWithRoutable_NamespacesResoldModels(t *testing.T) {
	catalog := &fakeModelDirectory{routable: []string{"anthropic/claude-sonnet-4-5"}}
	b := &Bifrost{}
	b.SetModelDirectory(catalog, true)

	resp := &schemas.BifrostListModelsResponse{Data: modelsFixture("openrouter/ai21/jamba-large-1.7")}
	req := &schemas.BifrostListModelsRequest{Provider: schemas.OpenRouter}

	got := modelIDs(b.reconcileListModelsWithRoutable(resp, schemas.OpenRouter, nil, req).Data)
	want := []string{"openrouter/ai21/jamba-large-1.7", "openrouter/anthropic/claude-sonnet-4-5"}
	if !slices.Equal(got, want) {
		t.Fatalf("reconciled = %v, want the resold model namespaced to its serving provider: %v", got, want)
	}
}

// A model the provider already listed under the serving-provider prefix must
// not be added again when the routable set names it without that prefix.
func TestReconcileListModelsWithRoutable_NoDuplicateForAlreadyNamespacedModel(t *testing.T) {
	catalog := &fakeModelDirectory{routable: []string{"anthropic/claude-sonnet-4-5"}}
	b := &Bifrost{}
	b.SetModelDirectory(catalog, true)

	// The provider already returned it in the namespaced form.
	resp := &schemas.BifrostListModelsResponse{Data: modelsFixture("openrouter/anthropic/claude-sonnet-4-5")}
	req := &schemas.BifrostListModelsRequest{Provider: schemas.OpenRouter}

	got := modelIDs(b.reconcileListModelsWithRoutable(resp, schemas.OpenRouter, nil, req).Data)
	if !slices.Equal(got, []string{"openrouter/anthropic/claude-sonnet-4-5"}) {
		t.Fatalf("reconciled = %v, want no duplicate", got)
	}
}

// Reconciliation runs on the complete result and is paginated afterwards, so a
// PageToken on the request must not suppress it. Skipping later pages would
// paginate page one over the reconciled list and every later page over the
// shorter un-reconciled one, so the offsets would disagree and models near each
// boundary would be dropped or repeated.
func TestReconcileListModelsWithRoutable_IgnoresPageToken(t *testing.T) {
	catalog := &fakeModelDirectory{routable: []string{"extra-model"}}
	b := &Bifrost{}
	b.SetModelDirectory(catalog, true)

	for _, pageToken := range []string{"", "page-2"} {
		resp := &schemas.BifrostListModelsResponse{Data: modelsFixture("gpt-4o")}
		req := &schemas.BifrostListModelsRequest{PageToken: pageToken}
		got := modelIDs(b.reconcileListModelsWithRoutable(resp, schemas.OpenAI, nil, req).Data)
		want := []string{"gpt-4o", "openai/extra-model"}
		if !slices.Equal(got, want) {
			t.Errorf("page token %q: reconciled = %v, want %v", pageToken, got, want)
		}
	}
}

// TestReconcileListModelsWithRoutable_ForwardsKeyScope is the regression for
// reconciliation widening a key-scoped answer. The response was produced from
// one key, so the routable set it is reconciled against has to be read for that
// key alone — reading the provider-wide union would append models a sibling
// key's allow-list permits and this one's does not, and the refresher would then
// store that widened set under this key's cache entry.
func TestReconcileListModelsWithRoutable_ForwardsKeyScope(t *testing.T) {
	catalog := &fakeModelDirectory{routable: []string{"gpt-4o"}}
	b := &Bifrost{}
	b.SetModelDirectory(catalog, true)

	resp := &schemas.BifrostListModelsResponse{Data: modelsFixture("openai/gpt-4o")}
	b.reconcileListModelsWithRoutable(resp, schemas.OpenAI, []string{"key-a"}, &schemas.BifrostListModelsRequest{})

	if !slices.Equal(catalog.gotRoutableKeyIDs, []string{"key-a"}) {
		t.Fatalf("routable read with keyIDs = %v, want [key-a]", catalog.gotRoutableKeyIDs)
	}
}

// The unscoped case is the counterpart: with no KeyID on the request there is
// no key to narrow to, and the provider-wide union is the correct answer rather
// than an over-broad one.
func TestReconcileListModelsWithRoutable_UnscopedReadsProviderWide(t *testing.T) {
	catalog := &fakeModelDirectory{routable: []string{"gpt-4o"}}
	b := &Bifrost{}
	b.SetModelDirectory(catalog, true)

	resp := &schemas.BifrostListModelsResponse{Data: modelsFixture("openai/gpt-4o")}
	b.reconcileListModelsWithRoutable(resp, schemas.OpenAI, nil, &schemas.BifrostListModelsRequest{})

	if !catalog.routableKeyIDsSeen {
		t.Fatal("expected the routable set to be consulted")
	}
	if len(catalog.gotRoutableKeyIDs) != 0 {
		t.Fatalf("routable read with keyIDs = %v, want empty for an unscoped request", catalog.gotRoutableKeyIDs)
	}
}

func TestReconcileListModelsWithRoutable_NoCatalogIsPassthrough(t *testing.T) {
	b := &Bifrost{}
	resp := &schemas.BifrostListModelsResponse{Data: modelsFixture("gpt-4o")}

	got := b.reconcileListModelsWithRoutable(resp, schemas.OpenAI, nil, &schemas.BifrostListModelsRequest{})
	if !slices.Equal(modelIDs(got.Data), []string{"gpt-4o"}) {
		t.Fatalf("expected the response untouched without a catalog, got %v", modelIDs(got.Data))
	}
}

// TestRoutableListModelsFallback covers the failure path: a provider that is
// momentarily unreachable must not vanish from list-models while its models
// remain routable, or callers are told a model does not exist and then served
// it on the next request.
func TestRoutableListModelsFallback(t *testing.T) {
	catalog := &fakeModelDirectory{routable: []string{"gpt-4o", "o1"}}
	b := &Bifrost{}
	b.SetModelDirectory(catalog, true)

	resp := b.routableListModelsFallback(schemas.OpenAI, nil, false)
	if resp == nil {
		t.Fatal("expected a fallback response for a provider with routable models")
	}
	if !slices.Equal(modelIDs(resp.Data), []string{"openai/gpt-4o", "openai/o1"}) {
		t.Fatalf("fallback = %v", modelIDs(resp.Data))
	}
}

// Serving disabled must not disable the fallback: the gate exists to stop a
// stale snapshot answering in place of a live call, but here the live call
// already failed and the alternative is returning nothing.
func TestRoutableListModelsFallback_WorksWhenServingDisabled(t *testing.T) {
	catalog := &fakeModelDirectory{routable: []string{"gpt-4o"}}
	b := &Bifrost{}
	b.SetModelDirectory(catalog, false)

	if resp := b.routableListModelsFallback(schemas.OpenAI, nil, false); resp == nil {
		t.Fatal("expected the fallback to work with serving from the catalog disabled")
	}
}

// Nothing routable means there is genuinely nothing to say, so the caller must
// surface the provider's original error rather than an empty success.
func TestRoutableListModelsFallback_NilWhenNothingRoutable(t *testing.T) {
	b := &Bifrost{}
	b.SetModelDirectory(&fakeModelDirectory{routable: nil}, true)

	if resp := b.routableListModelsFallback(schemas.OpenAI, nil, false); resp != nil {
		t.Fatalf("expected nil so the original error is surfaced, got %v", modelIDs(resp.Data))
	}
}

func TestRoutableListModelsFallback_NilWithoutCatalog(t *testing.T) {
	b := &Bifrost{}
	if resp := b.routableListModelsFallback(schemas.OpenAI, nil, false); resp != nil {
		t.Fatal("expected nil without a catalog installed")
	}
}

// TestCatalogMaySpeakFor is the guard against advertising models that cannot be
// called. Catalog entries outlive the keys that produced them, so a provider
// whose keys were all disabled can still have entries sitting there; publishing
// those would list models every request then rejects.
//
// The decision is made from configuration rather than from the provider's error
// text on purpose: error strings differ between providers and change over time,
// and one unrecognised case would resurrect a provider the operator turned off.
func TestCatalogMaySpeakFor(t *testing.T) {
	keyless := &schemas.CustomProviderConfig{IsKeyLess: true, BaseProviderType: schemas.OpenAI}
	listModelsOff := &schemas.CustomProviderConfig{
		BaseProviderType: schemas.OpenAI,
		AllowedRequests:  &schemas.AllowedRequests{}, // zero value: list_models false
	}
	listModelsOn := &schemas.CustomProviderConfig{
		BaseProviderType: schemas.OpenAI,
		AllowedRequests:  &schemas.AllowedRequests{ListModels: true},
	}

	tests := []struct {
		name   string
		config *schemas.ProviderConfig
		keys   []schemas.Key
		want   bool
	}{
		{"keyed provider with a usable key", &schemas.ProviderConfig{}, []schemas.Key{{ID: "k1"}}, true},
		{"keyed provider with every key disabled", &schemas.ProviderConfig{}, nil, false},
		{"keyless provider needs no keys", &schemas.ProviderConfig{CustomProviderConfig: keyless}, nil, true},
		{"list-models switched off", &schemas.ProviderConfig{CustomProviderConfig: listModelsOff}, []schemas.Key{{ID: "k1"}}, false},
		{"list-models explicitly on", &schemas.ProviderConfig{CustomProviderConfig: listModelsOn}, []schemas.Key{{ID: "k1"}}, true},
		{"nil config with a key", nil, []schemas.Key{{ID: "k1"}}, true},
		{"nil config with no keys", nil, nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := canCatalogServeModelListFor(tt.config, tt.keys); got != tt.want {
				t.Errorf("canCatalogServeModelListFor = %v, want %v", got, tt.want)
			}
		})
	}
}

// Regression: with every key on a provider disabled, /v1/models must not report
// its models. The catalog can still hold entries for it, and before this gate
// the failure fallback published them, so a provider the operator had switched
// off kept appearing in the listing.
func TestCatalogMaySpeakFor_DisabledProviderStaysSilentDespiteCatalogEntries(t *testing.T) {
	catalog := &fakeModelDirectory{
		cached:   modelsFixture("claude-sonnet-4-5"),
		hit:      true,
		routable: []string{"claude-sonnet-4-5", "claude-opus-4-6"},
	}
	b := &Bifrost{}
	b.SetModelDirectory(catalog, true)

	// The catalog would happily answer; the gate is the only thing stopping it.
	if resp := b.routableListModelsFallback(schemas.Anthropic, nil, false); resp == nil {
		t.Fatal("precondition: the catalog should hold routable models for this test to mean anything")
	}
	if canCatalogServeModelListFor(&schemas.ProviderConfig{}, nil) {
		t.Fatal("a keyed provider with no usable keys must not be spoken for")
	}
}
