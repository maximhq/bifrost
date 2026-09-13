package tencent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"
	tencent "github.com/maximhq/bifrost/core/providers/tencent"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

type testLogger struct{}

func (l testLogger) Debug(string, ...any)                   {}
func (l testLogger) Info(string, ...any)                    {}
func (l testLogger) Warn(string, ...any)                    {}
func (l testLogger) Error(string, ...any)                   {}
func (l testLogger) Fatal(string, ...any)                   {}
func (l testLogger) SetLevel(schemas.LogLevel)              {}
func (l testLogger) SetOutputType(schemas.LoggerOutputType) {}
func (l testLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

func newTestTencentProvider(baseURL string) (*tencent.TencentProvider, error) {
	return tencent.NewTencentProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        baseURL,
			DefaultRequestTimeoutInSeconds: 5,
			StreamIdleTimeoutInSeconds:     5,
			MaxConnsPerHost:                1,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 1,
			BufferSize:  1,
		},
	}, testLogger{})
}

func TestTencent(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("TENCENT_API_KEY")) == "" {
		t.Skip("Skipping Tencent TokenHub tests because TENCENT_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:  schemas.Tencent,
		ChatModel: "deepseek-v4-pro",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Tencent, Model: "deepseek-v4-pro"},
			{Provider: schemas.Tencent, Model: "deepseek-v4-flash"},
		},
		EmbeddingModel: "", // Tencent TokenHub doesn't support embedding
		ReasoningModel: "deepseek-v4-pro",
		Scenarios: llmtests.TestScenarios{
			TextCompletion:        false, // Not supported
			TextCompletionStream:  false, // Not supported
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              false,
			ImageBase64:           false,
			MultipleImages:        false,
			CompleteEnd2End:       true,
			Embedding:             false,
			ListModels:            true,
			Reasoning:             true,
		},
	}

	t.Run("TencentTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

func newOpenAIChatResponse() string {
	return `{"id":"chatcmpl_1","object":"chat.completion","model":"deepseek-v4-pro","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
}

func newAnthropicResponse() string {
	return `{"id":"msg_1","type":"message","role":"assistant","model":"deepseek-v4-pro","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
}

// TestChatCompletion_UsesOpenAIEndpoint verifies the default routing hits the
// OpenAI-compatible endpoint with Bearer authentication.
func TestChatCompletion_UsesOpenAIEndpoint(t *testing.T) {
	t.Parallel()

	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q, want /v1/chat/completions", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusBadRequest)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-api-key" {
			t.Errorf("Authorization = %q, want Bearer test-api-key", got)
			http.Error(w, "unexpected auth", http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			http.Error(w, "read body", http.StatusInternalServerError)
			return
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Errorf("decode body: %v", err)
			http.Error(w, "decode body", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, newOpenAIChatResponse())
	}))
	defer server.Close()

	provider, err := newTestTencentProvider(server.URL)
	if err != nil {
		t.Fatalf("NewTencentProvider: %v", err)
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	msg := "hello"
	resp, bifrostErr := provider.ChatCompletion(ctx, schemas.Key{Value: schemas.SecretVar{Val: "test-api-key"}}, &schemas.BifrostChatRequest{
		Provider: schemas.Tencent,
		Model:    "deepseek-v4-pro",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: &msg},
		}},
	})
	if bifrostErr != nil {
		t.Fatalf("ChatCompletion: %v", bifrostErr.Error.Message)
	}
	if resp == nil || len(resp.Choices) == 0 {
		t.Fatalf("expected chat response, got %#v", resp)
	}
	if _, ok := captured["messages"]; !ok {
		t.Fatalf("outbound body missing messages: %#v", captured)
	}
}

// TestChatCompletion_UsesAnthropicEndpoint verifies that a key opting into
// Anthropic endpoints routes to /v1/messages and authenticates with x-api-key.
func TestChatCompletion_UsesAnthropicEndpoint(t *testing.T) {
	t.Parallel()

	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path = %q, want /v1/messages", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusBadRequest)
			return
		}
		if got := r.Header.Get("x-api-key"); got != "test-api-key" {
			t.Errorf("x-api-key = %q, want test-api-key", got)
			http.Error(w, "unexpected api key", http.StatusBadRequest)
			return
		}
		if got := r.Header.Get("anthropic-version"); got != "2023-06-01" {
			t.Errorf("anthropic-version = %q, want 2023-06-01", got)
			http.Error(w, "missing anthropic-version", http.StatusBadRequest)
			return
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization = %q, want empty for the Anthropic endpoint", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			http.Error(w, "read body", http.StatusInternalServerError)
			return
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Errorf("decode body: %v", err)
			http.Error(w, "decode body", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, newAnthropicResponse())
	}))
	defer server.Close()

	provider, err := newTestTencentProvider(server.URL)
	if err != nil {
		t.Fatalf("NewTencentProvider: %v", err)
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	msg := "hello"
	resp, bifrostErr := provider.ChatCompletion(ctx, schemas.Key{Value: schemas.SecretVar{Val: "test-api-key"}, UseAnthropicEndpoints: new(true)}, &schemas.BifrostChatRequest{
		Provider: schemas.Tencent,
		Model:    "deepseek-v4-pro",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: &msg},
		}},
	})
	if bifrostErr != nil {
		t.Fatalf("ChatCompletion: %v", bifrostErr.Error.Message)
	}
	if resp == nil || len(resp.Choices) == 0 {
		t.Fatalf("expected chat response, got %#v", resp)
	}
	if _, ok := captured["messages"]; !ok {
		t.Fatalf("outbound body missing messages: %#v", captured)
	}
	// Anthropic Messages requests send a top-level max_tokens and a string
	// system field; the OpenAI shape uses max_completion_tokens instead.
	if _, ok := captured["max_tokens"]; !ok {
		t.Fatalf("outbound Anthropic body missing max_tokens: %#v", captured)
	}
}

// TestUnsupportedOperations verifies operations Tencent TokenHub does not offer
// return a not-implemented error rather than attempting a network call.
func TestUnsupportedOperations(t *testing.T) {
	t.Parallel()

	provider, err := newTestTencentProvider("http://127.0.0.1:1")
	if err != nil {
		t.Fatalf("NewTencentProvider: %v", err)
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: schemas.SecretVar{Val: "test-api-key"}}

	if _, bifrostErr := provider.TextCompletion(ctx, key, &schemas.BifrostTextCompletionRequest{}); bifrostErr == nil {
		t.Fatal("TextCompletion: got nil error, want unsupported-operation error")
	}
	if _, bifrostErr := provider.Embedding(ctx, key, &schemas.BifrostEmbeddingRequest{}); bifrostErr == nil {
		t.Fatal("Embedding: got nil error, want unsupported-operation error")
	}
	if _, bifrostErr := provider.Speech(ctx, key, &schemas.BifrostSpeechRequest{}); bifrostErr == nil {
		t.Fatal("Speech: got nil error, want unsupported-operation error")
	}
}

// TestCustomProviderConfig_OperationGating verifies a non-nil AllowedRequests
// restriction is enforced for every implemented operation.
func TestCustomProviderConfig_OperationGating(t *testing.T) {
	t.Parallel()

	provider, err := tencent.NewTencentProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        "http://127.0.0.1:1",
			DefaultRequestTimeoutInSeconds: 5,
		},
		CustomProviderConfig: &schemas.CustomProviderConfig{
			AllowedRequests: &schemas.AllowedRequests{ListModels: true},
		},
	}, testLogger{})
	if err != nil {
		t.Fatalf("NewTencentProvider: %v", err)
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: schemas.SecretVar{Val: "test-api-key"}}
	msg := "hello"

	if _, bifrostErr := provider.ChatCompletion(ctx, key, &schemas.BifrostChatRequest{
		Provider: schemas.Tencent,
		Model:    "deepseek-v4-pro",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &msg}}},
	}); bifrostErr == nil {
		t.Fatal("ChatCompletion: got nil error, want an operation-not-allowed error")
	}
	if _, bifrostErr := provider.Responses(ctx, key, &schemas.BifrostResponsesRequest{Provider: schemas.Tencent, Model: "deepseek-v4-pro"}); bifrostErr == nil {
		t.Fatal("Responses: got nil error, want an operation-not-allowed error")
	}
}
