package telemetry

import (
	"context"
	"testing"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// activeGauge returns the current bifrost_active_requests value for one request type.
func activeGauge(p *PrometheusPlugin, rt schemas.RequestType) float64 {
	return testutil.ToFloat64(p.ActiveRequests.WithLabelValues(string(rt)))
}

// newPreHookContext returns a fresh request context with PreLLMHook already run.
func newPreHookContext(t *testing.T, p *PrometheusPlugin, rt schemas.RequestType) *schemas.BifrostContext {
	t.Helper()
	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(time.Minute))
	if _, _, err := p.PreLLMHook(ctx, &schemas.BifrostRequest{RequestType: rt}); err != nil {
		t.Fatalf("PreLLMHook: %v", err)
	}
	return ctx
}

// streamError builds a terminal stream error as core reports it to post hooks.
func streamError(rt schemas.RequestType, status int) *schemas.BifrostError {
	bErr := &schemas.BifrostError{
		StatusCode: new(status),
		Error:      &schemas.ErrorField{Message: "stream ended"},
	}
	bErr.ExtraFields.RequestType = rt
	bErr.ExtraFields.Provider = schemas.OpenAI
	bErr.ExtraFields.OriginalModelRequested = "gpt-test"
	return bErr
}

// TestActiveRequestsReleasedByTransportHookWhenPostHookSkipped is the regression for
// the responses_stream gauge drift: a client that cancels before the stream reaches
// the handler gets no LLM post hook from core, so the once-per-HTTP-request transport
// post hook must release the slot.
func TestActiveRequestsReleasedByTransportHookWhenPostHookSkipped(t *testing.T) {
	p := newTestPlugin(t)
	ctx := newPreHookContext(t, p, schemas.ResponsesStreamRequest)
	if got := activeGauge(p, schemas.ResponsesStreamRequest); got != 1 {
		t.Fatalf("after PreLLMHook gauge = %v, want 1", got)
	}
	if err := p.HTTPTransportPostHook(ctx, &schemas.HTTPRequest{}, &schemas.HTTPResponse{}); err != nil {
		t.Fatalf("HTTPTransportPostHook: %v", err)
	}
	if got := activeGauge(p, schemas.ResponsesStreamRequest); got != 0 {
		t.Fatalf("after transport post hook gauge = %v, want 0", got)
	}
}

// TestActiveRequestsMidStreamCancelReleasesOnce covers the normal cancellation path:
// the terminal post hook releases, and the transport hook afterwards must not
// decrement again.
func TestActiveRequestsMidStreamCancelReleasesOnce(t *testing.T) {
	p := newTestPlugin(t)
	ctx := newPreHookContext(t, p, schemas.ResponsesStreamRequest)
	ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
	if _, _, err := p.PostLLMHook(ctx, nil, streamError(schemas.ResponsesStreamRequest, 499)); err != nil {
		t.Fatalf("PostLLMHook: %v", err)
	}
	if err := p.HTTPTransportPostHook(ctx, &schemas.HTTPRequest{}, &schemas.HTTPResponse{}); err != nil {
		t.Fatalf("HTTPTransportPostHook: %v", err)
	}
	if got := activeGauge(p, schemas.ResponsesStreamRequest); got != 0 {
		t.Fatalf("gauge = %v, want 0 (released exactly once)", got)
	}
}

// TestActiveRequestsStreamErrorWithoutEndIndicator covers plugin short-circuit
// rejections of stream requests, which run post hooks without the end indicator.
func TestActiveRequestsStreamErrorWithoutEndIndicator(t *testing.T) {
	p := newTestPlugin(t)
	ctx := newPreHookContext(t, p, schemas.ResponsesStreamRequest)
	if _, _, err := p.PostLLMHook(ctx, nil, streamError(schemas.ResponsesStreamRequest, 429)); err != nil {
		t.Fatalf("PostLLMHook: %v", err)
	}
	if got := activeGauge(p, schemas.ResponsesStreamRequest); got != 0 {
		t.Fatalf("gauge = %v, want 0", got)
	}
}

// TestActiveRequestsNonFinalChunkKeepsSlot pins that intermediate stream chunks do
// not release the slot; only the terminal call does.
func TestActiveRequestsNonFinalChunkKeepsSlot(t *testing.T) {
	p := newTestPlugin(t)
	ctx := newPreHookContext(t, p, schemas.ResponsesStreamRequest)
	chunk := &schemas.BifrostResponse{ResponsesStreamResponse: &schemas.BifrostResponsesStreamResponse{}}
	chunk.PopulateExtraFields(schemas.ResponsesStreamRequest, schemas.OpenAI, "gpt-test", "gpt-test")
	if _, _, err := p.PostLLMHook(ctx, chunk, nil); err != nil {
		t.Fatalf("PostLLMHook chunk: %v", err)
	}
	if got := activeGauge(p, schemas.ResponsesStreamRequest); got != 1 {
		t.Fatalf("after non-final chunk gauge = %v, want 1", got)
	}
	ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
	if _, _, err := p.PostLLMHook(ctx, chunk, nil); err != nil {
		t.Fatalf("PostLLMHook final: %v", err)
	}
	if got := activeGauge(p, schemas.ResponsesStreamRequest); got != 0 {
		t.Fatalf("after final chunk gauge = %v, want 0", got)
	}
}

// TestActiveRequestsFallbackAttemptsStayBalanced covers fallbacks, which run
// PreLLMHook once per attempt on the shared context.
func TestActiveRequestsFallbackAttemptsStayBalanced(t *testing.T) {
	p := newTestPlugin(t)
	ctx := newPreHookContext(t, p, schemas.ResponsesStreamRequest)
	// Attempt 1 fails terminally, then the fallback attempt starts.
	ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
	if _, _, err := p.PostLLMHook(ctx, nil, streamError(schemas.ResponsesStreamRequest, 500)); err != nil {
		t.Fatalf("PostLLMHook attempt 1: %v", err)
	}
	ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, false)
	if _, _, err := p.PreLLMHook(ctx, &schemas.BifrostRequest{RequestType: schemas.ResponsesStreamRequest}); err != nil {
		t.Fatalf("PreLLMHook attempt 2: %v", err)
	}
	if got := activeGauge(p, schemas.ResponsesStreamRequest); got != 1 {
		t.Fatalf("during fallback attempt gauge = %v, want 1", got)
	}
	// Attempt 2 is cancelled before its stream reaches the handler: only the transport hook runs.
	if err := p.HTTPTransportPostHook(ctx, &schemas.HTTPRequest{}, &schemas.HTTPResponse{}); err != nil {
		t.Fatalf("HTTPTransportPostHook: %v", err)
	}
	if got := activeGauge(p, schemas.ResponsesStreamRequest); got != 0 {
		t.Fatalf("after request gauge = %v, want 0", got)
	}
}

// TestActiveRequestsDoublePreHookCountsOnce pins that a repeated PreLLMHook for the
// same in-flight attempt cannot inflate the gauge.
func TestActiveRequestsDoublePreHookCountsOnce(t *testing.T) {
	p := newTestPlugin(t)
	ctx := newPreHookContext(t, p, schemas.ResponsesStreamRequest)
	if _, _, err := p.PreLLMHook(ctx, &schemas.BifrostRequest{RequestType: schemas.ResponsesStreamRequest}); err != nil {
		t.Fatalf("PreLLMHook: %v", err)
	}
	if got := activeGauge(p, schemas.ResponsesStreamRequest); got != 1 {
		t.Fatalf("gauge = %v, want 1", got)
	}
}

// TestActiveRequestsDoublePreHookDifferentTypeTransfers pins that a repeated PreLLMHook
// with a different request type moves the held slot to the new label, so the release
// decrements the label that was counted and neither label drifts.
func TestActiveRequestsDoublePreHookDifferentTypeTransfers(t *testing.T) {
	p := newTestPlugin(t)
	ctx := newPreHookContext(t, p, schemas.ResponsesRequest)
	if _, _, err := p.PreLLMHook(ctx, &schemas.BifrostRequest{RequestType: schemas.ResponsesStreamRequest}); err != nil {
		t.Fatalf("PreLLMHook: %v", err)
	}
	if got := activeGauge(p, schemas.ResponsesRequest); got != 0 {
		t.Fatalf("held-before label gauge = %v, want 0", got)
	}
	if got := activeGauge(p, schemas.ResponsesStreamRequest); got != 1 {
		t.Fatalf("held-after label gauge = %v, want 1", got)
	}
	if err := p.HTTPTransportPostHook(ctx, &schemas.HTTPRequest{}, &schemas.HTTPResponse{}); err != nil {
		t.Fatalf("HTTPTransportPostHook: %v", err)
	}
	if got := activeGauge(p, schemas.ResponsesRequest); got != 0 {
		t.Fatalf("after release %s gauge = %v, want 0", schemas.ResponsesRequest, got)
	}
	if got := activeGauge(p, schemas.ResponsesStreamRequest); got != 0 {
		t.Fatalf("after release %s gauge = %v, want 0", schemas.ResponsesStreamRequest, got)
	}
}

// TestActiveRequestsTransportHookWithoutLLMHooks checks requests that never reached
// the LLM pipeline (e.g. auth failures, non-inference routes) never go negative.
func TestActiveRequestsTransportHookWithoutLLMHooks(t *testing.T) {
	p := newTestPlugin(t)
	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(time.Minute))
	if err := p.HTTPTransportPostHook(ctx, &schemas.HTTPRequest{}, &schemas.HTTPResponse{}); err != nil {
		t.Fatalf("HTTPTransportPostHook: %v", err)
	}
	if got := activeGauge(p, schemas.ResponsesStreamRequest); got != 0 {
		t.Fatalf("gauge = %v, want 0", got)
	}
}
