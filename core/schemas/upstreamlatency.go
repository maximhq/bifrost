package schemas

import (
	"context"
	"sync/atomic"
	"time"
)

// Upstream-latency accumulation.
//
// Bifrost's "overhead" is the time a request spends inside Bifrost rather than
// blocked on a provider socket:
//
//	overhead = total_wall_time - upstream_latency
//
// The subtrahend has to be a running total, not a single measurement. A request
// can hit the network many times over its lifetime (retries, fallbacks, media
// fetches, cached-content lookups), and ExtraFields.Latency only ever holds the
// last attempt. So every site that blocks on a provider socket adds its own
// duration here, and the sum is read once at request end.
//
// The value is an *atomic.Int64 (nanoseconds) stored on the context. The pointer
// is installed once per request and only ever dereferenced afterwards, so
// concurrent adds never touch the context's value map. This matters: streaming
// keeps writing from the provider goroutine long after the request handler has
// returned, and there is no atomic read-modify-write on BifrostContext —
// GetAndSetValue is a swap, not an add.

// ResetUpstreamLatency installs a fresh accumulator on ctx. Call once at request
// entry, before any provider call.
//
// The reset is not optional. Bifrost falls back to a single process-global
// context when an SDK caller passes nil, so without this the counter would grow
// without bound across every such request and overhead would go negative.
func (bc *BifrostContext) ResetUpstreamLatency() {
	if bc == nil {
		return
	}
	bc.setReservedValue(BifrostContextKeyUpstreamLatency, &atomic.Int64{})
}

// AddUpstreamLatency adds d to the request's upstream total.
//
// Takes a plain context.Context so provider code can call it without knowing
// whether it holds a *BifrostContext or a derived one — Value traverses either
// way. A context with no accumulator (internal calls, tests, SDK callers on a
// path that never reset) is silently ignored: this is telemetry, and a missing
// counter must never break a request.
func AddUpstreamLatency(ctx context.Context, d time.Duration) {
	if d <= 0 || isNilContext(ctx) {
		return
	}
	if acc, ok := ctx.Value(BifrostContextKeyUpstreamLatency).(*atomic.Int64); ok && acc != nil {
		acc.Add(int64(d))
	}
}

// isNilContext reports whether ctx carries nothing readable.
//
// A plain ctx == nil check is not enough: a nil *BifrostContext stored in a
// context.Context interface is itself non-nil, and calling Value on it would
// panic. Provider code passes its context straight through to the helpers here,
// so that case has to be absorbed rather than crash a live request.
func isNilContext(ctx context.Context) bool {
	if ctx == nil {
		return true
	}
	bc, ok := ctx.(*BifrostContext)
	return ok && bc == nil
}

// GetUpstreamLatency returns the accumulated upstream total and whether an
// accumulator was present. Callers must treat !ok as "unknown", not as zero —
// reporting zero upstream time would attribute the entire request to Bifrost.
func GetUpstreamLatency(ctx context.Context) (time.Duration, bool) {
	if isNilContext(ctx) {
		return 0, false
	}
	if acc, ok := ctx.Value(BifrostContextKeyUpstreamLatency).(*atomic.Int64); ok && acc != nil {
		return time.Duration(acc.Load()), true
	}
	return 0, false
}

// StampUpstreamLatency writes the accumulated total onto the request's root span
// so trace-based connectors (OTEL, Datadog) can derive overhead at export time.
//
// It stamps the ROOT span, not the llm.call span: the root is the only one whose
// duration covers the whole request, and the accumulator spans every attempt.
// Only upstream is stamped, never overhead itself — each connector already knows
// its own notion of "total" and subtracting at export keeps the two consistent.
//
// Call as late as possible: at handler return for unary, and at stream
// completion for streaming, since a stream keeps accumulating until it drains.
// Every lookup is best-effort; a request with no tracer simply isn't stamped.
func (bc *BifrostContext) StampUpstreamLatency() {
	if bc == nil {
		return
	}
	upstream, ok := GetUpstreamLatency(bc)
	if !ok {
		return
	}
	traceID, _ := bc.Value(BifrostContextKeyTraceID).(string)
	if traceID == "" {
		return
	}
	tracer, _ := bc.Value(BifrostContextKeyTracer).(Tracer)
	if tracer == nil {
		return
	}
	// nil spanID selects the root span.
	handle := tracer.GetSpanHandleByID(traceID, nil)
	if handle == nil {
		return
	}
	tracer.SetAttribute(handle, AttrBifrostUpstreamDurationMs, float64(upstream)/float64(time.Millisecond))
}

// PopulateUpstreamLatency copies the accumulated total onto the response's
// ExtraFields so callers can derive overhead from the body alone.
//
// The response body is the only carrier that survives a proxy hop — response
// headers set by one node are not forwarded when another serves the request.
// Left nil when no accumulator is present, so absent stays distinguishable from
// zero.
func (r *BifrostResponse) PopulateUpstreamLatency(ctx context.Context) {
	if r == nil {
		return
	}
	upstream, ok := GetUpstreamLatency(ctx)
	if !ok {
		return
	}
	if ef := r.GetExtraFields(); ef != nil {
		ms := int64(upstream / time.Millisecond)
		ef.UpstreamLatency = &ms
	}
}

// PopulateOverheadLatency copies Bifrost's own cost onto the response's ExtraFields,
// derived from total via CalculateOverhead. Left nil when no accumulator is present,
// so absent stays distinct from zero.
func (r *BifrostResponse) PopulateOverheadLatency(ctx context.Context, total time.Duration) {
	if r == nil {
		return
	}
	overhead, ok := CalculateOverhead(ctx, total)
	if !ok {
		return
	}
	if ef := r.GetExtraFields(); ef != nil {
		ms := int64(overhead / time.Millisecond)
		ef.OverheadLatency = &ms
	}
}

// CalculateOverhead derives Bifrost's own cost from a total wall-clock duration.
//
// Clamps at zero. The two measurements come from different clocks started at
// different instants, so a fast request with a slow-to-observe total can round
// negative; a negative "overhead" is meaningless and would poison a histogram.
func CalculateOverhead(ctx context.Context, total time.Duration) (time.Duration, bool) {
	upstream, ok := GetUpstreamLatency(ctx)
	if !ok {
		return 0, false
	}
	overhead := total - upstream
	if overhead < 0 {
		overhead = 0
	}
	return overhead, true
}
