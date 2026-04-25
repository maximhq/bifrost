// Package utils — stream-timeout observability + bounded-goroutine controls.
//
// This file isolates the production-hardening concerns added on top of the
// idle / total stream-timeout helpers in utils.go:
//
//   1. StreamController — forward-compatible abstraction that hides closeFn
//      so future evolution (e.g. moving to net/http or h2c with native
//      Read-cancellation) does not require touching every provider.
//      Constructed centrally via FastHTTPStreamController / NetHTTPStreamController.
//   2. Goroutine accounting — atomic counters for active stream-cancellation
//      goroutines + active per-Read goroutines. Exposed via package functions
//      so observability plugins or pprof handlers can read live values.
//   3. Hard cap — env-tunable upper bound on simultaneous timeout goroutines.
//      When exceeded, NEW timeout wrappers are REFUSED with ErrStreamTimeoutCapExceeded.
//      This is enforced at admission via TryAcquireStreamTimeoutSlot so callers
//      degrade gracefully (stream still proceeds without timeout enforcement,
//      with a structured WARN). No unbounded goroutine growth is possible.
//   4. Structured timeout-event logger — single hook used by every fired-path
//      with truthful semantics:
//        - close_invoked      : did we call the close function?
//        - close_result       : ok | err:<msg> | nil-controller
//        - termination_guarantee : best_effort  (we DO NOT guarantee the
//                                  upstream Read returned synchronously —
//                                  fasthttp's pooled keep-alive connection
//                                  may take milliseconds-to-RTT to actually
//                                  unblock at the kernel layer)
//
// Why "best_effort" not "terminated": *fasthttp.closeReader.CloseBodyStream
// flips a flag and the next Read OBSERVES the flag — but a Read already
// parked in net.Conn.Read remains parked until kernel TCP close / RST /
// SO_RCVTIMEO. We surface this honestly rather than claim guarantees the
// transport cannot deliver.
package utils

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/valyala/fasthttp"
)

// ---------------------------------------------------------------------------
// StreamController — forward-compatible Close abstraction.
// ---------------------------------------------------------------------------

// StreamController is the contract every provider must satisfy when handing a
// streaming response to the timeout helpers. It hides whether the underlying
// transport is fasthttp (resp.CloseBodyStream) or net/http (resp.Body.Close)
// and lets us evolve the close path without touching providers.
//
// Implementations MUST be safe to invoke from any goroutine. The helpers
// guarantee Close is called at most once via sync.Once but a defensive
// implementation should still tolerate concurrent / repeated calls.
type StreamController interface {
	// Close terminates the upstream stream and unblocks any pending Read.
	// Returns the underlying Close error verbatim (helpers log it but do not
	// propagate to the caller).
	Close() error
}

// streamControllerFunc adapts a closeFn into a StreamController.
type streamControllerFunc struct {
	fn    StreamCloseFunc
	label string
}

func (s streamControllerFunc) Close() error {
	if s.fn == nil {
		return ErrNilStreamController
	}
	return s.fn()
}

// ErrNilStreamController is returned by helpers when closeFn is nil. Reaches
// the caller via ApplyStreamTimeouts so the provider knows enforcement is
// disabled and can decide whether to proceed.
var ErrNilStreamController = errors.New("bifrost: nil stream controller (closeFn missing)")

// ErrStreamTimeoutCapExceeded is returned by TryAcquireStreamTimeoutSlot
// when active timeout goroutines have hit the configured hard cap. Callers
// MUST handle this — typically by logging and proceeding without timeout
// enforcement (graceful degradation) rather than failing the whole stream.
var ErrStreamTimeoutCapExceeded = errors.New("bifrost: stream timeout goroutine cap exceeded")

// FastHTTPStreamController returns a StreamController backed by fasthttp's
// CloseBodyStream. Returns ErrNilStreamController-bearing controller if resp
// is nil (so the helpers surface the misuse via structured logs and the
// strict-contract path in ApplyStreamTimeouts).
//
// This is the canonical constructor for fasthttp-based providers. Providers
// MUST NOT manually wire `func() error { return resp.CloseBodyStream() }`;
// using this helper guarantees uniform error semantics and lets us evolve
// the close path (e.g. add metrics, tracing) in one place.
func FastHTTPStreamController(resp *fasthttp.Response) StreamController {
	if resp == nil {
		return streamControllerFunc{fn: nil, label: "fasthttp:nil-response"}
	}
	return streamControllerFunc{
		fn:    func() error { return resp.CloseBodyStream() },
		label: "fasthttp",
	}
}

// NetHTTPStreamController returns a StreamController backed by net/http's
// Body.Close. Use for providers built on standard net/http (e.g. Bedrock).
func NetHTTPStreamController(body io.Closer) StreamController {
	if body == nil {
		return streamControllerFunc{fn: nil, label: "net/http:nil-body"}
	}
	return streamControllerFunc{
		fn:    body.Close,
		label: "net/http",
	}
}

// NewStreamController wraps an arbitrary close function (test fakes, custom
// transports). A nil fn is a programming bug; this constructor logs a
// structured WARN at construction time so the misconfiguration is visible
// even if the stream never reaches a fired path. Helpers using a nil-fn
// controller will return ErrNilStreamController from their cleanup.
func NewStreamController(fn StreamCloseFunc, debugLabel string) StreamController {
	if fn == nil {
		getLogger().Warn(fmt.Sprintf(
			`{"event":"stream.controller.nil","label":%q,"impact":"timeouts disabled; upstream Read cannot be force-terminated"}`,
			debugLabel,
		))
	}
	return streamControllerFunc{fn: fn, label: debugLabel}
}

// asCloseFn pulls the StreamCloseFunc out of any StreamController. Used by
// the existing helpers (NewIdleTimeoutReader, NewTotalTimeoutReader,
// SetupStreamCancellation, ApplyStreamTimeouts) to keep their internal
// signatures unchanged while letting callers pass a controller.
func asCloseFn(c StreamController) StreamCloseFunc {
	if c == nil {
		return nil
	}
	if scf, ok := c.(streamControllerFunc); ok {
		return scf.fn
	}
	return c.Close
}

// controllerLabel extracts the label set by the typed constructors above.
// Returns "unknown" for foreign StreamController implementations.
func controllerLabel(c StreamController) string {
	if scf, ok := c.(streamControllerFunc); ok && scf.label != "" {
		return scf.label
	}
	return "unknown"
}

// ---------------------------------------------------------------------------
// Goroutine accounting + soft cap.
// ---------------------------------------------------------------------------

const (
	// envMaxStreamTimeoutGoroutines tunes the soft cap. 0 / unset → 4096.
	envMaxStreamTimeoutGoroutines = "BIFROST_MAX_STREAM_TIMEOUT_GOROUTINES"
	defaultMaxStreamTimeoutGoroutines int64 = 4096
)

var (
	// activeStreamCancellationGoroutines counts SetupStreamCancellation watchers
	// that have not yet exited (one per active stream).
	activeStreamCancellationGoroutines atomic.Int64

	// activeStreamReadGoroutines counts inner-Read goroutines spawned by
	// idleTimeoutReader.Read. Should typically equal the number of streams
	// currently blocked in Read (i.e. close to active streams, not multiples).
	activeStreamReadGoroutines atomic.Int64

	// totalStreamIdleTimeoutsFired / totalStreamTotalTimeoutsFired count
	// lifetime fired events. Useful for prometheus pull / debug endpoints.
	totalStreamIdleTimeoutsFired  atomic.Int64
	totalStreamTotalTimeoutsFired atomic.Int64
	totalStreamCtxCancelsFired    atomic.Int64

	// totalStreamCloseFnInvocations counts each successful at-most-once
	// closeFn dispatch (from any of the three trigger paths).
	totalStreamCloseFnInvocations atomic.Int64

	// maxStreamTimeoutGoroutines caches the parsed env value. Loaded once;
	// subsequent reads via maxStreamTimeoutGoroutinesValue() snapshot atomic.
	maxStreamTimeoutGoroutines atomic.Int64
)

func init() {
	maxStreamTimeoutGoroutines.Store(parseMaxStreamTimeoutGoroutines())
}

func parseMaxStreamTimeoutGoroutines() int64 {
	v, _ := strconv.ParseInt(os.Getenv(envMaxStreamTimeoutGoroutines), 10, 64)
	if v <= 0 {
		return defaultMaxStreamTimeoutGoroutines
	}
	return v
}

// MaxStreamTimeoutGoroutines returns the configured soft cap. Operators can
// raise it if they expect very high streaming concurrency.
func MaxStreamTimeoutGoroutines() int64 { return maxStreamTimeoutGoroutines.Load() }

// SetMaxStreamTimeoutGoroutines is exposed for tests and admin handlers.
func SetMaxStreamTimeoutGoroutines(n int64) {
	if n <= 0 {
		n = defaultMaxStreamTimeoutGoroutines
	}
	maxStreamTimeoutGoroutines.Store(n)
}

// StreamTimeoutMetricsSnapshot is a lightweight read-only view of the live
// goroutine / fired counters. Returned by StreamTimeoutMetrics.
type StreamTimeoutMetricsSnapshot struct {
	ActiveCancellationGoroutines int64 `json:"active_cancellation_goroutines"`
	ActiveReadGoroutines         int64 `json:"active_read_goroutines"`
	TotalIdleTimeoutsFired       int64 `json:"total_idle_timeouts_fired"`
	TotalTotalTimeoutsFired      int64 `json:"total_total_timeouts_fired"`
	TotalCtxCancelsFired         int64 `json:"total_ctx_cancels_fired"`
	TotalCloseFnInvocations      int64 `json:"total_closefn_invocations"`
	TotalAdmissionsRejected      int64 `json:"total_admissions_rejected"`
	HardCapGoroutines            int64 `json:"hard_cap_goroutines"`
}

// StreamTimeoutMetrics returns a snapshot of the current goroutine + fired
// counters. Safe to call concurrently. Useful from /debug/pprof and from
// observability plugins.
func StreamTimeoutMetrics() StreamTimeoutMetricsSnapshot {
	return StreamTimeoutMetricsSnapshot{
		ActiveCancellationGoroutines: activeStreamCancellationGoroutines.Load(),
		ActiveReadGoroutines:         activeStreamReadGoroutines.Load(),
		TotalIdleTimeoutsFired:       totalStreamIdleTimeoutsFired.Load(),
		TotalTotalTimeoutsFired:      totalStreamTotalTimeoutsFired.Load(),
		TotalCtxCancelsFired:         totalStreamCtxCancelsFired.Load(),
		TotalCloseFnInvocations:      totalStreamCloseFnInvocations.Load(),
		TotalAdmissionsRejected:      totalStreamTimeoutAdmissionsRejected.Load(),
		HardCapGoroutines:            maxStreamTimeoutGoroutines.Load(),
	}
}

// totalStreamTimeoutAdmissionsRejected counts the number of times the hard
// cap rejected a new timeout slot. Useful operational metric.
var totalStreamTimeoutAdmissionsRejected atomic.Int64

// TryAcquireStreamTimeoutSlot atomically reserves a goroutine slot under the
// hard cap. Returns ErrStreamTimeoutCapExceeded if the cap would be exceeded.
//
// On success the caller MUST call ReleaseStreamTimeoutSlot exactly once when
// the goroutine exits. On failure the caller must NOT spawn the goroutine.
//
// This is the only admission path for stream-cancellation goroutines —
// SetupStreamCancellation calls it internally. ApplyStreamTimeouts uses the
// same accounting via NewIdleTimeoutReader's internal Read goroutine, which
// is gated by activeStreamReadGoroutines (one-per-stream by construction).
func TryAcquireStreamTimeoutSlot() error {
	capVal := maxStreamTimeoutGoroutines.Load()
	if capVal <= 0 {
		activeStreamCancellationGoroutines.Add(1)
		return nil
	}
	for {
		current := activeStreamCancellationGoroutines.Load()
		if current >= capVal {
			n := totalStreamTimeoutAdmissionsRejected.Add(1)
			// Log on every rejection BUT only the first 100 per process to
			// avoid log-flood under sustained saturation; absolute counter
			// remains accurate via StreamTimeoutMetrics().
			if n <= 100 {
				getLogger().Warn(fmt.Sprintf(
					`{"event":"stream.timeout.admission_rejected","active":%d,"cap":%d,"total_rejected":%d,"action":"caller-degrades-without-timeout-enforcement"}`,
					current, capVal, n,
				))
			}
			return ErrStreamTimeoutCapExceeded
		}
		if activeStreamCancellationGoroutines.CompareAndSwap(current, current+1) {
			return nil
		}
	}
}

// ReleaseStreamTimeoutSlot releases a slot acquired via TryAcquireStreamTimeoutSlot.
func ReleaseStreamTimeoutSlot() { activeStreamCancellationGoroutines.Add(-1) }

// noteStreamCancellationStarted is retained as a thin wrapper for callers
// that have already passed admission via TryAcquireStreamTimeoutSlot.
// Direct callers (none in production code) are deprecated; admission MUST
// go through TryAcquireStreamTimeoutSlot.
func noteStreamCancellationStopped() { ReleaseStreamTimeoutSlot() }

func noteStreamReadStarted() { activeStreamReadGoroutines.Add(1) }
func noteStreamReadStopped() { activeStreamReadGoroutines.Add(-1) }

// ---------------------------------------------------------------------------
// Structured timeout event logging.
// ---------------------------------------------------------------------------

// streamTimeoutEvent is the canonical fired-path log shape. Emitted exactly
// once per fired trigger (idle / total / ctx-cancel) by the helpers.
//
// Format is JSON-in-message so existing structured logger backends (zerolog,
// zap, etc.) parse it. We intentionally do not introduce a new logger
// interface here — the existing schemas.Logger is sufficient.
type streamTimeoutEventReason string

const (
	streamTimeoutReasonIdle      streamTimeoutEventReason = "idle"
	streamTimeoutReasonTotal     streamTimeoutEventReason = "total"
	streamTimeoutReasonCtxCancel streamTimeoutEventReason = "ctx_cancel"
)

// emitStreamTimeoutEvent records and logs a fired stream-timeout with
// truthful, unambiguous semantics:
//
//   - close_invoked         : did we actually call the controller's Close?
//   - close_result          : ok | err:<msg> | nil-controller (close_invoked=false)
//   - termination_guarantee : best_effort
//
// We deliberately do NOT log close=ok in a way that implies the upstream
// Read returned synchronously. fasthttp's CloseBodyStream flips a flag; the
// kernel TCP read is unblocked by the OS when the peer flushes / RSTs /
// SO_RCVTIMEO fires. From the user's Read perspective our wrapper returns
// the timeout sentinel immediately (via the goroutine + select pattern),
// but the inner goroutine may live a few extra ms-to-RTT until the kernel
// closes. Calling that "terminated" would be a lie.
func emitStreamTimeoutEvent(reason streamTimeoutEventReason, configured time.Duration, elapsed time.Duration, closeFnNil bool, closeFnErr error) {
	switch reason {
	case streamTimeoutReasonIdle:
		totalStreamIdleTimeoutsFired.Add(1)
	case streamTimeoutReasonTotal:
		totalStreamTotalTimeoutsFired.Add(1)
	case streamTimeoutReasonCtxCancel:
		totalStreamCtxCancelsFired.Add(1)
	}
	closeInvoked := !closeFnNil
	if closeInvoked && closeFnErr == nil {
		totalStreamCloseFnInvocations.Add(1)
	}

	closeResult := "ok"
	switch {
	case closeFnNil:
		closeResult = "nil-controller"
	case closeFnErr != nil:
		closeResult = "err:" + closeFnErr.Error()
	}

	getLogger().Warn(fmt.Sprintf(
		`{"event":"stream.timeout.fired","reason":%q,"configured_ms":%d,"elapsed_ms":%d,"close_invoked":%t,"close_result":%q,"termination_guarantee":"best_effort"}`,
		reason,
		configured.Milliseconds(),
		elapsed.Milliseconds(),
		closeInvoked,
		closeResult,
	))
}
