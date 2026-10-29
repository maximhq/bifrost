package kimchi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/kimchi"
	"github.com/maximhq/bifrost/core/schemas"
)

// TestKimchi runs the shared live scenario suite against the Kimchi API. It is skipped when
// KIMCHI_API_KEY is not set.
func TestKimchi(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("KIMCHI_API_KEY")) == "" {
		t.Skip("Skipping Kimchi tests because KIMCHI_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:       schemas.Kimchi,
		ChatModel:      "deepseek-v4.1-flash",
		ReasoningModel: "glm-5.3-flash",
		EmbeddingModel: "bge-m3",
		Scenarios: llmtests.TestScenarios{
			TextCompletion:        false, // Kimchi does not serve /v1/completions
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
			Embedding:             true,
			ListModels:            true,
			Reasoning:             true,
		},
	}

	t.Run("KimchiTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

type capturedRequest struct {
	method string
	path   string
	auth   string
}

// newMockKimchi starts a server that records each request and replies with the handler registered for "METHOD /path".
func newMockKimchi(t *testing.T, handlers map[string]http.HandlerFunc) (*kimchi.KimchiProvider, func() []capturedRequest) {
	t.Helper()
	var mu sync.Mutex
	var captured []capturedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		captured = append(captured, capturedRequest{method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization")})
		mu.Unlock()
		if handler, ok := handlers[r.Method+" "+r.URL.Path]; ok {
			handler(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	provider, err := kimchi.NewKimchiProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        server.URL + "/",
			DefaultRequestTimeoutInSeconds: 10,
			AllowPrivateNetwork:            true,
		},
	}, bifrost.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewKimchiProvider: %v", err)
	}
	return provider, func() []capturedRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]capturedRequest(nil), captured...)
	}
}

func jsonResponse(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
}

// sseResponse writes each event as-is and flushes, so comment lines reach the client the way a live stream sends them.
func sseResponse(events ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, event := range events {
			_, _ = w.Write([]byte(event))
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

func testContext(t *testing.T) *schemas.BifrostContext {
	t.Helper()
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func testKey() schemas.Key {
	return schemas.Key{Value: *schemas.NewSecretVar("test-key"), Models: []string{"*"}}
}

func chatRequest() *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: schemas.Kimchi,
		Model:    "glm-5.3-flash",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}}},
	}
}

func passThroughPostHook(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return result, err
}

const chatBody = `{"id":"c1","object":"chat.completion","created":1,"model":"glm-5.3-flash","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"ok","reasoning_content":"thinking"}}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5,"completion_tokens_details":{"reasoning_tokens":1}}}`

// TestKimchiDefaultBaseURL checks that the constructor fills in Kimchi's base URL when the
// config leaves it empty.
func TestKimchiDefaultBaseURL(t *testing.T) {
	config := &schemas.ProviderConfig{}
	if _, err := kimchi.NewKimchiProvider(config, bifrost.NewNoOpLogger()); err != nil {
		t.Fatalf("NewKimchiProvider: %v", err)
	}
	if config.NetworkConfig.BaseURL != "https://llm.kimchi.dev/openai" {
		t.Fatalf("BaseURL = %q, want https://llm.kimchi.dev/openai", config.NetworkConfig.BaseURL)
	}
}

// TestKimchiRequestPaths pins the upstream path of every supported operation, including
// Responses, which is served through chat completions.
func TestKimchiRequestPaths(t *testing.T) {
	tests := []struct {
		name     string
		wantCall string
		run      func(t *testing.T, provider *kimchi.KimchiProvider)
	}{
		{
			name:     "ChatCompletion",
			wantCall: "POST /v1/chat/completions",
			run: func(t *testing.T, provider *kimchi.KimchiProvider) {
				if _, err := provider.ChatCompletion(testContext(t), testKey(), chatRequest()); err != nil {
					t.Fatalf("ChatCompletion: %v", llmtests.GetErrorMessage(err))
				}
			},
		},
		{
			name:     "Responses",
			wantCall: "POST /v1/chat/completions",
			run: func(t *testing.T, provider *kimchi.KimchiProvider) {
				resp, err := provider.Responses(testContext(t), testKey(), &schemas.BifrostResponsesRequest{
					Provider: schemas.Kimchi,
					Model:    "glm-5.3-flash",
					Input:    []schemas.ResponsesMessage{llmtests.CreateBasicResponsesMessage("hi")},
				})
				if err != nil {
					t.Fatalf("Responses: %v", llmtests.GetErrorMessage(err))
				}
				if resp == nil || len(resp.Output) == 0 {
					t.Fatalf("expected responses output, got %#v", resp)
				}
			},
		},
		{
			name:     "Embedding",
			wantCall: "POST /v1/embeddings",
			run: func(t *testing.T, provider *kimchi.KimchiProvider) {
				resp, err := provider.Embedding(testContext(t), testKey(), &schemas.BifrostEmbeddingRequest{
					Provider: schemas.Kimchi,
					Model:    "bge-m3",
					Input: []schemas.EmbeddingInputItem{
						{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: schemas.Ptr("hi")}}},
					},
				})
				if err != nil {
					t.Fatalf("Embedding: %v", llmtests.GetErrorMessage(err))
				}
				if resp == nil || len(resp.Data) != 1 {
					t.Fatalf("unexpected embedding response: %#v", resp)
				}
			},
		},
		{
			name:     "ListModels",
			wantCall: "GET /v1/models",
			run: func(t *testing.T, provider *kimchi.KimchiProvider) {
				if _, err := provider.ListModels(testContext(t), []schemas.Key{testKey()}, &schemas.BifrostListModelsRequest{Provider: schemas.Kimchi}); err != nil {
					t.Fatalf("ListModels: %v", llmtests.GetErrorMessage(err))
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, captured := newMockKimchi(t, map[string]http.HandlerFunc{
				"POST /v1/chat/completions": jsonResponse(chatBody),
				"POST /v1/embeddings":       jsonResponse(`{"object":"list","model":"bge-m3","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"usage":{"prompt_tokens":1,"total_tokens":1}}`),
				"GET /v1/models":            jsonResponse(`{"data":[{"id":"glm-5.3-flash","object":"model","created":0,"owned_by":"castai"}]}`),
			})

			tt.run(t, provider)

			reqs := captured()
			if len(reqs) != 1 {
				t.Fatalf("expected 1 upstream request, got %d", len(reqs))
			}
			if got := reqs[0].method + " " + reqs[0].path; got != tt.wantCall {
				t.Errorf("upstream call = %q, want %q", got, tt.wantCall)
			}
			if reqs[0].auth != "Bearer test-key" {
				t.Errorf("Authorization = %q, want Bearer test-key", reqs[0].auth)
			}
		})
	}
}

// TestKimchiChatMapsReasoningContent checks that reasoning_content in a non-streaming chat
// response is mapped to the message's Reasoning field.
func TestKimchiChatMapsReasoningContent(t *testing.T) {
	provider, _ := newMockKimchi(t, map[string]http.HandlerFunc{
		"POST /v1/chat/completions": jsonResponse(chatBody),
	})

	resp, err := provider.ChatCompletion(testContext(t), testKey(), chatRequest())
	if err != nil {
		t.Fatalf("ChatCompletion: %v", llmtests.GetErrorMessage(err))
	}
	msg := resp.Choices[0].ChatNonStreamResponseChoice.Message
	if msg.ChatAssistantMessage == nil || msg.ChatAssistantMessage.Reasoning == nil || *msg.ChatAssistantMessage.Reasoning != "thinking" {
		t.Fatalf("reasoning_content not mapped to Reasoning: %+v", msg.ChatAssistantMessage)
	}
}

// Kimchi sends ": keep-alive" comments while the model is silent and streams reasoning under
// delta.reasoning_content. The comments must be skipped and the reasoning mapped.
func TestKimchiChatStreamSkipsKeepAliveAndMapsReasoning(t *testing.T) {
	provider, _ := newMockKimchi(t, map[string]http.HandlerFunc{
		"POST /v1/chat/completions": sseResponse(
			": keep-alive\n\n",
			`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"glm-5.3-flash","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"thinking"}}]}`+"\n\n",
			": keep-alive\n\n",
			`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"glm-5.3-flash","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\n",
			`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"glm-5.3-flash","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`+"\n\n",
			"data: [DONE]\n\n",
		),
	})

	stream, bifrostErr := provider.ChatCompletionStream(testContext(t), passThroughPostHook, nil, testKey(), chatRequest())
	if bifrostErr != nil {
		t.Fatalf("ChatCompletionStream: %v", llmtests.GetErrorMessage(bifrostErr))
	}

	var reasoning, content strings.Builder
	for chunk := range stream {
		if chunk.BifrostError != nil {
			t.Fatalf("unexpected stream error: %v", llmtests.GetErrorMessage(chunk.BifrostError))
		}
		if chunk.BifrostChatResponse == nil {
			continue
		}
		for _, choice := range chunk.BifrostChatResponse.Choices {
			if choice.ChatStreamResponseChoice == nil || choice.ChatStreamResponseChoice.Delta == nil {
				continue
			}
			delta := choice.ChatStreamResponseChoice.Delta
			if delta.Reasoning != nil {
				reasoning.WriteString(*delta.Reasoning)
			}
			if delta.Content != nil {
				content.WriteString(*delta.Content)
			}
		}
	}
	if reasoning.String() != "thinking" || content.String() != "ok" {
		t.Fatalf("reasoning = %q, content = %q; want thinking, ok", reasoning.String(), content.String())
	}
}

// Kimchi reports a failure after the stream has started as a chunk carrying a top-level error
// object. It must reach the caller as a regular Bifrost stream error.
func TestKimchiChatStreamSurfacesMidStreamError(t *testing.T) {
	provider, _ := newMockKimchi(t, map[string]http.HandlerFunc{
		"POST /v1/chat/completions": sseResponse(
			`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"glm-5.3-flash","choices":[{"index":0,"delta":{"role":"assistant","content":"partial"}}]}`+"\n\n",
			`data: {"id":"","object":"chat.completion.chunk","created":1,"choices":[{"index":0,"delta":{},"finish_reason":"network_error"}],"error":{"message":"The service is temporarily unavailable.","type":"overloaded_error"}}`+"\n\n",
			"data: [DONE]\n\n",
		),
	})

	stream, bifrostErr := provider.ChatCompletionStream(testContext(t), passThroughPostHook, nil, testKey(), chatRequest())
	if bifrostErr != nil {
		t.Fatalf("ChatCompletionStream: %v", llmtests.GetErrorMessage(bifrostErr))
	}

	var streamErr *schemas.BifrostError
	for chunk := range stream {
		if chunk.BifrostError != nil {
			streamErr = chunk.BifrostError
		}
	}
	if streamErr == nil || streamErr.Error == nil {
		t.Fatal("expected a stream error chunk")
	}
	if streamErr.Error.Message != "The service is temporarily unavailable." {
		t.Errorf("error message = %q", streamErr.Error.Message)
	}
	if streamErr.Error.Type == nil || *streamErr.Error.Type != "overloaded_error" {
		t.Errorf("error type = %v, want overloaded_error", streamErr.Error.Type)
	}
}

// When Kimchi returns context_window on /v1/models in the OpenAI envelope, it is carried
// into the Bifrost model list, so clients can size their context from it.
func TestKimchiListModelsCarriesContextWindow(t *testing.T) {
	provider, _ := newMockKimchi(t, map[string]http.HandlerFunc{
		"GET /v1/models": jsonResponse(`{"object":"list","data":[{"id":"glm-5.3-flash","object":"model","created":0,"owned_by":"castai","context_window":1048576},{"id":"bge-m3","object":"model","created":0,"owned_by":"castai"}]}`),
	})

	resp, err := provider.ListModels(testContext(t), []schemas.Key{testKey()}, &schemas.BifrostListModelsRequest{Provider: schemas.Kimchi})
	if err != nil {
		t.Fatalf("ListModels: %v", llmtests.GetErrorMessage(err))
	}
	contextLengths := map[string]*int{}
	for _, model := range resp.Data {
		contextLengths[model.ID] = model.ContextLength
	}
	if got := contextLengths["kimchi/glm-5.3-flash"]; got == nil || *got != 1048576 {
		t.Errorf("kimchi/glm-5.3-flash context length = %v, want 1048576 (models: %v)", got, contextLengths)
	}
	if got, ok := contextLengths["kimchi/bge-m3"]; !ok || got != nil {
		t.Errorf("kimchi/bge-m3 should be listed without a context length, got present=%v value=%v", ok, got)
	}
}

// TestKimchiTextCompletionUnsupported checks that text completions return unsupported_operation
// without calling the upstream API, because Kimchi does not serve /v1/completions.
func TestKimchiTextCompletionUnsupported(t *testing.T) {
	provider, captured := newMockKimchi(t, nil)
	prompt := "hi"
	request := &schemas.BifrostTextCompletionRequest{Provider: schemas.Kimchi, Model: "glm-5.3-flash", Input: &schemas.TextCompletionInput{PromptStr: &prompt}}

	_, err := provider.TextCompletion(testContext(t), testKey(), request)
	if err == nil || err.Error == nil || err.Error.Code == nil || *err.Error.Code != "unsupported_operation" {
		t.Errorf("TextCompletion error = %+v, want unsupported_operation", err)
	}
	_, err = provider.TextCompletionStream(testContext(t), passThroughPostHook, nil, testKey(), request)
	if err == nil || err.Error == nil || err.Error.Code == nil || *err.Error.Code != "unsupported_operation" {
		t.Errorf("TextCompletionStream error = %+v, want unsupported_operation", err)
	}
	if reqs := captured(); len(reqs) != 0 {
		t.Errorf("unsupported operations must not call upstream, got %d requests", len(reqs))
	}
}
