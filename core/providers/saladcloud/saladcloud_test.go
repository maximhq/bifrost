package saladcloud_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/saladcloud"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestSaladcloud(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("SALAD_CLOUD_API_KEY")) == "" {
		t.Skip("Skipping Salad AI Gateway tests because SALAD_CLOUD_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:       schemas.SaladCloud,
		ChatModel:      "qwen3.6-35b-a3b",
		VisionModel:    "qwen3.6-35b-a3b",
		ReasoningModel: "qwen3.6-35b-a3b",
		Scenarios: llmtests.TestScenarios{
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              true,
			CompleteEnd2End:       true,
			ListModels:            true,
			StructuredOutputs:     true,
		},
	}

	t.Run("SaladCloudTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

func TestSaladCloudListModelsOnlyReturns35B(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/models" {
			t.Errorf("unexpected path: %s", request.URL.Path)
		}
		if authorization := request.Header.Get("Authorization"); authorization != "Bearer test-key" {
			t.Errorf("unexpected authorization header: %q", authorization)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(writer, `{"object":"list","data":[{"id":"qwen3.6-35b-a3b","object":"model","owned_by":"salad"},{"id":"qwen3.6-27b","object":"model","owned_by":"salad"},{"id":"qwen3.5-9b","object":"model","owned_by":"salad"}]}`)
	}))
	defer server.Close()

	provider, err := saladcloud.NewSaladCloudProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        server.URL,
			DefaultRequestTimeoutInSeconds: 10,
			AllowPrivateNetwork:            true,
		},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		t.Fatalf("NewSaladCloudProvider: %v", err)
	}

	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	response, bifrostErr := provider.ListModels(
		ctx,
		[]schemas.Key{{
			Value:  *schemas.NewSecretVar("test-key"),
			Models: schemas.WhiteList{"*"},
		}},
		&schemas.BifrostListModelsRequest{Provider: schemas.SaladCloud, Unfiltered: true},
	)
	if bifrostErr != nil {
		t.Fatalf("ListModels: %v", bifrostErr)
	}
	if len(response.Data) != 1 {
		t.Fatalf("expected one listed model, got %d", len(response.Data))
	}
	if response.Data[0].ID != "saladcloud/qwen3.6-35b-a3b" {
		t.Fatalf("unexpected model ID: %s", response.Data[0].ID)
	}
}

func TestSaladCloudReasoningUsesChatTemplateKwargs(t *testing.T) {
	t.Parallel()

	var capturedBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&capturedBody); err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(writer, `{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"qwen3.6-35b-a3b","choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	provider := newTestSaladCloudProvider(t, server.URL)
	request := newReasoningDisabledRequest()
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	_, bifrostErr := provider.ChatCompletion(ctx, testSaladCloudKey(), request)
	if bifrostErr != nil {
		t.Fatalf("ChatCompletion: %v", bifrostErr)
	}

	assertThinkingDisabledPayload(t, capturedBody)
	if request.Params.Reasoning == nil {
		t.Fatal("ChatCompletion mutated the caller's reasoning parameters")
	}
}

func TestSaladCloudStreamingReasoningUsesChatTemplateKwargs(t *testing.T) {
	t.Parallel()

	bodyCh := make(chan map[string]interface{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]interface{}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		bodyCh <- body
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.Header().Set("Cache-Control", "no-cache")
		_, _ = io.WriteString(writer, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider := newTestSaladCloudProvider(t, server.URL)
	request := newReasoningDisabledRequest()
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	stream, bifrostErr := provider.ChatCompletionStream(ctx, passthroughPostHook, nil, testSaladCloudKey(), request)
	if bifrostErr != nil {
		t.Fatalf("ChatCompletionStream: %v", bifrostErr)
	}
	if stream != nil {
		go func() {
			for range stream {
			}
		}()
	}

	select {
	case body := <-bodyCh:
		assertThinkingDisabledPayload(t, body)
	case <-time.After(5 * time.Second):
		t.Fatal("mock server did not receive the streaming request")
	}
	if request.Params.Reasoning == nil {
		t.Fatal("ChatCompletionStream mutated the caller's reasoning parameters")
	}
}

func newTestSaladCloudProvider(t *testing.T, baseURL string) *saladcloud.SaladCloudProvider {
	t.Helper()
	provider, err := saladcloud.NewSaladCloudProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        baseURL,
			DefaultRequestTimeoutInSeconds: 10,
			AllowPrivateNetwork:            true,
		},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		t.Fatalf("NewSaladCloudProvider: %v", err)
	}
	return provider
}

func newReasoningDisabledRequest() *schemas.BifrostChatRequest {
	content := "Hello"
	effort := "none"
	return &schemas.BifrostChatRequest{
		Provider: schemas.SaladCloud,
		Model:    "qwen3.6-35b-a3b",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: &content},
		}},
		Params: &schemas.ChatParameters{
			Reasoning: &schemas.ChatReasoning{Effort: &effort},
		},
	}
}

func testSaladCloudKey() schemas.Key {
	return schemas.Key{Value: *schemas.NewSecretVar("test-key")}
}

func assertThinkingDisabledPayload(t *testing.T, body map[string]interface{}) {
	t.Helper()
	if _, exists := body["reasoning_effort"]; exists {
		t.Fatalf("unexpected reasoning_effort in Salad request: %#v", body)
	}
	kwargs, ok := body[saladCloudChatTemplateKwargsKeyForTest].(map[string]interface{})
	if !ok {
		t.Fatalf("expected chat_template_kwargs object in Salad request: %#v", body)
	}
	if enabled, ok := kwargs["enable_thinking"].(bool); !ok || enabled {
		t.Fatalf("expected enable_thinking=false, got %#v", kwargs["enable_thinking"])
	}
}

func passthroughPostHook(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return result, err
}

const saladCloudChatTemplateKwargsKeyForTest = "chat_template_kwargs"
