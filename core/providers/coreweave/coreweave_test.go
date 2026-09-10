package coreweave_test

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
	"github.com/maximhq/bifrost/core/providers/coreweave"
	"github.com/maximhq/bifrost/core/schemas"
)

// TestCoreWeave runs the shared live scenario suite against W&B Inference.
func TestCoreWeave(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("COREWEAVE_API_KEY")) == "" {
		t.Skip("Skipping CoreWeave tests because COREWEAVE_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	// Model IDs come from GET /v1/models. The multi-tool scenarios need a chat
	// model that calls two tools in one turn; gpt-oss and Llama 3.3 call them
	// one at a time.
	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:  schemas.CoreWeave,
		ChatModel: "Qwen/Qwen3-235B-A22B-Instruct-2507",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.CoreWeave, Model: "deepseek-ai/DeepSeek-V4-Flash"},
		},
		TextModel:      "meta-llama/Llama-3.1-8B-Instruct",
		EmbeddingModel: "", // W&B Inference has no embeddings endpoint
		ReasoningModel: "openai/gpt-oss-120b",
		VisionModel:    "google/gemma-4-31B-it",
		Scenarios: llmtests.TestScenarios{
			TextCompletion:             true,
			TextCompletionStream:       true,
			SimpleChat:                 true,
			CompletionStream:           true,
			MultiTurnConversation:      true,
			ToolCalls:                  true,
			ToolCallsStreaming:         true,
			MultipleToolCalls:          true,
			MultipleToolCallsStreaming: true,
			End2EndToolCalling:         true,
			AutomaticFunctionCall:      true,
			ImageURL:                   true,
			ImageBase64:                true,
			MultipleImages:             true,
			CompleteEnd2End:            true,
			Embedding:                  false,
			ListModels:                 true,
			Reasoning:                  true,
		},
	}

	t.Run("CoreWeaveTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

func newTestProvider(t *testing.T, baseURL string) *coreweave.CoreWeaveProvider {
	t.Helper()
	provider, err := coreweave.NewCoreWeaveProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        baseURL,
			DefaultRequestTimeoutInSeconds: 10,
		},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		t.Fatalf("NewCoreWeaveProvider: %v", err)
	}
	return provider
}

// TestCoreWeaveDefaults pins the default base URL and provider key.
func TestCoreWeaveDefaults(t *testing.T) {
	t.Parallel()

	config := &schemas.ProviderConfig{}
	provider, err := coreweave.NewCoreWeaveProvider(config, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		t.Fatalf("NewCoreWeaveProvider: %v", err)
	}
	if provider.GetProviderKey() != schemas.CoreWeave {
		t.Errorf("GetProviderKey: got %q, want %q", provider.GetProviderKey(), schemas.CoreWeave)
	}
	if config.NetworkConfig.BaseURL != coreweave.DefaultBaseURL {
		t.Errorf("default BaseURL: got %q, want %q", config.NetworkConfig.BaseURL, coreweave.DefaultBaseURL)
	}

	trailing := &schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: "https://example.test/v1///"}}
	if _, err := coreweave.NewCoreWeaveProvider(trailing, bifrost.NewDefaultLogger(schemas.LogLevelError)); err != nil {
		t.Fatalf("NewCoreWeaveProvider: %v", err)
	}
	if trailing.NetworkConfig.BaseURL != "https://example.test/v1" {
		t.Errorf("trailing slashes: got %q", trailing.NetworkConfig.BaseURL)
	}
}

// TestCoreWeaveChatCompletionWire checks the chat path, bearer auth, extra-param
// passthrough, and that vLLM's message-level "reasoning" string maps to reasoning.
func TestCoreWeaveChatCompletionWire(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var gotPath, gotAuth string
	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.Unmarshal(body, &gotBody)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-1", "object": "chat.completion", "created": 1788466719,
			"model": "openai/gpt-oss-120b",
			"choices": [{"index": 0, "finish_reason": "stop", "logprobs": null,
				"message": {"role": "assistant", "content": "ok", "reasoning": "Need to reply ok.", "tool_calls": []}}],
			"usage": {"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7},
			"system_fingerprint": "vllm-0.22.1", "prompt_token_ids": null, "kv_transfer_params": null
		}`))
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL+"/v1")
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	key := schemas.Key{Models: []string{"*"}, Value: *schemas.NewSecretVar("test-key")}
	prompt := "Reply with ok."
	response, bifrostErr := provider.ChatCompletion(ctx, key, &schemas.BifrostChatRequest{
		Provider: schemas.CoreWeave,
		Model:    "openai/gpt-oss-120b",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: &prompt},
		}},
		Params: &schemas.ChatParameters{
			MaxCompletionTokens: schemas.Ptr(20),
			ExtraParams:         map[string]any{"top_k": 5},
		},
	})
	if bifrostErr != nil {
		t.Fatalf("ChatCompletion returned an error: %v", bifrostErr.Error.Message)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/v1/chat/completions" {
		t.Errorf("path: got %q, want %q", gotPath, "/v1/chat/completions")
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization: got %q, want %q", gotAuth, "Bearer test-key")
	}
	if gotBody["model"] != "openai/gpt-oss-120b" {
		t.Errorf("model on the wire: got %v", gotBody["model"])
	}
	if topK, ok := gotBody["top_k"].(float64); !ok || topK != 5 {
		t.Errorf("extra param top_k did not pass through: body=%v", gotBody)
	}
	if len(response.Choices) != 1 || response.Choices[0].ChatNonStreamResponseChoice == nil {
		t.Fatalf("choices: got %+v", response.Choices)
	}
	msg := response.Choices[0].ChatNonStreamResponseChoice.Message
	if msg.Content == nil || msg.Content.ContentStr == nil || *msg.Content.ContentStr != "ok" {
		t.Errorf("content: got %+v", msg.Content)
	}
	if msg.ChatAssistantMessage == nil || msg.ChatAssistantMessage.Reasoning == nil || *msg.ChatAssistantMessage.Reasoning != "Need to reply ok." {
		t.Errorf("reasoning: got %+v, want the gateway's message.reasoning string", msg.ChatAssistantMessage)
	}
	if response.Usage == nil || response.Usage.TotalTokens != 7 {
		t.Errorf("usage: got %+v", response.Usage)
	}
}

// TestCoreWeaveResponsesUsesNativeEndpoint pins that non-streaming Responses hit
// /responses rather than chat completions, and that extra params reach the wire.
func TestCoreWeaveResponsesUsesNativeEndpoint(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var gotPath string
	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotPath = r.URL.Path
		_ = json.Unmarshal(body, &gotBody)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "resp_1", "object": "response", "created_at": 1788466733, "status": "completed",
			"model": "meta-llama/Llama-3.1-8B-Instruct",
			"output": [{"type": "message", "id": "msg_1", "role": "assistant", "status": "completed",
				"content": [{"type": "output_text", "text": "hi", "annotations": []}]}],
			"usage": {"input_tokens": 3, "output_tokens": 1, "total_tokens": 4}
		}`))
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL+"/v1")
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	role := schemas.ResponsesInputMessageRoleUser
	prompt := "hi"
	response, bifrostErr := provider.Responses(ctx, schemas.Key{Models: []string{"*"}, Value: *schemas.NewSecretVar("test-key")}, &schemas.BifrostResponsesRequest{
		Provider: schemas.CoreWeave,
		Model:    "meta-llama/Llama-3.1-8B-Instruct",
		Input: []schemas.ResponsesMessage{{
			Role:    &role,
			Content: &schemas.ResponsesMessageContent{ContentStr: &prompt},
		}},
		Params: &schemas.ResponsesParameters{
			ExtraParams: map[string]any{"top_k": 5},
		},
	})
	if bifrostErr != nil {
		t.Fatalf("Responses returned an error: %v", bifrostErr.Error.Message)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/v1/responses" {
		t.Errorf("path: got %q, want %q", gotPath, "/v1/responses")
	}
	if topK, ok := gotBody["top_k"].(float64); !ok || topK != 5 {
		t.Errorf("extra param top_k did not pass through: body=%v", gotBody)
	}
	if response.ID == nil || *response.ID != "resp_1" {
		t.Errorf("ID: got %v", response.ID)
	}
	if len(response.Output) != 1 {
		t.Errorf("output items: got %d, want 1", len(response.Output))
	}
}

// TestCoreWeaveResponsesStreamUsesChatCompletions pins that streaming Responses
// hit /chat/completions; the gateway's /responses stream never emits
// function_call events.
func TestCoreWeaveResponsesStreamUsesChatCompletions(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var gotPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath = r.URL.Path
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1788466719,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hi\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1788466719,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL+"/v1")
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	role := schemas.ResponsesInputMessageRoleUser
	prompt := "hi"
	postHook := func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return result, err
	}
	stream, bifrostErr := provider.ResponsesStream(ctx, postHook, nil, schemas.Key{Models: []string{"*"}, Value: *schemas.NewSecretVar("test-key")}, &schemas.BifrostResponsesRequest{
		Provider: schemas.CoreWeave,
		Model:    "m",
		Input: []schemas.ResponsesMessage{{
			Role:    &role,
			Content: &schemas.ResponsesMessageContent{ContentStr: &prompt},
		}},
	})
	if bifrostErr != nil {
		t.Fatalf("ResponsesStream returned an error: %v", bifrostErr.Error.Message)
	}
	chunks := 0
	for range stream {
		chunks++
	}
	if chunks == 0 {
		t.Error("expected at least one stream chunk")
	}

	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/v1/chat/completions" {
		t.Errorf("path: got %q, want %q (streaming Responses must ride chat completions)", gotPath, "/v1/chat/completions")
	}
}

// TestCoreWeaveErrorShapes covers the three error envelopes the gateway emits.
func TestCoreWeaveErrorShapes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		status      int
		body        string
		wantMessage string
		wantCode    string
		wantType    string
	}{
		{
			name:        "wrapped openai envelope from auth",
			status:      http.StatusUnauthorized,
			body:        `{"error":{"code":"invalid_api_key","message":"Invalid Authentication","type":"invalid_request_error"}}`,
			wantMessage: "Invalid Authentication",
			wantCode:    "invalid_api_key",
			wantType:    "invalid_request_error",
		},
		{
			name:        "flat envelope with string code from routing",
			status:      http.StatusNotFound,
			body:        `{"code": "", "type": "invalid_request_error", "message": "The requested resource was not found"}`,
			wantMessage: "The requested resource was not found",
			wantCode:    "",
			wantType:    "invalid_request_error",
		},
		{
			name:        "flat envelope with numeric code from the edge",
			status:      http.StatusForbidden,
			body:        `{"message":"Forbidden","type":"Forbidden","code":403}`,
			wantMessage: "Forbidden",
			wantCode:    "403",
			wantType:    "Forbidden",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			provider := newTestProvider(t, server.URL+"/v1")
			ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			// Chat completions use ParseCoreWeaveError; list-models has no error
			// hook and stays on the OpenAI parser.
			prompt := "hi"
			_, bifrostErr := provider.ChatCompletion(ctx, schemas.Key{Models: []string{"*"}, Value: *schemas.NewSecretVar("test-key")}, &schemas.BifrostChatRequest{
				Provider: schemas.CoreWeave,
				Model:    "nope/nope",
				Input: []schemas.ChatMessage{{
					Role:    schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{ContentStr: &prompt},
				}},
			})
			if bifrostErr == nil {
				t.Fatal("expected an error")
			}
			if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != tt.status {
				t.Errorf("status: got %v, want %d", bifrostErr.StatusCode, tt.status)
			}
			if bifrostErr.Error == nil {
				t.Fatal("Error field is nil")
			}
			if bifrostErr.Error.Message != tt.wantMessage {
				t.Errorf("message: got %q, want %q", bifrostErr.Error.Message, tt.wantMessage)
			}
			gotCode := ""
			if bifrostErr.Error.Code != nil {
				gotCode = *bifrostErr.Error.Code
			}
			if gotCode != tt.wantCode {
				t.Errorf("code: got %q, want %q", gotCode, tt.wantCode)
			}
			gotType := ""
			if bifrostErr.Error.Type != nil {
				gotType = *bifrostErr.Error.Type
			}
			if gotType != tt.wantType {
				t.Errorf("type: got %q, want %q", gotType, tt.wantType)
			}
		})
	}
}

// TestCoreWeaveUnsupportedOperations pins that unsupported operations fail
// locally and never reach the wire.
func TestCoreWeaveUnsupportedOperations(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("unsupported operation reached the wire")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL+"/v1")
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	key := schemas.Key{Models: []string{"*"}, Value: *schemas.NewSecretVar("test-key")}

	_, embeddingErr := provider.Embedding(ctx, key, &schemas.BifrostEmbeddingRequest{Provider: schemas.CoreWeave, Model: "m"})
	_, speechErr := provider.Speech(ctx, key, &schemas.BifrostSpeechRequest{Provider: schemas.CoreWeave, Model: "m"})
	_, fileErr := provider.FileUpload(ctx, key, &schemas.BifrostFileUploadRequest{Provider: schemas.CoreWeave})

	for name, bifrostErr := range map[string]*schemas.BifrostError{"Embedding": embeddingErr, "Speech": speechErr, "FileUpload": fileErr} {
		if bifrostErr == nil || bifrostErr.Error == nil || bifrostErr.Error.Code == nil || *bifrostErr.Error.Code != "unsupported_operation" {
			t.Errorf("%s: got %+v, want unsupported_operation", name, bifrostErr)
		}
	}
}
