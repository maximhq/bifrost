package bifrost

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestClearBillingAttemptStartTimeMasksInheritedValue ensures a child context
// cannot expose a stale attempt timestamp inherited from its parent.
func TestClearBillingAttemptStartTimeMasksInheritedValue(t *testing.T) {
	parent := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	stamp := time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)
	parent.SetBillingAttemptStartTime(stamp)

	child := schemas.NewBifrostContext(parent, schemas.NoDeadline)
	if got, ok := child.Value(schemas.BifrostContextKeyBillingAttemptStartTime).(time.Time); !ok || !got.Equal(stamp) {
		t.Fatalf("child should inherit the parent timestamp, got %#v", got)
	}

	child.ClearBillingAttemptStartTime()
	if _, ok := child.Value(schemas.BifrostContextKeyBillingAttemptStartTime).(time.Time); ok {
		t.Fatal("cleared child context exposed an inherited billing timestamp")
	}
}

// TestNilUnaryRequestsUseIsolatedContexts pins that nil-context callers no longer
// share the mutable Bifrost-wide context used for request-scoped pricing state.
func TestNilUnaryRequestsUseIsolatedContexts(t *testing.T) {
	newBifrost := func() *Bifrost {
		return &Bifrost{
			ctx:             schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
			logger:          NewNoOpLogger(),
			account:         NewMockAccount(),
			providerMutexes: sync.Map{},
		}
	}
	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "test-model",
		Input:    []schemas.ChatMessage{{}},
	}
	bifrost := newBifrost()
	bifrost.bifrostRequestPool = sync.Pool{New: func() interface{} { return &schemas.BifrostRequest{} }}
	bifrost.llmPlugins.Store(&[]schemas.LLMPlugin{})
	bifrost.mcpPlugins.Store(&[]schemas.MCPPlugin{})
	bifrost.pluginPipelinePool = sync.Pool{New: func() interface{} {
		return &PluginPipeline{
			preHookErrors:  make([]error, 0),
			postHookErrors: make([]error, 0),
		}
	}}
	bifrost.tracer.Store(&tracerWrapper{tracer: &schemas.NoOpTracer{}})
	_, err := bifrost.ChatCompletionRequest(nil, req)
	if err == nil || err.Error == nil {
		t.Fatalf("expected request to fail without providers, got %#v", err)
	}
	if bifrost.ctx.Value(schemas.BifrostContextKeyRequestStartTime) != nil {
		t.Fatal("nil unary caller wrote a request start time to the shared Bifrost context")
	}
	if _, ok := bifrost.ctx.Value(schemas.BifrostContextKeyBillingAttemptStartTime).(time.Time); ok {
		t.Fatal("nil unary caller left a billing timestamp on the shared Bifrost context")
	}
	if v, ok := bifrost.ctx.Value(schemas.BifrostContextKeyRequestID).(string); ok && v != "" {
		t.Fatalf("nil unary caller changed the shared Bifrost request ID: %q", v)
	}
}

type billingAttemptPostHookPlugin struct {
	name            string
	replacement     *schemas.BifrostResponse
	observedAttempt *time.Time
}

// GetName returns the test plugin name.
func (p *billingAttemptPostHookPlugin) GetName() string { return p.name }

// Cleanup satisfies the plugin lifecycle contract for the test double.
func (p *billingAttemptPostHookPlugin) Cleanup() error { return nil }

// PreRequestHook leaves the test request unchanged.
func (p *billingAttemptPostHookPlugin) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}

// PreLLMHook forwards the test request to the provider path.
func (p *billingAttemptPostHookPlugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	return req, nil, nil
}

// PostLLMHook replaces or observes the response for timestamp propagation tests.
func (p *billingAttemptPostHookPlugin) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if p.replacement != nil {
		return p.replacement, bifrostErr, nil
	}
	if resp != nil {
		p.observedAttempt = resp.GetExtraFields().BillingAttemptStartedAt
	}
	return resp, bifrostErr, nil
}

// TestClearCtxForFallbackClearsBillingAttemptStartTime pins that fallback
// pre-hooks cannot observe the primary provider attempt's timestamp.
func TestClearCtxForFallbackClearsBillingAttemptStartTime(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetBillingAttemptStartTime(time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC))

	clearCtxForFallback(ctx)

	if got := ctx.Value(schemas.BifrostContextKeyBillingAttemptStartTime); got != nil {
		t.Fatalf("billing attempt timestamp survived fallback reset: %#v", got)
	}
}

// TestRunPostLLMHooksRestampsReplacementResponse ensures a post-hook that
// replaces a response cannot hide the provider attempt time from an outer
// logging or pricing hook.
func TestRunPostLLMHooksRestampsReplacementResponse(t *testing.T) {
	startedAt := time.Date(2026, 8, 27, 10, 30, 0, 0, time.UTC)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetBillingAttemptStartTime(startedAt)

	observer := &billingAttemptPostHookPlugin{name: "observer"}
	replacement := &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{}}
	replacer := &billingAttemptPostHookPlugin{name: "replacer", replacement: replacement}
	pipeline := &PluginPipeline{
		logger:     NewNoOpLogger(),
		tracer:     &schemas.NoOpTracer{},
		llmPlugins: []schemas.LLMPlugin{observer, replacer},
	}

	resp, bifrostErr := pipeline.RunPostLLMHooks(ctx, &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{},
	}, nil, 2)
	if bifrostErr != nil {
		t.Fatalf("RunPostLLMHooks returned error: %#v", bifrostErr)
	}
	if resp != replacement {
		t.Fatalf("RunPostLLMHooks response = %p, want replacement %p", resp, replacement)
	}
	if observer.observedAttempt == nil || !observer.observedAttempt.Equal(startedAt) {
		t.Fatalf("outer post-hook observed billing attempt %v, want %v", observer.observedAttempt, startedAt)
	}
	if got := resp.GetExtraFields().BillingAttemptStartedAt; got == nil || !got.Equal(startedAt) {
		t.Fatalf("final replacement response billing attempt = %v, want %v", got, startedAt)
	}
}
