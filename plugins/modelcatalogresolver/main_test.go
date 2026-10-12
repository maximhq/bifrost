package modelcatalogresolver

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

// testCatalog serves "shared-model" from anthropic, azure and openai, and "solo-model" from openai
// alone, as the live cache holds them once each provider's list-models response has landed.
func testCatalog() *modelcatalog.ModelCatalog {
	catalog := modelcatalog.NewTestCatalog(nil)
	catalog.UpsertLive(schemas.OpenAI, "openai-key", false, []string{"shared-model", "solo-model"})
	catalog.UpsertLive(schemas.Azure, "azure-key", false, []string{"shared-model"})
	catalog.UpsertLive(schemas.Anthropic, "anthropic-key", false, []string{"shared-model"})
	return catalog
}

func newTestContext() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
}

// catalogLogs returns the messages the resolver wrote to the request's routing trail.
func catalogLogs(ctx *schemas.BifrostContext) []string {
	var out []string
	for _, entry := range ctx.GetRoutingEngineLogs() {
		if entry.Engine == schemas.RoutingEngineModelCatalog {
			out = append(out, entry.Message)
		}
	}
	return out
}

func requireLog(t *testing.T, ctx *schemas.BifrostContext, want string) {
	t.Helper()
	for _, message := range catalogLogs(ctx) {
		if strings.Contains(message, want) {
			return
		}
	}
	t.Fatalf("no model-catalog trail entry contains %q: %v", want, catalogLogs(ctx))
}

func TestInitRequiresACatalog(t *testing.T) {
	if _, err := Init(nil, nil); err == nil {
		t.Fatal("Init accepted a nil catalog")
	}
}

// TestResolveProviderFromCatalog pins how a provider is picked for a model named without one. The
// candidates are sorted so that, with nothing else to go on, the pick is the same on every process.
// The drop-in route the request came in on prefers its own provider when that provider is a
// candidate, an Azure OpenAI SDK on the openai route prefers azure, and an allowlist an earlier
// plugin set narrows the candidates before any of that.
func TestResolveProviderFromCatalog(t *testing.T) {
	everyCandidate := []schemas.ModelProvider{schemas.Anthropic, schemas.Azure, schemas.OpenAI}
	for _, tc := range []struct {
		name           string
		model          string
		integration    string
		azureSDK       bool
		allowed        []schemas.ModelProvider
		wantSelected   schemas.ModelProvider
		wantCandidates []schemas.ModelProvider
		wantLog        string
	}{
		{
			name:    "a model no provider serves resolves to nothing",
			model:   "unknown-model",
			wantLog: "Model catalog has no provider serving model unknown-model",
		},
		{
			name:           "with nothing to go on the alphabetically first candidate is picked",
			model:          "shared-model",
			wantSelected:   schemas.Anthropic,
			wantCandidates: everyCandidate,
		},
		{
			name:           "the openai route prefers openai",
			model:          "shared-model",
			integration:    "openai",
			wantSelected:   schemas.OpenAI,
			wantCandidates: everyCandidate,
		},
		{
			name:           "an Azure OpenAI SDK on the openai route prefers azure",
			model:          "shared-model",
			integration:    "openai",
			azureSDK:       true,
			wantSelected:   schemas.Azure,
			wantCandidates: everyCandidate,
		},
		{
			name:           "an Azure OpenAI SDK falls back to openai when azure does not serve the model",
			model:          "solo-model",
			integration:    "openai",
			azureSDK:       true,
			wantSelected:   schemas.OpenAI,
			wantCandidates: []schemas.ModelProvider{schemas.OpenAI},
		},
		{
			name:           "a route whose provider is not a candidate keeps the first candidate",
			model:          "shared-model",
			integration:    "genai",
			wantSelected:   schemas.Anthropic,
			wantCandidates: everyCandidate,
		},
		{
			name:           "the allowlist narrows the candidates before the pick",
			model:          "shared-model",
			allowed:        []schemas.ModelProvider{schemas.OpenAI, schemas.Azure},
			wantSelected:   schemas.Azure,
			wantCandidates: []schemas.ModelProvider{schemas.Azure, schemas.OpenAI},
			wantLog:        "provider allowlist is [openai, azure], so excluded 1; remaining providers are [azure, openai]",
		},
		{
			name:           "the allowlist outranks the route's preference",
			model:          "shared-model",
			integration:    "anthropic",
			allowed:        []schemas.ModelProvider{schemas.OpenAI},
			wantSelected:   schemas.OpenAI,
			wantCandidates: []schemas.ModelProvider{schemas.OpenAI},
		},
		{
			name:    "an allowlist that excludes every candidate resolves to nothing",
			model:   "shared-model",
			allowed: []schemas.ModelProvider{schemas.Gemini},
			wantLog: "provider allowlist [gemini] excluded all of them",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := newTestContext()
			if tc.integration != "" {
				ctx.SetValue(schemas.BifrostContextKeyIntegrationType, tc.integration)
			}
			if tc.azureSDK {
				ctx.SetValue(schemas.BifrostContextKeyIsAzureUserAgent, true)
			}
			if tc.allowed != nil {
				ctx.SetValue(schemas.BifrostContextKeyRoutingAllowedProviders, tc.allowed)
			}
			selected, candidates := ResolveProviderFromCatalog(ctx, testCatalog(), tc.model)
			if selected != tc.wantSelected {
				t.Fatalf("selected %q, want %q", selected, tc.wantSelected)
			}
			if !slices.Equal(candidates, tc.wantCandidates) {
				t.Fatalf("candidates %v, want %v", candidates, tc.wantCandidates)
			}
			if tc.wantLog != "" {
				requireLog(t, ctx, tc.wantLog)
			}
		})
	}

	t.Run("the realtime paths resolve without a request context", func(t *testing.T) {
		selected, candidates := ResolveProviderFromCatalog(nil, testCatalog(), "shared-model")
		if selected != schemas.Anthropic || !slices.Equal(candidates, everyCandidate) {
			t.Fatalf("got %q from %v, want anthropic from %v", selected, candidates, everyCandidate)
		}
	})
}

// TestPreRequestHook pins what the resolver does to a request. A model named without a provider is
// given the picked provider, and when the caller set no fallbacks of their own, every other candidate
// becomes a fallback for the same model. A provider the caller named, a passthrough request and the
// caller's own fallbacks are never touched.
func TestPreRequestHook(t *testing.T) {
	chat := func(provider schemas.ModelProvider, model string, fallbacks []schemas.Fallback) *schemas.BifrostRequest {
		return &schemas.BifrostRequest{
			RequestType: schemas.ChatCompletionRequest,
			ChatRequest: &schemas.BifrostChatRequest{Provider: provider, Model: model, Fallbacks: fallbacks},
		}
	}
	callerFallbacks := []schemas.Fallback{{Provider: schemas.Gemini, Model: "gemini-2.5-flash"}}
	for _, tc := range []struct {
		name          string
		req           *schemas.BifrostRequest
		allowed       []schemas.ModelProvider
		untouched     bool
		wantProvider  schemas.ModelProvider
		wantFallbacks []schemas.Fallback
		wantLogs      []string
	}{
		{
			name:         "a bare model gets the picked provider and every other candidate as a fallback",
			req:          chat("", "shared-model", nil),
			wantProvider: schemas.Anthropic,
			wantFallbacks: []schemas.Fallback{
				{Provider: schemas.Azure, Model: "shared-model"},
				{Provider: schemas.OpenAI, Model: "shared-model"},
			},
			wantLogs: []string{
				"No provider specified for model shared-model, found 3 options in model catalog: [anthropic, azure, openai], selected: anthropic",
				"Added 2 catalog fallback provider(s) for model shared-model: [azure, openai]",
			},
		},
		{
			name:          "the caller's own fallbacks are kept",
			req:           chat("", "shared-model", callerFallbacks),
			wantProvider:  schemas.Anthropic,
			wantFallbacks: callerFallbacks,
		},
		{
			name:         "a model only one provider serves gets no fallbacks",
			req:          chat("", "solo-model", nil),
			wantProvider: schemas.OpenAI,
		},
		{
			name:          "the allowlist decides the fallbacks as well as the pick",
			req:           chat("", "shared-model", nil),
			allowed:       []schemas.ModelProvider{schemas.OpenAI, schemas.Azure},
			wantProvider:  schemas.Azure,
			wantFallbacks: []schemas.Fallback{{Provider: schemas.OpenAI, Model: "shared-model"}},
		},
		{
			name: "an embedding request is resolved the same way",
			req: &schemas.BifrostRequest{
				RequestType:      schemas.EmbeddingRequest,
				EmbeddingRequest: &schemas.BifrostEmbeddingRequest{Model: "solo-model"},
			},
			wantProvider: schemas.OpenAI,
		},
		{
			name:     "a model no provider serves leaves the provider empty and says the request will fail",
			req:      chat("", "unknown-model", nil),
			wantLogs: []string{"No provider could be resolved for model unknown-model; request will fail provider validation"},
		},
		{
			name:         "a provider the caller named is left alone",
			req:          chat(schemas.OpenAI, "shared-model", nil),
			untouched:    true,
			wantProvider: schemas.OpenAI,
		},
		{
			name: "a passthrough request is left alone",
			req: &schemas.BifrostRequest{
				RequestType:        schemas.PassthroughRequest,
				PassthroughRequest: &schemas.BifrostPassthroughRequest{Model: "shared-model"},
			},
			untouched: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plugin, err := Init(testCatalog(), nil)
			if err != nil {
				t.Fatalf("Init: %v", err)
			}
			ctx := newTestContext()
			if tc.allowed != nil {
				ctx.SetValue(schemas.BifrostContextKeyRoutingAllowedProviders, tc.allowed)
			}
			if err := plugin.PreRequestHook(ctx, tc.req); err != nil {
				t.Fatalf("PreRequestHook: %v", err)
			}
			provider, _, fallbacks := tc.req.GetRequestFields()
			if provider != tc.wantProvider {
				t.Fatalf("provider %q, want %q", provider, tc.wantProvider)
			}
			if !slices.Equal(fallbacks, tc.wantFallbacks) {
				t.Fatalf("fallbacks %v, want %v", fallbacks, tc.wantFallbacks)
			}
			for _, want := range tc.wantLogs {
				requireLog(t, ctx, want)
			}
			engines, _ := ctx.Value(schemas.BifrostContextKeyRoutingEnginesUsed).([]string)
			resolved := !tc.untouched && tc.wantProvider != ""
			if got := slices.Contains(engines, schemas.RoutingEngineModelCatalog); got != resolved {
				t.Fatalf("model-catalog recorded as a routing engine used: %v, want %v (%v)", got, resolved, engines)
			}
			if tc.untouched && len(catalogLogs(ctx)) > 0 {
				t.Fatalf("a request the resolver should not touch has trail entries: %v", catalogLogs(ctx))
			}
		})
	}
}
