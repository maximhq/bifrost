package utils

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// errNonSSE mirrors the error the provider streaming handlers construct when
// DrainNonSSEStreamReader reports a drain. Kept as a sentinel so the tests below
// can distinguish "the pool handed us a corrupt stream" from any other failure.
var errNonSSE = errors.New("provider returned non-SSE response for streaming request")

// newSSETestServer starts a loopback server that emits `chunks` SSE frames at
// `interval`, so a test can land a cancellation mid-body deterministically.
func newSSETestServer(t *testing.T, chunks int, interval time.Duration) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			return
		}
		flusher.Flush()
		for i := range chunks {
			if _, err := fmt.Fprintf(w, "data: {\"i\":%d}\n\n", i); err != nil {
				return
			}
			flusher.Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(interval):
			}
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	t.Cleanup(srv.Close)
	return srv
}

// connCounter tracks how many dialed sockets are still open. fasthttp does not
// expose its per-host HostClient through *fasthttp.Client, so counting at the
// dialer is both reachable and a more direct measure of a connection leak than
// HostClient.ConnsCount would be.
type connCounter struct {
	mu     sync.Mutex
	dialed int
	closed int
}

func (c *connCounter) open() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dialed - c.closed
}

func (c *connCounter) totalDialed() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dialed
}

// countedConn decrements exactly once however many times Close is called, so a
// double close shows up as a leak rather than as a negative count.
type countedConn struct {
	net.Conn
	counter *connCounter
	once    sync.Once
}

func (c *countedConn) Close() error {
	c.once.Do(func() {
		c.counter.mu.Lock()
		c.counter.closed++
		c.counter.mu.Unlock()
	})
	return c.Conn.Close()
}

// newTestStreamingClient builds the same client pair a provider constructor
// builds (see NewOpenAIProvider) and returns the streaming half, with every
// dialed socket counted.
func newTestStreamingClient(maxConnsPerHost int) (*fasthttp.Client, *connCounter) {
	base := &fasthttp.Client{
		ReadTimeout:         30 * time.Second,
		WriteTimeout:        30 * time.Second,
		MaxConnsPerHost:     maxConnsPerHost,
		MaxIdleConnDuration: 30 * time.Second,
		MaxConnWaitTimeout:  30 * time.Second,
		MaxConnDuration:     300 * time.Second,
		ConnPoolStrategy:    fasthttp.FIFO,
	}
	base = ConfigureDialer(base, false)

	counter := &connCounter{}
	inner := base.Dial
	base.Dial = func(addr string) (net.Conn, error) {
		conn, err := inner(addr)
		if err != nil {
			return nil, err
		}
		counter.mu.Lock()
		counter.dialed++
		counter.mu.Unlock()
		return &countedConn{Conn: conn, counter: counter}, nil
	}
	return BuildStreamingClient(base), counter
}

// runSSEStream issues one streaming request and consumes it, mirroring the
// structure and defer order of HandleOpenAIChatCompletionStreaming: same
// StreamBody setup, same DecompressStreamBody -> NewIdleTimeoutReader ->
// SetupStreamCancellation -> DrainNonSSEStreamReader chain, same LIFO release.
func runSSEStream(ctx *schemas.BifrostContext, client *fasthttp.Client, url string) (int, error) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true
	defer fasthttp.ReleaseRequest(req)

	req.Header.SetMethod(http.MethodPost)
	req.SetRequestURI(url)
	req.Header.SetContentType("application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	req.SetBodyString(`{"stream":true}`)

	if err := DoStreamingRequest(ctx, client, req, resp); err != nil {
		ReleaseStreamingResponse(ctx, resp)
		return 0, fmt.Errorf("request failed: %w", err)
	}
	if resp.StatusCode() != fasthttp.StatusOK {
		ReleaseStreamingResponse(ctx, resp)
		return 0, fmt.Errorf("unexpected status %d", resp.StatusCode())
	}

	defer ReleaseStreamingResponse(ctx, resp)
	reader, releaseGzip := DecompressStreamBody(resp)
	defer releaseGzip()
	reader, stopIdleTimeout := NewIdleTimeoutReader(reader, resp.BodyStream(), GetStreamIdleTimeout(ctx), ctx)
	defer stopIdleTimeout()
	stopCancellation := SetupStreamCancellation(ctx, resp.BodyStream(), getLogger())
	defer stopCancellation()

	reader, nonSSE := DrainNonSSEStreamReader(resp, reader)
	if nonSSE != nil {
		return 0, fmt.Errorf("%w: %v", errNonSSE, nonSSE.Kind)
	}

	// Use the production SSE reader, which stops at "data: [DONE]" rather than
	// consuming the body to EOF. That distinction matters: ReleaseStreamingResponse
	// drains resp.BodyStream() afterwards, and fasthttp's requestStream blocks if
	// it is read again after it has already returned io.EOF.
	sseReader := GetSSEDataReader(ctx, reader)
	count := 0
	for {
		if ctx.Err() != nil {
			return count, ctx.Err()
		}
		_, readErr := sseReader.ReadDataLine()
		if readErr != nil {
			if readErr != io.EOF {
				return count, readErr
			}
			break
		}
		count++
	}
	return count, nil
}

// cancelledStreamLoad runs `iterations` streaming requests at `concurrency`,
// cancelling each one mid-body. This is the load that poisons the pool.
func cancelledStreamLoad(client *fasthttp.Client, url string, iterations, concurrency int, cancelAfter time.Duration) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)
	for range iterations {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
			done := make(chan struct{})
			go func() {
				defer close(done)
				_, _ = runSSEStream(ctx, client, url)
			}()
			// Jitter the cancellation so it lands at varying points relative to a
			// blocked Read, rather than always at the same phase of the stream.
			time.Sleep(cancelAfter/2 + time.Duration(rand.Int64N(int64(cancelAfter))))
			cancel()
			<-done
		}()
	}
	wg.Wait()
}

// streamOutcome classifies one clean streaming attempt.
type streamOutcome struct {
	frames int
	err    error
	stuck  bool
}

// cleanStream runs one uncancelled stream and bounds it, so a desynced
// connection surfaces as a reported failure instead of parking the test.
// A healthy stream against the fixture server finishes well inside the bound.
func cleanStream(client *fasthttp.Client, url string, bound time.Duration) streamOutcome {
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	type res struct {
		frames int
		err    error
	}
	ch := make(chan res, 1)
	go func() {
		n, err := runSSEStream(ctx, client, url)
		ch <- res{n, err}
	}()

	select {
	case r := <-ch:
		return streamOutcome{frames: r.frames, err: r.err}
	case <-time.After(bound):
		// Unblock the parked reader so the goroutine does not outlive the test.
		cancel()
		return streamOutcome{stuck: true}
	}
}

// TestStreamingPoolSurvivesCancelledStreams is the reported bug stated as a
// test. It checks the two things a cancelled stream must never do to a healthy
// one, in the order they matter:
//
//  1. Concurrent: a stream running alongside cancelled traffic must complete.
//     This is the mechanism #6143's race makes possible - the cancelled request
//     returns a *requestStream to fasthttp's pool while its own reader is still
//     inside Read, so another in-flight request can acquire and alias it.
//  2. Persistent: once the cancelled traffic stops, clean streams must work
//     again. This is the reported symptom, where only a restart recovered.
//
// The control phase first proves the fixture itself is healthy, so any failure
// afterwards is attributable to the cancelled load rather than the harness.
func TestStreamingPoolSurvivesCancelledStreams(t *testing.T) {
	const (
		chunks       = 40
		chunkGap     = 1 * time.Millisecond
		streamBudget = 5 * time.Second // ~100x a healthy stream (40 * 1ms)
		cleanWorkers = 16
	)
	srv := newSSETestServer(t, chunks, chunkGap)
	client, _ := newTestStreamingClient(96)

	for attempt := range 5 {
		got := cleanStream(client, srv.URL, streamBudget)
		if got.stuck || got.err != nil || got.frames == 0 {
			t.Fatalf("control stream %d unhealthy before load (stuck=%v frames=%d err=%v)",
				attempt, got.stuck, got.frames, got.err)
		}
	}

	// Phase 1: clean streams running alongside cancelled ones.
	var loadDone sync.WaitGroup
	loadDone.Add(1)
	go func() {
		defer loadDone.Done()
		cancelledStreamLoad(client, srv.URL, 20000, 96, 4*time.Millisecond)
	}()

	var (
		mu       sync.Mutex
		failures []string
	)
	var cleanDone sync.WaitGroup
	for range cleanWorkers {
		cleanDone.Add(1)
		go func() {
			defer cleanDone.Done()
			for range 40 {
				got := cleanStream(client, srv.URL, streamBudget)
				if got.stuck || got.err != nil || got.frames != chunks {
					mu.Lock()
					failures = append(failures, fmt.Sprintf("stuck=%v frames=%d err=%v",
						got.stuck, got.frames, got.err))
					mu.Unlock()
				}
			}
		}()
	}
	cleanDone.Wait()
	loadDone.Wait()

	if len(failures) > 0 {
		t.Errorf("%d concurrent clean streams corrupted by cancelled traffic; first few: %v",
			len(failures), failures[:min(len(failures), 5)])
	}

	// Phase 2: the pool must still be usable once the cancelled traffic stops.
	time.Sleep(100 * time.Millisecond)
	var nonSSE, stuck, other int
	for range 100 {
		got := cleanStream(client, srv.URL, streamBudget)
		switch {
		case got.stuck:
			stuck++
		case errors.Is(got.err, errNonSSE):
			nonSSE++
		case got.err != nil || got.frames == 0:
			other++
		}
	}
	if nonSSE+stuck+other > 0 {
		t.Errorf("pool still poisoned after cancelled traffic stopped: %d non-SSE, %d stuck, %d other (of 100)",
			nonSSE, stuck, other)
	}
}

// TestStreamingConnsCountDoesNotDrift asserts the streaming client does not leak
// sockets across cancelled streams. Every dialed connection must eventually be
// closed once traffic stops.
func TestStreamingConnsCountDoesNotDrift(t *testing.T) {
	srv := newSSETestServer(t, 40, 5*time.Millisecond)
	const maxConns = 32
	client, counter := newTestStreamingClient(maxConns)

	cancelledStreamLoad(client, srv.URL, 1000, 16, 12*time.Millisecond)

	// Connections are reclaimed by fasthttp's idle cleaner once traffic stops,
	// which runs on a MaxIdleConnDuration cadence, so poll rather than assert
	// immediately.
	deadline := time.Now().Add(60 * time.Second)
	var open int
	for time.Now().Before(deadline) {
		open = counter.open()
		if open == 0 {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("%d of %d dialed connections still open after cancelled load settled",
		open, counter.totalDialed())
}

// TestStreamCancellationNoDataRace reproduces maximhq/bifrost#6143: cancelling a
// streaming request while the SSE reader is still inside (*requestStream).Read
// lets SetupStreamCancellation's watchdog run fasthttp's cleanup callback, which
// zeroes the *requestStream and returns it to requestStreamPool underneath the
// reader.
//
// The BifrostContextKeyConnectionClosed CAS only serializes closer against
// closer; the reader never participates in the claim, so it does not cover this.
//
// Meaningful only under -race: the race detector is the assertion. Per #6143 the
// repro is 40 SSE chunks at 50ms with cancellation landing mid-body.
func TestStreamCancellationNoDataRace(t *testing.T) {
	srv := newSSETestServer(t, 40, 50*time.Millisecond)
	client, _ := newTestStreamingClient(64)

	cancelledStreamLoad(client, srv.URL, 48, 8, 250*time.Millisecond)
}

// runSSEStreamWithHardClose mirrors runSSEStream but closes the raw body stream
// from a separate goroutine while the reader is active. This is precisely what
// immediate stream cancellation does, and what maximhq/bifrost#6143 showed to be
// unsafe: fasthttp's close callback returned the *requestStream and the
// connection's *bufio.Reader to sync.Pools while the reader was still inside
// (*requestStream).Read.
//
// Whether this is safe is a property of fasthttp, not of bifrost. It is safe
// only on a fasthttp that waits for an in-flight read before pooling (upstream
// commit fb3b29e, "wait for streaming reads before releasing pooled resources",
// unreleased as of v1.73.0).
func runSSEStreamWithHardClose(ctx *schemas.BifrostContext, client *fasthttp.Client, url string, closeAfter time.Duration) error {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true
	defer fasthttp.ReleaseRequest(req)

	req.Header.SetMethod(http.MethodPost)
	req.SetRequestURI(url)
	req.Header.SetContentType("application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.SetBodyString(`{"stream":true}`)

	if err := DoStreamingRequest(ctx, client, req, resp); err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	if resp.StatusCode() != fasthttp.StatusOK {
		return fmt.Errorf("unexpected status %d", resp.StatusCode())
	}

	bodyStream := resp.BodyStream()
	closerDone := make(chan struct{})
	go func() {
		defer close(closerDone)
		time.Sleep(closeAfter)
		closeBodyStream(bodyStream, context.Canceled)
	}()

	sseReader := GetSSEDataReader(ctx, resp.BodyStream())
	for {
		if _, err := sseReader.ReadDataLine(); err != nil {
			break
		}
	}
	<-closerDone
	return nil
}

// TestStreamCloseUnderActiveReaderIsSafe asserts that closing a streaming body
// while its reader is mid-read does not corrupt fasthttp's pooled objects.
//
// Meaningful under -race, where it is the direct check on whether the fasthttp
// in use can support immediate cancellation of a stalled stream. If this passes,
// SetupStreamCancellation can go back to closing on ctx.Done() and cancellation
// no longer has to wait for the idle timeout.
func TestStreamCloseUnderActiveReaderIsSafe(t *testing.T) {
	srv := newSSETestServer(t, 40, 5*time.Millisecond)
	client, _ := newTestStreamingClient(64)

	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	for range 200 {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
			defer cancel()
			_ = runSSEStreamWithHardClose(ctx, client, srv.URL, 40*time.Millisecond)
		}()
	}
	wg.Wait()
}

// TestReleaseAfterBodyEOFDoesNotBlock guards the drain in
// ReleaseStreamingResponse against a body that the consumer already read to EOF.
//
// fasthttp's requestStream used to have no terminal state: after returning
// io.EOF it re-entered parseChunkSize and waited for a chunk header that never
// arrives, because a keep-alive connection stays open once the body ends. The
// unbounded io.Copy in ReleaseStreamingResponse then parked that goroutine and
// its connection permanently.
//
// Production usually escapes this because ReadDataLine stops at "data: [DONE]",
// leaving the terminating chunk for the drain to consume. A stream that ends on
// a bare body EOF instead does not, and sse.go names that case explicitly.
//
// Fixed upstream in maximhq/fasthttp#1 (requestStream stays at EOF). This test
// fails by timing out on any fasthttp without that fix.
func TestReleaseAfterBodyEOFDoesNotBlock(t *testing.T) {
	srv := newSSETestServer(t, 5, time.Millisecond)
	client, _ := newTestStreamingClient(8)

	done := make(chan error, 1)
	go func() {
		ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
		defer cancel()

		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()
		resp.StreamBody = true
		defer fasthttp.ReleaseRequest(req)
		req.Header.SetMethod(http.MethodPost)
		req.SetRequestURI(srv.URL)
		req.Header.Set("Accept", "text/event-stream")
		req.SetBodyString(`{"stream":true}`)

		if err := DoStreamingRequest(ctx, client, req, resp); err != nil {
			// Release on the way out too, or a failing run abandons the acquired
			// response and its connection. Not a defer: the release below has to
			// happen before `done <- nil` so the select timeout still covers the
			// drain this test exists to measure, and ReleaseStreamingResponse is
			// not safe to call twice (it reads resp.BodyStream() before its claim
			// check, by which point resp is back in fasthttp's pool).
			ReleaseStreamingResponse(ctx, resp)
			done <- err
			return
		}
		// Read the body all the way to EOF, unlike the SSE reader which stops at
		// the [DONE] marker, then let the release drain it again.
		if _, err := io.Copy(io.Discard, resp.BodyStream()); err != nil {
			ReleaseStreamingResponse(ctx, resp)
			done <- err
			return
		}
		ReleaseStreamingResponse(ctx, resp)
		done <- nil
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ReleaseStreamingResponse blocked draining a body already at EOF")
	}
}
