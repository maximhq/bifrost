package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// testVirtualConfig builds a live lib.Config carrying two virtual models on two providers,
// which is the shape that makes the filtering worth testing: a provider-scoped listing must
// not leak the other provider's virtual name, and a virtual key granted one provider must
// not see the other's.
func testVirtualConfig(t *testing.T) *lib.Config {
	t.Helper()
	cfg := &lib.Config{Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
		"omlx":   {NetworkConfig: &schemas.NetworkConfig{BaseURL: "http://127.0.0.1:1", AllowPrivateNetwork: true}},
		"ollama": {NetworkConfig: &schemas.NetworkConfig{BaseURL: "http://127.0.0.1:2", AllowPrivateNetwork: true}},
	}}
	service := lib.NewVirtualModelService(nil, nil)
	err := service.Install(map[string]schemas.VirtualModel{
		"qwen38": {Name: "qwen38", Provider: "omlx", Models: []string{"flash", "27b"}, OnProbeError: schemas.VirtualModelErrorOnProbeError, Description: "frontier qwen"},
		"small":  {Name: "small", Provider: "ollama", Models: []string{"qwen3:30b"}, OnProbeError: schemas.VirtualModelErrorOnProbeError},
	}, cfg.Providers)
	if err != nil {
		t.Fatalf("install failed: %v", err)
	}
	cfg.VirtualModels = service
	return cfg
}

func TestAppendVirtualModels(t *testing.T) {
	cfg := testVirtualConfig(t)

	all := &schemas.BifrostListModelsResponse{Data: []schemas.Model{{ID: "gpt-4o"}}}
	appendVirtualModels(all, cfg, "", nil, false)
	if len(all.Data) != 3 {
		t.Fatalf("got %d entries, want the concrete model plus both virtual names", len(all.Data))
	}
	if all.Data[1].ID != "qwen38" || all.Data[2].ID != "small" {
		t.Fatalf("virtual entries = %s, %s, want qwen38 then small", all.Data[1].ID, all.Data[2].ID)
	}
	if all.Data[0].ID != "gpt-4o" {
		t.Fatal("a backend's own model must keep its position ahead of synthetic entries")
	}
	if all.Data[1].AdditionalAttributes["bifrost:virtual"] != "true" {
		t.Fatal("virtual entry is not marked virtual, so a client cannot tell it from a real one")
	}

	scoped := &schemas.BifrostListModelsResponse{Data: []schemas.Model{{ID: "gpt-4o"}}}
	appendVirtualModels(scoped, cfg, "omlx", nil, false)
	if len(scoped.Data) != 2 || scoped.Data[1].ID != "qwen38" {
		t.Fatalf("provider-scoped listing = %+v, want only the omlx virtual name", scoped.Data)
	}

	// A backend that really serves that string keeps its own richer entry.
	dup := &schemas.BifrostListModelsResponse{Data: []schemas.Model{{ID: "qwen38", ContextLength: ptrInt(262144)}}}
	appendVirtualModels(dup, cfg, "", nil, false)
	if len(dup.Data) != 2 {
		t.Fatalf("got %d entries, want the real qwen38 and small with no synthetic duplicate", len(dup.Data))
	}
	if dup.Data[0].ContextLength == nil {
		t.Fatal("the backend's own entry was replaced, losing its metadata")
	}
}

// A gateway with no virtual models must be untouched, not merely empty-looking.
func TestAppendVirtualModelsDisabled(t *testing.T) {
	resp := &schemas.BifrostListModelsResponse{Data: []schemas.Model{{ID: "gpt-4o"}}}
	appendVirtualModels(resp, &lib.Config{}, "", nil, false)
	if len(resp.Data) != 1 {
		t.Fatalf("got %d entries, want the listing untouched", len(resp.Data))
	}
	appendVirtualModels(nil, testVirtualConfig(t), "", nil, false)
}

func TestVirtualListedModelsHonoursGrantedProviders(t *testing.T) {
	cfg := testVirtualConfig(t)

	granted := virtualListedModels(cfg, []schemas.ModelProvider{"omlx"}, nil, false, "")
	if len(granted) != 1 || granted[0].Name != "qwen38" {
		t.Fatalf("granted listing = %+v, want only qwen38", granted)
	}

	none := virtualListedModels(cfg, []schemas.ModelProvider{}, nil, false, "")
	if len(none) != 0 {
		t.Fatalf("denied listing = %+v, want no virtual names", none)
	}
}

// A virtual name answers from Bifrost rather than from the backend, so a client that
// validates against this route does not conclude the model is missing.
func TestVirtualModelRetrieve(t *testing.T) {
	cfg := testVirtualConfig(t)
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)

	// The port is closed, so the probe fails and residency is simply absent - which must
	// not stop the metadata from answering.
	resp, ok := virtualModelRetrieve(ctx, cfg, "qwen38", "", nil, false)
	if !ok {
		t.Fatal("a virtual name was not recognised")
	}
	if resp.ID != "qwen38" {
		t.Fatalf("id = %q, want the virtual name", resp.ID)
	}
	if resp.AdditionalAttributes["bifrost:candidate-models"] != "flash,27b" {
		t.Fatalf("candidates = %q, want flash,27b", resp.AdditionalAttributes["bifrost:candidate-models"])
	}
	if resp.Description == nil || *resp.Description != "frontier qwen" {
		t.Fatal("the operator's description was dropped")
	}

	// The provider-prefixed form addresses the same model.
	if _, ok := virtualModelRetrieve(ctx, cfg, "omlx%2Fqwen38", "", nil, false); !ok {
		t.Fatal("provider/model form was not recognised")
	}

	if _, ok := virtualModelRetrieve(ctx, cfg, "gpt-4o", "", nil, false); ok {
		t.Fatal("a concrete model name must fall through to the upstream path")
	}
	if _, ok := virtualModelRetrieve(ctx, &lib.Config{}, "qwen38", "", nil, false); ok {
		t.Fatal("a gateway without virtual models must fall through")
	}
}

func ptrInt(n int) *int { return &n }

// An explicit provider is a constraint on who is being asked, not a hint. When it disagrees with
// the provider that owns the virtual model, the client wants that provider's own model of the same
// name, so the virtual entry must not answer. It did: ?provider= was ignored outright, and the
// prefixed form came back 403 for a concrete model that exists. Both fall through to the upstream
// path now, which is where that 404 belongs.
func TestVirtualRetrieveHonoursTheRequestedProvider(t *testing.T) {
	cfg := testVirtualConfig(t)
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	defer ctx.Cancel()

	if resp, handled := virtualModelRetrieve(ctx, cfg, "openai/qwen38", "", nil, false); handled {
		t.Fatalf("openai/qwen38 was answered by the omlx virtual model (resp=%v), want it left for openai", resp)
	}
	if resp, handled := virtualModelRetrieve(ctx, cfg, "qwen38", "openai", nil, false); handled {
		t.Fatalf("?provider=openai was ignored and the virtual entry answered (resp=%v)", resp)
	}

	// The owning provider answers whichever way it is spelled, and so does a bare name: that is
	// the construct itself, and a case-sensitive comparison would break it for ?provider=OMLX.
	for _, requested := range []schemas.ModelProvider{"", "omlx", "OMLX"} {
		resp, handled := virtualModelRetrieve(ctx, cfg, "qwen38", requested, nil, false)
		if !handled || resp == nil {
			t.Fatalf("provider %q: the virtual model did not answer (handled=%v, resp=%v)", requested, handled, resp)
		}
	}
}

// A provider filter arrives spelled however the client likes while owners are stored lowercased, and
// created must not move between two identical listings.
func TestVirtualListingProviderCaseAndStableCreated(t *testing.T) {
	cfg := testVirtualConfig(t)

	upper := &schemas.BifrostListModelsResponse{}
	appendVirtualModels(upper, cfg, "OMLX", nil, false)
	if len(upper.Data) != 1 || upper.Data[0].ID != "qwen38" {
		t.Fatalf("uppercase provider filter matched %+v, want only qwen38", upper.Data)
	}

	lower := &schemas.BifrostListModelsResponse{}
	appendVirtualModels(lower, cfg, "omlx", nil, false)
	if len(lower.Data) != 1 {
		t.Fatalf("lowercase provider filter matched %d entries, want 1", len(lower.Data))
	}
	if upper.Data[0].Created == nil || *upper.Data[0].Created == 0 {
		t.Fatal("created is missing; a virtual model should report when its definitions were installed")
	}
	if *upper.Data[0].Created != *lower.Data[0].Created {
		t.Fatalf("created moved between two identical listings: %d vs %d", *upper.Data[0].Created, *lower.Data[0].Created)
	}
}

// TestVirtualDiscoveryRestrictedAccess pins the rule that a virtual model's metadata — its
// candidates and their residency — is only published to a caller that may reach the provider and
// every candidate. Partial access hides the whole entry rather than leaking the candidate the
// caller was not granted.
func TestVirtualDiscoveryRestrictedAccess(t *testing.T) {
	cfg := testVirtualConfig(t)
	tests := []struct {
		name   string
		access schemas.Access
		want   int
	}{
		{"no permits", accessForProviderPermits(), 0},
		{"wrong provider", accessForProviderPermits(schemas.ProviderPermit{Provider: "openai", AllowedModels: schemas.WhiteList{"*"}}), 0},
		{"partial candidates", accessForProviderPermits(schemas.ProviderPermit{Provider: "omlx", AllowedModels: schemas.WhiteList{"flash"}}), 0},
		{"all candidates", accessForProviderPermits(schemas.ProviderPermit{Provider: "omlx", AllowedModels: schemas.WhiteList{"flash", "27b"}}), 1},
		{"blacklisted candidate", accessForProviderPermits(schemas.ProviderPermit{Provider: "omlx", AllowedModels: schemas.WhiteList{"*"}, BlacklistedModels: schemas.BlackList{"27b"}}), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			listing := &schemas.BifrostListModelsResponse{}
			appendVirtualModels(listing, cfg, "", tt.access, false)
			if len(listing.Data) != tt.want {
				t.Fatalf("inference listing = %+v, want %d entries", listing.Data, tt.want)
			}

			management := virtualListedModels(cfg, []schemas.ModelProvider{"omlx", "ollama"}, tt.access, false, "")
			if len(management) != tt.want {
				t.Fatalf("management listing = %+v, want %d entries", management, tt.want)
			}

			ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
			defer ctx.Cancel()
			retrieved, handled := virtualModelRetrieve(ctx, cfg, "qwen38", "", tt.access, false)
			if !handled {
				t.Fatal("virtual name was not handled")
			}
			if (retrieved != nil) != (tt.want == 1) {
				t.Fatalf("retrieve returned %v for access that wants %d", retrieved, tt.want)
			}
		})
	}
}

// A nil access is the same reading the concrete listing gives when nothing was resolved - no
// governance deployed, or a route that carries no grant - so virtual names stay visible there
// rather than the gateway appearing to have none.
func TestVirtualDiscoveryUnfilteredWithoutAccess(t *testing.T) {
	cfg := testVirtualConfig(t)
	listing := &schemas.BifrostListModelsResponse{Data: []schemas.Model{{ID: "gpt-4o"}}}
	appendVirtualModels(listing, cfg, "", nil, false)
	if len(listing.Data) != 3 {
		t.Fatalf("got %d entries, want both virtual names beside the concrete one", len(listing.Data))
	}
	if got := len(virtualListedModels(cfg, []schemas.ModelProvider{"omlx"}, nil, false, "")); got != 1 {
		t.Fatalf("management listing = %d, want the omlx virtual name", got)
	}
}

// A prefix naming some other provider is not a request for this construct: the client is asking
// for that provider's own model of the same name, which has to stay reachable, since a virtual
// model named after a vendor model is the usual migration shape. Falling through hands the request
// to the provider the client named, which answers with its own truth, so no unmeasured mapping
// is ever handed out. Claiming it here instead 403'd a valid concrete lookup.
func TestVirtualRetrieveFallsThroughForAnotherProvider(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	defer ctx.Cancel()
	if resp, handled := virtualModelRetrieve(ctx, testVirtualConfig(t), "ollama/qwen38", "", nil, false); handled {
		t.Fatalf("wrong provider prefix was claimed as virtual (resp=%v), want it left for the named provider", resp)
	}
}

type failingAccessManager struct {
	ModelsManager
}

func (failingAccessManager) ResolveAccess(*schemas.BifrostContext) (schemas.Access, error) {
	return nil, errors.New("resolution failed")
}

// Access resolution is best-effort by design: a manager that is missing or that refuses to settle
// the caller yields nil, which reads as "unfiltered" downstream. Authentication is enforced
// upstream by the auth layer, so a listing route must not turn a resolution error into a hard
// refusal - /api/models legitimately carries no grant, and refusing there would close the
// dashboard.
func TestVirtualAccessIsBestEffort(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	defer ctx.Cancel()

	if access := resolveVirtualModelAccess(ctx, nil); access != nil {
		t.Fatal("a missing resolver returned access")
	}
	if access := resolveVirtualModelAccess(ctx, failingAccessManager{}); access != nil {
		t.Fatal("a failing resolver returned access")
	}
	granted := accessForProviderPermits(schemas.ProviderPermit{Provider: "omlx", AllowedModels: schemas.WhiteList{"*"}})
	h := &CompletionHandler{modelsManager: &mockModelsManager{access: granted}}
	if access := h.virtualModelAccess(ctx); access != granted {
		t.Fatal("the resolved access was not handed to the caller")
	}
	if access := (&CompletionHandler{}).virtualModelAccess(ctx); access != nil {
		t.Fatal("a handler without a manager returned access")
	}
}

// An unauthorized lookup must refuse before reaching the backend: a name the caller may not use
// should not spend a probe on the operator's engine, and must not answer with the candidate list.
func TestVirtualRetrieveHTTPAuthorizationBeforeProbe(t *testing.T) {
	var probes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		probes.Add(1)
		fmt.Fprint(w, `{"models":[{"id":"flash","loaded":true},{"id":"27b","loaded":false}]}`)
	}))
	defer server.Close()

	cfg := testVirtualConfig(t)
	cfg.ClientConfig = &configstore.ClientConfig{}
	cfg.Providers["omlx"] = configstore.ProviderConfig{
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL:             server.URL,
			AllowPrivateNetwork: true,
			ModelResidency:      &schemas.ModelResidencyConfig{Adapter: schemas.ResidencyAdapterOMLX},
		},
	}
	if err := cfg.VirtualModels.Install(map[string]schemas.VirtualModel{
		"qwen38": {Provider: "omlx", Models: []string{"flash", "27b"}},
	}, cfg.Providers); err != nil {
		t.Fatalf("install: %v", err)
	}

	for _, tt := range []struct {
		allowed bool
		access  schemas.Access
		status  int
	}{
		{false, accessForProviderPermits(schemas.ProviderPermit{Provider: "omlx", AllowedModels: schemas.WhiteList{"flash"}}), fasthttp.StatusForbidden},
		{true, accessForProviderPermits(schemas.ProviderPermit{Provider: "omlx", AllowedModels: schemas.WhiteList{"flash", "27b"}}), fasthttp.StatusOK},
	} {
		h := &CompletionHandler{config: cfg, modelsManager: &mockModelsManager{access: tt.access}}
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.SetRequestURI("/v1/models/qwen38")
		ctx.SetUserValue("model", "qwen38")
		h.modelRetrieve(ctx)
		if ctx.Response.StatusCode() != tt.status {
			t.Fatalf("allowed=%v status=%d body=%s", tt.allowed, ctx.Response.StatusCode(), ctx.Response.Body())
		}
		if !tt.allowed && probes.Load() != 0 {
			t.Fatal("an unauthorized lookup probed the backend")
		}
	}
	if probes.Load() != 1 {
		t.Fatalf("probes = %d, want one, for the authorized lookup only", probes.Load())
	}
}

func BenchmarkVirtualModelAllowed(b *testing.B) {
	vm := &schemas.VirtualModel{Provider: "omlx", Models: []string{"flash", "27b"}}
	access := accessForProviderPermits(schemas.ProviderPermit{Provider: "omlx", AllowedModels: schemas.WhiteList{"*"}})
	b.ReportAllocs()
	for b.Loop() {
		virtualModelAllowed(vm, access, false)
	}
}

// An unresolved access is two cases and they must not share a default. A caller that presented no
// credential keeps the unfiltered listing, because authentication belongs to the auth layer and
// these routes do not run it. A caller that DID present a virtual key and resolved to nothing was
// refused by governance: it is shown no virtual names, and a direct lookup is denied rather than
// spending a residency probe on a credential that has no right to the backend.
func TestVirtualDiscoveryDeniesUnresolvedCredential(t *testing.T) {
	cfg := testVirtualConfig(t)

	listing := &schemas.BifrostListModelsResponse{Data: []schemas.Model{{ID: "gpt-4o"}}}
	appendVirtualModels(listing, cfg, "", nil, true)
	if len(listing.Data) != 1 || listing.Data[0].ID != "gpt-4o" {
		t.Fatalf("a presented-but-unresolved credential was shown virtual names: %+v", listing.Data)
	}

	management := virtualListedModels(cfg, []schemas.ModelProvider{"omlx", "ollama"}, nil, true, "")
	if len(management) != 0 {
		t.Fatalf("management listing = %+v, want nothing for a refused credential", management)
	}

	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	defer ctx.Cancel()
	resp, handled := virtualModelRetrieve(ctx, cfg, "qwen38", "", nil, true)
	if !handled {
		t.Fatal("virtual name was forwarded upstream instead of answered here")
	}
	if resp != nil {
		t.Fatalf("retrieve returned %+v, want denial", resp)
	}

	// Same shape, no credential presented: unchanged, so the dashboard keeps working.
	appendVirtualModels(listing, cfg, "", nil, false)
	if len(listing.Data) != 3 {
		t.Fatalf("unrestricted caller = %d entries, want the virtual names kept", len(listing.Data))
	}
}

// Virtual names take the same search filter concrete names take. A search for one string must not
// answer with every virtual name, and it uses the shared matcher, so a query that matches a
// concrete name through normalization matches a virtual one the same way.
func TestVirtualListedModelsTakeTheSearchFilter(t *testing.T) {
	cfg := testVirtualConfig(t)
	providers := []schemas.ModelProvider{"omlx", "ollama"}

	if got := len(virtualListedModels(cfg, providers, nil, false, "")); got != 2 {
		t.Fatalf("empty query = %d entries, want both virtual names", got)
	}

	filtered := virtualListedModels(cfg, providers, nil, false, "qwen")
	if len(filtered) != 1 || filtered[0].Name != "qwen38" {
		t.Fatalf("query qwen = %+v, want only qwen38", filtered)
	}

	// The matcher normalizes separators; a virtual name must behave like a concrete one.
	normalized := virtualListedModels(cfg, providers, nil, false, "qwen-38")
	if len(normalized) != 1 || normalized[0].Name != "qwen38" {
		t.Fatalf("query qwen-38 = %+v, want qwen38 through normalization", normalized)
	}

	if got := virtualListedModels(cfg, providers, nil, false, "nothing-matches"); len(got) != 0 {
		t.Fatalf("unmatched query = %+v, want nothing", got)
	}
}

// The management listing filters concrete models by the keys selected with ?keys= and records which
// keys reach each one in AccessibleByKeys. Virtual names must go through the same filter: a virtual
// model is served by whichever candidate is resident, so a key reaches it only when it reaches every
// candidate, and the keys that do are reported the way concrete entries report them. Before this,
// ?keys= answered with virtual names none of the selected keys could serve, and left the field empty,
// which downstream reads as unrestricted.
func TestVirtualManagementListingAppliesKeysFilter(t *testing.T) {
	cfg := testVirtualConfig(t)
	all := schemas.Key{ID: "key-all", Models: schemas.WhiteList{"*"}}
	flashOnly := schemas.Key{ID: "key-flash", Models: schemas.WhiteList{"flash"}}
	cfg.Providers["omlx"] = configstore.ProviderConfig{
		NetworkConfig: &schemas.NetworkConfig{BaseURL: "http://127.0.0.1:1", AllowPrivateNetwork: true},
		Keys:          []schemas.Key{all, flashOnly},
	}
	h := &ProviderHandler{inMemoryStore: cfg}

	virtual := virtualListedModels(cfg, []schemas.ModelProvider{"omlx"}, nil, false, "")
	if len(virtual) != 1 {
		t.Fatalf("control failed: expected the omlx virtual name, got %+v", virtual)
	}

	// No keys selected: nothing to apply, entry kept with the field empty.
	unfiltered := h.virtualModelsForListing(nil, virtual, modelListQuery{})
	if len(unfiltered) != 1 || len(unfiltered[0].AccessibleByKeys) != 0 {
		t.Fatalf("no keys selected = %+v, want the entry kept with no keys attached", unfiltered)
	}

	// A key that misses one candidate cannot serve the model at all.
	partial := h.virtualModelsForListing(nil, virtual, modelListQuery{KeyIDs: []string{"key-flash"}})
	if len(partial) != 0 {
		t.Fatalf("key permitting one of two candidates = %+v, want it dropped", partial)
	}

	kept := h.virtualModelsForListing(nil, virtual, modelListQuery{KeyIDs: []string{"key-all"}})
	if len(kept) != 1 || len(kept[0].AccessibleByKeys) != 1 || kept[0].AccessibleByKeys[0] != "key-all" {
		t.Fatalf("key permitting every candidate = %+v, want it kept and attributed to key-all", kept)
	}

	// Both selected: only the key reaching both candidates is reported.
	both := h.virtualModelsForListing(nil, virtual, modelListQuery{KeyIDs: []string{"key-all", "key-flash"}})
	if len(both) != 1 || len(both[0].AccessibleByKeys) != 1 || both[0].AccessibleByKeys[0] != "key-all" {
		t.Fatalf("two selected keys = %+v, want AccessibleByKeys to hold only key-all", both)
	}

	// An unknown key selects nothing, so the model cannot be shown as reachable through it.
	unknown := h.virtualModelsForListing(nil, virtual, modelListQuery{KeyIDs: []string{"key-does-not-exist"}})
	if len(unknown) != 0 {
		t.Fatalf("unknown selected key = %+v, want the entry dropped", unknown)
	}

	// Unfiltered is an explicit opt-out of the key filter.
	opted := h.virtualModelsForListing(nil, virtual, modelListQuery{KeyIDs: []string{"key-flash"}, Unfiltered: true})
	if len(opted) != 1 {
		t.Fatalf("unfiltered request = %+v, want the entry kept", opted)
	}
}

// A provider that lists a concrete model under a virtual model's name must not get a second row for
// it: the duplicate inflates total and takes a pagination slot. The concrete entry is the one kept.
// The same name on a different provider is a different model and survives.
func TestVirtualManagementListingDoesNotDuplicateConcreteNames(t *testing.T) {
	cfg := testVirtualConfig(t)
	h := &ProviderHandler{inMemoryStore: cfg}

	virtual := virtualListedModels(cfg, []schemas.ModelProvider{"omlx", "ollama"}, nil, false, "")
	if len(virtual) != 2 {
		t.Fatalf("control failed: expected both virtual names, got %+v", virtual)
	}

	existing := []listedModel{
		{Name: "qwen38", Provider: "omlx"},
		{Name: "QWEN38", Provider: "ollama"}, // different provider: not a duplicate
	}
	kept := h.virtualModelsForListing(existing, virtual, modelListQuery{})
	if len(kept) != 1 || kept[0].Name != "small" || kept[0].Provider != "ollama" {
		t.Fatalf("kept = %+v, want only the ollama virtual name and qwen38@omlx deduplicated", kept)
	}

	// Case-insensitive on the matching side too: the collision is by identity, not spelling.
	cased := h.virtualModelsForListing([]listedModel{{Name: "SMALL", Provider: "ollama"}}, virtual, modelListQuery{})
	for _, row := range cased {
		if row.Provider == "ollama" && row.Name == "small" {
			t.Fatalf("case-insensitive dedupe missed %v", row)
		}
	}
}
