package utils

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// ---------------------------------------------------------------------------
// ResolveTimeoutConfig — exhaustive precedence and clamping matrix.
// ---------------------------------------------------------------------------

func tcIntPtr(v int) *int { return &v }

func TestResolveTimeoutConfig_PrecedenceAndClamping(t *testing.T) {
	cases := []struct {
		name      string
		key       *schemas.Key
		hasKey    bool
		vk        schemas.TimeoutConfig
		def       schemas.TimeoutConfig
		want      schemas.TimeoutConfig
	}{
		{
			name:   "all zero / no key → all zero",
			hasKey: false,
			want:   schemas.TimeoutConfig{},
		},
		{
			name:   "defaults only / no key fills request+idle but not total",
			hasKey: false,
			def:    schemas.TimeoutConfig{Request: 30 * time.Second, StreamIdle: 60 * time.Second},
			want:   schemas.TimeoutConfig{Request: 30 * time.Second, StreamIdle: 60 * time.Second},
		},
		{
			name:   "VK overrides default",
			hasKey: false,
			vk:     schemas.TimeoutConfig{Request: 5 * time.Second, StreamIdle: 7 * time.Second, StreamTotal: 90 * time.Second},
			def:    schemas.TimeoutConfig{Request: 30 * time.Second, StreamIdle: 60 * time.Second},
			want:   schemas.TimeoutConfig{Request: 5 * time.Second, StreamIdle: 7 * time.Second, StreamTotal: 90 * time.Second},
		},
		{
			name:   "key overrides VK and default",
			hasKey: true,
			key: &schemas.Key{
				RequestTimeoutInSeconds:     tcIntPtr(2),
				StreamIdleTimeoutInSeconds:  tcIntPtr(3),
				StreamTotalTimeoutInSeconds: tcIntPtr(4),
			},
			vk:   schemas.TimeoutConfig{Request: 10 * time.Second, StreamIdle: 11 * time.Second, StreamTotal: 12 * time.Second},
			def:  schemas.TimeoutConfig{Request: 100 * time.Second, StreamIdle: 100 * time.Second, StreamTotal: 100 * time.Second},
			want: schemas.TimeoutConfig{Request: 2 * time.Second, StreamIdle: 3 * time.Second, StreamTotal: 4 * time.Second},
		},
		{
			name:   "partial key — non-set fields fall back to VK then default",
			hasKey: true,
			key: &schemas.Key{
				StreamIdleTimeoutInSeconds: tcIntPtr(9),
			},
			vk:   schemas.TimeoutConfig{Request: 10 * time.Second},
			def:  schemas.TimeoutConfig{Request: 30 * time.Second, StreamIdle: 60 * time.Second},
			want: schemas.TimeoutConfig{Request: 10 * time.Second, StreamIdle: 9 * time.Second},
		},
		{
			name:   "negative VK leaks → clamped → default applied",
			hasKey: false,
			vk:     schemas.TimeoutConfig{Request: -5 * time.Second, StreamIdle: -1, StreamTotal: -100},
			def:    schemas.TimeoutConfig{Request: 30 * time.Second, StreamIdle: 60 * time.Second},
			want:   schemas.TimeoutConfig{Request: 30 * time.Second, StreamIdle: 60 * time.Second},
		},
		{
			name:   "zero key fields are NOT overrides (RequestTimeoutInSeconds=0 means unset)",
			hasKey: true,
			key: &schemas.Key{
				RequestTimeoutInSeconds:    tcIntPtr(0),
				StreamIdleTimeoutInSeconds: tcIntPtr(0),
			},
			vk:   schemas.TimeoutConfig{Request: 11 * time.Second, StreamIdle: 13 * time.Second},
			def:  schemas.TimeoutConfig{Request: 30 * time.Second, StreamIdle: 60 * time.Second},
			want: schemas.TimeoutConfig{Request: 11 * time.Second, StreamIdle: 13 * time.Second},
		},
		{
			name:   "default StreamTotal applied when neither VK nor key set it",
			hasKey: false,
			def:    schemas.TimeoutConfig{Request: 30 * time.Second, StreamIdle: 60 * time.Second, StreamTotal: 600 * time.Second},
			want:   schemas.TimeoutConfig{Request: 30 * time.Second, StreamIdle: 60 * time.Second, StreamTotal: 600 * time.Second},
		},
		{
			name:   "hasKey=true but key=nil treated as no key override (no panic)",
			hasKey: true,
			key:    nil,
			vk:     schemas.TimeoutConfig{Request: 7 * time.Second},
			def:    schemas.TimeoutConfig{Request: 30 * time.Second, StreamIdle: 60 * time.Second},
			want:   schemas.TimeoutConfig{Request: 7 * time.Second, StreamIdle: 60 * time.Second},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveTimeoutConfig(tc.key, tc.hasKey, tc.vk, tc.def)
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ApplyHTTPRequestTimeout — net/http (Bedrock) deadline propagation.
// ---------------------------------------------------------------------------

func TestApplyHTTPRequestTimeout_NoOverride(t *testing.T) {
	parent := context.Background()
	derived, cancel := ApplyHTTPRequestTimeout(parent, &http.Client{}, schemas.TimeoutConfig{})
	defer cancel()
	if derived != parent {
		t.Fatalf("expected original ctx returned when tc.Request<=0, got derived=%v", derived)
	}
	// cancel must be a callable no-op.
	cancel()
}

func TestApplyHTTPRequestTimeout_DeadlineFires(t *testing.T) {
	parent := context.Background()
	tc := schemas.TimeoutConfig{Request: 30 * time.Millisecond}
	ctx, cancel := ApplyHTTPRequestTimeout(parent, &http.Client{}, tc)
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected deadline on derived context")
	}
	if remaining := time.Until(deadline); remaining > 35*time.Millisecond || remaining < 0 {
		t.Fatalf("unexpected remaining=%v (want ~30ms)", remaining)
	}

	select {
	case <-ctx.Done():
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			t.Fatalf("ctx.Err = %v, want DeadlineExceeded", ctx.Err())
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("ctx.Done() did not fire within 200ms")
	}
}

func TestApplyHTTPRequestTimeout_CapsToClientTimeout(t *testing.T) {
	parent := context.Background()
	client := &http.Client{Timeout: 50 * time.Millisecond}
	tc := schemas.TimeoutConfig{Request: 5 * time.Second} // way above client ceiling
	ctx, cancel := ApplyHTTPRequestTimeout(parent, client, tc)
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected deadline")
	}
	rem := time.Until(deadline)
	if rem > 60*time.Millisecond {
		t.Fatalf("expected deadline capped at <=client.Timeout (50ms), got remaining=%v", rem)
	}
}

// ---------------------------------------------------------------------------
// ApplyStreamTimeouts — idle + total enforcement and cleanup safety.
// ---------------------------------------------------------------------------

// hangingReader blocks Read until ctxStop closes (simulates a stalled stream).
type hangingReader struct {
	ctxStop chan struct{}
	closed  atomic.Bool
}

func (h *hangingReader) Read(p []byte) (int, error) {
	<-h.ctxStop
	return 0, io.EOF
}
func (h *hangingReader) Close() error { h.closed.Store(true); close(h.ctxStop); return nil }

func TestApplyStreamTimeouts_IdleClosesStalledStream(t *testing.T) {
	body := &hangingReader{ctxStop: make(chan struct{})}
	tc := schemas.TimeoutConfig{StreamIdle: 30 * time.Millisecond}
	wrapped, cleanup := ApplyStreamTimeouts(tc, body, body.Close)
	defer cleanup()

	// Read should unblock once idle fires (which closes body via Closer assertion).
	done := make(chan error, 1)
	go func() {
		buf := make([]byte, 16)
		_, err := wrapped.Read(buf)
		done <- err
	}()
	select {
	case <-done:
		if !body.closed.Load() {
			t.Fatal("body was not closed by idle timer")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("idle timeout did not fire")
	}
}

// streamingReader emits a chunk, sleeps `interval`, repeats — never naturally ends.
type streamingReader struct {
	chunk    []byte
	interval time.Duration
	ctxStop  chan struct{}
	closed   atomic.Bool
}

func (s *streamingReader) Read(p []byte) (int, error) {
	select {
	case <-s.ctxStop:
		return 0, io.EOF
	case <-time.After(s.interval):
	}
	n := copy(p, s.chunk)
	return n, nil
}
func (s *streamingReader) Close() error {
	if s.closed.CompareAndSwap(false, true) {
		close(s.ctxStop)
	}
	return nil
}

func TestApplyStreamTimeouts_TotalCapTerminatesActiveStream(t *testing.T) {
	body := &streamingReader{
		chunk:    []byte("data: keepalive\n\n"),
		interval: 5 * time.Millisecond, // chunks fast enough that idle never fires
		ctxStop:  make(chan struct{}),
	}
	tc := schemas.TimeoutConfig{StreamIdle: 100 * time.Millisecond, StreamTotal: 60 * time.Millisecond}
	wrapped, cleanup := ApplyStreamTimeouts(tc, body, body.Close)
	defer cleanup()

	start := time.Now()
	buf := make([]byte, 64)
	for {
		_, err := wrapped.Read(buf)
		if err != nil {
			break
		}
		if time.Since(start) > 500*time.Millisecond {
			t.Fatal("total timeout did not fire within 500ms")
		}
	}
	elapsed := time.Since(start)
	if elapsed < 50*time.Millisecond {
		t.Fatalf("stream closed too early (elapsed=%v)", elapsed)
	}
	if elapsed > 250*time.Millisecond {
		t.Fatalf("stream closed too late (elapsed=%v, want ~60ms)", elapsed)
	}
	if !body.closed.Load() {
		t.Fatal("body was not closed by total timer")
	}
}

func TestApplyStreamTimeouts_NoTotal_OnlyIdle(t *testing.T) {
	// When StreamTotal=0, the cleanup func is a single timer's Stop — calling it twice
	// must remain safe and the wrapped reader must not enforce a wall-clock cap.
	body := &hangingReader{ctxStop: make(chan struct{})}
	tc := schemas.TimeoutConfig{StreamIdle: 0} // → DefaultStreamIdleTimeout
	wrapped, cleanup := ApplyStreamTimeouts(tc, body, body.Close)
	if wrapped == nil {
		t.Fatal("nil wrapped reader")
	}
	cleanup()
	cleanup() // double-cleanup must not panic
}

func TestApplyStreamTimeouts_DefaultsToProviderIdleWhenZero(t *testing.T) {
	body := &hangingReader{ctxStop: make(chan struct{})}
	defer body.Close()
	wrapped, cleanup := ApplyStreamTimeouts(schemas.TimeoutConfig{}, body, body.Close)
	defer cleanup()
	// Just exercise the path — internal timer must be set to DefaultStreamIdleTimeout (60s)
	// so the read should NOT close immediately.
	if wrapped == nil {
		t.Fatal("nil wrapped reader")
	}
	// Sanity: a brief read with a short stop window must NOT race the 60s timer.
	doneCh := make(chan struct{})
	go func() {
		<-time.After(10 * time.Millisecond)
		close(doneCh)
	}()
	<-doneCh
	if body.closed.Load() {
		t.Fatal("body was closed prematurely (idle timer fired before DefaultStreamIdleTimeout)")
	}
}

// ---------------------------------------------------------------------------
// End-to-end: net/http server with ApplyHTTPRequestTimeout actually aborts.
// Mirrors the Bedrock path (ApplyHTTPRequestTimeout → http.NewRequestWithContext).
// ---------------------------------------------------------------------------

func TestApplyHTTPRequestTimeout_AbortsHangingHTTPServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()

	client := &http.Client{}
	tc := schemas.TimeoutConfig{Request: 50 * time.Millisecond}
	ctx, cancel := ApplyHTTPRequestTimeout(context.Background(), client, tc)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = client.Do(req)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected error from canceled request, got nil")
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") &&
		!errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unexpected err: %v", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("request did not abort promptly (elapsed=%v)", elapsed)
	}
}
