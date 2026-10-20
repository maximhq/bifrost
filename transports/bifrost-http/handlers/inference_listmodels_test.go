package handlers

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/valyala/fasthttp"
)

// The listing hands the narrowing to whoever can answer what the request may reach, so the
// providers it publishes are the ones that answer grants.
func TestApplyListModelsProviderFilterDelegatesToTheModelsManager(t *testing.T) {
	permit := grant.NewPermit(grant.PermitVirtualKey, "vk-test", "Test VK", true, false, []schemas.ProviderPermit{
		{Provider: "openai", AllowedModels: []string{"gpt-4o"}},
		// A provider granted no model at all is still asked: the fan-out decides who can
		// serve the request, and the response is filtered per model afterwards.
		{Provider: "anthropic"},
	}, nil)
	manager := &mockModelsManager{access: grant.NewAccess([]schemas.Permit{permit}, nil, "", nil)}
	h := &CompletionHandler{modelsManager: manager}

	bifrostCtx := schemas.NewBifrostContext(context.Background(), time.Time{})

	h.applyListModelsProviderFilter(bifrostCtx)

	if manager.narrowCalls != 1 {
		t.Fatalf("narrow calls = %d, want 1", manager.narrowCalls)
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

// Nothing resolved must leave the fan-out alone. Publishing an empty list here would mean "no
// provider may serve this", turning an unrestricted request into one that lists nothing.
func TestApplyListModelsProviderFilterLeavesFanOutAloneWhenNothingResolved(t *testing.T) {
	manager := &mockModelsManager{}
	h := &CompletionHandler{modelsManager: manager}

	bifrostCtx := schemas.NewBifrostContext(context.Background(), time.Time{})

	h.applyListModelsProviderFilter(bifrostCtx)

	if manager.narrowCalls != 1 {
		t.Fatalf("narrow calls = %d, want 1", manager.narrowCalls)
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

// OpenAI SDKs percent-encode the id ("openai%2Fgpt-4o-mini") and the router hands the
// catch-all over still encoded, so the target must be read after decoding it once.
func TestModelRetrieveTarget(t *testing.T) {
	for _, test := range []struct {
		name         string
		pathModel    string
		query        string
		wantProvider schemas.ModelProvider
		wantModel    string
		wantErr      string
	}{
		{name: "literal provider/model", pathModel: "openai/gpt-4o-mini", wantProvider: schemas.OpenAI, wantModel: "gpt-4o-mini"},
		{name: "encoded provider/model", pathModel: "openai%2Fgpt-4o-mini", wantProvider: schemas.OpenAI, wantModel: "gpt-4o-mini"},
		{name: "encoded namespaced id", pathModel: "groq%2Fopenai%2Fgpt-oss-120b", wantProvider: schemas.Groq, wantModel: "openai/gpt-oss-120b"},
		{name: "encoded id under ?provider=", pathModel: "openai%2Fgpt-oss-120b", query: "provider=groq", wantProvider: schemas.Groq, wantModel: "openai/gpt-oss-120b"},
		{name: "bare model without a provider", pathModel: "gpt-4o-mini", wantErr: "provider is required"},
		{name: "no model", pathModel: "%2F", wantErr: "model is required"},
		{name: "malformed encoding", pathModel: "openai%2", wantErr: "invalid model encoding"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.SetRequestURI("/v1/models/x?" + test.query)
			ctx.SetUserValue("model", test.pathModel)

			provider, model, err := modelRetrieveTarget(ctx)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if provider != test.wantProvider || model != test.wantModel {
				t.Errorf("got (%q, %q), want (%q, %q)", provider, model, test.wantProvider, test.wantModel)
			}
		})
	}
}
