package bifrost

import (
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// armRequestTimeout arms a timer for the total request/stream deadline
// declared via the x-bf-request-timeout header (schemas.BifrostContextKeyRequestTimeout),
// if any, and returns a stop function that must be called exactly once when
// the request (handleRequest) or the whole stream across every attempt and
// fallback (handleStreamRequest) truly completes. stop is a no-op if no
// deadline was declared.
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
func armRequestTimeout(ctx *schemas.BifrostContext) (stop func()) {
	d, _ := ctx.Value(schemas.BifrostContextKeyRequestTimeout).(time.Duration)
	if d <= 0 {
		return func() {}
	}
	timer := time.AfterFunc(d, func() {
		ctx.SetValue(schemas.BifrostContextKeyRequestTimeoutFired, true)
		ctx.Cancel()
	})
	return func() {
		timer.Stop()
	}
}

// wrapStreamForRequestTimeout proxies ch so stop runs once ch is fully
// drained (closed by the producer), rather than as soon as
// handleStreamRequest returns the channel to its caller -- the caller may
// still be reading chunks long after handleStreamRequest itself has
// returned, and the deadline must keep the whole stream, not just its setup,
// in scope.
func wrapStreamForRequestTimeout(ch chan *schemas.BifrostStreamChunk, stop func()) chan *schemas.BifrostStreamChunk {
	if ch == nil {
		stop()
		return ch
	}
	out := make(chan *schemas.BifrostStreamChunk)
	go func() {
		defer close(out)
		defer stop()
		for chunk := range ch {
			out <- chunk
		}
	}()
	return out
}
