package vllm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// TestChatCompletion_ExtraParamsForwardedAutomatically verifies that provider-specific
// extra params (e.g. chat_template_kwargs) are forwarded to vLLM without requiring
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
			"model": "gemma",
			"choices": [{
				"index": 0,
				"message": {"role": "assistant", "content": "Hello!"},
				"finish_reason": "stop"
			}],
			"usage": {"prompt_tokens": 5, "completion_tokens": 3, "total_tokens": 8}
		}`)
	}))
	defer server.Close()

	provider := newTestVLLMProvider()
	key := schemas.Key{
		ID:    "test-key",
		Value: schemas.SecretVar{Val: "test-api-key"},
		VLLMKeyConfig: &schemas.VLLMKeyConfig{
			URL: schemas.SecretVar{Val: server.URL},
		},
	}

	// Intentionally do NOT set BifrostContextKeyPassthroughExtraParams — the provider
	// should set it automatically.
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	hello := "Hello"
	req := &schemas.BifrostChatRequest{
		Provider: schemas.VLLM,
		Model:    "gemma",
		Input: []schemas.ChatMessage{
			{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: &hello},
			},
		},
		Params: &schemas.ChatParameters{
			ExtraParams: map[string]interface{}{
				"chat_template_kwargs": map[string]interface{}{
					"enable_thinking": true,
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
	if kwargsMap["enable_thinking"] != true {
		t.Fatalf("expected enable_thinking=true, got %v", kwargsMap["enable_thinking"])
	}
}

func keys(m map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestChatCompletion_XHighReasoningEffortPreserved(t *testing.T) {
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
		if _, err := fmt.Fprint(w, `{
			"id": "chatcmpl-test",
			"object": "chat.completion",
			"created": 1234567890,
			"model": "qwen/qwen3.8-27b",
			"choices": [{
				"index": 0,
				"message": {"role": "assistant", "content": "Hello!"},
				"finish_reason": "stop"
			}],
			"usage": {"prompt_tokens": 5, "completion_tokens": 3, "total_tokens": 8}
		}`); err != nil {
			t.Errorf("write mock response: %v", err)
		}
	}))
	defer server.Close()

	provider := newTestVLLMProvider()
	key := schemas.Key{
		ID:    "test-key",
		Value: schemas.SecretVar{Val: "test-api-key"},
		VLLMKeyConfig: &schemas.VLLMKeyConfig{
			URL: schemas.SecretVar{Val: server.URL},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	hello := "Hello"
	xhigh := "xhigh"
	req := &schemas.BifrostChatRequest{
		Provider: schemas.VLLM,
		Model:    "qwen/qwen3.8-27b",
		Input: []schemas.ChatMessage{
			{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: &hello},
			},
		},
		Params: &schemas.ChatParameters{
			Reasoning: &schemas.ChatReasoning{
				Effort: &xhigh,
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

	rawEffort, ok := capturedBody["reasoning_effort"]
	if !ok {
		t.Fatalf("reasoning_effort missing from outgoing request body; got keys: %v", keys(capturedBody))
	}

	effortStr, ok := rawEffort.(string)
	if !ok {
		t.Fatalf("expected reasoning_effort to be a string, got %T", rawEffort)
	}

	if effortStr != "xhigh" {
		t.Fatalf("expected reasoning_effort=xhigh, got %q", effortStr)
	}
}

func TestResponses_XHighReasoningEffortPreserved(t *testing.T) {
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
		if _, err := fmt.Fprint(w, `{
			"id": "resp-test",
			"object": "response",
			"created_at": 1234567890,
			"model": "qwen/qwen3.8-27b",
			"status": "completed",
			"output": [{
				"type": "message",
				"id": "msg_01",
				"role": "assistant",
				"content": [{"type": "text", "text": "Hello!"}]
			}],
			"usage": {"input_tokens": 5, "output_tokens": 3, "total_tokens": 8}
		}`); err != nil {
			t.Errorf("write mock response: %v", err)
		}
	}))
	defer server.Close()

	provider := newTestVLLMProvider()
	key := schemas.Key{
		ID:    "test-key",
		Value: schemas.SecretVar{Val: "test-api-key"},
		VLLMKeyConfig: &schemas.VLLMKeyConfig{
			URL: schemas.SecretVar{Val: server.URL},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	hello := "Hello"
	xhigh := "xhigh"
	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.VLLM,
		Model:    "qwen/qwen3.8-27b",
		Input: []schemas.ResponsesMessage{
			{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: &hello},
			},
		},
		Params: &schemas.ResponsesParameters{
			Reasoning: &schemas.ResponsesParametersReasoning{
				Effort: &xhigh,
			},
		},
	}

	_, bifrostErr := provider.Responses(ctx, key, req)
	if bifrostErr != nil {
		t.Fatalf("Responses returned error: %v", bifrostErr.Error.Message)
	}

	if capturedBody == nil {
		t.Fatal("mock server did not receive a request body")
	}

	// Responses API format uses reasoning: { effort: "xhigh" }
	rawReasoning, ok := capturedBody["reasoning"]
	if !ok {
		t.Fatalf("reasoning missing from outgoing request body; got keys: %v", keys(capturedBody))
	}

	reasoningMap, ok := rawReasoning.(map[string]interface{})
	if !ok {
		t.Fatalf("expected reasoning to be an object, got %T", rawReasoning)
	}

	rawEffort, ok := reasoningMap["effort"]
	if !ok {
		t.Fatalf("effort missing from reasoning object; got keys: %v", keys(reasoningMap))
	}

	effortStr, ok := rawEffort.(string)
	if !ok {
		t.Fatalf("expected effort to be a string, got %T", rawEffort)
	}

	if effortStr != "xhigh" {
		t.Fatalf("expected reasoning.effort=xhigh, got %q", effortStr)
	}
}

// TestResponses_AnthropicIngressExtraParamsReachVLLM pins that an unknown field sent on
// /anthropic/v1/messages without x-bf-passthrough-extra-params reaches vLLM on both the
// OpenAI and Anthropic-compatible endpoints, and that the provider's passthrough flag does
// not outlive the attempt (a fallback to a strict provider must not inherit it).
func TestResponses_AnthropicIngressExtraParamsReachVLLM(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name         string
		useAnthropic bool
		wantPath     string
		response     string
	}{
		{
			name:     "openai endpoint",
			wantPath: "/v1/responses",
			response: `{"id":"resp-test","object":"response","created_at":1234567890,"model":"gemma","status":"completed",` +
				`"output":[{"type":"message","id":"msg_01","role":"assistant","content":[{"type":"output_text","text":"hi"}]}],` +
				`"usage":{"input_tokens":5,"output_tokens":3,"total_tokens":8}}`,
		},
		{
			name:         "anthropic endpoint",
			useAnthropic: true,
			wantPath:     "/v1/messages",
			response: `{"id":"msg_01","type":"message","role":"assistant","model":"gemma",` +
				`"content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":3}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var capturedPath string
			var capturedBody map[string]interface{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				capturedPath = r.URL.Path
				body, _ := io.ReadAll(r.Body)
				if err := json.Unmarshal(body, &capturedBody); err != nil {
					http.Error(w, "json error", http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, tc.response)
			}))
			defer server.Close()

			key := testVLLMKey(server.URL)
			if tc.useAnthropic {
				key.UseAnthropicEndpoints = schemas.Ptr(true)
			}

			var ingress anthropic.AnthropicMessageRequest
			if err := sonic.Unmarshal([]byte(`{"model":"vllm/gemma","max_tokens":16,`+
				`"messages":[{"role":"user","content":"hi"}],`+
				`"chat_template_kwargs":{"enable_thinking":false}}`), &ingress); err != nil {
				t.Fatalf("unmarshal ingress request: %v", err)
			}
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			req := ingress.ToBifrostResponsesRequest(ctx)

			if _, bifrostErr := newTestVLLMProvider().Responses(ctx, key, req); bifrostErr != nil {
				t.Fatalf("Responses returned error: %v", bifrostErr.Error.Message)
			}

			if capturedPath != tc.wantPath {
				t.Fatalf("expected request on %s, got %s", tc.wantPath, capturedPath)
			}
			kwargs, ok := capturedBody["chat_template_kwargs"].(map[string]interface{})
			if !ok {
				t.Fatalf("chat_template_kwargs missing from outgoing body; got keys: %v", keys(capturedBody))
			}
			if kwargs["enable_thinking"] != false {
				t.Fatalf("expected enable_thinking=false, got %v", kwargs["enable_thinking"])
			}
			if v := ctx.Value(schemas.BifrostContextKeyPassthroughExtraParams); v != nil {
				t.Fatalf("passthrough flag leaked past the vLLM attempt: %v", v)
			}
		})
	}
}

// TestResponses_KeepsCallerPassthroughFlag pins that the provider restores, not clears, a
// flag the caller set via x-bf-passthrough-extra-params, so it survives into fallbacks.
func TestResponses_KeepsCallerPassthroughFlag(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"resp-test","object":"response","created_at":1,"model":"gemma","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	}))
	defer server.Close()

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
	hello := "hi"
	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.VLLM,
		Model:    "gemma",
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: &hello},
		}},
	}
	if _, bifrostErr := newTestVLLMProvider().Responses(ctx, testVLLMKey(server.URL), req); bifrostErr != nil {
		t.Fatalf("Responses returned error: %v", bifrostErr.Error.Message)
	}
	if v := ctx.Value(schemas.BifrostContextKeyPassthroughExtraParams); v != true {
		t.Fatalf("expected caller's passthrough flag to survive, got %v", v)
	}
}
