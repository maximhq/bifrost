package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/grant"
)

// The fan-out is narrowed to the providers the request is granted.
func TestApplyListModelsProviderFilterPublishesGrantedProviders(t *testing.T) {
	permit := grant.NewPermit(grant.PermitVirtualKey, "vk-test", "Test VK", true, false, []schemas.ProviderPermit{
		{Provider: "openai", AllowedModels: []string{"gpt-4o"}},
		// A provider granted no model at all is still asked: the fan-out decides who can
		// serve the request, and the response is filtered per model afterwards.
		{Provider: "anthropic"},
	}, nil)
	h := &CompletionHandler{
		modelsManager: &mockModelsManager{
			access: grant.NewAccess([]schemas.Permit{permit}, nil, "", nil),
		},
	}

	bifrostCtx := schemas.NewBifrostContext(context.Background(), time.Time{})

	h.applyListModelsProviderFilter(bifrostCtx)

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

// Nothing resolved must leave the fan-out alone. Publishing an empty list here would mean "no
// provider may serve this", turning an unrestricted request into one that lists nothing.
func TestApplyListModelsProviderFilterLeavesFanOutAloneWhenNothingResolved(t *testing.T) {
	manager := &mockModelsManager{}
	h := &CompletionHandler{modelsManager: manager}

	bifrostCtx := schemas.NewBifrostContext(context.Background(), time.Time{})

	h.applyListModelsProviderFilter(bifrostCtx)

	if manager.resolveCalls != 1 {
		t.Fatalf("resolve calls = %d, want 1", manager.resolveCalls)
	}
	if got := bifrostCtx.Value(schemas.BifrostContextKeyAvailableProviders); got != nil {
		t.Fatalf("expected nothing to be published, got %#v", got)
	}
}

// Narrowing is an optimization, not a permission check, so a handler wired without a models
// manager falls through instead of panicking on the request path.
func TestApplyListModelsProviderFilterWithoutModelsManager(t *testing.T) {
	h := &CompletionHandler{}

	bifrostCtx := schemas.NewBifrostContext(context.Background(), time.Time{})

	h.applyListModelsProviderFilter(bifrostCtx)

	if got := bifrostCtx.Value(schemas.BifrostContextKeyAvailableProviders); got != nil {
		t.Fatalf("expected nothing to be published, got %#v", got)
	}
}
