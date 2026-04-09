package openai

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

func TestResponses_CustomProviderFallsBackToChatCompletion(t *testing.T) {
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
		case "/v1/chat/completions":
			chatHits.Add(1)
			requestBody.Store(string(bodyBytes))
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

	response, bifrostErr := provider.Responses(testOpenAIResponsesCtx(), schemas.Key{}, testOpenAIResponsesRequest())
	if bifrostErr != nil {
		t.Fatalf("Responses returned error: %v", bifrostErr.Error)
	}
	if response == nil {
		t.Fatal("Responses returned nil response")
	}
	if chatHits.Load() != 1 {
		t.Fatalf("expected one chat completion request, got %d", chatHits.Load())
	}
	if responsesHits.Load() != 0 {
		t.Fatalf("expected zero responses endpoint requests, got %d", responsesHits.Load())
	}

	body, _ := requestBody.Load().(string)
	if !strings.Contains(body, `"messages"`) {
		t.Fatalf("expected chat completion payload to contain messages, got %s", body)
	}
	if strings.Contains(body, `"input"`) {
		t.Fatalf("expected chat completion payload to omit responses input, got %s", body)
	}
	if !strings.Contains(body, `"max_completion_tokens":`) {
		t.Fatalf("expected chat completion payload to contain max_completion_tokens, got %s", body)
	}
	if strings.Contains(body, `"max_output_tokens"`) {
		t.Fatalf("expected chat completion payload to omit max_output_tokens, got %s", body)
	}

	if response.ExtraFields.Provider != schemas.ModelProvider("lmstudio") {
		t.Fatalf("expected provider to remain lmstudio, got %s", response.ExtraFields.Provider)
	}
	if response.ExtraFields.RequestType != schemas.ResponsesRequest {
		t.Fatalf("expected request type %s, got %s", schemas.ResponsesRequest, response.ExtraFields.RequestType)
	}
	if response.Model != "test-model" {
		t.Fatalf("expected model test-model, got %s", response.Model)
	}
	if len(response.Output) == 0 || response.Output[0].Content == nil || len(response.Output[0].Content.ContentBlocks) == 0 {
		t.Fatalf("expected converted responses output, got %+v", response.Output)
	}
	if got := response.Output[0].Content.ContentBlocks[0].Text; got == nil || *got != "fallback response" {
		t.Fatalf("expected converted output text fallback response, got %+v", got)
	}
}

func TestResponses_CustomProviderAutoFallsBackAfterUnsupportedResponsesError(t *testing.T) {
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

	response, bifrostErr := provider.Responses(testOpenAIResponsesCtx(), schemas.Key{}, testOpenAIResponsesRequest())
	if bifrostErr != nil {
		t.Fatalf("Responses returned error: %v", bifrostErr.Error)
	}
	if response == nil {
		t.Fatal("Responses returned nil response")
	}
	if responsesHits.Load() != 1 {
		t.Fatalf("expected one native responses attempt before fallback, got %d", responsesHits.Load())
	}
	if chatHits.Load() != 1 {
		t.Fatalf("expected one chat completion fallback request, got %d", chatHits.Load())
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

	if got := response.Output[0].Content.ContentBlocks[0].Text; got == nil || *got != "auto fallback response" {
		t.Fatalf("expected converted output text auto fallback response, got %+v", got)
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

	if response.ExtraFields.Provider != schemas.ModelProvider("lmstudio") {
		t.Fatalf("expected provider to remain lmstudio, got %s", response.ExtraFields.Provider)
	}
	if len(response.Output) == 0 || response.Output[0].Content == nil || len(response.Output[0].Content.ContentBlocks) == 0 {
		t.Fatalf("expected native responses output, got %+v", response.Output)
	}
	if got := response.Output[0].Content.ContentBlocks[0].Text; got == nil || *got != "native custom response" {
		t.Fatalf("expected native output text native custom response, got %+v", got)
	}
}

func TestResponsesStream_CustomProviderAutoFallsBackToChatCompletionStream(t *testing.T) {
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
		case "/v1/chat/completions":
			chatHits.Add(1)
			requestBody.Store(string(bodyBytes))
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, ok := w.(http.Flusher)
			if !ok {
				t.Fatal("response writer does not implement http.Flusher")
			}

			chunks := []string{
				`{"id":"chatcmpl-test","object":"chat.completion.chunk","created":1,"model":"test-model","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
				`{"id":"chatcmpl-test","object":"chat.completion.chunk","created":1,"model":"test-model","choices":[{"index":0,"delta":{"content":"fallback stream"}}]}`,
				`{"id":"chatcmpl-test","object":"chat.completion.chunk","created":1,"model":"test-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
			}

			for _, chunk := range chunks {
				_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
				flusher.Flush()
			}
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
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
		CustomProviderKey: "lmstudio",
		BaseProviderType:  schemas.OpenAI,
		IsKeyLess:         true,
	})

	ctx := testOpenAIResponsesCtx()
	streamChan, bifrostErr := provider.ResponsesStream(ctx, noopOpenAIPostHookRunner, schemas.Key{}, testOpenAIResponsesRequest())
	if bifrostErr != nil {
		t.Fatalf("ResponsesStream returned error: %v", bifrostErr.Error)
	}

	responses := collectResponsesStream(t, streamChan)
	if chatHits.Load() != 1 {
		t.Fatalf("expected one chat completion stream request, got %d", chatHits.Load())
	}
	if responsesHits.Load() != 1 {
		t.Fatalf("expected one native responses stream attempt before fallback, got %d", responsesHits.Load())
	}

	body, _ := requestBody.Load().(string)
	if !strings.Contains(body, `"messages"`) {
		t.Fatalf("expected streaming chat payload to contain messages, got %s", body)
	}
	if strings.Contains(body, `"input"`) {
		t.Fatalf("expected streaming chat payload to omit responses input, got %s", body)
	}

	seenTypes := map[schemas.ResponsesStreamResponseType]bool{}
	for _, response := range responses {
		seenTypes[response.Type] = true
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
	if ctx.Value(schemas.BifrostContextKeyIsResponsesToChatCompletionFallback) != true {
		t.Fatalf("expected fallback context marker to be set, got %v", ctx.Value(schemas.BifrostContextKeyIsResponsesToChatCompletionFallback))
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

func collectResponsesStream(t *testing.T, stream chan *schemas.BifrostStreamChunk) []*schemas.BifrostResponsesStreamResponse {
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
