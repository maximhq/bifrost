package tracing

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

func newHandleTestCtx(tracer *Tracer) (string, context.Context) {
	traceID := tracer.CreateTrace("")
	ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyTraceID, traceID)
	return traceID, ctx
}

// The *IfMatch guards act only while the span's SpanID still equals the handle's,
// which is how a cached-pointer handle detects a pooled span that ReleaseTrace reset
// or another trace reused. This is the unit-level contract the tracer relies on.
func TestSpanIfMatchGuards(t *testing.T) {
	s := &schemas.Span{SpanID: "abc"}

	if !s.EndIfMatch("abc", schemas.SpanStatusOk, "done") {
		t.Fatal("EndIfMatch should succeed when the SpanID matches")
	}
	if s.Status != schemas.SpanStatusOk {
		t.Fatalf("span not ended: status=%v", s.Status)
	}

	// ReleaseTrace resets the span before returning it to the pool.
	s.Reset()
	if s.EndIfMatch("abc", schemas.SpanStatusError, "stale") {
		t.Fatal("EndIfMatch must fail on a reset (recycled) span")
	}
	if s.Status != schemas.SpanStatusUnset {
		t.Fatalf("reset span mutated by a stale handle: status=%v", s.Status)
	}

	// The pooled span is reused by another trace under a new SpanID.
	s.SpanID = "xyz"
	if s.SetAttributeIfMatch("abc", "leak", true) {
		t.Fatal("SetAttributeIfMatch must fail when the handle's ID no longer matches")
	}
	if _, ok := s.Attributes["leak"]; ok {
		t.Fatal("stale handle leaked an attribute onto a reused span")
	}
	if !s.SetAttributeIfMatch("xyz", "own", true) {
		t.Fatal("SetAttributeIfMatch should succeed for the current owner")
	}
	if !s.MatchesID("xyz") || s.MatchesID("abc") {
		t.Fatal("MatchesID returned the wrong result")
	}
}

// After ReleaseTrace pools a span, the old handle must be a safe no-op, and if the
// pool hands that same object to a new trace, the stale handle must not corrupt the
// new span. Single-goroutine with no GC means the pool returns the same object, so
// this exercises the reuse path deterministically.
func TestSpanHandle_UseAfterReleaseTraceIsSafe(t *testing.T) {
	store := NewTraceStore(time.Hour, nil)
	tracer := NewTracer(store, nil, nil)

	traceID1, ctx1 := newHandleTestCtx(tracer)
	_, handle1 := tracer.StartSpanID(ctx1, "s1", schemas.SpanKindPlugin)
	if tracer.SpanFromHandle(handle1) == nil {
		t.Fatal("expected a live span for trace1")
	}

	// Release trace1: span1 is reset and returned to the pool.
	if tr := store.CompleteTrace(traceID1); tr != nil {
		tracer.ReleaseTrace(tr)
	}

	// A new trace pulls the recycled span from the pool.
	_, ctx2 := newHandleTestCtx(tracer)
	_, handle2 := tracer.StartSpanID(ctx2, "s2", schemas.SpanKindPlugin)
	span2 := tracer.SpanFromHandle(handle2)
	if span2 == nil {
		t.Fatal("expected a live span for trace2")
	}

	// The stale handle must not touch the reused span.
	tracer.EndSpan(handle1, schemas.SpanStatusError, "stale")
	tracer.SetAttribute(handle1, "leak", true)

	if span2.Status == schemas.SpanStatusError {
		t.Error("stale handle ended the reused span")
	}
	if _, ok := span2.Attributes["leak"]; ok {
		t.Error("stale handle leaked an attribute onto the reused span")
	}
	// The released handle no longer resolves to a span.
	if got := tracer.SpanFromHandle(handle1); got == span2 {
		t.Error("released handle resolved to the reused span")
	}
}

// The TTL sweep releases a trace by age, even one still referenced by a live handle.
// A handle used after the sweep must stay a safe no-op rather than mutate a pooled
// span.
func TestSpanHandle_UseAfterTTLCleanupIsSafe(t *testing.T) {
	store := NewTraceStore(time.Hour, nil)
	tracer := NewTracer(store, nil, nil)

	traceID, ctx := newHandleTestCtx(tracer)
	_, handle := tracer.StartSpanID(ctx, "s", schemas.SpanKindPlugin)

	// Age the trace past the cutoff and run the sweep directly.
	if tr := store.GetTrace(traceID); tr != nil {
		tr.StartTime = time.Now().Add(-2 * time.Hour)
	}
	store.cleanupOldTraces()
	if store.GetTrace(traceID) != nil {
		t.Fatal("trace should have been swept")
	}

	// Must not panic; the guard fails and the by-ID fallback finds no trace.
	tracer.EndSpan(handle, schemas.SpanStatusOk, "")
	tracer.SetAttribute(handle, "x", 1)
	if got := tracer.SpanFromHandle(handle); got != nil {
		t.Error("handle to a swept trace should resolve to nil")
	}
}
