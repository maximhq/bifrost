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
			name:           "the azure route prefers azure",
			model:          "shared-model",
			integration:    "azure",
			wantSelected:   schemas.Azure,
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
			// Marked Inferred: core does not let a chain nobody asked for answer a request the
			// primary refused as malformed.
			name:         "a bare model gets the picked provider and every other candidate as an inferred fallback",
			req:          chat("", "shared-model", nil),
			wantProvider: schemas.Anthropic,
			wantFallbacks: []schemas.Fallback{
				{Provider: schemas.Azure, Model: "shared-model", Inferred: true},
				{Provider: schemas.OpenAI, Model: "shared-model", Inferred: true},
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
			wantFallbacks: []schemas.Fallback{{Provider: schemas.OpenAI, Model: "shared-model", Inferred: true}},
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

// TestPreRequestHookDecisionWithSeveralProviders pins that a decision model named without a provider
// is not sent to the alphabetically first of several providers serving it: the provider stays unset
// and the reason core returns names every candidate. One candidate, or an integration route whose
// own provider is a candidate, resolves as for any other request; a chat request with the same
// candidates keeps the alphabetical pick (TestPreRequestHook).
func TestPreRequestHookDecisionWithSeveralProviders(t *testing.T) {
	for _, tc := range []struct {
		name         string
		model        string
		integration  string
		alsoServedBy schemas.ModelProvider // also serves shared-model, beyond testCatalog
		allowed      []schemas.ModelProvider
		wantProvider schemas.ModelProvider
		wantReason   string
	}{
		{
			name:       "two candidates after the allowlist are refused with both named",
			model:      "shared-model",
			allowed:    []schemas.ModelProvider{schemas.OpenAI, schemas.Azure},
			wantReason: "decision model shared-model is served by more than one provider (azure, openai); send it as provider/model, for example azure/shared-model, or add a routing rule that sets the provider",
		},
		{
			name:         "an allowlist that leaves one candidate resolves to it",
			model:        "shared-model",
			allowed:      []schemas.ModelProvider{schemas.Azure},
			wantProvider: schemas.Azure,
		},
		{
			name:         "a model one provider serves resolves to it",
			model:        "solo-model",
			wantProvider: schemas.OpenAI,
		},
		{
			name:         "the openai route still resolves to openai",
			model:        "shared-model",
			integration:  "openai",
			wantProvider: schemas.OpenAI,
		},
		{
			name:         "the typesafe route resolves to typesafe",
			model:        "shared-model",
			integration:  "typesafe",
			alsoServedBy: schemas.Typesafe,
			wantProvider: schemas.Typesafe,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog := testCatalog()
			if tc.alsoServedBy != "" {
				catalog.UpsertLive(tc.alsoServedBy, string(tc.alsoServedBy)+"-key", false, []string{"shared-model"})
			}
			plugin, err := Init(catalog, nil)
			if err != nil {
				t.Fatalf("Init: %v", err)
			}
			ctx := newTestContext()
			if tc.allowed != nil {
				ctx.SetValue(schemas.BifrostContextKeyRoutingAllowedProviders, tc.allowed)
			}
			if tc.integration != "" {
				ctx.SetValue(schemas.BifrostContextKeyIntegrationType, tc.integration)
			}
			req := &schemas.BifrostRequest{
				RequestType:     schemas.DecisionRequest,
				DecisionRequest: &schemas.BifrostDecisionRequest{Model: tc.model},
			}
			if err := plugin.PreRequestHook(ctx, req); err != nil {
				t.Fatalf("PreRequestHook: %v", err)
			}
			provider, _, fallbacks := req.GetRequestFields()
			if provider != tc.wantProvider {
				t.Fatalf("provider %q, want %q", provider, tc.wantProvider)
			}
			reason, _ := ctx.Value(schemas.BifrostContextKeyProviderResolutionError).(string)
			if reason != tc.wantReason {
				t.Fatalf("resolution error %q, want %q", reason, tc.wantReason)
			}
			if tc.wantReason != "" && len(fallbacks) != 0 {
				t.Fatalf("an unresolved decision request got fallbacks %v", fallbacks)
			}
		})
	}
}

// TestKeySelectionBypass pins the resolution of a request that skips key selection (Claude Code
// OAuth passthrough, issue #5792). Such a request is pinned to the provider of the route it came in
// on only when the catalog cannot resolve the model, because a zero-key passthrough provider never
// appears in the catalog. Whenever the catalog does resolve, its pick, candidates and fallbacks are
// unchanged, and a routing allowlist that excludes the route's provider prevents the pin.
func TestKeySelectionBypass(t *testing.T) {
	everyCandidate := []schemas.ModelProvider{schemas.Anthropic, schemas.Azure, schemas.OpenAI}
	for _, tc := range []struct {
		name           string
		model          string
		skip           bool
		integration    string
		allowed        []schemas.ModelProvider
		wantSelected   schemas.ModelProvider
		wantCandidates []schemas.ModelProvider
	}{
		{
			name:         "a bare model on the anthropic route resolves to anthropic although the catalog has no provider for it",
			model:        "claude-sonnet-4-6",
			skip:         true,
			integration:  "anthropic",
			wantSelected: schemas.Anthropic,
		},
		{
			name:           "on the anthropic route the catalog pick and its candidates stand when the catalog serves the model",
			model:          "shared-model",
			skip:           true,
			integration:    "anthropic",
			wantSelected:   schemas.Anthropic,
			wantCandidates: everyCandidate,
		},
		{
			name:           "on the anthropic route a model only another provider serves still resolves to that provider",
			model:          "solo-model",
			skip:           true,
			integration:    "anthropic",
			wantSelected:   schemas.OpenAI,
			wantCandidates: []schemas.ModelProvider{schemas.OpenAI},
		},
		{
			name:           "an allowlist that prunes the catalog's candidates still leaves the allowed one to the catalog",
			model:          "shared-model",
			skip:           true,
			integration:    "anthropic",
			allowed:        []schemas.ModelProvider{schemas.Azure},
			wantSelected:   schemas.Azure,
			wantCandidates: []schemas.ModelProvider{schemas.Azure},
		},
		{
			name:         "an allowlist that prunes every candidate but allows the route's provider pins to it",
			model:        "solo-model",
			skip:         true,
			integration:  "anthropic",
			allowed:      []schemas.ModelProvider{schemas.Anthropic},
			wantSelected: schemas.Anthropic,
		},
		{
			name:        "an allowlist that excludes the route's provider prevents the pin",
			model:       "claude-sonnet-4-6",
			skip:        true,
			integration: "anthropic",
			allowed:     []schemas.ModelProvider{schemas.Bedrock},
		},
		{
			name:        "without the flag a model the catalog does not serve still resolves to nothing",
			model:       "claude-sonnet-4-6",
			integration: "anthropic",
		},
		{
			name:        "a route with no canonical provider falls through to the catalog",
			model:       "claude-sonnet-4-6",
			skip:        true,
			integration: "langchain",
		},
		{
			name:           "a request with no route falls through to the catalog",
			model:          "shared-model",
			skip:           true,
			wantSelected:   schemas.Anthropic,
			wantCandidates: everyCandidate,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := newTestContext()
			if tc.skip {
				ctx.SetValue(schemas.BifrostContextKeySkipKeySelection, true)
			}
			if tc.integration != "" {
				ctx.SetValue(schemas.BifrostContextKeyIntegrationType, tc.integration)
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
		})
	}

	runHook := func(t *testing.T, model string) (*schemas.BifrostContext, *schemas.BifrostRequest) {
		t.Helper()
		plugin, err := Init(testCatalog(), nil)
		if err != nil {
			t.Fatalf("Init: %v", err)
		}
		ctx := newTestContext()
		ctx.SetValue(schemas.BifrostContextKeySkipKeySelection, true)
		ctx.SetValue(schemas.BifrostContextKeyIntegrationType, "anthropic")
		req := &schemas.BifrostRequest{
			RequestType: schemas.ChatCompletionRequest,
			ChatRequest: &schemas.BifrostChatRequest{Model: model},
		}
		if err := plugin.PreRequestHook(ctx, req); err != nil {
			t.Fatalf("PreRequestHook: %v", err)
		}
		return ctx, req
	}

	t.Run("the hook pins the provider, adds no fallbacks and logs the pin rather than a catalog pick", func(t *testing.T) {
		ctx, req := runHook(t, "claude-sonnet-4-6")
		provider, _, fallbacks := req.GetRequestFields()
		if provider != schemas.Anthropic || len(fallbacks) != 0 {
			t.Fatalf("got provider %q with fallbacks %v, want anthropic with none", provider, fallbacks)
		}
		requireLog(t, ctx, "key selection is bypassed, so pinned to the integration's provider anthropic")
		for _, message := range catalogLogs(ctx) {
			if strings.Contains(message, "options in model catalog") || strings.Contains(message, "request will fail provider validation") {
				t.Fatalf("the pin was logged as a catalog pick or a failure: %q", message)
			}
		}
		engines, _ := ctx.Value(schemas.BifrostContextKeyRoutingEnginesUsed).([]string)
		if !slices.Contains(engines, schemas.RoutingEngineModelCatalog) {
			t.Fatalf("model-catalog not recorded as a routing engine used: %v", engines)
		}
	})

	t.Run("with the flag set a model the catalog serves keeps its catalog fallbacks and is not logged as a pin", func(t *testing.T) {
		ctx, req := runHook(t, "shared-model")
		provider, _, fallbacks := req.GetRequestFields()
		if provider != schemas.Anthropic {
			t.Fatalf("got provider %q, want anthropic", provider)
		}
		got := make([]schemas.ModelProvider, len(fallbacks))
		for i, fb := range fallbacks {
			got[i] = fb.Provider
		}
		if !slices.Equal(got, []schemas.ModelProvider{schemas.Azure, schemas.OpenAI}) {
			t.Fatalf("fallbacks %v, want [azure openai]", got)
		}
		requireLog(t, ctx, "found 3 options in model catalog")
		for _, message := range catalogLogs(ctx) {
			if strings.Contains(message, "key selection is bypassed") {
				t.Fatalf("a catalog pick was logged as a pin: %q", message)
			}
		}
	})
}
