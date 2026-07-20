package minimax

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

type testLogger struct{}

func (testLogger) Debug(string, ...any)                   {}
func (testLogger) Info(string, ...any)                    {}
func (testLogger) Warn(string, ...any)                    {}
func (testLogger) Error(string, ...any)                   {}
func (testLogger) Fatal(string, ...any)                   {}
func (testLogger) SetLevel(schemas.LogLevel)              {}
func (testLogger) SetOutputType(schemas.LoggerOutputType) {}
func (testLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

func testProvider(baseURL string) *MinimaxProvider {
	client := &fasthttp.Client{
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}
	return &MinimaxProvider{
		client:          client,
		streamingClient: client,
		networkConfig: schemas.NetworkConfig{
			BaseURL:                    baseURL,
			StreamIdleTimeoutInSeconds: 5,
		},
		logger: testLogger{},
	}
}

func passThroughPostHook(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return result, err
}

func TestChatCompletionStreamUsesContextPathAndExtraParams(t *testing.T) {
	t.Parallel()

	pathCh := make(chan string, 1)
	bodyCh := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read request", http.StatusInternalServerError)
			return
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, "failed to decode request", http.StatusBadRequest)
			return
		}
		pathCh <- r.URL.Path
		bodyCh <- payload

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyURLPath, "/custom/chat")
	var content schemas.ChatMessageContent
	if err := json.Unmarshal([]byte(`[
		{"type":"text","text":"Describe this media."},
		{"type":"image_url","image_url":{"url":"https://example.com/image.png","detail":"default","max_long_side_pixel":2048}},
		{"type":"video_url","video_url":{"url":"mm_file://file_id","detail":"default","fps":1,"max_long_side_pixel":1920}}
	]`), &content); err != nil {
		t.Fatalf("failed to decode multimodal content: %v", err)
	}
	request := &schemas.BifrostChatRequest{
		Provider: schemas.Minimax,
		Model:    "MiniMax-M3",
		Input: []schemas.ChatMessage{
			{
				Role:    schemas.ChatMessageRoleUser,
				Content: &content,
			},
		},
		Params: &schemas.ChatParameters{
			ExtraParams: map[string]any{
				"thinking": map[string]any{"type": "disabled"},
			},
		},
	}
	key := schemas.Key{Value: schemas.SecretVar{Val: "test-key"}}

	stream, bifrostErr := testProvider(server.URL).ChatCompletionStream(ctx, passThroughPostHook, nil, key, request)
	if bifrostErr != nil {
		t.Fatalf("ChatCompletionStream returned error: %v", bifrostErr.Error.Message)
	}
	if stream == nil {
		t.Fatal("ChatCompletionStream returned a nil stream")
	}
	streamDone := make(chan struct{})
	go func() {
		for range stream {
		}
		close(streamDone)
	}()

	select {
	case path := <-pathCh:
		if path != "/custom/chat" {
			t.Fatalf("expected context path %q, got %q", "/custom/chat", path)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("streaming request was not received")
	}

	select {
	case payload := <-bodyCh:
		thinking, ok := payload["thinking"].(map[string]any)
		if !ok || thinking["type"] != "disabled" {
			t.Fatalf("expected thinking parameters in request body, got %#v", payload["thinking"])
		}
		messages, ok := payload["messages"].([]any)
		if !ok || len(messages) != 1 {
			t.Fatalf("expected one message in request body, got %#v", payload["messages"])
		}
		message, ok := messages[0].(map[string]any)
		if !ok {
			t.Fatalf("expected message object, got %#v", messages[0])
		}
		contentBlocks, ok := message["content"].([]any)
		if !ok || len(contentBlocks) != 3 {
			t.Fatalf("expected three content blocks, got %#v", message["content"])
		}
		imageBlock, ok := contentBlocks[1].(map[string]any)
		if !ok {
			t.Fatalf("expected image content block, got %#v", contentBlocks[1])
		}
		imageURL, ok := imageBlock["image_url"].(map[string]any)
		if !ok || imageURL["url"] != "https://example.com/image.png" || imageURL["detail"] != "default" || imageURL["max_long_side_pixel"] != float64(2048) {
			t.Fatalf("expected complete image content, got %#v", imageBlock)
		}
		videoBlock, ok := contentBlocks[2].(map[string]any)
		if !ok {
			t.Fatalf("expected video content block, got %#v", contentBlocks[2])
		}
		videoURL, ok := videoBlock["video_url"].(map[string]any)
		if !ok || videoURL["url"] != "mm_file://file_id" || videoURL["detail"] != "default" || videoURL["fps"] != float64(1) || videoURL["max_long_side_pixel"] != float64(1920) {
			t.Fatalf("expected complete video content, got %#v", videoBlock)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("streaming request body was not captured")
	}

	select {
	case <-streamDone:
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not close")
	}
}

func TestResponsesFallbackUsesChatCompletions(t *testing.T) {
	t.Parallel()

	pathCh := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pathCh <- r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"MiniMax-M3","choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	var input []schemas.ResponsesMessage
	if err := json.Unmarshal([]byte(`[{"role":"user","content":"Hello"}]`), &input); err != nil {
		t.Fatalf("failed to decode responses input: %v", err)
	}
	request := &schemas.BifrostResponsesRequest{
		Provider: schemas.Minimax,
		Model:    "MiniMax-M3",
		Input:    input,
	}
	baseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	deadline, _ := baseCtx.Deadline()
	ctx := schemas.NewBifrostContext(baseCtx, deadline)
	key := schemas.Key{Value: schemas.SecretVar{Val: "test-key"}}

	response, bifrostErr := testProvider(server.URL).Responses(ctx, key, request)
	if bifrostErr != nil {
		t.Fatalf("Responses returned error: %v", bifrostErr.Error.Message)
	}
	if response == nil || len(response.Output) == 0 {
		t.Fatalf("expected a converted Responses output, got %#v", response)
	}

	select {
	case path := <-pathCh:
		if path != "/v1/chat/completions" {
			t.Fatalf("expected Responses fallback path %q, got %q", "/v1/chat/completions", path)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Chat Completions fallback request was not received")
	}
}
