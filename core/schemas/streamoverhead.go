package schemas

import (
	"context"
	"sync/atomic"
	"time"
)

// Streaming overhead attribution.
//
// For a streamed response, Bifrost's overhead (total wall minus the upstream
// socket total) is dominated by work the provider goroutine does between socket
// reads: decoding each SSE event's JSON, mapping it to the Bifrost shape, and
// handing each chunk to the transport. None of that sits on an instrumented span,
// so it all lands in the breakdown's "core" bucket, which for streams can be
// hundreds of ms and says nothing about where the time went.
//
// This accumulator splits that time into the SAME phases a unary request already
// reports, so a stream's breakdown reads like a unary one:
//   - parse:   per-event JSON decode        -> folds into "response-parse" (Serialization)
//   - convert: per-event struct->unified map -> folds into "convertor" (Convertor)
//   - backpressure: time blocked handing chunks to the transport. This has no unary
//     twin and is not Bifrost CPU (it is the client/transport draining slowly), so it
//     gets its own bucket rather than joining a compute phase.
//
// Each category is an *atomic.Int64 (nanoseconds) on a single struct pointer
// installed once per request; per-chunk adds are lock-free and safe from the
// provider goroutine after the handler has returned (same reasoning as the upstream
// accumulator: the pointer is installed once, then only dereferenced).
type streamOverhead struct {
	parseNs        atomic.Int64 // per-event SSE JSON decode CPU
	convertNs      atomic.Int64 // per-event provider->Bifrost struct mapping CPU
	backpressureNs atomic.Int64 // time blocked handing a chunk to the transport (downstream/client backpressure)
}

// ResetStreamOverhead installs a fresh accumulator on ctx. Call once at stream
// entry, before the provider goroutine starts, alongside ResetUpstreamLatency.
func (bc *BifrostContext) ResetStreamOverhead() {
	if bc == nil {
		return
	}
	bc.setReservedValue(BifrostContextKeyStreamOverhead, &streamOverhead{})
}

// streamOverheadFrom returns the accumulator on ctx, or nil when absent (internal
// calls, tests, SDK callers that never reset). A nil accumulator is silently
// ignored by the Add helpers: this is telemetry and must never break a request.
func streamOverheadFrom(ctx context.Context) *streamOverhead {
	if isNilContext(ctx) {
		return nil
	}
	acc, _ := ctx.Value(BifrostContextKeyStreamOverhead).(*streamOverhead)
	return acc
}

// AddStreamParse adds d to the request's per-event JSON-decode total.
func AddStreamParse(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	if acc := streamOverheadFrom(ctx); acc != nil {
		acc.parseNs.Add(int64(d))
	}
}

// AddStreamConvert adds d to the request's per-event struct-mapping total.
func AddStreamConvert(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	if acc := streamOverheadFrom(ctx); acc != nil {
		acc.convertNs.Add(int64(d))
	}
}

// AddStreamBackpressure adds d to the request's downstream-backpressure total: the
// time the provider goroutine spends blocked handing a chunk to the transport
// (client-side slowness), which today masquerades as Bifrost overhead.
func AddStreamBackpressure(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	if acc := streamOverheadFrom(ctx); acc != nil {
		acc.backpressureNs.Add(int64(d))
	}
}

// StampStreamOverhead writes the accumulated stream phases onto the request's root
// span so the overhead breakdown can carve them out of "core" and fold them into
// the matching unary phases. Mirrors StampUpstreamLatency: call at stream
// completion, once the accumulator has stopped growing. Best-effort; a request with
// no tracer or no accumulator is left unstamped, so absent stays distinct from zero.
func (bc *BifrostContext) StampStreamOverhead() {
	if bc == nil {
		return
	}
	acc := streamOverheadFrom(bc)
	if acc == nil {
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
	if parse := acc.parseNs.Load(); parse > 0 {
		tracer.SetAttribute(handle, AttrBifrostStreamParseMs, float64(parse)/float64(time.Millisecond))
	}
	if convert := acc.convertNs.Load(); convert > 0 {
		tracer.SetAttribute(handle, AttrBifrostStreamConvertMs, float64(convert)/float64(time.Millisecond))
	}
	if bp := acc.backpressureNs.Load(); bp > 0 {
		tracer.SetAttribute(handle, AttrBifrostStreamBackpressureMs, float64(bp)/float64(time.Millisecond))
	}
}

// StampWorkerHandoff writes the worker->caller goroutine-hop latency onto the
// request's root span so the overhead breakdown can carve it out of "core" into a
// "worker-handoff" bucket. d is time.Since(the worker's pre-send timestamp),
// measured by tryRequest the moment it receives the result/error. Best-effort:
// no tracer / no trace / non-positive d leaves the root span unstamped, so absent
// stays distinct from zero.
func (bc *BifrostContext) StampWorkerHandoff(d time.Duration) {
	if bc == nil || d <= 0 {
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
	tracer.SetAttribute(handle, AttrBifrostWorkerHandoffMs, float64(d)/float64(time.Millisecond))
}

// StampStreamTransport writes the transport goroutine's per-chunk split onto the
// root span: (B) outbound convert+marshal CPU and (A) client-socket write wait.
// These run concurrently with the provider goroutine, so they are NOT part of the
// overhead total; the breakdown uses them only as weights to split the provider-side
// backpressure bucket into its (A) client vs (B) Bifrost composition. Called from the
// transport goroutine after its send loop drains, before trace completion. Values are
// passed in (accumulated locally in the transport loop) rather than via the shared
// context, so reachability of the accumulator across goroutines is never in question.
func (bc *BifrostContext) StampStreamTransport(transportCPU, clientWrite time.Duration) {
	if bc == nil {
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
	handle := tracer.GetSpanHandleByID(traceID, nil)
	if handle == nil {
		return
	}
	if transportCPU > 0 {
		tracer.SetAttribute(handle, AttrBifrostStreamTransportCPUMs, float64(transportCPU)/float64(time.Millisecond))
	}
	if clientWrite > 0 {
		tracer.SetAttribute(handle, AttrBifrostStreamClientWriteMs, float64(clientWrite)/float64(time.Millisecond))
	}
}
