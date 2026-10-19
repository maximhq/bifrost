package integrations

import (
	"context"
	"testing"
	"time"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/valyala/fasthttp"
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

// The retrieve routes must coexist with the static list route rather than shadow it, and the
// catch-all has to survive a "provider/model" id, which is how Bifrost addresses models.
func TestModelRetrieveRoutesCoexistWithListModels(t *testing.T) {
	r := router.New()
	seen := map[string]bool{}
	var configs []RouteConfig
	configs = append(configs, CreateOpenAIRouteConfigs("/openai", nil)...)
	configs = append(configs, CreateOpenAIListModelsRouteConfigs("/openai", nil)...)
	configs = append(configs, CreateOpenAIModelRetrieveRouteConfigs("/openai", nil)...)
	for _, config := range configs {
		key := config.Method + " " + config.Path
		if seen[key] {
			continue
		}
		seen[key] = true
		matched := config.Path
		r.Handle(config.Method, config.Path, func(ctx *fasthttp.RequestCtx) { ctx.SetBodyString(matched) })
	}

	for _, test := range []struct {
		path      string
		wantRoute string
		wantModel string
	}{
		{"/openai/v1/models", "/openai/v1/models", ""},
		{"/openai/v1/models/gpt-5", "/openai/v1/models/{model:*}", "gpt-5"},
		{"/openai/v1/models/openai/gpt-5", "/openai/v1/models/{model:*}", "openai/gpt-5"},
	} {
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.Header.SetMethod("GET")
		ctx.Request.SetRequestURI(test.path)
		r.Handler(ctx)

		if got := string(ctx.Response.Body()); got != test.wantRoute {
			t.Errorf("%s matched route %q, want %q", test.path, got, test.wantRoute)
		}
		model, _ := ctx.UserValue("model").(string)
		if model != test.wantModel {
			t.Errorf("%s captured model %q, want %q", test.path, model, test.wantModel)
		}
	}
}

// The provider is decided before the request leaves the integration layer: an explicit header
// wins, a known prefix on the path is next, and a bare model falls back to OpenAI.
func TestExtractOpenAIModelRetrieveParams(t *testing.T) {
	for _, test := range []struct {
		name         string
		pathModel    string
		header       string
		wantProvider schemas.ModelProvider
		wantModel    string
	}{
		{"bare model defaults to openai", "gpt-5", "", schemas.OpenAI, "gpt-5"},
		{"known prefix picks the provider", "anthropic/claude-sonnet-4-5", "", schemas.Anthropic, "claude-sonnet-4-5"},
		{"header wins over the path", "gpt-5", "azure", schemas.ModelProvider("azure"), "gpt-5"},
		{"header strips its own prefix", "myproxy/gpt-5", "myproxy", schemas.ModelProvider("myproxy"), "gpt-5"},
		{"unknown prefix stays in the model", "myproxy/gpt-5", "", schemas.OpenAI, "myproxy/gpt-5"},
		{"encoded provider/model (what OpenAI SDKs send)", "anthropic%2Fclaude-sonnet-4-5", "", schemas.Anthropic, "claude-sonnet-4-5"},
		{"encoded namespaced id under the header", "openai%2Fgpt-oss-120b", "groq", schemas.ModelProvider("groq"), "openai/gpt-oss-120b"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			ctx.SetUserValue("model", test.pathModel)
			if test.header != "" {
				ctx.Request.Header.Set("x-model-provider", test.header)
			}
			req := &schemas.BifrostModelRetrieveRequest{}

			if err := extractOpenAIModelRetrieveParams(ctx, listModelsCtx(), req); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if req.Provider != test.wantProvider {
				t.Errorf("provider = %q, want %q", req.Provider, test.wantProvider)
			}
			if req.Model != test.wantModel {
				t.Errorf("model = %q, want %q", req.Model, test.wantModel)
			}
		})
	}
}

func TestExtractOpenAIModelRetrieveParamsRejectsMalformedEncoding(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.SetUserValue("model", "openai%2")
	if err := extractOpenAIModelRetrieveParams(ctx, listModelsCtx(), &schemas.BifrostModelRetrieveRequest{}); err == nil {
		t.Fatal("expected an error for a malformed percent-encoding")
	}
}

func TestExtractOpenAIModelRetrieveParamsRejectsEmptyModel(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.SetUserValue("model", "///")
	if err := extractOpenAIModelRetrieveParams(ctx, listModelsCtx(), &schemas.BifrostModelRetrieveRequest{}); err == nil {
		t.Fatal("expected an error for a path that carries no model")
	}
}
