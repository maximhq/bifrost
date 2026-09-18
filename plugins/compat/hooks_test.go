package compat

import (
	"fmt"
	"os"
	"path/filepath"
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

// newReasoningToolsPlugin seeds the catalog from a model-parameters feed so
// the endpoint index and the parameter allowlist come from the same row, as
// they do in production. Both are needed: the conversion asks whether
// /responses is reachable, and the allowlist says whether reasoning may ride
// alongside tools on chat completions.
func newReasoningToolsPlugin(t *testing.T, cfg Config, paramsJSON string) *CompatPlugin {
	t.Helper()
	paramsPath := filepath.Join(t.TempDir(), "params.json")
	if err := os.WriteFile(paramsPath, []byte(paramsJSON), 0o600); err != nil {
		t.Fatalf("write model-parameters testdata: %v", err)
	}
	ds := datasheet.New(nil, bifrost.NewNoOpLogger(), datasheet.Config{
		ModelParametersURL: "file://" + paramsPath,
	})
	if err := ds.LoadModelParamsFromURLIntoMemory(t.Context()); err != nil {
		t.Fatalf("load model-parameters testdata: %v", err)
	}
	p, err := Init(cfg, bifrost.NewNoOpLogger(), modelcatalog.NewTestCatalogWithDatasheet(ds))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	return p
}

// Astra-shaped row: reasons, takes tools, but not both on chat completions,
// and /responses is available. This is what the datasheet must say for
// gpt-6-astra / gpt-5.6-* once supports_reasoning_with_tool_calls is
// published as false (#7275).
const reasoningToolsChatOnlyParams = `{
	"astra-like": {
		"mode": "chat",
		"supported_endpoints": ["/v1/chat/completions", "/v1/responses"],
		"supports_reasoning": true,
		"supports_function_calling": true,
		"supports_reasoning_with_tool_calls": false,
		"supports_none_reasoning_effort": true
	},
	"reasoning-with-tools-ok": {
		"mode": "chat",
		"supported_endpoints": ["/v1/chat/completions", "/v1/responses"],
		"supports_reasoning": true,
		"supports_function_calling": true,
		"supports_reasoning_with_tool_calls": true
	},
	"chat-only": {
		"mode": "chat",
		"supported_endpoints": ["/v1/chat/completions"],
		"supports_reasoning": true,
		"supports_function_calling": true,
		"supports_reasoning_with_tool_calls": false,
		"supports_none_reasoning_effort": true
	}
}`

func newReasoningToolsChatRequest(model string, requestType schemas.RequestType, withTools bool) *schemas.BifrostRequest {
	params := &schemas.ChatParameters{
		Reasoning: &schemas.ChatReasoning{Effort: schemas.Ptr("medium")},
	}
	if withTools {
		params.Tools = []schemas.ChatTool{{
			Type: schemas.ChatToolTypeFunction,
			Function: &schemas.ChatToolFunction{
				Name:        "get_weather",
				Description: schemas.Ptr("Returns weather"),
			},
		}}
	}
	return &schemas.BifrostRequest{
		RequestType: requestType,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    model,
			Params:   params,
		},
	}
}

// Chat + function tools on a model that cannot reason alongside tools on chat
// completions is moved to /responses, where the combination is legal, instead
// of having its reasoning disabled — which Azure rejects for these models.
func TestPluginChatWithToolsAndReasoningConvertsToResponses(t *testing.T) {
	for _, requestType := range []schemas.RequestType{schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest} {
		t.Run(string(requestType), func(t *testing.T) {
			p := newReasoningToolsPlugin(t, Config{ConvertChatToResponses: true, ShouldDropParams: true}, reasoningToolsChatOnlyParams)
			ctx := newTestContext()

			got, _, err := p.PreLLMHook(ctx, newReasoningToolsChatRequest("astra-like", requestType, true))
			if err != nil {
				t.Fatalf("PreLLMHook: %v", err)
			}

			changeType, ok := ctx.Value(schemas.BifrostContextKeyChangeRequestType).(schemas.RequestType)
			if !ok || changeType != schemas.ResponsesRequest {
				t.Fatalf("change request type = %v (%v), want %v", changeType, ok, schemas.ResponsesRequest)
			}
			if got.ChatRequest.Params.Reasoning == nil || got.ChatRequest.Params.Reasoning.Effort == nil || *got.ChatRequest.Params.Reasoning.Effort != "medium" {
				t.Fatalf("reasoning = %+v, want effort \"medium\" preserved for the /responses wire", got.ChatRequest.Params.Reasoning)
			}
			if len(got.ChatRequest.Params.Tools) != 1 {
				t.Fatalf("tools = %v, want preserved", got.ChatRequest.Params.Tools)
			}
			if dropped, _ := ctx.Value(schemas.BifrostContextKeyCompatDroppedParams).([]string); slices.Contains(dropped, "reasoning") {
				t.Errorf("dropped params %v contain reasoning, want untouched", dropped)
			}
		})
	}
}

// Without convert_chat_to_responses the request stays on chat completions and
// the pre-existing effort=none fallback still applies.
func TestPluginChatWithToolsAndReasoningConversionDisabled(t *testing.T) {
	p := newReasoningToolsPlugin(t, Config{ConvertChatToResponses: false, ShouldDropParams: true}, reasoningToolsChatOnlyParams)
	ctx := newTestContext()

	got, _, err := p.PreLLMHook(ctx, newReasoningToolsChatRequest("astra-like", schemas.ChatCompletionRequest, true))
	if err != nil {
		t.Fatalf("PreLLMHook: %v", err)
	}

	if _, converted := ctx.Value(schemas.BifrostContextKeyChangeRequestType).(schemas.RequestType); converted {
		t.Fatal("request was converted with convert_chat_to_responses off")
	}
	if effort := got.ChatRequest.Params.Reasoning.Effort; effort == nil || *effort != "none" {
		t.Fatalf("reasoning.effort = %v, want the existing \"none\" fallback when conversion is off", effort)
	}
}

// The x-bf-compat header sets BifrostContextKeyCompatConvertChatToResponses,
// which turns the conversion on for a single request with the config off.
func TestPluginChatWithToolsAndReasoningHeaderOverride(t *testing.T) {
	p := newReasoningToolsPlugin(t, Config{ConvertChatToResponses: false, ShouldDropParams: true}, reasoningToolsChatOnlyParams)
	ctx := newTestContext()
	ctx.SetValue(schemas.BifrostContextKeyCompatConvertChatToResponses, true)

	got, _, err := p.PreLLMHook(ctx, newReasoningToolsChatRequest("astra-like", schemas.ChatCompletionRequest, true))
	if err != nil {
		t.Fatalf("PreLLMHook: %v", err)
	}

	if changeType, ok := ctx.Value(schemas.BifrostContextKeyChangeRequestType).(schemas.RequestType); !ok || changeType != schemas.ResponsesRequest {
		t.Fatalf("change request type = %v (%v), want %v via header override", changeType, ok, schemas.ResponsesRequest)
	}
	if effort := got.ChatRequest.Params.Reasoning.Effort; effort == nil || *effort != "medium" {
		t.Fatalf("reasoning.effort = %v, want \"medium\" preserved", effort)
	}
}

// The conversion is specific to the chat + tools + reasoning combination:
// a request missing any leg, or a model that either handles the combination
// on chat or cannot reach /responses, is left on chat completions.
func TestPluginChatWithToolsAndReasoningNotConverted(t *testing.T) {
	cases := []struct {
		name      string
		model     string
		withTools bool
	}{
		{name: "no tools", model: "astra-like", withTools: false},
		{name: "reasoning with tools supported", model: "reasoning-with-tools-ok", withTools: true},
		{name: "responses unsupported", model: "chat-only", withTools: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newReasoningToolsPlugin(t, Config{ConvertChatToResponses: true, ShouldDropParams: true}, reasoningToolsChatOnlyParams)
			ctx := newTestContext()

			if _, _, err := p.PreLLMHook(ctx, newReasoningToolsChatRequest(tc.model, schemas.ChatCompletionRequest, tc.withTools)); err != nil {
				t.Fatalf("PreLLMHook: %v", err)
			}
			if changeType, converted := ctx.Value(schemas.BifrostContextKeyChangeRequestType).(schemas.RequestType); converted {
				t.Fatalf("request was converted to %v, want left on chat completions", changeType)
			}
		})
	}
}
