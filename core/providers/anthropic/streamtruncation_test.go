package anthropic

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
	"github.com/valyala/fasthttp"
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

// A turn the provider cut short at its output-token cap arrives on the Responses
// stream as response.incomplete, not response.completed. Anthropic's Messages
// stream has no [DONE] sentinel, so an egress that drops that event leaves the
// client holding a half-written tool_use with no stop_reason to explain it —
// Claude Code then fails to parse the partial input_json and retries in a loop.
func TestAnthropicEgressIncompleteEmitsTerminalEvents(t *testing.T) {
	cases := []struct {
		name       string
		stopReason *string
		incomplete *schemas.ResponsesResponseIncompleteDetails
		want       AnthropicStopReason
	}{
		{
			name:       "MaxTokens",
			stopReason: schemas.Ptr("length"),
			incomplete: &schemas.ResponsesResponseIncompleteDetails{Reason: schemas.ResponsesResponseIncompleteReasonMaxOutputTokens},
			want:       AnthropicStopReasonMaxTokens,
		},
		{
			// Stop reason absent: the reason has to come off incomplete_details,
			// never the end_turn default, which would report a clean finish.
			name:       "MaxTokensFromIncompleteDetailsOnly",
			incomplete: &schemas.ResponsesResponseIncompleteDetails{Reason: schemas.ResponsesResponseIncompleteReasonMaxOutputTokens},
			want:       AnthropicStopReasonMaxTokens,
		},
		{
			name:       "ContentFilterFromIncompleteDetailsOnly",
			incomplete: &schemas.ResponsesResponseIncompleteDetails{Reason: schemas.ResponsesResponseIncompleteReasonContentFilter},
			want:       AnthropicStopReasonRefusal,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
			events := ToAnthropicResponsesStreamResponse(ctx, &schemas.BifrostResponsesStreamResponse{
				Type: schemas.ResponsesStreamResponseTypeIncomplete,
				Response: &schemas.BifrostResponsesResponse{
					Status:            schemas.Ptr(schemas.ResponsesResponseStatusIncomplete),
					StopReason:        tc.stopReason,
					IncompleteDetails: tc.incomplete,
					Usage:             &schemas.ResponsesResponseUsage{InputTokens: 55974, OutputTokens: 4096, TotalTokens: 60070},
				},
			})

			if len(events) != 2 {
				t.Fatalf("got %d events, want message_delta + message_stop", len(events))
			}
			if events[0].Type != AnthropicStreamEventTypeMessageDelta {
				t.Errorf("first event = %s, want message_delta", events[0].Type)
			}
			if events[1].Type != AnthropicStreamEventTypeMessageStop {
				t.Errorf("second event = %s, want message_stop", events[1].Type)
			}
			if events[0].Delta == nil || events[0].Delta.StopReason == nil {
				t.Fatal("message_delta must carry a stop_reason")
			}
			if got := *events[0].Delta.StopReason; got != tc.want {
				t.Errorf("stop_reason = %q, want %q", got, tc.want)
			}
			if events[0].Usage == nil || events[0].Usage.OutputTokens != 4096 {
				t.Error("message_delta must carry the turn's usage")
			}
		})
	}
}

// response.failed is terminal too. Anthropic spells a failed turn as an error
// event; dropping it stalls the client with no frame at all.
func TestAnthropicEgressFailedEmitsErrorEvent(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	events := ToAnthropicResponsesStreamResponse(ctx, &schemas.BifrostResponsesStreamResponse{
		Type: schemas.ResponsesStreamResponseTypeFailed,
		Response: &schemas.BifrostResponsesResponse{
			Error: &schemas.ResponsesResponseError{Message: "upstream generation failed"},
		},
	})

	if len(events) != 1 {
		t.Fatalf("got %d events, want a single error event", len(events))
	}
	if events[0].Type != AnthropicStreamEventTypeError {
		t.Fatalf("event = %s, want error", events[0].Type)
	}
	if events[0].Error == nil || events[0].Error.Message != "upstream generation failed" {
		t.Error("error event must carry the upstream failure message")
	}
}

// An SSE error event rides a committed HTTP 200, so the error carries no status of its
// own. Left nil it reached metrics as a caller 400, and ClassifyFailure had nothing to
// act on — an overloaded_error mid-stream was neither retried nor rotated away from.
func TestStreamErrorStatusMapsAnthropicErrorTypes(t *testing.T) {
	cases := []struct {
		errType string
		want    int
	}{
		{"overloaded_error", 529}, // Anthropic's own; already in the transient retry set
		{"api_error", 500},
		{"rate_limit_error", 429},
		{"request_too_large", 413},
		{"invalid_request_error", 400},
		{"authentication_error", 401},
		{"permission_error", 403},
		{"not_found_error", 404},
		{"some_future_type", 502}, // unknown: upstream failed and gave nothing to go on
		{"", 502},
	}
	for _, tc := range cases {
		if got := streamErrorStatus(tc.errType); got != tc.want {
			t.Errorf("streamErrorStatus(%q) = %d, want %d", tc.errType, got, tc.want)
		}
	}
}

// TestStreamRawResponseDeclaresAnthropicFormat verifies every streamed Chat and Responses chunk that carries an
// Anthropic event as its raw payload declares the Anthropic format, and that the text delta's payload follows a
// change to the normalized text it backs.
func TestStreamRawResponseDeclaresAnthropicFormat(t *testing.T) {
	newRawCaptureProvider := func(baseURL string) *AnthropicProvider {
		return NewAnthropicProvider(&schemas.ProviderConfig{
			NetworkConfig:       schemas.NetworkConfig{BaseURL: baseURL},
			SendBackRawResponse: true,
		}, truncationTestLogger{})
	}
	key := schemas.Key{Value: *schemas.NewSecretVar("test-key")}
	body := anthropicMessageStart + anthropicTextDelta + anthropicMessageStop

	// assertDeclared fails on any raw payload without the Anthropic format and on a stream that carried none.
	assertDeclared := func(t *testing.T, extras []*schemas.BifrostResponseExtraFields) {
		t.Helper()
		withRaw := 0
		for _, extra := range extras {
			if extra.RawResponse == nil {
				continue
			}
			withRaw++
			if extra.RawResponseFormat != schemas.RawResponseFormatAnthropicMessagesStreamEvent {
				t.Errorf("chunk %d carries raw %v with format %q", extra.ChunkIndex, extra.RawResponse, extra.RawResponseFormat)
			}
		}
		if withRaw == 0 {
			t.Fatal("no chunk carried a raw response; the assertion would be vacuous")
		}
	}

	t.Run("chat", func(t *testing.T) {
		server := anthropicSSEServer(t, body, false)
		defer server.Close()
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		stream, bifrostErr := newRawCaptureProvider(server.URL).ChatCompletionStream(ctx, truncationPassthroughPostHook, nil, key, truncationChatRequest())
		if bifrostErr != nil {
			t.Fatalf("stream setup failed: %v", bifrostErr)
		}
		var extras []*schemas.BifrostResponseExtraFields
		var text *schemas.BifrostChatResponse
		for _, chunk := range collectTruncationChunks(t, stream) {
			if chunk.BifrostChatResponse == nil {
				continue
			}
			extras = append(extras, &chunk.BifrostChatResponse.ExtraFields)
			if chunksContainContent([]*schemas.BifrostStreamChunk{chunk}, anthropicPartialText) {
				text = chunk.BifrostChatResponse
			}
		}
		assertDeclared(t, extras)
		if text == nil {
			t.Fatal("no chunk carried the text delta")
		}

		after := *text
		choice := after.Choices[0]
		streamChoice := *choice.ChatStreamResponseChoice
		delta := *streamChoice.Delta
		delta.Content = schemas.Ptr("[REDACTED]")
		streamChoice.Delta = &delta
		choice.ChatStreamResponseChoice = &streamChoice
		after.Choices = []schemas.BifrostResponseChoice{choice}
		synced, err := schemas.SyncRawResponseText(text.ExtraFields.RawResponse, text.ExtraFields.RawResponseFormat, text, &after)
		if err != nil {
			t.Fatalf("raw payload did not follow the text change: %v", err)
		}
		if got := gjson.Get(synced.(string), "delta.text").String(); got != "[REDACTED]" {
			t.Fatalf("raw text = %q, want the redacted text", got)
		}
	})

	t.Run("responses", func(t *testing.T) {
		server := anthropicSSEServer(t, body, false)
		defer server.Close()
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
		stream, bifrostErr := newRawCaptureProvider(server.URL).ResponsesStream(ctx, truncationPassthroughPostHook, nil, key, request)
		if bifrostErr != nil {
			t.Fatalf("stream setup failed: %v", bifrostErr)
		}
		var extras []*schemas.BifrostResponseExtraFields
		var text *schemas.BifrostResponsesStreamResponse
		for _, chunk := range collectTruncationChunks(t, stream) {
			event := chunk.BifrostResponsesStreamResponse
			if event == nil {
				continue
			}
			extras = append(extras, &event.ExtraFields)
			if event.Type == schemas.ResponsesStreamResponseTypeOutputTextDelta && event.Delta != nil && *event.Delta == anthropicPartialText {
				text = event
			}
		}
		assertDeclared(t, extras)
		if text == nil || text.ExtraFields.RawResponse == nil {
			t.Fatal("the text delta carried no raw payload")
		}

		after := *text
		after.Delta = schemas.Ptr("[REDACTED]")
		synced, err := schemas.SyncRawResponseText(text.ExtraFields.RawResponse, text.ExtraFields.RawResponseFormat, text, &after)
		if err != nil {
			t.Fatalf("raw payload did not follow the text change: %v", err)
		}
		if got := gjson.Get(synced.(string), "delta.text").String(); got != "[REDACTED]" {
			t.Fatalf("raw text = %q, want the redacted text", got)
		}
	})
}

// A client that cancels mid-stream must also end the upstream request: the
// connection to Anthropic is closed at once, so Claude stops generating (and
// billing) output nobody will read. The upstream here holds the stream open and
// reports how long it waited for the client to leave.
func TestAnthropicStreamCancelClosesUpstreamConnection(t *testing.T) {
	for _, responses := range []bool{false, true} {
		name := "chat"
		if responses {
			name = "responses"
		}
		t.Run(name, func(t *testing.T) {
			released := make(chan time.Duration, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// net/http notices a client hang-up only once the request body is consumed.
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(anthropicMessageStart + anthropicTextDelta))
				w.(http.Flusher).Flush()
				held := time.Now()
				select {
				case <-r.Context().Done():
				case <-time.After(10 * time.Second):
				}
				released <- time.Since(held)
			}))
			defer server.Close()

			provider := newTruncationTestProvider(server.URL)
			ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
			defer cancel()
			key := schemas.Key{Value: *schemas.NewSecretVar("test-key")}
			var stream chan *schemas.BifrostStreamChunk
			var bifrostErr *schemas.BifrostError
			if responses {
				stream, bifrostErr = provider.ResponsesStream(ctx, truncationPassthroughPostHook, nil, key, truncationChatRequest().ToResponsesRequest())
			} else {
				stream, bifrostErr = provider.ChatCompletionStream(ctx, truncationPassthroughPostHook, nil, key, truncationChatRequest())
			}
			if bifrostErr != nil {
				t.Fatalf("stream setup failed: %v", bifrostErr)
			}
			for chunk := range stream {
				if chunk != nil && chunk.BifrostError == nil {
					cancel()
				}
			}
			select {
			case held := <-released:
				if held > 2*time.Second {
					t.Fatalf("upstream connection stayed open %v after the cancel", held)
				}
			case <-time.After(15 * time.Second):
				t.Fatal("upstream handler never returned")
			}
		})
	}
}

// The message_start output count is provisional: the handlers mark it so on the
// context until a message_delta reports usage, which a cancelled stream's
// estimate reads to decide whether to re-count the received output.
func TestAnthropicStreamMarksProvisionalUsage(t *testing.T) {
	const messageDelta = "event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":null},"usage":{"output_tokens":3}}` + "\n\n"
	for _, tc := range []struct {
		name        string
		body        string
		provisional bool
	}{
		{name: "message_start only", body: anthropicMessageStart + anthropicTextDelta, provisional: true},
		{name: "after message_delta", body: anthropicMessageStart + anthropicTextDelta + messageDelta, provisional: false},
	} {
		for _, responses := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/responses=%v", tc.name, responses), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					w.Header().Set("Content-Type", "text/event-stream")
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(tc.body))
					w.(http.Flusher).Flush()
					<-r.Context().Done()
				}))
				defer server.Close()
				provider := newTruncationTestProvider(server.URL)
				ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
				defer cancel()
				key := schemas.Key{Value: *schemas.NewSecretVar("test-key")}
				var stream chan *schemas.BifrostStreamChunk
				var bifrostErr *schemas.BifrostError
				if responses {
					stream, bifrostErr = provider.ResponsesStream(ctx, truncationPassthroughPostHook, nil, key, truncationChatRequest().ToResponsesRequest())
				} else {
					stream, bifrostErr = provider.ChatCompletionStream(ctx, truncationPassthroughPostHook, nil, key, truncationChatRequest())
				}
				if bifrostErr != nil {
					t.Fatalf("stream setup failed: %v", bifrostErr)
				}
				seen := false
				for chunk := range stream {
					if chunk == nil || chunk.BifrostError != nil {
						continue
					}
					if !seen && chunksContainContent([]*schemas.BifrostStreamChunk{chunk}, anthropicPartialText) || chunk.BifrostResponsesStreamResponse != nil && chunk.BifrostResponsesStreamResponse.Delta != nil && *chunk.BifrostResponsesStreamResponse.Delta == anthropicPartialText {
						seen = true
					}
					if seen {
						// The handler may still be reading the rest of the flushed body.
						for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
							if got, ok := ctx.Value(schemas.BifrostContextKeyStreamUsageProvisional).(bool); ok && got == tc.provisional {
								break
							}
						}
						cancel()
					}
				}
				if !seen {
					t.Fatal("the text delta never arrived")
				}
				got, ok := ctx.Value(schemas.BifrostContextKeyStreamUsageProvisional).(bool)
				if !ok || got != tc.provisional {
					t.Fatalf("provisional usage mark = %v (set=%v), want %v", got, ok, tc.provisional)
				}
			})
		}
	}
}

// anthropicConnCounter tracks dialed and closed upstream TCP sockets, matching
// the connCounter / countedConn pattern in core/providers/utils/streampoison_test.go.
type anthropicConnCounter struct {
	mu     sync.Mutex
	dialed int
	closed int
}

func (c *anthropicConnCounter) open() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dialed - c.closed
}

func (c *anthropicConnCounter) totalDialed() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dialed
}

type anthropicCountedConn struct {
	net.Conn
	counter *anthropicConnCounter
	once    sync.Once
}

func (c *anthropicCountedConn) Close() error {
	c.once.Do(func() {
		c.counter.mu.Lock()
		c.counter.closed++
		c.counter.mu.Unlock()
	})
	return c.Conn.Close()
}

func newAnthropicTestStreamingClient(maxConnsPerHost int, idleDuration time.Duration) (*fasthttp.Client, *anthropicConnCounter) {
	base := &fasthttp.Client{
		ReadTimeout:         30 * time.Second,
		WriteTimeout:        30 * time.Second,
		MaxConnsPerHost:     maxConnsPerHost,
		MaxIdleConnDuration: idleDuration,
		MaxConnWaitTimeout:  30 * time.Second,
		MaxConnDuration:     300 * time.Second,
		ConnPoolStrategy:    fasthttp.FIFO,
	}
	base = providerUtils.ConfigureDialer(base, true)

	counter := &anthropicConnCounter{}
	inner := base.Dial
	base.Dial = func(addr string) (net.Conn, error) {
		conn, err := inner(addr)
		if err != nil {
			return nil, err
		}
		counter.mu.Lock()
		counter.dialed++
		counter.mu.Unlock()
		return &anthropicCountedConn{Conn: conn, counter: counter}, nil
	}
	return providerUtils.BuildStreamingClient(base), counter
}

// TestAnthropicStreamingSequentialConnectionReuse verifies that sequential SSE
// streams through HandleAnthropicChatCompletionStreaming and
// HandleAnthropicResponsesStream reuse a single pooled keep-alive connection
// and never send "Connection: close".
func TestAnthropicStreamingSequentialConnectionReuse(t *testing.T) {
	const iterations = 15

	newServer := func(t *testing.T, connCloseSeen *atomic.Int32, reqCount *atomic.Int32) *httptest.Server {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n := reqCount.Add(1)
			if r.Close || strings.EqualFold(r.Header.Get("Connection"), "close") {
				connCloseSeen.Add(1)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			flusher, ok := w.(http.Flusher)
			if !ok {
				t.Error("ResponseWriter does not implement http.Flusher")
				return
			}
			flusher.Flush()

			events := []string{
				"event: message_start\n" +
					`data: {"type":"message_start","message":{"id":"msg_reuse","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[],"usage":{"input_tokens":5,"output_tokens":0}}}` + "\n\n",
				"event: content_block_start\n" +
					`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n",
				fmt.Sprintf("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"turn-%d\"}}\n\n", n),
				"event: content_block_stop\n" +
					`data: {"type":"content_block_stop","index":0}` + "\n\n",
				"event: message_delta\n" +
					`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}` + "\n\n",
				"event: message_stop\n" +
					`data: {"type":"message_stop"}` + "\n\n",
			}
			for _, ev := range events {
				if _, err := w.Write([]byte(ev)); err != nil {
					return
				}
				flusher.Flush()
			}
		}))
		t.Cleanup(srv.Close)
		return srv
	}

	body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"stream":true}`)
	headers := map[string]string{
		"Accept":        "text/event-stream",
		"Cache-Control": "no-cache",
	}

	t.Run("HandleAnthropicChatCompletionStreaming", func(t *testing.T) {
		var connCloseSeen, reqCount atomic.Int32
		srv := newServer(t, &connCloseSeen, &reqCount)
		client, counter := newAnthropicTestStreamingClient(8, 30*time.Second)

		for i := 1; i <= iterations; i++ {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			stream, bifrostErr := HandleAnthropicChatCompletionStreaming(
				ctx, client, srv.URL, body, headers, nil, 30, nil,
				false, false, schemas.Anthropic, truncationPassthroughPostHook,
				nil, nil, truncationTestLogger{}, nil,
			)
			if bifrostErr != nil {
				t.Fatalf("iteration %d setup failed: %v", i, bifrostErr)
			}
			chunks := collectTruncationChunks(t, stream)
			for idx, c := range chunks {
				if c.BifrostError != nil {
					t.Fatalf("iteration %d chunk %d error: %+v", i, idx, c.BifrostError)
				}
			}
			wantText := fmt.Sprintf("turn-%d", i)
			if !chunksContainContent(chunks, wantText) {
				t.Fatalf("iteration %d missing expected delta %q in %d chunks", i, wantText, len(chunks))
			}
			final := chunks[len(chunks)-1].BifrostChatResponse
			if final == nil || final.Usage == nil || final.Usage.TotalTokens != 8 {
				t.Fatalf("iteration %d unexpected terminal chunk: %+v", i, final)
			}
		}

		if got := reqCount.Load(); got != iterations {
			t.Fatalf("server received %d requests, want %d", got, iterations)
		}
		if got := connCloseSeen.Load(); got != 0 {
			t.Fatalf("server observed Connection: close on %d requests, want 0", got)
		}
		if got := counter.totalDialed(); got != 1 {
			t.Fatalf("totalDialed = %d across %d sequential streams, want 1", got, iterations)
		}
		if got := counter.open(); got != 1 {
			t.Fatalf("open pooled connections = %d, want 1", got)
		}
	})

	t.Run("HandleAnthropicResponsesStream", func(t *testing.T) {
		var connCloseSeen, reqCount atomic.Int32
		srv := newServer(t, &connCloseSeen, &reqCount)
		client, counter := newAnthropicTestStreamingClient(8, 30*time.Second)

		for i := 1; i <= iterations; i++ {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			stream, bifrostErr := HandleAnthropicResponsesStream(
				ctx, client, srv.URL, body, headers, nil, 30, nil,
				false, false, schemas.Anthropic, truncationPassthroughPostHook,
				nil, nil, truncationTestLogger{}, nil,
			)
			if bifrostErr != nil {
				t.Fatalf("iteration %d setup failed: %v", i, bifrostErr)
			}
			chunks := collectTruncationChunks(t, stream)
			var gotDelta strings.Builder
			sawCompleted := false
			for idx, c := range chunks {
				if c.BifrostError != nil {
					t.Fatalf("iteration %d chunk %d error: %+v", i, idx, c.BifrostError)
				}
				resp := c.BifrostResponsesStreamResponse
				if resp == nil {
					continue
				}
				if resp.Type == schemas.ResponsesStreamResponseTypeOutputTextDelta && resp.Delta != nil {
					gotDelta.WriteString(*resp.Delta)
				}
				if resp.Type == schemas.ResponsesStreamResponseTypeCompleted {
					sawCompleted = true
					if resp.Response == nil || resp.Response.Usage == nil || resp.Response.Usage.TotalTokens != 8 {
						t.Fatalf("iteration %d unexpected completed response usage: %+v", i, resp.Response)
					}
				}
			}
			wantText := fmt.Sprintf("turn-%d", i)
			if gotDelta.String() != wantText {
				t.Fatalf("iteration %d delta = %q, want %q", i, gotDelta.String(), wantText)
			}
			if !sawCompleted {
				t.Fatalf("iteration %d missing response.completed event", i)
			}
		}

		if got := reqCount.Load(); got != iterations {
			t.Fatalf("server received %d requests, want %d", got, iterations)
		}
		if got := connCloseSeen.Load(); got != 0 {
			t.Fatalf("server observed Connection: close on %d requests, want 0", got)
		}
		if got := counter.totalDialed(); got != 1 {
			t.Fatalf("totalDialed = %d across %d sequential streams, want 1", got, iterations)
		}
		if got := counter.open(); got != 1 {
			t.Fatalf("open pooled connections = %d, want 1", got)
		}
	})
}

func newAnthropicMultiChunkSSEServer(t *testing.T, deltaCount int, interval time.Duration) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		holdForCancel := r.Header.Get("X-Hold-For-Cancel") == "1"
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			return
		}
		if _, err := w.Write([]byte(anthropicMessageStart)); err != nil {
			return
		}
		flusher.Flush()

		emitCount := deltaCount
		if holdForCancel && emitCount > 4 {
			emitCount = 4
		}
		for i := range emitCount {
			frame := fmt.Sprintf("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"[%d]\"}}\n\n", i)
			if _, err := w.Write([]byte(frame)); err != nil {
				return
			}
			flusher.Flush()
			select {
			case <-release:
				return
			case <-r.Context().Done():
				return
			case <-time.After(interval):
			}
		}
		if holdForCancel {
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return
		}

		if _, err := w.Write([]byte(anthropicMessageStop)); err != nil {
			return
		}
		flusher.Flush()
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	return srv
}

func anthropicFixtureSaturated(bErr *schemas.BifrostError) bool {
	if bErr == nil || bErr.Error == nil || bErr.Error.Error == nil {
		return false
	}
	err := bErr.Error.Error
	var opErr *net.OpError
	if !errors.As(err, &opErr) || opErr.Op != "dial" {
		return false
	}
	return errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.EADDRNOTAVAIL) || opErr.Timeout()
}

var errRetrySaturated = errors.New("loopback fixture saturated")

// TestAnthropicStreamingConcurrentCancellationStress shares one streamingClient
// across concurrent HandleAnthropicChatCompletionStreaming and
// HandleAnthropicResponsesStream workers while cancelling a parallel stream load
// mid-body, confirming #6143 stays fixed (no reader-pool double-put panic or
// stream corruption) and open connections stay bounded.
func TestAnthropicStreamingConcurrentCancellationStress(t *testing.T) {
	const (
		deltaCount    = 24
		chunkGap      = 2 * time.Millisecond
		streamBudget  = 5 * time.Second
		maxConns      = 64
		cleanWorkers  = 8
		cleanPerWork  = 20
		cancelStreams = 240
		cancelConc    = 24
	)

	var wantBuilder strings.Builder
	for i := range deltaCount {
		fmt.Fprintf(&wantBuilder, "[%d]", i)
	}
	wantText := wantBuilder.String()

	srv := newAnthropicMultiChunkSSEServer(t, deltaCount, chunkGap)
	client, counter := newAnthropicTestStreamingClient(maxConns, 300*time.Millisecond)

	body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"stream":true}`)
	headers := map[string]string{
		"Accept":        "text/event-stream",
		"Cache-Control": "no-cache",
	}
	cancelHeaders := map[string]string{
		"Accept":            "text/event-stream",
		"Cache-Control":     "no-cache",
		"X-Hold-For-Cancel": "1",
	}

	runCleanOnce := func(useResponses bool) (err error) {
		ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
		defer cancel()

		done := make(chan error, 1)
		go func() {
			if !useResponses {
				stream, bErr := HandleAnthropicChatCompletionStreaming(
					ctx, client, srv.URL, body, headers, nil, 30, nil,
					false, false, schemas.Anthropic, truncationPassthroughPostHook,
					nil, nil, truncationTestLogger{}, nil,
				)
				if bErr != nil {
					if anthropicFixtureSaturated(bErr) {
						done <- errRetrySaturated
						return
					}
					done <- fmt.Errorf("chat setup error: %v", bErr.Error)
					return
				}
				var got strings.Builder
				var sawFinal bool
				for c := range stream {
					if c == nil {
						continue
					}
					if c.BifrostError != nil {
						done <- fmt.Errorf("chat chunk error: %+v", c.BifrostError.Error)
						return
					}
					if c.BifrostChatResponse != nil {
						for _, choice := range c.BifrostChatResponse.Choices {
							if choice.ChatStreamResponseChoice != nil && choice.Delta != nil && choice.Delta.Content != nil {
								got.WriteString(*choice.Delta.Content)
							}
						}
						if c.BifrostChatResponse.Usage != nil && c.BifrostChatResponse.Usage.TotalTokens == 8 {
							sawFinal = true
						}
					}
				}
				if got.String() != wantText {
					done <- fmt.Errorf("chat corrupted text: got %q, want %q", got.String(), wantText)
					return
				}
				if !sawFinal {
					done <- errors.New("chat missing terminal usage chunk")
					return
				}
				done <- nil
				return
			}

			stream, bErr := HandleAnthropicResponsesStream(
				ctx, client, srv.URL, body, headers, nil, 30, nil,
				false, false, schemas.Anthropic, truncationPassthroughPostHook,
				nil, nil, truncationTestLogger{}, nil,
			)
			if bErr != nil {
				if anthropicFixtureSaturated(bErr) {
					done <- errRetrySaturated
					return
				}
				done <- fmt.Errorf("responses setup error: %v", bErr.Error)
				return
			}
			var got strings.Builder
			var sawCompleted bool
			for c := range stream {
				if c == nil {
					continue
				}
				if c.BifrostError != nil {
					done <- fmt.Errorf("responses chunk error: %+v", c.BifrostError.Error)
					return
				}
				resp := c.BifrostResponsesStreamResponse
				if resp == nil {
					continue
				}
				if resp.Type == schemas.ResponsesStreamResponseTypeOutputTextDelta && resp.Delta != nil {
					got.WriteString(*resp.Delta)
				}
				if resp.Type == schemas.ResponsesStreamResponseTypeCompleted && resp.Response != nil &&
					resp.Response.Usage != nil && resp.Response.Usage.TotalTokens == 8 {
					sawCompleted = true
				}
			}
			if got.String() != wantText {
				done <- fmt.Errorf("responses corrupted text: got %q, want %q", got.String(), wantText)
				return
			}
			if !sawCompleted {
				done <- errors.New("responses missing completed chunk with usage")
				return
			}
			done <- nil
		}()

		select {
		case resErr := <-done:
			return resErr
		case <-time.After(streamBudget):
			cancel()
			return errors.New("clean stream timed out (stuck)")
		}
	}

	runClean := func(useResponses bool) error {
		for retry := range 50 {
			err := runCleanOnce(useResponses)
			if !errors.Is(err, errRetrySaturated) || retry == 49 {
				return err
			}
			time.Sleep(10 * time.Millisecond)
		}
		return nil
	}

	var (
		mu             sync.Mutex
		failures       []string
		midBodyCancels atomic.Int32
		cancelWG       sync.WaitGroup
		cleanWG        sync.WaitGroup
	)

	cancelWG.Add(1)
	go func() {
		defer cancelWG.Done()
		var wg sync.WaitGroup
		sem := make(chan struct{}, cancelConc)
		for i := range cancelStreams {
			wg.Add(1)
			sem <- struct{}{}
			useResponses := i%2 == 1
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
				drained := make(chan struct{})
				started := make(chan struct{})
				var startOnce sync.Once
				go func() {
					defer close(drained)
					var stream chan *schemas.BifrostStreamChunk
					var bErr *schemas.BifrostError
					if useResponses {
						stream, bErr = HandleAnthropicResponsesStream(
							ctx, client, srv.URL, body, cancelHeaders, nil, 30, nil,
							false, false, schemas.Anthropic, truncationPassthroughPostHook,
							nil, nil, truncationTestLogger{}, nil,
						)
					} else {
						stream, bErr = HandleAnthropicChatCompletionStreaming(
							ctx, client, srv.URL, body, cancelHeaders, nil, 30, nil,
							false, false, schemas.Anthropic, truncationPassthroughPostHook,
							nil, nil, truncationTestLogger{}, nil,
						)
					}
					if bErr != nil {
						if ctx.Err() == nil && !anthropicFixtureSaturated(bErr) {
							mu.Lock()
							failures = append(failures, fmt.Sprintf("cancel worker setup error: %v", bErr.Error))
							mu.Unlock()
						}
						return
					}
					for c := range stream {
						if c != nil {
							startOnce.Do(func() { close(started) })
						}
					}
				}()
				select {
				case <-started:
					select {
					case <-drained:
					default:
						cancel()
						midBodyCancels.Add(1)
					}
				case <-drained:
				}
				cancel()
				<-drained
			}()
		}
		wg.Wait()
	}()

	for w := range cleanWorkers {
		cleanWG.Add(1)
		workerIdx := w
		go func() {
			defer cleanWG.Done()
			for i := range cleanPerWork {
				useResponses := (workerIdx+i)%2 == 1
				if err := runClean(useResponses); err != nil {
					mu.Lock()
					failures = append(failures, err.Error())
					mu.Unlock()
				}
			}
		}()
	}

	cleanWG.Wait()
	cancelWG.Wait()

	if len(failures) > 0 {
		t.Fatalf("%d stream failures alongside cancellations; first few: %v",
			len(failures), failures[:min(len(failures), 5)])
	}
	if got := midBodyCancels.Load(); got < cancelStreams/2 {
		t.Fatalf("only %d of %d cancellations landed after body start; mid-body cancellation path was not exercised",
			got, cancelStreams)
	}

	for i := range 20 {
		if err := runClean(i%2 == 1); err != nil {
			t.Fatalf("post-load clean stream %d failed: %v", i, err)
		}
	}

	if open := counter.open(); open < 0 || open > maxConns {
		t.Fatalf("open connections = %d after workers finished, want within [0, %d]", open, maxConns)
	}
	client.CloseIdleConnections()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if counter.open() == 0 {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("%d of %d dialed connections still open after idle cleanup", counter.open(), counter.totalDialed())
}
