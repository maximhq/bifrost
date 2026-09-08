package bifrost

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// Regression tests for https://github.com/maximhq/bifrost/issues/4788.
//
// When a streaming attempt fails through an error embedded in an HTTP 200 SSE
// stream (e.g. rate limits sent as SSE events), the provider goroutine exits
// and its teardown claims the connection_closed flag on the request's shared
// BifrostContext (ReleaseStreamingResponse). That claim is scoped to the
// response it released, but the flag stayed set on the context, so the
// idle-timeout reader of the next attempt's stream saw the context as already
// closed and failed every read with "stream closed". Any streaming retry or
// fallback that followed a first-chunk error was dead on arrival.

// sseHandler serves the given payloads as one SSE data event each.
func sseHandler(payloads ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for _, p := range payloads {
			fmt.Fprintf(w, "data: %s\n\n", p)
			if fl != nil {
				fl.Flush()
			}
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		if fl != nil {
			fl.Flush()
		}
	}
}

// anthropicMessagesHandler serves a minimal valid Anthropic Messages API
// stream that produces the text "hello".
func anthropicMessagesHandler() http.HandlerFunc {
	events := []struct{ typ, data string }{
		{"message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-3-5-haiku-20241022","usage":{"input_tokens":10,"output_tokens":1}}}`},
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`},
		{"content_block_stop", `{"type":"content_block_stop","index":0}`},
		{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`},
		{"message_stop", `{"type":"message_stop"}`},
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for _, e := range events {
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.typ, e.data)
			if fl != nil {
				fl.Flush()
			}
		}
	}
}

// drainChatStream collects streamed content and any error chunks.
func drainChatStream(ch chan *schemas.BifrostStreamChunk) (string, []string) {
	var content strings.Builder
	var errs []string
	for chunk := range ch {
		if chunk.BifrostError != nil && chunk.BifrostError.Error != nil {
			errs = append(errs, chunk.BifrostError.Error.Message)
			continue
		}
		if chunk.BifrostChatResponse == nil {
			continue
		}
		for _, choice := range chunk.BifrostChatResponse.Choices {
			if choice.ChatStreamResponseChoice != nil && choice.ChatStreamResponseChoice.Delta != nil && choice.ChatStreamResponseChoice.Delta.Content != nil {
				content.WriteString(*choice.ChatStreamResponseChoice.Delta.Content)
			}
		}
	}
	return content.String(), errs
}

func newStreamTestClient(t *testing.T, account *MockAccount) *Bifrost {
	t.Helper()
	client, err := Init(context.Background(), schemas.BifrostConfig{
		Account: account,
		Logger:  NewDefaultLogger(schemas.LogLevelError),
	})
	if err != nil {
		t.Fatalf("failed to initialize bifrost: %v", err)
	}
	t.Cleanup(client.Shutdown)
	return client
}

func TestAzureSpeechErrorAfterPreamble(t *testing.T) {
	primary := httptest.NewServer(sseHandler(
		`{"type":"speech.audio.delta","audio":""}`,
		`{"error":{"message":"rate limited","type":"rate_limit_error"}}`,
	))
	defer primary.Close()

	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.Azure, 1, 1, primary.URL)
	account.configs[schemas.Azure].NetworkConfig.MaxRetries = 0
	account.SetKeysForProvider(schemas.Azure, []schemas.Key{{
		ID: "azure-key", Value: *schemas.NewSecretVar("test-key"),
		Models: schemas.WhiteList{"*"}, Weight: 100,
		AzureKeyConfig: &schemas.AzureKeyConfig{
			Endpoint: *schemas.NewSecretVar(primary.URL),
		},
	}})
	client := newStreamTestClient(t, account)

	ctx := schemas.NewBifrostContext(
		context.Background(), time.Now().Add(5*time.Second),
	)
	stream, err := client.SpeechStreamRequest(ctx, &schemas.BifrostSpeechRequest{
		Provider: schemas.Azure,
		Model:    "gpt-4o-mini-tts",
		Input:    &schemas.SpeechInput{Input: "hello"},
	})
	if stream != nil {
		for range stream {
		}
	}
	if err == nil || err.Error == nil || err.Error.Message != "rate limited" {
		t.Fatalf("expected original startup error, got %v", err)
	}
}

func TestStreamFallbackAfterFirstChunkError(t *testing.T) {
	primary := httptest.NewServer(sseHandler(`{"error":{"message":"rate limited","type":"rate_limit_error"}}`))
	defer primary.Close()
	var fallbackHits atomic.Int32
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackHits.Add(1)
		anthropicMessagesHandler()(w, r)
	}))
	defer fallback.Close()

	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 1, primary.URL)
	account.AddProviderWithBaseURL(schemas.Anthropic, 1, 1, fallback.URL)
	account.configs[schemas.OpenAI].NetworkConfig.MaxRetries = 0
	account.configs[schemas.Anthropic].NetworkConfig.MaxRetries = 0
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
		{ID: "primary-key", Value: *schemas.NewSecretVar("sk-primary"), Models: schemas.WhiteList{"*"}, Weight: 100},
	})
	account.SetKeysForProvider(schemas.Anthropic, []schemas.Key{
		{ID: "fallback-key", Value: *schemas.NewSecretVar("sk-fallback"), Models: schemas.WhiteList{"*"}, Weight: 100},
	})
	client := newStreamTestClient(t, account)

	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(30*time.Second))
	stream, bifrostErr := client.ChatCompletionStreamRequest(ctx, &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}},
		},
		Fallbacks: []schemas.Fallback{{Provider: schemas.Anthropic, Model: "claude-3-5-haiku-20241022"}},
	})
	if bifrostErr != nil {
		t.Fatalf("fallback stream failed (fallback server hit %d time(s)): %s", fallbackHits.Load(), bifrostErr.Error.Message)
	}
	content, errs := drainChatStream(stream)
	if got := fallbackHits.Load(); got != 1 {
		t.Fatalf("fallback server hits = %d, want 1", got)
	}
	if len(errs) > 0 {
		t.Fatalf("fallback stream emitted error chunks: %v", errs)
	}
	if content != "hello" {
		t.Fatalf("fallback stream content = %q, want %q", content, "hello")
	}
}

func TestAzureChatFallbackAfterPreamble(t *testing.T) {
	testAzureFallbackAfterPreamble(t, false)
}

func TestAzureResponsesFallbackAfterPreamble(t *testing.T) {
	testAzureFallbackAfterPreamble(t, true)
}

func testAzureFallbackAfterPreamble(t *testing.T, responses bool) {
	t.Helper()
	payloads := []string{
		`{"choices":[],"prompt_filter_results":[]}`,
		`{"id":"failed-attempt","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		`{"error":{"message":"rate limit exceeded","type":"rate_limit_error"}}`,
	}
	if responses {
		payloads = []string{
			`{"type":"response.created","response":{"id":"failed-attempt","status":"in_progress","output":[]}}`,
			`{"type":"response.in_progress","response":{"id":"failed-attempt","status":"in_progress","output":[]}}`,
			`{"type":"response.output_item.added","item":{"id":"failed-item","type":"message","role":"assistant","status":"in_progress","content":[]}}`,
			`{"type":"response.content_part.added","part":{"type":"output_text","text":"","annotations":[]}}`,
			`{"type":"response.failed","response":{"id":"failed-attempt","error":{"code":"rate_limit_exceeded","message":"rate limit exceeded"}}}`,
		}
	}
	primary := httptest.NewServer(sseHandler(payloads...))
	defer primary.Close()
	var fallbackHits atomic.Int32
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackHits.Add(1)
		anthropicMessagesHandler()(w, r)
	}))
	defer fallback.Close()

	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.Azure, 1, 1, primary.URL)
	account.AddProviderWithBaseURL(schemas.Anthropic, 1, 1, fallback.URL)
	account.configs[schemas.Azure].NetworkConfig.MaxRetries = 0
	account.configs[schemas.Anthropic].NetworkConfig.MaxRetries = 0
	account.SetKeysForProvider(schemas.Azure, []schemas.Key{{
		ID: "azure-key", Value: *schemas.NewSecretVar("test-key"),
		Models: schemas.WhiteList{"*"}, Weight: 100,
		AzureKeyConfig: &schemas.AzureKeyConfig{
			Endpoint: *schemas.NewSecretVar(primary.URL),
		},
	}})
	account.SetKeysForProvider(schemas.Anthropic, []schemas.Key{{
		ID: "fallback-key", Value: *schemas.NewSecretVar("test-key"),
		Models: schemas.WhiteList{"*"}, Weight: 100,
	}})
	client := newStreamTestClient(t, account)

	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(5*time.Second))
	request := &schemas.BifrostChatRequest{
		Provider: schemas.Azure,
		Model:    "gpt-4o-mini",
		Input: []schemas.ChatMessage{{
			Role: schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{
				ContentStr: schemas.Ptr("hi"),
			},
		}},
		Fallbacks: []schemas.Fallback{{
			Provider: schemas.Anthropic, Model: "claude-3-5-haiku-20241022",
		}},
	}
	var stream chan *schemas.BifrostStreamChunk
	var err *schemas.BifrostError
	if responses {
		stream, err = client.ResponsesStreamRequest(ctx, request.ToResponsesRequest())
	} else {
		stream, err = client.ChatCompletionStreamRequest(ctx, request)
	}
	if err != nil {
		t.Fatalf("fallback failed: %v", err)
	}
	if stream == nil {
		t.Fatal("expected fallback stream")
	}
	var content strings.Builder
	for chunk := range stream {
		if chunk == nil || chunk.BifrostError != nil {
			t.Fatalf("unexpected fallback chunk: %v", chunk)
		}
		if responses {
			response := chunk.BifrostResponsesStreamResponse
			if response == nil || response.ExtraFields.Provider != schemas.Anthropic {
				t.Fatal("received a chunk outside the successful fallback attempt")
			}
			if response.Type == schemas.ResponsesStreamResponseTypeOutputTextDelta &&
				response.Delta != nil {
				content.WriteString(*response.Delta)
			}
			continue
		}
		response := chunk.BifrostChatResponse
		if response == nil || response.ExtraFields.Provider != schemas.Anthropic {
			t.Fatal("received a chunk outside the successful fallback attempt")
		}
		for _, choice := range response.Choices {
			if choice.ChatStreamResponseChoice != nil && choice.Delta != nil &&
				choice.Delta.Content != nil {
				content.WriteString(*choice.Delta.Content)
			}
		}
	}
	if fallbackHits.Load() != 1 || content.String() != "hello" {
		t.Fatalf("fallback hits = %d, content = %q", fallbackHits.Load(), content.String())
	}
}

func TestStreamRetryAfterFirstChunkError(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			sseHandler(`{"error":{"message":"rate limit exceeded, please retry","type":"rate_limit_error"}}`)(w, r)
			return
		}
		sseHandler(
			`{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"he"}}]}`,
			`{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"llo"}}]}`,
			`{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`,
		)(w, r)
	}))
	defer server.Close()

	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 1, server.URL)
	account.configs[schemas.OpenAI].NetworkConfig.MaxRetries = 1
	account.configs[schemas.OpenAI].NetworkConfig.RetryBackoffInitial = time.Millisecond
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
		{ID: "retry-key", Value: *schemas.NewSecretVar("sk-retry"), Models: schemas.WhiteList{"*"}, Weight: 100},
	})
	client := newStreamTestClient(t, account)

	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(30*time.Second))
	stream, bifrostErr := client.ChatCompletionStreamRequest(ctx, &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}},
		},
	})
	if bifrostErr != nil {
		t.Fatalf("retried stream failed (server hit %d time(s)): %s", hits.Load(), bifrostErr.Error.Message)
	}
	content, errs := drainChatStream(stream)
	if hits.Load() != 2 {
		t.Fatalf("server hits = %d, want 2 (initial attempt plus one retry)", hits.Load())
	}
	if len(errs) > 0 {
		t.Fatalf("retried stream emitted error chunks: %v", errs)
	}
	if content != "hello" {
		t.Fatalf("retried stream content = %q, want %q", content, "hello")
	}
}
