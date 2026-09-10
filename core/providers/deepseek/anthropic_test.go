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
	// Handlers run on their own goroutine, so they assert with t.Errorf and
	// return an error status rather than t.Fatalf: FailNow is only legal from
	// the goroutine running the test, and from a handler it aborts the response
	// mid-write, surfacing an opaque transport error instead of this message.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/anthropic/v1/messages" {
			t.Errorf("path = %q, want /anthropic/v1/messages", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusBadRequest)
			return
		}
		if got := r.Header.Get("x-api-key"); got != "test-api-key" {
			t.Errorf("x-api-key = %q, want test-api-key", got)
			http.Error(w, "unexpected api key", http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			http.Error(w, "read body", http.StatusInternalServerError)
			return
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Errorf("decode body: %v", err)
			http.Error(w, "decode body", http.StatusInternalServerError)
			return
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
			t.Errorf("path = %q, want /anthropic/v1/messages", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusBadRequest)
			return
		}
		if got := r.Header.Get("x-api-key"); got != "test-api-key" {
			t.Errorf("x-api-key = %q, want test-api-key", got)
			http.Error(w, "unexpected api key", http.StatusBadRequest)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			http.Error(w, "read body", http.StatusInternalServerError)
			return
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Errorf("decode body: %v", err)
			http.Error(w, "decode body", http.StatusInternalServerError)
			return
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

// captureAnthropicMountBody drives a request through DeepSeek's Anthropic-compatible
// mount and returns the decoded outbound wire body.
func captureAnthropicMountBody(t *testing.T, request *schemas.BifrostChatRequest) map[string]any {
	t.Helper()

	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/anthropic/v1/messages" {
			t.Errorf("path = %q, want /anthropic/v1/messages", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			http.Error(w, "read body", http.StatusInternalServerError)
			return
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Errorf("decode body: %v", err)
			http.Error(w, "decode body", http.StatusInternalServerError)
			return
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
	if _, bifrostErr := provider.ChatCompletion(ctx, schemas.Key{Value: schemas.SecretVar{Val: "test-api-key"}, UseAnthropicEndpoints: new(true)}, request); bifrostErr != nil {
		t.Fatalf("ChatCompletion: %v", bifrostErr.Error.Message)
	}
	return captured
}

// TestChatCompletion_AnthropicEndpointForwardsEffort pins the wire shape for
// reasoning-effort control on DeepSeek's Anthropic mount. DeepSeek documents
// output_config.effort as the effort control and thinking.budget_tokens as
// ignored (https://api-docs.deepseek.com/guides/anthropic_api), so an effort
// request must carry output_config.effort verbatim — and must not dress the
// intent up as a synthesized thinking.budget_tokens, the field this incident
// showed silently carrying no meaning upstream. Thinking stays absent: DeepSeek
// enables thinking by default, so effort alone is the canonical request shape.
func TestChatCompletion_AnthropicEndpointForwardsEffort(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		effort    string
		wantWire  string
		wantModel string
	}{
		{"max", "max", "max", "deepseek-v4-flash"},
		{"high", "high", "high", "deepseek-v4-pro"},
		// minimal is remapped to low before it reaches output_config
		// (MapBifrostEffortToAnthropic); low is a documented DeepSeek tier.
		{"minimal_maps_to_low", "minimal", "low", "deepseek-v4-flash"},
		// Non-v4 model names: the mount aliases them to deepseek-v4-flash
		// upstream (same effort mapping), so effort forwards for them too.
		// "deepseek-flash" is the production model name from the incident
		// that exposed the v4-only scope gap.
		{"non_v4_model_name", "max", "max", "deepseek-flash"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			captured := captureAnthropicMountBody(t, &schemas.BifrostChatRequest{
				Provider: schemas.DeepSeek,
				Model:    tc.wantModel,
				Input: []schemas.ChatMessage{{
					Role:    schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{ContentStr: new("What is 17 * 23?")},
				}},
				Params: &schemas.ChatParameters{
					MaxCompletionTokens: new(128000),
					Reasoning:           &schemas.ChatReasoning{Effort: new(tc.effort)},
				},
			})

			outputConfig, ok := captured["output_config"].(map[string]any)
			if !ok {
				t.Fatalf("outbound body missing output_config: %#v", captured)
			}
			if got := outputConfig["effort"]; got != tc.wantWire {
				t.Fatalf("output_config.effort = %v, want %q", got, tc.wantWire)
			}
			if thinking, ok := captured["thinking"]; ok {
				t.Fatalf("thinking must be absent when the caller only asked for effort "+
					"(DeepSeek defaults thinking on and ignores budget_tokens), got %#v", thinking)
			}
		})
	}
}

// TestChatCompletion_AnthropicEndpointKeepsCallerBudgetWithEffort pins the
// co-existence shape: a caller who explicitly supplies a reasoning budget
// alongside an effort keeps their thinking field verbatim. The budget is
// documented-ignored by DeepSeek, but it is the caller's own field — the
// gateway only stops inventing budgets, it does not rewrite explicit ones.
func TestChatCompletion_AnthropicEndpointKeepsCallerBudgetWithEffort(t *testing.T) {
	t.Parallel()

	captured := captureAnthropicMountBody(t, &schemas.BifrostChatRequest{
		Provider: schemas.DeepSeek,
		Model:    "deepseek-v4-flash",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: new("What is 17 * 23?")},
		}},
		Params: &schemas.ChatParameters{
			MaxCompletionTokens: new(128000),
			Reasoning: &schemas.ChatReasoning{
				Effort:    new("max"),
				MaxTokens: new(60000),
			},
		},
	})

	outputConfig, ok := captured["output_config"].(map[string]any)
	if !ok {
		t.Fatalf("outbound body missing output_config: %#v", captured)
	}
	if got := outputConfig["effort"]; got != "max" {
		t.Fatalf("output_config.effort = %v, want max", got)
	}
	thinking, ok := captured["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("outbound body missing caller-supplied thinking: %#v", captured)
	}
	if got := thinking["budget_tokens"]; got != float64(60000) {
		t.Fatalf("thinking.budget_tokens = %v, want 60000 (caller's explicit budget, verbatim)", got)
	}
}

func TestChatCompletion_DisablesThinkingForForcedToolChoice(t *testing.T) {
	t.Parallel()

	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			http.Error(w, "read body", http.StatusInternalServerError)
			return
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Errorf("decode body: %v", err)
			http.Error(w, "decode body", http.StatusInternalServerError)
			return
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
			t.Errorf("read body: %v", err)
			http.Error(w, "read body", http.StatusInternalServerError)
			return
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Errorf("decode body: %v", err)
			http.Error(w, "decode body", http.StatusInternalServerError)
			return
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
			t.Errorf("Authorization = %q, want Bearer test-api-key", got)
			http.Error(w, "unexpected authorization", http.StatusBadRequest)
			return
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
			t.Errorf("read body: %v", err)
			http.Error(w, "read body", http.StatusInternalServerError)
			return
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Errorf("decode body: %v", err)
			http.Error(w, "decode body", http.StatusInternalServerError)
			return
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

// TestChatCompletion_AliasOverrideUsesAnthropicEndpoint pins the alias branch of
// ResolveUseAnthropicEndpoints: a key with no use_anthropic_endpoints of its own
// still routes through /anthropic/v1/messages when the resolved alias carries the
// flag. This is the shape the provider harness relies on (#7610): the shared
// DeepSeek key stays on the OpenAI-compatible wire and only the *-anthropic
// aliases opt into the Anthropic-compatible endpoint.
func TestChatCompletion_AliasOverrideUsesAnthropicEndpoint(t *testing.T) {
	t.Parallel()

	var gotPath, gotAPIKey, gotAuthorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAPIKey = r.Header.Get("x-api-key")
		gotAuthorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, newAnthropicResponse())
	}))
	defer server.Close()

	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{
		Key:    "deepseek-v4-flash-anthropic",
		Config: &schemas.AliasConfig{ModelID: "deepseek-v4-flash", UseAnthropicEndpoints: new(true)},
	})
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
	if gotPath != "/anthropic/v1/messages" {
		t.Fatalf("path = %q, want /anthropic/v1/messages (alias-level use_anthropic_endpoints was ignored)", gotPath)
	}
	if gotAPIKey != "test-api-key" {
		t.Fatalf("x-api-key = %q, want test-api-key", gotAPIKey)
	}
	if gotAuthorization != "" {
		t.Fatalf("Authorization = %q, want empty (bearer auth belongs to the OpenAI-compatible wire)", gotAuthorization)
	}
}
