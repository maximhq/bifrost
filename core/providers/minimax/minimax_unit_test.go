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
	message := "Hello"
	request := &schemas.BifrostChatRequest{
		Provider: schemas.Minimax,
		Model:    "MiniMax-M3",
		Input: []schemas.ChatMessage{
			{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: &message},
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
	case <-time.After(5 * time.Second):
		t.Fatal("streaming request body was not captured")
	}

	select {
	case <-streamDone:
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not close")
	}
}
