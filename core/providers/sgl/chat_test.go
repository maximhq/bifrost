package sgl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// testLogger is a minimal schemas.Logger implementation for unit tests.
type testLogger struct{}

func (testLogger) Debug(string, ...any)                   {}
func (testLogger) Info(string, ...any)                    {}
func (testLogger) Warn(string, ...any)                    {}
func (testLogger) Error(string, ...any)                   {}
func (testLogger) Fatal(string, ...any)                   {}
func (testLogger) SetLevel(schemas.LogLevel)              {}
func (testLogger) SetOutputType(schemas.LoggerOutputType) {}
func (testLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

// newTestSGLProvider creates an SGLProvider suitable for unit tests.
// It uses a short timeout and no base URL (per-key URLs are expected).
func newTestSGLProvider() *SGLProvider {
	client := &fasthttp.Client{
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}
	return &SGLProvider{
		client:          client,
		streamingClient: client,
		networkConfig:   schemas.NetworkConfig{},
		logger:          testLogger{},
	}
}

// TestChatCompletion_ExtraParamsForwardedAutomatically verifies that provider-specific
// extra params (e.g. chat_template_kwargs) are forwarded to SGLang without requiring
// the caller to set BifrostContextKeyPassthroughExtraParams on the context.
func TestChatCompletion_ExtraParamsForwardedAutomatically(t *testing.T) {
	t.Parallel()

	var capturedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read error", http.StatusInternalServerError)
			return
		}
		if err := json.Unmarshal(body, &capturedBody); err != nil {
			http.Error(w, "json error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"id": "chatcmpl-test",
			"object": "chat.completion",
			"created": 1234567890,
			"model": "qwen",
			"choices": [{
				"index": 0,
				"message": {"role": "assistant", "content": "Hello!"},
				"finish_reason": "stop"
			}],
			"usage": {"prompt_tokens": 5, "completion_tokens": 3, "total_tokens": 8}
		}`)
	}))
	defer server.Close()

	provider := newTestSGLProvider()
	key := schemas.Key{
		ID:    "test-key",
		Value: schemas.SecretVar{Val: "test-api-key"},
		SGLKeyConfig: &schemas.SGLKeyConfig{
			URL: schemas.SecretVar{Val: server.URL},
		},
	}

	// Intentionally do NOT set BifrostContextKeyPassthroughExtraParams — the provider
	// should set it automatically.
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	hello := "Hello"
	req := &schemas.BifrostChatRequest{
		Provider: schemas.SGL,
		Model:    "qwen",
		Input: []schemas.ChatMessage{
			{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: &hello},
			},
		},
		Params: &schemas.ChatParameters{
			ExtraParams: map[string]interface{}{
				"chat_template_kwargs": map[string]interface{}{
					"enable_thinking": false,
				},
			},
		},
	}

	_, bifrostErr := provider.ChatCompletion(ctx, key, req)
	if bifrostErr != nil {
		t.Fatalf("ChatCompletion returned error: %v", bifrostErr.Error.Message)
	}

	if capturedBody == nil {
		t.Fatal("mock server did not receive a request body")
	}

	rawKwargs, ok := capturedBody["chat_template_kwargs"]
	if !ok {
		t.Fatalf("chat_template_kwargs missing from outgoing request body; got keys: %v", keys(capturedBody))
	}

	kwargsMap, ok := rawKwargs.(map[string]interface{})
	if !ok {
		t.Fatalf("expected chat_template_kwargs to be an object, got %T", rawKwargs)
	}
	if kwargsMap["enable_thinking"] != false {
		t.Fatalf("expected enable_thinking=false, got %v", kwargsMap["enable_thinking"])
	}
}

func keys(m map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// noopPostHookRunner is a PostHookRunner that passes through results unchanged.
func noopPostHookRunner(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return result, err
}

// drainStream consumes chunks from streamChan in a goroutine that exits when
// the channel closes or when the test completes (via t.Cleanup), whichever
// comes first. This prevents a goroutine leak if the streaming pipeline ever
// fails to close the channel — e.g. on a test timeout.
func drainStream[T any](t *testing.T, streamChan <-chan T) {
	t.Helper()
	if streamChan == nil {
		return
	}
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	go func() {
		for {
			select {
			case _, ok := <-streamChan:
				if !ok {
					return
				}
			case <-done:
				return
			}
		}
	}()
}

// streamCaptureServer returns an httptest.Server that captures the inbound
// Authorization header into the provided channel and immediately closes the
// SSE stream with a [DONE] sentinel. The streaming pipeline only needs to
// have set the request headers by this point — the test asserts on what was
// captured and does not need to fully drain the response.
func streamCaptureServer(authCh chan<- string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Capture Authorization header (non-blocking; tests read it after the call).
		select {
		case authCh <- r.Header.Get("Authorization"):
		default:
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Send a single [DONE] marker so the stream terminates promptly.
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
}

// TestChatCompletionStream_SetsAuthorizationHeader is a regression test for a
// bug where ChatCompletionStream passed nil for the authHeader parameter to
// the OpenAI streaming helper, causing SGLang servers (which always require
// the api key configured via --api-key) to return 401 on streaming requests
// while the non-streaming path worked. The fix mirrors the vLLM pattern of
// building the auth header from key.Value.
func TestChatCompletionStream_SetsAuthorizationHeader(t *testing.T) {
	t.Parallel()

	authCh := make(chan string, 1)
	server := streamCaptureServer(authCh)
	defer server.Close()

	provider := newTestSGLProvider()
	const apiKey = "sgl-streaming-secret"
	key := schemas.Key{
		ID:    "test-key",
		Value: schemas.SecretVar{Val: apiKey},
		SGLKeyConfig: &schemas.SGLKeyConfig{
			URL: schemas.SecretVar{Val: server.URL},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	hello := "Hello"
	req := &schemas.BifrostChatRequest{
		Provider: schemas.SGL,
		Model:    "qwen",
		Input: []schemas.ChatMessage{
			{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: &hello},
			},
		},
	}

	streamChan, bifrostErr := provider.ChatCompletionStream(ctx, noopPostHookRunner, nil, key, req)
	if bifrostErr != nil {
		t.Fatalf("ChatCompletionStream returned error: %v", bifrostErr.Error.Message)
	}

	// Drain any chunks so the streaming goroutine completes; the drain itself
	// is cancelled via t.Cleanup so it cannot leak if the channel never closes.
	drainStream(t, streamChan)

	select {
	case got := <-authCh:
		want := "Bearer " + apiKey
		if got != want {
			t.Fatalf("expected Authorization header %q on streaming request, got %q", want, got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("mock server did not receive a streaming request within timeout")
	}
}

// TestTextCompletionStream_SetsAuthorizationHeader is the equivalent regression
// test for TextCompletionStream, which had the same nil authHeader bug.
func TestTextCompletionStream_SetsAuthorizationHeader(t *testing.T) {
	t.Parallel()

	authCh := make(chan string, 1)
	server := streamCaptureServer(authCh)
	defer server.Close()

	provider := newTestSGLProvider()
	const apiKey = "sgl-streaming-secret"
	key := schemas.Key{
		ID:    "test-key",
		Value: schemas.SecretVar{Val: apiKey},
		SGLKeyConfig: &schemas.SGLKeyConfig{
			URL: schemas.SecretVar{Val: server.URL},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	prompt := "Hello"
	req := &schemas.BifrostTextCompletionRequest{
		Provider: schemas.SGL,
		Model:    "qwen",
		Input:    &schemas.TextCompletionInput{PromptStr: &prompt},
	}

	streamChan, bifrostErr := provider.TextCompletionStream(ctx, noopPostHookRunner, nil, key, req)
	if bifrostErr != nil {
		t.Fatalf("TextCompletionStream returned error: %v", bifrostErr.Error.Message)
	}

	drainStream(t, streamChan)

	select {
	case got := <-authCh:
		want := "Bearer " + apiKey
		if got != want {
			t.Fatalf("expected Authorization header %q on streaming request, got %q", want, got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("mock server did not receive a streaming request within timeout")
	}
}
