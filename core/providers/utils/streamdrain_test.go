package utils

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// chunkedStreamServer serves a chunked response whose body is the concatenation of parts,
// flushed one part at a time, over a keep-alive connection.
func chunkedStreamServer(t *testing.T, parts ...string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/octet-stream")
		flusher, ok := w.(http.Flusher)
		for _, part := range parts {
			_, _ = w.Write([]byte(part))
			if ok {
				flusher.Flush()
			}
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// startChunkedStream issues a streaming GET and returns the client plus the live response.
// The caller owns the response and must release it.
func startChunkedStream(t *testing.T, url string) (*fasthttp.Client, *fasthttp.Response) {
	t.Helper()

	client := BuildStreamingClient(&fasthttp.Client{
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	})

	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodGet)

	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true
	if err := client.Do(req, resp); err != nil {
		fasthttp.ReleaseResponse(resp)
		t.Fatalf("streaming request failed: %v", err)
	}
	if resp.StatusCode() != fasthttp.StatusOK {
		fasthttp.ReleaseResponse(resp)
		t.Fatalf("unexpected status %d", resp.StatusCode())
	}
	return client, resp
}

// runWithDeadline reports whether fn returned before d elapsed. A blocked call is reported
// rather than allowed to hang the package until the global test timeout.
func runWithDeadline(d time.Duration, fn func()) bool {
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// TestReleaseStreamingResponseAfterEOFDoesNotBlock covers a body that the caller has already
// read all the way to io.EOF, which is what every raw-byte streaming path does (audio
// synthesis, binary passthrough) as opposed to the SSE paths that stop on a terminal marker.
//
// fasthttp's chunked requestStream keeps no finished flag: after the terminating zero-size
// chunk it returns io.EOF, but a subsequent Read re-enters parseChunkSize and waits for a
// chunk header that a keep-alive connection never sends. Draining such a stream on release
// therefore blocks forever, and because the release runs in the provider's deferred cleanup
// the stream channel is never closed and the consumer deadlocks.
func TestReleaseStreamingResponseAfterEOFDoesNotBlock(t *testing.T) {
	server := chunkedStreamServer(t, "RIFF", "fake", "audio")
	_, resp := startChunkedStream(t, server.URL)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	reader, releaseGzip := DecompressStreamBody(resp)
	defer releaseGzip()
	reader, stopIdleTimeout := NewIdleTimeoutReader(reader, resp.BodyStream(), 5*time.Second, ctx)
	defer stopIdleTimeout()

	// Consume the body exactly as a raw-byte streaming provider does.
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("reading stream returned %v", err)
	}
	if string(body) != "RIFFfakeaudio" {
		t.Fatalf("unexpected body %q", string(body))
	}

	if !runWithDeadline(5*time.Second, func() { ReleaseStreamingResponse(ctx, resp) }) {
		t.Fatal("ReleaseStreamingResponse blocked on a body that was already read to EOF")
	}
}

// TestReleaseStreamingResponseDrainsPartiallyReadBody is the counterpart: the SSE paths stop
// on a terminal marker with bytes still buffered, and those leftovers must be flushed before
// the connection goes back to the pool. Skipping the drain there resurfaces the
// "whitespace in header" corruption of valyala/fasthttp#1743 on the next request, so this
// pins that the EOF fast-path above did not disable draining in general.
func TestReleaseStreamingResponseDrainsPartiallyReadBody(t *testing.T) {
	server := chunkedStreamServer(t, "first", "second", "third")
	client, resp := startChunkedStream(t, server.URL)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	reader, releaseGzip := DecompressStreamBody(resp)
	defer releaseGzip()
	reader, stopIdleTimeout := NewIdleTimeoutReader(reader, resp.BodyStream(), 5*time.Second, ctx)
	defer stopIdleTimeout()

	// Stop early, leaving unread bytes on the wire.
	buf := make([]byte, 5)
	if _, err := io.ReadFull(reader, buf); err != nil {
		t.Fatalf("reading first chunk returned %v", err)
	}
	if string(buf) != "first" {
		t.Fatalf("unexpected first chunk %q", string(buf))
	}

	if exhausted, _ := ctx.Value(schemas.BifrostContextKeyStreamBodyExhausted).(bool); exhausted {
		t.Fatal("a partially read body must not be marked exhausted")
	}

	if !runWithDeadline(5*time.Second, func() { ReleaseStreamingResponse(ctx, resp) }) {
		t.Fatal("ReleaseStreamingResponse blocked draining a partially read body")
	}

	// The connection is only safe to reuse if the leftovers were actually drained; a dirty
	// one surfaces as a header parse error on the next request over the same pool.
	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	req.SetRequestURI(server.URL)
	req.Header.SetMethod(http.MethodGet)

	next := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(next)
	if err := client.Do(req, next); err != nil {
		t.Fatalf("reusing the pooled connection failed after release: %v", err)
	}
	if got := string(next.Body()); got != "firstsecondthird" {
		t.Fatalf("unexpected body on reused connection: %q", got)
	}
}

// TestIdleTimeoutReaderMarksBodyExhaustedOnEOF pins the signal the release path depends on.
func TestIdleTimeoutReaderMarksBodyExhaustedOnEOF(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	source := &sequenceReader{chunks: []string{"one", "two"}}
	reader, stop := NewIdleTimeoutReader(source, source, 5*time.Second, ctx)
	defer stop()

	if exhausted, _ := ctx.Value(schemas.BifrostContextKeyStreamBodyExhausted).(bool); exhausted {
		t.Fatal("body marked exhausted before any read")
	}

	if _, err := io.ReadAll(reader); err != nil {
		t.Fatalf("reading returned %v", err)
	}

	if exhausted, _ := ctx.Value(schemas.BifrostContextKeyStreamBodyExhausted).(bool); !exhausted {
		t.Fatal("reaching io.EOF must mark the body exhausted")
	}
}

// sequenceReader yields each chunk from its own Read call, then io.EOF, mirroring how a
// chunked transfer surfaces to the idle-timeout reader.
type sequenceReader struct {
	chunks []string
}

func (s *sequenceReader) Read(p []byte) (int, error) {
	if len(s.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, s.chunks[0])
	if n < len(s.chunks[0]) {
		s.chunks[0] = s.chunks[0][n:]
		return n, nil
	}
	s.chunks = s.chunks[1:]
	return n, nil
}
