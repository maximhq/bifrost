package nearai_test

import (
	"context"
	"fmt"
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
	"github.com/maximhq/bifrost/core/providers/nearai"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestNearai(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("NEARAI_API_KEY")) == "" {
		t.Skip("Skipping NEAR AI tests because NEARAI_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	// The scenarios give models small output budgets (150 tokens for text completion, 300 for
	// streamed tool calls) and a 30s stream timeout, and they exercise json_schema structured
	// outputs. NEAR AI's hosted reasoning models spend those budgets thinking and miss the
	// timeout, and its DeepSeek route rejects json_schema, so chat and text completion use a
	// relayed non-reasoning model. NEAR AI-hosted models cover reasoning and embeddings.
	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:  schemas.NearAI,
		ChatModel: "openai/gpt-4.1-mini",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.NearAI, Model: "z-ai/glm-5.3-flash"},
		},
		TextModel:      "openai/gpt-4.1-mini",
		EmbeddingModel: "Qwen/Qwen3-Embedding-0.6B",
		ReasoningModel: "z-ai/glm-5.3-flash",
		Scenarios: llmtests.TestScenarios{
			TextCompletion:        true,
			TextCompletionStream:  true,
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			MultipleToolCalls:     false,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              false,
			ImageBase64:           false,
			MultipleImages:        false,
			CompleteEnd2End:       true,
			Embedding:             true,
			ListModels:            true,
			Reasoning:             true,
			StructuredOutputs:     true,
		},
	}

	t.Run("NearaiTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

const (
	testModel        = "z-ai/glm-5.3-flash"
	chatResponseBody = `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"z-ai/glm-5.3-flash","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`
)

// receivedRequest is what the fake NEAR AI server saw.
type receivedRequest struct {
	method string
	path   string
	auth   string
	body   string
}

// capturedRequest records the last request the fake NEAR AI server received.
type capturedRequest struct {
	mu   sync.Mutex
	last receivedRequest
}

func (c *capturedRequest) record(r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.last = receivedRequest{method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization"), body: string(body)}
}

func (c *capturedRequest) snapshot() receivedRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}

// newJSONServer serves body as a JSON response and records the request it received.
func newJSONServer(t *testing.T, status int, body string) (*httptest.Server, *capturedRequest) {
	t.Helper()
	captured := &capturedRequest{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.record(r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	return server, captured
}

// newSSEServer serves the given pre-formatted SSE events and records the request it received.
func newSSEServer(t *testing.T, events ...string) (*httptest.Server, *capturedRequest) {
	t.Helper()
	captured := &capturedRequest{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.record(r)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for _, event := range events {
			_, _ = fmt.Fprint(w, event)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	t.Cleanup(server.Close)
	return server, captured
}

// newTestProvider points the provider at baseURL. A trailing slash is appended on purpose
// so every route test also checks that the constructor trims it.
func newTestProvider(t *testing.T, baseURL string) *nearai.NearAIProvider {
	t.Helper()
	provider, err := nearai.NewNearAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: baseURL + "/", DefaultRequestTimeoutInSeconds: 10},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		t.Fatalf("NewNearAIProvider: %v", err)
	}
	return provider
}

func newTestKey() schemas.Key {
	return schemas.Key{Value: schemas.SecretVar{Val: "test-key"}, Models: []string{"*"}}
}

func newTestContext() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
}

func newChatRequest() *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: schemas.NearAI,
		Model:    testModel,
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")},
		}},
	}
}

func newResponsesRequest() *schemas.BifrostResponsesRequest {
	return &schemas.BifrostResponsesRequest{
		Provider: schemas.NearAI,
		Model:    testModel,
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")},
		}},
	}
}

var passthroughPostHookRunner schemas.PostHookRunner = func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return result, err
}

// drainStream collects every chunk until the stream closes, failing instead of hanging.
func drainStream(t *testing.T, stream chan *schemas.BifrostStreamChunk) []*schemas.BifrostStreamChunk {
	t.Helper()
	var chunks []*schemas.BifrostStreamChunk
	done := make(chan struct{})
	go func() {
		defer close(done)
		for chunk := range stream {
			chunks = append(chunks, chunk)
		}
	}()
	select {
	case <-done:
		return chunks
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not close within 5s")
		return nil
	}
}

func assertRequest(t *testing.T, got receivedRequest, wantMethod, wantPath string) {
	t.Helper()
	if got.method != wantMethod {
		t.Errorf("method = %q, want %q", got.method, wantMethod)
	}
	if got.path != wantPath {
		t.Errorf("path = %q, want %q", got.path, wantPath)
	}
	if got.auth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want %q", got.auth, "Bearer test-key")
	}
}

func TestChatCompletion_SendsBearerKeyToChatCompletions(t *testing.T) {
	t.Parallel()
	server, captured := newJSONServer(t, http.StatusOK, chatResponseBody)

	response, bifrostErr := newTestProvider(t, server.URL).ChatCompletion(newTestContext(), newTestKey(), newChatRequest())
	if bifrostErr != nil {
		t.Fatalf("ChatCompletion: %v", bifrostErr.Error.Message)
	}

	got := captured.snapshot()
	assertRequest(t, got, http.MethodPost, "/v1/chat/completions")
	if !strings.Contains(got.body, `"model":"z-ai/glm-5.3-flash"`) {
		t.Errorf("body does not carry the model unchanged: %s", got.body)
	}
	if len(response.Choices) != 1 {
		t.Fatalf("len(Choices) = %d, want 1", len(response.Choices))
	}
}

func TestTextCompletion_SendsBearerKeyToCompletions(t *testing.T) {
	t.Parallel()
	server, captured := newJSONServer(t, http.StatusOK, `{"id":"cmpl-1","object":"text_completion","created":1,"model":"Qwen/Qwen3.8-27B","choices":[{"index":0,"text":"hi","finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)

	request := &schemas.BifrostTextCompletionRequest{
		Provider: schemas.NearAI,
		Model:    "Qwen/Qwen3.8-27B",
		Input:    &schemas.TextCompletionInput{PromptStr: schemas.Ptr("hi")},
	}
	if _, bifrostErr := newTestProvider(t, server.URL).TextCompletion(newTestContext(), newTestKey(), request); bifrostErr != nil {
		t.Fatalf("TextCompletion: %v", bifrostErr.Error.Message)
	}
	assertRequest(t, captured.snapshot(), http.MethodPost, "/v1/completions")
}

// TestResponses_GoesThroughChatCompletions pins the chat fallback: NEAR AI's own /v1/responses
// is not OpenAI wire-compatible, so a Responses request must reach /v1/chat/completions.
func TestResponses_GoesThroughChatCompletions(t *testing.T) {
	t.Parallel()
	server, captured := newJSONServer(t, http.StatusOK, chatResponseBody)

	response, bifrostErr := newTestProvider(t, server.URL).Responses(newTestContext(), newTestKey(), newResponsesRequest())
	if bifrostErr != nil {
		t.Fatalf("Responses: %v", bifrostErr.Error.Message)
	}
	got := captured.snapshot()
	assertRequest(t, got, http.MethodPost, "/v1/chat/completions")
	if !strings.Contains(got.body, `"messages"`) || strings.Contains(got.body, `"input"`) {
		t.Errorf("request body is not chat-shaped: %s", got.body)
	}
	if len(response.Output) == 0 {
		t.Fatal("Responses output is empty; the chat reply was not converted")
	}
}

func TestEmbedding_SendsBearerKeyToEmbeddings(t *testing.T) {
	t.Parallel()
	server, captured := newJSONServer(t, http.StatusOK, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"model":"Qwen/Qwen3-Embedding-0.6B","usage":{"prompt_tokens":1,"total_tokens":1}}`)

	request := &schemas.BifrostEmbeddingRequest{
		Provider: schemas.NearAI,
		Model:    "Qwen/Qwen3-Embedding-0.6B",
		Input:    schemas.EmbeddingInput{{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: schemas.Ptr("hi")}}}},
	}
	response, bifrostErr := newTestProvider(t, server.URL).Embedding(newTestContext(), newTestKey(), request)
	if bifrostErr != nil {
		t.Fatalf("Embedding: %v", bifrostErr.Error.Message)
	}
	assertRequest(t, captured.snapshot(), http.MethodPost, "/v1/embeddings")
	if len(response.Data) != 1 {
		t.Fatalf("len(Data) = %d, want 1", len(response.Data))
	}
}

func TestListModels_ReadsModelsEndpoint(t *testing.T) {
	t.Parallel()
	server, captured := newJSONServer(t, http.StatusOK, `{"object":"list","data":[{"id":"z-ai/glm-5.3-flash","object":"model","created":1,"owned_by":"nearai"},{"id":"Qwen/Qwen3-Embedding-0.6B","object":"model","created":1,"owned_by":"nearai"}]}`)

	response, bifrostErr := newTestProvider(t, server.URL).ListModels(newTestContext(), []schemas.Key{newTestKey()}, &schemas.BifrostListModelsRequest{Provider: schemas.NearAI})
	if bifrostErr != nil {
		t.Fatalf("ListModels: %v", bifrostErr.Error.Message)
	}
	assertRequest(t, captured.snapshot(), http.MethodGet, "/v1/models")
	if len(response.Data) != 2 {
		t.Fatalf("len(Data) = %d, want 2", len(response.Data))
	}
}

func TestChatCompletionStream_StreamsChatCompletions(t *testing.T) {
	t.Parallel()
	server, captured := newSSEServer(t,
		"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"z-ai/glm-5.3-flash\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hi\"},\"finish_reason\":null}]}\n\n",
		"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"z-ai/glm-5.3-flash\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1,\"total_tokens\":4}}\n\n",
		"data: [DONE]\n\n",
	)

	stream, bifrostErr := newTestProvider(t, server.URL).ChatCompletionStream(newTestContext(), passthroughPostHookRunner, nil, newTestKey(), newChatRequest())
	if bifrostErr != nil {
		t.Fatalf("ChatCompletionStream: %v", bifrostErr.Error.Message)
	}

	var text strings.Builder
	for _, chunk := range drainStream(t, stream) {
		if chunk.BifrostError != nil {
			t.Fatalf("unexpected stream error: %v", chunk.BifrostError.Error.Message)
		}
		if chunk.BifrostChatResponse == nil {
			continue
		}
		for _, choice := range chunk.BifrostChatResponse.Choices {
			if choice.ChatStreamResponseChoice != nil && choice.Delta != nil && choice.Delta.Content != nil {
				text.WriteString(*choice.Delta.Content)
			}
		}
	}

	got := captured.snapshot()
	assertRequest(t, got, http.MethodPost, "/v1/chat/completions")
	if !strings.Contains(got.body, `"stream":true`) {
		t.Errorf("request body is missing stream=true: %s", got.body)
	}
	if text.String() != "hi" {
		t.Errorf("streamed text = %q, want %q", text.String(), "hi")
	}
}

// TestResponsesStream_GoesThroughChatCompletions pins the streaming chat fallback and the
// context flag that tells the core to convert chat chunks into Responses events.
func TestResponsesStream_GoesThroughChatCompletions(t *testing.T) {
	t.Parallel()
	server, captured := newSSEServer(t,
		"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"z-ai/glm-5.3-flash\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\n",
		"data: [DONE]\n\n",
	)

	ctx := newTestContext()
	stream, bifrostErr := newTestProvider(t, server.URL).ResponsesStream(ctx, passthroughPostHookRunner, nil, newTestKey(), newResponsesRequest())
	if bifrostErr != nil {
		t.Fatalf("ResponsesStream: %v", bifrostErr.Error.Message)
	}
	for _, chunk := range drainStream(t, stream) {
		if chunk.BifrostError != nil {
			t.Fatalf("unexpected stream error: %v", chunk.BifrostError.Error.Message)
		}
	}

	got := captured.snapshot()
	assertRequest(t, got, http.MethodPost, "/v1/chat/completions")
	if !strings.Contains(got.body, `"stream":true`) {
		t.Errorf("request body is missing stream=true: %s", got.body)
	}
	if fallback, _ := ctx.Value(schemas.BifrostContextKeyIsResponsesToChatCompletionFallback).(bool); !fallback {
		t.Error("Responses-to-chat fallback flag was not set on the context")
	}
}

func TestChatCompletion_SurfacesNearAIErrorEnvelope(t *testing.T) {
	t.Parallel()
	server, _ := newJSONServer(t, http.StatusUnauthorized, `{"error":{"message":"Invalid API key","type":"invalid_api_key","param":null,"code":null}}`)

	_, bifrostErr := newTestProvider(t, server.URL).ChatCompletion(newTestContext(), newTestKey(), newChatRequest())
	if bifrostErr == nil || bifrostErr.Error == nil {
		t.Fatal("expected an error")
	}
	if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %v, want 401", bifrostErr.StatusCode)
	}
	if bifrostErr.Error.Message != "Invalid API key" {
		t.Errorf("message = %q, want %q", bifrostErr.Error.Message, "Invalid API key")
	}
}

// TestUnsupportedOperations_ReturnUnsupportedError pins the operations this provider does not
// route yet, so enabling one is a deliberate change rather than an accident.
func TestUnsupportedOperations_ReturnUnsupportedError(t *testing.T) {
	t.Parallel()
	provider := newTestProvider(t, "http://127.0.0.1:1")
	ctx, key := newTestContext(), newTestKey()

	cases := map[string]func() *schemas.BifrostError{
		"rerank": func() *schemas.BifrostError {
			_, err := provider.Rerank(ctx, key, &schemas.BifrostRerankRequest{Provider: schemas.NearAI, Model: "Qwen/Qwen3-Reranker-0.6B"})
			return err
		},
		"transcription": func() *schemas.BifrostError {
			_, err := provider.Transcription(ctx, key, &schemas.BifrostTranscriptionRequest{Provider: schemas.NearAI, Model: "openai/whisper-large-v3"})
			return err
		},
		"image generation": func() *schemas.BifrostError {
			_, err := provider.ImageGeneration(ctx, key, &schemas.BifrostImageGenerationRequest{Provider: schemas.NearAI, Model: "black-forest-labs/FLUX.2-klein-4B"})
			return err
		},
		"speech": func() *schemas.BifrostError {
			_, err := provider.Speech(ctx, key, &schemas.BifrostSpeechRequest{Provider: schemas.NearAI, Model: "any"})
			return err
		},
	}
	for name, call := range cases {
		bifrostErr := call()
		if bifrostErr == nil || bifrostErr.Error == nil {
			t.Errorf("%s: expected an unsupported operation error, got nil", name)
			continue
		}
		if bifrostErr.Error.Code == nil || *bifrostErr.Error.Code != "unsupported_operation" {
			t.Errorf("%s: error code = %v, want unsupported_operation (message %q)", name, bifrostErr.Error.Code, bifrostErr.Error.Message)
		}
	}
}

// BenchmarkChatCompletion measures the provider's own per-request overhead against a local
// server; the work is the shared OpenAI chat handler, so it should match other
// OpenAI-compatible providers such as Cerebras.
func BenchmarkChatCompletion(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, chatResponseBody)
	}))
	defer server.Close()

	provider, err := nearai.NewNearAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		b.Fatalf("NewNearAIProvider: %v", err)
	}
	key, request := newTestKey(), newChatRequest()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, bifrostErr := provider.ChatCompletion(newTestContext(), key, request); bifrostErr != nil {
			b.Fatalf("ChatCompletion: %v", bifrostErr.Error.Message)
		}
	}
}
