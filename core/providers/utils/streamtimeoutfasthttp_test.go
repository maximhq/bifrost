package utils

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// REGRESSION TESTS — protect against the silent no-op bug where
// fasthttp.Response.BodyStream() returns *fasthttp.closeReader which does NOT
// implement io.Closer. Before the closeFn refactor the timeout helpers used a
// type assertion `bodyStream.(io.Closer)` that silently failed for fasthttp,
// turning every stream_total / stream_idle timeout into a no-op.
//
// Note on -race: The two TotalCap_RealFastHTTP / Idle_RealFastHTTP cases skip
// under the race detector. They drive a real fasthttp client where the close
// path (resp.CloseBodyStream) inherently races with the in-flight Read on
// fasthttp's internal *requestStream — this is a fasthttp-internal race, not
// in Bifrost code. Production accepts the race because (a) the alternative is
// leaking goroutines forever, and (b) the upstream connection is being torn
// down regardless. The Cleanup_* tests (which exercise our code paths without
// fasthttp internals) run cleanly under -race.

func streamingHandler(interval time.Duration, chunks *atomic.Int64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		ctx := r.Context()
		i := 0
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if _, err := fmt.Fprintf(w, "data: chunk-%d\n\n", i); err != nil {
					return
				}
				if flusher != nil {
					flusher.Flush()
				}
				chunks.Add(1)
				i++
			}
		}
	}
}

func streamOnceThenIdleHandler(initialDelay time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		time.Sleep(initialDelay)
		_, _ = w.Write([]byte("data: hello\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
		<-r.Context().Done()
	}
}

func newFastHTTPStreamingClient(srv *httptest.Server) *fasthttp.Client {
	return &fasthttp.Client{
		StreamResponseBody: true,
		ReadTimeout:        0,
		Dial: func(addr string) (net.Conn, error) {
			return net.Dial("tcp", srv.Listener.Addr().String())
		},
	}
}

// TestApplyStreamTimeouts_TotalCap_RealFastHTTP — drives a real fasthttp
// client against a real httptest server. With stream_total=300ms the wrapped
// reader must error out within ~1s and closeFn must run exactly once.
func TestApplyStreamTimeouts_TotalCap_RealFastHTTP(t *testing.T) {
	t.Parallel()
	skipUnderRace(t)

	chunks := &atomic.Int64{}
	srv := httptest.NewServer(streamingHandler(50*time.Millisecond, chunks))
	defer srv.Close()

	client := newFastHTTPStreamingClient(srv)

	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(srv.URL)
	req.Header.SetMethod("GET")

	if err := client.Do(req, resp); err != nil {
		t.Fatalf("do: %v", err)
	}

	closeCalls := &atomic.Int64{}
	closeFn := StreamCloseFunc(func() error {
		closeCalls.Add(1)
		return resp.CloseBodyStream()
	})

	tc := schemas.TimeoutConfig{StreamTotal: 300 * time.Millisecond}
	reader, cleanup := ApplyStreamTimeouts(tc, resp.BodyStream(), closeFn)
	defer cleanup()

	start := time.Now()
	buf := make([]byte, 4096)
	var totalRead int
	for {
		n, err := reader.Read(buf)
		totalRead += n
		if err != nil {
			break
		}
	}
	elapsed := time.Since(start)

	if elapsed > 1500*time.Millisecond {
		t.Fatalf("expected total-cap to fire near 300ms, but Read blocked for %v", elapsed)
	}
	if elapsed < 200*time.Millisecond {
		t.Fatalf("Read returned too early (%v); did the timer not arm?", elapsed)
	}
	if totalRead == 0 {
		t.Fatalf("expected to receive some chunks before total cap fired (server emitted %d)", chunks.Load())
	}
	if closeCalls.Load() != 1 {
		t.Fatalf("expected closeFn invoked exactly once, got %d", closeCalls.Load())
	}
}

// TestApplyStreamTimeouts_Idle_RealFastHTTP — server emits one chunk then
// stays silent. With stream_idle=200ms the wrapped reader must error out and
// closeFn must run exactly once.
func TestApplyStreamTimeouts_Idle_RealFastHTTP(t *testing.T) {
	t.Parallel()
	skipUnderRace(t)

	srv := httptest.NewServer(streamOnceThenIdleHandler(50 * time.Millisecond))
	defer srv.Close()

	client := newFastHTTPStreamingClient(srv)

	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(srv.URL)
	req.Header.SetMethod("GET")
	if err := client.Do(req, resp); err != nil {
		t.Fatalf("do: %v", err)
	}

	closeCalls := &atomic.Int64{}
	closeFn := StreamCloseFunc(func() error {
		closeCalls.Add(1)
		return resp.CloseBodyStream()
	})

	tc := schemas.TimeoutConfig{StreamIdle: 200 * time.Millisecond}
	reader, cleanup := ApplyStreamTimeouts(tc, resp.BodyStream(), closeFn)
	defer cleanup()

	start := time.Now()
	buf := make([]byte, 4096)
	for {
		_, err := reader.Read(buf)
		if err != nil {
			break
		}
	}
	elapsed := time.Since(start)

	if elapsed > 1500*time.Millisecond {
		t.Fatalf("idle timeout never fired (waited %v)", elapsed)
	}
	if closeCalls.Load() != 1 {
		t.Fatalf("expected closeFn invoked exactly once, got %d", closeCalls.Load())
	}
}

// TestApplyStreamTimeouts_Cleanup_NoSpuriousClose verifies cleanup before any
// timer fires does NOT invoke closeFn (cleanup must disarm timers).
func TestApplyStreamTimeouts_Cleanup_NoSpuriousClose(t *testing.T) {
	t.Parallel()
	closeCalls := &atomic.Int64{}
	closeFn := StreamCloseFunc(func() error {
		closeCalls.Add(1)
		return nil
	})

	pr, pw := io.Pipe()
	defer pr.Close()
	defer pw.Close()

	tc := schemas.TimeoutConfig{StreamIdle: 50 * time.Millisecond, StreamTotal: 50 * time.Millisecond}
	_, cleanup := ApplyStreamTimeouts(tc, pr, closeFn)
	cleanup()

	time.Sleep(200 * time.Millisecond)
	if closeCalls.Load() != 0 {
		t.Fatalf("closeFn invoked %d times after cleanup; expected 0 (cleanup did not disarm timers)", closeCalls.Load())
	}
}

// TestApplyStreamTimeouts_Cleanup_SyncWithFiringTimer guarantees cleanup
// blocks until a concurrently-firing timer's closeFn has finished. Otherwise
// the caller could release the fasthttp response object while closeFn is mid-
// CloseBodyStream — corrupting the pool.
func TestApplyStreamTimeouts_Cleanup_SyncWithFiringTimer(t *testing.T) {
	t.Parallel()
	inFlight := &atomic.Int64{}
	released := &atomic.Bool{}
	closeFn := StreamCloseFunc(func() error {
		inFlight.Add(1)
		defer inFlight.Add(-1)
		time.Sleep(80 * time.Millisecond)
		if released.Load() {
			panic("closeFn ran AFTER caller released the response object")
		}
		return nil
	})

	pr, pw := io.Pipe()
	defer pr.Close()
	defer pw.Close()

	tc := schemas.TimeoutConfig{StreamIdle: 30 * time.Millisecond}
	_, cleanup := ApplyStreamTimeouts(tc, pr, closeFn)

	time.Sleep(50 * time.Millisecond)
	cleanup()
	released.Store(true)

	if inFlight.Load() != 0 {
		t.Fatalf("cleanup returned while closeFn still in flight (inFlight=%d) — would corrupt fasthttp pool", inFlight.Load())
	}
}

// TestSetupStreamCancellation_ClosesOnCtxCancel — verifies the ctx-cancel
// path invokes closeFn exactly once (used for client-disconnect detection).
func TestSetupStreamCancellation_ClosesOnCtxCancel(t *testing.T) {
	t.Parallel()
	closeCalls := &atomic.Int64{}
	closeFn := StreamCloseFunc(func() error {
		closeCalls.Add(1)
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	cleanup := SetupStreamCancellation(ctx, closeFn, nil)
	cancel()
	time.Sleep(50 * time.Millisecond)
	cleanup()

	if closeCalls.Load() != 1 {
		t.Fatalf("expected closeFn invoked once on ctx cancel, got %d", closeCalls.Load())
	}
}
