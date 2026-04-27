package streaming

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// newTestAccumulator returns a real Accumulator wired up like production.
func newTestAccumulator(t *testing.T) *Accumulator {
	t.Helper()
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	a := NewAccumulator(nil, logger)
	t.Cleanup(func() { a.Cleanup() })
	return a
}

// makeChunks builds n distinct *BifrostStreamChunk pointers tracked by reference equality.
func makeChunks(n int) []*schemas.BifrostStreamChunk {
	out := make([]*schemas.BifrostStreamChunk, n)
	for i := 0; i < n; i++ {
		out[i] = &schemas.BifrostStreamChunk{
			BifrostChatResponse: &schemas.BifrostChatResponse{ID: fmt.Sprintf("chunk-%d", i)},
		}
	}
	return out
}

// recorder consumes chunks off a channel, recording each with its arrival timestamp.
type recorder struct {
	mu      sync.Mutex
	chunks  []*schemas.BifrostStreamChunk
	stamps  []time.Time
	ch      chan *schemas.BifrostStreamChunk
	stopped chan struct{}
}

func newRecorder(bufSize int) *recorder {
	r := &recorder{
		ch:      make(chan *schemas.BifrostStreamChunk, bufSize),
		stopped: make(chan struct{}),
	}
	go r.run()
	return r
}

func (r *recorder) run() {
	defer close(r.stopped)
	for c := range r.ch {
		r.mu.Lock()
		r.chunks = append(r.chunks, c)
		r.stamps = append(r.stamps, time.Now())
		r.mu.Unlock()
	}
}

func (r *recorder) close() {
	close(r.ch)
	<-r.stopped
}

func (r *recorder) waitFor(n int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		got := len(r.chunks)
		r.mu.Unlock()
		if got >= n {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return false
}

func (r *recorder) snapshot() ([]*schemas.BifrostStreamChunk, []time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cs := make([]*schemas.BifrostStreamChunk, len(r.chunks))
	ts := make([]time.Time, len(r.stamps))
	copy(cs, r.chunks)
	copy(ts, r.stamps)
	return cs, ts
}

// TestGate_HappyPath: pause at 3, resume at 7. 1-2 live; 3-6 buffered until
// resume; 7-9 live; 10 final. Order preserved, gatePausedAt == 2.
func TestGate_HappyPath(t *testing.T) {
	a := newTestAccumulator(t)
	traceID := "trace-happy"
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	r := newRecorder(32)
	defer r.close()

	chunks := makeChunks(10)

	for i := 0; i < 2; i++ {
		if !a.GateSend(traceID, chunks[i], false, false, r.ch, ctx) {
			t.Fatalf("chunk %d: send returned false", i)
		}
	}
	if !r.waitFor(2, time.Second) {
		t.Fatalf("expected 2 chunks live before pause")
	}

	a.PauseStream(traceID)
	for i := 2; i < 6; i++ {
		if !a.GateSend(traceID, chunks[i], false, false, r.ch, ctx) {
			t.Fatalf("chunk %d: send returned false", i)
		}
	}
	// Verify chunks 3-6 are NOT delivered while paused.
	time.Sleep(20 * time.Millisecond)
	cs, _ := r.snapshot()
	if len(cs) != 2 {
		t.Fatalf("expected 2 chunks pre-resume, got %d", len(cs))
	}
	pauseObservedAt := time.Now()

	a.ResumeStream(traceID)
	for i := 6; i < 9; i++ {
		if !a.GateSend(traceID, chunks[i], false, false, r.ch, ctx) {
			t.Fatalf("chunk %d: send returned false", i)
		}
	}
	if !a.GateSend(traceID, chunks[9], true, false, r.ch, ctx) {
		t.Fatalf("final chunk: send returned false")
	}

	// Wait for the flusher to drain everything.
	sa := mustGet(t, a, traceID)
	sa.WaitForFlusher()
	if !r.waitFor(10, time.Second) {
		t.Fatalf("expected 10 chunks total")
	}

	cs, ts := r.snapshot()
	if len(cs) != 10 {
		t.Fatalf("expected 10 chunks, got %d", len(cs))
	}
	for i := 0; i < 10; i++ {
		if cs[i] != chunks[i] {
			t.Fatalf("position %d: pointer mismatch", i)
		}
	}
	for i := 2; i < 6; i++ {
		if !ts[i].After(pauseObservedAt) {
			t.Fatalf("chunk %d delivered before resume (stamp=%v, observed=%v)", i, ts[i], pauseObservedAt)
		}
	}
	if got := sa.gatePausedAt; got != 2 {
		t.Fatalf("gatePausedAt=%d, want 2", got)
	}
}

// TestGate_FinalChunkWhilePaused: a terminal chunk arriving while paused is
// buffered, not delivered. Resume drains all buffered chunks (including the
// terminal one) in order and transitions the gate to Ended.
func TestGate_FinalChunkWhilePaused(t *testing.T) {
	a := newTestAccumulator(t)
	traceID := "trace-final-while-paused"
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	r := newRecorder(32)
	defer r.close()
	chunks := makeChunks(6)

	if !a.GateSend(traceID, chunks[0], false, false, r.ch, ctx) {
		t.Fatalf("setup chunk 0: GateSend returned false")
	}
	a.PauseStream(traceID)
	for i := 1; i < 5; i++ {
		if !a.GateSend(traceID, chunks[i], false, false, r.ch, ctx) {
			t.Fatalf("setup chunk %d (paused): GateSend returned false", i)
		}
	}
	if !a.GateSend(traceID, chunks[5], true, false, r.ch, ctx) {
		t.Fatalf("setup terminal chunk: GateSend returned false")
	}

	// Pre-resume invariant: only chunks[0] (sent before pause) was delivered.
	// All subsequent chunks — including the terminal one — must be held.
	time.Sleep(20 * time.Millisecond)
	if pre, _ := r.snapshot(); len(pre) != 1 {
		t.Fatalf("expected 1 chunk delivered while paused, got %d", len(pre))
	}

	sa := mustGet(t, a, traceID)
	sa.mu.Lock()
	state := sa.gateState
	pending := sa.gatePendingTerminal
	sa.mu.Unlock()
	if state != StreamStatePaused {
		t.Fatalf("expected Paused before Resume, got %v", state)
	}
	if !pending {
		t.Fatalf("expected gatePendingTerminal to be set after a final chunk arrived while paused")
	}

	a.ResumeStream(traceID)
	sa.WaitForFlusher()
	if !r.waitFor(6, time.Second) {
		t.Fatalf("expected 6 chunks delivered")
	}
	cs, _ := r.snapshot()
	for i := 0; i < 6; i++ {
		if cs[i] != chunks[i] {
			t.Fatalf("order mismatch at %d", i)
		}
	}
	if sa.gateState != StreamStateEnded {
		t.Fatalf("expected Ended, got %v", sa.gateState)
	}
}

// TestGate_HardErrorWhilePaused: a hard provider error arriving while paused
// is buffered like any other terminal. Resume drains in order and transitions
// the gate to Ended; the error chunk is delivered last.
func TestGate_HardErrorWhilePaused(t *testing.T) {
	a := newTestAccumulator(t)
	traceID := "trace-hard-err"
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	r := newRecorder(32)
	defer r.close()
	chunks := makeChunks(5)

	if !a.GateSend(traceID, chunks[0], false, false, r.ch, ctx) {
		t.Fatalf("setup chunk 0: GateSend returned false")
	}
	a.PauseStream(traceID)
	for i := 1; i < 4; i++ {
		if !a.GateSend(traceID, chunks[i], false, false, r.ch, ctx) {
			t.Fatalf("setup chunk %d (paused): GateSend returned false", i)
		}
	}
	hardErr := &schemas.BifrostStreamChunk{BifrostError: &schemas.BifrostError{IsBifrostError: true, Error: &schemas.ErrorField{Message: "boom"}}}
	if !a.GateSend(traceID, hardErr, false, true, r.ch, ctx) {
		t.Fatalf("setup hard-error chunk: GateSend returned false")
	}

	time.Sleep(20 * time.Millisecond)
	if pre, _ := r.snapshot(); len(pre) != 1 {
		t.Fatalf("expected 1 chunk delivered while paused, got %d", len(pre))
	}

	sa := mustGet(t, a, traceID)
	sa.mu.Lock()
	state := sa.gateState
	pending := sa.gatePendingTerminal
	sa.mu.Unlock()
	if state != StreamStatePaused {
		t.Fatalf("expected Paused before Resume, got %v", state)
	}
	if !pending {
		t.Fatalf("expected gatePendingTerminal after hard error arrived while paused")
	}

	a.ResumeStream(traceID)
	sa.WaitForFlusher()
	if !r.waitFor(5, time.Second) {
		t.Fatalf("expected 5 chunks delivered")
	}
	cs, _ := r.snapshot()
	for i := 0; i < 4; i++ {
		if cs[i] != chunks[i] {
			t.Fatalf("order mismatch at %d", i)
		}
	}
	if cs[4] != hardErr {
		t.Fatalf("expected hard error chunk last")
	}
	if sa.gateState != StreamStateEnded {
		t.Fatalf("expected Ended after Resume drained the terminal, got %v", sa.gateState)
	}
}

// TestGate_PauseStreamMultipleCalls: PauseStream is idempotent. Calling it
// repeatedly while already paused must be a no-op — no extra state churn,
// gatePausedAt latches to the seq at the first transition, and a single
// matching Resume drains the buffer cleanly.
func TestGate_PauseStreamMultipleCalls(t *testing.T) {
	a := newTestAccumulator(t)
	traceID := "trace-pause-multi"
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	r := newRecorder(32)
	defer r.close()
	chunks := makeChunks(5)

	// Two live chunks, then pause repeatedly.
	a.GateSend(traceID, chunks[0], false, false, r.ch, ctx)
	a.GateSend(traceID, chunks[1], false, false, r.ch, ctx)
	if !r.waitFor(2, time.Second) {
		t.Fatalf("expected 2 chunks live before pause")
	}

	a.PauseStream(traceID)
	sa := mustGet(t, a, traceID)
	sa.mu.Lock()
	firstPausedAt := sa.gatePausedAt
	sa.mu.Unlock()

	// Repeat pause calls — must not change state or move gatePausedAt.
	for i := 0; i < 5; i++ {
		a.PauseStream(traceID)
	}
	sa.mu.Lock()
	state := sa.gateState
	pausedAt := sa.gatePausedAt
	sa.mu.Unlock()
	if state != StreamStatePaused {
		t.Fatalf("expected Paused after repeated PauseStream, got %v", state)
	}
	if pausedAt != firstPausedAt {
		t.Fatalf("gatePausedAt moved on repeated pause: first=%d after-repeat=%d", firstPausedAt, pausedAt)
	}

	// Send chunks while paused — they must be buffered.
	for i := 2; i < 5; i++ {
		if !a.GateSend(traceID, chunks[i], false, false, r.ch, ctx) {
			t.Fatalf("chunk %d: send returned false", i)
		}
	}
	time.Sleep(20 * time.Millisecond)
	if pre, _ := r.snapshot(); len(pre) != 2 {
		t.Fatalf("expected 2 delivered (only pre-pause), got %d", len(pre))
	}

	// One Resume must be enough — no need to call it once per Pause.
	a.ResumeStream(traceID)
	if !r.waitFor(5, time.Second) {
		t.Fatalf("expected 5 chunks after resume")
	}
	cs, _ := r.snapshot()
	for i := 0; i < 5; i++ {
		if cs[i] != chunks[i] {
			t.Fatalf("order mismatch at %d", i)
		}
	}
}

// TestGate_EndStreamNilWhilePaused: clean termination — flush, no synthetic error.
func TestGate_EndStreamNilWhilePaused(t *testing.T) {
	a := newTestAccumulator(t)
	traceID := "trace-endnil"
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	r := newRecorder(32)
	defer r.close()
	chunks := makeChunks(4)

	if !a.GateSend(traceID, chunks[0], false, false, r.ch, ctx) {
		t.Fatalf("setup chunk 0: GateSend returned false")
	}
	a.PauseStream(traceID)
	for i := 1; i < 4; i++ {
		if !a.GateSend(traceID, chunks[i], false, false, r.ch, ctx) {
			t.Fatalf("setup chunk %d (paused): GateSend returned false", i)
		}
	}
	a.EndStream(traceID, nil)

	sa := mustGet(t, a, traceID)
	sa.WaitForFlusher()
	if !r.waitFor(4, time.Second) {
		t.Fatalf("expected 4 chunks")
	}
	if sa.gateState != StreamStateEnded {
		t.Fatalf("expected Ended, got %v", sa.gateState)
	}
}

// TestGate_EndStreamErrorWhilePaused: flush + synthetic error chunk.
func TestGate_EndStreamErrorWhilePaused(t *testing.T) {
	a := newTestAccumulator(t)
	traceID := "trace-enderr"
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	r := newRecorder(32)
	defer r.close()
	chunks := makeChunks(3)

	if !a.GateSend(traceID, chunks[0], false, false, r.ch, ctx) {
		t.Fatalf("setup chunk 0: GateSend returned false")
	}
	a.PauseStream(traceID)
	for i := 1; i < 3; i++ {
		if !a.GateSend(traceID, chunks[i], false, false, r.ch, ctx) {
			t.Fatalf("setup chunk %d (paused): GateSend returned false", i)
		}
	}
	terminal := &schemas.BifrostError{IsBifrostError: true, Error: &schemas.ErrorField{Message: "policy"}}
	a.EndStream(traceID, terminal)

	sa := mustGet(t, a, traceID)
	sa.WaitForFlusher()
	if !r.waitFor(4, time.Second) {
		t.Fatalf("expected 4 chunks (3 buffered + synthetic err)")
	}
	cs, _ := r.snapshot()
	last := cs[3]
	if last == nil || last.BifrostError != terminal {
		t.Fatalf("expected terminal error chunk last")
	}
}

// TestGate_CtxCancelWhilePaused: cancellation tears down the flusher cleanly,
// gate transitions to Ended, no goroutine leak.
func TestGate_CtxCancelWhilePaused(t *testing.T) {
	a := newTestAccumulator(t)
	traceID := "trace-cancel"
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	// Use a 1-buffer channel and stop consuming after the first chunk so the
	// flusher's drain blocks; with ctx cancelled, sendOrCancel must observe
	// ctx.Done() (not the random-select ch<-) and finalize the gate.
	ch := make(chan *schemas.BifrostStreamChunk, 1)
	chunks := makeChunks(3)

	a.GateSend(traceID, chunks[0], false, false, ch, ctx)
	<-ch // consume so the channel is drained — no further consumer.

	a.PauseStream(traceID)
	a.GateSend(traceID, chunks[1], false, false, ch, ctx) // buffered
	a.GateSend(traceID, chunks[2], false, false, ch, ctx) // buffered

	cancel()
	a.ResumeStream(traceID)

	sa := mustGet(t, a, traceID)
	sa.WaitForFlusher()
	sa.mu.Lock()
	on := sa.gateFlusherOn
	state := sa.gateState
	sa.mu.Unlock()
	if on {
		t.Fatalf("flusher still running after ctx cancel")
	}
	if state != StreamStateEnded {
		t.Fatalf("expected Ended, got %v", state)
	}
}

// TestGate_AsyncResume: Resume from a separate goroutine is honored.
func TestGate_AsyncResume(t *testing.T) {
	a := newTestAccumulator(t)
	traceID := "trace-async"
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	r := newRecorder(32)
	defer r.close()
	chunks := makeChunks(5)

	a.GateSend(traceID, chunks[0], false, false, r.ch, ctx)
	a.PauseStream(traceID)
	a.GateSend(traceID, chunks[1], false, false, r.ch, ctx)
	a.GateSend(traceID, chunks[2], false, false, r.ch, ctx)

	go func() {
		time.Sleep(40 * time.Millisecond)
		a.ResumeStream(traceID)
	}()

	a.GateSend(traceID, chunks[3], false, false, r.ch, ctx)
	a.GateSend(traceID, chunks[4], true, false, r.ch, ctx)

	sa := mustGet(t, a, traceID)
	sa.WaitForFlusher()
	if !r.waitFor(5, time.Second) {
		t.Fatalf("expected 5 chunks")
	}
	cs, _ := r.snapshot()
	for i := 0; i < 5; i++ {
		if cs[i] != chunks[i] {
			t.Fatalf("order mismatch at %d", i)
		}
	}
}

// TestGate_FallbackIsolation: a fresh traceID gets a fresh gate state.
func TestGate_FallbackIsolation(t *testing.T) {
	a := newTestAccumulator(t)
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})

	traceA := "trace-A"
	chA := make(chan *schemas.BifrostStreamChunk, 4)
	a.GateSend(traceA, makeChunks(1)[0], false, false, chA, ctx)
	a.PauseStream(traceA)
	a.CreateStreamAccumulator(traceA, time.Now())
	_ = a.CleanupStreamAccumulator(traceA)
	_ = a.CleanupStreamAccumulator(traceA)

	traceB := "trace-B"
	saB := a.getOrCreateStreamAccumulator(traceB)
	saB.mu.Lock()
	state := saB.gateState
	pausedAt := saB.gatePausedAt
	saB.mu.Unlock()
	if state != StreamStateActive {
		t.Fatalf("fallback: expected Active, got %v", state)
	}
	if pausedAt != -1 {
		t.Fatalf("fallback: expected pausedAt=-1, got %d", pausedAt)
	}
}

// TestGate_Idempotency: double Pause/Resume/End and Pause-after-End are no-ops.
func TestGate_Idempotency(t *testing.T) {
	a := newTestAccumulator(t)
	traceID := "trace-idem"

	a.PauseStream(traceID)
	a.PauseStream(traceID)
	sa := mustGet(t, a, traceID)
	if sa.gateState != StreamStatePaused {
		t.Fatalf("after double Pause: %v", sa.gateState)
	}

	a.ResumeStream(traceID)
	a.ResumeStream(traceID)
	if sa.gateState != StreamStateActive {
		t.Fatalf("after double Resume: %v", sa.gateState)
	}

	a.EndStream(traceID, nil)
	a.EndStream(traceID, nil)
	if sa.gateState != StreamStateEnded {
		t.Fatalf("after double End: %v", sa.gateState)
	}

	a.PauseStream(traceID)
	if sa.gateState != StreamStateEnded {
		t.Fatalf("Pause after End should be a no-op, got %v", sa.gateState)
	}
}

// TestGate_CleanupDefersWhilePaused: refcount-driven cleanup must NOT tear
// down or force-end a gate that's currently paused. Teardown runs after the
// flusher exits naturally (e.g. via Resume + drain). This is the contract
// PauseStream relies on so a logging plugin's final-chunk cleanup doesn't
// race ahead of pause-buffered chunks.
func TestGate_CleanupDefersWhilePaused(t *testing.T) {
	a := newTestAccumulator(t)
	traceID := "trace-cleanup-defers"
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	r := newRecorder(32)
	defer r.close()

	chunks := makeChunks(3)
	a.GateSend(traceID, chunks[0], false, false, r.ch, ctx)
	a.PauseStream(traceID)
	a.GateSend(traceID, chunks[1], false, false, r.ch, ctx) // buffered
	a.GateSend(traceID, chunks[2], true, false, r.ch, ctx)  // buffered (terminal)

	sa := mustGet(t, a, traceID)

	// Refcount cleanup while paused: should be deferred, not torn down.
	a.CreateStreamAccumulator(traceID, time.Now())
	if err := a.CleanupStreamAccumulator(traceID); err != nil {
		t.Fatalf("Cleanup err: %v", err)
	}
	if err := a.CleanupStreamAccumulator(traceID); err != nil {
		t.Fatalf("Cleanup err 2: %v", err)
	}

	sa.mu.Lock()
	pendingAfterCleanup := sa.gatePendingCleanup
	bufLenWhilePaused := len(sa.gateReplayBuf)
	stateWhilePaused := sa.gateState
	sa.mu.Unlock()
	if !pendingAfterCleanup {
		t.Fatalf("expected gatePendingCleanup=true after refcount cleanup while paused")
	}
	if bufLenWhilePaused != 2 {
		t.Fatalf("expected buffer to retain 2 chunks while paused, got %d", bufLenWhilePaused)
	}
	if stateWhilePaused != StreamStatePaused {
		t.Fatalf("expected state still Paused after deferred cleanup, got %v", stateWhilePaused)
	}
	if _, ok := a.streamAccumulators.Load(traceID); !ok {
		t.Fatalf("accumulator must still be present in map (cleanup deferred)")
	}

	// Resume drains the buffer (including the terminal chunk → state=Ended).
	// The flusher's exit defer runs the deferred cleanup before close(done),
	// so WaitForFlusher returning means cleanup has also completed.
	a.ResumeStream(traceID)
	sa.WaitForFlusher()
	if !r.waitFor(3, time.Second) {
		t.Fatalf("expected 3 chunks delivered after resume")
	}
	if _, ok := a.streamAccumulators.Load(traceID); ok {
		t.Fatalf("accumulator must be cleaned up after flusher exit")
	}
}

// TestGate_CleanupForceEndsOrphan: TTL-driven cleanup (forceEndGate=true)
// MUST still tear down a paused gate — orphans have no live consumer that
// could resume them.
func TestGate_CleanupForceEndsOrphan(t *testing.T) {
	a := newTestAccumulator(t)
	traceID := "trace-cleanup-orphan"
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	r := newRecorder(32)
	defer r.close()

	a.GateSend(traceID, makeChunks(1)[0], false, false, r.ch, ctx)
	a.PauseStream(traceID)
	a.GateSend(traceID, makeChunks(1)[0], false, false, r.ch, ctx) // buffered

	sa := mustGet(t, a, traceID)

	// Simulate orphan reap (TTL path).
	sa.mu.Lock()
	a.cleanupStreamAccumulator(traceID, true)
	sa.mu.Unlock()

	sa.WaitForFlusher()
	sa.mu.Lock()
	bufLen := len(sa.gateReplayBuf)
	state := sa.gateState
	sa.mu.Unlock()
	if bufLen != 0 {
		t.Fatalf("orphan reap must clear buffer, got len=%d", bufLen)
	}
	if state != StreamStateEnded {
		t.Fatalf("orphan reap must force-end the gate, got %v", state)
	}
	if _, ok := a.streamAccumulators.Load(traceID); ok {
		t.Fatalf("orphan reap must remove the accumulator from the map")
	}
}

// TestGate_CleanupWhilePaused: refcount cleanup while paused defers teardown
// until the flusher exits. After Resume drains the buffer, the deferred
// cleanup runs and the accumulator is removed from the map.
func TestGate_CleanupWhilePaused(t *testing.T) {
	a := newTestAccumulator(t)
	traceID := "trace-cleanup"
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	r := newRecorder(32)
	defer r.close()
	chunks := makeChunks(2)

	a.GateSend(traceID, chunks[0], false, false, r.ch, ctx)
	a.PauseStream(traceID)
	a.GateSend(traceID, chunks[1], false, false, r.ch, ctx) // buffered

	// Capture the per-stream accumulator BEFORE cleanup so we can wait on its
	// flusher even after the map entry is removed.
	sa := mustGet(t, a, traceID)

	a.CreateStreamAccumulator(traceID, time.Now())
	if err := a.CleanupStreamAccumulator(traceID); err != nil {
		t.Fatalf("Cleanup err: %v", err)
	}
	if err := a.CleanupStreamAccumulator(traceID); err != nil {
		t.Fatalf("Cleanup err 2: %v", err)
	}

	// Deferred: the entry must STILL be present while paused.
	if _, ok := a.streamAccumulators.Load(traceID); !ok {
		t.Fatalf("accumulator must still be present while paused (cleanup deferred)")
	}

	// EndStream(nil) terminates the gate explicitly: state→Ended, flusher
	// drains the remaining chunk, exit defer runs the deferred cleanup
	// (before close(done), so WaitForFlusher returning implies it's done).
	a.EndStream(traceID, nil)
	sa.WaitForFlusher()
	if _, ok := a.streamAccumulators.Load(traceID); ok {
		t.Fatalf("accumulator must be cleaned up after flusher exit")
	}
}

// TestGate_StateTransitions covers the explicit transition paths.
func TestGate_StateTransitions(t *testing.T) {
	a := newTestAccumulator(t)

	a.PauseStream("t1")
	if mustGet(t, a, "t1").gateState != StreamStatePaused {
		t.Fatalf("t1: want Paused")
	}
	a.ResumeStream("t1")
	if mustGet(t, a, "t1").gateState != StreamStateActive {
		t.Fatalf("t1: want Active")
	}

	a.PauseStream("t2")
	a.EndStream("t2", nil)
	if mustGet(t, a, "t2").gateState != StreamStateEnded {
		t.Fatalf("t2: want Ended")
	}

	a.EndStream("t3", nil)
	if mustGet(t, a, "t3").gateState != StreamStateEnded {
		t.Fatalf("t3: want Ended")
	}
}

// mustGet pulls the per-stream accumulator entry by traceID for assertions.
func mustGet(t *testing.T, a *Accumulator, traceID string) *StreamAccumulator {
	t.Helper()
	v, ok := a.streamAccumulators.Load(traceID)
	if !ok {
		t.Fatalf("no accumulator entry for %s", traceID)
	}
	return v.(*StreamAccumulator)
}

// ─── error cases ────────────────────────────────────────────────────────────

// TestGate_HardErrorWhileActive: hard provider error during normal streaming
// (no prior pause) is delivered, transitions the gate to Ended, and any
// subsequent chunks are dropped.
func TestGate_HardErrorWhileActive(t *testing.T) {
	a := newTestAccumulator(t)
	traceID := "trace-hard-active"
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	r := newRecorder(32)
	defer r.close()
	chunks := makeChunks(2)

	a.GateSend(traceID, chunks[0], false, false, r.ch, ctx)
	hardErr := &schemas.BifrostStreamChunk{BifrostError: &schemas.BifrostError{IsBifrostError: true, Error: &schemas.ErrorField{Message: "rate limited"}}}
	if !a.GateSend(traceID, hardErr, false, true, r.ch, ctx) {
		t.Fatalf("hard error send returned false")
	}
	if !r.waitFor(2, time.Second) {
		t.Fatalf("expected 2 chunks delivered")
	}
	sa := mustGet(t, a, traceID)
	if sa.gateState != StreamStateEnded {
		t.Fatalf("expected Ended after hard error, got %v", sa.gateState)
	}
	// Subsequent chunks should drop.
	if a.GateSend(traceID, chunks[1], false, false, r.ch, ctx) {
		t.Fatalf("post-end chunk should drop, got true")
	}
	cs, _ := r.snapshot()
	if len(cs) != 2 {
		t.Fatalf("post-end chunk leaked through, got %d total", len(cs))
	}
}

// TestGate_SoftErrorPassesThrough: a chunk with a non-IsBifrostError BifrostError
// is treated as data (not as a stream-fatal signal). Gate stays Active.
func TestGate_SoftErrorPassesThrough(t *testing.T) {
	a := newTestAccumulator(t)
	traceID := "trace-soft-err"
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	r := newRecorder(32)
	defer r.close()

	soft := &schemas.BifrostStreamChunk{BifrostError: &schemas.BifrostError{IsBifrostError: false, Error: &schemas.ErrorField{Message: "transient"}}}
	if !a.GateSend(traceID, soft, false, false, r.ch, ctx) {
		t.Fatalf("soft error send returned false")
	}
	follow := makeChunks(1)[0]
	if !a.GateSend(traceID, follow, false, false, r.ch, ctx) {
		t.Fatalf("post-soft-error send returned false")
	}
	if !r.waitFor(2, time.Second) {
		t.Fatalf("expected 2 chunks delivered")
	}
	sa := mustGet(t, a, traceID)
	if sa.gateState != StreamStateActive {
		t.Fatalf("expected Active after soft error, got %v", sa.gateState)
	}
}

// TestGate_GateSendAfterEnd: every chunk after EndStream is dropped (returns false).
func TestGate_GateSendAfterEnd(t *testing.T) {
	a := newTestAccumulator(t)
	traceID := "trace-after-end"
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	r := newRecorder(32)
	defer r.close()

	a.EndStream(traceID, nil)
	chunks := makeChunks(3)
	for i := range chunks {
		if a.GateSend(traceID, chunks[i], false, false, r.ch, ctx) {
			t.Fatalf("chunk %d after End should drop", i)
		}
	}
	// Even the final chunk must drop, since the stream is already over.
	if a.GateSend(traceID, &schemas.BifrostStreamChunk{}, true, false, r.ch, ctx) {
		t.Fatalf("final chunk after End should drop")
	}
	if got, _ := r.snapshot(); len(got) != 0 {
		t.Fatalf("expected zero chunks delivered after End, got %d", len(got))
	}
}

// TestGate_EndStreamErrorWhileActive: proactive termination from a plugin
// during normal streaming (no prior pause) ends the gate and a flusher is
// spawned on demand to deliver the synthetic terminal error chunk to the
// client. Regression test for greptile P1: previously End(err) without an
// active flusher silently dropped the error.
func TestGate_EndStreamErrorWhileActive(t *testing.T) {
	a := newTestAccumulator(t)
	traceID := "trace-end-active"
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	r := newRecorder(32)
	defer r.close()

	first := makeChunks(1)[0]
	a.GateSend(traceID, first, false, false, r.ch, ctx)

	terminal := &schemas.BifrostError{IsBifrostError: true, Error: &schemas.ErrorField{Message: "policy-violation"}}
	a.EndStream(traceID, terminal)

	sa := mustGet(t, a, traceID)
	if sa.gateState != StreamStateEnded {
		t.Fatalf("expected Ended, got %v", sa.gateState)
	}
	// Wait for the on-demand flusher to deliver the terminal error.
	sa.WaitForFlusher()

	if !r.waitFor(2, time.Second) {
		t.Fatalf("expected first chunk + terminal err delivered")
	}
	cs, _ := r.snapshot()
	if cs[0] != first {
		t.Fatalf("first delivered chunk is wrong")
	}
	if cs[1] == nil || cs[1].BifrostError != terminal {
		t.Fatalf("expected terminal err chunk last, got %+v", cs[1])
	}
	if a.GateSend(traceID, makeChunks(1)[0], false, false, r.ch, ctx) {
		t.Fatalf("post-End chunk should drop")
	}
}

// TestGate_EndStreamErrorBeforeAnyChunk: EndStream(err) called before any
// GateSend has cached a delivery target — there is no consumer to deliver to,
// so the error is dropped without spawning a flusher. Gate must still End.
func TestGate_EndStreamErrorBeforeAnyChunk(t *testing.T) {
	a := newTestAccumulator(t)
	traceID := "trace-end-before-send"
	terminal := &schemas.BifrostError{IsBifrostError: true, Error: &schemas.ErrorField{Message: "early"}}

	a.EndStream(traceID, terminal)

	sa := mustGet(t, a, traceID)
	sa.mu.Lock()
	state := sa.gateState
	on := sa.gateFlusherOn
	sa.mu.Unlock()
	if state != StreamStateEnded {
		t.Fatalf("expected Ended, got %v", state)
	}
	if on {
		t.Fatalf("no flusher should be spawned without a cached channel")
	}
}

// TestGate_MultipleEndStreamCalls: only the first non-nil err sticks; further
// EndStream calls are no-ops that don't override or panic.
func TestGate_MultipleEndStreamCalls(t *testing.T) {
	a := newTestAccumulator(t)
	traceID := "trace-multi-end"

	first := &schemas.BifrostError{IsBifrostError: true, Error: &schemas.ErrorField{Message: "first"}}
	second := &schemas.BifrostError{IsBifrostError: true, Error: &schemas.ErrorField{Message: "second"}}

	a.EndStream(traceID, first)
	a.EndStream(traceID, second) // no-op
	a.EndStream(traceID, nil)    // no-op

	sa := mustGet(t, a, traceID)
	if sa.gateState != StreamStateEnded {
		t.Fatalf("expected Ended, got %v", sa.gateState)
	}
	if sa.gateEndError != nil && sa.gateEndError.Error.Message != "first" {
		t.Fatalf("expected first err to stick, got %q", sa.gateEndError.Error.Message)
	}
}

// ─── timeout cases ──────────────────────────────────────────────────────────

// TestGate_CtxDeadlineExpiresDuringActive: when ctx deadline expires, bare-send
// path returns false immediately (sendOrCancel observes ctx.Done()).
func TestGate_CtxDeadlineExpiresDuringActive(t *testing.T) {
	a := newTestAccumulator(t)
	traceID := "trace-deadline-active"
	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(40*time.Millisecond))
	defer ctx.Cancel()
	// Unbuffered: the send blocks until ctx fires.
	ch := make(chan *schemas.BifrostStreamChunk)

	start := time.Now()
	if a.GateSend(traceID, makeChunks(1)[0], false, false, ch, ctx) {
		t.Fatalf("send should have returned false on deadline expiry")
	}
	elapsed := time.Since(start)
	if elapsed < 30*time.Millisecond || elapsed > 500*time.Millisecond {
		t.Fatalf("expected send to unblock around the deadline, took %s", elapsed)
	}
}

// TestGate_CtxDeadlineExpiresDuringPause: a deadline that fires while the
// gate is paused causes the flusher (woken on Resume) to fail its drain via
// ctx.Done() and transition the gate to Ended.
func TestGate_CtxDeadlineExpiresDuringPause(t *testing.T) {
	a := newTestAccumulator(t)
	traceID := "trace-deadline-pause"
	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(60*time.Millisecond))
	defer ctx.Cancel()
	ch := make(chan *schemas.BifrostStreamChunk) // unbuffered: drain blocks
	chunks := makeChunks(3)

	// Active send needs a consumer present; spawn a one-shot receiver.
	go func() { <-ch }()
	a.GateSend(traceID, chunks[0], false, false, ch, ctx)

	a.PauseStream(traceID)
	a.GateSend(traceID, chunks[1], false, false, ch, ctx) // buffered
	a.GateSend(traceID, chunks[2], false, false, ch, ctx) // buffered

	// Wait for ctx to fire, then resume — flusher will try to drain to an
	// unbuffered channel with no consumer and observe ctx.Done().
	time.Sleep(80 * time.Millisecond)
	a.ResumeStream(traceID)

	sa := mustGet(t, a, traceID)
	sa.WaitForFlusher()
	sa.mu.Lock()
	state := sa.gateState
	bufLen := len(sa.gateReplayBuf)
	sa.mu.Unlock()
	if state != StreamStateEnded {
		t.Fatalf("expected Ended after deadline-driven drain abort, got %v", state)
	}
	if bufLen != 0 {
		t.Fatalf("expected empty buf after abort, got %d", bufLen)
	}
}

// ─── client-disconnect cases ────────────────────────────────────────────────

// TestGate_ChannelClosedDuringActive: when the consumer closes the response
// channel mid-stream, sendOrCancel recovers from the panic and returns false
// without crashing the producer.
func TestGate_ChannelClosedDuringActive(t *testing.T) {
	a := newTestAccumulator(t)
	traceID := "trace-ch-close-active"
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	ch := make(chan *schemas.BifrostStreamChunk, 1)

	// Drain the first chunk normally, then close the channel.
	chunks := makeChunks(3)
	if !a.GateSend(traceID, chunks[0], false, false, ch, ctx) {
		t.Fatalf("first send should have succeeded")
	}
	<-ch
	close(ch)

	// Subsequent sends must not panic; they should return false.
	if a.GateSend(traceID, chunks[1], false, false, ch, ctx) {
		t.Fatalf("send after channel close should return false")
	}
	if a.GateSend(traceID, chunks[2], false, false, ch, ctx) {
		t.Fatalf("repeated send after channel close should return false")
	}
}

// TestGate_ChannelClosedDuringPaused: the consumer disconnects while the gate
// is paused; on resume the flusher's drain hits the closed channel, recovers,
// and finalizes the gate to Ended without panicking the goroutine.
func TestGate_ChannelClosedDuringPaused(t *testing.T) {
	a := newTestAccumulator(t)
	traceID := "trace-ch-close-paused"
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	ch := make(chan *schemas.BifrostStreamChunk, 1)
	chunks := makeChunks(3)

	if !a.GateSend(traceID, chunks[0], false, false, ch, ctx) {
		t.Fatalf("first send should have succeeded")
	}
	<-ch

	a.PauseStream(traceID)
	a.GateSend(traceID, chunks[1], false, false, ch, ctx) // buffered
	a.GateSend(traceID, chunks[2], false, false, ch, ctx) // buffered

	// Consumer is gone — close the channel to simulate disconnect.
	close(ch)

	a.ResumeStream(traceID)

	sa := mustGet(t, a, traceID)
	sa.WaitForFlusher()
	sa.mu.Lock()
	state := sa.gateState
	on := sa.gateFlusherOn
	bufLen := len(sa.gateReplayBuf)
	sa.mu.Unlock()
	if on {
		t.Fatalf("flusher still running after disconnect")
	}
	if state != StreamStateEnded {
		t.Fatalf("expected Ended after disconnect drain, got %v", state)
	}
	if bufLen != 0 {
		t.Fatalf("expected empty buf, got %d", bufLen)
	}
}

// TestGate_ProducerContinuesAfterDisconnect: after the first send fails due to
// disconnect, the gate stays Ended and every following send is dropped without
// panic, even for final/hard-error chunks.
func TestGate_ProducerContinuesAfterDisconnect(t *testing.T) {
	a := newTestAccumulator(t)
	traceID := "trace-after-disconnect"
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	// Unbuffered channel with no consumer — every send blocks until ctx fires.
	ch := make(chan *schemas.BifrostStreamChunk)

	cancel() // disconnect right away
	chunks := makeChunks(5)
	for i := range chunks {
		if a.GateSend(traceID, chunks[i], false, false, ch, ctx) {
			t.Fatalf("chunk %d should drop after disconnect, got true", i)
		}
	}
	// Final + hard-error after disconnect should also drop, not panic.
	if a.GateSend(traceID, &schemas.BifrostStreamChunk{}, true, false, ch, ctx) {
		t.Fatalf("final chunk after disconnect should drop")
	}
	hardErr := &schemas.BifrostStreamChunk{BifrostError: &schemas.BifrostError{IsBifrostError: true, Error: &schemas.ErrorField{Message: "x"}}}
	if a.GateSend(traceID, hardErr, false, true, ch, ctx) {
		t.Fatalf("hard error after disconnect should drop")
	}
}

// TestGate_IsStreamEnded covers the Ended-state read across all four
// transitions: never-existed, Active, Paused, Ended.
func TestGate_IsStreamEnded(t *testing.T) {
	a := newTestAccumulator(t)
	traceID := "trace-is-ended"

	// 1. No accumulator → false.
	if a.IsStreamEnded(traceID) {
		t.Fatalf("missing accumulator must return false")
	}

	// 2. Active → false.
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	r := newRecorder(8)
	defer r.close()
	a.GateSend(traceID, makeChunks(1)[0], false, false, r.ch, ctx)
	if a.IsStreamEnded(traceID) {
		t.Fatalf("Active gate must return false")
	}

	// 3. Paused → false (paused is not ended).
	a.PauseStream(traceID)
	if a.IsStreamEnded(traceID) {
		t.Fatalf("Paused gate must return false for IsStreamEnded")
	}

	// 4. Ended → true.
	a.EndStream(traceID, nil)
	if !a.IsStreamEnded(traceID) {
		t.Fatalf("Ended gate must return true")
	}

	sa := mustGet(t, a, traceID)
	sa.WaitForFlusher()
}

// TestGate_IsStreamPaused covers the Paused-state read across all four
// transitions: never-existed, Active, Paused, Ended.
func TestGate_IsStreamPaused(t *testing.T) {
	a := newTestAccumulator(t)
	traceID := "trace-is-paused"

	// 1. No accumulator → false.
	if a.IsStreamPaused(traceID) {
		t.Fatalf("missing accumulator must return false")
	}

	// 2. Active → false.
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	r := newRecorder(8)
	defer r.close()
	a.GateSend(traceID, makeChunks(1)[0], false, false, r.ch, ctx)
	if a.IsStreamPaused(traceID) {
		t.Fatalf("Active gate must return false for IsStreamPaused")
	}

	// 3. Paused → true.
	a.PauseStream(traceID)
	if !a.IsStreamPaused(traceID) {
		t.Fatalf("Paused gate must return true")
	}

	// 4. Ended → false (ended is not paused).
	a.EndStream(traceID, nil)
	if a.IsStreamPaused(traceID) {
		t.Fatalf("Ended gate must return false for IsStreamPaused")
	}

	sa := mustGet(t, a, traceID)
	sa.WaitForFlusher()
}

// TestGate_QueriesDontCreateAccumulator: IsStreamEnded / IsStreamPaused on an
// unknown traceID must NOT spawn an accumulator as a side effect (they're
// read-only by contract).
func TestGate_QueriesDontCreateAccumulator(t *testing.T) {
	a := newTestAccumulator(t)
	traceID := "trace-never-seen"

	_ = a.IsStreamEnded(traceID)
	_ = a.IsStreamPaused(traceID)

	if _, ok := a.streamAccumulators.Load(traceID); ok {
		t.Fatalf("query must not create accumulator for unknown traceID")
	}
}

// TestGate_QueriesDontEngageGate: ctx-level queries must NOT flip the
// BifrostContextKeyStreamGated flag — that would force every subsequent
// provider chunk through tracer.GateSend even for a stream that never
// actually paused/resumed/ended.
func TestGate_QueriesDontEngageGate(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	// Wire a tracer + traceID so the ctx methods don't trivially return false
	// before reaching the engage point.
	a := newTestAccumulator(t)
	ctx.SetValue(schemas.BifrostContextKeyTracer, &gateQueryTracer{a: a})
	ctx.SetValue(schemas.BifrostContextKeyTraceID, "trace-no-engage")

	_ = ctx.IsStreamEnded()
	_ = ctx.IsStreamPaused()

	if gated, _ := ctx.Value(schemas.BifrostContextKeyStreamGated).(bool); gated {
		t.Fatalf("query methods must not engage the gate (StreamGated should remain unset)")
	}
}

// gateQueryTracer is a thin schemas.Tracer adapter for query-only tests:
// it wraps a real Accumulator so the IsStream* paths can exercise the full
// chain without pulling in framework/tracing.Tracer.
type gateQueryTracer struct {
	schemas.NoOpTracer
	a *Accumulator
}

func (t *gateQueryTracer) IsStreamEnded(traceID string) bool  { return t.a.IsStreamEnded(traceID) }
func (t *gateQueryTracer) IsStreamPaused(traceID string) bool { return t.a.IsStreamPaused(traceID) }
func (t *gateQueryTracer) GetAccumulatedResponse(traceID string) *schemas.BifrostResponse {
	return t.a.GetAccumulatedResponse(traceID)
}

// TestGate_GetAccumulatedResponse_NoAccumulator: missing traceID → nil.
func TestGate_GetAccumulatedResponse_NoAccumulator(t *testing.T) {
	a := newTestAccumulator(t)
	if got := a.GetAccumulatedResponse("never-seen"); got != nil {
		t.Fatalf("missing accumulator must return nil, got %+v", got)
	}
	// And it must NOT have created an accumulator as a side effect.
	if _, ok := a.streamAccumulators.Load("never-seen"); ok {
		t.Fatalf("query must not create accumulator")
	}
}

// TestGate_GetAccumulatedResponse_NoChunks: accumulator exists (e.g. from a
// PauseStream call before the first chunk arrived) but has no chat chunks
// yet — should return nil rather than an empty BifrostResponse.
func TestGate_GetAccumulatedResponse_NoChunks(t *testing.T) {
	a := newTestAccumulator(t)
	traceID := "trace-no-chunks"

	// Force-create the accumulator without adding any chunks.
	a.PauseStream(traceID)

	if got := a.GetAccumulatedResponse(traceID); got != nil {
		t.Fatalf("empty accumulator must return nil, got %+v", got)
	}
}

// TestGate_GetAccumulatedResponse_ChatSnapshot: add a few chat delta chunks,
// query mid-stream, and verify the assembled response carries the expected
// concatenated content.
func TestGate_GetAccumulatedResponse_ChatSnapshot(t *testing.T) {
	a := newTestAccumulator(t)
	traceID := "trace-snapshot"

	deltas := []string{"Hello, ", "world", "!"}
	for i, d := range deltas {
		text := d
		chunk := &ChatStreamChunk{
			ChunkIndex: i,
			Timestamp:  time.Now(),
			Delta: &schemas.ChatStreamResponseChoiceDelta{
				Content: &text,
			},
		}
		if err := a.addChatStreamChunk(traceID, chunk, false); err != nil {
			t.Fatalf("add chunk %d: %v", i, err)
		}
	}

	resp := a.GetAccumulatedResponse(traceID)
	if resp == nil {
		t.Fatalf("expected non-nil snapshot after 3 chunks")
	}
	if resp.ChatResponse == nil {
		t.Fatalf("expected ChatResponse populated")
	}
	if len(resp.ChatResponse.Choices) == 0 {
		t.Fatalf("expected at least one choice")
	}
	choice := resp.ChatResponse.Choices[0]
	if choice.ChatNonStreamResponseChoice == nil || choice.ChatNonStreamResponseChoice.Message == nil {
		t.Fatalf("expected assembled message in non-stream choice")
	}
	msg := choice.ChatNonStreamResponseChoice.Message
	if msg.Content == nil || msg.Content.ContentStr == nil {
		t.Fatalf("expected ContentStr to be populated")
	}
	if got := *msg.Content.ContentStr; got != "Hello, world!" {
		t.Fatalf("expected concatenated 'Hello, world!', got %q", got)
	}
}

// TestGate_GetAccumulatedResponse_DoesNotEngageGate: querying via ctx must
// not flip BifrostContextKeyStreamGated. Same contract as IsStreamEnded.
func TestGate_GetAccumulatedResponse_DoesNotEngageGate(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	a := newTestAccumulator(t)
	ctx.SetValue(schemas.BifrostContextKeyTracer, &gateQueryTracer{a: a})
	ctx.SetValue(schemas.BifrostContextKeyTraceID, "trace-snapshot-no-engage")

	_ = ctx.GetAccumulatedResponse()

	if gated, _ := ctx.Value(schemas.BifrostContextKeyStreamGated).(bool); gated {
		t.Fatalf("GetAccumulatedResponse must not engage the gate (StreamGated should remain unset)")
	}
}
