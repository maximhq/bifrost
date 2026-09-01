package bifrost

import (
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// resolveRequestContext resolves the context handleRequest/handleStreamRequest
// runs on when the caller passed nil, and reports which context (if any) this
// call owns and must therefore release once the request completes. A
// caller-supplied context is never owned and must never be cancelled here.
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
	if ctx != nil {
		return ctx, nil
	}
	if d, _ := root.Value(schemas.BifrostContextKeyRequestTimeout).(time.Duration); d > 0 {
		owned = schemas.NewBifrostContext(root, schemas.NoDeadline)
		return owned, owned
	}
	return root, nil
}

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
func armRequestTimeout(ctx *schemas.BifrostContext) (stop func(), armed bool) {
	d, _ := ctx.Value(schemas.BifrostContextKeyRequestTimeout).(time.Duration)
	if d <= 0 {
		return func() {}, false
	}
	timer := time.AfterFunc(d, func() {
		ctx.SetValue(schemas.BifrostContextKeyRequestTimeoutFired, true)
		ctx.Cancel()
	})
	return func() {
		timer.Stop()
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
// could still win the race against an already-closed ctx.Done(). Once
// cancelled, the goroutine stops trying to forward and instead just drains
// ch to exhaustion -- discarding chunks instead of leaving the producer
// blocked writing into ch -- before running its deferred cleanup.
func wrapStreamForRequestTimeout(ctx *schemas.BifrostContext, ch chan *schemas.BifrostStreamChunk, stop func(), armed bool) chan *schemas.BifrostStreamChunk {
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
		for chunk := range ch {
			select {
			case <-ctx.Done():
				for range ch {
				}
				return
			default:
			}
			select {
			case out <- chunk:
			case <-ctx.Done():
				for range ch {
				}
				return
			}
		}
	}()
	return out
}
