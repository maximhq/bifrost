package handlers

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// TestResponseHeadersHookAppliedBeforeSSEStreamStarts exercises the actual wire
// boundary: the hook runs after SetBodyStream, yet its header must be present in
// the HTTP response block that precedes the first SSE bytes.
func TestResponseHeadersHookAppliedBeforeSSEStreamStarts(t *testing.T) {
	plugin := &fakeResponseHeadersPlugin{
		name: "wire-response-headers",
		hook: func(_ *schemas.BifrostContext, _ *schemas.HTTPRequest, resp *schemas.HTTPResponseMetadata) error {
			resp.SetHeader("X-Streaming-Header", "set-before-commit")
			return nil
		},
	}

	handler := TransportInterceptorMiddleware(responseHeadersTestConfig(t, plugin))(func(ctx *fasthttp.RequestCtx) {
		ctx.SetContentType("text/event-stream")
		ctx.SetUserValue(schemas.BifrostContextKeyDeferTraceCompletion, true)
		ctx.Response.SetBodyStream(strings.NewReader("data: ok\n\n"), -1)
	})

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	go func() {
		_ = fasthttp.ServeConn(serverConn, handler)
	}()

	if _, err := clientConn.Write([]byte("GET /stream HTTP/1.1\r\nHost: test\r\n\r\n")); err != nil {
		t.Fatalf("write request: %v", err)
	}
	req, err := http.NewRequest(http.MethodGet, "http://test/stream", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(clientConn), req)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("X-Streaming-Header"); got != "set-before-commit" {
		t.Fatalf("X-Streaming-Header = %q, want set-before-commit", got)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read stream body: %v", err)
	}
	if got := string(body); got != "data: ok\n\n" {
		t.Fatalf("stream body = %q, want data event", got)
	}
}

// TestSSEStreamReaderNoEventBatching verifies that SSE events are delivered
// individually through fasthttp's chunked transfer encoding, not batched
// into larger TCP segments. This is the core regression test for the
// fasthttputil.PipeConns batching fix.
func TestSSEStreamReaderNoEventBatching(t *testing.T) {
	const numEvents = 20

	// Build expected events
	events := make([]string, numEvents)
	for i := range events {
		events[i] = fmt.Sprintf("data: {\"index\":%d,\"content\":\"chunk-%d\"}\n\n", i, i)
	}

	handler := func(ctx *fasthttp.RequestCtx) {
		ctx.SetContentType("text/event-stream")
		ctx.Response.Header.Set("Cache-Control", "no-cache")

		reader := lib.NewSSEStreamReader()

		go func() {
			defer reader.Done()
			for _, event := range events {
				if !reader.Send([]byte(event)) {
					return
				}
			}
		}()

		ctx.Response.SetBodyStream(reader, -1)
	}

	// Use net.Pipe for deterministic in-process testing
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()

	// Run fasthttp server on one end of the pipe
	go func() {
		_ = fasthttp.ServeConn(serverConn, handler)
	}()

	// Send HTTP request through the pipe
	_, err := clientConn.Write([]byte("GET /stream HTTP/1.1\r\nHost: test\r\n\r\n"))
	if err != nil {
		t.Fatalf("failed to write request: %v", err)
	}

	// Read response using bufio to parse chunked encoding
	br := bufio.NewReader(clientConn)

	// Read and skip HTTP response headers
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("failed to read response header: %v", err)
		}
		if strings.TrimSpace(line) == "" {
			break // End of headers
		}
	}

	// Read chunked transfer-encoded body.
	// Each HTTP chunk should contain exactly one SSE event.
	var receivedEvents []string
	for {
		// Read chunk size line (hex size + CRLF)
		sizeLine, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("failed to read chunk size: %v", err)
		}
		sizeLine = strings.TrimSpace(sizeLine)

		var chunkSize int
		_, err = fmt.Sscanf(sizeLine, "%x", &chunkSize)
		if err != nil {
			t.Fatalf("failed to parse chunk size %q: %v", sizeLine, err)
		}

		if chunkSize == 0 {
			break // Terminal chunk
		}

		// Read exactly chunkSize bytes + trailing CRLF
		chunkData := make([]byte, chunkSize+2) // +2 for CRLF
		n := 0
		for n < len(chunkData) {
			nn, err := br.Read(chunkData[n:])
			if err != nil {
				t.Fatalf("failed to read chunk data: %v", err)
			}
			n += nn
		}

		chunk := string(chunkData[:chunkSize])
		receivedEvents = append(receivedEvents, chunk)
	}

	// Verify each chunk contains exactly one SSE event
	if len(receivedEvents) != numEvents {
		t.Errorf("expected %d individual chunks, got %d (events were batched)", numEvents, len(receivedEvents))
		for i, chunk := range receivedEvents {
			eventCount := strings.Count(chunk, "\n\n")
			t.Logf("  chunk %d: %d SSE events, %d bytes", i, eventCount, len(chunk))
		}
	}

	for i, chunk := range receivedEvents {
		if i >= len(events) {
			break
		}
		if chunk != events[i] {
			t.Errorf("chunk %d: got %q, want %q", i, chunk, events[i])
		}
	}
}
