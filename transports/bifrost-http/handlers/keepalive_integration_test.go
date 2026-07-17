package handlers

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/valyala/fasthttp"
)

// TestHandleStreamingResponse_KeepAliveDuringNonEmittingChunks drives the real
// streaming loop with chunks that are received but produce no client-visible
// output (nil chunks). Without the per-iteration keepalive check, a select over
// the stream channel would keep taking the ready-chunk case and starve the
// heartbeat; this asserts a `: keep-alive` is still emitted.
func TestHandleStreamingResponse_KeepAliveDuringNonEmittingChunks(t *testing.T) {
	h := &CompletionHandler{config: &lib.Config{
		ClientConfig: &configstore.ClientConfig{StreamKeepAliveInterval: 1}, // 1s (minimum granularity)
	}}

	handler := func(ctx *fasthttp.RequestCtx) {
		getStream := func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
			ch := make(chan *schemas.BifrostStreamChunk)
			go func() {
				defer close(ch)
				stop := time.Now().Add(1400 * time.Millisecond)
				for time.Now().Before(stop) {
					ch <- nil // received, but produces no client-visible SSE
					time.Sleep(150 * time.Millisecond)
				}
			}()
			return ch, nil
		}
		bctx := schemas.NewBifrostContext(context.Background(), time.Time{})
		h.handleStreamingResponse(ctx, bctx, getStream, func() {})
	}

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	go func() { _ = fasthttp.ServeConn(serverConn, handler) }()

	_, err := clientConn.Write([]byte("GET /v1/responses HTTP/1.1\r\nHost: test\r\n\r\n"))
	if err != nil {
		t.Fatalf("write request: %v", err)
	}

	_ = clientConn.SetReadDeadline(time.Now().Add(4 * time.Second))
	buf := make([]byte, 4096)
	var got strings.Builder
	for {
		n, readErr := clientConn.Read(buf)
		if n > 0 {
			got.WriteString(string(buf[:n]))
		}
		if strings.Contains(got.String(), ": keep-alive") || readErr != nil {
			break
		}
	}
	assert.Contains(t, got.String(), ": keep-alive", "a keepalive must be emitted during a stream of non-emitting chunks")
}
