package bifrost

import (
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// resolveRequestContext resolves the context handleRequest/handleStreamRequest
// runs on when the caller passed nil, and reports which context (if any) this
// call owns and must therefore release once the request completes. A
// caller-supplied context distinct from root is never owned, so it is never
// released here; it is cancelled only if the deadline the caller declared on it
// fires, as with context.WithTimeout. It is not swapped for a derived child,
// because core writes per-request values (response headers, routing info)
// with SetValue, which a child keeps to itself, and the caller reads them back
// from its own context.
//
// ctx == root is treated the same as ctx == nil: every public entry point
// (ChatCompletionRequest and its siblings) already substitutes bifrost.ctx for
// a nil ctx before handleRequest/handleStreamRequest ever runs, so by the time
// this function sees ctx it already equals root for every nil-ctx caller --
// nil itself no longer reaches here in practice. Treating only literal nil as
// shared would make the derivation below dead code: root would be returned
// unchanged, and arming the timer directly on root would let one slow
// request's deadline cancel every other nil-ctx request sharing it.
//
// A child is derived only when root actually declares a request timeout. The
// derivation exists solely so armRequestTimeout's ctx.Cancel() on expiry cannot
// reach root -- the single long-lived context every nil-ctx caller shares --
// and there is nothing to isolate when no timer will ever fire.
//
// Deriving unconditionally instead would leak a goroutine per nil-ctx request:
// NewBifrostContext starts watchCancellation whenever the parent is
// cancellable, and for a child with no deadline of its own that goroutine
// blocks on parent.Done() (root, cancelled only at Shutdown), its own nil
// timer channel, and its own done channel -- so nothing releases it until the
// child is cancelled.
func resolveRequestContext(ctx *schemas.BifrostContext, root *schemas.BifrostContext) (resolved *schemas.BifrostContext, owned *schemas.BifrostContext) {
	if ctx != nil && ctx != root {
		return ctx, nil
	}
	if d, _ := root.Value(schemas.BifrostContextKeyRequestTimeout).(time.Duration); d > 0 {
		owned = schemas.NewBifrostContext(root, schemas.NoDeadline)
		return owned, owned
	}
	return root, nil
}

// requestTimeoutFired reports whether ctx's declared x-bf-request-timeout
// deadline has fired. nil-safe for callers that may pass no ctx.
func requestTimeoutFired(ctx *schemas.BifrostContext) bool {
	if ctx == nil {
		return false
	}
	fired, _ := ctx.Value(schemas.BifrostContextKeyRequestTimeoutFired).(bool)
	return fired
}

// requestTimeoutArmedKey holds the *requestTimeoutArm live on a ctx.
type requestTimeoutArmedKey struct{}

// requestTimeoutArm is one timer shared by every armRequestTimeout call on a
// ctx; the last stop stops it. requestTimeoutArmMu guards refs and the
// get-or-create, so concurrent first arms cannot each start a timer. owner is
// the ctx the arm was stored on. A derived child sees it through Value but
// never joins it, see armRequestTimeout.
type requestTimeoutArm struct {
	owner *schemas.BifrostContext
	refs  int
	timer *time.Timer
}

var requestTimeoutArmMu sync.Mutex

// armRequestTimeout arms a timer for the total request/stream deadline
// declared via the x-bf-request-timeout header (schemas.BifrostContextKeyRequestTimeout),
// if any, and returns a stop function that must be called exactly once when
// the request (handleRequest) or the whole stream across every attempt and
// fallback (handleStreamRequest) truly completes, plus whether a deadline was
// actually armed. stop is a no-op when armed is false. Callers that
// conditionally wrap a stream (see wrapStreamForRequestTimeout) use armed to
// skip wrapping when there is nothing to enforce.
//
// On expiry the timer sets BifrostContextKeyRequestTimeoutFired before
// cancelling ctx, so PostLLMHook plugins can distinguish a declared deadline
// firing from an arbitrary caller-disconnect or network cancellation -- the
// same distinction BifrostContextKeyStreamEndIndicator makes for the
// terminal-chunk case.
//
// ctx here is the context handleRequest/handleStreamRequest received directly
// from the public API surface (Go SDK caller or the HTTP transport's
// ConvertToBifrostContext), before any plugin hook has run -- WithPluginScope
// wrapping only happens later, when core calls into an individual plugin's
// hook method -- so ctx.Cancel() here needs no Root() indirection.
//
// Calls nest: while a timer armed on ctx is live, a further call on the same
// ctx shares it instead of starting a fresh one, and the timer stops only when
// the last of those calls stops. The MCP agent loop depends on this:
// ChatCompletionRequest and ResponsesRequest arm once around the whole call,
// and each turn's handleRequest inside it must not restart the caller's
// budget. Two concurrent calls on one caller ctx share the budget too, measured
// from the first arm; they cannot have independent ones, since either timer
// firing cancels the ctx both run on. What the count prevents is the first
// call's stop leaving the second with no deadline at all.
//
// Only an arm stored on this exact ctx is shared. A ctx derived from one that
// is armed reads the parent's arm through Value, but it may declare a shorter
// timeout of its own, so it arms its own timer. The parent's deadline still
// reaches it, because cancelling the parent cancels the child, so the child
// ends at whichever deadline comes first.
func armRequestTimeout(ctx *schemas.BifrostContext) (stop func(), armed bool) {
	d, _ := ctx.Value(schemas.BifrostContextKeyRequestTimeout).(time.Duration)
	if d <= 0 {
		return func() {}, false
	}
	requestTimeoutArmMu.Lock()
	arm, _ := ctx.Value(requestTimeoutArmedKey{}).(*requestTimeoutArm)
	if arm == nil || arm.owner != ctx {
		arm = &requestTimeoutArm{owner: ctx, timer: time.AfterFunc(d, func() {
			ctx.SetValue(schemas.BifrostContextKeyRequestTimeoutFired, true)
			ctx.Cancel()
		})}
		ctx.SetValue(requestTimeoutArmedKey{}, arm)
	}
	arm.refs++
	requestTimeoutArmMu.Unlock()
	return func() {
		requestTimeoutArmMu.Lock()
		defer requestTimeoutArmMu.Unlock()
		arm.refs--
		if arm.refs == 0 {
			arm.timer.Stop()
			arm.owner.SetValue(requestTimeoutArmedKey{}, (*requestTimeoutArm)(nil))
		}
	}, true
}

// wrapStreamForRequestTimeout proxies ch so stop runs once ch is fully
// drained (closed by the producer), rather than as soon as
// handleStreamRequest returns the channel to its caller -- the caller may
// still be reading chunks long after handleStreamRequest itself has
// returned, and the deadline must keep the whole stream, not just its setup,
// in scope.
//
// Returns ch unwrapped when armed is false (no deadline was declared): stop
// is a no-op in that case too, so there is nothing to enforce, and every
// successful stream would otherwise pay for an extra channel and forwarding
// goroutine it never needed.
//
// The forwarding send checks ctx.Done() before each send, and again
// alongside it in the select: a consumer that abandons the stream (e.g. an
// SDK caller returning on its own ctx cancellation instead of finishing the
// range loop) would otherwise leave `out <- chunk` blocked forever, so
// close(out)/stop() would never run. The leading check also keeps a chunk
// from being forwarded in the same instant cancellation fires -- select does
// not otherwise prefer one ready case over another, so without it a chunk
// could still win the race against an already-closed ctx.Done(). The receive
// from ch selects on ctx.Done() too, so a source that goes silent without
// closing cannot keep the timeout from being reported. Once cancelled, the
// goroutine stops forwarding and drains ch, see drainAfterCancel, before
// running its deferred cleanup.
func wrapStreamForRequestTimeout(ctx *schemas.BifrostContext, ch chan *schemas.BifrostStreamChunk, stop func(), armed bool) chan *schemas.BifrostStreamChunk {
	return wrapStreamWithDrainGrace(ctx, ch, stop, armed, cancelDrainGrace)
}

func wrapStreamWithDrainGrace(ctx *schemas.BifrostContext, ch chan *schemas.BifrostStreamChunk, stop func(), armed bool, drainGrace time.Duration) chan *schemas.BifrostStreamChunk {
	if ch == nil {
		stop()
		return ch
	}
	if !armed {
		return ch
	}
	out := make(chan *schemas.BifrostStreamChunk)
	go func() {
		defer close(out)
		defer stop()
		for {
			var chunk *schemas.BifrostStreamChunk
			var ok bool
			select {
			case <-ctx.Done():
				forwardTimeoutChunkIfFired(ctx, out)
				drainAfterCancel(ch, drainGrace)
				return
			case chunk, ok = <-ch:
			}
			if !ok {
				// ch closed without either cancellation check observing
				// ctx.Done(): the deadline can still have fired in the same
				// instant the producer finished, or the caller sees a
				// truncated response close like any successful stream.
				forwardTimeoutChunkIfFired(ctx, out)
				return
			}
			select {
			case <-ctx.Done():
				forwardTimeoutChunkIfFired(ctx, out)
				drainAfterCancel(ch, drainGrace)
				return
			default:
			}
			select {
			case out <- chunk:
			case <-ctx.Done():
				forwardTimeoutChunkIfFired(ctx, out)
				drainAfterCancel(ch, drainGrace)
				return
			}
		}
	}()
	return out
}

// cancelDrainGrace bounds how long drainAfterCancel holds the wrapped stream
// open waiting for the producer to close ch.
const cancelDrainGrace = 5 * time.Second

// drainAfterCancel discards what is left on ch after cancellation so the
// producer is never left blocked writing into it. A provider closes ch
// promptly once ctx is cancelled, so the drain normally finishes in place,
// before close(out), and the transport still sees the producer's cancellation
// post-hook and log land before it completes the trace. A source that neither
// sends nor closes (a plugin short-circuit stream that ignores ctx) must not
// hold the caller's stream open, so after the grace period the rest of the
// drain moves to a background goroutine; it ends whenever that source closes.
func drainAfterCancel(ch chan *schemas.BifrostStreamChunk, grace time.Duration) {
	timer := time.NewTimer(grace)
	defer timer.Stop()
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-timer.C:
			go func() {
				for range ch {
				}
			}()
			return
		}
	}
}

// requestTimeoutStreamChunk is the terminal chunk forwardTimeoutChunkIfFired
// sends when the declared x-bf-request-timeout deadline actually expired.
// Shaped like providerUtils.NewBifrostTimeoutError (StatusCode 504,
// Error.Type RequestTimedOut) so the HTTP transport's handleStreaming routes
// it through the route's error converter and ends the stream, instead of
// treating ch's close as normal completion and sending the client a
// successful [DONE].
func requestTimeoutStreamChunk() *schemas.BifrostStreamChunk {
	statusCode := 504
	errorType := schemas.RequestTimedOut
	return &schemas.BifrostStreamChunk{
		BifrostError: &schemas.BifrostError{
			IsBifrostError: true,
			StatusCode:     &statusCode,
			Error: &schemas.ErrorField{
				Type:    &errorType,
				Message: "request timed out: the x-bf-request-timeout deadline was reached before the stream finished",
			},
		},
	}
}

// forwardTimeoutChunkSendGrace bounds how long forwardTimeoutChunkIfFired
// blocks trying to deliver the terminal chunk. out is unbuffered, so a plain
// non-blocking send only succeeds if the consumer happens to already be
// waiting on a receive -- the common HTTP transport consumer is instead
// mid-write to the client socket for the previous chunk at the exact moment
// the deadline fires, so a bare `select { default: }` would drop the error
// almost every time. The grace period gives a merely-busy consumer -- which
// always loops back to receive again, see handleStreaming's `for chunk :=
// range streamChan` -- time to come back, while still bounding how long this
// goroutine waits for a consumer that is never coming back (the SDK-caller
// abandonment case). 5s comfortably covers one slow chunk write without
// meaningfully extending how long a truly abandoned goroutine lingers.
const forwardTimeoutChunkSendGrace = 5 * time.Second

// forwardTimeoutChunkIfFired makes a best-effort, bounded-blocking send of
// requestTimeoutStreamChunk on out, but only when ctx was cancelled because
// the request-timeout deadline actually fired (BifrostContextKeyRequestTimeoutFired) --
// never for an arbitrary cancellation such as a client disconnect, where there
// is no caller left to report the error to and no declared deadline to blame.
// Bounded: an abandoned consumer (nobody reading out any more, see
// TestWrapStreamForRequestTimeout_CancelUnblocksAbandonedConsumer) must not
// re-block this goroutine on out <- chunk forever, just long enough to give a
// merely-busy consumer a real chance to receive it.
func forwardTimeoutChunkIfFired(ctx *schemas.BifrostContext, out chan<- *schemas.BifrostStreamChunk) {
	if !requestTimeoutFired(ctx) {
		return
	}
	select {
	case out <- requestTimeoutStreamChunk():
	case <-time.After(forwardTimeoutChunkSendGrace):
	}
}
