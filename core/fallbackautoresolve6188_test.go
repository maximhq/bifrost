package bifrost

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// Regression tests for https://github.com/maximhq/bifrost/issues/6188.
//
// When a request carries a model but no provider and no plugin can auto-resolve
// one, handleRequest/handleStreamRequest used to return the provider
// auto-resolution validation error before the fallback loop ever ran — skipping
// fallbacks the request had explicitly configured. Auto-resolution failure with
// configured fallbacks must instead count as the primary attempt's failure so
// the fallback chain (which sets an explicit provider/model per attempt) gets
// evaluated. A missing required model stays a terminal validation error.

const autoResolveChatBody = `{
  "id": "chatcmpl-6188",
  "object": "chat.completion",
  "created": 1700000000,
  "model": "gpt-4o-mini",
  "choices": [{"index": 0, "message": {"role": "assistant", "content": "ok"}, "finish_reason": "stop"}],
  "usage": {"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7}
}`

func TestFallbackRunsWhenProviderCannotBeAutoResolved(t *testing.T) {
	var fallbackHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, autoResolveChatBody)
	}))
	defer server.Close()

	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 1, server.URL)
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
		{ID: "fallback-key", Value: *schemas.NewSecretVar("sk-fallback"), Models: schemas.WhiteList{"*"}, Weight: 100},
	})
	client := newStreamTestClient(t, account)

	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(30*time.Second))
	resp, bifrostErr := client.ChatCompletionRequest(ctx, &schemas.BifrostChatRequest{
		// No provider, and nothing in this bare client can auto-resolve one
		// for this model — the configured fallback must still be attempted.
		Model: "unresolvable-primary-model",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}},
		},
		Fallbacks: []schemas.Fallback{{Provider: schemas.OpenAI, Model: "gpt-4o-mini"}},
	})
	if bifrostErr != nil {
		t.Fatalf("expected fallback to answer, got error (fallback hits=%d): %s", fallbackHits.Load(), bifrostErr.Error.Message)
	}
	if got := fallbackHits.Load(); got != 1 {
		t.Fatalf("fallback server hits = %d, want 1", got)
	}
	if resp == nil || len(resp.Choices) == 0 {
		t.Fatalf("unexpected empty response: %+v", resp)
	}
}

func TestStreamFallbackRunsWhenProviderCannotBeAutoResolved(t *testing.T) {
	var fallbackHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackHits.Add(1)
		sseHandler(
			`{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"he"}}]}`,
			`{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"llo"}}]}`,
			`{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`,
		)(w, r)
	}))
	defer server.Close()

	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 1, server.URL)
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
		{ID: "fallback-key", Value: *schemas.NewSecretVar("sk-fallback"), Models: schemas.WhiteList{"*"}, Weight: 100},
	})
	client := newStreamTestClient(t, account)

	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(30*time.Second))
	stream, bifrostErr := client.ChatCompletionStreamRequest(ctx, &schemas.BifrostChatRequest{
		Model: "unresolvable-primary-model",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}},
		},
		Fallbacks: []schemas.Fallback{{Provider: schemas.OpenAI, Model: "gpt-4o-mini"}},
	})
	if bifrostErr != nil {
		t.Fatalf("expected fallback stream, got error (fallback hits=%d): %s", fallbackHits.Load(), bifrostErr.Error.Message)
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

// A missing required model must stay terminal even with fallbacks configured.
func TestMissingModelStaysTerminalWithFallbacks(t *testing.T) {
	var fallbackHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, autoResolveChatBody)
	}))
	defer server.Close()

	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 1, server.URL)
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
		{ID: "fallback-key", Value: *schemas.NewSecretVar("sk-fallback"), Models: schemas.WhiteList{"*"}, Weight: 100},
	})
	client := newStreamTestClient(t, account)

	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(30*time.Second))
	_, bifrostErr := client.ChatCompletionRequest(ctx, &schemas.BifrostChatRequest{
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}},
		},
		Fallbacks: []schemas.Fallback{{Provider: schemas.OpenAI, Model: "gpt-4o-mini"}},
	})
	if bifrostErr == nil {
		t.Fatalf("expected terminal validation error for missing model, got success")
	}
	if got := fallbackHits.Load(); got != 0 {
		t.Fatalf("fallback server hits = %d, want 0 (missing model is terminal)", got)
	}
}
