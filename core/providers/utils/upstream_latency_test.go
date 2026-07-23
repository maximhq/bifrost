package utils

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

func accumCtx() *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.ResetUpstreamLatency()
	return ctx
}

func upstream(t *testing.T, ctx *schemas.BifrostContext) time.Duration {
	t.Helper()
	d, ok := schemas.GetUpstreamLatency(ctx)
	if !ok {
		t.Fatal("no accumulator on context")
	}
	return d
}

// The unary choke point every non-streaming provider call funnels through.
func TestMakeRequestWithDoFuncAccumulates(t *testing.T) {
	ctx := accumCtx()

	_, bifrostErr, wait := makeRequestWithDoFunc(ctx, func() error {
		time.Sleep(60 * time.Millisecond)
		return nil
	})
	wait()

	if bifrostErr != nil {
		t.Fatalf("unexpected error: %v", bifrostErr)
	}
	if got := upstream(t, ctx); got < 50*time.Millisecond {
		t.Fatalf("upstream = %v, want >= 50ms", got)
	}
}

// A cancelled request still spent real time on the socket; that time belongs to
// upstream, not to Bifrost.
func TestMakeRequestWithDoFuncAccumulatesOnCancel(t *testing.T) {
	base := accumCtx()
	cancellable, cancel := context.WithCancel(base)
	defer cancel()

	go func() {
		time.Sleep(40 * time.Millisecond)
		cancel()
	}()

	done := make(chan struct{})
	_, bifrostErr, wait := makeRequestWithDoFunc(cancellable, func() error {
		<-done
		return nil
	})
	if bifrostErr == nil {
		t.Fatal("expected a cancellation error")
	}
	close(done)
	wait()

	// The accumulator lives on base; the cancellable context reads through to it.
	if got := upstream(t, base); got < 30*time.Millisecond {
		t.Fatalf("upstream = %v, want >= 30ms on the cancel path", got)
	}
}

// Retries and fallbacks reuse one context, so their provider time must sum
// rather than overwrite — this is what ExtraFields.Latency cannot express.
func TestUnaryAccumulatesAcrossAttempts(t *testing.T) {
	ctx := accumCtx()

	for range 3 {
		_, _, wait := makeRequestWithDoFunc(ctx, func() error {
			time.Sleep(30 * time.Millisecond)
			return nil
		})
		wait()
	}

	if got := upstream(t, ctx); got < 80*time.Millisecond {
		t.Fatalf("upstream = %v, want >= 80ms across 3 attempts", got)
	}
}

// The net/http path used by Bedrock and the media fetcher. Attribution runs off
// req.Context(), so no explicit context argument is threaded through.
func TestDoHTTPRequestAccumulates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ctx := accumCtx()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}

	resp, err := DoHTTPRequest(server.Client(), req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if got := upstream(t, ctx); got < 40*time.Millisecond {
		t.Fatalf("upstream = %v, want >= 40ms", got)
	}
}

// client.Do returns at response headers; draining the body is still socket
// time. The wrapped body must attribute it to upstream, not Bifrost.
func TestDoHTTPRequestAccumulatesBodyReadTime(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush() // headers out immediately; the wait below is body-only
		time.Sleep(60 * time.Millisecond)
		w.Write([]byte("payload"))
	}))
	defer server.Close()

	ctx := accumCtx()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}

	resp, err := DoHTTPRequest(server.Client(), req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	afterHeaders := upstream(t, ctx)
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("reading body: %v", err)
	}

	if got := upstream(t, ctx) - afterHeaders; got < 50*time.Millisecond {
		t.Fatalf("body read added %v to upstream, want >= 50ms", got)
	}
}

// Streaming consumes a DoHTTPRequest body through NewIdleTimeoutReader, which
// does its own per-chunk timing — the timed wrapper must be unwrapped there or
// every chunk counts twice.
func TestIdleTimeoutReaderUnwrapsTimedBody(t *testing.T) {
	ctx := accumCtx()

	timed := &upstreamTimingBody{inner: io.NopCloser(strings.NewReader("payload")), ctx: ctx}
	reader, stop := NewIdleTimeoutReader(timed, timed, 5*time.Second, ctx)
	defer stop()

	itr := reader.(*idleTimeoutReader)
	if _, ok := itr.reader.(*upstreamTimingBody); ok {
		t.Fatal("reader not unwrapped — chunk reads would be double-counted")
	}
	if _, ok := itr.bodyStream.(*upstreamTimingBody); ok {
		t.Fatal("bodyStream not unwrapped")
	}
}

// slowReader blocks before returning each chunk, standing in for a provider
// generating tokens.
type slowReader struct {
	chunks []string
	delay  time.Duration
	idx    int
}

func (s *slowReader) Read(p []byte) (int, error) {
	if s.idx >= len(s.chunks) {
		return 0, io.EOF
	}
	time.Sleep(s.delay)
	n := copy(p, s.chunks[s.idx])
	s.idx++
	return n, nil
}

// The streaming seam. Time blocked inside the wrapped Read is the provider
// generating; everything else in that goroutine is Bifrost's own cost. Without
// this, the whole generation window would be reported as Bifrost overhead.
func TestIdleTimeoutReaderAccumulatesReadTime(t *testing.T) {
	ctx := accumCtx()

	src := &slowReader{chunks: []string{"a", "b", "c"}, delay: 30 * time.Millisecond}
	reader, stop := NewIdleTimeoutReader(src, src, 5*time.Second, ctx)
	defer stop()

	if _, err := io.ReadAll(reader); err != nil {
		t.Fatalf("reading stream: %v", err)
	}

	// Three 30ms chunk waits, minus scheduler slop.
	if got := upstream(t, ctx); got < 80*time.Millisecond {
		t.Fatalf("upstream = %v, want >= 80ms across 3 chunks", got)
	}
}

// Parsing between reads is Bifrost's cost and must stay out of the total.
func TestIdleTimeoutReaderExcludesProcessingTime(t *testing.T) {
	ctx := accumCtx()

	src := &slowReader{chunks: []string{"x", "y"}, delay: 20 * time.Millisecond}
	reader, stop := NewIdleTimeoutReader(src, src, 5*time.Second, ctx)
	defer stop()

	buf := make([]byte, 8)
	for {
		_, err := reader.Read(buf)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		// Stand-in for SSE parsing and schema transformation.
		time.Sleep(60 * time.Millisecond)
	}

	got := upstream(t, ctx)
	if got < 30*time.Millisecond {
		t.Fatalf("upstream = %v, want >= 30ms of genuine read time", got)
	}
	// Two reads (~40ms) plus two sleeps (~120ms) is ~160ms wall clock. If the
	// simulated processing leaked in, this would be well over 100ms.
	if got > 100*time.Millisecond {
		t.Fatalf("upstream = %v, want < 100ms — processing time leaked into upstream", got)
	}
}

// A reader with no accumulator on its context must not panic or misreport.
func TestIdleTimeoutReaderWithoutAccumulator(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	reader, stop := NewIdleTimeoutReader(strings.NewReader("payload"), nil, 5*time.Second, ctx)
	defer stop()

	if _, err := io.ReadAll(reader); err != nil {
		t.Fatalf("reading stream: %v", err)
	}
	if _, ok := schemas.GetUpstreamLatency(ctx); ok {
		t.Fatal("accumulator appeared without a reset")
	}
}
