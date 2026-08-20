package utils

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttputil"
)

// ---------------------------------------------------------------------------
// Regression coverage for maximhq/bifrost#6143
//
// fasthttp's streaming close callback (client.go, transport.RoundTrip) runs:
//
//	hc.ReleaseReader(br)            // *bufio.Reader -> sync.Pool
//	releaseRequestStream(rbs)       // zeroes rs.reader/header/chunkLeft -> sync.Pool
//	hc.CloseConn(cc)                // only NOW does the blocked Read wake up
//
// The pooled objects are recycled BEFORE the connection is closed, so a reader
// parked inside (*requestStream).Read is still executing methods on a struct
// that another request may already have taken out of the pool. This is not a
// double-close problem — a single, perfectly serialized close is unsafe.
//
// The contract Bifrost must therefore honour is stricter than "close at most
// once" (which the existing BifrostContextKeyConnectionClosed CAS already
// gives us): nothing may invoke CloseWithError on the fasthttp body stream
// while a Read on that stream is in flight.
// ---------------------------------------------------------------------------

// TestDoStreamingRequest_BodyIsNotPooledFastHTTPStream asserts the invariant the
// #6143 fix establishes.
//
// The original bug is not "two goroutines touched a stream". It is that
// fasthttp's streaming close callback returns the pooled *requestStream and
// *bufio.Reader to their sync.Pools BEFORE closing the connection, while
// closing the connection is the only thing that wakes a reader parked in
// (*requestStream).Read. No caller-side locking can make that safe, so the fix
// is to keep fasthttp's pooled stream off the streaming path entirely: the
// request goes out over net/http, whose Body guards its closed flag with a
// mutex and pools nothing.
//
// This test fails if anyone routes streaming responses back through fasthttp's
// body stream, which would silently reintroduce the race.
func TestDoStreamingRequest_BodyIsNotPooledFastHTTPStream(t *testing.T) {
	t.Parallel()

	client, cleanup := newSSEStreamServer(t, 3, time.Millisecond)
	defer cleanup()

	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	resp := fasthttp.AcquireResponse()

	req.SetRequestURI("http://test/v1/chat/completions")
	req.Header.SetMethod(fasthttp.MethodPost)
	req.SetBodyString(`{"stream":true}`)
	resp.StreamBody = true

	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	if err := DoStreamingRequest(ctx, client, req, resp); err != nil {
		t.Fatalf("streaming request failed: %v", err)
	}
	defer ReleaseStreamingResponse(ctx, resp)

	body := resp.BodyStream()
	if body == nil {
		t.Fatal("streaming response has no body stream")
	}
	if _, ok := body.(io.Closer); !ok {
		t.Fatalf("body stream %T is not an io.Closer, cancellation cannot close it", body)
	}
	if got := fmt.Sprintf("%T", body); strings.Contains(got, "fasthttp") {
		t.Fatalf("body stream is a fasthttp type (%s): closing it under an in-flight "+
			"Read recycles the pooled *requestStream into sync.Pool (issue #6143)", got)
	}
	if resp.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("status %d, want 200: header copy from the net/http response is broken", resp.StatusCode())
	}
	if ct := string(resp.Header.ContentType()); !strings.Contains(ct, "event-stream") {
		t.Fatalf("content-type %q, want text/event-stream: header copy is broken", ct)
	}
}

// ---------------------------------------------------------------------------
// End-to-end reproduction against a real fasthttp client + server.
// Run with -race to see the detector report.
// ---------------------------------------------------------------------------

// newSSEStreamServer serves a chunked SSE body, emitting chunks at the given
// interval, over an in-memory listener.
func newSSEStreamServer(t *testing.T, chunks int, interval time.Duration) (*fasthttp.Client, func()) {
	t.Helper()
	ln := fasthttputil.NewInmemoryListener()

	server := &fasthttp.Server{
		Handler: func(ctx *fasthttp.RequestCtx) {
			ctx.SetStatusCode(fasthttp.StatusOK)
			ctx.SetContentType("text/event-stream")
			ctx.SetBodyStreamWriter(func(w *bufio.Writer) {
				for i := 0; i < chunks; i++ {
					if _, err := w.WriteString("data: {\"i\":" + string(rune('0'+i%10)) + "}\n\n"); err != nil {
						return
					}
					if err := w.Flush(); err != nil {
						return
					}
					time.Sleep(interval)
				}
				_, _ = w.WriteString("data: [DONE]\n\n") //nolint:errcheck
				_ = w.Flush()                            //nolint:errcheck
			})
		},
	}
	go server.Serve(ln) //nolint:errcheck

	base := &fasthttp.Client{
		Dial:         func(string) (net.Conn, error) { return ln.Dial() },
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}
	return BuildStreamingClient(base), func() { _ = ln.Close() } //nolint:errcheck
}

// TestStreamCancellation_PooledRequestStreamRace drives the exact path from the
// issue report: a real fasthttp streaming response, the SSE reader parked in
// (*requestStream).Read via idleTimeoutReader, and SetupStreamCancellation's
// watchdog closing the stream on ctx cancellation mid-body.
//
// Under -race this reports "WARNING: DATA RACE" between releaseRequestStream
// (write) and (*requestStream).Read (read). Without -race it is a no-op
// smoke test; the deterministic contract assertion lives in the test above.
func TestStreamCancellation_PooledRequestStreamRace(t *testing.T) {
	client, cleanup := newSSEStreamServer(t, 40, 50*time.Millisecond)
	defer cleanup()

	// A pool of concurrent cancelled streams makes the aliasing window wide
	// enough for the detector to catch it on a single run.
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runCancelledStream(t, client)
		}()
	}
	wg.Wait()
}

func runCancelledStream(t *testing.T, client *fasthttp.Client) {
	t.Helper()

	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	resp := fasthttp.AcquireResponse()

	req.SetRequestURI("http://test/v1/chat/completions")
	req.Header.SetMethod(fasthttp.MethodPost)
	req.SetBodyString(`{"stream":true}`)
	resp.StreamBody = true

	goCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := schemas.NewBifrostContext(goCtx, time.Time{})

	if err := DoStreamingRequest(ctx, client, req, resp); err != nil {
		t.Errorf("streaming request failed: %v", err)
		fasthttp.ReleaseResponse(resp)
		return
	}

	// Mirror the provider streaming goroutine's wrapper stack exactly.
	defer ReleaseStreamingResponse(ctx, resp)
	reader, stopIdleTimeout := NewIdleTimeoutReader(resp.BodyStream(), resp.BodyStream(), time.Minute, ctx)
	defer stopIdleTimeout()
	stopCancellation := SetupStreamCancellation(ctx, resp.BodyStream(), getLogger())
	defer stopCancellation()

	// Cancel while the body is still streaming — the trigger condition.
	time.AfterFunc(250*time.Millisecond, cancel)

	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}
	}
}
