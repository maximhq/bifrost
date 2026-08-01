package anthropic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// Anthropic's Messages stream carries no [DONE] sentinel: message_stop is the
// only completion signal. An upstream that dies closes on a chunk boundary,
// which fasthttp reports as a plain io.EOF — the same read result as a healthy
// close — so a stream cut short before message_stop must be surfaced rather
// than finished off with a synthesized final chunk.
// See https://github.com/maximhq/bifrost/issues/5546.

type truncationTestLogger struct{}

func (truncationTestLogger) Debug(string, ...any)                   {}
func (truncationTestLogger) Info(string, ...any)                    {}
func (truncationTestLogger) Warn(string, ...any)                    {}
func (truncationTestLogger) Error(string, ...any)                   {}
func (truncationTestLogger) Fatal(string, ...any)                   {}
func (truncationTestLogger) SetLevel(schemas.LogLevel)              {}
func (truncationTestLogger) SetOutputType(schemas.LoggerOutputType) {}
func (truncationTestLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

// anthropicSSEServer serves prelude and then either ends cleanly or kills the
// connection mid-response. panic(http.ErrAbortHandler) is net/http's documented
// way to drop a connection without the terminating chunk, reproducing what an
// upstream whose generator died does on the wire.
func anthropicSSEServer(t *testing.T, prelude string, truncate bool) *httptest.Server {
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
		if truncate {
			panic(http.ErrAbortHandler)
		}
	}))
}

func newTruncationTestProvider(baseURL string) *AnthropicProvider {
	return NewAnthropicProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: baseURL},
	}, truncationTestLogger{})
}

func truncationPassthroughPostHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return resp, err
}

func collectTruncationChunks(t *testing.T, stream chan *schemas.BifrostStreamChunk) []*schemas.BifrostStreamChunk {
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

func assertAnthropicTruncationError(t *testing.T, err *schemas.BifrostError) {
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
}

func truncationChatRequest() *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-repro",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")},
		}},
	}
}

const anthropicMessageStart = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_repro","type":"message","role":"assistant","model":"claude-repro","content":[],"usage":{"input_tokens":5,"output_tokens":0}}}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n"

// anthropicPartialText is the one text delta the fixtures emit. Derived into the
// SSE payload rather than duplicated, so the assertion cannot drift from the wire.
const anthropicPartialText = "partial answer"

const anthropicTextDelta = "event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + anthropicPartialText + `"}}` + "\n\n"

const anthropicMessageStop = "event: content_block_stop\n" +
	`data: {"type":"content_block_stop","index":0}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}` + "\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n\n"

// Pre-first-byte death: the error must be the stream's first chunk so
// CheckFirstStreamChunkForError can turn it into a synchronous, retryable error.
func TestAnthropicChatStreamTruncatedPreFirstByte(t *testing.T) {
	server := anthropicSSEServer(t, "", true)
	defer server.Close()

	provider := newTruncationTestProvider(server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	stream, bifrostErr := provider.ChatCompletionStream(ctx, truncationPassthroughPostHook, nil,
		schemas.Key{Value: *schemas.NewSecretVar("test-key")}, truncationChatRequest())
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	chunks := collectTruncationChunks(t, stream)
	if len(chunks) != 1 {
		t.Fatalf("expected exactly one chunk (the error), got %d: %+v", len(chunks), chunks)
	}
	assertAnthropicTruncationError(t, chunks[0].BifrostError)
	if chunks[0].BifrostChatResponse != nil {
		t.Error("no synthetic chat chunk may be emitted for a truncated stream")
	}
}

// Mid-stream death: forwarded content stays, then the error frame lands, and no
// content-free terminal chunk is appended.
func TestAnthropicChatStreamTruncatedMidStream(t *testing.T) {
	server := anthropicSSEServer(t, anthropicMessageStart+anthropicTextDelta, true)
	defer server.Close()

	provider := newTruncationTestProvider(server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	stream, bifrostErr := provider.ChatCompletionStream(ctx, truncationPassthroughPostHook, nil,
		schemas.Key{Value: *schemas.NewSecretVar("test-key")}, truncationChatRequest())
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	chunks := collectTruncationChunks(t, stream)
	if len(chunks) < 2 {
		t.Fatalf("expected the forwarded content chunk followed by the error, got %d chunk(s)", len(chunks))
	}
	final := chunks[len(chunks)-1]
	assertAnthropicTruncationError(t, final.BifrostError)
	for i, chunk := range chunks[:len(chunks)-1] {
		if chunk.BifrostError != nil {
			t.Errorf("chunk %d unexpectedly carried an error: %+v", i, chunk.BifrostError)
		}
	}
	// Detecting truncation must not cost the client the content that did arrive:
	// the text delta forwarded before the connection died has to survive.
	if !chunksContainContent(chunks[:len(chunks)-1], anthropicPartialText) {
		t.Errorf("expected a pre-error chunk carrying %q; content forwarded before the truncation was dropped", anthropicPartialText)
	}
}

// chunksContainContent reports whether any chunk carried the given delta text.
func chunksContainContent(chunks []*schemas.BifrostStreamChunk, want string) bool {
	for _, chunk := range chunks {
		if chunk.BifrostChatResponse == nil {
			continue
		}
		for _, choice := range chunk.BifrostChatResponse.Choices {
			if choice.ChatStreamResponseChoice == nil || choice.ChatStreamResponseChoice.Delta == nil {
				continue
			}
			if content := choice.ChatStreamResponseChoice.Delta.Content; content != nil && *content == want {
				return true
			}
		}
	}
	return false
}

// Guard against false positives: a stream that reaches message_stop is complete
// and must still receive its synthesized final chunk, with no error.
func TestAnthropicChatStreamCompleteUnaffected(t *testing.T) {
	server := anthropicSSEServer(t, anthropicMessageStart+anthropicTextDelta+anthropicMessageStop, false)
	defer server.Close()

	provider := newTruncationTestProvider(server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	stream, bifrostErr := provider.ChatCompletionStream(ctx, truncationPassthroughPostHook, nil,
		schemas.Key{Value: *schemas.NewSecretVar("test-key")}, truncationChatRequest())
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	chunks := collectTruncationChunks(t, stream)
	if len(chunks) == 0 {
		t.Fatal("expected chunks from a well-formed stream")
	}
	for i, chunk := range chunks {
		if chunk.BifrostError != nil {
			t.Fatalf("chunk %d unexpectedly carried an error: %+v", i, chunk.BifrostError)
		}
	}
	// A plain text delta is also a non-nil BifrostChatResponse, so identity has to be
	// checked on properties only the synthesized terminal chunk carries: usage,
	// a finish reason, and an empty delta.
	final := chunks[len(chunks)-1].BifrostChatResponse
	if final == nil {
		t.Fatalf("expected a synthesized final chat chunk, got %+v", chunks[len(chunks)-1])
	}
	if final.Usage == nil {
		t.Fatal("terminal chunk must carry the accumulated usage; it is the only chunk the cost calculation can bill on")
	}
	// message_start reports input_tokens 5, message_delta reports a cumulative
	// output_tokens 3 (https://platform.claude.com/docs/en/build-with-claude/streaming).
	if final.Usage.PromptTokens != 5 || final.Usage.CompletionTokens != 3 || final.Usage.TotalTokens != 8 {
		t.Errorf("terminal usage = prompt %d / completion %d / total %d, want 5 / 3 / 8",
			final.Usage.PromptTokens, final.Usage.CompletionTokens, final.Usage.TotalTokens)
	}
	if len(final.Choices) != 1 {
		t.Fatalf("expected exactly one choice on the terminal chunk, got %d", len(final.Choices))
	}
	choice := final.Choices[0]
	if choice.FinishReason == nil {
		t.Fatal("terminal chunk must carry a finish reason; content deltas never do")
	}
	if *choice.FinishReason != "stop" {
		t.Errorf("finish reason = %q, want %q (mapped from Anthropic end_turn)", *choice.FinishReason, "stop")
	}
	if choice.ChatStreamResponseChoice == nil || choice.ChatStreamResponseChoice.Delta == nil {
		t.Fatal("terminal chunk must still carry a stream choice with an empty delta")
	}
	if content := choice.ChatStreamResponseChoice.Delta.Content; content != nil && *content != "" {
		t.Errorf("terminal chunk delta must be empty, got %q", *content)
	}
	// The content that arrived mid-stream is still expected to have been forwarded.
	if !chunksContainContent(chunks[:len(chunks)-1], anthropicPartialText) {
		t.Errorf("expected an earlier chunk carrying %q", anthropicPartialText)
	}
}

// Anthropic reports cache_read_input_tokens and cache_creation_input_tokens
// exclusive of input_tokens - total input is the sum of all three
// (https://platform.claude.com/docs/en/build-with-claude/prompt-caching). The
// stream accumulator therefore folds the cache counters into PromptTokens/
// TotalTokens exactly once at stream end. SendStreamTruncatedError snapshots the
// usage handle when it bills, so the fold must happen before the send: otherwise a
// cache-heavy request that dies mid-stream bills only the post-breakpoint tokens.

const (
	truncationCacheInputTokens  = 5
	truncationCacheReadTokens   = 100
	truncationCacheWriteTokens  = 20
	truncationCacheFoldedTokens = truncationCacheInputTokens + truncationCacheReadTokens + truncationCacheWriteTokens
)

const anthropicCachedMessageStart = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_repro","type":"message","role":"assistant","model":"claude-repro","content":[],` +
	`"usage":{"input_tokens":5,"output_tokens":0,"cache_read_input_tokens":100,"cache_creation_input_tokens":20}}}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n"

// assertCacheFoldedBilledUsage pins the billed snapshot carried by a truncation
// error: the cache counters must survive intact *and* already be folded into the
// top-level prompt/total, which is what the cost calculation downstream charges on.
func assertCacheFoldedBilledUsage(t *testing.T, err *schemas.BifrostError) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a truncation error carrying billed usage, got nil")
	}
	billed := err.ExtraFields.BilledUsage
	if billed == nil {
		t.Fatal("truncated stream must bill for tokens the provider already reported")
	}
	if billed.PromptTokens != truncationCacheFoldedTokens {
		t.Errorf("PromptTokens = %d, want %d (input %d + cache read %d + cache write %d)",
			billed.PromptTokens, truncationCacheFoldedTokens,
			truncationCacheInputTokens, truncationCacheReadTokens, truncationCacheWriteTokens)
	}
	if billed.TotalTokens != truncationCacheFoldedTokens {
		t.Errorf("TotalTokens = %d, want %d", billed.TotalTokens, truncationCacheFoldedTokens)
	}
	if billed.PromptTokensDetails == nil {
		t.Fatal("expected the cache breakdown to survive onto the billed snapshot")
	}
	if billed.PromptTokensDetails.CachedReadTokens != truncationCacheReadTokens {
		t.Errorf("CachedReadTokens = %d, want %d", billed.PromptTokensDetails.CachedReadTokens, truncationCacheReadTokens)
	}
	if billed.PromptTokensDetails.CachedWriteTokens != truncationCacheWriteTokens {
		t.Errorf("CachedWriteTokens = %d, want %d", billed.PromptTokensDetails.CachedWriteTokens, truncationCacheWriteTokens)
	}
}

func TestAnthropicChatStreamTruncatedBillsCachedTokens(t *testing.T) {
	server := anthropicSSEServer(t, anthropicCachedMessageStart+anthropicTextDelta, true)
	defer server.Close()

	provider := newTruncationTestProvider(server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	stream, bifrostErr := provider.ChatCompletionStream(ctx, truncationPassthroughPostHook, nil,
		schemas.Key{Value: *schemas.NewSecretVar("test-key")}, truncationChatRequest())
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	chunks := collectTruncationChunks(t, stream)
	if len(chunks) == 0 {
		t.Fatal("expected at least the error chunk")
	}
	final := chunks[len(chunks)-1]
	assertAnthropicTruncationError(t, final.BifrostError)
	assertCacheFoldedBilledUsage(t, final.BifrostError)
}

func TestAnthropicResponsesStreamTruncatedBillsCachedTokens(t *testing.T) {
	server := anthropicSSEServer(t, anthropicCachedMessageStart+anthropicTextDelta, true)
	defer server.Close()

	provider := newTruncationTestProvider(server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	request := &schemas.BifrostResponsesRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-repro",
		Input: []schemas.ResponsesMessage{{
			Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")},
		}},
	}
	stream, bifrostErr := provider.ResponsesStream(ctx, truncationPassthroughPostHook, nil,
		schemas.Key{Value: *schemas.NewSecretVar("test-key")}, request)
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	chunks := collectTruncationChunks(t, stream)
	if len(chunks) == 0 {
		t.Fatal("expected at least the error chunk")
	}
	final := chunks[len(chunks)-1]
	assertAnthropicTruncationError(t, final.BifrostError)
	assertCacheFoldedBilledUsage(t, final.BifrostError)
}
