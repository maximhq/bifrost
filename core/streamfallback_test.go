package bifrost

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
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
