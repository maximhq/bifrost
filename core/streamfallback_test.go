package bifrost

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"sync"
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

func newStreamTestClient(t *testing.T, account *MockAccount, plugins ...schemas.LLMPlugin) *Bifrost {
	t.Helper()
	client, err := Init(context.Background(), schemas.BifrostConfig{
		Account:    account,
		Logger:     NewDefaultLogger(schemas.LogLevelError),
		LLMPlugins: plugins,
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
	testFallbackAfterPreamble(t, schemas.Azure, false,
		`{"error":{"message":"rate limit exceeded","type":"rate_limit_error"}}`)
}

func TestAzureResponsesFallbackAfterPreamble(t *testing.T) {
	testFallbackAfterPreamble(t, schemas.Azure, true,
		`{"type":"response.failed","response":{"id":"failed-attempt","error":{"code":"rate_limit_exceeded","message":"rate limit exceeded"}}}`)
}

// Direct OpenAI under overload: the stream opens, emits startup events, then
// fails with server_is_overloaded (no HTTP status). The fallback must serve it.
func TestOpenAIChatFallbackAfterPreamble(t *testing.T) {
	testFallbackAfterPreamble(t, schemas.OpenAI, false,
		`{"error":{"message":"The server is overloaded. Please try again later.","type":"server_error","code":"server_is_overloaded"}}`)
}

func TestOpenAIResponsesFallbackAfterPreamble(t *testing.T) {
	testFallbackAfterPreamble(t, schemas.OpenAI, true,
		`{"type":"response.failed","response":{"id":"failed-attempt","status":"failed","error":{"code":"server_is_overloaded","message":"The server is overloaded. Please try again later."}}}`)
}

// testFallbackAfterPreamble serves startup events followed by failure from the
// primary and asserts the Anthropic fallback serves the whole stream.
func testFallbackAfterPreamble(t *testing.T, primaryProvider schemas.ModelProvider, responses bool, failure string) {
	t.Helper()
	payloads := []string{
		`{"choices":[],"prompt_filter_results":[]}`,
		`{"id":"failed-attempt","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		failure,
	}
	if responses {
		payloads = []string{
			`{"type":"response.created","response":{"id":"failed-attempt","status":"in_progress","output":[]}}`,
			`{"type":"response.in_progress","response":{"id":"failed-attempt","status":"in_progress","output":[]}}`,
			`{"type":"response.output_item.added","item":{"id":"failed-item","type":"message","role":"assistant","status":"in_progress","content":[]}}`,
			`{"type":"response.content_part.added","part":{"type":"output_text","text":"","annotations":[]}}`,
			failure,
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
	account.AddProviderWithBaseURL(primaryProvider, 1, 1, primary.URL)
	account.AddProviderWithBaseURL(schemas.Anthropic, 1, 1, fallback.URL)
	account.configs[primaryProvider].NetworkConfig.MaxRetries = 0
	account.configs[schemas.Anthropic].NetworkConfig.MaxRetries = 0
	primaryKey := schemas.Key{
		ID: "primary-key", Value: *schemas.NewSecretVar("test-key"),
		Models: schemas.WhiteList{"*"}, Weight: 100,
	}
	if primaryProvider == schemas.Azure {
		primaryKey.AzureKeyConfig = &schemas.AzureKeyConfig{
			Endpoint: *schemas.NewSecretVar(primary.URL),
		}
	}
	account.SetKeysForProvider(primaryProvider, []schemas.Key{primaryKey})
	account.SetKeysForProvider(schemas.Anthropic, []schemas.Key{{
		ID: "fallback-key", Value: *schemas.NewSecretVar("test-key"),
		Models: schemas.WhiteList{"*"}, Weight: 100,
	}})
	client := newStreamTestClient(t, account)

	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(5*time.Second))
	request := &schemas.BifrostChatRequest{
		Provider: primaryProvider,
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

// streamHookRecorder records the post-hooks a streamed request reaches, the way
// the logging and telemetry plugins see them.
type streamHookRecorder struct {
	mu     sync.Mutex
	pre    int
	errs   []string
	chunks int
	finals int
}

func (r *streamHookRecorder) GetName() string { return "stream-hook-recorder" }

func (r *streamHookRecorder) Cleanup() error { return nil }

func (r *streamHookRecorder) PreRequestHook(*schemas.BifrostContext, *schemas.BifrostRequest) error {
	return nil
}

func (r *streamHookRecorder) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pre++
	return req, nil, nil
}

func (r *streamHookRecorder) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if bifrostErr != nil {
		r.errs = append(r.errs, bifrostErr.GetErrorString())
		return resp, bifrostErr, nil
	}
	r.chunks++
	if IsFinalChunk(ctx) {
		r.finals++
	}
	return resp, bifrostErr, nil
}

func (r *streamHookRecorder) snapshot() (pre int, errs []string, chunks, finals int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pre, slices.Clone(r.errs), r.chunks, r.finals
}

const streamRateLimitEvent = `{"error":{"message":"rate limit exceeded, please retry","type":"rate_limit_error"}}`

// anthropicRateLimitHandler fails an Anthropic stream with a rate-limit error event.
func anthropicRateLimitHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"message\":\"rate limit exceeded, please retry\"}}\n\n")
}

// A streamed attempt that fails before any output and is then retried must stay
// invisible to plugins: one request, no error, exactly one last chunk. Before
// the fix the failed attempt ran every post-hook, so logging closed the request
// and dropped the retry that served the client, and telemetry's active-requests
// gauge was decremented twice.
func TestStreamRetryRunsPostHooksOnlyForServedAttempt(t *testing.T) {
	openAISuccess := sseHandler(
		`{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"he"}}]}`,
		`{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"llo"}}]}`,
		`{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`,
	)
	cases := []struct {
		name     string
		provider schemas.ModelProvider
		model    string
		failure  http.HandlerFunc
		success  http.HandlerFunc
	}{
		{
			name:     "openai error as first event",
			provider: schemas.OpenAI,
			model:    "gpt-4o-mini",
			failure:  sseHandler(streamRateLimitEvent),
			success:  openAISuccess,
		},
		{
			name:     "openai error after startup event",
			provider: schemas.OpenAI,
			model:    "gpt-4o-mini",
			failure:  sseHandler(`{"id":"failed","choices":[{"index":0,"delta":{"role":"assistant"}}]}`, streamRateLimitEvent),
			success:  openAISuccess,
		},
		{
			name:     "anthropic error as first event",
			provider: schemas.Anthropic,
			model:    "claude-3-5-haiku-20241022",
			failure:  anthropicRateLimitHandler,
			success:  anthropicMessagesHandler(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if hits.Add(1) == 1 {
					tc.failure(w, r)
					return
				}
				tc.success(w, r)
			}))
			defer server.Close()

			account := NewMockAccount()
			account.AddProviderWithBaseURL(tc.provider, 1, 1, server.URL)
			account.configs[tc.provider].NetworkConfig.MaxRetries = 1
			account.configs[tc.provider].NetworkConfig.RetryBackoffInitial = time.Millisecond
			account.SetKeysForProvider(tc.provider, []schemas.Key{
				{ID: "retry-key", Value: *schemas.NewSecretVar("sk-retry"), Models: schemas.WhiteList{"*"}, Weight: 100},
			})
			recorder := &streamHookRecorder{}
			client := newStreamTestClient(t, account, recorder)

			ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(30*time.Second))
			stream, bifrostErr := client.ChatCompletionStreamRequest(ctx, &schemas.BifrostChatRequest{
				Provider: tc.provider,
				Model:    tc.model,
				Input: []schemas.ChatMessage{
					{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: new("hi")}},
				},
			})
			if bifrostErr != nil {
				t.Fatalf("retried stream failed (server hit %d time(s)): %s", hits.Load(), bifrostErr.GetErrorString())
			}
			content, chunkErrs := drainChatStream(stream)
			if hits.Load() != 2 || content != "hello" || len(chunkErrs) > 0 {
				t.Fatalf("server hits = %d, content = %q, error chunks = %v; want 2, %q, none", hits.Load(), content, chunkErrs, "hello")
			}

			pre, errs, chunks, finals := recorder.snapshot()
			if pre != 1 {
				t.Errorf("pre-hooks = %d, want 1", pre)
			}
			if len(errs) != 0 {
				t.Errorf("error post-hooks = %v, want none: the retried attempt's error reached plugins", errs)
			}
			if finals != 1 {
				t.Errorf("post-hooks marked as the last chunk = %d of %d, want exactly 1", finals, chunks)
			}
		})
	}
}

// resetCountingTracer gives each request a trace and counts accumulator resets on it.
type resetCountingTracer struct {
	schemas.NoOpTracer
	resets atomic.Int32
}

func (t *resetCountingTracer) CreateTrace(_ string, _ ...string) string { return "retry-trace" }

func (t *resetCountingTracer) ResetStreamAccumulator(traceID string) {
	if traceID == "retry-trace" {
		t.resets.Add(1)
	}
}

// A retried stream attempt starts with the request's accumulated chunks cleared,
// so the failed attempt's startup events cannot shadow the retry's, whose chunk
// indexes restart at 0.
func TestStreamRetryResetsAccumulatedChunks(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			sseHandler(`{"id":"failed","choices":[{"index":0,"delta":{"role":"assistant"}}]}`, streamRateLimitEvent)(w, r)
			return
		}
		sseHandler(`{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`)(w, r)
	}))
	defer server.Close()

	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 1, server.URL)
	account.configs[schemas.OpenAI].NetworkConfig.MaxRetries = 1
	account.configs[schemas.OpenAI].NetworkConfig.RetryBackoffInitial = time.Millisecond
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
		{ID: "retry-key", Value: *schemas.NewSecretVar("sk-retry"), Models: schemas.WhiteList{"*"}, Weight: 100},
	})
	tracer := &resetCountingTracer{}
	client, err := Init(context.Background(), schemas.BifrostConfig{
		Account: account,
		Logger:  NewDefaultLogger(schemas.LogLevelError),
		Tracer:  tracer,
	})
	if err != nil {
		t.Fatalf("failed to initialize bifrost: %v", err)
	}
	t.Cleanup(client.Shutdown)

	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(30*time.Second))
	stream, bifrostErr := client.ChatCompletionStreamRequest(ctx, &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: new("hi")}},
		},
	})
	if bifrostErr != nil {
		t.Fatalf("retried stream failed: %s", bifrostErr.GetErrorString())
	}
	drainChatStream(stream)
	if hits.Load() != 2 || tracer.resets.Load() != 1 {
		t.Fatalf("server hits = %d, accumulator resets = %d; want 2 and one reset before the retry", hits.Load(), tracer.resets.Load())
	}
}

// When every streamed attempt fails before any output, plugins see the request
// end exactly once. Before the fix each attempt ran the error post-hooks and the
// caller ran them again, so logging wrote one error row per attempt plus one.
func TestStreamRetriesExhaustedRunErrorPostHooksOnce(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		sseHandler(streamRateLimitEvent)(w, r)
	}))
	defer server.Close()

	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 1, server.URL)
	account.configs[schemas.OpenAI].NetworkConfig.MaxRetries = 1
	account.configs[schemas.OpenAI].NetworkConfig.RetryBackoffInitial = time.Millisecond
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
		{ID: "retry-key", Value: *schemas.NewSecretVar("sk-retry"), Models: schemas.WhiteList{"*"}, Weight: 100},
	})
	recorder := &streamHookRecorder{}
	client := newStreamTestClient(t, account, recorder)

	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(30*time.Second))
	stream, bifrostErr := client.ChatCompletionStreamRequest(ctx, &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: new("hi")}},
		},
	})
	if bifrostErr == nil {
		drainChatStream(stream)
		t.Fatal("expected the request to fail once retries were exhausted")
	}
	if hits.Load() != 2 {
		t.Fatalf("server hits = %d, want 2 (initial attempt plus one retry)", hits.Load())
	}

	pre, errs, _, _ := recorder.snapshot()
	if pre != 1 || len(errs) != 1 {
		t.Fatalf("pre-hooks = %d, error post-hooks = %v; want 1 and exactly one error", pre, errs)
	}
	if !strings.Contains(errs[0], "rate limit exceeded") {
		t.Fatalf("error post-hook = %q, want the last attempt's rate-limit error", errs[0])
	}
}

// postHookSpanTracer records the plugin post-hook spans a request emits.
type postHookSpanTracer struct {
	schemas.NoOpTracer
	mu    sync.Mutex
	spans []string
}

func (t *postHookSpanTracer) StartSpan(ctx context.Context, name string, kind schemas.SpanKind) (context.Context, schemas.SpanHandle) {
	if strings.HasSuffix(name, ".posthook") {
		t.mu.Lock()
		t.spans = append(t.spans, name)
		t.mu.Unlock()
	}
	return t.NoOpTracer.StartSpan(ctx, name, kind)
}

// Stream post-hooks only accumulate timing; spans come from finalizing the
// pipeline. The worker's final error hooks for a held-back error must finalize
// theirs, or a request whose retries all fail traces no post-hook span.
func TestStreamRetriesExhaustedTraceErrorPostHooks(t *testing.T) {
	server := httptest.NewServer(sseHandler(streamRateLimitEvent))
	defer server.Close()

	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 1, server.URL)
	account.configs[schemas.OpenAI].NetworkConfig.MaxRetries = 1
	account.configs[schemas.OpenAI].NetworkConfig.RetryBackoffInitial = time.Millisecond
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
		{ID: "retry-key", Value: *schemas.NewSecretVar("sk-retry"), Models: schemas.WhiteList{"*"}, Weight: 100},
	})
	tracer := &postHookSpanTracer{}
	client, err := Init(context.Background(), schemas.BifrostConfig{
		Account:    account,
		Logger:     NewDefaultLogger(schemas.LogLevelError),
		Tracer:     tracer,
		LLMPlugins: []schemas.LLMPlugin{&streamHookRecorder{}},
	})
	if err != nil {
		t.Fatalf("failed to initialize bifrost: %v", err)
	}
	t.Cleanup(client.Shutdown)

	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(30*time.Second))
	stream, bifrostErr := client.ChatCompletionStreamRequest(ctx, &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: new("hi")}},
		},
	})
	if bifrostErr == nil {
		drainChatStream(stream)
		t.Fatal("expected the request to fail once retries were exhausted")
	}

	tracer.mu.Lock()
	defer tracer.mu.Unlock()
	if len(tracer.spans) != 1 || tracer.spans[0] != "plugin.stream-hook-recorder.posthook" {
		t.Fatalf("post-hook spans = %v, want one for the final error hooks", tracer.spans)
	}
}

// A client that gives up while a failed streamed attempt waits out its retry
// backoff must still leave plugins one terminal error. The failed attempt's
// error is held back in case the retry serves the request, so once no retry
// will run, the worker owes plugins the end of the request.
func TestStreamHeldBackErrorReachesPluginsWhenClientLeavesDuringBackoff(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		sseHandler(streamRateLimitEvent)(w, r)
	}))
	defer server.Close()

	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 1, server.URL)
	account.configs[schemas.OpenAI].NetworkConfig.MaxRetries = 1
	account.configs[schemas.OpenAI].NetworkConfig.RetryBackoffInitial = 3 * time.Second
	account.configs[schemas.OpenAI].NetworkConfig.RetryBackoffMax = 3 * time.Second
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
		{ID: "retry-key", Value: *schemas.NewSecretVar("sk-retry"), Models: schemas.WhiteList{"*"}, Weight: 100},
	})
	recorder := &streamHookRecorder{}
	client := newStreamTestClient(t, account, recorder)

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		stream, _ := client.ChatCompletionStreamRequest(ctx, &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o-mini",
			Input: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: new("hi")}},
			},
		})
		if stream != nil {
			drainChatStream(stream)
		}
	}()

	// Leave once the first attempt has failed and the retry is waiting out its backoff.
	deadline := time.Now().Add(2 * time.Second)
	for hits.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if hits.Load() != 1 {
		t.Fatalf("server hits = %d, want the first attempt to have run", hits.Load())
	}
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, errs, _, _ := recorder.snapshot(); len(errs) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Give a duplicate terminal time to land before counting.
	time.Sleep(100 * time.Millisecond)
	pre, errs, _, _ := recorder.snapshot()
	if pre != 1 || len(errs) != 1 {
		t.Fatalf("pre-hooks = %d, error post-hooks = %v; want 1 and exactly one error", pre, errs)
	}
	if hits.Load() != 1 {
		t.Fatalf("server hits = %d, want 1: the retry must not run after the client left", hits.Load())
	}
}

// errorRecoveringPlugin turns every error into an empty chat response, as a plugin
// that answers failed requests itself does.
type errorRecoveringPlugin struct{}

func (errorRecoveringPlugin) GetName() string { return "error-recovering" }
func (errorRecoveringPlugin) Cleanup() error  { return nil }
func (errorRecoveringPlugin) PreRequestHook(*schemas.BifrostContext, *schemas.BifrostRequest) error {
	return nil
}
func (errorRecoveringPlugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	return req, nil, nil
}
func (errorRecoveringPlugin) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if bifrostErr != nil {
		return &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{}}, nil, nil
	}
	return resp, nil, nil
}

// A held-back error that a plugin recovers after the client left must not leave the
// recovered stream's sender blocked. The worker's handoff to the departed caller can
// win its select over ctx.Done, and then nothing ever reads that stream.
func TestStreamRecoveredAfterClientLeftLeavesNoBlockedSender(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		sseHandler(streamRateLimitEvent)(w, r)
	}))
	defer server.Close()

	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 1, server.URL)
	account.configs[schemas.OpenAI].NetworkConfig.MaxRetries = 1
	account.configs[schemas.OpenAI].NetworkConfig.RetryBackoffInitial = 300 * time.Millisecond
	account.configs[schemas.OpenAI].NetworkConfig.RetryBackoffMax = 300 * time.Millisecond
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
		{ID: "retry-key", Value: *schemas.NewSecretVar("sk-retry"), Models: schemas.WhiteList{"*"}, Weight: 100},
	})
	client := newStreamTestClient(t, account, errorRecoveringPlugin{})

	// Each handoff is a coin flip, so repeat until a leak is all but certain.
	const requests = 20
	for range requests {
		before := hits.Load()
		ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			stream, _ := client.ChatCompletionStreamRequest(ctx, &schemas.BifrostChatRequest{
				Provider: schemas.OpenAI,
				Model:    "gpt-4o-mini",
				Input: []schemas.ChatMessage{
					{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: new("hi")}},
				},
			})
			if stream != nil {
				drainChatStream(stream)
			}
		}()
		// Leave once the first attempt has failed and the retry is waiting out its backoff.
		deadline := time.Now().Add(2 * time.Second)
		for hits.Load() == before && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if hits.Load() == before {
			cancel()
			t.Fatal("first attempt never reached the server")
		}
		time.Sleep(50 * time.Millisecond)
		cancel()
		<-done
	}

	blocked := 0
	deadline := time.Now().Add(2 * time.Second)
	for {
		buf := make([]byte, 1<<20)
		blocked = strings.Count(string(buf[:runtime.Stack(buf, true)]), "newBifrostMessageChan.func1(")
		if blocked == 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if blocked != 0 {
		t.Fatalf("%d of %d recovered streams left their sender goroutine blocked", blocked, requests)
	}
}

// Regression test for https://github.com/maximhq/bifrost/issues/7034.
//
// The primary accepts the TCP connection and never sends response headers
// (Failover Bench scenario S06). default_request_timeout_in_seconds must bound
// that wait so the streaming attempt fails with a 504, and the request's
// fallback must then be used. Before the fix the streaming client had no read
// timeout and the provider worker stayed pinned in client.Do until the upstream
// itself closed the socket; the fallback was never attempted.
func TestStreamFallbackAfterSilentPrimaryHeaderTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var (
		primaryHits atomic.Int32
		connsMu     sync.Mutex
		conns       []net.Conn
	)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			primaryHits.Add(1)
			connsMu.Lock()
			conns = append(conns, conn)
			connsMu.Unlock()
			go func() {
				// Consume the request, never answer.
				buf := make([]byte, 4096)
				for {
					if _, err := conn.Read(buf); err != nil {
						return
					}
				}
			}()
		}
	}()

	var fallbackHits atomic.Int32
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackHits.Add(1)
		anthropicMessagesHandler()(w, r)
	}))
	defer fallback.Close()

	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 1, "http://"+ln.Addr().String())
	account.AddProviderWithBaseURL(schemas.Anthropic, 1, 1, fallback.URL)
	// The reporter's config: 2 retries and a short request timeout. The timeout
	// must apply to the header wait, and a header timeout goes straight to the
	// fallback (504 RequestTimedOut is not retried).
	account.configs[schemas.OpenAI].NetworkConfig.DefaultRequestTimeoutInSeconds = 1
	account.configs[schemas.OpenAI].NetworkConfig.MaxRetries = 2
	account.configs[schemas.Anthropic].NetworkConfig.MaxRetries = 0
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
		{ID: "primary-key", Value: *schemas.NewSecretVar("sk-primary"), Models: schemas.WhiteList{"*"}, Weight: 100},
	})
	account.SetKeysForProvider(schemas.Anthropic, []schemas.Key{
		{ID: "fallback-key", Value: *schemas.NewSecretVar("sk-fallback"), Models: schemas.WhiteList{"*"}, Weight: 100},
	})
	client := newStreamTestClient(t, account)
	// Registered after newStreamTestClient so it runs BEFORE client.Shutdown
	// (cleanups are LIFO): closing the silent sockets is what lets a worker that
	// is still pinned in client.Do (the pre-fix behaviour) exit, otherwise
	// Shutdown would wait on it forever.
	t.Cleanup(func() {
		ln.Close()
		connsMu.Lock()
		defer connsMu.Unlock()
		for _, c := range conns {
			c.Close()
		}
	})

	type outcome struct {
		stream chan *schemas.BifrostStreamChunk
		err    *schemas.BifrostError
	}
	outcomes := make(chan outcome, 1)
	start := time.Now()
	go func() {
		ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(30*time.Second))
		stream, bifrostErr := client.ChatCompletionStreamRequest(ctx, &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o-mini",
			Input: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}},
			},
			Fallbacks: []schemas.Fallback{{Provider: schemas.Anthropic, Model: "claude-3-5-haiku-20241022"}},
		})
		outcomes <- outcome{stream: stream, err: bifrostErr}
	}()

	var got outcome
	select {
	case got = <-outcomes:
	case <-time.After(10 * time.Second):
		t.Fatalf("silent primary never timed out: no stream and no error after 10s (primary hit %d time(s), fallback hit %d time(s)); issue #7034", primaryHits.Load(), fallbackHits.Load())
	}
	elapsed := time.Since(start)
	if got.err != nil {
		t.Fatalf("fallback stream failed after %v (primary hit %d, fallback hit %d): %s", elapsed, primaryHits.Load(), fallbackHits.Load(), got.err.Error.Message)
	}
	content, errs := drainChatStream(got.stream)
	if hits := fallbackHits.Load(); hits != 1 {
		t.Fatalf("fallback server hits = %d, want 1", hits)
	}
	if len(errs) > 0 {
		t.Fatalf("fallback stream emitted error chunks: %v", errs)
	}
	if content != "hello" {
		t.Fatalf("fallback stream content = %q, want %q", content, "hello")
	}
	if elapsed < 900*time.Millisecond {
		t.Fatalf("fallback answered after %v, before the 1s request timeout could have fired", elapsed)
	}
	if elapsed > 6*time.Second {
		t.Fatalf("fallback answered after %v, want roughly the 1s request timeout", elapsed)
	}
}

// newClosingListener serves an upstream that accepts each connection, reads the
// request once and closes the socket without a single response byte. It returns
// the listener, the accepted-connection counter, and the closed-connection
// counter; the latter is the signal that an attempt has actually failed, since
// accept happens before the request is read and the socket closed.
func newClosingListener(t *testing.T) (net.Listener, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	accepted := new(atomic.Int32)
	closed := new(atomic.Int32)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			go func() {
				buf := make([]byte, 64*1024)
				_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
				_, _ = conn.Read(buf)
				_ = conn.Close()
				closed.Add(1)
			}()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return ln, accepted, closed
}

// awaitFirstClose blocks until the closing upstream has closed its first
// connection, which is the moment the first attempt fails and the worker moves
// into the retry backoff.
func awaitFirstClose(t *testing.T, closed *atomic.Int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for closed.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if closed.Load() == 0 {
		t.Fatal("upstream never closed the first attempt's connection")
	}
}

// Regression test for https://github.com/maximhq/bifrost/issues/7035.
//
// fasthttp's stale-connection retry hook (network.StaleConnectionRetryIfErr)
// fired on any close-before-headers, including on a socket dialed for this very
// attempt, so each Bifrost attempt could become four upstream dials. With
// max_retries 2 the reporter counted 10 upstream attempts for one request (12
// was the ceiling). The budget must be max_retries + 1 upstream attempts,
// whoever closed the socket.
func TestStreamRetryBudgetWhenUpstreamClosesBeforeHeaders(t *testing.T) {
	ln, accepted, _ := newClosingListener(t)

	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 1, "http://"+ln.Addr().String())
	account.configs[schemas.OpenAI].NetworkConfig.MaxRetries = 2
	account.configs[schemas.OpenAI].NetworkConfig.RetryBackoffInitial = 10 * time.Millisecond
	account.configs[schemas.OpenAI].NetworkConfig.RetryBackoffMax = 20 * time.Millisecond
	account.configs[schemas.OpenAI].NetworkConfig.DefaultRequestTimeoutInSeconds = 5
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
		{ID: "primary-key", Value: *schemas.NewSecretVar("sk-primary"), Models: schemas.WhiteList{"*"}, Weight: 100},
	})
	client := newStreamTestClient(t, account)

	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(30*time.Second))
	start := time.Now()
	stream, bifrostErr := client.ChatCompletionStreamRequest(ctx, &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}},
		},
	})
	elapsed := time.Since(start)
	if stream != nil {
		for range stream {
		}
	}
	if bifrostErr == nil || bifrostErr.Error == nil {
		t.Fatalf("expected an upstream connection error, got %v (upstream accepted %d)", bifrostErr, accepted.Load())
	}
	if msg := bifrostErr.Error.Message; msg != schemas.ErrProviderDoRequest && msg != schemas.ErrProviderNetworkError {
		t.Fatalf("error message = %q, want a retryable connection error", msg)
	}
	if got := accepted.Load(); got != 3 {
		t.Fatalf("upstream accepted %d connection(s), want 3 (one attempt plus max_retries 2): fasthttp retried inside each Bifrost attempt (issue #7035)", got)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("request took %v, want well under the 5s request timeout", elapsed)
	}
}

// The sleep between retry attempts must end when the request context is
// cancelled, otherwise a client that has already gone (issue #7035) keeps a
// provider worker parked for up to retry_backoff_max before the loop notices.
// The caller itself returns as soon as its context is cancelled, so the worker
// is what this test measures: Shutdown waits for every worker to exit. The
// loop must also leave the backoff as a cancellation, not as one more attempt:
// the attempt trail keeps exactly the attempt that ran.
func TestRetryBackoffStopsWhenRequestCancelled(t *testing.T) {
	ln, accepted, closed := newClosingListener(t)

	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 1, "http://"+ln.Addr().String())
	account.configs[schemas.OpenAI].NetworkConfig.MaxRetries = 1
	account.configs[schemas.OpenAI].NetworkConfig.RetryBackoffInitial = 3 * time.Second
	account.configs[schemas.OpenAI].NetworkConfig.RetryBackoffMax = 3 * time.Second
	account.configs[schemas.OpenAI].NetworkConfig.DefaultRequestTimeoutInSeconds = 5
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
		{ID: "primary-key", Value: *schemas.NewSecretVar("sk-primary"), Models: schemas.WhiteList{"*"}, Weight: 100},
	})
	client, err := Init(context.Background(), schemas.BifrostConfig{
		Account: account,
		Logger:  NewDefaultLogger(schemas.LogLevelError),
	})
	if err != nil {
		t.Fatalf("failed to initialize bifrost: %v", err)
	}
	// Registered so a t.Fatal before the timed inline Shutdown below still stops
	// the workers; Shutdown is idempotent, so the inline call stays.
	t.Cleanup(client.Shutdown)

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	errCh := make(chan *schemas.BifrostError, 1)
	go func() {
		stream, bifrostErr := client.ChatCompletionStreamRequest(ctx, &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o-mini",
			Input: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}},
			},
		})
		if stream != nil {
			for range stream {
			}
		}
		errCh <- bifrostErr
	}()

	// Once the upstream has closed the first attempt's socket the worker is in
	// its 3s backoff; a short grace covers the client observing the close.
	awaitFirstClose(t, closed)
	time.Sleep(100 * time.Millisecond)
	cancel()

	start := time.Now()
	client.Shutdown()
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Fatalf("Shutdown waited %v for a worker whose request was cancelled: the retry backoff ignored the cancelled context", elapsed)
	}

	bifrostErr := <-errCh
	if bifrostErr == nil || bifrostErr.Error == nil || bifrostErr.Error.Type == nil || *bifrostErr.Error.Type != schemas.RequestCancelled {
		t.Fatalf("error after cancel = %v, want type %s", bifrostErr, schemas.RequestCancelled)
	}
	if got := accepted.Load(); got != 1 {
		t.Fatalf("upstream accepted %d connection(s), want 1: a cancelled request must not be retried", got)
	}
	trail, _ := ctx.Value(schemas.BifrostContextKeyAttemptTrail).([]schemas.KeyAttemptRecord)
	if len(trail) != 1 {
		t.Fatalf("attempt trail has %d record(s), want 1: the cancelled backoff was recorded as another attempt", len(trail))
	}
	// The routing log must record the outcome as a cancellation, not as an
	// internal Bifrost error: the cancel error carries IsBifrostError too, and
	// the terminal log entry has to look at the cancellation first.
	var sawCancelled bool
	for _, entry := range ctx.GetRoutingEngineLogs() {
		if strings.Contains(entry.Message, "internal Bifrost error") {
			t.Fatalf("routing log classified the cancelled backoff as an internal error: %q", entry.Message)
		}
		if strings.Contains(entry.Message, "cancelled after 1 attempt(s)") {
			sawCancelled = true
		}
	}
	if !sawCancelled {
		t.Fatal("routing log has no terminal entry reporting the request as cancelled after 1 attempt(s)")
	}
}

// A request whose deadline expires during the retry backoff ends as a timeout:
// the worker is freed at once and the routing log records a timeout, not an
// internal Bifrost error, even though the timeout error carries IsBifrostError.
func TestRetryBackoffStopsWhenRequestDeadlineExpires(t *testing.T) {
	ln, accepted, closed := newClosingListener(t)

	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 1, "http://"+ln.Addr().String())
	account.configs[schemas.OpenAI].NetworkConfig.MaxRetries = 1
	account.configs[schemas.OpenAI].NetworkConfig.RetryBackoffInitial = 3 * time.Second
	account.configs[schemas.OpenAI].NetworkConfig.RetryBackoffMax = 3 * time.Second
	account.configs[schemas.OpenAI].NetworkConfig.DefaultRequestTimeoutInSeconds = 5
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
		{ID: "primary-key", Value: *schemas.NewSecretVar("sk-primary"), Models: schemas.WhiteList{"*"}, Weight: 100},
	})
	client, err := Init(context.Background(), schemas.BifrostConfig{
		Account: account,
		Logger:  NewDefaultLogger(schemas.LogLevelError),
	})
	if err != nil {
		t.Fatalf("failed to initialize bifrost: %v", err)
	}
	// Registered so a t.Fatal before the timed inline Shutdown below still stops
	// the workers; Shutdown is idempotent, so the inline call stays.
	t.Cleanup(client.Shutdown)

	// The deadline lands inside the 3s backoff that follows the first failed
	// attempt, with margin for the request to be enqueued and fail on a slow runner.
	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(1*time.Second))
	errCh := make(chan *schemas.BifrostError, 1)
	go func() {
		stream, bifrostErr := client.ChatCompletionStreamRequest(ctx, &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o-mini",
			Input: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}},
			},
		})
		if stream != nil {
			for range stream {
			}
		}
		errCh <- bifrostErr
	}()

	// The first attempt must have failed before the deadline, otherwise the
	// timeout would come from the attempt itself and never reach the backoff.
	awaitFirstClose(t, closed)
	if ctx.Err() != nil {
		t.Fatal("deadline expired before the first attempt failed; the backoff path was not exercised")
	}
	<-ctx.Done()

	start := time.Now()
	client.Shutdown()
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Fatalf("Shutdown waited %v for a worker whose request had timed out: the retry backoff ignored the expired context", elapsed)
	}

	bifrostErr := <-errCh
	if bifrostErr == nil || bifrostErr.Error == nil || bifrostErr.Error.Type == nil || *bifrostErr.Error.Type != schemas.RequestTimedOut {
		t.Fatalf("error after the deadline = %v, want type %s", bifrostErr, schemas.RequestTimedOut)
	}
	if got := accepted.Load(); got != 1 {
		t.Fatalf("upstream accepted %d connection(s), want 1: a timed-out request must not be retried", got)
	}
	var sawTimedOut bool
	for _, entry := range ctx.GetRoutingEngineLogs() {
		if strings.Contains(entry.Message, "internal Bifrost error") {
			t.Fatalf("routing log classified the expired backoff as an internal error: %q", entry.Message)
		}
		if strings.Contains(entry.Message, "timed out after 1 attempt(s)") {
			sawTimedOut = true
		}
	}
	if !sawTimedOut {
		t.Fatal("routing log has no terminal entry reporting the request as timed out after 1 attempt(s)")
	}
}

// cancelOnKeySelectionTracer cancels the request context when the n-th
// key.selection span starts. Core opens that span once per attempt that selects
// a key, so the second one is the rotation after a per-key failure: cancelling
// there lands the cancel on the no-backoff path between selection and dispatch.
type cancelOnKeySelectionTracer struct {
	schemas.NoOpTracer
	selections atomic.Int32
	onNth      int32
	cancel     context.CancelFunc
}

func (t *cancelOnKeySelectionTracer) StartSpan(ctx context.Context, name string, kind schemas.SpanKind) (context.Context, schemas.SpanHandle) {
	if name == "key.selection" && t.selections.Add(1) == t.onNth {
		t.cancel()
	}
	return t.NoOpTracer.StartSpan(ctx, name, kind)
}

// A permanent per-key failure (401) rotates to the next key without a backoff.
// A request cancelled while that rotation is being selected must not dispatch
// the rotated attempt: no upstream hit, no trail record, a cancellation error.
func TestRotationWithoutBackoffStopsWhenRequestCancelled(t *testing.T) {
	var hits atomic.Int32
	upstream := newRecordingServer(func(attempt int, w http.ResponseWriter) {
		hits.Add(1)
		writeJSON(w, http.StatusUnauthorized, `{"error":{"message":"invalid api key","type":"invalid_request_error","code":"invalid_api_key"}}`)
	})
	defer upstream.Close()

	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 1, upstream.URL)
	account.configs[schemas.OpenAI].NetworkConfig.MaxRetries = 1
	account.configs[schemas.OpenAI].NetworkConfig.RetryBackoffInitial = 3 * time.Second
	account.configs[schemas.OpenAI].NetworkConfig.RetryBackoffMax = 3 * time.Second
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
		{ID: "key-a", Name: "key-a", Value: *schemas.NewSecretVar("sk-a"), Models: schemas.WhiteList{"*"}, Weight: 100},
		{ID: "key-b", Name: "key-b", Value: *schemas.NewSecretVar("sk-b"), Models: schemas.WhiteList{"*"}, Weight: 100},
	})
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	// Bifrost installs its configured tracer on every request context, so the
	// hook has to be the configured tracer rather than a context value.
	tracer := &cancelOnKeySelectionTracer{onNth: 2, cancel: cancel}
	client, err := Init(context.Background(), schemas.BifrostConfig{
		Account: account,
		Logger:  NewDefaultLogger(schemas.LogLevelError),
		Tracer:  tracer,
	})
	if err != nil {
		t.Fatalf("failed to initialize bifrost: %v", err)
	}
	// Registered so a t.Fatal before the timed inline Shutdown below still stops
	// the workers; Shutdown is idempotent, so the inline call stays.
	t.Cleanup(client.Shutdown)

	errCh := make(chan *schemas.BifrostError, 1)
	go func() {
		_, bifrostErr := client.ChatCompletionRequest(ctx, &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o-mini",
			Input: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}},
			},
		})
		errCh <- bifrostErr
	}()

	// Wait for the rotation's key selection, which is where the cancel fires.
	deadline := time.Now().Add(2 * time.Second)
	for tracer.selections.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := tracer.selections.Load(); got < 2 {
		t.Fatalf("key.selection spans = %d, want at least 2 (the 401 must have triggered a rotation)", got)
	}

	start := time.Now()
	client.Shutdown()
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Fatalf("Shutdown waited %v: the worker kept going after the cancel", elapsed)
	}
	bifrostErr := <-errCh
	if bifrostErr == nil || bifrostErr.Error == nil || bifrostErr.Error.Type == nil || *bifrostErr.Error.Type != schemas.RequestCancelled {
		t.Fatalf("error after cancel = %v, want type %s", bifrostErr, schemas.RequestCancelled)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("upstream served %d request(s), want 1: the rotated attempt was dispatched after the cancel", got)
	}
	trail, _ := ctx.Value(schemas.BifrostContextKeyAttemptTrail).([]schemas.KeyAttemptRecord)
	if len(trail) != 1 {
		t.Fatalf("attempt trail has %d record(s), want 1: the cancelled rotation was recorded as a dispatched attempt (%+v)", len(trail), trail)
	}
}

// A 429 rotates to the next key and the rotation is recorded on the failed
// attempt's trail record. If the request is cancelled during the backoff that
// precedes the rotated attempt, that attempt never runs, so the record must
// not claim it triggered a rotation (maximhq/bifrost#7105 review).
func TestRotationMarkerNotSetWhenCancelledDuringBackoff(t *testing.T) {
	var hits atomic.Int32
	upstream := newRecordingServer(func(attempt int, w http.ResponseWriter) {
		hits.Add(1)
		writeJSON(w, http.StatusTooManyRequests, azureRateLimitBody)
	})
	defer upstream.Close()

	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 1, upstream.URL)
	account.configs[schemas.OpenAI].NetworkConfig.MaxRetries = 1
	account.configs[schemas.OpenAI].NetworkConfig.RetryBackoffInitial = 3 * time.Second
	account.configs[schemas.OpenAI].NetworkConfig.RetryBackoffMax = 3 * time.Second
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
		{ID: "key-a", Name: "key-a", Value: *schemas.NewSecretVar("sk-a"), Models: schemas.WhiteList{"*"}, Weight: 100},
		{ID: "key-b", Name: "key-b", Value: *schemas.NewSecretVar("sk-b"), Models: schemas.WhiteList{"*"}, Weight: 100},
	})
	client, err := Init(context.Background(), schemas.BifrostConfig{
		Account: account,
		Logger:  NewDefaultLogger(schemas.LogLevelError),
	})
	if err != nil {
		t.Fatalf("failed to initialize bifrost: %v", err)
	}
	// Registered so a t.Fatal before the timed inline Shutdown below still stops
	// the workers; Shutdown is idempotent, so the inline call stays.
	t.Cleanup(client.Shutdown)

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	go func() {
		_, _ = client.ChatCompletionRequest(ctx, &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o-mini",
			Input: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}},
			},
		})
	}()

	// The first attempt is rate limited within milliseconds; the worker then
	// rotates to the other key and enters its 3s backoff.
	deadline := time.Now().Add(2 * time.Second)
	for hits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if hits.Load() == 0 {
		t.Fatal("upstream never saw the first attempt")
	}
	time.Sleep(100 * time.Millisecond)
	cancel()
	client.Shutdown()

	if got := hits.Load(); got != 1 {
		t.Fatalf("upstream served %d request(s), want 1: the rotated attempt must not run after the cancel", got)
	}
	trail, _ := ctx.Value(schemas.BifrostContextKeyAttemptTrail).([]schemas.KeyAttemptRecord)
	if len(trail) != 1 {
		t.Fatalf("attempt trail has %d record(s), want 1", len(trail))
	}
	if trail[0].TriggeredRotation {
		t.Fatalf("trail record %+v claims it triggered a rotation, but the rotated attempt was cancelled before it ran", trail[0])
	}
}

// ---------------------------------------------------------------------------
// TTFT deadline (BifrostContextKeyStreamFirstTokenTimeout)
// ---------------------------------------------------------------------------

// ttftUpstream records hits and hangups for a streaming test server.
type ttftUpstream struct {
	hits    atomic.Int32
	hangups chan struct{}
}

func newTTFTUpstream() *ttftUpstream {
	return &ttftUpstream{hangups: make(chan struct{}, 8)}
}

// stall blocks until delay passes, or records a hangup and reports false when
// the client closed the request first. The body is read first: net/http only
// notices a client hangup (and cancels r.Context) once the body is consumed.
func (u *ttftUpstream) stall(r *http.Request, delay time.Duration) bool {
	_, _ = io.Copy(io.Discard, r.Body)
	select {
	case <-time.After(delay):
		return true
	case <-r.Context().Done():
		u.hangups <- struct{}{}
		return false
	}
}

// slowOpenAIHandler sends startup chunks only (an empty choices frame and a
// role-only delta), stalls for delay, then sends the text "hello".
func (u *ttftUpstream) slowOpenAIHandler(delay time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u.hits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		send := func(data string) {
			fmt.Fprintf(w, "data: %s\n\n", data)
			fl.Flush()
		}
		send(`{"id":"c1","object":"chat.completion.chunk","choices":[]}`)
		send(`{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant"}}]}`)
		if !u.stall(r, delay) {
			return
		}
		send(`{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hello"}}]}`)
		send(`{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		send("[DONE]")
	}
}

// slowAnthropicHandler sends message_start and an empty content block (no
// token yet), stalls for delay, then streams the text "hello".
func (u *ttftUpstream) slowAnthropicHandler(delay time.Duration) http.HandlerFunc {
	events := []struct{ typ, data string }{
		{"message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-3-5-haiku-20241022","usage":{"input_tokens":10,"output_tokens":1}}}`},
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`},
		{"content_block_stop", `{"type":"content_block_stop","index":0}`},
		{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`},
		{"message_stop", `{"type":"message_stop"}`},
	}
	return func(w http.ResponseWriter, r *http.Request) {
		u.hits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for i, e := range events {
			if i == 2 && !u.stall(r, delay) {
				return
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.typ, e.data)
			fl.Flush()
		}
	}
}

// newTTFTTestClient wires OpenAI (primary) and Anthropic (fallback) to the two
// handlers. The primary is allowed retries, so a test can prove a TTFT miss
// never retries the same provider.
func newTTFTTestClient(t *testing.T, primary, fallback http.Handler) *Bifrost {
	t.Helper()
	primarySrv := httptest.NewServer(primary)
	fallbackSrv := httptest.NewServer(fallback)
	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 1, primarySrv.URL)
	account.AddProviderWithBaseURL(schemas.Anthropic, 1, 1, fallbackSrv.URL)
	account.configs[schemas.OpenAI].NetworkConfig.MaxRetries = 2
	account.configs[schemas.Anthropic].NetworkConfig.MaxRetries = 0
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
		{ID: "primary-key", Value: *schemas.NewSecretVar("sk-primary"), Models: schemas.WhiteList{"*"}, Weight: 100},
	})
	account.SetKeysForProvider(schemas.Anthropic, []schemas.Key{
		{ID: "fallback-key", Value: *schemas.NewSecretVar("sk-fallback"), Models: schemas.WhiteList{"*"}, Weight: 100},
	})
	client := newStreamTestClient(t, account)
	// LIFO: close the servers before Shutdown so a stalled handler cannot pin it.
	t.Cleanup(func() {
		primarySrv.CloseClientConnections()
		fallbackSrv.CloseClientConnections()
		primarySrv.Close()
		fallbackSrv.Close()
	})
	return client
}

func ttftChatRequest(fallbacks ...schemas.Fallback) *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}},
		},
		Fallbacks: fallbacks,
	}
}

func ttftContext(timeout time.Duration) *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(30*time.Second))
	if timeout > 0 {
		ctx.SetValue(schemas.BifrostContextKeyStreamFirstTokenTimeout, timeout)
	}
	return ctx
}

var anthropicFallback = schemas.Fallback{Provider: schemas.Anthropic, Model: "claude-3-5-haiku-20241022"}

// A primary that opens the stream but sends only startup events is cut off at
// the TTFT deadline, without a same-provider retry, and the fallback serves the
// whole stream. The primary's socket is closed, not left to the idle timeout.
func TestStreamTTFTCutsOffStartupOnlyPrimary(t *testing.T) {
	primary, fallback := newTTFTUpstream(), newTTFTUpstream()
	client := newTTFTTestClient(t, primary.slowOpenAIHandler(10*time.Second), fallback.slowAnthropicHandler(0))

	ctx := ttftContext(300 * time.Millisecond)
	start := time.Now()
	stream, bifrostErr := client.ChatCompletionStreamRequest(ctx, ttftChatRequest(anthropicFallback))
	if bifrostErr != nil {
		t.Fatalf("expected the fallback to serve the stream, got %s", bifrostErr.Error.Message)
	}
	content, errs := drainChatStream(stream)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("TTFT cutoff took %v; the primary was not cut off at 300ms", elapsed)
	}
	if content != "hello" || len(errs) > 0 {
		t.Fatalf("content = %q errs = %v, want the fallback's %q", content, errs, "hello")
	}
	if got := primary.hits.Load(); got != 1 {
		t.Fatalf("primary hits = %d, want 1: a TTFT miss must not retry the same provider", got)
	}
	if got := fallback.hits.Load(); got != 1 {
		t.Fatalf("fallback hits = %d, want 1", got)
	}
	select {
	case <-primary.hangups:
	case <-time.After(3 * time.Second):
		t.Fatal("the cut-off primary's connection is still open")
	}
	found := false
	for _, entry := range ctx.GetRoutingEngineLogs() {
		if strings.Contains(entry.Message, "TTFT timeout") && strings.Contains(entry.Message, "openai/gpt-4o-mini") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no TTFT timeout entry in routing engine logs: %+v", ctx.GetRoutingEngineLogs())
	}
	if ctx.Value(schemas.BifrostContextKeyStreamAttemptAbort) != nil {
		t.Fatal("the attempt abort handle leaked past its attempt")
	}
}

// A primary that never sends response headers is cut off at the TTFT deadline
// well before default_request_timeout_in_seconds.
func TestStreamTTFTCutsOffSilentPrimaryHeaderWait(t *testing.T) {
	primary, fallback := newTTFTUpstream(), newTTFTUpstream()
	silent := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primary.hits.Add(1)
		primary.stall(r, 30*time.Second)
	})
	client := newTTFTTestClient(t, silent, fallback.slowAnthropicHandler(0))

	start := time.Now()
	stream, bifrostErr := client.ChatCompletionStreamRequest(ttftContext(300*time.Millisecond), ttftChatRequest(anthropicFallback))
	if bifrostErr != nil {
		t.Fatalf("expected the fallback to serve the stream, got %s", bifrostErr.Error.Message)
	}
	content, _ := drainChatStream(stream)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("TTFT cutoff took %v during the header wait", elapsed)
	}
	if content != "hello" {
		t.Fatalf("content = %q, want the fallback's %q", content, "hello")
	}
	if got := primary.hits.Load(); got != 1 {
		t.Fatalf("primary hits = %d, want 1: a TTFT miss must not retry the same provider", got)
	}
	select {
	case <-primary.hangups:
	case <-time.After(3 * time.Second):
		t.Fatal("the cut-off primary's socket is still open after the header wait was cut off")
	}
}

// The last attempt always runs to an answer: a slow fallback that is the last
// in line is not cut off, and neither is a primary with no fallbacks.
func TestStreamTTFTNeverCutsOffLastAttempt(t *testing.T) {
	t.Run("last fallback", func(t *testing.T) {
		primary, fallback := newTTFTUpstream(), newTTFTUpstream()
		client := newTTFTTestClient(t, primary.slowOpenAIHandler(10*time.Second), fallback.slowAnthropicHandler(time.Second))
		stream, bifrostErr := client.ChatCompletionStreamRequest(ttftContext(200*time.Millisecond), ttftChatRequest(anthropicFallback))
		if bifrostErr != nil {
			t.Fatalf("the last fallback was cut off: %s", bifrostErr.Error.Message)
		}
		if content, _ := drainChatStream(stream); content != "hello" {
			t.Fatalf("content = %q, want %q", content, "hello")
		}
	})
	t.Run("primary without fallbacks", func(t *testing.T) {
		primary, fallback := newTTFTUpstream(), newTTFTUpstream()
		client := newTTFTTestClient(t, primary.slowOpenAIHandler(time.Second), fallback.slowAnthropicHandler(0))
		stream, bifrostErr := client.ChatCompletionStreamRequest(ttftContext(200*time.Millisecond), ttftChatRequest())
		if bifrostErr != nil {
			t.Fatalf("a primary with no fallbacks was cut off: %s", bifrostErr.Error.Message)
		}
		if content, _ := drainChatStream(stream); content != "hello" {
			t.Fatalf("content = %q, want %q", content, "hello")
		}
		if got := fallback.hits.Load(); got != 0 {
			t.Fatalf("fallback hits = %d, want 0", got)
		}
	})
}

// A primary whose first token beats the deadline serves the stream, and a
// request without a deadline keeps today's behaviour for a slow primary.
func TestStreamTTFTLeavesTimelyOrUnconfiguredPrimaryAlone(t *testing.T) {
	cases := []struct {
		name    string
		delay   time.Duration
		timeout time.Duration
	}{
		{name: "first token before deadline", delay: 50 * time.Millisecond, timeout: 2 * time.Second},
		{name: "no deadline configured", delay: time.Second, timeout: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			primary, fallback := newTTFTUpstream(), newTTFTUpstream()
			client := newTTFTTestClient(t, primary.slowOpenAIHandler(tc.delay), fallback.slowAnthropicHandler(0))
			stream, bifrostErr := client.ChatCompletionStreamRequest(ttftContext(tc.timeout), ttftChatRequest(anthropicFallback))
			if bifrostErr != nil {
				t.Fatalf("primary failed: %s", bifrostErr.Error.Message)
			}
			if content, errs := drainChatStream(stream); content != "hello" || len(errs) > 0 {
				t.Fatalf("content = %q errs = %v, want the primary's %q", content, errs, "hello")
			}
			if got := fallback.hits.Load(); got != 0 {
				t.Fatalf("fallback hits = %d, want 0", got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Provider response headers once every fallback failed
// ---------------------------------------------------------------------------

// notFoundUpstream answers every request with a 404 in its provider's error shape, tagged with an
// X-Upstream header naming the provider, so a test can tell whose headers survived.
func notFoundUpstream(provider, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Upstream", provider)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(body))
	}
}

// providerHeader reads one forwarded provider response header off ctx, matching its name in any case.
func providerHeader(ctx *schemas.BifrostContext, name string) string {
	headers, _ := ctx.Value(schemas.BifrostContextKeyProviderResponseHeaders).(map[string]string)
	for k, v := range headers {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

// When the primary and every fallback fail, the primary's error is returned, so the provider
// response headers left on the context for the transport to forward must be the primary's too.
// They used to be the last fallback's: an OpenAI error went out with Anthropic's request-id.
func TestExhaustedFallbacksKeepThePrimaryProviderHeaders(t *testing.T) {
	openAI404 := notFoundUpstream("openai", `{"error":{"message":"The model does not exist","type":"invalid_request_error","code":"model_not_found"}}`)
	anthropic404 := notFoundUpstream("anthropic", `{"type":"error","error":{"type":"not_found_error","message":"model: claude-missing"}}`)
	fallback := schemas.Fallback{Provider: schemas.Anthropic, Model: "claude-missing"}

	t.Run("non-streaming", func(t *testing.T) {
		client := newTTFTTestClient(t, openAI404, anthropic404)
		ctx := ttftContext(0)
		_, bifrostErr := client.ChatCompletionRequest(ctx, ttftChatRequest(fallback))
		if bifrostErr == nil {
			t.Fatal("expected the primary's error once every fallback failed")
		}
		if got := providerHeader(ctx, "X-Upstream"); got != "openai" {
			t.Fatalf("forwarded provider headers belong to %q, want the primary's (openai) with the primary's error", got)
		}
	})
	t.Run("streaming", func(t *testing.T) {
		client := newTTFTTestClient(t, openAI404, anthropic404)
		ctx := ttftContext(0)
		_, bifrostErr := client.ChatCompletionStreamRequest(ctx, ttftChatRequest(fallback))
		if bifrostErr == nil {
			t.Fatal("expected the primary's error once every fallback failed")
		}
		if got := providerHeader(ctx, "X-Upstream"); got != "openai" {
			t.Fatalf("forwarded provider headers belong to %q, want the primary's (openai) with the primary's error", got)
		}
	})

	// A primary that never answered had no headers, so none go out with its error, not the last
	// fallback's: they would describe a provider the error did not come from.
	dropsConnection := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
			_ = conn.Close()
		}
	})
	t.Run("non-streaming, primary sent no headers", func(t *testing.T) {
		client := newTTFTTestClient(t, dropsConnection, anthropic404)
		ctx := ttftContext(0)
		_, bifrostErr := client.ChatCompletionRequest(ctx, ttftChatRequest(fallback))
		if bifrostErr == nil {
			t.Fatal("expected the primary's error once every fallback failed")
		}
		if headers := ctx.Value(schemas.BifrostContextKeyProviderResponseHeaders); headers != nil {
			t.Fatalf("provider headers %v were left on the context with the primary's error, want none", headers)
		}
	})
	t.Run("streaming, primary sent no headers", func(t *testing.T) {
		client := newTTFTTestClient(t, dropsConnection, anthropic404)
		ctx := ttftContext(0)
		_, bifrostErr := client.ChatCompletionStreamRequest(ctx, ttftChatRequest(fallback))
		if bifrostErr == nil {
			t.Fatal("expected the primary's error once every fallback failed")
		}
		if headers := ctx.Value(schemas.BifrostContextKeyProviderResponseHeaders); headers != nil {
			t.Fatalf("provider headers %v were left on the context with the primary's error, want none", headers)
		}
	})
}

// fallbackAnsweringPlugin answers requests for one provider itself, as a cache hit does, and lets
// every other provider through.
type fallbackAnsweringPlugin struct{ answers schemas.ModelProvider }

func (p *fallbackAnsweringPlugin) GetName() string { return "fallback-answering" }
func (p *fallbackAnsweringPlugin) Cleanup() error  { return nil }
func (p *fallbackAnsweringPlugin) PreRequestHook(*schemas.BifrostContext, *schemas.BifrostRequest) error {
	return nil
}
func (p *fallbackAnsweringPlugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	if provider, _, _ := req.GetRequestFields(); provider != p.answers {
		return req, nil, nil
	}
	return req, &schemas.LLMPluginShortCircuit{Response: &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{ID: "answered-by-plugin", Model: "claude-3-5-haiku-20241022"},
	}}, nil
}
func (p *fallbackAnsweringPlugin) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, bifrostErr, nil
}

// streamFallbackAnsweredByPlugins streams a request whose openai primary fails and whose anthropic
// fallback the given plugins handle, and returns the routing info the stream's headers are written from.
func streamFallbackAnsweredByPlugins(t *testing.T, plugins ...schemas.LLMPlugin) schemas.RoutingInfo {
	t.Helper()
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream down","type":"server_error"}}`))
	}))
	t.Cleanup(primary.Close)

	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 1, primary.URL)
	// Anthropic is configured but never called: the plugin answers for it.
	account.AddProviderWithBaseURL(schemas.Anthropic, 1, 1, "http://127.0.0.1:1")
	account.configs[schemas.OpenAI].NetworkConfig.MaxRetries = 0
	// The primary's key aliases the model, so the failed attempt leaves its alias on the context.
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{{ID: "openai-key", Value: *schemas.NewSecretVar("sk-openai"), Models: schemas.WhiteList{"*"}, Weight: 1,
		Aliases: schemas.KeyAliases{"gpt-4o-mini": {ModelID: "gpt-4o-mini-deployment"}}}})
	account.SetKeysForProvider(schemas.Anthropic, []schemas.Key{{ID: "anthropic-key", Value: *schemas.NewSecretVar("sk-anthropic"), Models: schemas.WhiteList{"*"}, Weight: 1}})
	client, err := Init(context.Background(), schemas.BifrostConfig{
		Account:    account,
		Logger:     NewDefaultLogger(schemas.LogLevelError),
		LLMPlugins: plugins,
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(client.Shutdown)

	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(30*time.Second))
	stream, bifrostErr := client.ChatCompletionStreamRequest(ctx, &schemas.BifrostChatRequest{
		Provider:  schemas.OpenAI,
		Model:     "gpt-4o-mini",
		Input:     []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}}},
		Fallbacks: []schemas.Fallback{{Provider: schemas.Anthropic, Model: "claude-3-5-haiku-20241022"}},
	})
	if bifrostErr != nil {
		t.Fatalf("the plugin should have answered the fallback, got %v", bifrostErr.Error.Message)
	}
	drainChatStream(stream)

	info, ok := ctx.Value(schemas.BifrostContextKeyRoutingInfo).(schemas.RoutingInfo)
	if !ok {
		t.Fatal("no routing info recorded for the stream's headers")
	}
	return info
}

// TestStreamFallbackAnsweredByAPluginReportsTheFallback pins the bug where a streaming fallback a
// plugin answered named the primary in the stream's headers. Stream headers come from the routing
// info a provider worker records for each attempt, and a plugin's answer runs on no worker, so the
// primary's record was marked as the fallback's and the headers named the provider that failed.
func TestStreamFallbackAnsweredByAPluginReportsTheFallback(t *testing.T) {
	info := streamFallbackAnsweredByPlugins(t, &fallbackAnsweringPlugin{answers: schemas.Anthropic})
	if info.Provider != schemas.Anthropic || info.Model != "claude-3-5-haiku-20241022" {
		t.Fatalf("headers name %s/%s, want the anthropic fallback the plugin answered: %+v", info.Provider, info.Model, info)
	}
	if !info.IsFallback || info.PrimaryProvider == nil || *info.PrimaryProvider != schemas.OpenAI {
		t.Fatalf("headers must mark a fallback with openai as its primary: %+v", info)
	}
	if info.ResolvedKeyAlias != nil {
		t.Fatalf("headers carry the failed primary's key alias %+v, but no provider key served the plugin's answer", *info.ResolvedKeyAlias)
	}
}

// fallbackModelRewritingPlugin moves requests for one provider onto another model, as a routing
// plugin's pre-hook does, and lets every other request through untouched.
type fallbackModelRewritingPlugin struct {
	provider schemas.ModelProvider
	model    string
}

func (p *fallbackModelRewritingPlugin) GetName() string { return "fallback-model-rewriting" }
func (p *fallbackModelRewritingPlugin) Cleanup() error  { return nil }
func (p *fallbackModelRewritingPlugin) PreRequestHook(*schemas.BifrostContext, *schemas.BifrostRequest) error {
	return nil
}
func (p *fallbackModelRewritingPlugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	if provider, _, _ := req.GetRequestFields(); provider == p.provider {
		req.SetModel(p.model)
	}
	return req, nil, nil
}
func (p *fallbackModelRewritingPlugin) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, bifrostErr, nil
}

// TestStreamFallbackAnsweredByAPluginReportsTheRewrittenTarget pins the headers to the target the
// pre-hooks left the fallback on. One plugin moves the fallback to another model and a later plugin
// answers it there, so the headers must name the model the answer is for, not the one requested.
func TestStreamFallbackAnsweredByAPluginReportsTheRewrittenTarget(t *testing.T) {
	info := streamFallbackAnsweredByPlugins(t,
		&fallbackModelRewritingPlugin{provider: schemas.Anthropic, model: "claude-3-7-sonnet-20250219"},
		&fallbackAnsweringPlugin{answers: schemas.Anthropic},
	)
	if info.Provider != schemas.Anthropic || info.Model != "claude-3-7-sonnet-20250219" {
		t.Fatalf("headers name %s/%s, want anthropic/claude-3-7-sonnet-20250219, the model the pre-hooks moved the fallback to: %+v", info.Provider, info.Model, info)
	}
	if !info.IsFallback {
		t.Fatalf("headers must mark the answered fallback as one: %+v", info)
	}
}

// primaryRefusingPlugin refuses every request for one provider with a short-circuit error, the way a
// policy plugin does, saying whether the request may still fall back.
type primaryRefusingPlugin struct {
	refuses        schemas.ModelProvider
	allowFallbacks *bool
}

func (p *primaryRefusingPlugin) GetName() string { return "primary-refusing" }
func (p *primaryRefusingPlugin) Cleanup() error  { return nil }
func (p *primaryRefusingPlugin) PreRequestHook(*schemas.BifrostContext, *schemas.BifrostRequest) error {
	return nil
}
func (p *primaryRefusingPlugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	if provider, _, _ := req.GetRequestFields(); provider != p.refuses {
		return req, nil, nil
	}
	return req, &schemas.LLMPluginShortCircuit{Error: &schemas.BifrostError{
		StatusCode:     schemas.Ptr(http.StatusForbidden),
		Error:          &schemas.ErrorField{Message: "refused by policy"},
		AllowFallbacks: p.allowFallbacks,
	}}, nil
}
func (p *primaryRefusingPlugin) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, bifrostErr, nil
}

// anthropicMessageJSON answers like a non-streaming Anthropic Messages call.
func anthropicMessageJSON(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-3-5-haiku-20241022","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
}

// TestPrimaryShortCircuitErrorFallsBackUnlessItForbidsFallbacks pins what a plugin's refusal of the
// primary does to the chain: a refusal that leaves AllowFallbacks unset is a failure like any other
// and the fallback serves, while one that sets it false ends the request with the refusal.
func TestPrimaryShortCircuitErrorFallsBackUnlessItForbidsFallbacks(t *testing.T) {
	for _, tc := range []struct {
		name           string
		allowFallbacks *bool
		wantServed     bool
	}{
		{"a refusal that says nothing about fallbacks", nil, true},
		{"a refusal that forbids fallbacks", schemas.Ptr(false), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var primaryHits, fallbackHits atomic.Int32
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { primaryHits.Add(1) }))
			defer primary.Close()
			fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fallbackHits.Add(1)
				anthropicMessageJSON(w, r)
			}))
			defer fallback.Close()

			account := NewMockAccount()
			account.AddProviderWithBaseURL(schemas.OpenAI, 1, 1, primary.URL)
			account.AddProviderWithBaseURL(schemas.Anthropic, 1, 1, fallback.URL)
			account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{{ID: "openai-key", Value: *schemas.NewSecretVar("sk-openai"), Models: schemas.WhiteList{"*"}, Weight: 1}})
			account.SetKeysForProvider(schemas.Anthropic, []schemas.Key{{ID: "anthropic-key", Value: *schemas.NewSecretVar("sk-anthropic"), Models: schemas.WhiteList{"*"}, Weight: 1}})
			client, err := Init(context.Background(), schemas.BifrostConfig{
				Account:    account,
				Logger:     NewDefaultLogger(schemas.LogLevelError),
				LLMPlugins: []schemas.LLMPlugin{&primaryRefusingPlugin{refuses: schemas.OpenAI, allowFallbacks: tc.allowFallbacks}},
			})
			if err != nil {
				t.Fatalf("Init: %v", err)
			}
			t.Cleanup(client.Shutdown)

			resp, bifrostErr := client.ChatCompletionRequest(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), &schemas.BifrostChatRequest{
				Provider:  schemas.OpenAI,
				Model:     "gpt-4o-mini",
				Input:     []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}}},
				Fallbacks: []schemas.Fallback{{Provider: schemas.Anthropic, Model: "claude-3-5-haiku-20241022"}},
			})
			if primaryHits.Load() != 0 {
				t.Fatalf("the refused primary reached its upstream")
			}
			if tc.wantServed {
				if bifrostErr != nil || resp == nil || resp.ExtraFields.RoutingInfo.Provider != schemas.Anthropic {
					t.Fatalf("want the anthropic fallback to serve, got resp=%v err=%v", resp, bifrostErr)
				}
				if n := fallbackHits.Load(); n != 1 {
					t.Fatalf("fallback hits = %d, want 1", n)
				}
				return
			}
			if bifrostErr == nil || bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusForbidden {
				t.Fatalf("want the refusal back, got resp=%v err=%v", resp, bifrostErr)
			}
			if n := fallbackHits.Load(); n != 0 {
				t.Fatalf("the fallback ran %d time(s) after a refusal that forbids fallbacks", n)
			}
		})
	}
}

// TestStreamErrorAfterContentDoesNotFallBack pins that a stream which already sent content to the
// caller is not restarted on a fallback when it then fails: the caller would get the fallback's
// answer spliced onto half of the primary's. The error reaches the caller on the stream instead.
func TestStreamErrorAfterContentDoesNotFallBack(t *testing.T) {
	primary := httptest.NewServer(sseHandler(
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant","content":"hel"},"finish_reason":null}]}`,
		`{"error":{"message":"The server had an error while processing your request.","type":"server_error"}}`,
	))
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
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{{ID: "openai-key", Value: *schemas.NewSecretVar("sk-openai"), Models: schemas.WhiteList{"*"}, Weight: 1}})
	account.SetKeysForProvider(schemas.Anthropic, []schemas.Key{{ID: "anthropic-key", Value: *schemas.NewSecretVar("sk-anthropic"), Models: schemas.WhiteList{"*"}, Weight: 1}})
	client := newStreamTestClient(t, account)

	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(30*time.Second))
	stream, bifrostErr := client.ChatCompletionStreamRequest(ctx, &schemas.BifrostChatRequest{
		Provider:  schemas.OpenAI,
		Model:     "gpt-4o-mini",
		Input:     []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}}},
		Fallbacks: []schemas.Fallback{{Provider: schemas.Anthropic, Model: "claude-3-5-haiku-20241022"}},
	})
	if bifrostErr != nil {
		t.Fatalf("the stream should have started on the primary, got %v", bifrostErr.Error.Message)
	}
	content, errs := drainChatStream(stream)
	if content != "hel" {
		t.Fatalf("stream content = %q, want the primary's partial %q", content, "hel")
	}
	if len(errs) == 0 {
		t.Fatal("the primary's failure after its content never reached the caller")
	}
	if n := fallbackHits.Load(); n != 0 {
		t.Fatalf("the fallback ran %d time(s) after the primary had already sent content", n)
	}
}

// TestCancelDuringAFallbackStopsTheChain pins that a caller who gives up while a fallback is running
// ends the request there: the next fallback is not tried on behalf of someone who has gone.
func TestCancelDuringAFallbackStopsTheChain(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream down","type":"server_error"}}`))
	}))
	defer primary.Close()
	reached := make(chan struct{}, 1)
	var middleHits atomic.Int32
	slowFallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		middleHits.Add(1)
		reached <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer slowFallback.Close()
	var lastHits atomic.Int32
	lastFallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastHits.Add(1)
		anthropicMessageJSON(w, r)
	}))
	defer lastFallback.Close()

	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 1, primary.URL)
	account.AddProviderWithBaseURL(schemas.Groq, 1, 1, slowFallback.URL)
	account.AddProviderWithBaseURL(schemas.Anthropic, 1, 1, lastFallback.URL)
	account.configs[schemas.OpenAI].NetworkConfig.MaxRetries = 0
	account.configs[schemas.Groq].NetworkConfig.MaxRetries = 0
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{{ID: "openai-key", Value: *schemas.NewSecretVar("sk-openai"), Models: schemas.WhiteList{"*"}, Weight: 1}})
	account.SetKeysForProvider(schemas.Groq, []schemas.Key{{ID: "groq-key", Value: *schemas.NewSecretVar("sk-groq"), Models: schemas.WhiteList{"*"}, Weight: 1}})
	account.SetKeysForProvider(schemas.Anthropic, []schemas.Key{{ID: "anthropic-key", Value: *schemas.NewSecretVar("sk-anthropic"), Models: schemas.WhiteList{"*"}, Weight: 1}})
	client := newStreamTestClient(t, account)

	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-reached:
			cancel()
		case <-time.After(10 * time.Second):
		}
	}()
	ctx := schemas.NewBifrostContext(parent, schemas.NoDeadline)
	_, bifrostErr := client.ChatCompletionRequest(ctx, &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}}},
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Groq, Model: "llama-3.1-8b-instant"},
			{Provider: schemas.Anthropic, Model: "claude-3-5-haiku-20241022"},
		},
	})
	if n := middleHits.Load(); n != 1 {
		t.Fatalf("the first fallback was reached %d time(s), want 1: the cancel never happened mid-chain", n)
	}
	if bifrostErr == nil || bifrostErr.Error == nil || bifrostErr.Error.Type == nil || *bifrostErr.Error.Type != schemas.RequestCancelled {
		t.Fatalf("want the request to end as cancelled, got %+v", bifrostErr)
	}
	for _, entry := range ctx.GetRoutingEngineLogs() {
		if strings.Contains(entry.Message, "Trying fallback 2/2") {
			t.Fatalf("the chain went on to the next fallback after the caller cancelled: %v", ctx.GetRoutingEngineLogs())
		}
	}
	if n := lastHits.Load(); n != 0 {
		t.Fatalf("the last fallback ran %d time(s) after the caller cancelled", n)
	}
}
