package handlers

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
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
