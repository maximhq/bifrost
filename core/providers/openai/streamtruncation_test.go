package openai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
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

// chatUsageOnlyChunk is the frame OpenAI-compatible upstreams send after finish_reason
// when stream_options.include_usage is set - which Bifrost sets unconditionally. It has
// no choices, so it never reaches the finish_reason branch of the read loop.
const chatUsageOnlyChunk = `data: {"id":"chatcmpl-repro","object":"chat.completion.chunk","created":1,"model":"repro-model",` +
	`"choices":[],"usage":{"prompt_tokens":1000,"completion_tokens":100,"total_tokens":1100}}` + "\n\n"

// https://github.com/maximhq/bifrost/issues/7143: does_not_send_done_marker ends the read
// loop on finish_reason, which lands before the trailing usage-only frame and so bills the
// request at zero tokens. custom_provider_config.wait_for_usage is the operator's statement
// that this upstream does send that frame, so the loop keeps reading until it arrives.
func TestChatStreamOptInWithWaitForUsageKeepsTrailingUsage(t *testing.T) {
	toolCalls := "tool_calls"
	server := heartbeatSSEServer(t, chatChunk("hello", nil)+chatChunk("", &toolCalls)+chatUsageOnlyChunk)
	defer server.Close()

	ctx := newStreamTestContext()
	ctx.SetValue(schemas.BifrostContextKeyDoesNotSendDoneMarker, true)
	ctx.SetValue(schemas.BifrostContextKeyWaitForUsage, true)

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
	if final.BifrostChatResponse == nil || final.BifrostChatResponse.Usage == nil {
		t.Fatalf("expected the final chunk to carry the trailing usage, got %+v", final)
	}
	if final.BifrostChatResponse.Usage.TotalTokens != 1100 {
		t.Errorf("expected total_tokens 1100 from the chunk after finish_reason, got %d", final.BifrostChatResponse.Usage.TotalTokens)
	}
	if len(final.BifrostChatResponse.Choices) == 0 ||
		final.BifrostChatResponse.Choices[0].FinishReason == nil ||
		*final.BifrostChatResponse.Choices[0].FinishReason != toolCalls {
		t.Errorf("expected the final chunk to carry finish_reason %q, got %+v", toolCalls, final.BifrostChatResponse.Choices)
	}
}

// Without wait_for_usage the opt-in keeps breaking on finish_reason. That discards the
// trailing usage frame, which is the documented cost of the flag - pinned here so the
// default can never change silently for operators who did not opt in.
func TestChatStreamOptInWithoutWaitForUsageStillBreaksOnFinishReason(t *testing.T) {
	toolCalls := "tool_calls"
	server := heartbeatSSEServer(t, chatChunk("hello", nil)+chatChunk("", &toolCalls)+chatUsageOnlyChunk)
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
	final := chunks[len(chunks)-1]
	if final.BifrostChatResponse == nil {
		t.Fatalf("expected a synthesized final chat chunk, got %+v", final)
	}
	if final.BifrostChatResponse.Usage != nil && final.BifrostChatResponse.Usage.TotalTokens != 0 {
		t.Errorf("expected the opt-in without wait_for_usage to stop before the usage frame, got total_tokens %d",
			final.BifrostChatResponse.Usage.TotalTokens)
	}
}

// wait_for_usage must not turn into an unbounded wait. An upstream that sends
// finish_reason and then goes completely silent - no usage, no heartbeat, no close -
// ends on network_config.stream_idle_timeout_in_seconds through the #7108 path, with
// the buffered finish_reason and no error chunk.
// A usage-only frame carries choices: [], so it is accumulated and then skipped by the
// empty-choices `continue` - which sits *above* the no-[DONE] termination check. Under
// wait_for_usage that frame is the exact thing the loop is waiting for, so skipping it
// leaves a request that is semantically finished blocked on the next read: termination
// falls through to a heartbeat pair, EOF, or stream_idle_timeout_in_seconds.
//
// Asserting the usage total alone cannot catch this - it passes either way, since the
// usage was accumulated before the stall. The defect is the *wait*, so the assertion has
// to be the wait: this upstream sends every frame and then goes silent forever, so a
// correct loop returns immediately and a stalled one returns only when the idle timer
// fires.
func TestChatStreamWaitForUsageTerminatesOnUsageFrameNotIdleTimeout(t *testing.T) {
	stop := "stop"
	server := silentParkSSEServer(t, chatChunk("hello", nil)+chatChunk("", &stop)+chatUsageOnlyChunk)
	defer server.Close()

	const idleTimeout = 3 * time.Second

	ctx := newStreamTestContext()
	ctx.SetValue(schemas.BifrostContextKeyDoesNotSendDoneMarker, true)
	ctx.SetValue(schemas.BifrostContextKeyWaitForUsage, true)
	ctx.SetValue(schemas.BifrostContextKeyStreamIdleTimeout, idleTimeout)

	provider := newStreamTestProvider(server.URL)
	started := time.Now()
	stream, bifrostErr := provider.ChatCompletionStream(ctx, passthroughPostHook, nil, testKey(), basicChatRequest())
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}
	chunks := collectChunks(t, stream)
	elapsed := time.Since(started)

	final := chunks[len(chunks)-1]
	if final.BifrostChatResponse == nil || final.BifrostChatResponse.Usage == nil {
		t.Fatalf("expected the final chunk to carry the trailing usage, got %+v", final)
	}
	if final.BifrostChatResponse.Usage.TotalTokens != 1100 {
		t.Errorf("expected total_tokens 1100, got %d", final.BifrostChatResponse.Usage.TotalTokens)
	}
	// Generous bound: the point is idle-timer vs immediate, not a precise budget.
	if elapsed > idleTimeout/3 {
		t.Errorf("stream took %v; the usage frame should end it immediately, not wait out the %v idle timeout", elapsed, idleTimeout)
	}
}

// The text-completion loop skips the termination check on the same empty-choices
// `continue`, so it needs the same guarantee.
func TestTextCompletionStreamWaitForUsageTerminatesOnUsageFrameNotIdleTimeout(t *testing.T) {
	server := silentParkSSEServer(t, `data: {"id":"cmpl-repro","object":"text_completion","created":1,"model":"repro-model","choices":[{"index":0,"text":"hello","finish_reason":null}]}`+"\n\n"+
		`data: {"id":"cmpl-repro","object":"text_completion","created":1,"model":"repro-model","choices":[{"index":0,"text":"","finish_reason":"stop"}]}`+"\n\n"+
		`data: {"id":"cmpl-repro","object":"text_completion","created":1,"model":"repro-model","choices":[],"usage":{"prompt_tokens":1000,"completion_tokens":100,"total_tokens":1100}}`+"\n\n")
	defer server.Close()

	const idleTimeout = 3 * time.Second

	ctx := newStreamTestContext()
	ctx.SetValue(schemas.BifrostContextKeyDoesNotSendDoneMarker, true)
	ctx.SetValue(schemas.BifrostContextKeyWaitForUsage, true)
	ctx.SetValue(schemas.BifrostContextKeyStreamIdleTimeout, idleTimeout)

	provider := newStreamTestProvider(server.URL)
	request := &schemas.BifrostTextCompletionRequest{
		Provider: schemas.OpenAI,
		Model:    "repro-model",
		Input:    &schemas.TextCompletionInput{PromptStr: schemas.Ptr("hi")},
	}
	started := time.Now()
	stream, bifrostErr := provider.TextCompletionStream(ctx, passthroughPostHook, nil, testKey(), request)
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}
	chunks := collectChunks(t, stream)
	elapsed := time.Since(started)

	final := chunks[len(chunks)-1]
	if final.BifrostTextCompletionResponse == nil || final.BifrostTextCompletionResponse.Usage == nil {
		t.Fatalf("expected the final chunk to carry the trailing usage, got %+v", final)
	}
	if final.BifrostTextCompletionResponse.Usage.TotalTokens != 1100 {
		t.Errorf("expected total_tokens 1100, got %d", final.BifrostTextCompletionResponse.Usage.TotalTokens)
	}
	if elapsed > idleTimeout/3 {
		t.Errorf("stream took %v; the usage frame should end it immediately, not wait out the %v idle timeout", elapsed, idleTimeout)
	}
}

// Some OpenAI-compatible upstreams report usage incrementally rather than only in the
// trailing frame - vLLM exposes stream_continuous_usage_stats for exactly that, and the
// accumulation block above carries a pre-existing comment saying "in some cases usage
// comes before final message". Usage is processed before finish_reason is assigned, so a
// preliminary frame would otherwise satisfy wait_for_usage and let the finish frame end
// the stream before the authoritative trailing total arrives - reintroducing #7143 through
// a different door. Only usage observed at or after finish_reason ends the wait.
func TestChatStreamWaitForUsageIgnoresPreliminaryUsageBeforeFinish(t *testing.T) {
	preliminary := `data: {"id":"chatcmpl-repro","object":"chat.completion.chunk","created":1,"model":"repro-model",` +
		`"choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}],` +
		`"usage":{"prompt_tokens":10,"completion_tokens":1,"total_tokens":11}}` + "\n\n"
	stop := "stop"
	server := silentParkSSEServer(t, preliminary+chatChunk("", &stop)+chatUsageOnlyChunk)
	defer server.Close()

	ctx := newStreamTestContext()
	ctx.SetValue(schemas.BifrostContextKeyDoesNotSendDoneMarker, true)
	ctx.SetValue(schemas.BifrostContextKeyWaitForUsage, true)
	ctx.SetValue(schemas.BifrostContextKeyStreamIdleTimeout, 3*time.Second)

	provider := newStreamTestProvider(server.URL)
	stream, bifrostErr := provider.ChatCompletionStream(ctx, passthroughPostHook, nil, testKey(), basicChatRequest())
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}
	chunks := collectChunks(t, stream)

	final := chunks[len(chunks)-1]
	if final.BifrostChatResponse == nil || final.BifrostChatResponse.Usage == nil {
		t.Fatalf("expected the final chunk to carry usage, got %+v", final)
	}
	if got := final.BifrostChatResponse.Usage.TotalTokens; got != 1100 {
		t.Errorf("expected the authoritative trailing total_tokens 1100, got %d (a preliminary usage frame ended the wait early)", got)
	}
}

// The text-completion loop orders usage before finish_reason identically.
func TestTextCompletionStreamWaitForUsageIgnoresPreliminaryUsageBeforeFinish(t *testing.T) {
	server := silentParkSSEServer(t, `data: {"id":"cmpl-repro","object":"text_completion","created":1,"model":"repro-model","choices":[{"index":0,"text":"hello","finish_reason":null}],"usage":{"prompt_tokens":10,"completion_tokens":1,"total_tokens":11}}`+"\n\n"+
		`data: {"id":"cmpl-repro","object":"text_completion","created":1,"model":"repro-model","choices":[{"index":0,"text":"","finish_reason":"stop"}]}`+"\n\n"+
		`data: {"id":"cmpl-repro","object":"text_completion","created":1,"model":"repro-model","choices":[],"usage":{"prompt_tokens":1000,"completion_tokens":100,"total_tokens":1100}}`+"\n\n")
	defer server.Close()

	ctx := newStreamTestContext()
	ctx.SetValue(schemas.BifrostContextKeyDoesNotSendDoneMarker, true)
	ctx.SetValue(schemas.BifrostContextKeyWaitForUsage, true)
	ctx.SetValue(schemas.BifrostContextKeyStreamIdleTimeout, 3*time.Second)

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

	final := chunks[len(chunks)-1]
	if final.BifrostTextCompletionResponse == nil || final.BifrostTextCompletionResponse.Usage == nil {
		t.Fatalf("expected the final chunk to carry usage, got %+v", final)
	}
	if got := final.BifrostTextCompletionResponse.Usage.TotalTokens; got != 1100 {
		t.Errorf("expected the authoritative trailing total_tokens 1100, got %d (a preliminary usage frame ended the wait early)", got)
	}
}

// The mirror of the case above: an upstream that puts usage ON the finish frame has
// nothing further to send, so the wait must end there rather than hold the stream to the
// idle timeout. Green both before and after the gating change - it pins that tightening
// the rule to "at or after finish_reason" did not turn the same-frame shape into a stall.
func TestChatStreamWaitForUsageAcceptsUsageOnTheFinishFrame(t *testing.T) {
	finishWithUsage := `data: {"id":"chatcmpl-repro","object":"chat.completion.chunk","created":1,"model":"repro-model",` +
		`"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":1000,"completion_tokens":100,"total_tokens":1100}}` + "\n\n"
	const idleTimeout = 3 * time.Second
	server := silentParkSSEServer(t, chatChunk("hello", nil)+finishWithUsage)
	defer server.Close()

	ctx := newStreamTestContext()
	ctx.SetValue(schemas.BifrostContextKeyDoesNotSendDoneMarker, true)
	ctx.SetValue(schemas.BifrostContextKeyWaitForUsage, true)
	ctx.SetValue(schemas.BifrostContextKeyStreamIdleTimeout, idleTimeout)

	provider := newStreamTestProvider(server.URL)
	started := time.Now()
	stream, bifrostErr := provider.ChatCompletionStream(ctx, passthroughPostHook, nil, testKey(), basicChatRequest())
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}
	chunks := collectChunks(t, stream)
	elapsed := time.Since(started)

	final := chunks[len(chunks)-1]
	if final.BifrostChatResponse == nil || final.BifrostChatResponse.Usage == nil {
		t.Fatalf("expected the final chunk to carry usage, got %+v", final)
	}
	if got := final.BifrostChatResponse.Usage.TotalTokens; got != 1100 {
		t.Errorf("expected total_tokens 1100 from the finish frame, got %d", got)
	}
	if elapsed > idleTimeout/3 {
		t.Errorf("stream took %v; usage on the finish frame should end the wait immediately", elapsed)
	}
}

func TestChatStreamWaitForUsageSilentParkEndsOnIdleTimeout(t *testing.T) {
	stop := "stop"
	server := silentParkSSEServer(t, chatChunk("hello", nil)+chatChunk("", &stop))
	defer server.Close()

	const idleTimeout = 300 * time.Millisecond

	ctx := newStreamTestContext()
	ctx.SetValue(schemas.BifrostContextKeyDoesNotSendDoneMarker, true)
	ctx.SetValue(schemas.BifrostContextKeyWaitForUsage, true)
	ctx.SetValue(schemas.BifrostContextKeyStreamIdleTimeout, idleTimeout)

	provider := newStreamTestProvider(server.URL)
	started := time.Now()
	stream, bifrostErr := provider.ChatCompletionStream(ctx, passthroughPostHook, nil, testKey(), basicChatRequest())
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	chunks := collectChunks(t, stream)
	elapsed := time.Since(started)
	if len(chunks) == 0 {
		t.Fatal("expected chunks from a stream that reached finish_reason")
	}
	for i, chunk := range chunks {
		if chunk.BifrostError != nil {
			t.Fatalf("chunk %d unexpectedly carried an error: %+v", i, chunk.BifrostError.Error)
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
	// The outcome above is identical whether the stream waited or ended at finish_reason,
	// so assert the wait itself. Lower bound only: a Go timer fires after at least its
	// duration, never early, so this cannot flake on a slow machine - it fails only if
	// something other than the idle timer ended a stream that still had usage to wait for.
	// time.Since subtracts on the monotonic clock, so a wall-clock change cannot fool it.
	if elapsed < idleTimeout/2 {
		t.Errorf("stream returned in %v; with wait_for_usage and no usage frame it must wait out the %v idle timeout, not end at finish_reason", elapsed, idleTimeout)
	}
}

// The text-completion loop breaks on the same switch and accumulates usage the same
// way, so wait_for_usage has to reach it too.
func TestTextCompletionStreamOptInWithWaitForUsageKeepsTrailingUsage(t *testing.T) {
	server := heartbeatSSEServer(t, `data: {"id":"cmpl-repro","object":"text_completion","created":1,"model":"repro-model","choices":[{"index":0,"text":"hello","finish_reason":null}]}`+"\n\n"+
		`data: {"id":"cmpl-repro","object":"text_completion","created":1,"model":"repro-model","choices":[{"index":0,"text":"","finish_reason":"stop"}]}`+"\n\n"+
		`data: {"id":"cmpl-repro","object":"text_completion","created":1,"model":"repro-model","choices":[],"usage":{"prompt_tokens":1000,"completion_tokens":100,"total_tokens":1100}}`+"\n\n")
	defer server.Close()

	ctx := newStreamTestContext()
	ctx.SetValue(schemas.BifrostContextKeyDoesNotSendDoneMarker, true)
	ctx.SetValue(schemas.BifrostContextKeyWaitForUsage, true)

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
	final := chunks[len(chunks)-1]
	if final.BifrostTextCompletionResponse == nil || final.BifrostTextCompletionResponse.Usage == nil {
		t.Fatalf("expected the final text completion chunk to carry the trailing usage, got %+v", final)
	}
	if final.BifrostTextCompletionResponse.Usage.TotalTokens != 1100 {
		t.Errorf("expected total_tokens 1100 from the chunk after finish_reason, got %d", final.BifrostTextCompletionResponse.Usage.TotalTokens)
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

// silentParkSSEServer writes body and then holds the connection open without
// sending another byte until the client goes away: no [DONE], no heartbeat
// comments, no close. This is the one parked-upstream shape neither the
// post-finish comment rule nor custom_provider_config.does_not_send_done_marker
// reaches, so stream_idle_timeout_in_seconds is the only thing that can end it.
func silentParkSSEServer(t *testing.T, body string) *httptest.Server {
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
		<-r.Context().Done()
	}))
}

// https://github.com/maximhq/bifrost/issues/7108: an upstream that omits [DONE] and
// parks silently after finish_reason has already delivered a complete response, so
// the idle timeout that finally unblocks the read must end the stream cleanly with
// the buffered finish_reason. Surfacing it as a read error tells the client a
// response it has fully received failed.
func TestChatStreamSilentParkAfterFinishReasonEndsCleanlyOnIdleTimeout(t *testing.T) {
	stop := "stop"
	server := silentParkSSEServer(t, chatChunk("hello", nil)+chatChunk("", &stop))
	defer server.Close()

	ctx := newStreamTestContext()
	ctx.SetValue(schemas.BifrostContextKeyStreamIdleTimeout, 300*time.Millisecond)

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
			t.Fatalf("chunk %d unexpectedly carried an error: %+v", i, chunk.BifrostError.Error)
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

// The text-completion loop handles the idle-timeout read error on the same switch.
func TestTextCompletionStreamSilentParkAfterFinishReasonEndsCleanlyOnIdleTimeout(t *testing.T) {
	server := silentParkSSEServer(t, `data: {"id":"cmpl-repro","object":"text_completion","created":1,"model":"repro-model","choices":[{"index":0,"text":"hello","finish_reason":null}]}`+"\n\n"+
		`data: {"id":"cmpl-repro","object":"text_completion","created":1,"model":"repro-model","choices":[{"index":0,"text":"","finish_reason":"stop"}]}`+"\n\n")
	defer server.Close()

	ctx := newStreamTestContext()
	ctx.SetValue(schemas.BifrostContextKeyStreamIdleTimeout, 300*time.Millisecond)

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
			t.Fatalf("chunk %d unexpectedly carried an error: %+v", i, chunk.BifrostError.Error)
		}
	}
	final := chunks[len(chunks)-1]
	if final.BifrostTextCompletionResponse == nil {
		t.Fatalf("expected a synthesized final text completion chunk, got %+v", final)
	}
	if len(final.BifrostTextCompletionResponse.Choices) == 0 ||
		final.BifrostTextCompletionResponse.Choices[0].FinishReason == nil ||
		*final.BifrostTextCompletionResponse.Choices[0].FinishReason != "stop" {
		t.Errorf("expected the final chunk to carry finish_reason \"stop\", got %+v", final.BifrostTextCompletionResponse.Choices)
	}
}

// The Responses-to-Chat fallback reaches the same loop through ResponsesStream with native
// Responses disabled. There the terminal signal is the pending completed event synthesized
// from finish_reason, not finishReason itself, so the idle-timeout branch has to honour it
// too or the parked stream is reported as an error and the completed event is never flushed.
func TestResponsesStreamFallbackSilentParkAfterFinishReasonEndsCleanlyOnIdleTimeout(t *testing.T) {
	server := silentParkSSEServer(t, chatChunk("hello", nil)+chatChunkNullDeltaFinish("stop"))
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

	ctx := newStreamTestContext()
	ctx.SetValue(schemas.BifrostContextKeyStreamIdleTimeout, 300*time.Millisecond)

	request := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "repro-model",
		Input: []schemas.ResponsesMessage{{
			Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")},
		}},
	}
	stream, bifrostErr := provider.ResponsesStream(ctx, passthroughPostHook, nil, testKey(), request)
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	var completed *schemas.BifrostResponsesStreamResponse
	for i, chunk := range collectChunks(t, stream) {
		if chunk.BifrostError != nil {
			t.Fatalf("chunk %d unexpectedly carried an error: %+v", i, chunk.BifrostError.Error)
		}
		if chunk.BifrostResponsesStreamResponse != nil && chunk.BifrostResponsesStreamResponse.Type == schemas.ResponsesStreamResponseTypeCompleted {
			completed = chunk.BifrostResponsesStreamResponse
		}
	}
	if completed == nil {
		t.Fatal("expected a completed Responses stream event from a parked fallback stream")
	}
	if completed.Response == nil || completed.Response.StopReason == nil || *completed.Response.StopReason != "stop" {
		t.Fatalf("expected stop_reason stop on the completed event, got %+v", completed.Response)
	}
}

// Raw-response capture must be independent of semantic chunk forwarding.
//
// The OpenAI chat streaming loop only attaches ExtraFields.RawResponse inside the
// branch that forwards a semantic chunk. Some documented, perfectly normal frame
// shapes never enter that branch and so their bytes would be discarded before the
// framework's accumulator (which reconstructs raw_response purely by concatenating
// chunk.RawResponse) could ever see them:
//
//   - finish-only delta {} with finish_reason set
//   - usage-only  choices: [] with the authoritative token counts
//
// OpenAI documents the last two explicitly: with stream_options.include_usage the
// usage arrives on a final chunk whose choices array is empty, preceded by a chunk
// whose delta is {} and which carries finish_reason.
// https://developers.openai.com/api/reference/resources/chat/subresources/completions/streaming-events
//
// Losing them contradicts the documented raw_response contract ("the exact response
// body received from the provider") and specifically hides the provider's own
// billing numbers from audit, even though Bifrost reads and bills from them.
// See https://github.com/maximhq/bifrost/issues/7144.

// rawCaptureStreamProvider is newStreamTestProvider with raw-response capture on.
func rawCaptureStreamProvider(baseURL string) *OpenAIProvider {
	return NewOpenAIProvider(&schemas.ProviderConfig{
		NetworkConfig:       schemas.NetworkConfig{BaseURL: baseURL},
		SendBackRawResponse: true,
	}, testNoopLogger{})
}

// reconstructRawResponse mirrors what framework/streaming reconstructs for logging
// and plugins: sort the chunks by ChunkIndex and concatenate their RawResponse with
// a blank line between them. Asserting here keeps the test inside core while still
// pinning exactly the string the framework would persist.
func reconstructRawResponse(t *testing.T, chunks []*schemas.BifrostStreamChunk) string {
	t.Helper()
	type indexedRaw struct {
		index int
		raw   string
	}
	var raws []indexedRaw
	for _, chunk := range chunks {
		if chunk.BifrostChatResponse == nil {
			continue
		}
		raw := chunk.BifrostChatResponse.ExtraFields.RawResponse
		if raw == nil {
			continue
		}
		raws = append(raws, indexedRaw{
			index: chunk.BifrostChatResponse.ExtraFields.ChunkIndex,
			raw:   fmt.Sprintf("%v", raw),
		})
	}
	sort.SliceStable(raws, func(i, j int) bool { return raws[i].index < raws[j].index })
	parts := make([]string, 0, len(raws))
	for _, r := range raws {
		parts = append(parts, r.raw)
	}
	return strings.Join(parts, "\n\n")
}

// fullShapeSSEBody is the frame sequence from issue #7144: role-only, content,
// finish-only, usage-only, [DONE].
const (
	rawRoleOnlyFrame   = `{"id":"chatcmpl-repro","object":"chat.completion.chunk","created":1,"model":"repro-model","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}],"usage":null}`
	rawContentFrame    = `{"id":"chatcmpl-repro","object":"chat.completion.chunk","created":1,"model":"repro-model","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}],"usage":null}`
	rawFinishOnlyFrame = `{"id":"chatcmpl-repro","object":"chat.completion.chunk","created":1,"model":"repro-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":null}`
	rawUsageOnlyFrame  = `{"id":"chatcmpl-repro","object":"chat.completion.chunk","created":1,"model":"repro-model","choices":[],"usage":{"prompt_tokens":1000,"completion_tokens":100,"total_tokens":1100}}`
)

func fullShapeSSEBody() string {
	return "data: " + rawRoleOnlyFrame + "\n\n" +
		"data: " + rawContentFrame + "\n\n" +
		"data: " + rawFinishOnlyFrame + "\n\n" +
		"data: " + rawUsageOnlyFrame + "\n\n" +
		"data: [DONE]\n\n"
}

// The usage-only frame carries the provider's authoritative token counts. Bifrost
// reads them (the normalized usage below proves it) and bills from them, so
// dropping their bytes leaves an audit trail that cannot be reconciled against the
// invoice.
func TestChatStreamRawResponseKeepsUsageOnlyFrame(t *testing.T) {
	server := completeSSEServer(t, fullShapeSSEBody())
	defer server.Close()

	provider := rawCaptureStreamProvider(server.URL)
	stream, bifrostErr := provider.ChatCompletionStream(newStreamTestContext(), passthroughPostHook, nil, testKey(), basicChatRequest())
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}
	chunks := collectChunks(t, stream)

	// Guard: the usage really was read, so this is a raw-capture gap and not a
	// parsing failure that would make the assertion below trivially true.
	var total int
	for _, chunk := range chunks {
		if chunk.BifrostChatResponse != nil && chunk.BifrostChatResponse.Usage != nil {
			total = chunk.BifrostChatResponse.Usage.TotalTokens
		}
	}
	if total != 1100 {
		t.Fatalf("expected normalized total_tokens 1100 (proving the usage frame was read), got %d", total)
	}

	raw := reconstructRawResponse(t, chunks)
	if !strings.Contains(raw, rawUsageOnlyFrame) {
		t.Errorf("captured raw response is missing the usage-only frame.\nwant to contain:\n%s\ngot:\n%s", rawUsageOnlyFrame, raw)
	}
}

// A chunk whose delta is {} and which carries finish_reason is how OpenAI ends a
// completion. It never reaches the content-forwarding branch, so its bytes vanish.
func TestChatStreamRawResponseKeepsFinishOnlyFrame(t *testing.T) {
	server := completeSSEServer(t, fullShapeSSEBody())
	defer server.Close()

	provider := rawCaptureStreamProvider(server.URL)
	stream, bifrostErr := provider.ChatCompletionStream(newStreamTestContext(), passthroughPostHook, nil, testKey(), basicChatRequest())
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	raw := reconstructRawResponse(t, collectChunks(t, stream))
	if !strings.Contains(raw, rawFinishOnlyFrame) {
		t.Errorf("captured raw response is missing the finish-only frame.\nwant to contain:\n%s\ngot:\n%s", rawFinishOnlyFrame, raw)
	}
}

// The opening role-only frame arrives before the content frames. Keeping it in this
// ordering assertion prevents raw-response capture from silently moving it to the
// final synthetic chunk.
func TestChatStreamRawResponseKeepsEveryFrameInUpstreamOrder(t *testing.T) {
	server := completeSSEServer(t, fullShapeSSEBody())
	defer server.Close()

	provider := rawCaptureStreamProvider(server.URL)
	stream, bifrostErr := provider.ChatCompletionStream(newStreamTestContext(), passthroughPostHook, nil, testKey(), basicChatRequest())
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	raw := reconstructRawResponse(t, collectChunks(t, stream))

	want := []struct {
		name  string
		frame string
	}{
		{"role-only", rawRoleOnlyFrame},
		{"content", rawContentFrame},
		{"finish-only", rawFinishOnlyFrame},
		{"usage-only", rawUsageOnlyFrame},
	}
	prev := -1
	for _, w := range want {
		// Order alone is not enough: strings.Index reports only the first match, so a
		// frame emitted twice still reads as correctly ordered. Exactly-once is the
		// other half of the contract this path claims - the drain resets the queue
		// after every forwarded chunk precisely so a frame cannot be replayed.
		if n := strings.Count(raw, w.frame); n != 1 {
			t.Errorf("%s frame appears %d times in the captured raw response; expected exactly 1", w.name, n)
		}
		at := strings.Index(raw, w.frame)
		if at < 0 {
			t.Errorf("captured raw response is missing the %s frame:\n%s", w.name, w.frame)
			continue
		}
		if at <= prev {
			t.Errorf("%s frame is out of upstream order (found at %d, previous frame at %d)", w.name, at, prev)
		}
		prev = at
	}
	if t.Failed() {
		t.Logf("reconstructed raw response was:\n%s", raw)
	}
}

// The chunk-forwarding predicate checks Content, Reasoning, ReasoningDetails,
// Audio and ToolCalls - but not Refusal or Annotations, both of which are fields
// on ChatStreamResponseChoiceDelta. A frame carrying only one of those is
// therefore dropped outright: the client never receives it, and because
// framework/streaming assembles ChatAssistantMessage.Refusal (chat.go:298) and
// .Annotations (chat.go:356) purely from forwarded chunk deltas, the accumulated
// message loses them too. That assembly code is unreachable for every
// OpenAI-compatible provider until the predicate lets these chunks through.
//
// OpenAI documents delta.refusal as "The refusal message generated by the model":
// https://developers.openai.com/api/reference/resources/chat/subresources/completions/streaming-events
// Annotations are Bifrost's delta field for streamed URL citations.

const (
	rawRefusalFrame    = `{"id":"chatcmpl-repro","object":"chat.completion.chunk","created":1,"model":"repro-model","choices":[{"index":0,"delta":{"refusal":"I cannot help with that."},"finish_reason":null}],"usage":null}`
	rawAnnotationFrame = `{"id":"chatcmpl-repro","object":"chat.completion.chunk","created":1,"model":"repro-model","choices":[{"index":0,"delta":{"annotations":[{"type":"url_citation","url_citation":{"start_index":0,"end_index":5,"title":"Example","url":"https://example.com"}}]},"finish_reason":null}],"usage":null}`
)

// collectChatDeltas returns the deltas of every forwarded chat chunk, which is
// exactly what a streaming client sees and what the framework accumulates from.
func collectChatDeltas(chunks []*schemas.BifrostStreamChunk) []*schemas.ChatStreamResponseChoiceDelta {
	var deltas []*schemas.ChatStreamResponseChoiceDelta
	for _, chunk := range chunks {
		if chunk.BifrostChatResponse == nil || len(chunk.BifrostChatResponse.Choices) == 0 {
			continue
		}
		choice := chunk.BifrostChatResponse.Choices[0]
		if choice.ChatStreamResponseChoice != nil && choice.ChatStreamResponseChoice.Delta != nil {
			deltas = append(deltas, choice.ChatStreamResponseChoice.Delta)
		}
	}
	return deltas
}

// OpenAI begins many streams with role:"assistant" and empty content. Strict
// clients use that delta to assign the role of the accumulated message, so it must
// reach the client even though it carries no text.
func TestChatStreamForwardsRoleOnlyDelta(t *testing.T) {
	server := completeSSEServer(t, fullShapeSSEBody())
	defer server.Close()

	provider := newStreamTestProvider(server.URL)
	stream, bifrostErr := provider.ChatCompletionStream(newStreamTestContext(), passthroughPostHook, nil, testKey(), basicChatRequest())
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	deltas := collectChatDeltas(collectChunks(t, stream))
	if len(deltas) < 2 {
		t.Fatalf("expected role and content deltas, got %d", len(deltas))
	}
	if deltas[0].Role == nil || *deltas[0].Role != "assistant" {
		t.Fatalf("expected first delta role assistant, got %+v", deltas[0].Role)
	}
	if deltas[0].Content == nil || *deltas[0].Content != "" {
		t.Fatalf("expected empty content on first delta, got %+v", deltas[0].Content)
	}
	if deltas[1].Content == nil || *deltas[1].Content != "hello" {
		t.Fatalf("expected content delta after role, got %+v", deltas[1].Content)
	}
}

// A refusal is the model's answer. Dropping it hands the client an empty stream
// that looks like a successful, content-free completion.
func TestChatStreamForwardsRefusalOnlyDelta(t *testing.T) {
	body := "data: " + rawRefusalFrame + "\n\n" +
		"data: " + rawFinishOnlyFrame + "\n\n" +
		"data: [DONE]\n\n"
	server := completeSSEServer(t, body)
	defer server.Close()

	provider := newStreamTestProvider(server.URL)
	stream, bifrostErr := provider.ChatCompletionStream(newStreamTestContext(), passthroughPostHook, nil, testKey(), basicChatRequest())
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	for _, delta := range collectChatDeltas(collectChunks(t, stream)) {
		if delta.Refusal != nil && *delta.Refusal == "I cannot help with that." {
			return
		}
	}
	t.Error("the refusal-only chunk was never forwarded; the client sees an empty completion and the accumulated message has no refusal")
}

// Streamed URL citations travel on delta.annotations. Dropping them silently
// strips every source from a web-search answer.
func TestChatStreamForwardsAnnotationOnlyDelta(t *testing.T) {
	body := "data: " + rawAnnotationFrame + "\n\n" +
		"data: " + rawFinishOnlyFrame + "\n\n" +
		"data: [DONE]\n\n"
	server := completeSSEServer(t, body)
	defer server.Close()

	provider := newStreamTestProvider(server.URL)
	stream, bifrostErr := provider.ChatCompletionStream(newStreamTestContext(), passthroughPostHook, nil, testKey(), basicChatRequest())
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	for _, delta := range collectChatDeltas(collectChunks(t, stream)) {
		for _, a := range delta.Annotations {
			if a.URLCitation.URL != nil && *a.URLCitation.URL == "https://example.com" {
				return
			}
		}
	}
	t.Error("the annotation-only chunk was never forwarded; streamed citations are lost")
}

// The fallback ingress owes the same contract as the direct chat path: every frame
// the provider sent, exactly once, in order. It broke that contract in BOTH
// directions.
//
// Too many: one upstream chat frame spreads into several Responses events and each
// was stamped with the same jsonData, so framework/streaming/responses.go:1039
// concatenated a single frame N times.
//
// Too few: a usage-only frame has choices: [], and
// ToBifrostResponsesStreamResponse returns nil for that (core/schemas/mux.go:1710),
// so the spread loop body never runs and the frame is dropped outright - #7144's
// own omission, surviving on the second ingress.
//
// Counting occurrences rather than merely capping them is what catches the second
// case: an earlier version of this test asserted only "not more than once" and was
// blind to a frame that was missing entirely.
func TestResponsesFallbackRawResponseKeepsEveryFrameExactlyOnce(t *testing.T) {
	server := completeSSEServer(t, fullShapeSSEBody())
	defer server.Close()

	provider := NewOpenAIProvider(&schemas.ProviderConfig{
		NetworkConfig:       schemas.NetworkConfig{BaseURL: server.URL},
		SendBackRawResponse: true,
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

	var raws []string
	for _, chunk := range collectChunks(t, stream) {
		if chunk.BifrostResponsesStreamResponse == nil {
			continue
		}
		if raw := chunk.BifrostResponsesStreamResponse.ExtraFields.RawResponse; raw != nil {
			raws = append(raws, fmt.Sprintf("%v", raw))
		}
	}
	joined := strings.Join(raws, "\n\n")

	prev := -1
	for _, w := range []struct {
		name  string
		frame string
	}{
		{"role-only", rawRoleOnlyFrame},
		{"content", rawContentFrame},
		{"finish-only", rawFinishOnlyFrame},
		{"usage-only", rawUsageOnlyFrame},
	} {
		if n := strings.Count(joined, w.frame); n != 1 {
			t.Errorf("%s frame appears %d times in the captured raw response; expected exactly 1", w.name, n)
		}
		// Exactly-once says nothing about sequence, so assert order here too, exactly as
		// the direct chat path does. On this fixture every drain happens to carry a single
		// frame, so ordering is enforced structurally and no production change can make
		// this particular assertion fail today. It guards the case where pendingRawFrames
		// actually batches - which it does as soon as several non-forwarding frames arrive
		// back to back (see TestChatStreamRawCaptureIsBoundedByBytes, which queues
		// hundreds) - because a drain that emitted a batch out of order would go unnoticed
		// by the exactly-once check above.
		at := strings.Index(joined, w.frame)
		if at < 0 {
			continue // the count assertion above already reported the miss
		}
		if at <= prev {
			t.Errorf("%s frame is out of upstream order (found at %d, previous frame at %d)", w.name, at, prev)
		}
		prev = at
	}
	if t.Failed() {
		t.Logf("captured raw response was:\n%s", joined)
	}
}

// Raw capture queues every frame that produces no client chunk, and nothing drains
// that queue until a chunk is actually forwarded. An upstream that streams only
// such frames therefore grows it for the life of the connection - and because every
// frame read resets the idle-timeout reader, that connection stays alive
// indefinitely. Before the byte ceiling this was an unbounded allocation driven
// entirely by the upstream.
//
// The bound has to hold without damaging the stream: usage, content and the
// terminal chunk must all still be correct, because a ceiling that corrupts the
// response is worse than the growth it prevents.
func TestChatStreamRawCaptureIsBoundedByBytes(t *testing.T) {
	// Padding rides in system_fingerprint: a real OpenAI chunk field that the
	// forwarding predicate ignores, so each frame is large, parses cleanly, and
	// still produces no client chunk.
	pad := strings.Repeat("p", 3500)
	var body strings.Builder
	const padFrames = 400 // ~1.4 MiB, comfortably past the 1 MiB ceiling
	for i := 0; i < padFrames; i++ {
		body.WriteString(`data: {"id":"chatcmpl-repro","object":"chat.completion.chunk","created":1,"model":"repro-model","system_fingerprint":"` + pad +
			`","choices":[{"index":0,"delta":{},"finish_reason":null}],"usage":null}` + "\n\n")
	}
	body.WriteString("data: " + rawContentFrame + "\n\n")
	body.WriteString("data: " + rawFinishOnlyFrame + "\n\n")
	body.WriteString("data: " + rawUsageOnlyFrame + "\n\n")
	body.WriteString("data: [DONE]\n\n")

	server := completeSSEServer(t, body.String())
	defer server.Close()

	provider := rawCaptureStreamProvider(server.URL)
	stream, bifrostErr := provider.ChatCompletionStream(newStreamTestContext(), passthroughPostHook, nil, testKey(), basicChatRequest())
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}
	chunks := collectChunks(t, stream)

	var capturedBytes, total int
	var sawContent bool
	for _, chunk := range chunks {
		if chunk.BifrostError != nil {
			t.Fatalf("unexpected error chunk: %+v", chunk.BifrostError)
		}
		if chunk.BifrostChatResponse == nil {
			continue
		}
		if raw := chunk.BifrostChatResponse.ExtraFields.RawResponse; raw != nil {
			capturedBytes += len(fmt.Sprintf("%v", raw))
		}
		if chunk.BifrostChatResponse.Usage != nil {
			total = chunk.BifrostChatResponse.Usage.TotalTokens
		}
		for _, delta := range collectChatDeltas([]*schemas.BifrostStreamChunk{chunk}) {
			if delta.Content != nil && *delta.Content == "hello" {
				sawContent = true
			}
		}
	}

	// The ceiling must not cost correctness.
	if total != 1100 {
		t.Errorf("expected normalized total_tokens 1100, got %d", total)
	}
	if !sawContent {
		t.Error("expected the content chunk to still be forwarded")
	}

	// 1 MiB ceiling, plus one frame of slack for the frame that crosses it.
	const limit = (1 << 20) + (8 << 10)
	if capturedBytes > limit {
		t.Errorf("captured raw response is %d bytes, above the %d byte ceiling; an upstream streaming non-forwarding frames can grow this without bound", capturedBytes, limit)
	}
}
