package bifrost

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestArmRequestTimeout_NoDeadlineIsNoOp verifies armRequestTimeout returns a
// no-op stop function and armed=false, and never cancels ctx, when no
// x-bf-request-timeout deadline was declared.
func TestArmRequestTimeout_NoDeadlineIsNoOp(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	stop, armed := armRequestTimeout(ctx)
	if armed {
		t.Fatal("armed must be false when no deadline was declared")
	}
	stop() // must not panic

	select {
	case <-ctx.Done():
		t.Fatal("context must not be cancelled when no deadline was declared")
	case <-time.After(50 * time.Millisecond):
	}
	fired, _ := ctx.Value(schemas.BifrostContextKeyRequestTimeoutFired).(bool)
	if fired {
		t.Fatal("fired sentinel must not be set when no deadline was declared")
	}
}

// TestArmRequestTimeout_FiresAndSetsSentinel verifies the armed timer cancels
// ctx and sets BifrostContextKeyRequestTimeoutFired when the declared
// deadline expires.
func TestArmRequestTimeout_FiresAndSetsSentinel(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	ctx.SetValue(schemas.BifrostContextKeyRequestTimeout, 20*time.Millisecond)

	stop, armed := armRequestTimeout(ctx)
	if !armed {
		t.Fatal("armed must be true when a deadline was declared")
	}
	defer stop()

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("timer did not cancel the context")
	}
	if ctx.Err() != context.Canceled {
		t.Fatalf("context.Err() must report context.Canceled once the timer fires, got %v", ctx.Err())
	}
	fired, _ := ctx.Value(schemas.BifrostContextKeyRequestTimeoutFired).(bool)
	if !fired {
		t.Fatal("fired sentinel must be set once the timer fires")
	}
}

// TestArmRequestTimeout_StopPreventsFiring verifies calling stop before the
// deadline elapses prevents the timer from ever cancelling ctx or setting the
// fired sentinel -- the normal path for a request that completes in time.
func TestArmRequestTimeout_StopPreventsFiring(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	ctx.SetValue(schemas.BifrostContextKeyRequestTimeout, 50*time.Millisecond)

	stop, armed := armRequestTimeout(ctx)
	if !armed {
		t.Fatal("armed must be true when a deadline was declared")
	}
	stop()

	select {
	case <-ctx.Done():
		t.Fatal("context must not be cancelled after stop was called before the deadline")
	case <-time.After(200 * time.Millisecond):
	}
	fired, _ := ctx.Value(schemas.BifrostContextKeyRequestTimeoutFired).(bool)
	if fired {
		t.Fatal("fired sentinel must not be set after stop was called before the deadline")
	}
}

// TestArmRequestTimeout_DerivedContextDoesNotCancelSharedRoot verifies the
// handleRequest/handleStreamRequest nil-ctx fix: deriving a fresh
// per-request context from the shared root (schemas.NewBifrostContext(root,
// schemas.NoDeadline), exactly as bifrost.ctx is derived from) rather than
// arming the timer directly on the root itself. A leaked
// BifrostContextKeyRequestTimeout value on the root (e.g. set on the ctx
// passed to Init) is still honored by armRequestTimeout on the derived
// context, but its Cancel() on firing must only affect that one derived
// context -- not the root, and not any other request derived from it.
// Cancelling the root must still propagate down to a derived context, since
// that path (e.g. Shutdown) is relied on elsewhere.
func TestArmRequestTimeout_DerivedContextDoesNotCancelSharedRoot(t *testing.T) {
	root, cancelRoot := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancelRoot()
	root.SetValue(schemas.BifrostContextKeyRequestTimeout, 20*time.Millisecond)

	reqA := schemas.NewBifrostContext(root, schemas.NoDeadline)
	reqB := schemas.NewBifrostContext(root, schemas.NoDeadline)

	stopA, armedA := armRequestTimeout(reqA)
	defer stopA()
	if !armedA {
		t.Fatal("expected the value inherited from root to still arm the derived context's timer")
	}

	select {
	case <-reqA.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("reqA's timer did not fire")
	}

	select {
	case <-reqB.Done():
		t.Fatal("reqA's timer firing must not cancel a sibling context derived from the same root")
	case <-time.After(50 * time.Millisecond):
	}
	if root.Err() != nil {
		t.Fatalf("reqA's timer firing must not cancel the shared root, got root.Err() = %v", root.Err())
	}

	cancelRoot()
	select {
	case <-reqB.Done():
	case <-time.After(time.Second):
		t.Fatal("cancelling the shared root must still propagate down to a derived context")
	}
}

// TestWrapStreamForRequestTimeout_NilChannelStopsImmediately verifies a nil
// input channel calls stop immediately and returns nil, matching the error
// return paths in handleStreamRequest that have no channel to wrap.
func TestWrapStreamForRequestTimeout_NilChannelStopsImmediately(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	stopped := false
	out := wrapStreamForRequestTimeout(ctx, nil, func() { stopped = true }, true)
	if out != nil {
		t.Fatal("expected nil passthrough for a nil input channel")
	}
	if !stopped {
		t.Fatal("stop must be called immediately for a nil input channel")
	}
}

// TestWrapStreamForRequestTimeout_StopsOnlyAfterDrain verifies stop runs only
// once the wrapped channel is fully drained and closed by the producer, not
// as soon as wrapStreamForRequestTimeout returns -- the deadline must keep
// the whole stream, not just its setup, in scope.
func TestWrapStreamForRequestTimeout_StopsOnlyAfterDrain(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	in := make(chan *schemas.BifrostStreamChunk, 2)
	in <- &schemas.BifrostStreamChunk{}
	in <- &schemas.BifrostStreamChunk{}
	close(in)

	var stopped atomicBool
	out := wrapStreamForRequestTimeout(ctx, in, func() { stopped.set(true) }, true)

	if stopped.get() {
		t.Fatal("stop must not be called before the caller has drained the channel")
	}

	count := 0
	for range out {
		count++
	}
	if count != 2 {
		t.Fatalf("expected 2 chunks forwarded, got %d", count)
	}

	deadline := time.Now().Add(time.Second)
	for !stopped.get() {
		if time.Now().After(deadline) {
			t.Fatal("stop was not called after the channel was fully drained")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestWrapStreamForRequestTimeout_CancelUnblocksAbandonedConsumer verifies
// that once ctx is cancelled, the proxy goroutine stops blocking on a send to
// an out channel nobody is reading anymore -- it falls back to draining ch
// (so the upstream producer is never left blocked writing into ch either)
// and still runs stop/close(out) once ch is exhausted, instead of leaking the
// goroutine forever on out <- chunk.
func TestWrapStreamForRequestTimeout_CancelUnblocksAbandonedConsumer(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	in := make(chan *schemas.BifrostStreamChunk)
	var stopped atomicBool
	out := wrapStreamForRequestTimeout(ctx, in, func() { stopped.set(true) }, true)

	// Normal path: producer sends, consumer receives.
	in <- &schemas.BifrostStreamChunk{}
	<-out

	// Producer sends a second chunk. The proxy receives it and blocks trying
	// to forward it to out -- the consumer has now abandoned the stream
	// (stopped reading out) without draining, e.g. an SDK caller that
	// returns on its own ctx cancellation instead of finishing its range
	// loop.
	in <- &schemas.BifrostStreamChunk{}

	// A third send from the producer must block: the proxy goroutine is
	// parked in the select trying to forward the second chunk, so it hasn't
	// looped back to receive from ch yet. This is the observable evidence
	// that the proxy is genuinely stuck on the abandoned out send pre-fix.
	thirdSent := make(chan struct{})
	go func() {
		in <- &schemas.BifrostStreamChunk{}
		close(thirdSent)
	}()

	select {
	case <-thirdSent:
		t.Fatal("third send completed before cancellation -- proxy was not actually blocked forwarding the abandoned chunk")
	case <-time.After(50 * time.Millisecond):
	}

	cancel()

	select {
	case <-thirdSent:
	case <-time.After(time.Second):
		t.Fatal("producer send into ch must not stay blocked forever once ctx is cancelled and the consumer has abandoned out")
	}

	close(in)

	deadline := time.Now().Add(time.Second)
	for !stopped.get() {
		if time.Now().After(deadline) {
			t.Fatal("stop was not called after ctx cancellation unblocked the abandoned consumer")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestWrapStreamForRequestTimeout_UnarmedReturnsOriginalChannel verifies that
// when no deadline was armed, wrapStreamForRequestTimeout returns the input
// channel unchanged instead of proxying it through an extra channel and
// goroutine -- a successful stream with no x-bf-request-timeout declared has
// nothing for the wrapper to enforce.
func TestWrapStreamForRequestTimeout_UnarmedReturnsOriginalChannel(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	in := make(chan *schemas.BifrostStreamChunk)
	stopCalled := false
	out := wrapStreamForRequestTimeout(ctx, in, func() { stopCalled = true }, false)

	if out != in {
		t.Fatal("expected the original channel back unwrapped when armed is false")
	}
	if stopCalled {
		t.Fatal("stop must not be called when armed is false -- the caller owns draining the unwrapped channel")
	}
}

// TestWrapStreamForRequestTimeout_NoForwardAfterCancellation verifies the
// forwarding send does not deliver a chunk once ctx is already cancelled --
// a plain `select { case out <- chunk: case <-ctx.Done(): }` does not
// prioritize either ready case, so without an explicit check first, a chunk
// could still be forwarded after the deadline fired. Cancelling before any
// chunk is produced, with an eager reader on out, makes both cases become
// ready at the same time under the old implementation; the leading
// non-blocking check on ctx.Done() must still win deterministically.
func TestWrapStreamForRequestTimeout_NoForwardAfterCancellation(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	cancel()

	in := make(chan *schemas.BifrostStreamChunk, 1)
	in <- &schemas.BifrostStreamChunk{}
	close(in)

	var stopped atomicBool
	out := wrapStreamForRequestTimeout(ctx, in, func() { stopped.set(true) }, true)

	select {
	case chunk, ok := <-out:
		if ok {
			t.Fatalf("expected no chunk forwarded once ctx was already cancelled, got %+v", chunk)
		}
	case <-time.After(time.Second):
		t.Fatal("out was never closed")
	}

	deadline := time.Now().Add(time.Second)
	for !stopped.get() {
		if time.Now().After(deadline) {
			t.Fatal("stop was not called after the cancelled-before-drain path completed")
		}
		time.Sleep(time.Millisecond)
	}
}

// atomicBool is a minimal race-safe bool for the drain-ordering assertion
// above (the proxy goroutine and the test goroutine both touch it).
type atomicBool struct {
	mu sync.Mutex
	v  bool
}

func (b *atomicBool) set(v bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.v = v
}

func (b *atomicBool) get() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.v
}
