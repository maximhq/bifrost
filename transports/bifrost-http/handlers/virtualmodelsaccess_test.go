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
	"github.com/valyala/fasthttp"
)

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
			retrieved, handled := virtualModelRetrieve(ctx, cfg, "qwen38", tt.access, false)
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

// A provider-prefixed name must not address another provider's virtual model: residency was
// measured on the owning provider, so honouring the prefix would hand out a mapping nobody
// checked. It is handled (never forwarded upstream) and answers nothing.
func TestVirtualRetrieveRejectsWrongProvider(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	defer ctx.Cancel()
	resp, handled := virtualModelRetrieve(ctx, testVirtualConfig(t), "ollama/qwen38", nil, false)
	if !handled || resp != nil {
		t.Fatalf("wrong provider prefix returned %+v handled=%v, want nothing handled", resp, handled)
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
		NetworkConfig:  &schemas.NetworkConfig{BaseURL: server.URL, AllowPrivateNetwork: true},
		ModelResidency: &schemas.ModelResidencyConfig{Adapter: schemas.ResidencyAdapterOMLX},
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
	resp, handled := virtualModelRetrieve(ctx, cfg, "qwen38", nil, true)
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
