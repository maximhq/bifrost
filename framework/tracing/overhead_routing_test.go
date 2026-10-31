package tracing

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// overheadRecorder is an observability plugin that records whether the trace it was
// injected with still contained any overhead-breakdown span.
type overheadRecorder struct {
	name         string
	injected     atomic.Bool
	sawBreakdown atomic.Bool
}

func (r *overheadRecorder) GetName() string { return r.name }
func (r *overheadRecorder) Cleanup() error  { return nil }
func (r *overheadRecorder) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}
func (r *overheadRecorder) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	return req, nil, nil
}
func (r *overheadRecorder) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, e *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, e, nil
}
func (r *overheadRecorder) Inject(_ context.Context, trace *schemas.Trace) error {
	r.injected.Store(true)
	for _, s := range trace.Spans {
		if schemas.IsOverheadBreakdownSpan(s) {
			r.sawBreakdown.Store(true)
		}
	}
	return nil
}

// overheadConsumer additionally implements schemas.OverheadSpanConsumer, opting into
// (or out of) the breakdown spans via its consumes field.
type overheadConsumer struct {
	overheadRecorder
	consumes bool
}

func (c *overheadConsumer) ConsumesOverheadSpans() bool { return c.consumes }

// Only a plugin whose ConsumesOverheadSpans returns true receives the breakdown
// spans; a plugin that does not implement the interface, and one that returns false,
// both receive the stripped trace.
func TestCompleteAndFlushTrace_RoutesOverheadSpansByConsumer(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()
	tracer := NewTracer(store, nil, nil)
	defer tracer.Stop()

	plain := &overheadRecorder{name: "plain"}
	optOut := &overheadConsumer{overheadRecorder: overheadRecorder{name: "opt-out"}, consumes: false}
	optIn := &overheadConsumer{overheadRecorder: overheadRecorder{name: "opt-in"}, consumes: true}
	tracer.SetObservabilityPlugins([]schemas.ObservabilityPlugin{plain, optOut, optIn}, nil)

	traceID := tracer.CreateTrace("")
	ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyTraceID, traceID)
	// Root, then a breakdown phase span (stripped for non-consumers) and a normal span.
	tracer.StartSpanID(ctx, "/v1/chat/completions", schemas.SpanKindHTTPRequest)
	tracer.StartSpanID(ctx, "convertor", schemas.SpanKindInternal)
	tracer.StartSpanID(ctx, "chat", schemas.SpanKindLLMCall)

	tracer.CompleteAndFlushTrace(traceID)
	if !tracer.waitForFlushes(2 * time.Second) {
		t.Fatal("trace flushes did not complete in time")
	}

	if !plain.injected.Load() || !optOut.injected.Load() || !optIn.injected.Load() {
		t.Fatal("every registered plugin should have been injected")
	}
	if !optIn.sawBreakdown.Load() {
		t.Error("opt-in consumer should receive the overhead breakdown spans")
	}
	if plain.sawBreakdown.Load() {
		t.Error("a plugin not implementing OverheadSpanConsumer should receive the stripped trace")
	}
	if optOut.sawBreakdown.Load() {
		t.Error("a consumer returning false should receive the stripped trace")
	}
}
