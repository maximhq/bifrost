package minimax

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// Exercise the provider's real serializers and HTTP transport, including the
// Responses compatibility adapter. Explicit model selections must never change.
func TestRolloutModelRequests(t *testing.T) {
	for _, model := range []string{"MiniMax-M3.1-Flash", "MiniMax-M3", "MiniMax-M2.7", "custom-minimax-model"} {
		for _, operation := range []string{"chat", "chat-stream", "responses", "responses-stream"} {
			t.Run(model+"/"+operation, func(t *testing.T) {
				type capturedRequest struct {
					method, path, auth string
					body               map[string]any
				}
				captured := make(chan capturedRequest, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
					captured <- capturedRequest{r.Method, r.URL.Path, r.Header.Get("Authorization"), body}
					if body["stream"] == true {
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprint(w, "data: [DONE]\n\n")
						return
					}
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprintf(w, `{"id":"chatcmpl-rollout","object":"chat.completion","created":1,"model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}]}`, model)
				}))
				defer server.Close()
				baseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				deadline, _ := baseCtx.Deadline()
				ctx := schemas.NewBifrostContext(baseCtx, deadline)
				provider := testProvider(server.URL)
				key := schemas.Key{Value: schemas.SecretVar{Val: "test-rollout-key"}}
				var chatInput []schemas.ChatMessage
				if err := json.Unmarshal([]byte(`[{"role":"user","content":"Hello"}]`), &chatInput); err != nil {
					t.Fatal(err)
				}
				var responsesInput []schemas.ResponsesMessage
				if err := json.Unmarshal([]byte(`[{"role":"user","content":"Hello"}]`), &responsesInput); err != nil {
					t.Fatal(err)
				}
				extraParams := map[string]any{
					"thinking":         map[string]any{"type": "adaptive"},
					"reasoning_effort": "max",
				}
				chatRequest := &schemas.BifrostChatRequest{Provider: schemas.Minimax, Model: model, Input: chatInput, Params: &schemas.ChatParameters{ExtraParams: extraParams}}
				responsesRequest := &schemas.BifrostResponsesRequest{Provider: schemas.Minimax, Model: model, Input: responsesInput, Params: &schemas.ResponsesParameters{ExtraParams: extraParams}}
				var requestErr *schemas.BifrostError
				var stream chan *schemas.BifrostStreamChunk
				switch operation {
				case "chat":
					_, requestErr = provider.ChatCompletion(ctx, key, chatRequest)
				case "chat-stream":
					stream, requestErr = provider.ChatCompletionStream(ctx, passThroughPostHook, nil, key, chatRequest)
				case "responses":
					_, requestErr = provider.Responses(ctx, key, responsesRequest)
				case "responses-stream":
					stream, requestErr = provider.ResponsesStream(ctx, passThroughPostHook, nil, key, responsesRequest)
				}
				if requestErr != nil {
					t.Fatalf("provider request failed: %+v", requestErr)
				}
				if stream != nil {
					draining := true
					for draining {
						select {
						case chunk, ok := <-stream:
							if !ok {
								draining = false
							} else if chunk.BifrostError != nil {
								t.Fatalf("stream error: %+v", chunk.BifrostError)
							}
						case <-baseCtx.Done():
							t.Fatal("stream did not close")
						}
					}
				}
				select {
				case got := <-captured:
					if got.method != http.MethodPost || got.path != "/v1/chat/completions" || got.auth != "Bearer test-rollout-key" {
						t.Fatalf("unexpected transport: %+v", got)
					}
					if got.body["model"] != model {
						t.Fatalf("model rewritten: got %v, want %s", got.body["model"], model)
					}
					thinking, ok := got.body["thinking"].(map[string]any)
					if !ok || thinking["type"] != "adaptive" || got.body["reasoning_effort"] != "max" {
						t.Fatalf("thinking/effort changed: %+v", got.body)
					}
					if _, exists := got.body["service_tier"]; exists {
						t.Fatal("unexpected service_tier in default request")
					}
					wantStream := operation == "chat-stream" || operation == "responses-stream"
					if (got.body["stream"] == true) != wantStream {
						t.Fatalf("unexpected stream field: %v", got.body["stream"])
					}
					if messages, ok := got.body["messages"].([]any); !ok || len(messages) != 1 {
						t.Fatalf("missing serialized messages: %+v", got.body)
					}
				case <-baseCtx.Done():
					t.Fatal("no serialized request received")
				}
			})
		}
	}
}

func TestRolloutRegionalOrigins(t *testing.T) {
	for _, origin := range []string{"", "https://api.minimax.io/", "https://api.minimaxi.com/"} {
		t.Run(origin, func(t *testing.T) {
			provider, err := NewMinimaxProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: origin}}, testLogger{})
			if err != nil {
				t.Fatal(err)
			}
			want := "https://api.minimax.io"
			if origin == "https://api.minimaxi.com/" {
				want = "https://api.minimaxi.com"
			}
			if provider.networkConfig.BaseURL != want {
				t.Fatalf("origin = %q, want %q", provider.networkConfig.BaseURL, want)
			}
		})
	}
}
