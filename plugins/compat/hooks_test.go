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
		p, err := Init(Config{ShouldDropParams: true, ForceReasoningOnlyModelsToResponses: enabled}, bifrost.NewNoOpLogger(), modelcatalog.NewTestCatalogWithDatasheet(ds))
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
