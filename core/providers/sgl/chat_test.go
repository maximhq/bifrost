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

// newTestSGLProvider creates an SGLProvider suitable for unit tests.
// It uses a short timeout and no base URL (per-key URLs are expected).
func newTestSGLProvider() *SGLProvider {
	return &SGLProvider{
		client: &fasthttp.Client{
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 5 * time.Second,
		},
		networkConfig: schemas.NetworkConfig{},
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
		Value: schemas.EnvVar{Val: "test-api-key"},
		SGLKeyConfig: &schemas.SGLKeyConfig{
			URL: schemas.EnvVar{Val: server.URL},
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
