package tracing

// Deterministic before/after benchmark for fix C1: batching the per-request
// LLM-call span attribute writes. The old path issued ~30 individual
// tracer.SetAttribute calls, each re-resolving the span (GetTrace + linear
// GetSpan scan over all spans in the trace) and taking the span lock. The new
// path (SetAttributes) resolves the span once and writes under a single lock.
//
// Run:
//
//	go test ./framework/tracing/ -run '^$' -bench 'FixC1' -benchmem

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// benchC1Setup builds a trace with a realistic number of sibling spans (so the
// per-call GetSpan linear scan is exercised) and returns the tracer plus a
// handle to the last span (worst-case scan position).
func benchC1Setup(nSpans int) (*Tracer, schemas.SpanHandle) {
	store := NewTraceStore(5*time.Minute, nil)
	tracer := NewTracer(store, nil, nil)
	traceID := tracer.CreateTrace("")
	ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyTraceID, traceID)
	ctx, target := tracer.StartSpan(ctx, "http-request", schemas.SpanKindHTTPRequest)
	for i := 0; i < nSpans; i++ {
		_, h := tracer.StartSpan(ctx, "span"+strconv.Itoa(i), schemas.SpanKindPlugin)
		target = h
	}
	return tracer, target
}

// 30 attribute key/value pairs, matching the count set on the LLM-call span.
var (
	benchC1Keys = func() []string {
		k := make([]string, 30)
		for i := range k {
			k[i] = "attr" + strconv.Itoa(i)
		}
		return k
	}()
	benchC1Values = func() []any {
		v := make([]any, 30)
		for i := range v {
			v[i] = "value" + strconv.Itoa(i)
		}
		return v
	}()
)

// Old path: one SetAttribute per attribute (re-resolves span + locks each time).
func BenchmarkFixC1_PerAttribute(b *testing.B) {
	tracer, h := benchC1Setup(15)
	defer tracer.Stop()
	b.ReportAllocs()
	for b.Loop() {
		for i, k := range benchC1Keys {
			tracer.SetAttribute(h, k, benchC1Values[i])
		}
	}
}

// New path: resolve the span once (SpanFromHandle), then write directly. One
// lookup instead of ~30, no extra allocation.
func BenchmarkFixC1_ResolveOnce(b *testing.B) {
	tracer, h := benchC1Setup(15)
	defer tracer.Stop()
	b.ReportAllocs()
	for b.Loop() {
		span := tracer.SpanFromHandle(h)
		for i, k := range benchC1Keys {
			span.SetAttribute(k, benchC1Values[i])
		}
	}
}

// Fix C7: StartSpanID avoids the per-span context.WithValue (valueCtx) node that
// StartSpan allocates. The plugin-hook pipeline only needs the span ID (it
// mirrors it into the BifrostContext), so the valueCtx was pure waste. Both
// benchmarks create a span per iteration; the delta is the valueCtx allocation.
func BenchmarkFixC7_StartSpan(b *testing.B) {
	store := NewTraceStore(time.Hour, nil)
	defer store.Stop()
	tracer := NewTracer(store, nil, nil)
	defer tracer.Stop()
	traceID := tracer.CreateTrace("")
	ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyTraceID, traceID)
	b.ReportAllocs()
	for b.Loop() {
		_, _ = tracer.StartSpan(ctx, "s", schemas.SpanKindPlugin)
	}
}

func BenchmarkFixC7_StartSpanID(b *testing.B) {
	store := NewTraceStore(time.Hour, nil)
	defer store.Stop()
	tracer := NewTracer(store, nil, nil)
	defer tracer.Stop()
	traceID := tracer.CreateTrace("")
	ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyTraceID, traceID)
	b.ReportAllocs()
	for b.Loop() {
		_, _ = tracer.StartSpanID(ctx, "s", schemas.SpanKindPlugin)
	}
}

// Fix C2: the Populate*Attributes output map already exists (it's reused for
// root-span propagation), so the win is writing it into the span under ONE lock
// instead of a range + SetAttribute loop (one lock per entry). Span resolved once.
var benchC2Attrs = map[string]any{
	"gen_ai.provider.name": "openai", "bifrost.provider.name": "openai",
	"gen_ai.request.model": "gpt-4o", "gen_ai.operation.name": "chat",
	"gen_ai.request.temperature": 0.7, "gen_ai.request.max_tokens": 512,
	"gen_ai.request.top_p": 0.9, "gen_ai.input.messages": "hello",
	"gen_ai.request.stream": false, "bifrost.request.tools_count": 1,
}

func BenchmarkFixC2_PerEntryLoop(b *testing.B) {
	tracer, h := benchC1Setup(15)
	defer tracer.Stop()
	span := tracer.SpanFromHandle(h)
	b.ReportAllocs()
	for b.Loop() {
		for k, v := range benchC2Attrs {
			span.SetAttribute(k, v)
		}
	}
}

func BenchmarkFixC2_BulkSetAttributes(b *testing.B) {
	tracer, h := benchC1Setup(15)
	defer tracer.Stop()
	span := tracer.SpanFromHandle(h)
	b.ReportAllocs()
	for b.Loop() {
		span.SetAttributes(benchC2Attrs)
	}
}
