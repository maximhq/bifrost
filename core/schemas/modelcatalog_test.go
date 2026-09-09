package schemas

import (
	"testing"
)

// stubModelInfoProvider records what the context delegated to it.
type stubModelInfoProvider struct {
	model    *Model
	cost     float64
	gotProv  ModelProvider
	gotModel string
	gotCtx   *BifrostContext
	calls    int
}

func (s *stubModelInfoProvider) GetModelInfo(provider ModelProvider, model string) *Model {
	s.calls++
	s.gotProv = provider
	s.gotModel = model
	return s.model
}

func (s *stubModelInfoProvider) CalculateRequestCost(ctx *BifrostContext, resp *BifrostResponse) float64 {
	s.calls++
	s.gotCtx = ctx
	return s.cost
}

// A context with no catalog wired must stay silently inert rather than panic;
// core is usable as a standalone SDK without the framework, and plugins run
// unchanged in both setups.
func TestModelInfoAccessorsNoCatalogWired(t *testing.T) {
	ctx := NewBifrostContext(nil, NoDeadline)

	if got := ctx.GetModelInfo(Anthropic, "claude-opus-5"); got != nil {
		t.Fatalf("GetModelInfo with no catalog = %v, want nil", got)
	}
	if got := ctx.CalculateCost(&BifrostResponse{}); got != 0 {
		t.Fatalf("CalculateCost with no catalog = %v, want 0", got)
	}
}

func TestModelInfoAccessorsDelegate(t *testing.T) {
	want := &Model{ID: "claude-opus-5"}
	stub := &stubModelInfoProvider{model: want, cost: 1.25}

	ctx := NewBifrostContext(nil, NoDeadline)
	ctx.SetValue(BifrostContextKeyModelCatalog, stub)

	got := ctx.GetModelInfo(Anthropic, "claude-opus-5")
	if got != want {
		t.Fatalf("GetModelInfo = %v, want %v", got, want)
	}
	if stub.gotProv != Anthropic || stub.gotModel != "claude-opus-5" {
		t.Fatalf("delegated (%q, %q), want (anthropic, claude-opus-5)", stub.gotProv, stub.gotModel)
	}

	if cost := ctx.CalculateCost(&BifrostResponse{}); cost != 1.25 {
		t.Fatalf("CalculateCost = %v, want 1.25", cost)
	}
}

// Guard the arguments that make the delegate call pointless, so a catalog
// never sees an empty model or a nil response.
func TestModelInfoAccessorsSkipEmptyArgs(t *testing.T) {
	stub := &stubModelInfoProvider{model: &Model{ID: "x"}, cost: 9}
	ctx := NewBifrostContext(nil, NoDeadline)
	ctx.SetValue(BifrostContextKeyModelCatalog, stub)

	if got := ctx.GetModelInfo(Anthropic, ""); got != nil {
		t.Fatalf("GetModelInfo with empty model = %v, want nil", got)
	}
	if got := ctx.CalculateCost(nil); got != 0 {
		t.Fatalf("CalculateCost with nil response = %v, want 0", got)
	}
	if stub.calls != 0 {
		t.Fatalf("delegated %d times for empty args, want 0", stub.calls)
	}
}

// The whole design rests on plugin-scoped contexts delegating Value lookups to
// the root: core stamps the catalog once per request, and every plugin scope
// minted from it must see the handle without any extra wiring.
func TestModelInfoVisibleFromPluginScope(t *testing.T) {
	want := &Model{ID: "gpt-5"}
	stub := &stubModelInfoProvider{model: want, cost: 2}

	root := NewBifrostContext(nil, NoDeadline)
	root.SetValue(BifrostContextKeyModelCatalog, stub)

	name := "my-plugin"
	scoped := root.WithPluginScope(&name)
	defer scoped.ReleasePluginScope()

	if got := scoped.GetModelInfo(OpenAI, "gpt-5"); got != want {
		t.Fatalf("GetModelInfo from plugin scope = %v, want %v", got, want)
	}
	if got := scoped.CalculateCost(&BifrostResponse{}); got != 2 {
		t.Fatalf("CalculateCost from plugin scope = %v, want 2", got)
	}
	// CalculateRequestCost receives the scoped context, which is what the
	// governance scope lookup reads request identity from.
	if stub.gotCtx != scoped {
		t.Fatal("CalculateRequestCost did not receive the scoped context")
	}

	// Nested scopes (plugin pipelines that re-scope) must still resolve.
	inner := scoped.WithPluginScope(&name)
	defer inner.ReleasePluginScope()
	if got := inner.GetModelInfo(OpenAI, "gpt-5"); got != want {
		t.Fatalf("GetModelInfo from nested scope = %v, want %v", got, want)
	}
}

// Fallbacks and per-provider fan-out derive a fresh BifrostContext from the
// request context rather than scoping it. Those children inherit through the
// parent chain, not valueDelegate. That is the reason the handle lives in the value
// map instead of a struct field.
func TestModelInfoVisibleFromDerivedContext(t *testing.T) {
	want := &Model{ID: "gemini-3-pro"}
	stub := &stubModelInfoProvider{model: want}

	root := NewBifrostContext(nil, NoDeadline)
	root.SetValue(BifrostContextKeyModelCatalog, stub)

	derived := NewBifrostContext(root, NoDeadline)
	if got := derived.GetModelInfo(Gemini, "gemini-3-pro"); got != want {
		t.Fatalf("GetModelInfo from derived context = %v, want %v", got, want)
	}
}
