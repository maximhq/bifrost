package compat

import (
	"fmt"
	"slices"
	"sync"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
)

// newTestPlugin builds a drop-params-enabled plugin backed by an in-memory
// catalog seeded with supported, so tests can exercise the full
// PreLLMHook/PostLLMHook pair without a datasheet sync.
func newTestPlugin(t *testing.T, supported map[string][]string) *CompatPlugin {
	t.Helper()
	ds := datasheet.NewTestStore(nil)
	ds.SetSupportedParamsForTest(supported)
	p, err := Init(Config{ShouldDropParams: true, AzureDeepseek: true}, bifrost.NewNoOpLogger(), modelcatalog.NewTestCatalogWithDatasheet(ds))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	return p
}

// newServiceTierChatRequest builds a chat request carrying service_tier plus a
// param every model in these tests supports, so a mix-up between two concurrent
// requests shows up as a difference in the reported dropped list.
func newServiceTierChatRequest(model string) *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    model,
			Params: &schemas.ChatParameters{
				ServiceTier: schemas.Ptr(schemas.BifrostServiceTierPriority),
				Temperature: schemas.Ptr(0.5),
			},
		},
	}
}

// TestPluginDroppedParamsAreRequestScoped guards that the dropped-parameter
// list reported on a response belongs to that response's own request.
//
// The plugin is registered once per process, so any per-request state parked on
// the plugin struct is shared by every in-flight request: one request's
// PreLLMHook overwrites another's list before that other request reaches
// PostLLMHook. Callers debugging a silently scrubbed param (see the service_tier
// drop in dropUnsupportedParams) read exactly this field, so it has to describe
// the request it is attached to.
func TestPluginDroppedParamsAreRequestScoped(t *testing.T) {
	p := newTestPlugin(t, map[string][]string{
		"tier-model":   {"service_tier", "temperature"},
		"notier-model": {"temperature"},
	})

	cases := []struct {
		model       string
		wantDropped []string
	}{
		{model: "tier-model", wantDropped: nil},
		{model: "notier-model", wantDropped: []string{"service_tier"}},
	}

	if len(cases) != 2 {
		t.Fatalf("the rendezvous below is a two-party barrier; got %d cases", len(cases))
	}

	const iterations = 200
	failures := make(chan string, len(cases)*iterations)

	// Rendezvous between the two goroutines, one buffered slot each. Both park
	// here after PreLLMHook and before PostLLMHook, so the interleaving that
	// exposes plugin-level state - one request's PreLLMHook landing between the
	// other's PreLLMHook and PostLLMHook - happens on every iteration instead of
	// whenever the scheduler happens to produce it. Nothing below may return
	// early: a goroutine that leaves the loop strands its partner at the barrier.
	arrived := [2]chan struct{}{make(chan struct{}, 1), make(chan struct{}, 1)}

	var wg sync.WaitGroup
	for i, tc := range cases {
		wg.Add(1)
		go func(self int, model string, want []string) {
			defer wg.Done()
			for range iterations {
				ctx := newTestContext()
				_, _, preErr := p.PreLLMHook(ctx, newServiceTierChatRequest(model))

				arrived[self] <- struct{}{}
				<-arrived[1-self]

				if preErr != nil {
					failures <- fmt.Sprintf("model %s: PreLLMHook: %v", model, preErr)
					continue
				}

				resp := &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{}}
				if _, _, err := p.PostLLMHook(ctx, resp, nil); err != nil {
					failures <- fmt.Sprintf("model %s: PostLLMHook: %v", model, err)
					continue
				}

				got := resp.ChatResponse.ExtraFields.DroppedCompatPluginParams
				if !slices.Equal(got, want) {
					failures <- fmt.Sprintf("model %s: dropped_compat_plugin_params = %v, want %v", model, got, want)
				}
			}
		}(i, tc.model, tc.wantDropped)
	}
	wg.Wait()
	close(failures)

	for msg := range failures {
		t.Error(msg)
	}
}

// TestPluginDroppedParamsSingleRequest pins the same reporting contract on the
// sequential path, so a regression in the concurrent test is not mistaken for
// the field having stopped being populated at all.
func TestPluginDroppedParamsSingleRequest(t *testing.T) {
	p := newTestPlugin(t, map[string][]string{"notier-model": {"temperature"}})

	ctx := newTestContext()
	if _, _, err := p.PreLLMHook(ctx, newServiceTierChatRequest("notier-model")); err != nil {
		t.Fatalf("PreLLMHook: %v", err)
	}

	resp := &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{}}
	if _, _, err := p.PostLLMHook(ctx, resp, nil); err != nil {
		t.Fatalf("PostLLMHook: %v", err)
	}

	got := resp.ChatResponse.ExtraFields.DroppedCompatPluginParams
	if !slices.Contains(got, "service_tier") {
		t.Errorf("dropped_compat_plugin_params = %v, want it to report service_tier", got)
	}
}

// TestPluginDroppedParamsClearedBetweenAttempts guards the fallback path, where
// the same context is carried into a second attempt against a different model.
// PreLLMHook only writes the dropped list when something was dropped, so an
// attempt that drops nothing must not inherit the previous attempt's list.
func TestPluginDroppedParamsClearedBetweenAttempts(t *testing.T) {
	p := newTestPlugin(t, map[string][]string{
		"notier-model": {"temperature"},
		"tier-model":   {"service_tier", "temperature"},
	})

	ctx := newTestContext()

	// First attempt drops service_tier.
	if _, _, err := p.PreLLMHook(ctx, newServiceTierChatRequest("notier-model")); err != nil {
		t.Fatalf("PreLLMHook (first attempt): %v", err)
	}

	// Fallback to a model that supports every param on the request, on the same
	// context the first attempt used.
	if _, _, err := p.PreLLMHook(ctx, newServiceTierChatRequest("tier-model")); err != nil {
		t.Fatalf("PreLLMHook (fallback attempt): %v", err)
	}

	resp := &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{}}
	if _, _, err := p.PostLLMHook(ctx, resp, nil); err != nil {
		t.Fatalf("PostLLMHook: %v", err)
	}

	if got := resp.ChatResponse.ExtraFields.DroppedCompatPluginParams; len(got) != 0 {
		t.Errorf("dropped_compat_plugin_params = %v, want empty - the fallback attempt dropped nothing", got)
	}
}

// TestReasoningWithToolsResponsesRouting covers routing chat requests to
// Responses for models whose datasheet sets supports_reasoning_with_tool_calls false,
// and the reasoning-off fallback when the toggle is off.
func TestReasoningWithToolsResponsesRouting(t *testing.T) {
	newPlugin := func(t *testing.T, enabled bool) *CompatPlugin {
		t.Helper()
		ds := datasheet.NewTestStore(nil)
		ds.SetSupportedParamsForTest(map[string][]string{
			"no-reasoning-with-tools": {"reasoning", "tools"},
			"reasoning-with-tools":    {"reasoning", "tools", "reasoning_with_tool_calls"},
		})
		mc := modelcatalog.NewTestCatalogWithDatasheet(ds)
		mc.SetKeyConfigForProvider(schemas.OpenAI, []schemas.Key{{ID: "openai-key", Aliases: schemas.KeyAliases{"team-reasoner": {ModelID: "no-reasoning-with-tools"}}}})
		p, err := Init(Config{ShouldDropParams: true, ForceReasoningOnlyModelsToResponses: enabled}, bifrost.NewNoOpLogger(), mc)
		if err != nil {
			t.Fatalf("Init: %v", err)
		}
		return p
	}
	newChatRequest := func(model string, requestType schemas.RequestType, withTools bool) *schemas.BifrostRequest {
		params := &schemas.ChatParameters{Reasoning: &schemas.ChatReasoning{Effort: schemas.Ptr("high")}}
		if withTools {
			params.Tools = []schemas.ChatTool{{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: "lookup"}}}
		}
		return &schemas.BifrostRequest{
			RequestType: requestType,
			ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: model, Params: params},
		}
	}

	tests := []struct {
		name          string
		enabled       bool
		override      bool
		model         string
		requestType   schemas.RequestType
		withTools     bool
		wantConverted bool
		wantReasoning bool
	}{
		{name: "flag false with tools", enabled: true, model: "no-reasoning-with-tools", requestType: schemas.ChatCompletionRequest, withTools: true, wantConverted: true, wantReasoning: true},
		{name: "flag false with tools stream", enabled: true, model: "no-reasoning-with-tools", requestType: schemas.ChatCompletionStreamRequest, withTools: true, wantConverted: true, wantReasoning: true},
		{name: "flag false without tools", enabled: true, model: "no-reasoning-with-tools", requestType: schemas.ChatCompletionRequest, wantConverted: true, wantReasoning: true},
		{name: "alias resolved to flag false model", enabled: true, model: "team-reasoner", requestType: schemas.ChatCompletionRequest, withTools: true, wantConverted: true, wantReasoning: true},
		{name: "reasoning with tools supported", enabled: true, model: "reasoning-with-tools", requestType: schemas.ChatCompletionRequest, withTools: true, wantReasoning: true},
		{name: "no datasheet entry", enabled: true, model: "unknown", requestType: schemas.ChatCompletionRequest, withTools: true, wantReasoning: true},
		{name: "toggle off forces reasoning off", model: "no-reasoning-with-tools", requestType: schemas.ChatCompletionRequest, withTools: true},
		{name: "header override", override: true, model: "no-reasoning-with-tools", requestType: schemas.ChatCompletionRequest, withTools: true, wantConverted: true, wantReasoning: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newTestContext()
			if tt.override {
				ctx.SetValue(schemas.BifrostContextKeyCompatForceReasoningOnlyToResponses, true)
			}
			got, _, err := newPlugin(t, tt.enabled).PreLLMHook(ctx, newChatRequest(tt.model, tt.requestType, tt.withTools))
			if err != nil {
				t.Fatalf("PreLLMHook: %v", err)
			}
			changeType, ok := ctx.Value(schemas.BifrostContextKeyChangeRequestType).(schemas.RequestType)
			converted := ok && changeType == schemas.ResponsesRequest
			if converted != tt.wantConverted {
				t.Errorf("converted to responses = %v, want %v", converted, tt.wantConverted)
			}
			if hasReasoning := got.ChatRequest.Params.Reasoning != nil; hasReasoning != tt.wantReasoning {
				t.Errorf("reasoning preserved = %v, want %v", hasReasoning, tt.wantReasoning)
			}
		})
	}
}

func TestConfigForceReasoningOnlyModelsToResponsesDefaultsOn(t *testing.T) {
	var c Config
	if err := c.UnmarshalJSON([]byte(`{}`)); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	if !c.ForceReasoningOnlyModelsToResponses {
		t.Error("force_reasoning_only_models_to_responses should default to true when absent")
	}
}

// TestDecisionToResponsesEmulationOptIn pins that, with convert_decision_to_responses
// on (config or x-bf-compat override), the compat plugin marks a decision for
// emulation unless the model catalog lists the model as a native decisions model:
// a supports_decisions row decides in either direction, and without a row the
// gpt-6-luna on OpenAI, Typesafe, Clef on Cloudflare, deciders on Perplexity, System One
// models on Ollama and Microsoft's decision models and gpt-6-luna on Azure are native. Typesafe and other
// request types are never marked.
func TestDecisionToResponsesEmulationOptIn(t *testing.T) {
	rows := map[string]bool{"microsoft/decision-1": true, "typesafe/jev-router": false, "clef": false, "drex-v2.0": false}
	schemas.SetCapabilityResolver(func(provider schemas.ModelProvider, model string) *schemas.ModelCapabilities {
		if provider == schemas.Groq && model == "clef" {
			return &schemas.ModelCapabilities{SupportsDecisions: schemas.Ptr(true)}
		}
		if supports, ok := rows[model]; ok {
			return &schemas.ModelCapabilities{SupportsDecisions: &supports}
		}
		return nil
	})
	t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })

	decision := func(provider schemas.ModelProvider, model string) func() *schemas.BifrostRequest {
		return func() *schemas.BifrostRequest {
			return &schemas.BifrostRequest{
				RequestType:     schemas.DecisionRequest,
				DecisionRequest: &schemas.BifrostDecisionRequest{Provider: provider, Model: model},
			}
		}
	}
	chat := func() *schemas.BifrostRequest {
		return &schemas.BifrostRequest{
			RequestType: schemas.ChatCompletionRequest,
			ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "gpt-4o"},
		}
	}

	tests := []struct {
		name          string
		enabled       bool
		override      bool
		req           func() *schemas.BifrostRequest
		wantConverted bool
	}{
		{name: "off by default", req: decision(schemas.OpenAI, "gpt-4o")},
		{name: "chat model marked", enabled: true, req: decision(schemas.OpenAI, "gpt-4o"), wantConverted: true},
		{name: "header override", override: true, req: decision(schemas.OpenAI, "gpt-4o"), wantConverted: true},
		{name: "gpt-6-luna family is native", enabled: true, req: decision(schemas.OpenAI, "gpt-6-luna")},
		{name: "gpt-6-luna chat model on openrouter marked", enabled: true, req: decision(schemas.OpenRouter, "openai/gpt-6-luna-pro"), wantConverted: true},
		{name: "typesafe model on openrouter is native", enabled: true, req: decision(schemas.OpenRouter, "~typesafe/jev-latest")},
		{name: "true row is native", enabled: true, req: decision(schemas.OpenRouter, "microsoft/decision-1")},
		{name: "false row overrides the typesafe name", enabled: true, req: decision(schemas.OpenRouter, "typesafe/jev-router"), wantConverted: true},
		{name: "clef on cloudflare is native", enabled: true, req: decision(schemas.Cloudflare, "clef-flash")},
		{name: "clef full id on cloudflare is native", enabled: true, req: decision(schemas.Cloudflare, "@cf/cloudflare/clef-flash")},
		{name: "clef on groq marked", enabled: true, req: decision(schemas.Groq, "clef-flash"), wantConverted: true},
		{name: "clef full id on groq marked", enabled: true, req: decision(schemas.Groq, "@cf/cloudflare/clef-flash"), wantConverted: true},
		{name: "clef on openai marked by header", override: true, req: decision(schemas.OpenAI, "clef-flash"), wantConverted: true},
		{name: "true row overrides provider-scoped clef fallback", enabled: true, req: decision(schemas.Groq, "clef")},
		{name: "chat model on cloudflare marked", enabled: true, req: decision(schemas.Cloudflare, "@cf/meta/llama-4-scout-17b-16e-instruct"), wantConverted: true},
		{name: "false row overrides the clef name", enabled: true, req: decision(schemas.Cloudflare, "clef"), wantConverted: true},
		{name: "decider on perplexity is native", enabled: true, req: decision(schemas.Perplexity, "pplx-decider-v1.1-27b")},
		{name: "dated decider on perplexity is native", enabled: true, req: decision(schemas.Perplexity, "pplx-decider-v1.1-27b-20261006")},
		{name: "decision model on baseten is native", enabled: true, req: decision(schemas.Baseten, "decision-models/baseten-joey-2b-latest")},
		{name: "chat model on baseten marked", enabled: true, req: decision(schemas.Baseten, "zai-org/GLM-5.3"), wantConverted: true},
		{name: "baseten decision model on openai marked", enabled: true, req: decision(schemas.OpenAI, "decision-models/baseten-joey-2b-latest"), wantConverted: true},
		{name: "decision model on gemini is native", enabled: true, req: decision(schemas.Gemini, "gemini-decision-1")},
		{name: "chat model on gemini marked", enabled: true, req: decision(schemas.Gemini, "gemini-3-flash"), wantConverted: true},
		{name: "gemini decision model on vertex marked", enabled: true, req: decision(schemas.Vertex, "gemini-decision-1"), wantConverted: true},
		{name: "chat model on perplexity marked", enabled: true, req: decision(schemas.Perplexity, "sonar-pro"), wantConverted: true},
		{name: "decider on openai marked", enabled: true, req: decision(schemas.OpenAI, "pplx-decider-v1.1-27b"), wantConverted: true},
		{name: "system one model on ollama is native", enabled: true, req: decision(schemas.Ollama, "nimble")},
		{name: "tagged system one model on ollama is native", enabled: true, req: decision(schemas.Ollama, "tev1:0.8b")},
		{name: "clef on ollama is native", enabled: true, req: decision(schemas.Ollama, "clef-flash")},
		{name: "chat model on ollama marked", enabled: true, req: decision(schemas.Ollama, "llama3.1:latest"), wantConverted: true},
		{name: "gpt-6-luna on azure is native", enabled: true, req: decision(schemas.Azure, "gpt-6-luna")},
		{name: "dated gpt-6-luna on azure is native", enabled: true, req: decision(schemas.Azure, "gpt-6-luna-2026-09-22")},
		{name: "gpt-6-luna on groq marked", enabled: true, req: decision(schemas.Groq, "gpt-6-luna"), wantConverted: true},
		{name: "gpt-6-luna on a custom provider marked", enabled: true, req: decision(schemas.ModelProvider("my-openai"), "gpt-6-luna"), wantConverted: true},
		{name: "microsoft decision model on azure is native", enabled: true, req: decision(schemas.Azure, "Microsoft-Decision-1")},
		{name: "chat model on azure marked", enabled: true, req: decision(schemas.Azure, "gpt-4o"), wantConverted: true},
		{name: "microsoft decision model on openai marked", enabled: true, req: decision(schemas.OpenAI, "Microsoft-Decision-1"), wantConverted: true},
		{name: "ai_decide on databricks is native", enabled: true, req: decision(schemas.Databricks, "ai_decide")},
		{name: "openjev on databricks is native", enabled: true, req: decision(schemas.Databricks, "system.ai.openjev-qwen35-4b")},
		{name: "typesafe jev on databricks is native", enabled: true, req: decision(schemas.Databricks, "system.ai.typesafeai-jev-1-14-0")},
		{name: "serving endpoint model on databricks marked", enabled: true, req: decision(schemas.Databricks, "databricks-claude-sonnet-4"), wantConverted: true},
		{name: "ai_decide on openai marked", enabled: true, req: decision(schemas.OpenAI, "ai_decide"), wantConverted: true},
		{name: "typesafe provider never marked", enabled: true, req: decision(schemas.Typesafe, "jev-latest")},
		{name: "nace provider native despite false row", enabled: true, req: decision(schemas.Nace, "drex-v2.0")},
		{name: "nace provider never marked by header", override: true, req: decision(schemas.Nace, "drex-max")},
		{name: "vllm provider never marked", enabled: true, req: decision(schemas.VLLM, "Qwen/Qwen3-0.6B")},
		{name: "vllm provider never marked by header", override: true, req: decision(schemas.VLLM, "clef-flash")},
		{name: "sgl provider never marked", enabled: true, req: decision(schemas.SGL, "clef-flash")},
		{name: "sgl provider never marked by header", override: true, req: decision(schemas.SGL, "jaredpalmer/kev-4b")},
		{name: "chat request untouched", enabled: true, req: chat},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := Init(Config{ConvertDecisionToResponses: tt.enabled}, bifrost.NewNoOpLogger(), nil)
			if err != nil {
				t.Fatalf("Init: %v", err)
			}
			ctx := newTestContext()
			if tt.override {
				ctx.SetValue(schemas.BifrostContextKeyCompatConvertDecisionToResponses, true)
			}
			if _, _, err := p.PreLLMHook(ctx, tt.req()); err != nil {
				t.Fatalf("PreLLMHook: %v", err)
			}
			changeType, ok := ctx.Value(schemas.BifrostContextKeyChangeRequestType).(schemas.RequestType)
			converted := ok && changeType == schemas.ResponsesRequest
			if converted != tt.wantConverted {
				t.Errorf("converted to responses = %v, want %v", converted, tt.wantConverted)
			}
		})
	}
}

func TestConfigConvertDecisionToResponsesDefaultsOff(t *testing.T) {
	var c Config
	if err := c.UnmarshalJSON([]byte(`{}`)); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	if c.ConvertDecisionToResponses {
		t.Error("convert_decision_to_responses should default to false when absent")
	}
	if err := c.UnmarshalJSON([]byte(`{"convert_decision_to_responses": true}`)); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	if !c.ConvertDecisionToResponses {
		t.Error("convert_decision_to_responses should be true when set")
	}
}

// newAliasDropTestPlugin builds a drop-params plugin whose bedrock key maps
// key-level aliases onto models that carry a supported-parameter list.
func newAliasDropTestPlugin(t *testing.T, supported map[string][]string, aliases schemas.KeyAliases) *CompatPlugin {
	t.Helper()
	ds := datasheet.NewTestStore(nil)
	ds.SetSupportedParamsForTest(supported)
	mc := modelcatalog.NewTestCatalogWithDatasheet(ds)
	mc.SetKeyConfigForProvider(schemas.Bedrock, []schemas.Key{{ID: "bedrock-key", Aliases: aliases}})
	p, err := Init(Config{ShouldDropParams: true}, bifrost.NewNoOpLogger(), mc)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	return p
}

func newStopChatRequest(provider schemas.ModelProvider, model string) *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: provider,
			Model:    model,
			Params: &schemas.ChatParameters{
				Stop:        []string{"</block>"},
				Temperature: schemas.Ptr(0.5),
			},
		},
	}
}

// TestPluginDropParamsResolvesKeyAlias guards that drop-params reads the
// supported-parameter list of the model an alias names, not the alias string,
// which the catalog never has a row for.
func TestPluginDropParamsResolvesKeyAlias(t *testing.T) {
	cases := []struct {
		name      string
		supported map[string][]string
		aliases   schemas.KeyAliases
		model     string
	}{
		{
			name:      "model_id",
			supported: map[string][]string{"deepseek.v3.2": {"temperature"}},
			aliases:   schemas.KeyAliases{"claude-deepseek-v3": {ModelID: "deepseek.v3.2"}},
			model:     "claude-deepseek-v3",
		},
		{
			name:      "model_name wins over an opaque model_id",
			supported: map[string][]string{"deepseek.v3.2": {"temperature"}},
			aliases:   schemas.KeyAliases{"claude-deepseek-v3": {ModelID: "abc12xyz", ModelName: schemas.Ptr("deepseek.v3.2")}},
			model:     "claude-deepseek-v3",
		},
		{
			name:      "alias case-insensitive",
			supported: map[string][]string{"deepseek.v3.2": {"temperature"}},
			aliases:   schemas.KeyAliases{"claude-deepseek-v3": {ModelID: "deepseek.v3.2"}},
			model:     "Claude-DeepSeek-V3",
		},
		{
			// Migrated deployment maps keep the real model as the alias name and an opaque deployment id as model_id.
			name:      "deployment alias falls back to the alias name's list",
			supported: map[string][]string{"gpt-4o": {"temperature"}},
			aliases:   schemas.KeyAliases{"gpt-4o": {ModelID: "my-gpt4o-prod-east"}},
			model:     "gpt-4o",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newAliasDropTestPlugin(t, tc.supported, tc.aliases)
			ctx := newTestContext()
			out, _, err := p.PreLLMHook(ctx, newStopChatRequest(schemas.Bedrock, tc.model))
			if err != nil {
				t.Fatalf("PreLLMHook: %v", err)
			}
			if out.ChatRequest.Params.Stop != nil {
				t.Errorf("stop = %v, want it dropped", out.ChatRequest.Params.Stop)
			}
			if out.ChatRequest.Params.Temperature == nil {
				t.Errorf("temperature was dropped, want it kept")
			}
			if out.ChatRequest.Model != tc.model {
				t.Errorf("model = %q, want the alias %q left for core to resolve", out.ChatRequest.Model, tc.model)
			}
			dropped, _ := ctx.Value(schemas.BifrostContextKeyCompatDroppedParams).([]string)
			if !slices.Equal(dropped, []string{"stop"}) {
				t.Errorf("dropped = %v, want [stop]", dropped)
			}
		})
	}
}

// TestPluginDropParamsAliasOnOtherProvider guards that an alias is resolved
// against the request's own provider only.
func TestPluginDropParamsAliasOnOtherProvider(t *testing.T) {
	p := newAliasDropTestPlugin(t,
		map[string][]string{"deepseek.v3.2": {"temperature"}},
		schemas.KeyAliases{"claude-deepseek-v3": {ModelID: "deepseek.v3.2"}})
	ctx := newTestContext()
	out, _, err := p.PreLLMHook(ctx, newStopChatRequest(schemas.OpenAI, "claude-deepseek-v3"))
	if err != nil {
		t.Fatalf("PreLLMHook: %v", err)
	}
	if out.ChatRequest.Params.Stop == nil {
		t.Errorf("stop dropped on a provider that has no such alias")
	}
}

// BenchmarkPreLLMHookDropParams measures the drop-params PreLLMHook for a plain
// model and for a key alias, which adds one alias-index lookup.
func BenchmarkPreLLMHookDropParams(b *testing.B) {
	ds := datasheet.NewTestStore(nil)
	ds.SetSupportedParamsForTest(map[string][]string{"deepseek.v3.2": {"temperature"}})
	mc := modelcatalog.NewTestCatalogWithDatasheet(ds)
	mc.SetKeyConfigForProvider(schemas.Bedrock, []schemas.Key{{ID: "bedrock-key", Aliases: schemas.KeyAliases{"claude-deepseek-v3": {ModelID: "deepseek.v3.2"}}}})
	p, err := Init(Config{ShouldDropParams: true}, bifrost.NewNoOpLogger(), mc)
	if err != nil {
		b.Fatalf("Init: %v", err)
	}
	for _, model := range []string{"deepseek.v3.2", "claude-deepseek-v3"} {
		b.Run(model, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				ctx := newTestContext()
				if _, _, err := p.PreLLMHook(ctx, newStopChatRequest(schemas.Bedrock, model)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkMarkNativeDecision(b *testing.B) {
	for _, provider := range []schemas.ModelProvider{schemas.Typesafe, schemas.Nace, schemas.VLLM, schemas.SGL} {
		b.Run(string(provider), func(b *testing.B) {
			p := &CompatPlugin{}
			ctx := newTestContext()
			b.ReportAllocs()
			for b.Loop() {
				p.markDecisionForEmulation(ctx, provider, "native-model")
			}
		})
	}
}
