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
	p, err := Init(Config{ShouldDropParams: true}, bifrost.NewNoOpLogger(), modelcatalog.NewTestCatalogWithDatasheet(ds))
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
