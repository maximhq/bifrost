package openai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bytedance/sonic"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

type testOpenAILogger struct{}

func (testOpenAILogger) Debug(string, ...any)                   {}
func (testOpenAILogger) Info(string, ...any)                    {}
func (testOpenAILogger) Warn(string, ...any)                    {}
func (testOpenAILogger) Error(string, ...any)                   {}
func (testOpenAILogger) Fatal(string, ...any)                   {}
func (testOpenAILogger) SetLevel(schemas.LogLevel)              {}
func (testOpenAILogger) SetOutputType(schemas.LoggerOutputType) {}
func (testOpenAILogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

func testOpenAIResponsesCtx() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
}

func testOpenAIResponsesRequest() *schemas.BifrostResponsesRequest {
	content := "hello"
	return &schemas.BifrostResponsesRequest{
		Model: "test-model",
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

func noopOpenAIPostHookRunner(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return result, err
}

func newTestOpenAIProvider(baseURL string, customConfig *schemas.CustomProviderConfig) *OpenAIProvider {
	return NewOpenAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        baseURL,
			DefaultRequestTimeoutInSeconds: 5,
		},
		CustomProviderConfig: customConfig,
	}, testOpenAILogger{})
}

func testChatCompletionBody(text string) []byte {
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

func testResponsesBody(text string) []byte {
	messageType := schemas.ResponsesMessageTypeMessage
	role := schemas.ResponsesInputMessageRoleAssistant
	status := "completed"
	textType := schemas.ResponsesOutputMessageContentTypeText
	response := &schemas.BifrostResponsesResponse{
		ID:        schemas.Ptr("resp-test"),
		Object:    "response",
		CreatedAt: 1,
		Model:     "test-model",
		Status:    &status,
		Output: []schemas.ResponsesMessage{{
			Type: &messageType,
			Role: &role,
			Content: &schemas.ResponsesMessageContent{
				ContentBlocks: []schemas.ResponsesMessageContentBlock{{
					Type: textType,
					Text: &text,
				}},
			},
		}},
	}
	body, _ := sonic.Marshal(response)
	return body
}

func TestResponses_CustomProviderConfiguredUnsupported_DoesNotFallbackInsideProvider(t *testing.T) {
	t.Parallel()

	var chatHits atomic.Int32
	var responsesHits atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/chat/completions":
			chatHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(testChatCompletionBody("fallback response"))
		case "/v1/responses":
			responsesHits.Add(1)
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"unexpected responses endpoint"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	provider := newTestOpenAIProvider(server.URL, &schemas.CustomProviderConfig{
		CustomProviderKey:    "lmstudio",
		BaseProviderType:     schemas.OpenAI,
		IsKeyLess:            true,
		SupportsResponsesAPI: schemas.Ptr(false),
	})

	ctx := testOpenAIResponsesCtx()
	response, bifrostErr := provider.Responses(ctx, schemas.Key{}, testOpenAIResponsesRequest())
	if bifrostErr == nil {
		t.Fatal("expected provider-level responses call to fail without compat fallback")
	}
	if response != nil {
		t.Fatalf("expected nil response on provider-level failure, got %+v", response)
	}
	if chatHits.Load() != 0 {
		t.Fatalf("expected zero chat completion requests, got %d", chatHits.Load())
	}
	if responsesHits.Load() != 1 {
		t.Fatalf("expected one responses endpoint request, got %d", responsesHits.Load())
	}
	if ctx.Value(schemas.BifrostContextKeyIsResponsesToChatCompletionFallback) != nil {
		t.Fatalf("expected no fallback marker on provider-only path, got %v", ctx.Value(schemas.BifrostContextKeyIsResponsesToChatCompletionFallback))
	}
}

func TestResponses_CustomProviderRuntimeUnsupported_DoesNotFallbackInsideProvider(t *testing.T) {
	t.Parallel()

	var chatHits atomic.Int32
	var responsesHits atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses":
			responsesHits.Add(1)
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"responses endpoint unsupported"}}`))
		case "/v1/chat/completions":
			chatHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(testChatCompletionBody("auto fallback response"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	provider := newTestOpenAIProvider(server.URL, &schemas.CustomProviderConfig{
		CustomProviderKey: "lmstudio",
		BaseProviderType:  schemas.OpenAI,
		IsKeyLess:         true,
	})

	ctx := testOpenAIResponsesCtx()
	response, bifrostErr := provider.Responses(ctx, schemas.Key{}, testOpenAIResponsesRequest())
	if bifrostErr == nil {
		t.Fatal("expected provider-level responses call to fail without runtime compat retry")
	}
	if response != nil {
		t.Fatalf("expected nil response on provider-level failure, got %+v", response)
	}
	if responsesHits.Load() != 1 {
		t.Fatalf("expected one native responses attempt, got %d", responsesHits.Load())
	}
	if chatHits.Load() != 0 {
		t.Fatalf("expected zero chat completion fallback requests, got %d", chatHits.Load())
	}
	if ctx.Value(schemas.BifrostContextKeyIsResponsesToChatCompletionFallback) != nil {
		t.Fatalf("expected no fallback marker on provider-only path, got %v", ctx.Value(schemas.BifrostContextKeyIsResponsesToChatCompletionFallback))
	}
}

func TestResponses_CustomProviderUsesNativeResponsesWhenEnabled(t *testing.T) {
	t.Parallel()

	var chatHits atomic.Int32
	var responsesHits atomic.Int32
	var requestBody atomic.Value

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}

		switch r.URL.Path {
		case "/v1/responses":
			responsesHits.Add(1)
			requestBody.Store(string(bodyBytes))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(testResponsesBody("native custom response"))
		case "/v1/chat/completions":
			chatHits.Add(1)
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"unexpected chat endpoint"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	provider := newTestOpenAIProvider(server.URL, &schemas.CustomProviderConfig{
		CustomProviderKey:    "lmstudio",
		BaseProviderType:     schemas.OpenAI,
		IsKeyLess:            true,
		SupportsResponsesAPI: schemas.Ptr(true),
	})

	response, bifrostErr := provider.Responses(testOpenAIResponsesCtx(), schemas.Key{}, testOpenAIResponsesRequest())
	if bifrostErr != nil {
		t.Fatalf("Responses returned error: %v", bifrostErr.Error)
	}
	if response == nil {
		t.Fatal("Responses returned nil response")
	}
	if responsesHits.Load() != 1 {
		t.Fatalf("expected one responses request, got %d", responsesHits.Load())
	}
	if chatHits.Load() != 0 {
		t.Fatalf("expected zero chat completion requests, got %d", chatHits.Load())
	}

	body, _ := requestBody.Load().(string)
	if !strings.Contains(body, `"input"`) {
		t.Fatalf("expected native responses payload to contain input, got %s", body)
	}
	if strings.Contains(body, `"messages"`) {
		t.Fatalf("expected native responses payload to omit messages, got %s", body)
	}
	if !strings.Contains(body, `"max_output_tokens":`) {
		t.Fatalf("expected native responses payload to contain max_output_tokens, got %s", body)
	}

	// ExtraFields.Provider is populated by the core layer (PopulateExtraFields),
	// not the provider itself, so we don't assert it in provider-level tests.
	if len(response.Output) == 0 || response.Output[0].Content == nil || len(response.Output[0].Content.ContentBlocks) == 0 {
		t.Fatalf("expected native responses output, got %+v", response.Output)
	}
	if got := response.Output[0].Content.ContentBlocks[0].Text; got == nil || *got != "native custom response" {
		t.Fatalf("expected native output text native custom response, got %+v", got)
	}
}

func TestResponsesStream_CustomProviderRuntimeUnsupported_DoesNotFallbackInsideProvider(t *testing.T) {
	t.Parallel()

	var chatHits atomic.Int32
	var responsesHits atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/chat/completions":
			chatHits.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("unused"))
		case "/v1/responses":
			responsesHits.Add(1)
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"responses endpoint unsupported"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	provider := newTestOpenAIProvider(server.URL, &schemas.CustomProviderConfig{
		CustomProviderKey: "lmstudio",
		BaseProviderType:  schemas.OpenAI,
		IsKeyLess:         true,
	})

	ctx := testOpenAIResponsesCtx()
	streamChan, bifrostErr := provider.ResponsesStream(ctx, noopOpenAIPostHookRunner, func(context.Context) {}, schemas.Key{}, testOpenAIResponsesRequest())
	if bifrostErr == nil {
		t.Fatal("expected provider-level responses stream call to fail without runtime compat retry")
	}
	if streamChan != nil {
		t.Fatal("expected nil stream when provider-level stream setup fails")
	}
	if chatHits.Load() != 0 {
		t.Fatalf("expected zero chat completion stream requests, got %d", chatHits.Load())
	}
	if responsesHits.Load() != 1 {
		t.Fatalf("expected one native responses stream attempt, got %d", responsesHits.Load())
	}
	if ctx.Value(schemas.BifrostContextKeyIsResponsesToChatCompletionFallback) != nil {
		t.Fatalf("expected no fallback marker on provider-only path, got %v", ctx.Value(schemas.BifrostContextKeyIsResponsesToChatCompletionFallback))
	}
}

func TestResponses_NativeOpenAIStillUsesResponsesEndpoint(t *testing.T) {
	t.Parallel()

	var chatHits atomic.Int32
	var responsesHits atomic.Int32
	var requestBody atomic.Value

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}

		switch r.URL.Path {
		case "/v1/responses":
			responsesHits.Add(1)
			requestBody.Store(string(bodyBytes))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(testResponsesBody("native response"))
		case "/v1/chat/completions":
			chatHits.Add(1)
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"unexpected chat endpoint"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	provider := newTestOpenAIProvider(server.URL, nil)

	response, bifrostErr := provider.Responses(testOpenAIResponsesCtx(), schemas.Key{}, testOpenAIResponsesRequest())
	if bifrostErr != nil {
		t.Fatalf("Responses returned error: %v", bifrostErr.Error)
	}
	if response == nil {
		t.Fatal("Responses returned nil response")
	}
	if responsesHits.Load() != 1 {
		t.Fatalf("expected one responses request, got %d", responsesHits.Load())
	}
	if chatHits.Load() != 0 {
		t.Fatalf("expected zero chat completion requests, got %d", chatHits.Load())
	}

	body, _ := requestBody.Load().(string)
	if !strings.Contains(body, `"input"`) {
		t.Fatalf("expected native responses payload to contain input, got %s", body)
	}
	if strings.Contains(body, `"messages"`) {
		t.Fatalf("expected native responses payload to omit messages, got %s", body)
	}
	if got := response.Output[0].Content.ContentBlocks[0].Text; got == nil || *got != "native response" {
		t.Fatalf("expected native output text native response, got %+v", got)
	}
}
