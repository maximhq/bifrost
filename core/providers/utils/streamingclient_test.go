package utils

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// TestBuildStreamingClient_KeepsHeaderTimeouts verifies the streaming client
// keeps the base ReadTimeout / WriteTimeout and carries a Transport, while
// preserving other config from the base. The timeouts bound dial, TLS
// handshake, request write and the wait for response headers only; the
// transport lifts them once headers arrive (issue #7034). MaxConnDuration is
// covered separately by TestBuildStreamingClient_PreservesMaxConnDuration.
func TestBuildStreamingClient_KeepsHeaderTimeouts(t *testing.T) {
	base := &fasthttp.Client{
		ReadTimeout:        30 * time.Second,
		WriteTimeout:       30 * time.Second,
		MaxConnDuration:    5 * time.Minute,
		MaxConnWaitTimeout: 15 * time.Second,
		MaxConnsPerHost:    123,
	}
	ConfigureDialer(base, false)

	stream := BuildStreamingClient(base)

	if stream.ReadTimeout != base.ReadTimeout {
		t.Errorf("ReadTimeout: got %v, want %v (header wait must be bounded)", stream.ReadTimeout, base.ReadTimeout)
	}
	if stream.WriteTimeout != base.WriteTimeout {
		t.Errorf("WriteTimeout: got %v, want %v (request write must be bounded)", stream.WriteTimeout, base.WriteTimeout)
	}
	if stream.Transport == nil {
		t.Error("Transport: got nil, want the context-aware transport that lifts the deadline after headers")
	}
	if !stream.StreamResponseBody {
		t.Error("StreamResponseBody: got false, want true")
	}
	if stream.MaxConnWaitTimeout != base.MaxConnWaitTimeout {
		t.Errorf("MaxConnWaitTimeout should be preserved: got %v, want %v",
			stream.MaxConnWaitTimeout, base.MaxConnWaitTimeout)
	}
	if stream.MaxConnsPerHost != base.MaxConnsPerHost {
		t.Errorf("MaxConnsPerHost should be preserved: got %v, want %v",
			stream.MaxConnsPerHost, base.MaxConnsPerHost)
	}
}

// TestBuildStreamingClient_BaseUnchanged verifies BuildStreamingClient does not
// mutate the base client (since unary callers still need the 30s timeout).
func TestBuildStreamingClient_BaseUnchanged(t *testing.T) {
	base := &fasthttp.Client{
		ReadTimeout:     30 * time.Second,
		WriteTimeout:    30 * time.Second,
		MaxConnDuration: 5 * time.Minute,
	}
	_ = BuildStreamingClient(base)

	if base.ReadTimeout != 30*time.Second {
		t.Errorf("base ReadTimeout mutated: got %v, want 30s", base.ReadTimeout)
	}
	if base.MaxConnDuration != 5*time.Minute {
		t.Errorf("base MaxConnDuration mutated: got %v, want 5m", base.MaxConnDuration)
	}
}

// TestBuildStreamingClient_LongStreamSurvives verifies that a stream sending
// chunks every 500ms for 2.5s (total) is not killed by the base client's 1s
// ReadTimeout. The request goes through DoStreamingRequest, which is the
// contract for streaming clients: the ReadTimeout bounds the wait for
// response headers and is lifted once they arrive, so it can never cut a
// healthy body.
func TestBuildStreamingClient_LongStreamSurvives(t *testing.T) {
	const chunkInterval = 500 * time.Millisecond
	const totalChunks = 5 // 2.5s total, well past base ReadTimeout=1s

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for i := 0; i < totalChunks; i++ {
			fmt.Fprintf(w, "data: chunk-%d\n\n", i)
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(chunkInterval)
		}
	}))
	defer srv.Close()

	base := &fasthttp.Client{
		ReadTimeout:  1 * time.Second, // would abort the stream without the fix
		WriteTimeout: 1 * time.Second,
	}
	ConfigureDialer(base, false)
	stream := BuildStreamingClient(base)

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(srv.URL)
	req.Header.SetMethod(http.MethodGet)
	resp.StreamBody = true

	if err := DoStreamingRequest(context.Background(), stream, req, resp); err != nil {
		t.Fatalf("DoStreamingRequest: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode())
	}

	scanner := bufio.NewScanner(resp.BodyStream())
	got := 0
	for scanner.Scan() {
		if line := scanner.Text(); len(line) >= 5 && line[:5] == "data:" {
			got++
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanner: %v", err)
	}
	if got != totalChunks {
		t.Errorf("chunks received: got %d, want %d (stream was likely killed early)", got, totalChunks)
	}
}

// TestBuildStreamingHTTPClient_ZerosTimeout verifies the net/http streaming
// client has Timeout=0 and shares the base's Transport.
func TestBuildStreamingHTTPClient_ZerosTimeout(t *testing.T) {
	transport := &http.Transport{ResponseHeaderTimeout: 10 * time.Second}
	base := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
	}

	stream := BuildStreamingHTTPClient(base)

	if stream.Timeout != 0 {
		t.Errorf("Timeout: got %v, want 0", stream.Timeout)
	}
	if stream.Transport != base.Transport {
		t.Error("Transport: streaming client should share base's Transport")
	}
	if base.Timeout != 30*time.Second {
		t.Errorf("base Timeout mutated: got %v, want 30s", base.Timeout)
	}
}

// TestBuildStreamingHTTPClient_Nil verifies nil base returns empty client
// (not a panic).
func TestBuildStreamingHTTPClient_Nil(t *testing.T) {
	stream := BuildStreamingHTTPClient(nil)
	if stream == nil {
		t.Fatal("BuildStreamingHTTPClient(nil) returned nil")
	}
	if stream.Timeout != 0 {
		t.Errorf("Timeout: got %v, want 0", stream.Timeout)
	}
}

// TestBuildStreamingHTTPClient_LongStreamSurvives verifies that the streaming
// client can read a response body that takes longer than the base client's
// Timeout — proving Timeout=0 actually lifts the whole-request deadline.
func TestBuildStreamingHTTPClient_LongStreamSurvives(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for i := 0; i < 4; i++ {
			fmt.Fprintf(w, "data: chunk-%d\n\n", i)
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(400 * time.Millisecond)
		}
	}))
	defer srv.Close()

	base := &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			ResponseHeaderTimeout: 5 * time.Second,
		},
		Timeout: 500 * time.Millisecond, // would abort the stream without the fix
	}
	stream := BuildStreamingHTTPClient(base)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	resp, err := stream.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	got := 0
	for scanner.Scan() {
		if line := scanner.Text(); len(line) >= 5 && line[:5] == "data:" {
			got++
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanner: %v", err)
	}
	if got != 4 {
		t.Errorf("chunks received: got %d, want 4 (stream was likely killed by Timeout)", got)
	}
}

// TestBuildStreamingClient_PreservesMaxConnDuration pins the one property that
// lets a poisoned streaming pool recover without a process restart.
//
// MaxConnDuration is checked once per request, before the request is written
// (fasthttp client.go:3110): an over-age connection gets Connection: close set
// on the outgoing request, which makes the streaming close callback take
// CloseConn instead of ReleaseConn. It therefore cannot cut a live stream, and
// it is the only age-based eviction the streaming pool has. Zeroing it left a
// bad connection in HostClient.conns indefinitely, because the idle cleaner only
// evicts on lastUseTime and a connection that keeps being handed out never goes
// idle. ReadTimeout and WriteTimeout are kept as well: the transport applies
// them to the header phase only and lifts them before the body streams.
func TestBuildStreamingClient_PreservesMaxConnDuration(t *testing.T) {
	base := &fasthttp.Client{
		ReadTimeout:     30 * time.Second,
		WriteTimeout:    30 * time.Second,
		MaxConnDuration: 5 * time.Minute,
	}
	ConfigureDialer(base, false)

	stream := BuildStreamingClient(base)

	if stream.MaxConnDuration != base.MaxConnDuration {
		t.Errorf("MaxConnDuration: got %v, want %v (streaming connections must still age out)",
			stream.MaxConnDuration, base.MaxConnDuration)
	}
	if stream.ReadTimeout != base.ReadTimeout {
		t.Errorf("ReadTimeout: got %v, want %v", stream.ReadTimeout, base.ReadTimeout)
	}
	if stream.WriteTimeout != base.WriteTimeout {
		t.Errorf("WriteTimeout: got %v, want %v", stream.WriteTimeout, base.WriteTimeout)
	}
}

// silentUpstream accepts TCP connections, consumes whatever the client sends and
// never writes a byte back. It models Failover Bench scenario S06 from issue
// #7034: the upstream is alive, the connection is established, and no response
// headers ever arrive. Every accepted connection is closed on test cleanup so a
// goroutine that is still blocked on it (the pre-fix behaviour) can exit.
type silentUpstream struct {
	ln         net.Listener
	mu         sync.Mutex
	conns      []net.Conn
	accepted   atomic.Int32
	acceptedCh chan struct{} // one send per accepted connection
	hangups    chan struct{} // one send per accepted connection the client closed
}

// awaitAccept blocks until the upstream has accepted a connection, so a test
// asserts on the accepted-connection header wait rather than on a connect that
// is still queued. Safe from any goroutine; returns false on timeout.
func (s *silentUpstream) awaitAccept(timeout time.Duration) bool {
	select {
	case <-s.acceptedCh:
		return true
	case <-time.After(timeout):
		return false
	}
}

func (s *silentUpstream) waitAccepted(t *testing.T) {
	t.Helper()
	if !s.awaitAccept(2 * time.Second) {
		t.Fatal("upstream never accepted the client connection")
	}
}

func newSilentUpstream(t *testing.T) *silentUpstream {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &silentUpstream{ln: ln, acceptedCh: make(chan struct{}, 64), hangups: make(chan struct{}, 64)}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			s.accepted.Add(1)
			s.mu.Lock()
			s.conns = append(s.conns, conn)
			s.mu.Unlock()
			select {
			case s.acceptedCh <- struct{}{}:
			default:
			}
			go func() {
				buf := make([]byte, 4096)
				for {
					if _, err := conn.Read(buf); err != nil {
						// Read only fails once the client hangs up or cleanup closes
						// the socket; the server itself never writes.
						select {
						case s.hangups <- struct{}{}:
						default:
						}
						return
					}
				}
			}()
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, c := range s.conns {
			c.Close()
		}
	})
	return s
}

func (s *silentUpstream) URL() string { return "http://" + s.ln.Addr().String() }

func newSilentStreamingRequest(target string) (*fasthttp.Request, *fasthttp.Response) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	req.SetRequestURI(target + "/v1/chat/completions")
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	req.SetBodyString(`{"model":"fb-s06-no-response","stream":true}`)
	return req, resp
}

// TestDoStreamingRequest_SilentUpstreamTimesOutAtHeaderWait is the regression
// test for issue #7034. A streaming request to an upstream that accepts the
// connection and never sends headers must fail with fasthttp.ErrTimeout once
// the client's ReadTimeout (default_request_timeout_in_seconds) elapses, so the
// core retry/fallback loop regains control. Before the fix the streaming client
// had ReadTimeout=0 and DoStreamingRequest blocked until the upstream closed.
func TestDoStreamingRequest_SilentUpstreamTimesOutAtHeaderWait(t *testing.T) {
	upstream := newSilentUpstream(t)

	base := &fasthttp.Client{
		ReadTimeout:  300 * time.Millisecond,
		WriteTimeout: 300 * time.Millisecond,
	}
	ConfigureDialer(base, true)
	stream := BuildStreamingClient(base)

	req, resp := newSilentStreamingRequest(upstream.URL())

	errCh := make(chan error, 1)
	start := time.Now()
	go func() { errCh <- DoStreamingRequest(context.Background(), stream, req, resp) }()
	upstream.waitAccepted(t)

	select {
	case err := <-errCh:
		elapsed := time.Since(start)
		if !errors.Is(err, fasthttp.ErrTimeout) {
			t.Fatalf("DoStreamingRequest error = %v, want fasthttp.ErrTimeout", err)
		}
		if elapsed > 2*time.Second {
			t.Fatalf("timed out after %v, want roughly the 300ms ReadTimeout", elapsed)
		}
		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)
	case <-time.After(3 * time.Second):
		t.Fatalf("header wait is unbounded: DoStreamingRequest did not return within 3s while the upstream stayed silent (accepted %d conn(s)); issue #7034", upstream.accepted.Load())
	}
}

// TestDoStreamingRequest_CtxCancelClosesSocketDuringHeaderWait verifies that
// cancelling the request context while waiting for response headers unblocks
// DoStreamingRequest promptly and closes the upstream socket, instead of
// leaving a goroutine parked on the read until the client's ReadTimeout.
func TestDoStreamingRequest_CtxCancelClosesSocketDuringHeaderWait(t *testing.T) {
	upstream := newSilentUpstream(t)

	base := &fasthttp.Client{
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	ConfigureDialer(base, true)
	stream := BuildStreamingClient(base)

	req, resp := newSilentStreamingRequest(upstream.URL())

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	start := time.Now()
	go func() { errCh <- DoStreamingRequest(ctx, stream, req, resp) }()
	upstream.waitAccepted(t) // cancel lands on an accepted connection in its header wait
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("DoStreamingRequest error = %v, want context.Canceled", err)
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("cancellation took %v to unblock the header wait", elapsed)
		}
		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)
	case <-time.After(3 * time.Second):
		t.Fatal("context cancellation does not reach the socket: DoStreamingRequest still blocked 3s after cancel()")
	}

	select {
	case <-upstream.hangups:
		// The client closed its end of the connection: nothing is leaked.
	case <-time.After(2 * time.Second):
		t.Fatal("upstream never observed the client hanging up: the cancelled request's socket is still open")
	}
}

// TestBuildLargeResponseClient_SilentUpstreamTimesOutAtHeaderWait covers the
// large-response variant of #7034: PrepareResponseStreaming swaps in a client
// built by BuildLargeResponseClient, which also used to zero ReadTimeout, so a
// unary request with a large-response threshold hung on a silent upstream.
func TestBuildLargeResponseClient_SilentUpstreamTimesOutAtHeaderWait(t *testing.T) {
	upstream := newSilentUpstream(t)

	base := &fasthttp.Client{
		ReadTimeout:  300 * time.Millisecond,
		WriteTimeout: 300 * time.Millisecond,
	}
	ConfigureDialer(base, true)

	req, resp := newSilentStreamingRequest(upstream.URL())

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyLargeResponseThreshold, int64(1024))
	active := PrepareResponseStreaming(ctx, base, resp)
	if active == base {
		t.Fatal("PrepareResponseStreaming did not build a large-response client")
	}

	type result struct {
		err  *schemas.BifrostError
		wait func()
	}
	resCh := make(chan result, 1)
	start := time.Now()
	go func() {
		_, bifrostErr, wait := MakeRequestWithContext(ctx, active, req, resp)
		resCh <- result{err: bifrostErr, wait: wait}
	}()
	upstream.waitAccepted(t)

	select {
	case res := <-resCh:
		res.wait()
		if res.err == nil {
			t.Fatal("expected a timeout error from a silent upstream")
		}
		if res.err.Error.Type == nil || *res.err.Error.Type != schemas.RequestTimedOut {
			t.Fatalf("error type = %v, want RequestTimedOut", res.err.Error.Type)
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("timed out after %v, want roughly the 300ms ReadTimeout", elapsed)
		}
		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)
	case <-time.After(3 * time.Second):
		t.Fatal("header wait is unbounded on the large-response client: MakeRequestWithContext did not return within 3s")
	}
}

// TestBuildLargeResponseClient_HeaderDeadlineDoesNotCapBody is the guard for
// the opposite direction: once headers have arrived, a large body that takes
// longer than ReadTimeout to download must still be readable to the end.
func TestBuildLargeResponseClient_HeaderDeadlineDoesNotCapBody(t *testing.T) {
	const chunks = 5
	const chunkInterval = 400 * time.Millisecond // 2s total, past the 1s ReadTimeout
	payload := make([]byte, 2048)
	for i := range payload {
		payload[i] = 'x'
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)*chunks))
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for range chunks {
			w.Write(payload) //nolint:errcheck
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(chunkInterval)
		}
	}))
	defer srv.Close()

	base := &fasthttp.Client{
		ReadTimeout:  1 * time.Second,
		WriteTimeout: 1 * time.Second,
	}
	ConfigureDialer(base, true)

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	req.SetRequestURI(srv.URL)
	req.Header.SetMethod(http.MethodGet)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyLargeResponseThreshold, int64(1024))
	active := PrepareResponseStreaming(ctx, base, resp)

	_, bifrostErr, wait := MakeRequestWithContext(ctx, active, req, resp)
	defer wait()
	if bifrostErr != nil {
		t.Fatalf("MakeRequestWithContext: %s", bifrostErr.Error.Message)
	}
	defer ReleaseStreamingResponse(ctx, resp)
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode())
	}
	body := resp.BodyStream()
	if body == nil {
		t.Fatal("expected a streamed body on the large-response client")
	}
	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("reading body: %v (stream was likely killed by ReadTimeout)", err)
	}
	if len(got) != len(payload)*chunks {
		t.Fatalf("body bytes: got %d, want %d", len(got), len(payload)*chunks)
	}
}

// stallingLargeServer advertises a large Content-Length, writes prelude bytes,
// flushes, then holds the connection open without writing anything else until
// the test ends. It models an upstream that stalls mid-body on a unary
// large-response download.
func stallingLargeServer(t *testing.T, contentLength, prelude int) *httptest.Server {
	t.Helper()
	hold := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", fmt.Sprint(contentLength))
		w.WriteHeader(http.StatusOK)
		chunk := make([]byte, prelude)
		for i := range chunk {
			chunk[i] = 'x'
		}
		w.Write(chunk) //nolint:errcheck
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-hold
	}))
	// Cleanups run last-in first-out: release the handler before srv.Close waits
	// for it, otherwise a test that failed while the upstream was stalled hangs.
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(hold) })
	return srv
}

// largeResponseRequest returns a context with the large-response threshold and a
// short stream idle timeout, and performs the unary request through the
// large-response client the way a provider does.
func largeResponseRequest(t *testing.T, srv *httptest.Server, idle time.Duration) (*schemas.BifrostContext, *fasthttp.Response, func()) {
	t.Helper()
	base := &fasthttp.Client{ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second}
	ConfigureDialer(base, true)

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	req.SetRequestURI(srv.URL)
	req.Header.SetMethod(http.MethodGet)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyLargeResponseThreshold, int64(1024))
	ctx.SetValue(schemas.BifrostContextKeyStreamIdleTimeout, idle)
	active := PrepareResponseStreaming(ctx, base, resp)

	_, bifrostErr, wait := MakeRequestWithContext(ctx, active, req, resp)
	wait()
	if bifrostErr != nil {
		t.Fatalf("MakeRequestWithContext: %s", bifrostErr.Error.Message)
	}
	return ctx, resp, func() { fasthttp.ReleaseRequest(req) }
}

// TestFinalizeResponseWithLargeDetection_StallDuringPrefetchReturnsError covers
// an upstream that stalls before the 64KB prefetch completes. The prefetch runs
// inside the provider call, so without an idle bound the provider worker is
// pinned for as long as the upstream keeps the socket open (PR #7104 review).
func TestFinalizeResponseWithLargeDetection_StallDuringPrefetchReturnsError(t *testing.T) {
	srv := stallingLargeServer(t, 256*1024, 10) // 10 bytes, then silence
	ctx, resp, release := largeResponseRequest(t, srv, 300*time.Millisecond)
	defer release()

	type result struct {
		isLarge bool
		err     *schemas.BifrostError
	}
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		_, isLarge, err := FinalizeResponseWithLargeDetection(ctx, resp, nil)
		done <- result{isLarge: isLarge, err: err}
	}()
	select {
	case res := <-done:
		if res.isLarge || res.err == nil {
			t.Fatalf("expected a decode error from the stalled prefetch, got isLarge=%v err=%v", res.isLarge, res.err)
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("prefetch stall took %v to surface, want roughly the 300ms idle timeout", elapsed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("large-response prefetch has no idle bound: FinalizeResponseWithLargeDetection did not return within 3s on a stalled upstream")
	}
}

// TestFinalizeResponseWithLargeDetection_StallAfterPrefetchUnblocksReader covers
// an upstream that stalls after the prefetch, while the transport is draining
// the LargeResponseReader to the client. The reader must fail with
// ErrStreamIdleTimeout instead of blocking the transport writer forever, and
// Close must not hang afterwards.
func TestFinalizeResponseWithLargeDetection_StallAfterPrefetchUnblocksReader(t *testing.T) {
	srv := stallingLargeServer(t, 256*1024, 96*1024) // more than the 64KB prefetch, then silence
	ctx, resp, release := largeResponseRequest(t, srv, 300*time.Millisecond)
	defer release()

	_, isLarge, bifrostErr := FinalizeResponseWithLargeDetection(ctx, resp, nil)
	if bifrostErr != nil {
		t.Fatalf("FinalizeResponseWithLargeDetection: %s", bifrostErr.Error.Message)
	}
	if !isLarge {
		t.Fatal("expected large-response mode")
	}
	reader, ok := ctx.Value(schemas.BifrostContextKeyLargeResponseReader).(io.ReadCloser)
	if !ok || reader == nil {
		t.Fatal("large response reader missing from context")
	}

	readErr := make(chan error, 1)
	start := time.Now()
	go func() {
		buf := make([]byte, 32*1024)
		for {
			if _, err := reader.Read(buf); err != nil {
				readErr <- err
				return
			}
		}
	}()
	select {
	case err := <-readErr:
		if !errors.Is(err, ErrStreamIdleTimeout) {
			t.Fatalf("reader error = %v, want ErrStreamIdleTimeout", err)
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("stall took %v to surface, want roughly the 300ms idle timeout", elapsed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("large-response reader has no idle bound: Read did not return within 3s on a stalled upstream")
	}

	closed := make(chan struct{})
	go func() {
		reader.Close() //nolint:errcheck
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("LargeResponseReader.Close hung after the idle timeout closed the stream")
	}
}

// TestFinalizeResponseWithLargeDetection_GzipClassifiedByDecompressedSize guards
// the size bound against compressed bodies: Content-Length describes the
// compressed bytes, so a gzip response that is small on the wire but large once
// decompressed must still enter large-response mode instead of being
// materialized in full through the "known small" branch (PR #7104 review).
func TestFinalizeResponseWithLargeDetection_GzipClassifiedByDecompressedSize(t *testing.T) {
	plain := bytes.Repeat([]byte("a"), 200*1024)
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write(plain); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	if compressed.Len() > 1024 {
		t.Fatalf("fixture compressed to %d bytes, want under the 1KB threshold", compressed.Len())
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Length", fmt.Sprint(compressed.Len()))
		w.WriteHeader(http.StatusOK)
		w.Write(compressed.Bytes()) //nolint:errcheck
	}))
	defer srv.Close()

	ctx, resp, release := largeResponseRequest(t, srv, 5*time.Second)
	defer release()

	body, isLarge, bifrostErr := FinalizeResponseWithLargeDetection(ctx, resp, nil)
	if bifrostErr != nil {
		t.Fatalf("FinalizeResponseWithLargeDetection: %s", bifrostErr.Error.Message)
	}
	if !isLarge {
		t.Fatalf("gzip response of %d compressed bytes decompressing to %d bytes was buffered in full (len(body)=%d) past the 1KB threshold; large-response mode must be decided by decompressed size", compressed.Len(), len(plain), len(body))
	}
	reader, ok := ctx.Value(schemas.BifrostContextKeyLargeResponseReader).(io.ReadCloser)
	if !ok || reader == nil {
		t.Fatal("large response reader missing from context")
	}
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("reading large response: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("closing large response: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("decompressed body: got %d bytes, want %d", len(got), len(plain))
	}
}
