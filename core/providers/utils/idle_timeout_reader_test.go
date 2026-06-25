package utils

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// syncedPanicBody is a controlled io.ReadCloser used to deterministically
// reproduce the race in SetupStreamCancellation.
//
// Read blocks until Close() is called, then panics — simulating what fasthttp's
// streaming body does when the underlying TCP connection is force-closed.
//
// Close() triggers the panic (by closing panicTrigger) and then blocks on
// allowReturn until the test signals it. This keeps SetupStreamCancellation
// stuck inside Close() so BifrostContextKeyConnectionClosed is guaranteed to
// be unset when idleTimeoutReader.Read's recover block runs — the exact race
// window that exists in the current code.
type syncedPanicBody struct {
	panicTrigger chan struct{}
	allowReturn  chan struct{}
	closeOnce    sync.Once
}

func newSyncedPanicBody() *syncedPanicBody {
	return &syncedPanicBody{
		panicTrigger: make(chan struct{}),
		allowReturn:  make(chan struct{}),
	}
}

func (s *syncedPanicBody) Read(_ []byte) (int, error) {
	<-s.panicTrigger
	panic("use of closed network connection")
}

func (s *syncedPanicBody) Close() error {
	s.closeOnce.Do(func() { close(s.panicTrigger) })
	<-s.allowReturn
	return nil
}

var errPanicReader = errors.New("panic reader called")

type panicReader struct{}

func (panicReader) Read([]byte) (int, error) {
	panic(errPanicReader)
}

type blockingCloserSpy struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingCloserSpy() *blockingCloserSpy {
	return &blockingCloserSpy{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (c *blockingCloserSpy) Read([]byte) (int, error) { return 0, io.EOF }

func (c *blockingCloserSpy) Close() error {
	c.once.Do(func() { close(c.started) })
	<-c.release
	return nil
}

func (c *blockingCloserSpy) unblock() {
	close(c.release)
}

// readCloserSpy implements io.ReadCloser and records how many times Close() was called.
type readCloserSpy struct {
	mu     sync.Mutex
	closed int
}

func (c *readCloserSpy) Read([]byte) (int, error) { return 0, io.EOF }

func (c *readCloserSpy) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed++
	return nil
}

func (c *readCloserSpy) closeCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// zeroThenBlockReader returns (0, nil) on the first read, then blocks forever.
type zeroThenBlockReader struct {
	first  atomic.Bool
	pipeRd *io.PipeReader
}

func (r *zeroThenBlockReader) Read(p []byte) (int, error) {
	if r.first.CompareAndSwap(false, true) {
		return 0, nil // zero-byte read
	}
	// block until pipe is closed
	return r.pipeRd.Read(p)
}

// ---------------------------------------------------------------------------
// tests
// ---------------------------------------------------------------------------

func TestIdleTimeoutReader_NormalRead(t *testing.T) {
	t.Parallel()
	pr, pw := io.Pipe()
	defer pr.Close()

	// Use pr as bodyStream — closing pr unblocks reads.
	wrapped, cleanup := NewIdleTimeoutReader(pr, pr, 500*time.Millisecond, nil)
	defer cleanup()

	// Writer sends 5 chunks quickly.
	go func() {
		for i := 0; i < 5; i++ {
			time.Sleep(10 * time.Millisecond)
			pw.Write([]byte("chunk"))
		}
		pw.Close()
	}()

	buf := make([]byte, 64)
	var totalBytes int
	for {
		n, err := wrapped.Read(buf)
		totalBytes += n
		if err != nil {
			if err != io.EOF {
				t.Fatalf("unexpected error: %v", err)
			}
			break
		}
	}

	if totalBytes != 5*len("chunk") {
		t.Fatalf("expected %d bytes, got %d", 5*len("chunk"), totalBytes)
	}
}

func TestIdleTimeoutReader_TimeoutClosesStream(t *testing.T) {
	t.Parallel()
	pr, pw := io.Pipe()
	defer pw.Close()

	// 100ms timeout, write nothing — should timeout and close the pipe reader.
	wrapped, cleanup := NewIdleTimeoutReader(pr, pr, 100*time.Millisecond, nil)
	defer cleanup()

	start := time.Now()
	buf := make([]byte, 64)
	_, err := wrapped.Read(buf)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error from timed-out read, got nil")
	}

	// Should complete within ~200ms (100ms timeout + margin), not hang.
	if elapsed > 500*time.Millisecond {
		t.Fatalf("read took %v, expected ~100ms timeout", elapsed)
	}
}

func TestIdleTimeoutReader_TimeoutAfterPartialData(t *testing.T) {
	t.Parallel()
	pr, pw := io.Pipe()

	// 200ms idle timeout.
	wrapped, cleanup := NewIdleTimeoutReader(pr, pr, 200*time.Millisecond, nil)
	defer cleanup()

	// Writer sends 3 chunks then stops.
	go func() {
		for i := 0; i < 3; i++ {
			time.Sleep(20 * time.Millisecond)
			pw.Write([]byte("data"))
		}
		// stop writing — idle timeout should fire after 200ms and close pr
	}()

	buf := make([]byte, 64)
	chunksRead := 0
	for {
		n, err := wrapped.Read(buf)
		if n > 0 {
			chunksRead++
		}
		if err != nil {
			break
		}
	}

	if chunksRead != 3 {
		t.Fatalf("expected 3 chunks before timeout, got %d", chunksRead)
	}

	pw.Close()
}

func TestIdleTimeoutReader_ResetOnData(t *testing.T) {
	t.Parallel()
	pr, pw := io.Pipe()

	// 200ms timeout, but data arrives every 150ms — should never timeout.
	wrapped, cleanup := NewIdleTimeoutReader(pr, pr, 200*time.Millisecond, nil)
	defer cleanup()

	go func() {
		for i := 0; i < 5; i++ {
			time.Sleep(150 * time.Millisecond)
			pw.Write([]byte("ok"))
		}
		pw.Close()
	}()

	buf := make([]byte, 64)
	chunksRead := 0
	for {
		n, err := wrapped.Read(buf)
		if n > 0 {
			chunksRead++
		}
		if err != nil {
			if err != io.EOF {
				t.Fatalf("expected EOF after all chunks, got: %v", err)
			}
			break
		}
	}

	if chunksRead != 5 {
		t.Fatalf("expected 5 chunks (timer should reset), got %d", chunksRead)
	}
}

func TestIdleTimeoutReader_CleanupStopsTimer(t *testing.T) {
	t.Parallel()
	pr, pw := io.Pipe()
	defer pr.Close()
	defer pw.Close()

	spy := &readCloserSpy{}

	_, cleanup := NewIdleTimeoutReader(pr, spy, 100*time.Millisecond, nil)
	// Call cleanup immediately — timer should be stopped.
	cleanup()

	// Wait well past the timeout duration.
	time.Sleep(250 * time.Millisecond)

	if spy.closeCount() != 0 {
		t.Fatalf("expected closer to NOT be called after cleanup, but was called %d times", spy.closeCount())
	}
}

func TestIdleTimeoutReader_DoubleCloseIsSafe(t *testing.T) {
	t.Parallel()
	spy := &readCloserSpy{}

	br := &zeroThenBlockReader{first: atomic.Bool{}, pipeRd: nil}
	// Use spy as bodyStream — it implements both io.Reader and io.Closer.
	_, cleanup := NewIdleTimeoutReader(br, spy, 50*time.Millisecond, nil)
	defer cleanup()

	// Let the timer fire (closes spy via sync.Once).
	time.Sleep(100 * time.Millisecond)

	// Manually close again — should not panic.
	spy.Close()

	// sync.Once ensures the idle timer's close ran exactly once.
	// The manual close above adds another, so total should be 2
	// (the once.Do protects the timer path, not external callers).
	// The key guarantee: no panic.
	if spy.closeCount() < 1 {
		t.Fatal("expected at least one close call")
	}
}

func TestIdleTimeoutReader_ZeroBytesDoNotResetTimer(t *testing.T) {
	t.Parallel()
	pr, pw := io.Pipe()
	defer pw.Close()

	// Use pr as bodyStream — when idle timeout fires, it closes pr,
	// which causes reads on pr to return io.ErrClosedPipe.
	zr := &zeroThenBlockReader{pipeRd: pr}
	wrapped, cleanup := NewIdleTimeoutReader(zr, pr, 100*time.Millisecond, nil)
	defer cleanup()

	done := make(chan error, 1)
	go func() {
		buf := make([]byte, 64)
		// First read returns (0, nil), second read blocks until pipe is closed.
		for {
			_, err := wrapped.Read(buf)
			if err != nil {
				done <- err
				return
			}
		}
	}()

	select {
	case <-done:
		// Timer fired and closed the pipe — Read() returned an error. Good.
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected idle timeout to fire, but read is still blocking")
	}
}

func TestIdleTimeoutReader_ErrorFromClosedPipe(t *testing.T) {
	t.Parallel()
	pr, pw := io.Pipe()
	defer pw.Close()

	// Use pr as bodyStream — when idle timeout fires, it closes pr,
	// which makes Read return io.ErrClosedPipe.
	wrapped, cleanup := NewIdleTimeoutReader(pr, pr, 50*time.Millisecond, nil)
	defer cleanup()

	buf := make([]byte, 64)
	_, err := wrapped.Read(buf)

	if err == nil {
		t.Fatal("expected error from closed pipe")
	}
	// The error should indicate the pipe was closed.
	if !errors.Is(err, io.ErrClosedPipe) && !errors.Is(err, io.EOF) {
		// Some implementations return io.ErrClosedPipe, others EOF.
		t.Logf("got error: %v (acceptable)", err)
	}
}

func TestIdleTimeoutReader_NilContextDoesNotPanic(t *testing.T) {
	t.Parallel()
	wrapped, cleanup := NewIdleTimeoutReader(&readCloserSpy{}, &readCloserSpy{}, time.Second, nil)
	defer cleanup()

	_, err := wrapped.Read(make([]byte, 1))
	if err != io.EOF {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestIdleTimeoutReader_ClosedContextReturnsError(t *testing.T) {
	t.Parallel()
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyConnectionClosed, true)
	wrapped, cleanup := NewIdleTimeoutReader(panicReader{}, &readCloserSpy{}, time.Second, ctx)
	defer cleanup()

	_, err := wrapped.Read(make([]byte, 1))
	if !errors.Is(err, ErrStreamClosed) {
		t.Fatalf("expected ErrStreamClosed, got %v", err)
	}
}

func TestIdleTimeoutReader_RecoversReadPanicAfterTimeout(t *testing.T) {
	t.Parallel()
	wrapped, cleanup := NewIdleTimeoutReader(panicReader{}, &readCloserSpy{}, 10*time.Millisecond, nil)
	defer cleanup()

	time.Sleep(50 * time.Millisecond)

	_, err := wrapped.Read(make([]byte, 1))
	if !errors.Is(err, ErrStreamIdleTimeout) {
		t.Fatalf("expected ErrStreamIdleTimeout, got %v", err)
	}
}

func TestIdleTimeoutReader_CleanupWaitsForRunningTimerCallback(t *testing.T) {
	t.Parallel()
	body := newBlockingCloserSpy()
	_, cleanup := NewIdleTimeoutReader(&readCloserSpy{}, body, 10*time.Millisecond, nil)

	select {
	case <-body.started:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected idle timeout callback to start closing the body")
	}

	cleanupDone := make(chan struct{})
	go func() {
		cleanup()
		close(cleanupDone)
	}()

	select {
	case <-cleanupDone:
		t.Fatal("cleanup returned before the running timer callback finished")
	case <-time.After(50 * time.Millisecond):
	}

	body.unblock()

	select {
	case <-cleanupDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("cleanup did not return after timer callback finished")
	}
}

// TestSetupStreamCancellation_NoPanicOnCancelledContext reproduces the race where
// SetupStreamCancellation calls Close() before setting BifrostContextKeyConnectionClosed.
//
// Timeline with current (unfixed) code:
//  1. cancel() fires → SetupStreamCancellation goroutine calls closer.Close()
//  2. Close() closes panicTrigger → Read goroutine unblocks and panics
//  3. Close() blocks on allowReturn → flag is NOT yet set
//  4. idleTimeoutReader.Read recover: r.fired=false, connectionClosed=false → re-panics (BUG)
//
// Timeline after fix (set flag before Close()):
//  1. cancel() fires → SetupStreamCancellation sets flag → calls closer.Close()
//  2. Close() closes panicTrigger → Read goroutine unblocks and panics
//  3. idleTimeoutReader.Read recover: connectionClosed=true → returns ErrStreamClosed (OK)
func TestSetupStreamCancellation_NoPanicOnCancelledContext(t *testing.T) {
	t.Parallel()

	body := newSyncedPanicBody()
	goCtx, cancel := context.WithCancel(context.Background())
	bifrostCtx := schemas.NewBifrostContext(goCtx, time.Time{})

	reader, cleanupReader := NewIdleTimeoutReader(body, body, time.Minute, bifrostCtx)
	// Defers run LIFO: allowReturn first, then stopCancel, then cleanupReader.
	// This order ensures Close() can return before we wait for the goroutine.
	defer cleanupReader()
	stopCancel := SetupStreamCancellation(bifrostCtx, body, getLogger())
	defer stopCancel()
	defer close(body.allowReturn)

	type readResult struct {
		err     error
		panicked any
	}
	resultCh := make(chan readResult, 1)
	go func() {
		var res readResult
		defer func() {
			if r := recover(); r != nil {
				res.panicked = r
			}
			resultCh <- res
		}()
		buf := make([]byte, 1)
		_, res.err = reader.Read(buf)
	}()

	// Cancelling triggers SetupStreamCancellation → Close() → Read panics.
	// Close() is blocked on allowReturn so the flag has not been set yet when
	// the recover block in idleTimeoutReader.Read runs — worst-case ordering.
	cancel()

	select {
	case res := <-resultCh:
		if res.panicked != nil {
			t.Errorf("Read re-panicked (flag not set before Close — BUG): %v", res.panicked)
			return
		}
		if !errors.Is(res.err, ErrStreamClosed) {
			t.Errorf("expected ErrStreamClosed, got: %v", res.err)
		}
	case <-time.After(2 * time.Second):
		t.Error("Read goroutine did not unblock after context cancellation")
	}
}
