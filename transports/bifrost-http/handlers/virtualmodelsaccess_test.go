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
			appendVirtualModels(listing, cfg, "", tt.access)
			if len(listing.Data) != tt.want {
				t.Fatalf("inference listing = %+v, want %d entries", listing.Data, tt.want)
			}

			management := virtualListedModels(cfg, []schemas.ModelProvider{"omlx", "ollama"}, tt.access)
			if len(management) != tt.want {
				t.Fatalf("management listing = %+v, want %d entries", management, tt.want)
			}

			ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
			defer ctx.Cancel()
			retrieved, handled := virtualModelRetrieve(ctx, cfg, "qwen38", tt.access)
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
	appendVirtualModels(listing, cfg, "", nil)
	if len(listing.Data) != 3 {
		t.Fatalf("got %d entries, want both virtual names beside the concrete one", len(listing.Data))
	}
	if got := len(virtualListedModels(cfg, []schemas.ModelProvider{"omlx"}, nil)); got != 1 {
		t.Fatalf("management listing = %d, want the omlx virtual name", got)
	}
}

// A provider-prefixed name must not address another provider's virtual model: residency was
// measured on the owning provider, so honouring the prefix would hand out a mapping nobody
// checked. It is handled (never forwarded upstream) and answers nothing.
func TestVirtualRetrieveRejectsWrongProvider(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	defer ctx.Cancel()
	resp, handled := virtualModelRetrieve(ctx, testVirtualConfig(t), "ollama/qwen38", nil)
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
		virtualModelAllowed(vm, access)
	}
}
