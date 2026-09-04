package integrations

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/grant"
)

// stubAccessResolver records that it was asked and publishes what a fixed access grants, standing
// in for the server, which is what answers this in production.
type stubAccessResolver struct {
	providers []schemas.ModelProvider
	calls     int
}

func (s *stubAccessResolver) NarrowListModelsProviders(bifrostCtx *schemas.BifrostContext) {
	s.calls++
	if s.providers == nil {
		return
	}
	bifrostCtx.SetValue(schemas.BifrostContextKeyAvailableProviders, s.providers)
}

func listModelsCtx() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), time.Time{})
}

// An all-providers listing asks the resolver to narrow the fan-out before it runs.
func TestListModelsNarrowsTheFanOut(t *testing.T) {
	resolver := &stubAccessResolver{providers: []schemas.ModelProvider{schemas.OpenAI, schemas.Anthropic}}
	g := NewGenericRouter(nil, &mockHandlerStore{}, resolver, nil, nil, nil)
	bifrostCtx := listModelsCtx()

	if g.accessResolver == nil {
		t.Fatal("expected the router to hold the resolver it was constructed with")
	}
	g.accessResolver.NarrowListModelsProviders(bifrostCtx)

	if resolver.calls != 1 {
		t.Fatalf("narrow calls = %d, want 1", resolver.calls)
	}
	got, ok := bifrostCtx.Value(schemas.BifrostContextKeyAvailableProviders).([]schemas.ModelProvider)
	if !ok {
		t.Fatalf("expected available providers to be published as []schemas.ModelProvider, got %#v",
			bifrostCtx.Value(schemas.BifrostContextKeyAvailableProviders))
	}
	want := []schemas.ModelProvider{schemas.OpenAI, schemas.Anthropic}
	if len(got) != len(want) {
		t.Fatalf("expected providers %#v, got %#v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected providers %#v, got %#v", want, got)
		}
	}
}

// A resolver that cannot narrow leaves the fan-out alone rather than narrowing it to nothing.
func TestListModelsLeavesFanOutAloneWhenNothingResolved(t *testing.T) {
	resolver := &stubAccessResolver{}
	g := NewGenericRouter(nil, &mockHandlerStore{}, resolver, nil, nil, nil)
	bifrostCtx := listModelsCtx()

	g.accessResolver.NarrowListModelsProviders(bifrostCtx)

	if resolver.calls != 1 {
		t.Fatalf("narrow calls = %d, want 1", resolver.calls)
	}
	if got := bifrostCtx.Value(schemas.BifrostContextKeyAvailableProviders); got != nil {
		t.Fatalf("expected nothing to be published, got %#v", got)
	}
}

// A router built without a resolver lists as it did before: the call site guards on nil, so
// narrowing stays an optimization the route works without.
func TestListModelsWithoutAccessResolver(t *testing.T) {
	g := NewGenericRouter(nil, &mockHandlerStore{}, nil, nil, nil, nil)

	if g.accessResolver != nil {
		t.Fatalf("expected no resolver, got %#v", g.accessResolver)
	}
}

// Guards the wiring the fix depends on: a grant that names providers is what the server turns
// into the published list, so this pins the shape the router's resolver must produce.
func TestGrantedProvidersShapeMatchesWhatTheRouterPublishes(t *testing.T) {
	permit := grant.NewPermit(grant.PermitVirtualKey, "vk-test", "Test VK", true, false, []schemas.ProviderPermit{
		{Provider: "openai", AllowedModels: []string{"gpt-4o"}},
		// A provider granted no model at all is still asked: the fan-out decides who can
		// serve the request, and the response is filtered per model afterwards.
		{Provider: "anthropic"},
	}, nil)
	access := grant.NewAccess([]schemas.Permit{permit}, nil, "", nil)

	granted := access.GrantedProvidersForModel("")
	want := []string{"openai", "anthropic"}
	if len(granted) != len(want) {
		t.Fatalf("expected granted providers %#v, got %#v", want, granted)
	}
	for i := range want {
		if granted[i] != want[i] {
			t.Fatalf("expected granted providers %#v, got %#v", want, granted)
		}
	}
}
