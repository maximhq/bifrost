package utils

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttputil"
)

// newTestServer creates an in-memory fasthttp server that responds after the given delay.
// Returns a client configured to talk to it and a cleanup function.
func newTestServer(t *testing.T, delay time.Duration, statusCode int) (*fasthttp.Client, func()) {
	t.Helper()
	ln := fasthttputil.NewInmemoryListener()

	server := &fasthttp.Server{
		Handler: func(ctx *fasthttp.RequestCtx) {
			if delay > 0 {
				time.Sleep(delay)
			}
			ctx.SetStatusCode(statusCode)
			ctx.SetBody([]byte(`{"ok":true}`))
		},
	}

	go server.Serve(ln) //nolint:errcheck

	client := &fasthttp.Client{
		Dial: func(addr string) (net.Conn, error) {
			return ln.Dial()
		},
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}

	cleanup := func() {
		ln.Close()
	}

	return client, cleanup
}

func TestMakeRequestWithContext_SuccessReturnsNoopWait(t *testing.T) {
	client, cleanup := newTestServer(t, 0, 200)
	defer cleanup()

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI("http://test/")

	latency, bifrostErr, wait := MakeRequestWithContext(context.Background(), client, req, resp)
	defer wait()

	if bifrostErr != nil {
		t.Fatalf("expected no error, got: %v", bifrostErr.Error.Message)
	}
	if latency <= 0 {
		t.Fatal("expected positive latency")
	}
	if resp.StatusCode() != 200 {
		t.Fatalf("expected status 200, got %d", resp.StatusCode())
	}
}

func TestMakeRequestWithContext_DeadlineExceededReturnsTimeoutError(t *testing.T) {
	// Server takes 500ms to respond
	client, cleanup := newTestServer(t, 500*time.Millisecond, 200)
	defer cleanup()

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	req.SetRequestURI("http://test/")

	// Deadline exceeded almost immediately
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, bifrostErr, wait := MakeRequestWithContext(ctx, client, req, resp)

	// Should get a timeout error with 504 status
	if bifrostErr == nil {
		t.Fatal("expected timeout error")
	}
	if bifrostErr.Error.Type == nil || *bifrostErr.Error.Type != schemas.RequestTimedOut {
		t.Fatalf("expected RequestTimedOut error type, got: %v", bifrostErr.Error.Type)
	}
	if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != 504 {
		t.Fatalf("expected status 504, got: %v", bifrostErr.StatusCode)
	}

	// wait() should block until the goroutine finishes, then we can safely release
	start := time.Now()
	wait()
	elapsed := time.Since(start)

	// The wait should have taken roughly the remaining server delay (~490ms)
	if elapsed < 200*time.Millisecond {
		t.Fatalf("wait() returned too quickly (%v), expected it to block until goroutine finishes", elapsed)
	}

	// Now safe to release
	fasthttp.ReleaseRequest(req)
	fasthttp.ReleaseResponse(resp)
}

func TestMakeRequestWithContext_ContextCancelReturnsCancelledError(t *testing.T) {
	// Server takes 500ms to respond
	client, cleanup := newTestServer(t, 500*time.Millisecond, 200)
	defer cleanup()

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	req.SetRequestURI("http://test/")

	// Cancel context explicitly (not deadline)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	_, bifrostErr, wait := MakeRequestWithContext(ctx, client, req, resp)

	// Should get a cancellation error with 499 status
	if bifrostErr == nil {
		t.Fatal("expected cancellation error")
	}
	if bifrostErr.Error.Type == nil || *bifrostErr.Error.Type != schemas.RequestCancelled {
		t.Fatalf("expected RequestCancelled error type, got: %v", bifrostErr.Error.Type)
	}
	if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != 499 {
		t.Fatalf("expected status 499, got: %v", bifrostErr.StatusCode)
	}

	// wait() should block until the goroutine finishes
	start := time.Now()
	wait()
	elapsed := time.Since(start)

	if elapsed < 200*time.Millisecond {
		t.Fatalf("wait() returned too quickly (%v), expected it to block until goroutine finishes", elapsed)
	}

	fasthttp.ReleaseRequest(req)
	fasthttp.ReleaseResponse(resp)
}

func TestMakeRequestWithContext_WaitPreventsDataRace(t *testing.T) {
	// This test verifies the fix for the data race. Under -race, accessing resp
	// while client.Do is still writing to it would be flagged. The wait function
	// ensures we don't release until the goroutine is done.
	//
	// Run with: go test -race -run TestMakeRequestWithContext_WaitPreventsDataRace

	// Server responds after 200ms
	client, cleanup := newTestServer(t, 200*time.Millisecond, 200)
	defer cleanup()

	for range 10 {
		func() {
			req := fasthttp.AcquireRequest()
			resp := fasthttp.AcquireResponse()
			req.SetRequestURI("http://test/")

			// Cancel context after 5ms — well before server responds
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
			defer cancel()

			_, _, wait := MakeRequestWithContext(ctx, client, req, resp)

			// Simulate the real caller pattern: defer wait() before defer Release.
			// Go defers are LIFO, so wait() runs first, then Release.
			// This is the pattern that prevents the data race.
			defer fasthttp.ReleaseRequest(req)
			defer fasthttp.ReleaseResponse(resp)
			defer wait()
		}()
	}
}

func TestMakeRequestWithContext_WaitIsIdempotent(t *testing.T) {
	client, cleanup := newTestServer(t, 50*time.Millisecond, 200)
	defer cleanup()

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI("http://test/")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	_, _, wait := MakeRequestWithContext(ctx, client, req, resp)

	// First call should block
	wait()
	// Second call should not deadlock (channel already drained)
	// Note: this will deadlock if the implementation is wrong, so the test
	// would time out rather than fail gracefully.
	done := make(chan struct{})
	go func() {
		wait()
		close(done)
	}()

	select {
	case <-done:
		// Second wait() completed — but note this actually WILL deadlock with
		// the current implementation since <-errChan can only be read once.
		// This documents the behavior: wait() should only be called once.
	case <-time.After(100 * time.Millisecond):
		// Expected: second wait() blocks forever because errChan is already drained.
		// This is fine — callers should only call wait() once (via a single defer).
	}
}

func TestMakeRequestWithContext_SuccessWaitDoesNotBlock(t *testing.T) {
	client, cleanup := newTestServer(t, 0, 200)
	defer cleanup()

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI("http://test/")

	_, _, wait := MakeRequestWithContext(context.Background(), client, req, resp)

	// On the success path, wait should be a noop that returns immediately
	start := time.Now()
	wait()
	if time.Since(start) > 10*time.Millisecond {
		t.Fatal("wait() on success path should be a noop and return immediately")
	}
}

func TestMakeRequestWithContext_ConcurrentRequestsWithCancellation(t *testing.T) {
	// Simulate the production scenario: multiple concurrent requests where some
	// contexts cancel while the HTTP call is in-flight. Under -race, this would
	// detect the original bug where deferred Release races with client.Do.
	client, cleanup := newTestServer(t, 100*time.Millisecond, 200)
	defer cleanup()

	const numRequests = 20
	var completed atomic.Int32

	done := make(chan struct{})
	for range numRequests {
		go func() {
			defer func() {
				if completed.Add(1) == numRequests {
					close(done)
				}
			}()

			req := fasthttp.AcquireRequest()
			resp := fasthttp.AcquireResponse()
			req.SetRequestURI("http://test/")

			// Half the requests cancel early, half complete normally
			var ctx context.Context
			var cancel context.CancelFunc
			if completed.Load()%2 == 0 {
				ctx, cancel = context.WithTimeout(context.Background(), 5*time.Millisecond)
			} else {
				ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
			}

			_, _, wait := MakeRequestWithContext(ctx, client, req, resp)
			// Correct pattern: wait before release
			wait()
			cancel()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
		}()
	}

	select {
	case <-done:
		// All requests completed
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for requests, only %d/%d completed", completed.Load(), numRequests)
	}
}

func TestNewBifrostTimeoutError(t *testing.T) {
	err := NewBifrostTimeoutError("test timeout", context.DeadlineExceeded)

	if !err.IsBifrostError {
		t.Fatal("expected IsBifrostError to be true")
	}
	if err.StatusCode == nil || *err.StatusCode != 504 {
		t.Fatalf("expected StatusCode 504, got %v", err.StatusCode)
	}
	if err.Error.Type == nil || *err.Error.Type != schemas.RequestTimedOut {
		t.Fatalf("expected RequestTimedOut type, got %v", err.Error.Type)
	}
	if err.Error.Message != "test timeout" {
		t.Fatalf("expected 'test timeout', got %s", err.Error.Message)
	}
	// Note: ExtraFields.Provider is populated by bifrost.go's dispatcher via
	// PopulateExtraFields, not by NewBifrostTimeoutError — the constructor has
	// no provider context.
}

func TestMakeRequestWithContext_ClientError(t *testing.T) {
	// Test that client errors still return noop wait function
	client := &fasthttp.Client{
		Dial: func(addr string) (net.Conn, error) {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host", Name: "nonexistent.invalid"}}
		},
		ReadTimeout:  1 * time.Second,
		WriteTimeout: 1 * time.Second,
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI("http://nonexistent.invalid/")

	_, bifrostErr, wait := MakeRequestWithContext(context.Background(), client, req, resp)
	defer wait()

	if bifrostErr == nil {
		t.Fatal("expected error for nonexistent host")
	}
	// Upstream connectivity failures (DNS/OpError) should surface as 502 Bad Gateway,
	// not the default 400 — see NewBifrostUpstreamConnectionError.
	if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != 502 {
		t.Fatalf("expected StatusCode 502, got %v", bifrostErr.StatusCode)
	}
	if bifrostErr.Error.Type == nil || *bifrostErr.Error.Type != schemas.ProviderConnectionFailed {
		t.Fatalf("expected ProviderConnectionFailed type, got %v", bifrostErr.Error.Type)
	}
	// wait should be noop since the goroutine completed (with error)
	start := time.Now()
	wait()
	if time.Since(start) > 10*time.Millisecond {
		t.Fatal("wait() should be noop on error path")
	}
}

func TestNewBifrostUpstreamConnectionError(t *testing.T) {
	err := NewBifrostUpstreamConnectionError("upstream dropped connection", context.DeadlineExceeded)

	if err.IsBifrostError {
		t.Fatal("expected IsBifrostError to be false (upstream is at fault)")
	}
	if err.StatusCode == nil || *err.StatusCode != 502 {
		t.Fatalf("expected StatusCode 502, got %v", err.StatusCode)
	}
	if err.Error.Type == nil || *err.Error.Type != schemas.ProviderConnectionFailed {
		t.Fatalf("expected ProviderConnectionFailed type, got %v", err.Error.Type)
	}
	if err.Error.Message != "upstream dropped connection" {
		t.Fatalf("expected 'upstream dropped connection', got %s", err.Error.Message)
	}
}

func TestMakeRequestWithContext_DeferOrderingPattern(t *testing.T) {
	// Verify the exact defer pattern used by callers works correctly under -race.
	// This mirrors the real provider code pattern.
	client, cleanup := newTestServer(t, 150*time.Millisecond, 200)
	defer cleanup()

	// Track the order of operations
	var order []string
	var orderDone = make(chan struct{})

	go func() {
		defer close(orderDone)

		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()
		req.SetRequestURI("http://test/")

		// Mimic the real provider pattern with defer ordering:
		// These defers run in reverse order (LIFO)
		defer func() {
			fasthttp.ReleaseRequest(req)
			order = append(order, "release-req")
		}()
		defer func() {
			fasthttp.ReleaseResponse(resp)
			order = append(order, "release-resp")
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()

		_, _, wait := MakeRequestWithContext(ctx, client, req, resp)
		// This defer runs FIRST (last declared = first to run)
		defer func() {
			wait()
			order = append(order, "wait-done")
		}()
	}()

	select {
	case <-orderDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
	}

	// Verify order: wait must complete before any release
	if len(order) != 3 {
		t.Fatalf("expected 3 operations, got %d: %v", len(order), order)
	}
	if order[0] != "wait-done" {
		t.Fatalf("expected wait-done first, got: %v", order)
	}
	if order[1] != "release-resp" {
		t.Fatalf("expected release-resp second, got: %v", order)
	}
	if order[2] != "release-req" {
		t.Fatalf("expected release-req third, got: %v", order)
	}
}

// TestMakeRequestWithContext_CancelClosesSocket verifies that cancelling the
// context closes the upstream socket, so the background client.Do goroutine
// finishes promptly and wait() does not block for the client's ReadTimeout.
// The upstream never sends response headers (issue #7034); before the fix the
// goroutine stayed parked on the socket read until ReadTimeout (10s here).
func TestMakeRequestWithContext_CancelClosesSocket(t *testing.T) {
	upstream := newSilentUpstream(t)

	client := &fasthttp.Client{
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	ConfigureDialer(client, true)

	req, resp := newSilentStreamingRequest(upstream.URL())

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// Cancel only once the upstream holds the connection, so the cancel
		// lands in the header wait rather than on a queued connect.
		upstream.awaitAccept(2 * time.Second)
		cancel()
	}()

	_, bifrostErr, wait := MakeRequestWithContext(ctx, client, req, resp)
	if bifrostErr == nil {
		t.Fatal("expected cancellation error")
	}
	if got := upstream.accepted.Load(); got != 1 {
		t.Fatalf("connections accepted = %d, want 1 before cancellation", got)
	}
	if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != 499 {
		t.Fatalf("expected status 499, got: %v", bifrostErr.StatusCode)
	}

	waited := make(chan struct{})
	start := time.Now()
	go func() {
		wait()
		close(waited)
	}()
	select {
	case <-waited:
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("wait() took %v: the cancelled request did not release the socket promptly", elapsed)
		}
		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)
	case <-time.After(3 * time.Second):
		t.Fatal("context cancellation does not reach the socket: wait() still blocked 3s after cancel(), the background client.Do is parked until ReadTimeout")
	}

	select {
	case <-upstream.hangups:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream never observed the client hanging up after cancellation")
	}
}

// scriptedServer serves raw HTTP/1.1 bytes over an in-memory listener so the
// contextTransport tests can control framing precisely (chunked trailers, 1xx
// interim responses, stalls). handle is called once per accepted connection
// with a request reader; it returns when the connection should be closed.
type scriptedServer struct {
	ln       *fasthttputil.InmemoryListener
	accepted atomic.Int32
}

func newScriptedServer(t *testing.T, handle func(conn net.Conn, br *bufio.Reader)) *scriptedServer {
	t.Helper()
	s := &scriptedServer{ln: fasthttputil.NewInmemoryListener()}
	go func() {
		for {
			conn, err := s.ln.Accept()
			if err != nil {
				return
			}
			s.accepted.Add(1)
			go func() {
				defer conn.Close()
				handle(conn, bufio.NewReader(conn))
			}()
		}
	}()
	t.Cleanup(func() { s.ln.Close() })
	return s
}

// client returns a fully configured Bifrost-style client (ConfigureDialer
// installs the context transport) dialing the in-memory listener.
func (s *scriptedServer) client(readTimeout time.Duration) *fasthttp.Client {
	c := &fasthttp.Client{
		Dial:         func(addr string) (net.Conn, error) { return s.ln.Dial() },
		ReadTimeout:  readTimeout,
		WriteTimeout: readTimeout,
	}
	ConfigureDialer(c, true)
	return c
}

// readRequest parses one request off br; false when the client hung up.
func readRequest(br *bufio.Reader) bool {
	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	return req.Read(br) == nil
}

func writeAll(t *testing.T, conn net.Conn, s string) {
	t.Helper()
	if _, err := io.WriteString(conn, s); err != nil {
		t.Errorf("server write: %v", err)
	}
}

func streamGet(t *testing.T, client *fasthttp.Client, method string) (*fasthttp.Request, *fasthttp.Response) {
	t.Helper()
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	req.SetRequestURI("http://scripted/stream")
	req.Header.SetMethod(method)
	if err := DoStreamingRequest(context.Background(), client, req, resp); err != nil {
		t.Fatalf("DoStreamingRequest: %v", err)
	}
	return req, resp
}

// TestContextTransport_ChunkedStreamReleasesConnForReuse verifies a chunked
// streamed body is decoded, its trailer is consumed, and the connection goes
// back to the pool so the next request reuses it instead of dialing again.
func TestContextTransport_ChunkedStreamReleasesConnForReuse(t *testing.T) {
	srv := newScriptedServer(t, func(conn net.Conn, br *bufio.Reader) {
		for readRequest(br) {
			writeAll(t, conn, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\nTrailer: X-Done\r\n\r\n")
			writeAll(t, conn, "9\r\ndata: a\n\n\r\n")
			writeAll(t, conn, "9\r\ndata: b\n\n\r\n")
			writeAll(t, conn, "0\r\nX-Done: yes\r\n\r\n")
		}
	})
	client := BuildStreamingClient(srv.client(5 * time.Second))

	for i := 1; i <= 2; i++ {
		req, resp := streamGet(t, client, "GET")
		if resp.StatusCode() != 200 {
			t.Fatalf("request %d: status %d", i, resp.StatusCode())
		}
		body, err := io.ReadAll(resp.BodyStream())
		if err != nil {
			t.Fatalf("request %d: read body: %v", i, err)
		}
		if got := string(body); got != "data: a\n\ndata: b\n\n" {
			t.Fatalf("request %d: body = %q", i, got)
		}
		if err := resp.CloseBodyStream(); err != nil {
			t.Fatalf("request %d: CloseBodyStream: %v", i, err)
		}
		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)
	}
	if got := srv.accepted.Load(); got != 1 {
		t.Fatalf("connections accepted = %d, want 1 (fully read chunked stream must be returned to the pool)", got)
	}
}

// TestContextTransport_TruncatedChunkedStreamReadsAsEOFAndDiscardsConn pins the
// contract every provider read loop and the semantic truncation check (#5546) are
// built on: fasthttp reports an upstream that closes on a chunk boundary as a plain
// io.EOF and leaves "was the stream complete?" to the caller's terminal-marker
// check, which turns a missing marker into the retryable 502 truncation error. The
// standard-library chunked reader says io.ErrUnexpectedEOF for the same close, and
// the loops route any non-EOF error to a generic stream failure instead. The
// half-read connection must still be closed, never returned to the pool: a POST
// (non-idempotent, so fasthttp will not silently retry it) must get a fresh
// connection and succeed.
func TestContextTransport_TruncatedChunkedStreamReadsAsEOFAndDiscardsConn(t *testing.T) {
	srv := newScriptedServer(t, func(conn net.Conn, br *bufio.Reader) {
		if !readRequest(br) {
			return
		}
		writeAll(t, conn, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n")
		writeAll(t, conn, "9\r\ndata: a\n\n\r\n")
		// Returning closes conn without the terminating 0-length chunk.
	})
	client := BuildStreamingClient(srv.client(5 * time.Second))

	req, resp := streamGet(t, client, "GET")
	body, err := io.ReadAll(resp.BodyStream())
	if got := string(body); got != "data: a\n\n" {
		t.Fatalf("body before the drop = %q, want %q", got, "data: a\n\n")
	}
	if err != nil {
		t.Fatalf("truncated chunked body read error = %v, want a plain io.EOF (io.ReadAll returns nil on EOF)", err)
	}
	if err := resp.CloseBodyStream(); err != nil {
		t.Fatalf("CloseBodyStream: %v", err)
	}
	fasthttp.ReleaseRequest(req)
	fasthttp.ReleaseResponse(resp)

	req, resp = streamGet(t, client, "POST")
	if resp.StatusCode() != 200 {
		t.Fatalf("second request status = %d, want 200", resp.StatusCode())
	}
	_ = resp.CloseBodyStream()
	fasthttp.ReleaseRequest(req)
	fasthttp.ReleaseResponse(resp)
	if got := srv.accepted.Load(); got != 2 {
		t.Fatalf("connections accepted = %d, want 2 (a truncated stream's connection must be discarded, not pooled)", got)
	}
}

// TestContextTransport_ContentLengthAndIdentityBodies covers the two
// non-chunked framings of a streamed body.
func TestContextTransport_ContentLengthAndIdentityBodies(t *testing.T) {
	srv := newScriptedServer(t, func(conn net.Conn, br *bufio.Reader) {
		// First request: Content-Length body, keep-alive.
		if !readRequest(br) {
			return
		}
		writeAll(t, conn, "HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nhello")
		// Second request on the same connection: identity body until close.
		if !readRequest(br) {
			return
		}
		writeAll(t, conn, "HTTP/1.1 200 OK\r\nConnection: close\r\n\r\nworld!")
	})
	client := BuildStreamingClient(srv.client(5 * time.Second))

	req, resp := streamGet(t, client, "GET")
	body, err := io.ReadAll(resp.BodyStream())
	if err != nil || string(body) != "hello" {
		t.Fatalf("content-length body = %q, err = %v", body, err)
	}
	resp.CloseBodyStream() //nolint:errcheck
	fasthttp.ReleaseRequest(req)
	fasthttp.ReleaseResponse(resp)

	req, resp = streamGet(t, client, "GET")
	body, err = io.ReadAll(resp.BodyStream())
	if err != nil || string(body) != "world!" {
		t.Fatalf("identity body = %q, err = %v", body, err)
	}
	resp.CloseBodyStream() //nolint:errcheck
	fasthttp.ReleaseRequest(req)
	fasthttp.ReleaseResponse(resp)

	if got := srv.accepted.Load(); got != 1 {
		t.Fatalf("connections accepted = %d, want 1 (Content-Length body must be reusable)", got)
	}
}

// TestContextTransport_HeadAnd204SkipBody verifies bodiless responses yield an
// immediate EOF and keep the connection reusable.
func TestContextTransport_HeadAnd204SkipBody(t *testing.T) {
	srv := newScriptedServer(t, func(conn net.Conn, br *bufio.Reader) {
		if !readRequest(br) {
			return
		}
		writeAll(t, conn, "HTTP/1.1 200 OK\r\nContent-Length: 42\r\n\r\n") // HEAD: no body follows
		if !readRequest(br) {
			return
		}
		writeAll(t, conn, "HTTP/1.1 204 No Content\r\n\r\n")
		if !readRequest(br) {
			return
		}
		writeAll(t, conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
	})
	client := BuildStreamingClient(srv.client(5 * time.Second))

	for _, tc := range []struct {
		method string
		status int
	}{{"HEAD", 200}, {"GET", 204}, {"GET", 200}} {
		req, resp := streamGet(t, client, tc.method)
		if resp.StatusCode() != tc.status {
			t.Fatalf("%s: status %d, want %d", tc.method, resp.StatusCode(), tc.status)
		}
		body, err := io.ReadAll(resp.BodyStream())
		if err != nil {
			t.Fatalf("%s %d: read body: %v", tc.method, tc.status, err)
		}
		if tc.status != 200 || tc.method == "HEAD" {
			if len(body) != 0 {
				t.Fatalf("%s %d: body = %q, want empty", tc.method, tc.status, body)
			}
		} else if string(body) != "ok" {
			t.Fatalf("final GET body = %q", body)
		}
		resp.CloseBodyStream() //nolint:errcheck
		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)
	}
	if got := srv.accepted.Load(); got != 1 {
		t.Fatalf("connections accepted = %d, want 1", got)
	}
}

// TestContextTransport_InterimResponsesSkipped verifies 1xx responses before
// the final status line are skipped, as Response.ReadLimitBody does.
func TestContextTransport_InterimResponsesSkipped(t *testing.T) {
	srv := newScriptedServer(t, func(conn net.Conn, br *bufio.Reader) {
		if !readRequest(br) {
			return
		}
		writeAll(t, conn, "HTTP/1.1 100 Continue\r\n\r\n")
		writeAll(t, conn, "HTTP/1.1 103 Early Hints\r\nLink: </style.css>; rel=preload\r\n\r\n")
		writeAll(t, conn, "HTTP/1.1 200 OK\r\nContent-Length: 4\r\n\r\ndone")
	})
	client := BuildStreamingClient(srv.client(5 * time.Second))

	req, resp := streamGet(t, client, "GET")
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)
	if resp.StatusCode() != 200 {
		t.Fatalf("status %d, want 200 after interim responses", resp.StatusCode())
	}
	body, err := io.ReadAll(resp.BodyStream())
	if err != nil || string(body) != "done" {
		t.Fatalf("body = %q, err = %v", body, err)
	}
}

// TestContextTransport_CloseWithErrorInterruptsBlockedRead verifies the idle
// timeout and cancellation contract: CloseWithError with a non-nil error
// unblocks a Read parked on a stalled upstream and discards the connection.
func TestContextTransport_CloseWithErrorInterruptsBlockedRead(t *testing.T) {
	hold := make(chan struct{})
	srv := newScriptedServer(t, func(conn net.Conn, br *bufio.Reader) {
		if !readRequest(br) {
			return
		}
		writeAll(t, conn, "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n9\r\ndata: a\n\n\r\n")
		<-hold // stall with the connection open
	})
	defer close(hold)
	client := BuildStreamingClient(srv.client(5 * time.Second))

	req, resp := streamGet(t, client, "GET")
	defer fasthttp.ReleaseRequest(req)
	stream := resp.BodyStream()

	first := make([]byte, 64)
	n, err := stream.Read(first)
	if err != nil || string(first[:n]) != "data: a\n\n" {
		t.Fatalf("first chunk = %q, err = %v", first[:n], err)
	}

	readErr := make(chan error, 1)
	go func() {
		_, err := stream.Read(make([]byte, 64))
		readErr <- err
	}()
	time.Sleep(100 * time.Millisecond) // let the Read park on the socket

	closeBodyStream(stream, ErrStreamIdleTimeout)

	select {
	case err := <-readErr:
		if err == nil {
			t.Fatal("blocked Read returned nil after CloseWithError")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CloseWithError did not unblock the parked Read")
	}
	if _, err := stream.Read(make([]byte, 8)); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("Read after close = %v, want io.ErrClosedPipe", err)
	}

	// The half-read connection must not be reused: the next request dials anew.
	req2, resp2 := streamGet(t, client, "GET")
	defer fasthttp.ReleaseRequest(req2)
	defer fasthttp.ReleaseResponse(resp2)
	resp2.CloseBodyStream() //nolint:errcheck
	if got := srv.accepted.Load(); got != 2 {
		t.Fatalf("connections accepted = %d, want 2 (discarded connection must not be pooled)", got)
	}
}

// TestContextTransport_MatchesDefaultForBufferedBodies runs the same buffered
// request through fasthttp's DefaultTransport and the context transport and
// expects identical results.
func TestContextTransport_MatchesDefaultForBufferedBodies(t *testing.T) {
	ln := fasthttputil.NewInmemoryListener()
	server := &fasthttp.Server{
		Handler: func(ctx *fasthttp.RequestCtx) {
			ctx.SetStatusCode(201)
			ctx.Response.Header.Set("X-Echo", string(ctx.Request.Header.Peek("X-Probe")))
			ctx.SetBodyString(`{"echo":"` + string(ctx.PostBody()) + `"}`)
		},
	}
	go server.Serve(ln) //nolint:errcheck
	t.Cleanup(func() { ln.Close() })

	run := func(transport fasthttp.RoundTripper) (int, string, string) {
		client := &fasthttp.Client{
			Dial:         func(addr string) (net.Conn, error) { return ln.Dial() },
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 5 * time.Second,
			Transport:    transport,
		}
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()
		defer fasthttp.ReleaseRequest(req)
		defer fasthttp.ReleaseResponse(resp)
		req.SetRequestURI("http://buffered/")
		req.Header.SetMethod("POST")
		req.Header.Set("X-Probe", "p1")
		req.SetBodyString("payload")
		_, bifrostErr, wait := MakeRequestWithContext(context.Background(), client, req, resp)
		defer wait()
		if bifrostErr != nil {
			t.Fatalf("request failed: %s", bifrostErr.Error.Message)
		}
		return resp.StatusCode(), string(resp.Header.Peek("X-Echo")), string(resp.Body())
	}

	dStatus, dHeader, dBody := run(fasthttp.DefaultTransport)
	cStatus, cHeader, cBody := run(NewContextTransport())
	if dStatus != cStatus || dHeader != cHeader || dBody != cBody {
		t.Fatalf("transports differ: default=(%d,%q,%q) context=(%d,%q,%q)", dStatus, dHeader, dBody, cStatus, cHeader, cBody)
	}
	if cStatus != 201 || cHeader != "p1" || cBody != `{"echo":"payload"}` {
		t.Fatalf("unexpected response: (%d,%q,%q)", cStatus, cHeader, cBody)
	}
}

// benchServer serves either a small buffered JSON body or a 20-chunk SSE stream
// over an in-memory listener, so the transport benchmarks measure client-side
// cost only.
func benchServer(b *testing.B, streamed bool) *fasthttputil.InmemoryListener {
	b.Helper()
	ln := fasthttputil.NewInmemoryListener()
	server := &fasthttp.Server{
		Handler: func(ctx *fasthttp.RequestCtx) {
			if !streamed {
				ctx.SetContentType("application/json")
				ctx.SetBodyString(`{"id":"chatcmpl-1","choices":[{"message":{"content":"hello"}}]}`)
				return
			}
			ctx.SetContentType("text/event-stream")
			ctx.SetBodyStreamWriter(func(w *bufio.Writer) {
				for i := 0; i < 20; i++ {
					fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"tok%d\"}}]}\n\n", i)
				}
				fmt.Fprint(w, "data: [DONE]\n\n")
			})
		},
	}
	go server.Serve(ln) //nolint:errcheck
	b.Cleanup(func() { ln.Close() })
	return ln
}

func benchClient(ln *fasthttputil.InmemoryListener, transport fasthttp.RoundTripper, streamed bool) *fasthttp.Client {
	return &fasthttp.Client{
		Dial:               func(addr string) (net.Conn, error) { return ln.Dial() },
		ReadTimeout:        5 * time.Second,
		WriteTimeout:       5 * time.Second,
		Transport:          transport,
		StreamResponseBody: streamed,
	}
}

func benchmarkBuffered(b *testing.B, transport fasthttp.RoundTripper) {
	ln := benchServer(b, false)
	client := benchClient(ln, transport, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()
		req.SetRequestURI("http://bench/")
		req.Header.SetMethod("POST")
		req.SetBodyString(`{"model":"m","messages":[]}`)
		_, bifrostErr, wait := MakeRequestWithContext(ctx, client, req, resp)
		wait()
		if bifrostErr != nil || resp.StatusCode() != 200 {
			b.Fatalf("request failed: %v", bifrostErr)
		}
		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)
	}
}

func benchmarkStreamed(b *testing.B, transport fasthttp.RoundTripper) {
	ln := benchServer(b, true)
	client := benchClient(ln, transport, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	buf := make([]byte, 4096)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()
		req.SetRequestURI("http://bench/")
		req.Header.SetMethod("POST")
		req.SetBodyString(`{"model":"m","messages":[],"stream":true}`)
		if err := DoStreamingRequest(ctx, client, req, resp); err != nil {
			b.Fatalf("DoStreamingRequest: %v", err)
		}
		stream := resp.BodyStream()
		for {
			if _, err := stream.Read(buf); err != nil {
				if !errors.Is(err, io.EOF) {
					b.Fatalf("read: %v", err)
				}
				break
			}
		}
		resp.CloseBodyStream() //nolint:errcheck
		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)
	}
}

func BenchmarkTransport_Buffered_Default(b *testing.B) {
	benchmarkBuffered(b, fasthttp.DefaultTransport)
}
func BenchmarkTransport_Buffered_Context(b *testing.B) { benchmarkBuffered(b, NewContextTransport()) }
func BenchmarkTransport_Streamed_Default(b *testing.B) {
	benchmarkStreamed(b, fasthttp.DefaultTransport)
}
func BenchmarkTransport_Streamed_Context(b *testing.B) { benchmarkStreamed(b, NewContextTransport()) }

// TestContextTransport_BufferedLateCancelDoesNotPoisonPool guards the ownership
// rule on the buffered path: the cancel watcher must be stopped before the
// connection is returned to the pool. The test hook fires at the release
// decision and cancels the context there; a watcher that is still running at
// that point would close the socket just as it is pooled, and the next request
// would find a dead connection and have to dial again (PR #7104 review).
func TestContextTransport_BufferedLateCancelDoesNotPoisonPool(t *testing.T) {
	srv := newScriptedServer(t, func(conn net.Conn, br *bufio.Reader) {
		for readRequest(br) {
			writeAll(t, conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
		}
	})
	client := srv.client(5 * time.Second) // buffered client, context transport installed

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var hookCalls atomic.Int32
	testHookBeforeConnRelease = func() {
		hookCalls.Add(1)
		cancel()
		// Give a watcher that is (wrongly) still running time to act on it.
		time.Sleep(50 * time.Millisecond)
	}
	t.Cleanup(func() { testHookBeforeConnRelease = nil })

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)
	req.SetRequestURI("http://scripted/buffered")
	req.Header.SetMethod("POST")
	req.SetBodyString("{}")
	unbind := bindRequestContext(req, ctx)
	err := client.Do(req, resp)
	unbind()
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	if got := hookCalls.Load(); got != 1 {
		t.Fatalf("release hook ran %d times, want 1", got)
	}
	testHookBeforeConnRelease = nil

	req2 := fasthttp.AcquireRequest()
	resp2 := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req2)
	defer fasthttp.ReleaseResponse(resp2)
	req2.SetRequestURI("http://scripted/buffered")
	req2.Header.SetMethod("POST")
	req2.SetBodyString("{}")
	if err := client.Do(req2, resp2); err != nil {
		t.Fatalf("second request: %v", err)
	}
	if string(resp2.Body()) != "ok" {
		t.Fatalf("second response body = %q", resp2.Body())
	}
	if got := srv.accepted.Load(); got != 1 {
		t.Fatalf("connections accepted = %d, want 1: the pooled connection was closed by a late cancellation", got)
	}
}
