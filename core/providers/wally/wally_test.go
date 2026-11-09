package wally_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/wally"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestWally(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("WALLY_API_KEY")) == "" {
		t.Skip("Skipping Wally tests because WALLY_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	// Model IDs come from the public catalog at https://inference.runanywhere.ai/v1/models.
	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:  schemas.Wally,
		ChatModel: "deepseek-v4.1-flash",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Wally, Model: "deepseek-v4.1-flash"},
			{Provider: schemas.Wally, Model: "deepseek-v4.1-flash-fast"},
		},
		TextModel:      "", // Wally serves chat completions only
		EmbeddingModel: "", // Wally doesn't support embedding
		ReasoningModel: "deepseek-v4.1-flash",
		Scenarios: llmtests.TestScenarios{
			TextCompletion:             false,
			TextCompletionStream:       false,
			SimpleChat:                 true,
			CompletionStream:           true,
			MultiTurnConversation:      true,
			ToolCalls:                  true,
			ToolCallsStreaming:         true,
			MultipleToolCalls:          true,
			MultipleToolCallsStreaming: true,
			End2EndToolCalling:         true,
			AutomaticFunctionCall:      true,
			// Image input works on the vision models (deepseek-v4.1-flash, kimi-k3), but they think by
			// default and these cases cap output at 200-600 tokens, which often runs out during
			// reasoning (finish_reason length, empty content), so they are off to keep the suite stable.
			ImageURL:          false,
			ImageBase64:       false,
			MultipleImages:    false,
			CompleteEnd2End:   false,
			StructuredOutputs: true,
			Embedding:         false,
			ListModels:        true,
			Reasoning:         true,
		},
	}

	t.Run("WallyTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

// TestWallyChatCompletionWire checks the request Wally receives against a stub
// upstream: the OpenAI-compatible /chat/completions path under the configured
// base URL, Bearer auth, and the model id passed through unchanged.
func TestWallyChatCompletionWire(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var gotMethod, gotPath, gotAuth string
	var gotBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotBody = body
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-1",
			"object": "chat.completion",
			"created": 1700000000,
			"model": "deepseek-v4.1-flash",
			"choices": [{"index": 0, "message": {"role": "assistant", "content": "ok"}, "finish_reason": "stop"}],
			"usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
		}`))
	}))
	defer server.Close()

	provider, err := wally.NewWallyProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        server.URL + "/v1/",
			DefaultRequestTimeoutInSeconds: 10,
		},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		t.Fatalf("NewWallyProvider: %v", err)
	}

	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	key := schemas.Key{Models: []string{"*"}, Value: *schemas.NewSecretVar("test-key")}
	request := &schemas.BifrostChatRequest{
		Provider: schemas.Wally,
		Model:    "deepseek-v4.1-flash",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")},
		}},
	}

	if _, bifrostErr := provider.ChatCompletion(ctx, key, request); bifrostErr != nil {
		t.Fatalf("ChatCompletion returned an error: %v", bifrostErr)
	}

	mu.Lock()
	defer mu.Unlock()

	if gotMethod != http.MethodPost {
		t.Errorf("method: got %q, want POST", gotMethod)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("path: got %q, want /v1/chat/completions", gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("authorization header: got %q, want %q", gotAuth, "Bearer test-key")
	}

	var wire struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(gotBody, &wire); err != nil {
		t.Fatalf("unmarshal outbound body: %v; got: %s", err, gotBody)
	}
	if wire.Model != "deepseek-v4.1-flash" {
		t.Errorf("outbound model: got %q, want deepseek-v4.1-flash", wire.Model)
	}
}

// TestWallyTextCompletionUnsupported pins that text completions fail fast with an
// unsupported-operation error instead of reaching Wally, which only serves chat.
func TestWallyTextCompletionUnsupported(t *testing.T) {
	t.Parallel()

	provider, err := wally.NewWallyProvider(&schemas.ProviderConfig{}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		t.Fatalf("NewWallyProvider: %v", err)
	}

	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, bifrostErr := provider.TextCompletion(ctx, schemas.Key{}, &schemas.BifrostTextCompletionRequest{
		Provider: schemas.Wally,
		Model:    "deepseek-v4.1-flash",
	})
	if bifrostErr == nil {
		t.Fatal("TextCompletion: expected an unsupported-operation error, got nil")
	}
	if bifrostErr.Error == nil || !strings.Contains(bifrostErr.Error.Message, "not supported") {
		t.Errorf("TextCompletion error: got %+v, want an unsupported-operation message", bifrostErr.Error)
	}
}

// Local upstream capture pins the documented token field on every Wally route.
func TestWallyOutputTokenContractWire(t *testing.T) {
	for _, responses := range []bool{false, true} {
		for _, streaming := range []bool{false, true} {
			name := "chat"
			if responses {
				name = "responses"
			}
			if streaming {
				name += "/stream"
			}
			t.Run(name, func(t *testing.T) {
				bodies := make(chan []byte, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
					}
					bodies <- body
					if r.URL.Path != "/v1/chat/completions" {
						t.Errorf("path: %s", r.URL.Path)
					}
					if streaming {
						w.Header().Set("Content-Type", "text/event-stream")
						io.WriteString(w, "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"deepseek-v4.1-flash\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"deepseek-v4.1-flash\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					} else {
						w.Header().Set("Content-Type", "application/json")
						io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"deepseek-v4.1-flash","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
					}
				}))
				defer server.Close()
				provider, err := wally.NewWallyProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL + "/v1", AllowPrivateNetwork: true}}, bifrost.NewDefaultLogger(schemas.LogLevelError))
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				key := schemas.Key{Value: *schemas.NewSecretVar("local-test-key")}
				chat := &schemas.BifrostChatRequest{Provider: schemas.Wally, Model: "deepseek-v4.1-flash", Input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}}}, Params: &schemas.ChatParameters{MaxCompletionTokens: schemas.Ptr(512)}}
				responseReq := &schemas.BifrostResponsesRequest{Provider: schemas.Wally, Model: chat.Model, Input: []schemas.ResponsesMessage{{Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser), Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")}}}, Params: &schemas.ResponsesParameters{MaxOutputTokens: schemas.Ptr(512)}}
				var stream chan *schemas.BifrostStreamChunk
				var bfErr *schemas.BifrostError
				postHookRunner := func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
					return result, err
				}
				if responses {
					if streaming {
						stream, bfErr = provider.ResponsesStream(ctx, postHookRunner, nil, key, responseReq)
					} else {
						_, bfErr = provider.Responses(ctx, key, responseReq)
					}
				} else {
					if streaming {
						stream, bfErr = provider.ChatCompletionStream(ctx, postHookRunner, nil, key, chat)
					} else {
						_, bfErr = provider.ChatCompletion(ctx, key, chat)
					}
				}
				if bfErr != nil {
					t.Fatalf("provider error: %v", bfErr)
				}
				if streaming {
					for chunk := range stream {
						if chunk.BifrostError != nil {
							t.Errorf("stream error: %v", chunk.BifrostError)
						}
					}
				}
				body := <-bodies
				t.Logf("outgoing body: %s", body)
				var wire map[string]json.RawMessage
				if err := json.Unmarshal(body, &wire); err != nil {
					t.Fatal(err)
				}
				if string(wire["max_tokens"]) != "512" {
					t.Errorf("want max_tokens=512; got %s", body)
				}
				if _, ok := wire["max_completion_tokens"]; ok {
					t.Errorf("unexpected max_completion_tokens: %s", body)
				}
			})
		}
	}
}
