package deepseek_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// captureAnthropicWire stands in for DeepSeek's /anthropic/v1/messages endpoint
// and records the decoded request body the provider actually sent.
func captureAnthropicWire(t *testing.T, captured *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			http.Error(w, "read body", http.StatusInternalServerError)
			return
		}
		if err := json.Unmarshal(body, captured); err != nil {
			t.Errorf("decode body: %v", err)
			http.Error(w, "decode body", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","model":"deepseek-v4-flash","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
}

func anthropicEndpointKey() schemas.Key {
	return schemas.Key{Value: schemas.SecretVar{Val: "test-api-key"}, UseAnthropicEndpoints: new(true)}
}

// TestChatCompletion_AnthropicEndpointEmitsOutputConfigEffort: DeepSeek's
// Anthropic-compatible API takes reasoning depth through output_config.effort
// ("output_config: only effort is supported") and ignores thinking.budget_tokens.
// A caller's reasoning.effort must therefore reach the wire as
// output_config.effort rather than being collapsed into a budget the upstream
// discards.
func TestChatCompletion_AnthropicEndpointEmitsOutputConfigEffort(t *testing.T) {
	t.Parallel()
	var captured map[string]any
	server := captureAnthropicWire(t, &captured)
	defer server.Close()

	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}
	effort := "medium"
	msg := "hello"
	_, bifrostErr := provider.ChatCompletion(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), anthropicEndpointKey(), &schemas.BifrostChatRequest{
		Provider: schemas.DeepSeek,
		Model:    "deepseek-v4-flash",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &msg}}},
		Params:   &schemas.ChatParameters{Reasoning: &schemas.ChatReasoning{Effort: &effort}},
	})
	if bifrostErr != nil {
		t.Fatalf("ChatCompletion: %v", bifrostErr.Error.Message)
	}
	outputConfig, ok := captured["output_config"].(map[string]any)
	if !ok || outputConfig["effort"] != effort {
		t.Fatalf("output_config.effort = %#v, want %q; wire body = %#v", captured["output_config"], effort, captured)
	}
}

// TestResponses_AnthropicEndpointEmitsOutputConfigEffort covers the Responses
// converter, which has its own reasoning ladder.
func TestResponses_AnthropicEndpointEmitsOutputConfigEffort(t *testing.T) {
	t.Parallel()
	var captured map[string]any
	server := captureAnthropicWire(t, &captured)
	defer server.Close()

	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}
	effort := "high"
	msg := "hello"
	_, bifrostErr := provider.Responses(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), anthropicEndpointKey(), &schemas.BifrostResponsesRequest{
		Provider: schemas.DeepSeek,
		Model:    "deepseek-v4-pro",
		Input:    []schemas.ResponsesMessage{{Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser), Content: &schemas.ResponsesMessageContent{ContentStr: &msg}}},
		Params:   &schemas.ResponsesParameters{Reasoning: &schemas.ResponsesParametersReasoning{Effort: &effort}},
	})
	if bifrostErr != nil {
		t.Fatalf("Responses: %v", bifrostErr.Error.Message)
	}
	outputConfig, ok := captured["output_config"].(map[string]any)
	if !ok || outputConfig["effort"] != effort {
		t.Fatalf("output_config.effort = %#v, want %q; wire body = %#v", captured["output_config"], effort, captured)
	}
}
