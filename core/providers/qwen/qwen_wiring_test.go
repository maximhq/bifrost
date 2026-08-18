package qwen

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// These tests point the provider at a mock upstream to verify request wiring
// (URL path composition, bearer auth) and response decoding without a live key.

func newWiringTestProvider(t *testing.T, baseURL string) *QwenProvider {
	t.Helper()
	provider, err := NewQwenProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: baseURL},
	}, nil)
	if err != nil {
		t.Fatalf("provider init: %v", err)
	}
	return provider
}

func wiringTestKey() schemas.Key {
	return schemas.Key{Value: *schemas.NewSecretVar("test-qwen-key")}
}

func TestQwenChatCompletionWiring(t *testing.T) {
	var mu sync.Mutex
	var gotPath, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"qwen3.7-flash",` +
			`"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	provider := newWiringTestProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	resp, bifrostErr := provider.ChatCompletion(ctx, wiringTestKey(), &schemas.BifrostChatRequest{
		Provider: schemas.Qwen,
		Model:    "qwen3.7-flash",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")},
		}},
	})
	if bifrostErr != nil {
		t.Fatalf("chat completion failed: %v", bifrostErr)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/chat/completions" {
		t.Errorf("request path = %q, want /chat/completions (base URL carries the version segment)", gotPath)
	}
	if gotAuth != "Bearer test-qwen-key" {
		t.Errorf("Authorization = %q, want Bearer test-qwen-key", gotAuth)
	}
	if len(resp.Choices) == 0 || resp.Choices[0].ChatNonStreamResponseChoice == nil {
		t.Fatalf("response not decoded: %+v", resp)
	}
}

func TestQwenDefaultBaseURL(t *testing.T) {
	provider := newWiringTestProvider(t, "")
	if provider.networkConfig.BaseURL != "https://dashscope-intl.aliyuncs.com/compatible-mode/v1" {
		t.Errorf("default base URL = %q", provider.networkConfig.BaseURL)
	}
}
