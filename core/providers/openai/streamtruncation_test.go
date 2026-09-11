package openai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// An upstream that dies mid-SSE closes its connection on a chunk boundary, which
// fasthttp reports as a plain io.EOF — byte-for-byte the same read result as a
// properly terminated body. These tests pin the semantic detection that replaces
// the impossible transport-level check: without a terminal marker
// ([DONE] / finish_reason / a terminal Responses event), the stream is truncated
// and must surface as an error instead of a synthetic clean completion.
// See https://github.com/maximhq/bifrost/issues/5546.

// truncatingSSEServer serves one streaming response that writes prelude (already
// SSE-framed) and then kills the connection without the terminating chunk.
// panic(http.ErrAbortHandler) is net/http's documented way to drop a connection
// mid-response without logging a stack trace, reproducing what an upstream whose
// generator raised does on the wire.
func truncatingSSEServer(t *testing.T, prelude string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("test server ResponseWriter is not an http.Flusher")
			return
		}
		flusher.Flush()
		if prelude != "" {
			if _, err := w.Write([]byte(prelude)); err != nil {
				t.Errorf("failed writing SSE prelude: %v", err)
				return
			}
			flusher.Flush()
		}
		panic(http.ErrAbortHandler)
	}))
}

// completeSSEServer serves a full, well-formed SSE stream.
func completeSSEServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("failed writing SSE body: %v", err)
		}
	}))
}

func newStreamTestProvider(baseURL string) *OpenAIProvider {
	return NewOpenAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: baseURL},
	}, testNoopLogger{})
}

// passthroughPostHook is the identity post-hook: streaming helpers require a
// runner, and these tests assert on what the provider produced, not on plugins.
func passthroughPostHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return resp, err
}

func newStreamTestContext() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
}

func testKey() schemas.Key {
	return schemas.Key{Value: *schemas.NewSecretVar("test-key")}
}

// collectChunks drains a provider stream, failing the test if it does not close
// in time (a stuck stream is itself a regression worth surfacing loudly).
func collectChunks(t *testing.T, stream chan *schemas.BifrostStreamChunk) []*schemas.BifrostStreamChunk {
	t.Helper()
	var chunks []*schemas.BifrostStreamChunk
	timeout := time.NewTimer(20 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case chunk, ok := <-stream:
			if !ok {
				return chunks
			}
			if chunk != nil {
				chunks = append(chunks, chunk)
			}
		case <-timeout.C:
			t.Fatal("timed out waiting for the provider stream to close")
			return chunks
		}
	}
}

// assertTruncationError checks the error carries the retryable upstream-connection
// shape. IsBifrostError must stay false and the status 502 so that
// executeRequestWithRetries retries / falls back instead of breaking out early.
func assertTruncationError(t *testing.T, err *schemas.BifrostError) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a truncation error, got nil")
	}
	if err.IsBifrostError {
		t.Error("truncation error must have IsBifrostError=false so the retry loop does not break early")
	}
	if err.StatusCode == nil || *err.StatusCode != 502 {
		t.Errorf("expected StatusCode 502, got %v", err.StatusCode)
	}
	if err.Error == nil || err.Error.Message != schemas.ErrProviderStreamTruncated {
		t.Errorf("expected message %q, got %+v", schemas.ErrProviderStreamTruncated, err.Error)
	}
	if err.Error != nil && (err.Error.Type == nil || *err.Error.Type != schemas.ProviderConnectionFailed) {
		t.Errorf("expected error type %q, got %v", schemas.ProviderConnectionFailed, err.Error.Type)
	}
}

func basicChatRequest() *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "repro-model",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")},
		}},
	}
}

func chatChunk(content string, finishReason *string) string {
	delta := `{}`
	if content != "" {
		delta = `{"content":"` + content + `"}`
	}
	finish := "null"
	if finishReason != nil {
		finish = `"` + *finishReason + `"`
	}
	return `data: {"id":"chatcmpl-repro","object":"chat.completion.chunk","created":1,"model":"repro-model",` +
		`"choices":[{"index":0,"delta":` + delta + `,"finish_reason":` + finish + `}]}` + "\n\n"
}

// Pre-first-byte death: nothing has reached the client yet, so the error must be
// the very first chunk. That is what lets CheckFirstStreamChunkForError convert it
// into a synchronous error and give the transport a real non-2xx status.
func TestChatStreamTruncatedPreFirstByte(t *testing.T) {
	server := truncatingSSEServer(t, "")
	defer server.Close()

	provider := newStreamTestProvider(server.URL)
	stream, bifrostErr := provider.ChatCompletionStream(newStreamTestContext(), passthroughPostHook, nil, testKey(), basicChatRequest())
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	chunks := collectChunks(t, stream)
	if len(chunks) != 1 {
		t.Fatalf("expected exactly one chunk (the error), got %d: %+v", len(chunks), chunks)
	}
	assertTruncationError(t, chunks[0].BifrostError)
	if chunks[0].BifrostChatResponse != nil {
		t.Error("no synthetic chat chunk may be emitted for a truncated stream")
	}
}

// Mid-stream death: already-forwarded content stays, then the error frame lands.
// Bifrost must not append the content-free terminal chunk that made the failure
// look like a normal short completion.
func TestChatStreamTruncatedMidStream(t *testing.T) {
	server := truncatingSSEServer(t, chatChunk("partial answer", nil))
	defer server.Close()

	provider := newStreamTestProvider(server.URL)
	stream, bifrostErr := provider.ChatCompletionStream(newStreamTestContext(), passthroughPostHook, nil, testKey(), basicChatRequest())
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	chunks := collectChunks(t, stream)
	if len(chunks) != 2 {
		t.Fatalf("expected the content chunk plus one error chunk, got %d: %+v", len(chunks), chunks)
	}
	if chunks[0].BifrostChatResponse == nil {
		t.Fatalf("expected the first chunk to be the forwarded content, got %+v", chunks[0])
	}
	assertTruncationError(t, chunks[1].BifrostError)
}

// Guard against false positives: a well-formed stream must still get its
// synthesized terminal chunk and no error.
func TestChatStreamCleanDoneUnaffected(t *testing.T) {
	stop := "stop"
	server := completeSSEServer(t, chatChunk("hello", nil)+chatChunk("", &stop)+"data: [DONE]\n\n")
	defer server.Close()

	provider := newStreamTestProvider(server.URL)
	stream, bifrostErr := provider.ChatCompletionStream(newStreamTestContext(), passthroughPostHook, nil, testKey(), basicChatRequest())
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	chunks := collectChunks(t, stream)
	if len(chunks) == 0 {
		t.Fatal("expected chunks from a well-formed stream")
	}
	for i, chunk := range chunks {
		if chunk.BifrostError != nil {
			t.Fatalf("chunk %d unexpectedly carried an error: %+v", i, chunk.BifrostError)
		}
	}
	final := chunks[len(chunks)-1]
	if final.BifrostChatResponse == nil {
		t.Fatalf("expected a synthesized final chat chunk, got %+v", final)
	}
	if len(final.BifrostChatResponse.Choices) == 0 ||
		final.BifrostChatResponse.Choices[0].FinishReason == nil ||
		*final.BifrostChatResponse.Choices[0].FinishReason != stop {
		t.Errorf("expected the final chunk to carry finish_reason %q, got %+v", stop, final.BifrostChatResponse.Choices)
	}
}

// [DONE] is a terminal marker in its own right: a provider that ends the stream
// properly but never sets finish_reason is not truncated.
func TestChatStreamDoneWithoutFinishReasonIsNotTruncated(t *testing.T) {
	server := completeSSEServer(t, chatChunk("hello", nil)+"data: [DONE]\n\n")
	defer server.Close()

	provider := newStreamTestProvider(server.URL)
	stream, bifrostErr := provider.ChatCompletionStream(newStreamTestContext(), passthroughPostHook, nil, testKey(), basicChatRequest())
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	for i, chunk := range collectChunks(t, stream) {
		if chunk.BifrostError != nil {
			t.Fatalf("chunk %d unexpectedly carried an error: %+v", i, chunk.BifrostError)
		}
	}
}

// finish_reason alone is terminal too — providers listed as not sending [DONE]
// (Cerebras, Perplexity) rely on exactly this.
func TestChatStreamFinishReasonWithoutDoneIsNotTruncated(t *testing.T) {
	stop := "stop"
	server := completeSSEServer(t, chatChunk("hello", nil)+chatChunk("", &stop))
	defer server.Close()

	provider := newStreamTestProvider(server.URL)
	stream, bifrostErr := provider.ChatCompletionStream(newStreamTestContext(), passthroughPostHook, nil, testKey(), basicChatRequest())
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	for i, chunk := range collectChunks(t, stream) {
		if chunk.BifrostError != nil {
			t.Fatalf("chunk %d unexpectedly carried an error: %+v", i, chunk.BifrostError)
		}
	}
}

// heartbeatSSEServer writes body and then keeps the connection alive with SSE
// comment frames until the client goes away. It never sends [DONE] and never
// closes: the shape an upstream has when it ends generation but leaves the
// connection parked. Write errors are swallowed rather than reported through t,
// since the handler outlives the test body once the client disconnects.
func heartbeatSSEServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			return
		}
		if _, err := w.Write([]byte(body)); err != nil {
			return
		}
		flusher.Flush()

		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				if _, err := w.Write([]byte(": ping\n\n")); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	}))
}

// https://github.com/maximhq/bifrost/issues/6784: heartbeat comments reset the raw-read
// idle timer before SSE framing is interpreted, so an upstream that finishes generating
// and then parks the connection holds the read loop open forever — the client waits on a
// finish_reason chunk that was already received. custom_provider_config.does_not_send_done_marker
// is the operator's declaration that this upstream ends on finish_reason, and core stamps it
// on the context per attempt.
func TestChatStreamHeartbeatAfterFinishReasonEndsOnOptIn(t *testing.T) {
	toolCalls := "tool_calls"
	server := heartbeatSSEServer(t, chatChunk("hello", nil)+chatChunk("", &toolCalls))
	defer server.Close()

	ctx := newStreamTestContext()
	ctx.SetValue(schemas.BifrostContextKeyDoesNotSendDoneMarker, true)

	provider := newStreamTestProvider(server.URL)
	stream, bifrostErr := provider.ChatCompletionStream(ctx, passthroughPostHook, nil, testKey(), basicChatRequest())
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	chunks := collectChunks(t, stream)
	if len(chunks) == 0 {
		t.Fatal("expected chunks from a stream that reached finish_reason")
	}
	for i, chunk := range chunks {
		if chunk.BifrostError != nil {
			t.Fatalf("chunk %d unexpectedly carried an error: %+v", i, chunk.BifrostError)
		}
	}
	final := chunks[len(chunks)-1]
	if final.BifrostChatResponse == nil {
		t.Fatalf("expected a synthesized final chat chunk, got %+v", final)
	}
	if len(final.BifrostChatResponse.Choices) == 0 ||
		final.BifrostChatResponse.Choices[0].FinishReason == nil ||
		*final.BifrostChatResponse.Choices[0].FinishReason != toolCalls {
		t.Errorf("expected the final chunk to carry finish_reason %q, got %+v", toolCalls, final.BifrostChatResponse.Choices)
	}
}

// The opt-in only reaches providers an operator has already diagnosed. The reported
// shape - a built-in or unconfigured custom provider that omits [DONE] and parks -
// has to terminate on its own, so the first heartbeat after finish_reason ends the
// read loop with no configuration at all.
func TestChatStreamHeartbeatAfterFinishReasonEndsWithoutOptIn(t *testing.T) {
	toolCalls := "tool_calls"
	server := heartbeatSSEServer(t, chatChunk("hello", nil)+chatChunk("", &toolCalls))
	defer server.Close()

	provider := newStreamTestProvider(server.URL)
	stream, bifrostErr := provider.ChatCompletionStream(newStreamTestContext(), passthroughPostHook, nil, testKey(), basicChatRequest())
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	chunks := collectChunks(t, stream)
	if len(chunks) == 0 {
		t.Fatal("expected chunks from a stream that reached finish_reason")
	}
	for i, chunk := range chunks {
		if chunk.BifrostError != nil {
			t.Fatalf("chunk %d unexpectedly carried an error: %+v", i, chunk.BifrostError)
		}
	}
	final := chunks[len(chunks)-1]
	if final.BifrostChatResponse == nil {
		t.Fatalf("expected a synthesized final chat chunk, got %+v", final)
	}
	if len(final.BifrostChatResponse.Choices) == 0 ||
		final.BifrostChatResponse.Choices[0].FinishReason == nil ||
		*final.BifrostChatResponse.Choices[0].FinishReason != toolCalls {
		t.Errorf("expected the final chunk to carry finish_reason %q, got %+v", toolCalls, final.BifrostChatResponse.Choices)
	}
}

// The reported upstream sends its usage-only chunk between finish_reason and the
// heartbeats. Ending on the heartbeat rather than on finish_reason is what keeps
// that chunk - and the cost derived from it - on a stream nobody configured.
func TestChatStreamHeartbeatAfterFinishReasonKeepsTrailingUsage(t *testing.T) {
	toolCalls := "tool_calls"
	usageChunk := `data: {"id":"chatcmpl-repro","object":"chat.completion.chunk","created":1,"model":"repro-model",` +
		`"choices":[],"usage":{"prompt_tokens":1000,"completion_tokens":100,"total_tokens":1100}}` + "\n\n"
	server := heartbeatSSEServer(t, chatChunk("hello", nil)+chatChunk("", &toolCalls)+usageChunk)
	defer server.Close()

	provider := newStreamTestProvider(server.URL)
	stream, bifrostErr := provider.ChatCompletionStream(newStreamTestContext(), passthroughPostHook, nil, testKey(), basicChatRequest())
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	chunks := collectChunks(t, stream)
	if len(chunks) == 0 {
		t.Fatal("expected chunks from a stream that reached finish_reason")
	}
	final := chunks[len(chunks)-1]
	if final.BifrostChatResponse == nil || final.BifrostChatResponse.Usage == nil {
		t.Fatalf("expected the final chunk to carry the trailing usage, got %+v", final)
	}
	if final.BifrostChatResponse.Usage.TotalTokens != 1100 {
		t.Errorf("expected total_tokens 1100 from the chunk after finish_reason, got %d", final.BifrostChatResponse.Usage.TotalTokens)
	}
}

// The text-completion loop terminates on the same switch, so the opt-in has to
// reach it too.
func TestTextCompletionStreamHeartbeatAfterFinishReasonEndsOnOptIn(t *testing.T) {
	server := heartbeatSSEServer(t, `data: {"id":"cmpl-repro","object":"text_completion","created":1,"model":"repro-model","choices":[{"index":0,"text":"hello","finish_reason":null}]}`+"\n\n"+
		`data: {"id":"cmpl-repro","object":"text_completion","created":1,"model":"repro-model","choices":[{"index":0,"text":"","finish_reason":"stop"}]}`+"\n\n")
	defer server.Close()

	ctx := newStreamTestContext()
	ctx.SetValue(schemas.BifrostContextKeyDoesNotSendDoneMarker, true)

	provider := newStreamTestProvider(server.URL)
	request := &schemas.BifrostTextCompletionRequest{
		Provider: schemas.OpenAI,
		Model:    "repro-model",
		Input:    &schemas.TextCompletionInput{PromptStr: schemas.Ptr("hi")},
	}
	stream, bifrostErr := provider.TextCompletionStream(ctx, passthroughPostHook, nil, testKey(), request)
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	chunks := collectChunks(t, stream)
	if len(chunks) == 0 {
		t.Fatal("expected chunks from a stream that reached finish_reason")
	}
	for i, chunk := range chunks {
		if chunk.BifrostError != nil {
			t.Fatalf("chunk %d unexpectedly carried an error: %+v", i, chunk.BifrostError)
		}
	}
	if chunks[len(chunks)-1].BifrostTextCompletionResponse == nil {
		t.Fatalf("expected a synthesized final text completion chunk, got %+v", chunks[len(chunks)-1])
	}
}

func TestTextCompletionStreamTruncated(t *testing.T) {
	server := truncatingSSEServer(t, `data: {"id":"cmpl-repro","object":"text_completion","created":1,"model":"repro-model","choices":[{"index":0,"text":"partial"}]}`+"\n\n")
	defer server.Close()

	provider := newStreamTestProvider(server.URL)
	request := &schemas.BifrostTextCompletionRequest{
		Provider: schemas.OpenAI,
		Model:    "repro-model",
		Input:    &schemas.TextCompletionInput{PromptStr: schemas.Ptr("hi")},
	}
	stream, bifrostErr := provider.TextCompletionStream(newStreamTestContext(), passthroughPostHook, nil, testKey(), request)
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	chunks := collectChunks(t, stream)
	if len(chunks) == 0 {
		t.Fatal("expected at least the error chunk")
	}
	final := chunks[len(chunks)-1]
	assertTruncationError(t, final.BifrostError)
	if final.BifrostTextCompletionResponse != nil {
		t.Error("no synthetic text completion chunk may be emitted for a truncated stream")
	}
}

// The Responses loop returns as soon as a terminal event arrives, so a stream
// that ends without one previously closed the channel silently — indistinguishable
// to the client from a stream that simply stopped emitting events.
func TestResponsesStreamTruncatedBeforeCompleted(t *testing.T) {
	server := truncatingSSEServer(t, "event: response.output_text.delta\n"+
		`data: {"type":"response.output_text.delta","sequence_number":1,"delta":"partial"}`+"\n\n")
	defer server.Close()

	provider := newStreamTestProvider(server.URL)
	request := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "repro-model",
		Input: []schemas.ResponsesMessage{{
			Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")},
		}},
	}
	stream, bifrostErr := provider.ResponsesStream(newStreamTestContext(), passthroughPostHook, nil, testKey(), request)
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	chunks := collectChunks(t, stream)
	if len(chunks) == 0 {
		t.Fatal("expected at least the error chunk")
	}
	assertTruncationError(t, chunks[len(chunks)-1].BifrostError)
}

// Azure/OpenAI Responses can accept a request with HTTP 200, emit lifecycle
// events, and then report a semantic failure in a terminal SSE event. The
// transport status is already committed, so the shared decoder must preserve
// the nested provider details in the semantic error event.
func TestResponsesStreamAzureStyleErrorEvent(t *testing.T) {
	server := completeSSEServer(t,
		`data: {"type":"response.created","sequence_number":0,"response":{"id":"r1","object":"response","created_at":1,"model":"repro-model","status":"in_progress"}}

`+
			`data: {"type":"error","sequence_number":1,"error":{"type":"too_many_requests","code":"no_capacity","message":"The service is temporarily unable to process this request."}}

`)
	defer server.Close()

	provider := newStreamTestProvider(server.URL)
	request := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "repro-model",
		Input: []schemas.ResponsesMessage{{
			Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")},
		}},
	}
	stream, bifrostErr := provider.ResponsesStream(newStreamTestContext(), passthroughPostHook, nil, testKey(), request)
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	var got *schemas.BifrostError
	for _, chunk := range collectChunks(t, stream) {
		if chunk.BifrostError != nil {
			got = chunk.BifrostError
			break
		}
	}
	if got == nil || got.Error == nil {
		t.Fatalf("expected an in-band Azure error chunk, got nil")
	}
	if got.Error.Type == nil || *got.Error.Type != "too_many_requests" {
		t.Fatalf("error type = %#v, want too_many_requests", got.Error.Type)
	}
	if got.Error.Code == nil || *got.Error.Code != "no_capacity" {
		t.Fatalf("error code = %#v, want no_capacity", got.Error.Code)
	}
	if got.Error.Message == "" {
		t.Fatal("expected non-empty Azure stream error message")
	}
}

// chatChunkNullDeltaFinish reproduces the terminal-chunk shape some OpenAI-compatible
// upstreams send: "delta" is null (or absent) alongside "finish_reason", rather than
// an empty object. ToBifrostResponsesStreamResponse must not discard finish_reason
// just because delta is nil, or the Responses->Chat-Completions fallback below never
// produces a Completed event and the stream closes with no usage/stop_reason at all.
func chatChunkNullDeltaFinish(finishReason string) string {
	return `data: {"id":"chatcmpl-repro","object":"chat.completion.chunk","created":1,"model":"repro-model",` +
		`"choices":[{"index":0,"delta":null,"finish_reason":"` + finishReason + `"}],` +
		`"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}` + "\n\n"
}

// A Responses request that falls back to Chat Completions (because the custom
// provider config disables native Responses but allows Chat Completions) must still
// produce a completed Responses stream event with usage/stop_reason, even when the
// upstream's terminal chunk carries "delta":null alongside "finish_reason".
func TestResponsesStreamFallbackNullDeltaFinishStillCompletes(t *testing.T) {
	server := completeSSEServer(t, chatChunk("hello", nil)+chatChunkNullDeltaFinish("stop")+"data: [DONE]\n\n")
	defer server.Close()

	provider := NewOpenAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL},
		CustomProviderConfig: &schemas.CustomProviderConfig{
			AllowedRequests: &schemas.AllowedRequests{
				ChatCompletionStream: true,
				ResponsesStream:      false,
			},
		},
	}, testNoopLogger{})

	request := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "repro-model",
		Input: []schemas.ResponsesMessage{{
			Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")},
		}},
	}
	stream, bifrostErr := provider.ResponsesStream(newStreamTestContext(), passthroughPostHook, nil, testKey(), request)
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	var completed *schemas.BifrostResponsesStreamResponse
	for _, chunk := range collectChunks(t, stream) {
		if chunk.BifrostError != nil {
			t.Fatalf("unexpected error chunk: %+v", chunk.BifrostError)
		}
		if chunk.BifrostResponsesStreamResponse != nil && chunk.BifrostResponsesStreamResponse.Type == schemas.ResponsesStreamResponseTypeCompleted {
			completed = chunk.BifrostResponsesStreamResponse
		}
	}

	if completed == nil {
		t.Fatal("expected a completed Responses stream event; stream closed silently instead")
	}
	if completed.Response == nil || completed.Response.Usage == nil {
		t.Fatal("expected completed event to carry usage")
	}
	if completed.Response.StopReason == nil || *completed.Response.StopReason != "stop" {
		t.Fatalf("expected stop_reason stop, got %+v", completed.Response.StopReason)
	}
}

// A non-EOF read error is already a reported failure. The truncation guard that
// follows the read loop must not fire a second time for the same dead stream, or
// the client sees two errors for one request and the retryable 502 synthesized by
// SendStreamTruncatedError muddies which failure the retry logic reacted to. The
// handler signals "already reported" by latching BifrostContextKeyStreamEndIndicator.

// failingSSEDataReader yields queued data lines and then a non-EOF error,
// reproducing an upstream whose SSE framing breaks mid-body (what bufio.Scanner
// surfaces when a line exceeds its ceiling). It deliberately implements
// SSEStreamTerminator returning false, so the post-loop truncation guard is live -
// that is precisely the condition under which a handler that forgets to latch the
// end indicator emits a duplicate.
type failingSSEDataReader struct {
	lines [][]byte
	err   error
}

func (r *failingSSEDataReader) ReadDataLine() ([]byte, error) {
	if len(r.lines) == 0 {
		return nil, r.err
	}
	line := r.lines[0]
	r.lines = r.lines[1:]
	return line, nil
}

func (r *failingSSEDataReader) SawDoneMarker() bool { return false }

// contextWithFailingSSEReader injects a reader that replays lines then fails.
// BifrostContextKeySSEReaderFactory is the same seam enterprise uses to swap in a
// streaming reader, so this drives the real handler loop rather than a stub of it.
func contextWithFailingSSEReader(lines ...string) *schemas.BifrostContext {
	ctx := newStreamTestContext()
	payloads := make([][]byte, 0, len(lines))
	for _, line := range lines {
		payloads = append(payloads, []byte(line))
	}
	ctx.SetValue(schemas.BifrostContextKeySSEReaderFactory, &providerUtils.SSEReaderFactory{
		NewDataReader: func(io.Reader) providerUtils.SSEDataReader {
			return &failingSSEDataReader{
				lines: payloads,
				err:   errors.New("sse framing error"),
			}
		},
	})
	return ctx
}

// assertSingleReadError checks the stream carried exactly one error, and that it
// is the read error rather than the synthesized truncation 502.
func assertSingleReadError(t *testing.T, chunks []*schemas.BifrostStreamChunk) {
	t.Helper()
	var errored []*schemas.BifrostError
	for _, chunk := range chunks {
		if chunk.BifrostError != nil {
			errored = append(errored, chunk.BifrostError)
		}
	}
	if len(errored) != 1 {
		for i, err := range errored {
			t.Logf("error %d: %+v", i, err.Error)
		}
		t.Fatalf("expected exactly one error for one dead stream, got %d", len(errored))
	}
	if errored[0].Error == nil {
		t.Fatal("error chunk carried no error field")
	}
	if errored[0].Error.Message == schemas.ErrProviderStreamTruncated {
		t.Error("expected the read error to be reported, not the synthesized truncation error")
	}
}

const openAIPartialImageEvent = `{"type":"image_generation.partial_image","b64_json":"aGk=","partial_image_index":0,"sequence_number":1}`

const openAIPartialImageEditEvent = `{"type":"image_edit.partial_image","b64_json":"aGk=","partial_image_index":0,"sequence_number":1}`

func TestImageGenerationStreamReadErrorReportsOnce(t *testing.T) {
	server := completeSSEServer(t, "data: "+openAIPartialImageEvent+"\n\n")
	defer server.Close()

	provider := newStreamTestProvider(server.URL)
	request := &schemas.BifrostImageGenerationRequest{
		Provider: schemas.OpenAI,
		Model:    "repro-model",
		Input:    &schemas.ImageGenerationInput{Prompt: "a cat"},
	}
	stream, bifrostErr := provider.ImageGenerationStream(
		contextWithFailingSSEReader(openAIPartialImageEvent), passthroughPostHook, nil, testKey(), request)
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	assertSingleReadError(t, collectChunks(t, stream))
}

func TestImageEditStreamReadErrorReportsOnce(t *testing.T) {
	server := completeSSEServer(t, "data: "+openAIPartialImageEditEvent+"\n\n")
	defer server.Close()

	provider := newStreamTestProvider(server.URL)
	request := &schemas.BifrostImageEditRequest{
		Provider: schemas.OpenAI,
		Model:    "repro-model",
		Input: &schemas.ImageEditInput{
			Prompt: "make it blue",
			Images: []schemas.ImageInput{{Image: []byte("fake-png-bytes")}},
		},
	}
	stream, bifrostErr := provider.ImageEditStream(
		contextWithFailingSSEReader(openAIPartialImageEditEvent), passthroughPostHook, nil, testKey(), request)
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	assertSingleReadError(t, collectChunks(t, stream))
}
