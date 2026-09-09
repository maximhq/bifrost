package vllm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
