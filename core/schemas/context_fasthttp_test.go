//go:build !tinygo && !wasm

package schemas

import (
	"sync"
	"testing"

	"github.com/valyala/fasthttp"
)

// TestBifrostContext_Value_DoesNotConsultPooledRequestCtx is the deterministic
// half of the streaming use-after-release crash (the chronic nil-pointer panic
// in HandleAnthropicResponsesStream → PostLLMHook → BifrostContext.Value).
//
// Production shape:
//
//	transports/bifrost-http/lib/ctx.go:199
//	    parent := context.Context(ctx)            // ctx is *fasthttp.RequestCtx
//	    bifrostCtx, _ = NewBifrostContextWithCancel(parent)
//	    ctx.VisitUserValuesAll(...)               // every user value is COPIED in
//
// The Anthropic stream goroutine outlives the HTTP handler. Once the handler
// returns, fasthttp resets and pools the RequestCtx. The detached goroutine
// then runs PostLLMHook, which reads many *optional* keys; any key that misses
// the local userValues map falls through to bc.parent.Value(key) — i.e. into
// the recycled RequestCtx — and SIGSEGVs in (*userData).Get.
//
// Because ConvertToBifrostContext already copies every RequestCtx user value
// into userValues, the RequestCtx parent is never legitimately needed for value
// lookups. The contract this test locks in: BifrostContext.Value must NOT read
// through to a *fasthttp.RequestCtx parent.
//
// Pre-fix:  bc.Value(key) returns the RequestCtx-only value (proves the unsafe
//           fall-through link exists) → test FAILS.
// Post-fix: bc.Value(key) returns nil without touching the pooled ctx → PASSES.
func TestBifrostContext_Value_DoesNotConsultPooledRequestCtx(t *testing.T) {
	var rctx fasthttp.RequestCtx
	rctx.Init(&fasthttp.Request{}, nil, nil) // wires ctx.s = fakeServer (non-cancelling)

	// A value present ONLY on the RequestCtx, never copied into the bifrost ctx.
	// This stands in for any key that misses userValues in PostLLMHook.
	key := "request-ctx-only-key"
	rctx.SetUserValue(key, "leaked-from-pooled-ctx")

	bc := NewBifrostContext(&rctx, NoDeadline)

	if got := bc.Value(key); got != nil {
		t.Fatalf("BifrostContext.Value fell through to the pooled *fasthttp.RequestCtx "+
			"(got %v); after the fix it must return nil without reading the recycled ctx", got)
	}
}

// TestBifrostContext_Value_RacesRecycledRequestCtx faithfully reproduces the
// production crash. Run with `go test -race` to deterministically surface the
// data race; without -race, the concurrent reset/reuse below will eventually
// SIGSEGV inside (*userData).Get — the exact frame seen in production.
//
// The reader goroutine is the detached Anthropic stream goroutine reading a key
// that misses userValues. The writer goroutine is fasthttp recycling/reusing
// the same RequestCtx for the next connection (Reset + SetUserValue mutate
// req.userValues concurrently with the read of *d in (*userData).Get).
//
// With the fix, bc.Value never reads the RequestCtx, so neither the race nor
// the crash exists.
func TestBifrostContext_Value_RacesRecycledRequestCtx(t *testing.T) {
	var rctx fasthttp.RequestCtx
	rctx.Init(&fasthttp.Request{}, nil, nil)

	bc := NewBifrostContext(&rctx, NoDeadline)

	const iterations = 100_000
	var wg sync.WaitGroup
	wg.Add(2)

	// Detached stream goroutine: read a key that is not in userValues, so Value
	// falls through to bc.parent (the RequestCtx).
	go func() {
		defer wg.Done()
		for range iterations {
			_ = bc.Value("a-key-that-was-never-set")
		}
	}()

	// fasthttp recycling the pooled RequestCtx for subsequent requests.
	go func() {
		defer wg.Done()
		for i := range iterations {
			rctx.Request.Reset()
			rctx.SetUserValue("reused", i)
		}
	}()

	wg.Wait()
}
