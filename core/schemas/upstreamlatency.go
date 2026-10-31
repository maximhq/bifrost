package schemas

import (
	"context"
	"sync"
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
// The value is an *upstreamAccumulator (nanoseconds) stored on the context. The pointer
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
	bc.setReservedValue(BifrostContextKeyUpstreamLatency, &upstreamAccumulator{})
}

// upstreamAccumulator is the per-request upstream total plus a mark of where the
// current attempt (one log row) began, so each row can report its own share.
type upstreamAccumulator struct {
	total             atomic.Int64                      // nanoseconds blocked on upstream, whole request
	attemptStartNanos atomic.Int64                      // unix nanos when the current attempt began; 0 = unmarked
	attemptUpstream   atomic.Int64                      // total at the moment the current attempt began
	attempts          atomic.Int64                      // attempts marked so far in this request
	wait              atomic.Pointer[upstreamWaitState] // set on the first BeginUpstreamWait
}

// upstreamWaitState tracks overlapping waits; only requests that make MCP tool calls allocate it.
type upstreamWaitState struct {
	mu       sync.Mutex
	inFlight int       // open waits
	since    time.Time // when inFlight last rose from zero
}

func upstreamAccumulatorFrom(ctx context.Context) *upstreamAccumulator {
	if isNilContext(ctx) {
		return nil
	}
	acc, _ := ctx.Value(BifrostContextKeyUpstreamLatency).(*upstreamAccumulator)
	return acc
}

// MarkAttemptStart records that a new attempt (an LLM call that gets its own log
// row: a fallback attempt or an agent-loop turn) begins now. No-op without an accumulator.
func (bc *BifrostContext) MarkAttemptStart() {
	acc := upstreamAccumulatorFrom(bc)
	if acc == nil {
		return
	}
	acc.attemptUpstream.Store(acc.total.Load())
	acc.attemptStartNanos.Store(time.Now().UnixNano())
	acc.attempts.Add(1)
}

// AttemptWindow is the span of one attempt on the request's upstream clock.
type AttemptWindow struct {
	Index         int // 1 for the request's first attempt
	Start         time.Time
	End           time.Time
	UpstreamStart time.Duration // request upstream total when the attempt began
	UpstreamEnd   time.Duration // request upstream total when the window was read
	StreamBefore  StreamTotals  // stream counters when the attempt began; set for stream rows only
}

// Upstream is the upstream time spent inside the window.
func (w AttemptWindow) Upstream() time.Duration {
	if d := w.UpstreamEnd - w.UpstreamStart; d > 0 {
		return d
	}
	return 0
}

// GetAttemptWindow returns the current attempt's window, ending now. ok is false
// when no accumulator is present or no attempt has been marked.
func GetAttemptWindow(ctx context.Context) (AttemptWindow, bool) {
	acc := upstreamAccumulatorFrom(ctx)
	if acc == nil {
		return AttemptWindow{}, false
	}
	startNanos := acc.attemptStartNanos.Load()
	if startNanos == 0 {
		return AttemptWindow{}, false
	}
	return AttemptWindow{
		Index:         int(acc.attempts.Load()),
		Start:         time.Unix(0, startNanos),
		End:           time.Now(),
		UpstreamStart: time.Duration(acc.attemptUpstream.Load()),
		UpstreamEnd:   time.Duration(acc.total.Load()),
	}, true
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
	if acc, ok := ctx.Value(BifrostContextKeyUpstreamLatency).(*upstreamAccumulator); ok && acc != nil {
		acc.total.Add(int64(d))
	}
}

// UpstreamWait is one open wait from BeginUpstreamWait; End closes it.
type UpstreamWait struct {
	acc *upstreamAccumulator
	ws  *upstreamWaitState
}

// BeginUpstreamWait opens a wait that may overlap others on the same request, such as
// parallel MCP tool calls. Overlapping waits add their union to the upstream total, not
// their sum, so concurrent calls never report more upstream time than wall time.
func BeginUpstreamWait(ctx context.Context) UpstreamWait {
	acc := upstreamAccumulatorFrom(ctx)
	if acc == nil {
		return UpstreamWait{}
	}
	ws := acc.wait.Load()
	if ws == nil {
		ws = &upstreamWaitState{}
		if !acc.wait.CompareAndSwap(nil, ws) {
			ws = acc.wait.Load()
		}
	}
	ws.mu.Lock()
	if ws.inFlight == 0 {
		ws.since = time.Now()
	}
	ws.inFlight++
	ws.mu.Unlock()
	return UpstreamWait{acc: acc, ws: ws}
}

// End closes the wait. The span since the first open wait is added once the last one closes.
func (w UpstreamWait) End() {
	ws := w.ws
	if ws == nil {
		return
	}
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if ws.inFlight == 0 {
		return
	}
	ws.inFlight--
	if ws.inFlight == 0 {
		w.acc.total.Add(int64(time.Since(ws.since)))
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
	if acc, ok := ctx.Value(BifrostContextKeyUpstreamLatency).(*upstreamAccumulator); ok && acc != nil {
		return time.Duration(acc.total.Load()), true
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

// PopulateUpstreamLatency is the A2A analogue of BifrostResponse.PopulateUpstreamLatency:
// it copies the accumulated upstream total onto the A2A response's ExtraFields.
// Left nil when no accumulator is present, so absent stays distinguishable from zero.
func (r *BifrostA2AResponse) PopulateUpstreamLatency(ctx context.Context) {
	if r == nil {
		return
	}
	upstream, ok := GetUpstreamLatency(ctx)
	if !ok {
		return
	}
	ms := int64(upstream / time.Millisecond)
	r.ExtraFields.UpstreamLatency = &ms
}

// PopulateOverheadLatency is the A2A analogue of BifrostResponse.PopulateOverheadLatency:
// it copies Bifrost's own cost onto the A2A response's ExtraFields, derived from total
// via CalculateOverhead. Left nil when no accumulator is present.
func (r *BifrostA2AResponse) PopulateOverheadLatency(ctx context.Context, total time.Duration) {
	if r == nil {
		return
	}
	overhead, ok := CalculateOverhead(ctx, total)
	if !ok {
		return
	}
	ms := int64(overhead / time.Millisecond)
	r.ExtraFields.OverheadLatency = &ms
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
