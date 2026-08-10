package bifrost

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestArmRequestTimeout_NoDeadlineIsNoOp verifies armRequestTimeout returns a
// no-op stop function, and never cancels ctx, when no x-bf-request-timeout
// deadline was declared.
func TestArmRequestTimeout_NoDeadlineIsNoOp(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	stop := armRequestTimeout(ctx)
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

	stop := armRequestTimeout(ctx)
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

	stop := armRequestTimeout(ctx)
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

// TestWrapStreamForRequestTimeout_NilChannelStopsImmediately verifies a nil
// input channel calls stop immediately and returns nil, matching the error
// return paths in handleStreamRequest that have no channel to wrap.
func TestWrapStreamForRequestTimeout_NilChannelStopsImmediately(t *testing.T) {
	stopped := false
	out := wrapStreamForRequestTimeout(nil, func() { stopped = true })
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
	in := make(chan *schemas.BifrostStreamChunk, 2)
	in <- &schemas.BifrostStreamChunk{}
	in <- &schemas.BifrostStreamChunk{}
	close(in)

	var stopped atomicBool
	out := wrapStreamForRequestTimeout(in, func() { stopped.set(true) })

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
