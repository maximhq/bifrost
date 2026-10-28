package bifrost

import (
	"context"
	"runtime"
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

// TestArmRequestTimeout_NestedArmKeepsOuterBudget covers the MCP agent loop:
// ChatCompletionRequest arms once, then each turn's handleRequest arms again on
// the same ctx. The inner call must not start a fresh full-length timer, and
// stopping it must not stop the outer one, or tool execution and later turns
// could run past the caller's declared deadline.
func TestArmRequestTimeout_NestedArmKeepsOuterBudget(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	ctx.SetValue(schemas.BifrostContextKeyRequestTimeout, 100*time.Millisecond)

	stopOuter, armed := armRequestTimeout(ctx)
	if !armed {
		t.Fatal("outer arm must report armed")
	}
	defer stopOuter()

	time.Sleep(60 * time.Millisecond)
	stopInner, innerArmed := armRequestTimeout(ctx)
	if !innerArmed {
		t.Fatal("a nested arm must still report armed so streams get wrapped")
	}
	stopInner()

	select {
	case <-ctx.Done():
	case <-time.After(90 * time.Millisecond):
		t.Fatal("outer deadline did not fire on schedule: the nested arm restarted or stopped the budget")
	}
	fired, _ := ctx.Value(schemas.BifrostContextKeyRequestTimeoutFired).(bool)
	if !fired {
		t.Fatal("fired sentinel must be set by the outer timer")
	}
}

// TestArmRequestTimeout_ConcurrentCallsKeepADeadline covers two independent
// calls sharing one caller ctx. The first call finishing must not stop the
// timer the second is still relying on: the shared timer stops only when the
// last caller stops.
func TestArmRequestTimeout_ConcurrentCallsKeepADeadline(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	ctx.SetValue(schemas.BifrostContextKeyRequestTimeout, 50*time.Millisecond)

	stopA, _ := armRequestTimeout(ctx)
	stopB, armedB := armRequestTimeout(ctx)
	if !armedB {
		t.Fatal("second call must report armed")
	}
	defer stopB()
	stopA()

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("the first call's stop left the second call with no deadline")
	}
}

// TestArmRequestTimeout_ConcurrentArmsStartOneTimer verifies concurrent first
// arms on one ctx share a single timer rather than each starting their own,
// and that the timer stops once every caller has stopped.
func TestArmRequestTimeout_ConcurrentArmsStartOneTimer(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	ctx.SetValue(schemas.BifrostContextKeyRequestTimeout, 100*time.Millisecond)

	const callers = 32
	stops := make([]func(), callers)
	var wg sync.WaitGroup
	for i := range stops {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			stops[i], _ = armRequestTimeout(ctx)
		}(i)
	}
	wg.Wait()

	arm, _ := ctx.Value(requestTimeoutArmedKey{}).(*requestTimeoutArm)
	if arm == nil || arm.refs != callers {
		t.Fatalf("expected one shared arm with %d refs, got %+v", callers, arm)
	}
	for _, stop := range stops {
		stop()
	}
	select {
	case <-ctx.Done():
		t.Fatal("timer fired after every caller had stopped")
	case <-time.After(200 * time.Millisecond):
	}
}

// TestArmRequestTimeout_StopReleasesNesting verifies a caller context reused
// for a later, sequential request gets a live timer again: the outer stop
// must clear the nesting marker rather than leave every later arm a no-op.
func TestArmRequestTimeout_StopReleasesNesting(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	ctx.SetValue(schemas.BifrostContextKeyRequestTimeout, 30*time.Millisecond)

	stopFirst, _ := armRequestTimeout(ctx)
	stopFirst()

	stopSecond, armed := armRequestTimeout(ctx)
	if !armed {
		t.Fatal("second arm must report armed")
	}
	defer stopSecond()

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("second request ran without a timer: the first stop did not clear the nesting marker")
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

// TestWrapStreamForRequestTimeout_FiredDeadlineEmitsTerminalError verifies
// that when ctx is cancelled because the declared deadline actually fired
// (BifrostContextKeyRequestTimeoutFired set, exactly as armRequestTimeout's
// timer sets it), the wrapper forwards a terminal 504 RequestTimedOut chunk
// before closing out -- a deadline-truncated stream must read to the HTTP
// transport as an error, not as a normal completion that gets a [DONE].
func TestWrapStreamForRequestTimeout_FiredDeadlineEmitsTerminalError(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	in := make(chan *schemas.BifrostStreamChunk)
	out := wrapStreamForRequestTimeout(ctx, in, func() {}, true)

	in <- &schemas.BifrostStreamChunk{}
	<-out

	ctx.SetValue(schemas.BifrostContextKeyRequestTimeoutFired, true)
	cancel()
	close(in)

	chunk, ok := <-out
	if !ok {
		t.Fatal("expected a terminal error chunk, got out closed with nothing forwarded")
	}
	if chunk.BifrostError == nil {
		t.Fatalf("expected a terminal chunk carrying BifrostError, got %+v", chunk)
	}
	if chunk.BifrostError.StatusCode == nil || *chunk.BifrostError.StatusCode != 504 {
		t.Fatalf("expected StatusCode 504, got %v", chunk.BifrostError.StatusCode)
	}
	if chunk.BifrostError.Error == nil || chunk.BifrostError.Error.Type == nil || *chunk.BifrostError.Error.Type != schemas.RequestTimedOut {
		t.Fatalf("expected Error.Type RequestTimedOut, got %+v", chunk.BifrostError.Error)
	}

	if _, ok := <-out; ok {
		t.Fatal("expected out to close after the terminal error chunk")
	}
}

// TestWrapStreamForRequestTimeout_FiredAfterNaturalCloseStillEmitsError
// covers the race where the producer closes ch on its own in the same
// instant the deadline fires, so neither ctx.Done() branch inside the loop
// ever runs -- the post-loop check must still catch BifrostContextKeyRequestTimeoutFired
// and emit the terminal error instead of treating the natural close as success.
func TestWrapStreamForRequestTimeout_FiredAfterNaturalCloseStillEmitsError(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	ctx.SetValue(schemas.BifrostContextKeyRequestTimeoutFired, true)
	cancel()

	in := make(chan *schemas.BifrostStreamChunk)
	close(in)

	out := wrapStreamForRequestTimeout(ctx, in, func() {}, true)

	chunk, ok := <-out
	if !ok {
		t.Fatal("expected a terminal error chunk even though ch closed naturally")
	}
	if chunk.BifrostError == nil || chunk.BifrostError.Error == nil || chunk.BifrostError.Error.Type == nil || *chunk.BifrostError.Error.Type != schemas.RequestTimedOut {
		t.Fatalf("expected a RequestTimedOut terminal chunk, got %+v", chunk)
	}

	if _, ok := <-out; ok {
		t.Fatal("expected out to close after the terminal error chunk")
	}
}

// TestWrapStreamForRequestTimeout_UnfiredCancellationForwardsNoError verifies
// the negative case: an ordinary cancellation that never set
// BifrostContextKeyRequestTimeoutFired (e.g. a client disconnect) must not
// manufacture a RequestTimedOut error -- there is no declared deadline to
// blame, and the existing drop-on-cancellation behavior
// (TestWrapStreamForRequestTimeout_NoForwardAfterCancellation) must hold.
func TestWrapStreamForRequestTimeout_UnfiredCancellationForwardsNoError(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	cancel()

	in := make(chan *schemas.BifrostStreamChunk, 1)
	in <- &schemas.BifrostStreamChunk{}
	close(in)

	out := wrapStreamForRequestTimeout(ctx, in, func() {}, true)

	if chunk, ok := <-out; ok {
		t.Fatalf("expected out to close with nothing forwarded, got %+v", chunk)
	}
}

// TestWrapStreamForRequestTimeout_FiredDeadlineDeliversToBusyConsumer
// verifies the terminal error chunk is still delivered when the consumer is
// briefly busy (e.g. mid-write to the client socket) rather than already
// waiting on a receive -- out is unbuffered, so a bare non-blocking send
// would drop the chunk in that common case. The consumer here only starts
// reading after the deadline has already fired and forwardTimeoutChunkIfFired
// has started its bounded wait, mirroring a slow write that outlasts the
// instant the deadline fires.
func TestWrapStreamForRequestTimeout_FiredDeadlineDeliversToBusyConsumer(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	ctx.SetValue(schemas.BifrostContextKeyRequestTimeoutFired, true)
	cancel()

	in := make(chan *schemas.BifrostStreamChunk)
	close(in)
	out := wrapStreamForRequestTimeout(ctx, in, func() {}, true)

	// Give forwardTimeoutChunkIfFired time to start its send attempt before
	// this goroutine reads out -- without a grace period the earlier
	// non-blocking send would already have hit its default branch by now.
	time.Sleep(50 * time.Millisecond)

	select {
	case chunk, ok := <-out:
		if !ok {
			t.Fatal("expected a terminal error chunk, got out closed with nothing forwarded")
		}
		if chunk.BifrostError == nil || chunk.BifrostError.Error == nil || chunk.BifrostError.Error.Type == nil || *chunk.BifrostError.Error.Type != schemas.RequestTimedOut {
			t.Fatalf("expected a RequestTimedOut terminal chunk, got %+v", chunk)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("terminal error chunk was not delivered to the delayed consumer")
	}
}

// TestWrapStreamForRequestTimeout_SilentSourceStillReportsTimeout covers a
// source that neither sends nor closes, such as a plugin short-circuit stream
// that ignores ctx. The deadline fires while the wrapper is waiting on the
// receive: it must still forward the timeout and close out after the drain
// grace rather than hold the caller's stream open forever, and a
// producer that sends later must not be left blocked.
func TestWrapStreamForRequestTimeout_SilentSourceStillReportsTimeout(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	in := make(chan *schemas.BifrostStreamChunk)
	var stopped atomicBool
	out := wrapStreamWithDrainGrace(ctx, in, func() { stopped.set(true) }, true, 50*time.Millisecond)

	time.Sleep(20 * time.Millisecond) // wrapper is now parked on the receive
	ctx.SetValue(schemas.BifrostContextKeyRequestTimeoutFired, true)
	cancel()

	select {
	case chunk, ok := <-out:
		if !ok || chunk.BifrostError == nil || chunk.BifrostError.Error == nil || chunk.BifrostError.Error.Type == nil || *chunk.BifrostError.Error.Type != schemas.RequestTimedOut {
			t.Fatalf("expected a RequestTimedOut chunk while the source was silent, got %+v (open=%v)", chunk, ok)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout was not reported while the wrapper waited on a silent source")
	}
	select {
	case _, ok := <-out:
		if ok {
			t.Fatal("expected out to close after the timeout chunk")
		}
	case <-time.After(time.Second):
		t.Fatal("out stayed open past the drain grace on a source that never closes")
	}
	if !stopped.get() {
		t.Fatal("stop was not called once out closed")
	}

	sent := make(chan struct{})
	go func() {
		in <- &schemas.BifrostStreamChunk{}
		close(sent)
	}()
	select {
	case <-sent:
	case <-time.After(time.Second):
		t.Fatal("a producer sending after the grace period was left blocked")
	}
	close(in)
}

// TestResolveRequestContext_RootPassedExplicitlyIsTreatedAsShared verifies
// the fix for the case where a caller never actually passes nil: every
// public entry point (e.g. ChatCompletionRequest) substitutes bifrost.ctx for
// a nil ctx itself before handleRequest/handleStreamRequest runs, so
// resolveRequestContext must treat ctx == root the same as ctx == nil --
// otherwise arming the timer directly on root would let one such request's
// deadline cancel every other request sharing that root.
func TestResolveRequestContext_RootPassedExplicitlyIsTreatedAsShared(t *testing.T) {
	root, cancelRoot := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancelRoot()
	root.SetValue(schemas.BifrostContextKeyRequestTimeout, time.Minute)

	resolved, owned := resolveRequestContext(root, root)
	if owned == nil {
		t.Fatal("passing root explicitly must still derive an owned context, exactly like passing nil")
	}
	if resolved == root {
		t.Fatal("the resolved context must not be root itself")
	}
}

// TestResolveRequestContext_CallerSuppliedContextIsNotOwned verifies a
// caller-supplied context is returned untouched and never reported as owned --
// cancelling it would cancel a context whose lifetime the caller controls.
func TestResolveRequestContext_CallerSuppliedContextIsNotOwned(t *testing.T) {
	root, cancelRoot := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancelRoot()
	root.SetValue(schemas.BifrostContextKeyRequestTimeout, time.Minute)

	caller, cancelCaller := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancelCaller()

	resolved, owned := resolveRequestContext(caller, root)
	if resolved != caller {
		t.Fatal("a caller-supplied context must be returned unchanged")
	}
	if owned != nil {
		t.Fatal("a caller-supplied context must never be reported as owned")
	}
}

// TestResolveRequestContext_NoDeadlineDoesNotDerive verifies the nil-ctx path
// aliases root directly when no deadline is declared. Deriving here would start
// a watchCancellation goroutine that nothing releases until Shutdown, leaking
// one goroutine per nil-ctx request.
func TestResolveRequestContext_NoDeadlineDoesNotDerive(t *testing.T) {
	root, cancelRoot := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancelRoot()

	resolved, owned := resolveRequestContext(nil, root)
	if resolved != root {
		t.Fatal("with no deadline declared, the nil-ctx path must alias root rather than derive")
	}
	if owned != nil {
		t.Fatal("nothing is owned when no context was derived")
	}
}

// TestResolveRequestContext_DeadlineDerivesOwnedChild verifies that when a
// deadline IS declared the nil-ctx path derives an isolated child, reports it
// as owned, still inherits the declared deadline, and that cancelling it
// leaves root and its other children alone.
func TestResolveRequestContext_DeadlineDerivesOwnedChild(t *testing.T) {
	root, cancelRoot := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancelRoot()
	root.SetValue(schemas.BifrostContextKeyRequestTimeout, time.Minute)

	resolved, owned := resolveRequestContext(nil, root)
	if owned == nil {
		t.Fatal("a declared deadline must derive an owned context to isolate Cancel() from root")
	}
	if resolved != owned {
		t.Fatal("the resolved context must be the derived one")
	}
	if resolved == root {
		t.Fatal("the derived context must not be root itself")
	}
	if d, _ := resolved.Value(schemas.BifrostContextKeyRequestTimeout).(time.Duration); d != time.Minute {
		t.Fatalf("the derived context must inherit the declared deadline, got %v", d)
	}

	sibling := schemas.NewBifrostContext(root, schemas.NoDeadline)
	defer sibling.Cancel()

	owned.Cancel()
	if root.Err() != nil {
		t.Fatalf("releasing an owned context must not cancel root, got %v", root.Err())
	}
	select {
	case <-sibling.Done():
		t.Fatal("releasing an owned context must not cancel a sibling request")
	case <-time.After(50 * time.Millisecond):
	}
}

// TestResolveRequestContext_ReleasedContextLeaksNoGoroutine is the regression
// guard for the nil-ctx goroutine leak: resolving and then releasing many
// nil-ctx requests must leave no watchCancellation goroutines behind. Before
// the fix, every nil-ctx request derived a child whose watchCancellation
// goroutine blocked on root until Shutdown, whether or not a deadline was
// declared.
func TestResolveRequestContext_ReleasedContextLeaksNoGoroutine(t *testing.T) {
	root, cancelRoot := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancelRoot()

	const requests = 200

	// Exercise both shapes: no deadline declared (must not derive at all), and
	// a deadline declared (derives, and the owned context must be released).
	for _, declareDeadline := range []bool{false, true} {
		if declareDeadline {
			root.SetValue(schemas.BifrostContextKeyRequestTimeout, time.Hour)
		}

		settle(t)
		before := runtime.NumGoroutine()

		for i := 0; i < requests; i++ {
			_, owned := resolveRequestContext(nil, root)
			if owned != nil {
				owned.Cancel()
			}
		}

		settle(t)
		after := runtime.NumGoroutine()

		// A small allowance absorbs unrelated runtime/test scheduler noise;
		// the leak this guards against grows with `requests`, so it lands far
		// outside it.
		if grew := after - before; grew > requests/10 {
			t.Fatalf("declareDeadline=%v: %d goroutines still running after %d released nil-ctx requests (before=%d after=%d)",
				declareDeadline, grew, requests, before, after)
		}
	}
}

// TestResolveRequestContext_StreamPathReleasesOwnedContextAfterDrain mirrors
// how handleStreamRequest composes resolveRequestContext, armRequestTimeout and
// wrapStreamForRequestTimeout: the stream returns to its caller before it is
// drained, so the owned context cannot be released with a defer and is instead
// folded into the stop function that runs once the stream is exhausted. Guards
// the ordering too -- the context must still be live while chunks are being
// forwarded, and released only after the channel closes.
func TestResolveRequestContext_StreamPathReleasesOwnedContextAfterDrain(t *testing.T) {
	root, cancelRoot := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancelRoot()
	root.SetValue(schemas.BifrostContextKeyRequestTimeout, time.Hour)

	ctx, ownedCtx := resolveRequestContext(nil, root)
	if ownedCtx == nil {
		t.Fatal("expected an owned context for a nil-ctx request with a declared deadline")
	}

	stopRequestTimeout, armed := armRequestTimeout(ctx)
	if !armed {
		t.Fatal("armed must be true whenever a context was derived, or the wrapped-stream path never runs stop")
	}
	stopTimer := stopRequestTimeout
	stopRequestTimeout = func() {
		stopTimer()
		ownedCtx.Cancel()
	}

	in := make(chan *schemas.BifrostStreamChunk, 1)
	out := wrapStreamForRequestTimeout(ctx, in, stopRequestTimeout, armed)

	in <- &schemas.BifrostStreamChunk{}
	<-out
	if ctx.Err() != nil {
		t.Fatalf("the owned context must stay live while the stream is being drained, got %v", ctx.Err())
	}

	close(in)
	for range out {
	}

	deadline := time.Now().Add(2 * time.Second)
	for ctx.Err() == nil {
		if time.Now().After(deadline) {
			t.Fatal("the owned context was never released after the stream was fully drained")
		}
		time.Sleep(time.Millisecond)
	}
	if root.Err() != nil {
		t.Fatalf("releasing the owned context must not cancel root, got %v", root.Err())
	}
}

// settle gives finished goroutines a chance to actually exit before
// NumGoroutine is sampled; watchCancellation returns asynchronously after its
// context is cancelled.
func settle(t *testing.T) {
	t.Helper()
	for i := 0; i < 50; i++ {
		runtime.Gosched()
		time.Sleep(2 * time.Millisecond)
	}
}

// BenchmarkRequestTimeoutUnset measures what x-bf-request-timeout adds to a
// request that does not declare one, the default: the work ChatCompletionRequest
// and handleRequest/handleStreamRequest do (resolve, arm, stop, stream wrap).
// Expected to allocate nothing.
func BenchmarkRequestTimeoutUnset(b *testing.B) {
	root := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	caller := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ch := make(chan *schemas.BifrostStreamChunk)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, in := range []*schemas.BifrostContext{caller, root} {
			ctx, owned := resolveRequestContext(in, root)
			if owned != nil {
				b.Fatal("derived a context with no timeout declared")
			}
			stop, armed := armRequestTimeout(ctx)
			if wrapStreamForRequestTimeout(ctx, ch, stop, armed) != ch {
				b.Fatal("wrapped a stream with no timeout declared")
			}
			stop()
		}
	}
}

// BenchmarkRequestTimeoutSet measures the cost when a caller declares a
// timeout: one outer arm plus a nested per-turn arm, as an MCP agent call does.
func BenchmarkRequestTimeoutSet(b *testing.B) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestTimeout, time.Minute)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stopOuter, _ := armRequestTimeout(ctx)
		stopInner, _ := armRequestTimeout(ctx)
		stopInner()
		stopOuter()
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
