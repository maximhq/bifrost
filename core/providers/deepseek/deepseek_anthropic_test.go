package deepseek_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"
	deepseek "github.com/maximhq/bifrost/core/providers/deepseek"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

type testLogger struct{}

func (l testLogger) Debug(string, ...any)                   {}
func (l testLogger) Info(string, ...any)                    {}
func (l testLogger) Warn(string, ...any)                    {}
func (l testLogger) Error(string, ...any)                   {}
func (l testLogger) Fatal(string, ...any)                   {}
func (l testLogger) SetLevel(schemas.LogLevel)              {}
func (l testLogger) SetOutputType(schemas.LoggerOutputType) {}
func (l testLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

func newTestDeepSeekProvider(baseURL string) (*deepseek.DeepSeekProvider, error) {
	return deepseek.NewDeepSeekProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        baseURL,
			DefaultRequestTimeoutInSeconds: 5,
			StreamIdleTimeoutInSeconds:     5,
			MaxConnsPerHost:                1,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 1,
			BufferSize:  1,
		},
	}, testLogger{})
}

func newAnthropicResponse() string {
	return `{"id":"msg_1","type":"message","role":"assistant","model":"deepseek-chat","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
}

func TestChatCompletion_UsesAnthropicEndpoint(t *testing.T) {
	t.Parallel()

	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/anthropic/v1/messages" {
			t.Fatalf("path = %q, want /anthropic/v1/messages", r.URL.Path)
		}
		if got := r.Header.Get("x-api-key"); got != "test-api-key" {
			t.Fatalf("x-api-key = %q, want test-api-key", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("decode body: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, newAnthropicResponse())
	}))
	defer server.Close()

	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	msg := "hello"
	resp, bifrostErr := provider.ChatCompletion(ctx, schemas.Key{Value: schemas.SecretVar{Val: "test-api-key"}, UseAnthropicEndpoints: new(true)}, &schemas.BifrostChatRequest{
		Provider: schemas.DeepSeek,
		Model:    "deepseek-v4-flash",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: &msg},
		}},
	})
	if bifrostErr != nil {
		t.Fatalf("ChatCompletion: %v", bifrostErr.Error.Message)
	}
	if resp == nil || len(resp.Choices) == 0 {
		t.Fatalf("expected chat response, got %#v", resp)
	}
	if _, ok := captured["messages"]; !ok {
		t.Fatalf("outbound body missing messages: %#v", captured)
	}
}

func TestResponses_UsesAnthropicEndpointAndKeepsWebSearch(t *testing.T) {
	t.Parallel()

	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/anthropic/v1/messages" {
			t.Fatalf("path = %q, want /anthropic/v1/messages", r.URL.Path)
		}
		if got := r.Header.Get("x-api-key"); got != "test-api-key" {
			t.Fatalf("x-api-key = %q, want test-api-key", got)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("decode body: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, newAnthropicResponse())
	}))
	defer server.Close()

	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bifrostErr := provider.Responses(ctx, schemas.Key{Value: schemas.SecretVar{Val: "test-api-key"}, UseAnthropicEndpoints: new(true)}, &schemas.BifrostResponsesRequest{
		Provider: schemas.DeepSeek,
		Model:    "deepseek-v4-pro",
		Input: []schemas.ResponsesMessage{{
			Type: new(schemas.ResponsesMessageTypeMessage),
			Role: new(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{
				ContentStr: new("hello"),
			},
		}},
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeWebSearch}},
		},
	})
	if bifrostErr != nil {
		t.Fatalf("Responses: %v", bifrostErr.Error.Message)
	}
	if resp == nil || len(resp.Output) == 0 {
		t.Fatalf("expected responses payload, got %#v", resp)
	}
	tools, ok := captured["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatalf("outbound body missing tools: %#v", captured)
	}
	toolJSON, _ := json.Marshal(tools[0])
	if !json.Valid(toolJSON) || !strings.Contains(string(toolJSON), "web_search") {
		t.Fatalf("outbound tool body did not preserve web search: %s", toolJSON)
	}
}

func TestChatCompletion_DisablesThinkingForForcedToolChoice(t *testing.T) {
	t.Parallel()

	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("decode body: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, newAnthropicResponse())
	}))
	defer server.Close()

	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}

	chatTool := llmtests.GetSampleChatTool(llmtests.SampleToolTypeTime)
	if chatTool == nil {
		t.Fatal("GetSampleChatTool returned nil")
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	msg := "get the current time in UTC"
	_, bifrostErr := provider.ChatCompletion(ctx, schemas.Key{Value: schemas.SecretVar{Val: "test-api-key"}, UseAnthropicEndpoints: new(true)}, &schemas.BifrostChatRequest{
		Provider: schemas.DeepSeek,
		Model:    "deepseek-v4-flash",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: &msg},
		}},
		Params: &schemas.ChatParameters{
			Tools: []schemas.ChatTool{*chatTool},
			ToolChoice: &schemas.ChatToolChoice{
				ChatToolChoiceStruct: &schemas.ChatToolChoiceStruct{
					Type: schemas.ChatToolChoiceTypeFunction,
					Function: &schemas.ChatToolChoiceFunction{
						Name: string(llmtests.SampleToolTypeTime),
					},
				},
			},
		},
	})
	if bifrostErr != nil {
		t.Fatalf("ChatCompletion: %v", bifrostErr.Error.Message)
	}

	thinking, ok := captured["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("expected thinking block in outbound body, got %#v", captured)
	}
	if got := thinking["type"]; got != "disabled" {
		t.Fatalf("thinking.type = %v, want disabled", got)
	}
}

func TestResponses_DisablesThinkingForForcedToolChoice(t *testing.T) {
	t.Parallel()

	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("decode body: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, newAnthropicResponse())
	}))
	defer server.Close()

	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}

	responsesTool := llmtests.GetSampleResponsesTool(llmtests.SampleToolTypeTime)
	if responsesTool == nil {
		t.Fatal("GetSampleResponsesTool returned nil")
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bifrostErr := provider.Responses(ctx, schemas.Key{Value: schemas.SecretVar{Val: "test-api-key"}, UseAnthropicEndpoints: new(true)}, &schemas.BifrostResponsesRequest{
		Provider: schemas.DeepSeek,
		Model:    "deepseek-v4-pro",
		Input: []schemas.ResponsesMessage{{
			Type: new(schemas.ResponsesMessageTypeMessage),
			Role: new(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{
				ContentStr: new("get the current time in UTC"),
			},
		}},
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{*responsesTool},
			ToolChoice: &schemas.ResponsesToolChoice{
				ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{
					Type: schemas.ResponsesToolChoiceTypeFunction,
					Name: new(string(llmtests.SampleToolTypeTime)),
				},
			},
		},
	})
	if bifrostErr != nil {
		t.Fatalf("Responses: %v", bifrostErr.Error.Message)
	}

	thinking, ok := captured["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("expected thinking block in outbound body, got %#v", captured)
	}
	if got := thinking["type"]; got != "disabled" {
		t.Fatalf("thinking.type = %v, want disabled", got)
	}
}

func TestChatCompletion_DefaultsToOpenAIEndpoint(t *testing.T) {
	t.Parallel()

	var capturedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		if got := r.Header.Get("Authorization"); got != "Bearer test-api-key" {
			t.Fatalf("Authorization = %q, want Bearer test-api-key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"chatcmpl_1","object":"chat.completion","model":"deepseek-v4-flash","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer server.Close()

	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	msg := "hello"
	resp, bifrostErr := provider.ChatCompletion(ctx, schemas.Key{Value: schemas.SecretVar{Val: "test-api-key"}}, &schemas.BifrostChatRequest{
		Provider: schemas.DeepSeek,
		Model:    "deepseek-v4-flash",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: &msg},
		}},
	})
	if bifrostErr != nil {
		t.Fatalf("ChatCompletion: %v", bifrostErr.Error.Message)
	}
	if resp == nil || len(resp.Choices) == 0 {
		t.Fatalf("expected chat response, got %#v", resp)
	}
	if capturedPath != "/chat/completions" {
		t.Fatalf("path = %q, want /chat/completions", capturedPath)
	}
}

func TestChatCompletion_OpenAIEndpointDisablesThinkingForRequiredToolChoice(t *testing.T) {
	t.Parallel()

	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"chatcmpl_1","object":"chat.completion","model":"deepseek-v4-flash","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer server.Close()

	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}

	chatTool := llmtests.GetSampleChatTool(llmtests.SampleToolTypeTime)
	if chatTool == nil {
		t.Fatal("GetSampleChatTool returned nil")
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	msg := "get the current time in UTC"
	requiredChoice := string(schemas.ChatToolChoiceTypeRequired)
	_, bifrostErr := provider.ChatCompletion(ctx, schemas.Key{Value: schemas.SecretVar{Val: "test-api-key"}}, &schemas.BifrostChatRequest{
		Provider: schemas.DeepSeek,
		Model:    "deepseek-v4-flash",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: &msg},
		}},
		Params: &schemas.ChatParameters{
			Tools: []schemas.ChatTool{*chatTool},
			ToolChoice: &schemas.ChatToolChoice{
				ChatToolChoiceStr: &requiredChoice,
			},
		},
	})
	if bifrostErr != nil {
		t.Fatalf("ChatCompletion: %v", bifrostErr.Error.Message)
	}

	thinking, ok := captured["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("expected thinking block in outbound body, got %#v", captured)
	}
	if got := thinking["type"]; got != "disabled" {
		t.Fatalf("thinking.type = %v, want disabled", got)
	}
}
