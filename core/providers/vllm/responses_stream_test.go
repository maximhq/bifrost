package vllm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// noopTestLogger is a minimal schemas.Logger implementation for tests that
// don't want to pull in the core package (which itself imports this
// package, so importing it back here would create an import cycle).
type noopTestLogger struct{}

func (noopTestLogger) Debug(string, ...any)                   {}
func (noopTestLogger) Info(string, ...any)                    {}
func (noopTestLogger) Warn(string, ...any)                    {}
func (noopTestLogger) Error(string, ...any)                   {}
func (noopTestLogger) Fatal(string, ...any)                   {}
func (noopTestLogger) SetLevel(schemas.LogLevel)              {}
func (noopTestLogger) SetOutputType(schemas.LoggerOutputType) {}
func (noopTestLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

// newTestVLLMResponsesStreamProvider creates a VLLMProvider with a real
// streaming client (via NewVLLMProvider) so ResponsesStream exercises the
// same fasthttp streaming path used in production.
func newTestVLLMResponsesStreamProvider(t *testing.T, sendBackRawRequest, sendBackRawResponse bool) *VLLMProvider {
	t.Helper()

	provider, err := NewVLLMProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 10,
		},
		SendBackRawRequest:  sendBackRawRequest,
		SendBackRawResponse: sendBackRawResponse,
	}, noopTestLogger{})
	if err != nil {
		t.Fatalf("failed to create vLLM provider: %v", err)
	}
	return provider
}

func testVLLMResponsesStreamRequest() *schemas.BifrostResponsesRequest {
	return &schemas.BifrostResponsesRequest{
		Provider: schemas.VLLM,
		Model:    "fake-model",
		Input: []schemas.ResponsesMessage{
			{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("hi"),
				},
			},
		},
	}
}

// drainStreamWithTimeout collects every chunk from stream until it closes,
// failing fast (instead of hanging until the test binary's own timeout) if
// the channel never closes — which is exactly the failure mode of #5504.
func drainStreamWithTimeout(t *testing.T, stream chan *schemas.BifrostStreamChunk, timeout time.Duration) []*schemas.BifrostStreamChunk {
	t.Helper()

	var chunks []*schemas.BifrostStreamChunk
	done := make(chan struct{})
	go func() {
		defer close(done)
		for chunk := range stream {
			chunks = append(chunks, chunk)
		}
	}()

	select {
	case <-done:
		return chunks
	case <-time.After(timeout):
		t.Fatalf("stream did not close within %s — possible regression of the vLLM streaming hang (issue #5504)", timeout)
		return nil
	}
}

// sseServer builds an httptest server that serves the given raw SSE body
// (each element already formatted as "data: {...}\n\n") at /v1/responses.
func sseServer(events ...string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for _, e := range events {
			fmt.Fprint(w, e)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
}

func testVLLMKey(serverURL string) schemas.Key {
	return schemas.Key{
		ID:    "test-key",
		Value: schemas.SecretVar{Val: "test-api-key"},
		VLLMKeyConfig: &schemas.VLLMKeyConfig{
			URL: schemas.SecretVar{Val: serverURL},
		},
	}
}

var noopPostHookRunner schemas.PostHookRunner = func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return result, err
}

type deadlineRecordingConn struct {
	net.Conn
	last *atomic.Int64
}

func (c deadlineRecordingConn) SetReadDeadline(d time.Time) error {
	if !d.IsZero() {
		c.last.Store(int64(time.Until(d)))
	}
	return c.Conn.SetReadDeadline(d)
}

func TestTranscriptionStreamSocketIdleTimeout(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		override time.Duration
	}{
		{"success", http.StatusOK, 0},
		{"error_body", http.StatusBadRequest, 0},
		{"caller_override", http.StatusOK, 7 * time.Second},
		{"stalled_rejection", http.StatusBadRequest, 40 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			watchdog := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				if tc.name == "stalled_rejection" {
					w.(http.Flusher).Flush()
					select {
					case <-r.Context().Done():
					case <-watchdog:
					}
					return
				}
				// Exceed the header reader's buffer to force a body socket read.
				if tc.status == http.StatusOK {
					fmt.Fprintf(w, ": %s\n\ndata: {\"usage\":{}}\n\n", strings.Repeat("x", 16<<10))
				} else {
					fmt.Fprintf(w, `{"error":{"message":"%s"}}`, strings.Repeat("x", 16<<10))
				}
			}))
			defer srv.Close()
			provider, initErr := NewVLLMProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 10,
				StreamIdleTimeoutInSeconds:     300,
			}}, noopTestLogger{})
			if initErr != nil {
				t.Fatal(initErr)
			}
			var last atomic.Int64
			provider.streamingClient.Dial = func(addr string) (net.Conn, error) {
				conn, err := net.DialTimeout("tcp", addr, time.Second)
				if err != nil {
					return nil, err
				}
				return deadlineRecordingConn{conn, &last}, nil
			}
			defer provider.streamingClient.CloseIdleConnections()
			ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
			defer cancel()
			want := 300 * time.Second
			if tc.override > 0 {
				ctx.SetValue(schemas.BifrostContextKeyStreamIdleTimeout, tc.override)
				want = tc.override
			}
			req := &schemas.BifrostTranscriptionRequest{
				Provider: schemas.VLLM, Model: "test-model", Input: &schemas.TranscriptionInput{File: []byte("audio")},
			}
			var timer *time.Timer
			if tc.name == "stalled_rejection" {
				timer = time.AfterFunc(2*time.Second, func() { close(watchdog) })
			}
			stream, err := provider.TranscriptionStream(ctx, noopPostHookRunner, nil, testVLLMKey(srv.URL), req)
			if timer != nil && !timer.Stop() {
				<-watchdog
				t.Fatal("watchdog released the upstream before the socket idle timeout")
			}
			if tc.status == http.StatusOK {
				if err != nil {
					t.Fatal(err)
				}
				chunks := drainStreamWithTimeout(t, stream, 5*time.Second)
				if len(chunks) != 1 || chunks[0].BifrostTranscriptionStreamResponse == nil || chunks[0].BifrostTranscriptionStreamResponse.Type != schemas.TranscriptionStreamResponseTypeDone {
					t.Fatalf("expected a completed transcription, got %+v", chunks)
				}
			} else if tc.name == "stalled_rejection" {
				if err == nil || err.Error == nil || !errors.Is(err.Error.Error, providerUtils.ErrStreamIdleTimeout) || err.EffectiveHTTPStatus() != 504 {
					t.Fatalf("expected a body idle timeout, got %v", err)
				}
			} else if err == nil || err.EffectiveHTTPStatus() != tc.status {
				t.Fatalf("upstream status = %v, want %d", err, tc.status)
			}
			if tc.status != http.StatusOK && tc.override == 0 && ctx.Value(schemas.BifrostContextKeyStreamIdleTimeout) != nil {
				t.Error("failed provider left its idle timeout on the context used by fallbacks")
			}
			if got := time.Duration(last.Load()); got <= 0 || got > want || got < want-min(time.Second, want/2) {
				t.Fatalf("socket idle timeout = %v, want %v", got.Round(time.Millisecond), want)
			}
		})
	}
}

// TestResponsesStream_SSEErrorEvent_SurfacesErrorAndCloses is a regression
// test for #5504: an in-band SSE "type":"error" event (a normal 200 stream
// that reports failure via a chunk, not an HTTP-level error) must surface as
// a BifrostError chunk and the stream must close promptly, not hang.
func TestResponsesStream_SSEErrorEvent_SurfacesErrorAndCloses(t *testing.T) {
	t.Parallel()

	server := sseServer(
		"data: {\"type\":\"response.output_text.delta\",\"sequence_number\":0,\"delta\":\"he\"}\n\n",
		"data: {\"type\":\"error\",\"sequence_number\":1,\"message\":\"rate limit exceeded\",\"code\":\"rate_limit_exceeded\"}\n\n",
	)
	defer server.Close()

	provider := newTestVLLMResponsesStreamProvider(t, false, false)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := testVLLMKey(server.URL)

	stream, bifrostErr := provider.ResponsesStream(ctx, noopPostHookRunner, nil, key, testVLLMResponsesStreamRequest())
	if bifrostErr != nil {
		t.Fatalf("ResponsesStream returned error synchronously: %v", bifrostErr.Error.Message)
	}

	chunks := drainStreamWithTimeout(t, stream, 5*time.Second)

	var errChunk *schemas.BifrostStreamChunk
	for _, c := range chunks {
		if c != nil && c.BifrostError != nil {
			errChunk = c
			break
		}
	}

	if errChunk == nil {
		t.Fatalf("expected a BifrostError chunk for the in-band SSE error event, got %d chunks: %+v", len(chunks), chunks)
	}
	if errChunk.BifrostError.Error == nil || errChunk.BifrostError.Error.Message != "rate limit exceeded" {
		t.Fatalf("expected error message %q, got %+v", "rate limit exceeded", errChunk.BifrostError.Error)
	}
	if errChunk.BifrostError.Error.Code == nil || *errChunk.BifrostError.Error.Code != "rate_limit_exceeded" {
		t.Fatalf("expected error code %q, got %+v", "rate_limit_exceeded", errChunk.BifrostError.Error.Code)
	}
}

// TestResponsesStream_SSEResponseFailedEvent_SurfacesErrorAndCloses is a
// regression test for #5504: a "response.failed" event (used by vLLM/some
// OpenAI-compatible backends to report generation failure on an HTTP 200
// stream) must be converted to a BifrostError and end the stream, not be
// silently dropped.
func TestResponsesStream_SSEResponseFailedEvent_SurfacesErrorAndCloses(t *testing.T) {
	t.Parallel()

	server := sseServer(
		"data: {\"type\":\"response.output_text.delta\",\"sequence_number\":0,\"delta\":\"he\"}\n\n",
		"data: {\"type\":\"response.failed\",\"sequence_number\":1,\"response\":{\"id\":\"r1\",\"object\":\"response\",\"created_at\":1,\"model\":\"fake-model\",\"status\":\"failed\",\"error\":{\"message\":\"upstream engine error\",\"code\":\"engine_error\"}}}\n\n",
	)
	defer server.Close()

	provider := newTestVLLMResponsesStreamProvider(t, false, false)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := testVLLMKey(server.URL)

	stream, bifrostErr := provider.ResponsesStream(ctx, noopPostHookRunner, nil, key, testVLLMResponsesStreamRequest())
	if bifrostErr != nil {
		t.Fatalf("ResponsesStream returned error synchronously: %v", bifrostErr.Error.Message)
	}

	chunks := drainStreamWithTimeout(t, stream, 5*time.Second)

	var errChunk *schemas.BifrostStreamChunk
	for _, c := range chunks {
		if c != nil && c.BifrostError != nil {
			errChunk = c
			break
		}
	}

	if errChunk == nil {
		t.Fatalf("expected a BifrostError chunk for the response.failed event, got %d chunks: %+v", len(chunks), chunks)
	}
	if errChunk.BifrostError.Error == nil || errChunk.BifrostError.Error.Message != "upstream engine error" {
		t.Fatalf("expected error message %q, got %+v", "upstream engine error", errChunk.BifrostError.Error)
	}
	if errChunk.BifrostError.Error.Code == nil || *errChunk.BifrostError.Error.Code != "engine_error" {
		t.Fatalf("expected error code %q, got %+v", "engine_error", errChunk.BifrostError.Error.Code)
	}
}

// TestResponsesStream_CompletesWithoutHanging is a regression guard for the
// root cause of #5504: every delta chunk must actually be forwarded (not
// silently dropped) and the stream must close as soon as response.completed
// arrives, instead of reading through to genuine EOF and hanging in cleanup.
func TestResponsesStream_CompletesWithoutHanging(t *testing.T) {
	t.Parallel()

	server := sseServer(
		"data: {\"type\":\"response.output_text.delta\",\"sequence_number\":0,\"delta\":\"hel\"}\n\n",
		"data: {\"type\":\"response.output_text.delta\",\"sequence_number\":1,\"delta\":\"lo\"}\n\n",
		"data: {\"type\":\"response.completed\",\"sequence_number\":2,\"response\":{\"id\":\"r1\",\"object\":\"response\",\"created_at\":1,\"model\":\"fake-model\",\"status\":\"completed\",\"output\":[{\"id\":\"msg_1\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\",\"annotations\":[],\"logprobs\":[]}]}]}}\n\n",
	)
	defer server.Close()

	provider := newTestVLLMResponsesStreamProvider(t, false, false)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := testVLLMKey(server.URL)

	stream, bifrostErr := provider.ResponsesStream(ctx, noopPostHookRunner, nil, key, testVLLMResponsesStreamRequest())
	if bifrostErr != nil {
		t.Fatalf("ResponsesStream returned error synchronously: %v", bifrostErr.Error.Message)
	}

	chunks := drainStreamWithTimeout(t, stream, 5*time.Second)

	var deltaCount int
	var sawCompleted bool
	seenChunkIndexes := map[int]bool{}
	for _, c := range chunks {
		if c == nil {
			continue
		}
		if c.BifrostError != nil {
			t.Fatalf("unexpected error chunk: %s", c.BifrostError.Error.Message)
		}
		if c.BifrostResponsesStreamResponse == nil {
			continue
		}
		seenChunkIndexes[c.BifrostResponsesStreamResponse.ExtraFields.ChunkIndex] = true
		switch c.BifrostResponsesStreamResponse.Type {
		case schemas.ResponsesStreamResponseTypeCompleted:
			sawCompleted = true
		default:
			if c.BifrostResponsesStreamResponse.Delta != nil {
				deltaCount++
			}
		}
	}

	if deltaCount != 2 {
		t.Fatalf("expected 2 delta chunks to be forwarded (not silently dropped), got %d: %+v", deltaCount, chunks)
	}
	if !sawCompleted {
		t.Fatalf("expected a response.completed chunk, got %+v", chunks)
	}
	// ChunkIndex must track SequenceNumber, not stay stuck at 0 for every chunk.
	if len(seenChunkIndexes) < 2 {
		t.Fatalf("expected ChunkIndex to vary across chunks (regression: always 0), got indexes: %v", seenChunkIndexes)
	}
}
