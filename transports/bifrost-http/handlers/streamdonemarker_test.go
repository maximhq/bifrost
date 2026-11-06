package handlers

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// A "data: [DONE]" marker states that the stream finished normally. Emitting it
// after an error frame reproduces, one layer up, the same ambiguity that made
// truncated upstream streams look successful: a client keyed on the marker sees
// a clean end either way. See https://github.com/maximhq/bifrost/issues/5546.

// serveStreamingResponse runs handleStreamingResponse against an in-process
// connection and returns the raw SSE body the client would receive.
func serveStreamingResponse(t *testing.T, chunks []*schemas.BifrostStreamChunk) string {
	t.Helper()
	return serveStreamingResponseWithContext(t, schemas.ChatCompletionStreamRequest, chunks, nil)
}

// serveStreamingResponseWithContext is serveStreamingResponse with a hook that
// sees the request's BifrostContext before the stream is consumed.
func serveStreamingResponseWithContext(t *testing.T, requestType schemas.RequestType, chunks []*schemas.BifrostStreamChunk, prepare func(*schemas.BifrostContext)) string {
	t.Helper()

	handler := func(reqCtx *fasthttp.RequestCtx) {
		// A zero Config loads no transport plugins, so no chunk interceptor runs.
		h := &CompletionHandler{config: &lib.Config{}}

		base, cancel := context.WithCancel(context.Background())
		bifrostCtx := schemas.NewBifrostContext(base, schemas.NoDeadline)
		if prepare != nil {
			prepare(bifrostCtx)
		}

		getStream := func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
			stream := make(chan *schemas.BifrostStreamChunk, len(chunks))
			for _, chunk := range chunks {
				stream <- chunk
			}
			close(stream)
			return stream, nil
		}

		h.handleStreamingResponse(reqCtx, bifrostCtx, requestType, getStream, cancel)
	}

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()

	go func() {
		_ = fasthttp.ServeConn(serverConn, handler)
	}()

	if err := clientConn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatalf("failed to set deadline: %v", err)
	}
	if _, err := clientConn.Write([]byte("POST /v1/chat/completions HTTP/1.1\r\nHost: test\r\n\r\n")); err != nil {
		t.Fatalf("failed to write request: %v", err)
	}

	br := bufio.NewReader(clientConn)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("failed to read response header: %v", err)
		}
		if strings.TrimSpace(line) == "" {
			break
		}
	}

	var body strings.Builder
	for {
		sizeLine, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("failed to read chunk size: %v", err)
		}
		var chunkSize int
		if _, err := fmt.Sscanf(strings.TrimSpace(sizeLine), "%x", &chunkSize); err != nil {
			t.Fatalf("failed to parse chunk size %q: %v", sizeLine, err)
		}
		if chunkSize == 0 {
			break
		}
		data := make([]byte, chunkSize+2) // +2 for the trailing CRLF
		for n := 0; n < len(data); {
			nn, err := br.Read(data[n:])
			if err != nil {
				t.Fatalf("failed to read chunk data: %v", err)
			}
			n += nn
		}
		body.Write(data[:chunkSize])
	}
	return body.String()
}

func contentChunk(text string) *schemas.BifrostStreamChunk {
	return &schemas.BifrostStreamChunk{
		BifrostChatResponse: &schemas.BifrostChatResponse{
			ID:     "chatcmpl-repro",
			Object: "chat.completion.chunk",
			Model:  "repro-model",
			Choices: []schemas.BifrostResponseChoice{{
				Index: 0,
				ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
					Delta: &schemas.ChatStreamResponseChoiceDelta{Content: schemas.Ptr(text)},
				},
			}},
		},
	}
}

func truncationErrorChunk() *schemas.BifrostStreamChunk {
	statusCode := 502
	errorType := schemas.ProviderConnectionFailed
	return &schemas.BifrostStreamChunk{
		BifrostError: &schemas.BifrostError{
			IsBifrostError: false,
			StatusCode:     &statusCode,
			Error: &schemas.ErrorField{
				Message: schemas.ErrProviderStreamTruncated,
				Type:    &errorType,
			},
		},
	}
}

// A stream that ends on an error frame is already terminated for the client;
// appending [DONE] would report it as a clean finish.
func TestStreamingResponseSkipsDoneAfterErrorChunk(t *testing.T) {
	body := serveStreamingResponse(t, []*schemas.BifrostStreamChunk{
		contentChunk("partial answer"),
		truncationErrorChunk(),
	})

	if !strings.Contains(body, schemas.ErrProviderStreamTruncated) {
		t.Errorf("expected the error frame to reach the client, got:\n%s", body)
	}
	if strings.Contains(body, "data: [DONE]") {
		t.Errorf("[DONE] must not follow an error frame, got:\n%s", body)
	}
}

// Guard against over-suppression: a clean stream still gets its marker.
func TestStreamingResponseSendsDoneOnCleanStream(t *testing.T) {
	body := serveStreamingResponse(t, []*schemas.BifrostStreamChunk{
		contentChunk("hello"),
		contentChunk(" world"),
	})

	if !strings.Contains(body, "data: [DONE]") {
		t.Errorf("expected [DONE] to terminate a clean stream, got:\n%s", body)
	}
}

// Core stops waiting for a consumer that stays busy past its send bound, so a
// timed-out stream can close without its timeout chunk. The handler must still
// report the timeout rather than mark a truncated stream as finished.
func TestStreamingResponseReportsFiredRequestTimeoutWithoutChunk(t *testing.T) {
	body := serveStreamingResponseWithContext(t, schemas.ChatCompletionStreamRequest, []*schemas.BifrostStreamChunk{
		contentChunk("partial answer"),
	}, func(ctx *schemas.BifrostContext) {
		ctx.SetValue(schemas.BifrostContextKeyRequestTimeoutFired, true)
	})

	if !strings.Contains(body, schemas.RequestTimedOut) {
		t.Errorf("expected a request_timed_out error frame, got:\n%s", body)
	}
	if strings.Contains(body, "data: [DONE]") {
		t.Errorf("[DONE] must not end a timed-out stream, got:\n%s", body)
	}
}

// The timeout chunk core did deliver is the only error frame the client gets.
func TestStreamingResponseSendsOneTimeoutFrame(t *testing.T) {
	body := serveStreamingResponseWithContext(t, schemas.ChatCompletionStreamRequest, []*schemas.BifrostStreamChunk{
		contentChunk("partial answer"),
		{BifrostError: schemas.NewRequestTimeoutStreamError()},
	}, func(ctx *schemas.BifrostContext) {
		ctx.SetValue(schemas.BifrostContextKeyRequestTimeoutFired, true)
	})

	if got := strings.Count(body, schemas.RequestTimedOut); got != 1 {
		t.Errorf("expected exactly one request_timed_out frame, got %d:\n%s", got, body)
	}
	if strings.Contains(body, "data: [DONE]") {
		t.Errorf("[DONE] must not end a timed-out stream, got:\n%s", body)
	}
}

// A Responses client gets the timeout as an "event: error" frame whether core
// delivered the chunk or the handler reported the fired marker itself.
func TestStreamingResponseFramesTimeoutAsErrorEventOnResponses(t *testing.T) {
	fired := func(ctx *schemas.BifrostContext) {
		ctx.SetValue(schemas.BifrostContextKeyRequestTimeoutFired, true)
	}
	for name, chunks := range map[string][]*schemas.BifrostStreamChunk{
		"core delivered the chunk": {{BifrostError: schemas.NewRequestTimeoutStreamError()}},
		"stream closed without it": nil,
	} {
		t.Run(name, func(t *testing.T) {
			body := serveStreamingResponseWithContext(t, schemas.ResponsesStreamRequest, chunks, fired)
			if !strings.Contains(body, "event: error\n") {
				t.Errorf("expected an event: error frame, got:\n%s", body)
			}
			if got := strings.Count(body, schemas.RequestTimedOut); got != 1 {
				t.Errorf("expected exactly one request_timed_out frame, got %d:\n%s", got, body)
			}
		})
	}
}
