package litellmcompat

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bytedance/sonic"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

type integrationAccount struct {
	mu      sync.RWMutex
	configs map[schemas.ModelProvider]*schemas.ProviderConfig
}

func newIntegrationAccount(baseURL string, supportsResponsesAPI *bool, allowedRequests *schemas.AllowedRequests) *integrationAccount {
	providerKey := schemas.ModelProvider("lmstudio")
	return &integrationAccount{
		configs: map[schemas.ModelProvider]*schemas.ProviderConfig{
			providerKey: &schemas.ProviderConfig{
				NetworkConfig: schemas.NetworkConfig{
					BaseURL:                        baseURL,
					DefaultRequestTimeoutInSeconds: 5,
				},
				ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
					Concurrency: 1,
					BufferSize:  8,
				},
				CustomProviderConfig: &schemas.CustomProviderConfig{
					CustomProviderKey:    string(providerKey),
					BaseProviderType:     schemas.OpenAI,
					SupportsResponsesAPI: supportsResponsesAPI,
					AllowedRequests:      allowedRequests,
					IsKeyLess:            true,
				},
			},
		},
	}
}

func (a *integrationAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	providers := make([]schemas.ModelProvider, 0, len(a.configs))
	for providerKey := range a.configs {
		providers = append(providers, providerKey)
	}

	return providers, nil
}

func (a *integrationAccount) GetKeysForProvider(context.Context, schemas.ModelProvider) ([]schemas.Key, error) {
	return nil, nil
}

func (a *integrationAccount) GetConfigForProvider(providerKey schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	config, ok := a.configs[providerKey]
	if !ok {
		return nil, fmt.Errorf("provider %s not configured", providerKey)
	}

	configCopy := *config
	if config.CustomProviderConfig != nil {
		customProviderConfigCopy := *config.CustomProviderConfig
		configCopy.CustomProviderConfig = &customProviderConfigCopy
	}

	return &configCopy, nil
}

func newIntegrationBifrost(t *testing.T, serverURL string, supportsResponsesAPI *bool, allowedRequests *schemas.AllowedRequests) *bifrost.Bifrost {
	t.Helper()

	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	plugin, err := InitWithModelCatalog(Config{Enabled: true}, logger, nil)
	if err != nil {
		t.Fatalf("InitWithModelCatalog returned error: %v", err)
	}

	instance, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account:         newIntegrationAccount(serverURL, supportsResponsesAPI, allowedRequests),
		Logger:          logger,
		LLMPlugins:      []schemas.LLMPlugin{plugin},
		InitialPoolSize: 1,
	})
	if err != nil {
		t.Fatalf("bifrost.Init returned error: %v", err)
	}

	return instance
}

func integrationResponsesRequest() *schemas.BifrostResponsesRequest {
	content := "hello"
	return &schemas.BifrostResponsesRequest{
		Provider: schemas.ModelProvider("lmstudio"),
		Model:    "test-model",
		Input: []schemas.ResponsesMessage{{
			Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{
				ContentStr: &content,
			},
		}},
		Params: &schemas.ResponsesParameters{
			MaxOutputTokens: schemas.Ptr(7),
		},
	}
}

func integrationChatCompletionBody(text string) []byte {
	finishReason := string(schemas.BifrostFinishReasonStop)
	response := &schemas.BifrostChatResponse{
		ID:      "chatcmpl-test",
		Object:  "chat.completion",
		Created: 1,
		Model:   "test-model",
		Choices: []schemas.BifrostResponseChoice{{
			Index:        0,
			FinishReason: &finishReason,
			ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
				Message: &schemas.ChatMessage{
					Role: schemas.ChatMessageRoleAssistant,
					Content: &schemas.ChatMessageContent{
						ContentStr: &text,
					},
				},
			},
		}},
		Usage: &schemas.BifrostLLMUsage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
	}
	body, _ := sonic.Marshal(response)
	return body
}

func collectRuntimeFallbackStream(t *testing.T, stream chan *schemas.BifrostStreamChunk) []*schemas.BifrostResponsesStreamResponse {
	t.Helper()

	responses := make([]*schemas.BifrostResponsesStreamResponse, 0)
	timeout := time.After(3 * time.Second)

	for {
		select {
		case chunk, ok := <-stream:
			if !ok {
				return responses
			}
			if chunk == nil {
				continue
			}
			if chunk.BifrostError != nil {
				t.Fatalf("unexpected stream error: %+v", chunk.BifrostError)
			}
			if chunk.BifrostResponsesStreamResponse != nil {
				responses = append(responses, chunk.BifrostResponsesStreamResponse)
			}
		case <-timeout:
			t.Fatal("timed out waiting for stream to complete")
		}
	}
}

func TestResponsesRequest_RuntimeFallbackRunsThroughBifrostCompat(t *testing.T) {
	t.Parallel()

	var chatHits atomic.Int32
	var responsesHits atomic.Int32
	var chatRequestBody atomic.Value
	var responsesRequestBody atomic.Value

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}

		switch r.URL.Path {
		case "/v1/responses":
			responsesHits.Add(1)
			responsesRequestBody.Store(string(bodyBytes))
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"responses endpoint unsupported"}}`))
		case "/v1/chat/completions":
			chatHits.Add(1)
			chatRequestBody.Store(string(bodyBytes))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(integrationChatCompletionBody("runtime fallback response"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	instance := newIntegrationBifrost(t, server.URL, nil, &schemas.AllowedRequests{Responses: true})
	defer instance.Shutdown()

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	response, bifrostErr := instance.ResponsesRequest(ctx, integrationResponsesRequest())
	if bifrostErr != nil {
		t.Fatalf("ResponsesRequest returned error: %v", bifrostErr.Error)
	}
	if response == nil {
		t.Fatal("ResponsesRequest returned nil response")
	}
	if responsesHits.Load() != 1 {
		t.Fatalf("expected one native responses attempt, got %d", responsesHits.Load())
	}
	if chatHits.Load() != 1 {
		t.Fatalf("expected one fallback chat attempt, got %d", chatHits.Load())
	}

	responsesBody, _ := responsesRequestBody.Load().(string)
	if !strings.Contains(responsesBody, `"input"`) {
		t.Fatalf("expected initial responses payload to contain input, got %s", responsesBody)
	}
	chatBody, _ := chatRequestBody.Load().(string)
	if !strings.Contains(chatBody, `"messages"`) {
		t.Fatalf("expected fallback chat payload to contain messages, got %s", chatBody)
	}
	if strings.Contains(chatBody, `"input"`) {
		t.Fatalf("expected fallback chat payload to omit responses input, got %s", chatBody)
	}

	if !response.ExtraFields.LiteLLMCompat {
		t.Fatal("expected litellm compat marker on fallback response")
	}
	if !response.ExtraFields.ResponsesToChatCompletionFallback {
		t.Fatal("expected explicit responses-to-chat fallback marker on response")
	}
	if response.ExtraFields.ResponsesToChatCompletionFallbackReason != string(schemas.ResponsesToChatCompletionFallbackReasonRuntimeUnsupported) {
		t.Fatalf("expected fallback reason %q, got %q", schemas.ResponsesToChatCompletionFallbackReasonRuntimeUnsupported, response.ExtraFields.ResponsesToChatCompletionFallbackReason)
	}
	if got := response.Output[0].Content.ContentBlocks[0].Text; got == nil || *got != "runtime fallback response" {
		t.Fatalf("expected converted output text runtime fallback response, got %+v", got)
	}
	if reason, ok := schemas.GetResponsesToChatCompletionFallback(ctx); !ok || reason != schemas.ResponsesToChatCompletionFallbackReasonRuntimeUnsupported {
		t.Fatalf("expected runtime fallback context reason, got %q (ok=%v)", reason, ok)
	}
}

func TestResponsesRequest_ConfiguredFallbackBypassesChatAllowedRequestsGate(t *testing.T) {
	t.Parallel()

	var chatHits atomic.Int32
	var responsesHits atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}

		switch r.URL.Path {
		case "/v1/responses":
			responsesHits.Add(1)
			w.WriteHeader(http.StatusNotFound)
		case "/v1/chat/completions":
			chatHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(integrationChatCompletionBody("configured fallback response"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	instance := newIntegrationBifrost(t, server.URL, schemas.Ptr(false), &schemas.AllowedRequests{Responses: true})
	defer instance.Shutdown()

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	response, bifrostErr := instance.ResponsesRequest(ctx, integrationResponsesRequest())
	if bifrostErr != nil {
		t.Fatalf("ResponsesRequest returned error: %v", bifrostErr.Error)
	}
	if response == nil {
		t.Fatal("ResponsesRequest returned nil response")
	}
	if responsesHits.Load() != 0 {
		t.Fatalf("expected zero native responses attempts during configured fallback, got %d", responsesHits.Load())
	}
	if chatHits.Load() != 1 {
		t.Fatalf("expected one fallback chat attempt, got %d", chatHits.Load())
	}
	if got := response.Output[0].Content.ContentBlocks[0].Text; got == nil || *got != "configured fallback response" {
		t.Fatalf("expected converted output text configured fallback response, got %+v", got)
	}
	if reason, ok := schemas.GetResponsesToChatCompletionFallback(ctx); !ok || reason != schemas.ResponsesToChatCompletionFallbackReasonConfiguredUnsupported {
		t.Fatalf("expected configured fallback context reason, got %q (ok=%v)", reason, ok)
	}
}

func TestResponsesStreamRequest_RuntimeFallbackRunsThroughBifrostCompat(t *testing.T) {
	t.Parallel()

	var chatHits atomic.Int32
	var responsesHits atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}

		switch r.URL.Path {
		case "/v1/responses":
			responsesHits.Add(1)
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"responses endpoint unsupported"}}`))
		case "/v1/chat/completions":
			chatHits.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, ok := w.(http.Flusher)
			if !ok {
				t.Fatal("response writer does not implement http.Flusher")
			}

			chunks := []string{
				`{"id":"chatcmpl-test","object":"chat.completion.chunk","created":1,"model":"test-model","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
				`{"id":"chatcmpl-test","object":"chat.completion.chunk","created":1,"model":"test-model","choices":[{"index":0,"delta":{"content":"runtime fallback stream"}}]}`,
				`{"id":"chatcmpl-test","object":"chat.completion.chunk","created":1,"model":"test-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
			}

			for _, chunk := range chunks {
				_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
				flusher.Flush()
			}
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	instance := newIntegrationBifrost(t, server.URL, nil, &schemas.AllowedRequests{ResponsesStream: true})
	defer instance.Shutdown()

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	streamChan, bifrostErr := instance.ResponsesStreamRequest(ctx, integrationResponsesRequest())
	if bifrostErr != nil {
		t.Fatalf("ResponsesStreamRequest returned error: %v", bifrostErr.Error)
	}

	responses := collectRuntimeFallbackStream(t, streamChan)
	if responsesHits.Load() != 1 {
		t.Fatalf("expected one native responses stream attempt, got %d", responsesHits.Load())
	}
	if chatHits.Load() != 1 {
		t.Fatalf("expected one fallback chat stream attempt, got %d", chatHits.Load())
	}

	seenTypes := map[schemas.ResponsesStreamResponseType]bool{}
	for _, response := range responses {
		seenTypes[response.Type] = true
		if response.ExtraFields.RequestType != schemas.ResponsesStreamRequest {
			t.Fatalf("expected request type %s, got %s", schemas.ResponsesStreamRequest, response.ExtraFields.RequestType)
		}
		if !response.ExtraFields.LiteLLMCompat {
			t.Fatal("expected litellm compat marker on streamed fallback response")
		}
		if !response.ExtraFields.ResponsesToChatCompletionFallback {
			t.Fatal("expected explicit responses-to-chat fallback marker on streamed response")
		}
		if response.ExtraFields.ResponsesToChatCompletionFallbackReason != string(schemas.ResponsesToChatCompletionFallbackReasonRuntimeUnsupported) {
			t.Fatalf("expected fallback reason %q, got %q", schemas.ResponsesToChatCompletionFallbackReasonRuntimeUnsupported, response.ExtraFields.ResponsesToChatCompletionFallbackReason)
		}
	}

	if !seenTypes[schemas.ResponsesStreamResponseTypeCreated] {
		t.Fatalf("expected response.created event, got %#v", seenTypes)
	}
	if !seenTypes[schemas.ResponsesStreamResponseTypeOutputTextDelta] {
		t.Fatalf("expected response.output_text.delta event, got %#v", seenTypes)
	}
	if !seenTypes[schemas.ResponsesStreamResponseTypeCompleted] {
		t.Fatalf("expected response.completed event, got %#v", seenTypes)
	}
	if reason, ok := schemas.GetResponsesToChatCompletionFallback(ctx); !ok || reason != schemas.ResponsesToChatCompletionFallbackReasonRuntimeUnsupported {
		t.Fatalf("expected runtime fallback context reason, got %q (ok=%v)", reason, ok)
	}
}
