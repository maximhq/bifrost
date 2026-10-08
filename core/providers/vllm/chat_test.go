package vllm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func TestChatCompletion_AssistantReasoningReplay(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		mode := "unary"
		if streaming {
			mode = "stream"
		}
		for _, tc := range []struct {
			name      string
			fields    string
			bypass    string
			wantField string
			wantValue string
		}{
			{name: "legacy", fields: `,"reasoning_content":"Deep Thoughts here..."`, wantField: "reasoning", wantValue: "Deep Thoughts here..."},
			{name: "canonical", fields: `,"reasoning":"Deep Thoughts here..."`, wantField: "reasoning", wantValue: "Deep Thoughts here..."},
			{name: "canonical_precedence", fields: `,"reasoning":"canonical","reasoning_content":"legacy"`, wantField: "reasoning", wantValue: "canonical"},
			{name: "no_reasoning"},
			{name: "raw_passthrough", fields: `,"reasoning_content":"raw reasoning"`, bypass: "raw", wantField: "reasoning_content", wantValue: "raw reasoning"},
			{name: "large_passthrough", fields: `,"reasoning_content":"large reasoning"`, bypass: "large", wantField: "reasoning_content", wantValue: "large reasoning"},
			{name: "messages_override", fields: `,"reasoning_content":"typed reasoning"`, bypass: "messages", wantField: "reasoning_content", wantValue: "override reasoning"},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				// Decode the same neutral ChatMessage type used by native HTTP ingress.
				messages := fmt.Sprintf(`[{"role":"user","content":"First prompt"},{"role":"assistant","content":"Final response"%s},{"role":"user","content":"Follow up question"}]`, tc.fields)
				req := &schemas.BifrostChatRequest{Provider: schemas.VLLM, Model: "fake-model"}
				if err := json.Unmarshal([]byte(messages), &req.Input); err != nil {
					t.Fatalf("decode neutral messages: %v", err)
				}
				before, err := json.Marshal(req.Input)
				if err != nil {
					t.Fatalf("snapshot caller messages: %v", err)
				}
				ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				raw := []byte(fmt.Sprintf(`{"model":"fake-model","messages":%s,"stream":%t}`, messages, streaming))
				switch tc.bypass {
				case "raw":
					req.RawRequestBody = raw
					ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
				case "large":
					ctx.SetValue(schemas.BifrostContextKeyLargePayloadMode, true)
					ctx.SetValue(schemas.BifrostContextKeyLargePayloadReader, strings.NewReader(string(raw)))
					ctx.SetValue(schemas.BifrostContextKeyLargePayloadContentLength, len(raw))
				case "messages":
					var override []any
					if err := json.Unmarshal([]byte(strings.ReplaceAll(messages, "typed reasoning", "override reasoning")), &override); err != nil {
						t.Fatalf("decode message override: %v", err)
					}
					req.Params = &schemas.ChatParameters{ExtraParams: map[string]any{"messages": override}}
				}
				captured := make(chan []byte, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil || r.URL.Path != "/v1/chat/completions" {
						http.Error(w, "invalid request", http.StatusBadRequest)
						return
					}
					captured <- body
					if streaming {
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprint(w, "data: {\"id\":\"chatcmpl-test\",\"object\":\"chat.completion.chunk\",\"model\":\"fake-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello!\"},\"finish_reason\":null}]}\n\n")
						fmt.Fprint(w, "data: {\"id\":\"chatcmpl-test\",\"object\":\"chat.completion.chunk\",\"model\":\"fake-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					} else {
						w.Header().Set("Content-Type", "application/json")
						fmt.Fprint(w, `{"id":"chatcmpl-test","object":"chat.completion","model":"fake-model","choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}]}`)
					}
				}))
				defer server.Close()
				provider := newTestVLLMResponsesStreamProvider(t, false, false)
				if streaming {
					stream, bifrostErr := provider.ChatCompletionStream(ctx, noopPostHookRunner, nil, testVLLMKey(server.URL), req)
					if bifrostErr != nil {
						t.Fatalf("ChatCompletionStream: %v", bifrostErr)
					}
					chunks := drainStreamWithTimeout(t, stream, 5*time.Second)
					if tc.bypass == "large" {
						reader, ok := ctx.Value(schemas.BifrostContextKeyLargeResponseReader).(io.ReadCloser)
						if !ok {
							t.Fatal("missing large response reader")
						}
						defer reader.Close()
						response, err := io.ReadAll(reader)
						if err != nil || !strings.Contains(string(response), "data: [DONE]") {
							t.Fatalf("invalid passthrough stream: %s, err=%v", response, err)
						}
					} else if len(chunks) == 0 {
						t.Fatal("empty stream")
					}
					for _, chunk := range chunks {
						if chunk.BifrostError != nil {
							t.Fatalf("stream error: %v", chunk.BifrostError)
						}
					}
				} else if _, bifrostErr := provider.ChatCompletion(ctx, testVLLMKey(server.URL), req); bifrostErr != nil {
					t.Fatalf("ChatCompletion: %v", bifrostErr)
				}
				var body []byte
				select {
				case body = <-captured:
				case <-time.After(time.Second):
					t.Fatal("upstream did not receive a request")
				}
				var wire struct {
					Model    string           `json:"model"`
					Stream   bool             `json:"stream"`
					Messages []map[string]any `json:"messages"`
				}
				if err := json.Unmarshal(body, &wire); err != nil {
					t.Fatalf("decode wire body: %v", err)
				}
				if wire.Model != "fake-model" || wire.Stream != streaming || len(wire.Messages) != 3 {
					t.Fatalf("unexpected wire request: %s", body)
				}
				assistant := wire.Messages[1]
				if assistant["role"] != "assistant" || assistant["content"] != "Final response" {
					t.Fatalf("assistant content changed: %#v", assistant)
				}
				for _, field := range []string{"reasoning", "reasoning_content"} {
					value, present := assistant[field]
					if field == tc.wantField {
						if value != tc.wantValue {
							t.Errorf("expected %s=%q, got %#v; body=%s", field, tc.wantValue, value, body)
						}
					} else if present {
						t.Errorf("unexpected %s in assistant message: %s", field, body)
					}
				}
				if tc.bypass == "raw" || tc.bypass == "large" {
					if string(body) != string(raw) {
						t.Errorf("passthrough body changed: want=%s got=%s", raw, body)
					}
				}
				after, err := json.Marshal(req.Input)
				if err != nil || string(before) != string(after) {
					t.Errorf("caller messages mutated: before=%s after=%s err=%v", before, after, err)
				}
			})
		}
	}
}
