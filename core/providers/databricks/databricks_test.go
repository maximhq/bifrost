package databricks_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/databricks"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestDatabricks(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("DATABRICKS_TOKEN")) == "" || strings.TrimSpace(os.Getenv("DATABRICKS_WORKSPACE_URL")) == "" {
		t.Skip("Skipping Databricks tests because DATABRICKS_TOKEN or DATABRICKS_WORKSPACE_URL is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	// Model Serving endpoint names. Availability varies by workspace region, so override
	// with a model the target workspace actually serves when these are unavailable.
	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:  schemas.Databricks,
		ChatModel: "databricks-claude-sonnet-4-5",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Databricks, Model: "databricks-claude-sonnet-4-5"},
			{Provider: schemas.Databricks, Model: "databricks-gpt-oss-120b"},
		},
		EmbeddingModel: "databricks-gte-large-en",
		ReasoningModel: "databricks-claude-sonnet-4-5",
		VisionModel:    "databricks-claude-sonnet-4-5",
		Scenarios: llmtests.TestScenarios{
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              true,
			ImageBase64:           true,
			CompleteEnd2End:       true,
			Embedding:             true,
			ListModels:            true,
			Reasoning:             true,
			// Text completions are not exposed by either Databricks surface.
			TextCompletion:       false,
			TextCompletionStream: false,
		},
	}

	t.Run("DatabricksTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

// newStubProvider returns a Databricks provider pointed at a TLS test server, along with a
// key whose workspace URL is that server's host. Databricks is always https, so the stub has
// to be a TLS server with verification disabled.
func newStubProvider(t *testing.T, handler http.HandlerFunc) (*databricks.DatabricksProvider, *httptest.Server) {
	t.Helper()

	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)

	provider, err := databricks.NewDatabricksProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 10,
			InsecureSkipVerify:             true,
			AllowPrivateNetwork:            true,
		},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		t.Fatalf("NewDatabricksProvider: %v", err)
	}
	return provider, server
}

func serverHost(t *testing.T, server *httptest.Server) string {
	t.Helper()
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	return u.Host
}

const stubChatResponse = `{
	"id": "chatcmpl-1",
	"object": "chat.completion",
	"created": 1700000000,
	"model": "m",
	"choices": [{"index": 0, "message": {"role": "assistant", "content": "ok"}, "finish_reason": "stop"}],
	"usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
}`

// TestDatabricksSurfaceRouting pins the base path each surface is addressed on, and the rule
// that picks between them. A dotted model name is a Unity Catalog model service and must go
// to the AI Gateway; a bare name is a Model Serving endpoint. Getting this wrong sends every
// request to a 404 on the wrong surface.
func TestDatabricksSurfaceRouting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		model     string
		apiFormat schemas.DatabricksAPIFormat
		wantPath  string
	}{
		{"auto routes a serving endpoint name to model serving", "databricks-claude-sonnet-4-5", "", "/serving-endpoints/chat/completions"},
		{"auto routes a system.ai name to the ai gateway", "system.ai.claude-sonnet-4-5", "", "/ai-gateway/mlflow/v1/chat/completions"},
		{"auto routes a unity catalog fqn to the ai gateway", "main.default.my-service", "", "/ai-gateway/mlflow/v1/chat/completions"},
		{"explicit model_serving overrides a dotted name", "system.ai.claude-sonnet-4-5", schemas.DatabricksAPIFormatModelServing, "/serving-endpoints/chat/completions"},
		{"explicit ai_gateway overrides a bare name", "databricks-claude-sonnet-4-5", schemas.DatabricksAPIFormatAIGateway, "/ai-gateway/mlflow/v1/chat/completions"},
		{"explicit auto falls back to the name rule", "databricks-gpt-oss-120b", schemas.DatabricksAPIFormatAuto, "/serving-endpoints/chat/completions"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var mu sync.Mutex
			var gotPath string

			provider, server := newStubProvider(t, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				gotPath = r.URL.Path
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(stubChatResponse))
			})

			key := schemas.Key{
				Models: []string{"*"},
				Value:  *schemas.NewSecretVar("dapi-test"),
				DatabricksKeyConfig: &schemas.DatabricksKeyConfig{
					WorkspaceURL: *schemas.NewSecretVar(serverHost(t, server)),
					APIFormat:    tt.apiFormat,
				},
			}

			ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			if _, bErr := provider.ChatCompletion(ctx, key, &schemas.BifrostChatRequest{
				Provider: schemas.Databricks,
				Model:    tt.model,
				Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}}},
			}); bErr != nil {
				t.Fatalf("ChatCompletion returned an error: %v", bErr)
			}

			mu.Lock()
			defer mu.Unlock()
			if gotPath != tt.wantPath {
				t.Errorf("path: got %q, want %q", gotPath, tt.wantPath)
			}
		})
	}
}

// TestDatabricksWorkspaceURLAcceptsScheme covers a workspace URL pasted straight out of the
// Databricks console, which carries a scheme and often a trailing slash.
func TestDatabricksWorkspaceURLAcceptsScheme(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var gotPath string

	provider, server := newStubProvider(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath = r.URL.Path
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(stubChatResponse))
	})

	key := schemas.Key{
		Models: []string{"*"},
		Value:  *schemas.NewSecretVar("dapi-test"),
		DatabricksKeyConfig: &schemas.DatabricksKeyConfig{
			WorkspaceURL: *schemas.NewSecretVar("https://" + serverHost(t, server) + "/"),
		},
	}

	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, bErr := provider.ChatCompletion(ctx, key, &schemas.BifrostChatRequest{
		Provider: schemas.Databricks,
		Model:    "databricks-gpt-oss-120b",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}}},
	}); bErr != nil {
		t.Fatalf("ChatCompletion returned an error: %v", bErr)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/serving-endpoints/chat/completions" {
		t.Errorf("path: got %q, want %q (a pasted scheme and trailing slash must be stripped)", gotPath, "/serving-endpoints/chat/completions")
	}
}

// TestDatabricksPATAuth pins that a key value is sent as a bearer token.
func TestDatabricksPATAuth(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var gotAuth string

	provider, server := newStubProvider(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotAuth = r.Header.Get("Authorization")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(stubChatResponse))
	})

	key := schemas.Key{
		Models: []string{"*"},
		Value:  *schemas.NewSecretVar("dapi-secret-token"),
		DatabricksKeyConfig: &schemas.DatabricksKeyConfig{
			WorkspaceURL: *schemas.NewSecretVar(serverHost(t, server)),
		},
	}

	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, bErr := provider.ChatCompletion(ctx, key, &schemas.BifrostChatRequest{
		Provider: schemas.Databricks,
		Model:    "databricks-gpt-oss-120b",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}}},
	}); bErr != nil {
		t.Fatalf("ChatCompletion returned an error: %v", bErr)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotAuth != "Bearer dapi-secret-token" {
		t.Errorf("Authorization: got %q, want %q", gotAuth, "Bearer dapi-secret-token")
	}
}

// TestDatabricksChatStripsUnsupportedFields reproduces the Unity AI Gateway
// rejection seen with Claude Code requests. Databricks' OpenAI-compatible Chat
// Completions surface rejects these extensions, while unrelated provider-specific
// extra params still need to pass through.
func TestDatabricksChatStripsUnsupportedFields(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var gotBody map[string]any

	provider, server := newStubProvider(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		mu.Lock()
		gotBody = body
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(stubChatResponse))
	})

	key := schemas.Key{
		Models: []string{"*"},
		Value:  *schemas.NewSecretVar("dapi-test"),
		DatabricksKeyConfig: &schemas.DatabricksKeyConfig{
			WorkspaceURL: *schemas.NewSecretVar(serverHost(t, server)),
		},
	}
	contextManagement := json.RawMessage(`{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]}`)
	request := &schemas.BifrostChatRequest{
		Provider: schemas.Databricks,
		Model:    "system.ai.claude-opus-5",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hey")}}},
		Params: &schemas.ChatParameters{
			ContextManagement: contextManagement,
			Reasoning:         &schemas.ChatReasoning{Effort: schemas.Ptr("medium")},
			ExtraParams: map[string]any{
				"context_management":    contextManagement,
				"reasoning_effort":      "medium",
				"databricks_test_param": "kept",
			},
		},
	}

	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, bErr := provider.ChatCompletion(ctx, key, request); bErr != nil {
		t.Fatalf("ChatCompletion returned an error: %v", bErr)
	}

	mu.Lock()
	defer mu.Unlock()
	if _, exists := gotBody["context_management"]; exists {
		t.Errorf("context_management reached Databricks: %#v", gotBody["context_management"])
	}
	if _, exists := gotBody["reasoning_effort"]; exists {
		t.Errorf("reasoning_effort reached Databricks: %#v", gotBody["reasoning_effort"])
	}
	if gotBody["databricks_test_param"] != "kept" {
		t.Errorf("unrelated extra param: got %#v, want %q", gotBody["databricks_test_param"], "kept")
	}
	if len(request.Params.ContextManagement) == 0 || request.Params.Reasoning == nil || request.Params.Reasoning.Effort == nil ||
		request.Params.ExtraParams["context_management"] == nil || request.Params.ExtraParams["reasoning_effort"] == nil {
		t.Error("sanitizing the wire request mutated the original request")
	}
}

// TestDatabricksChatStreamStripsUnsupportedFields covers the streaming handler
// used by Claude Code. It must apply the same field filtering as the unary path.
func TestDatabricksChatStreamStripsUnsupportedFields(t *testing.T) {
	t.Parallel()

	bodyCh := make(chan map[string]any, 1)
	provider, server := newStubProvider(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		bodyCh <- body
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	})

	key := schemas.Key{
		Models: []string{"*"},
		Value:  *schemas.NewSecretVar("dapi-test"),
		DatabricksKeyConfig: &schemas.DatabricksKeyConfig{
			WorkspaceURL: *schemas.NewSecretVar(serverHost(t, server)),
		},
	}
	contextManagement := json.RawMessage(`{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]}`)
	request := &schemas.BifrostChatRequest{
		Provider: schemas.Databricks,
		Model:    "system.ai.claude-opus-5",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hey")}}},
		Params: &schemas.ChatParameters{
			ContextManagement: contextManagement,
			Reasoning:         &schemas.ChatReasoning{Effort: schemas.Ptr("medium")},
			ExtraParams: map[string]any{
				"context_management":    contextManagement,
				"reasoning_effort":      "medium",
				"databricks_test_param": "kept",
			},
		},
	}

	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	postHookRunner := func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, bErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return result, bErr
	}
	stream, bErr := provider.ChatCompletionStream(ctx, postHookRunner, nil, key, request)
	if bErr != nil {
		t.Fatalf("ChatCompletionStream returned an error: %v", bErr)
	}
	for range stream {
	}

	select {
	case gotBody := <-bodyCh:
		if _, exists := gotBody["context_management"]; exists {
			t.Errorf("context_management reached Databricks: %#v", gotBody["context_management"])
		}
		if _, exists := gotBody["reasoning_effort"]; exists {
			t.Errorf("reasoning_effort reached Databricks: %#v", gotBody["reasoning_effort"])
		}
		if gotBody["databricks_test_param"] != "kept" {
			t.Errorf("unrelated extra param: got %#v, want %q", gotBody["databricks_test_param"], "kept")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("mock server did not receive the streaming request")
	}

	if len(request.Params.ContextManagement) == 0 || request.Params.Reasoning == nil || request.Params.Reasoning.Effort == nil ||
		request.Params.ExtraParams["context_management"] == nil || request.Params.ExtraParams["reasoning_effort"] == nil {
		t.Error("sanitizing the wire request mutated the original request")
	}
}

// TestDatabricksReasoningEffortFollowsDatasheet pins the model-parameter wiring: whether
// reasoning_effort reaches Databricks is decided by the datasheet record for the endpoint's
// model, not by a hardcoded provider-wide rule. Resolution order is unsupported_fields →
// supports_reasoning_effort / the effort ladder → supports_reasoning, and a model with no row
// keeps the conservative drop.
func TestDatabricksReasoningEffortFollowsDatasheet(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		caps       *schemas.ModelCapabilities
		wantEffort any
	}{
		{
			name:       "no datasheet row drops the effort",
			model:      "databricks-unknown-endpoint",
			wantEffort: nil,
		},
		{
			name:       "supports_reasoning keeps the effort",
			model:      "databricks-reasoning-endpoint",
			caps:       &schemas.ModelCapabilities{SupportsReasoning: schemas.Ptr(true)},
			wantEffort: "medium",
		},
		{
			// The shape of the real databricks/databricks-claude-opus-5 row: the model
			// reasons, but through Claude's thinking budget rather than an effort label.
			name:       "anthropic-family model drops the effort even when it reasons",
			model:      "databricks-claude-opus-5",
			caps:       &schemas.ModelCapabilities{SupportsReasoning: schemas.Ptr(true)},
			wantEffort: nil,
		},
		{
			// The shape of the real databricks/databricks-gpt-oss-120b row.
			name:  "a published reasoning_effort parameter keeps the effort",
			model: "databricks-gpt-oss-120b",
			caps: &schemas.ModelCapabilities{
				SupportsReasoning: schemas.Ptr(true),
				ModelParameters:   []schemas.ModelParameterDescriptor{{ID: "temperature"}, {ID: "reasoning_effort"}},
			},
			wantEffort: "medium",
		},
		{
			name:       "supports_reasoning_effort false drops the effort",
			model:      "databricks-budget-only-endpoint",
			caps:       &schemas.ModelCapabilities{SupportsReasoning: schemas.Ptr(true), SupportsReasoningEffort: schemas.Ptr(false)},
			wantEffort: nil,
		},
		{
			name:       "effort ladder keeps the effort and clamps it to a published rung",
			model:      "databricks-ladder-endpoint",
			caps:       &schemas.ModelCapabilities{ReasoningEffortLevels: []string{"low", "high"}},
			wantEffort: "high",
		},
		{
			name:  "unsupported_fields wins over the reasoning flags",
			model: "databricks-rejects-effort-endpoint",
			caps: &schemas.ModelCapabilities{
				SupportsReasoning:       schemas.Ptr(true),
				SupportsReasoningEffort: schemas.Ptr(true),
				UnsupportedFields:       map[string]bool{schemas.FieldReasoningEffort: true},
			},
			wantEffort: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The resolver is process-global, so answer only for this case's model and
			// leave every other lookup on the name-based fallbacks.
			schemas.SetCapabilityResolver(func(provider schemas.ModelProvider, model string) *schemas.ModelCapabilities {
				if provider == schemas.Databricks && model == tt.model {
					return tt.caps
				}
				return nil
			})
			t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })

			var mu sync.Mutex
			var gotBody map[string]any
			provider, server := newStubProvider(t, func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode request body: %v", err)
				}
				mu.Lock()
				gotBody = body
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(stubChatResponse))
			})

			key := schemas.Key{
				Models: []string{"*"},
				Value:  *schemas.NewSecretVar("dapi-test"),
				DatabricksKeyConfig: &schemas.DatabricksKeyConfig{
					WorkspaceURL: *schemas.NewSecretVar(serverHost(t, server)),
				},
			}
			request := &schemas.BifrostChatRequest{
				Provider: schemas.Databricks,
				Model:    tt.model,
				Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hey")}}},
				Params: &schemas.ChatParameters{
					Reasoning: &schemas.ChatReasoning{Effort: schemas.Ptr("medium")},
				},
			}

			ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, bErr := provider.ChatCompletion(ctx, key, request); bErr != nil {
				t.Fatalf("ChatCompletion returned an error: %v", bErr)
			}

			mu.Lock()
			defer mu.Unlock()
			if got := gotBody["reasoning_effort"]; got != tt.wantEffort {
				t.Errorf("reasoning_effort on the wire: got %#v, want %#v", got, tt.wantEffort)
			}
		})
	}
}

// TestDatabricksChatParamsFollowDatasheet covers the rest of the parameter wiring. Bifrost's
// neutral parameter set is wider than any single Databricks endpoint accepts, so each
// optional field is gated on the datasheet record for the model rather than on a
// provider-wide rule. A model the datasheet does not describe keeps every field.
func TestDatabricksChatParamsFollowDatasheet(t *testing.T) {
	sent := []string{
		"temperature", "top_p", "top_k", "tool_choice", "parallel_tool_calls",
		"response_format", "stop", "presence_penalty", "frequency_penalty",
	}

	tests := []struct {
		name        string
		model       string
		caps        *schemas.ModelCapabilities
		wantDropped []string
	}{
		{
			name:  "no datasheet row keeps every field",
			model: "databricks-unknown-endpoint",
		},
		{
			// The shape of the real databricks/databricks-claude-opus-5 row: adaptive-only
			// thinking, which rejects the sampling knobs with a 400.
			name:        "supports_sampling_params false drops temperature, top_p and top_k",
			model:       "databricks-adaptive-endpoint",
			caps:        &schemas.ModelCapabilities{SupportsSamplingParams: schemas.Ptr(false)},
			wantDropped: []string{"temperature", "top_p", "top_k"},
		},
		{
			name:        "supports_tool_choice false drops the tool_choice pin",
			model:       "databricks-no-tool-choice-endpoint",
			caps:        &schemas.ModelCapabilities{SupportsToolChoice: schemas.Ptr(false)},
			wantDropped: []string{"tool_choice"},
		},
		{
			name:        "supports_parallel_function_calling false drops parallel_tool_calls",
			model:       "databricks-serial-tools-endpoint",
			caps:        &schemas.ModelCapabilities{SupportsParallelFunctionCalling: schemas.Ptr(false)},
			wantDropped: []string{"parallel_tool_calls"},
		},
		{
			name:        "supports_response_schema false drops response_format",
			model:       "databricks-no-schema-endpoint",
			caps:        &schemas.ModelCapabilities{SupportsResponseSchema: schemas.Ptr(false)},
			wantDropped: []string{"response_format"},
		},
		{
			name:  "unsupported_fields drops stop and the penalties",
			model: "databricks-narrow-endpoint",
			caps: &schemas.ModelCapabilities{UnsupportedFields: map[string]bool{
				schemas.FieldStop:             true,
				schemas.FieldPresencePenalty:  true,
				schemas.FieldFrequencyPenalty: true,
			}},
			wantDropped: []string{"stop", "presence_penalty", "frequency_penalty"},
		},
		{
			name:        "unsupported_fields top_p drops the sampling knobs",
			model:       "databricks-no-top-p-endpoint",
			caps:        &schemas.ModelCapabilities{UnsupportedFields: map[string]bool{schemas.FieldTopP: true}},
			wantDropped: []string{"temperature", "top_p", "top_k"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			schemas.SetCapabilityResolver(func(provider schemas.ModelProvider, model string) *schemas.ModelCapabilities {
				if provider == schemas.Databricks && model == tt.model {
					return tt.caps
				}
				return nil
			})
			t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })

			var mu sync.Mutex
			var gotBody map[string]any
			provider, server := newStubProvider(t, func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode request body: %v", err)
				}
				mu.Lock()
				gotBody = body
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(stubChatResponse))
			})

			key := schemas.Key{
				Models: []string{"*"},
				Value:  *schemas.NewSecretVar("dapi-test"),
				DatabricksKeyConfig: &schemas.DatabricksKeyConfig{
					WorkspaceURL: *schemas.NewSecretVar(serverHost(t, server)),
				},
			}
			responseFormat := any(map[string]any{"type": "json_object"})
			request := &schemas.BifrostChatRequest{
				Provider: schemas.Databricks,
				Model:    tt.model,
				Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hey")}}},
				Params: &schemas.ChatParameters{
					Temperature:       schemas.Ptr(0.5),
					TopP:              schemas.Ptr(0.9),
					TopK:              schemas.Ptr(40),
					ToolChoice:        &schemas.ChatToolChoice{ChatToolChoiceStr: schemas.Ptr("auto")},
					ParallelToolCalls: schemas.Ptr(true),
					ResponseFormat:    &responseFormat,
					Stop:              []string{"stop"},
					PresencePenalty:   schemas.Ptr(0.1),
					FrequencyPenalty:  schemas.Ptr(0.2),
				},
			}

			ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, bErr := provider.ChatCompletion(ctx, key, request); bErr != nil {
				t.Fatalf("ChatCompletion returned an error: %v", bErr)
			}

			mu.Lock()
			defer mu.Unlock()
			for _, field := range sent {
				_, onWire := gotBody[field]
				wantDropped := slices.Contains(tt.wantDropped, field)
				if wantDropped && onWire {
					t.Errorf("%s reached Databricks: %#v", field, gotBody[field])
				}
				if !wantDropped && !onWire {
					t.Errorf("%s was dropped, but the datasheet does not reject it", field)
				}
			}
			// Dropping is a wire-level fixup; the caller's request must survive it intact
			// for fallbacks and post-hooks.
			if request.Params.Temperature == nil || request.Params.ToolChoice == nil || len(request.Params.Stop) == 0 {
				t.Error("sanitizing the wire request mutated the original request")
			}
		})
	}
}

// TestDatabricksChatStripsAnthropicOnlyFields pins the fields that are dropped whatever the
// datasheet says. They ride on the neutral chat parameters and serialize straight onto the
// wire, but both Databricks surfaces are OpenAI-shaped and reject them regardless of which
// model backs the endpoint — so this is a fact about the surface, not a model capability.
// The datasheet marks Claude-on-Databricks as supporting prompt caching and context editing,
// which would otherwise forward Anthropic-native fields onto an OpenAI-shaped request.
func TestDatabricksChatStripsAnthropicOnlyFields(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var gotBody map[string]any
	provider, server := newStubProvider(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		mu.Lock()
		gotBody = body
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(stubChatResponse))
	})

	key := schemas.Key{
		Models: []string{"*"},
		Value:  *schemas.NewSecretVar("dapi-test"),
		DatabricksKeyConfig: &schemas.DatabricksKeyConfig{
			WorkspaceURL: *schemas.NewSecretVar(serverHost(t, server)),
		},
	}
	request := &schemas.BifrostChatRequest{
		Provider: schemas.Databricks,
		Model:    "databricks-claude-sonnet-4-5",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hey")}}},
		Params: &schemas.ChatParameters{
			ContextManagement: json.RawMessage(`{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]}`),
			CacheControl:      &schemas.CacheControl{Type: "ephemeral"},
			Speed:             schemas.Ptr("fast"),
			InferenceGeo:      schemas.Ptr("us"),
			TaskBudget:        &schemas.ChatTaskBudget{},
			Container:         &schemas.ChatContainer{},
			MCPServers:        []schemas.ChatMCPServer{{Name: "docs"}},
			ExtraParams:       map[string]any{"databricks_test_param": "kept"},
		},
	}

	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, bErr := provider.ChatCompletion(ctx, key, request); bErr != nil {
		t.Fatalf("ChatCompletion returned an error: %v", bErr)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, field := range []string{
		"context_management", "cache_control", "speed", "inference_geo",
		"task_budget", "container", "mcp_servers",
	} {
		if _, exists := gotBody[field]; exists {
			t.Errorf("%s reached Databricks: %#v", field, gotBody[field])
		}
	}
	if gotBody["databricks_test_param"] != "kept" {
		t.Errorf("unrelated extra param: got %#v, want %q", gotBody["databricks_test_param"], "kept")
	}
	if request.Params.CacheControl == nil || request.Params.Speed == nil || len(request.Params.MCPServers) == 0 {
		t.Error("sanitizing the wire request mutated the original request")
	}
}

// TestDatabricksGatewayRequestTags covers forwarding Bifrost governance labels to Databricks
// for usage attribution. The header is opt-in, carries display names only, and must not
// displace a header the caller supplied.
func TestDatabricksGatewayRequestTags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		forward   bool
		preset    string
		wantTags  map[string]string
		wantEmpty bool
	}{
		{name: "disabled by default", forward: false, wantEmpty: true},
		{
			name:     "forwards governance labels when enabled",
			forward:  true,
			wantTags: map[string]string{"virtual_key": "vk-prod", "team": "platform", "customer": "acme"},
		},
		{
			name:    "a caller supplied header wins",
			forward: true,
			preset:  `{"project":"caller"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var mu sync.Mutex
			var gotTags string

			provider, server := newStubProvider(t, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				gotTags = r.Header.Get("Databricks-Ai-Gateway-Request-Tags")
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(stubChatResponse))
			})

			key := schemas.Key{
				Models: []string{"*"},
				Value:  *schemas.NewSecretVar("dapi-test"),
				DatabricksKeyConfig: &schemas.DatabricksKeyConfig{
					WorkspaceURL:       *schemas.NewSecretVar(serverHost(t, server)),
					ForwardGatewayTags: tt.forward,
				},
			}

			ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			ctx.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyName, "vk-prod")
			ctx.SetValue(schemas.BifrostContextKeyGovernanceTeamName, "platform")
			ctx.SetValue(schemas.BifrostContextKeyGovernanceCustomerName, "acme")
			if tt.preset != "" {
				ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{
					"Databricks-Ai-Gateway-Request-Tags": {tt.preset},
				})
			}

			if _, bErr := provider.ChatCompletion(ctx, key, &schemas.BifrostChatRequest{
				Provider: schemas.Databricks,
				Model:    "system.ai.claude-sonnet-4-5",
				Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}}},
			}); bErr != nil {
				t.Fatalf("ChatCompletion returned an error: %v", bErr)
			}

			mu.Lock()
			defer mu.Unlock()

			if tt.wantEmpty {
				if gotTags != "" {
					t.Errorf("request tags: got %q, want no header when forwarding is disabled", gotTags)
				}
				return
			}
			if tt.preset != "" {
				if gotTags != tt.preset {
					t.Errorf("request tags: got %q, want the caller supplied %q", gotTags, tt.preset)
				}
				return
			}

			var got map[string]string
			if err := json.Unmarshal([]byte(gotTags), &got); err != nil {
				t.Fatalf("request tags %q is not valid JSON: %v", gotTags, err)
			}
			for k, want := range tt.wantTags {
				if got[k] != want {
					t.Errorf("request tag %q: got %q, want %q", k, got[k], want)
				}
			}
		})
	}
}

// TestDatabricksListModelsDoesNotCallWorkspaceAPIs verifies that both configured API formats
// use the datasheet-only model catalog and never call a Databricks inventory endpoint.
func TestDatabricksListModelsDoesNotCallWorkspaceAPIs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		apiFormat schemas.DatabricksAPIFormat
		wantPath  string
		body      string
		wantIDs   []string
	}{
		{
			name:      "model serving endpoints",
			apiFormat: schemas.DatabricksAPIFormatModelServing,
			wantPath:  "/api/2.0/serving-endpoints",
			body: `{"endpoints":[
				{"name":"databricks-claude-sonnet-4-5","task":"llm/v1/chat","state":{"ready":"READY"}},
				{"name":"databricks-gte-large-en","task":"llm/v1/embeddings"},
				{"name":"half-built","state":{"ready":"NOT_READY"}}
			]}`,
			wantIDs: []string{"databricks/databricks-claude-sonnet-4-5", "databricks/databricks-gte-large-en"},
		},
		{
			name:      "unity catalog model services",
			apiFormat: schemas.DatabricksAPIFormatAIGateway,
			wantPath:  "/api/2.1/unity-catalog/model-services",
			body: `{"model_services":[
				{"full_name":"model-services/system.ai.claude-sonnet-4-5","name":"claude-sonnet-4-5"},
				{"catalog_name":"main","schema_name":"default","name":"my-service"}
			]}`,
			wantIDs: []string{"databricks/system.ai.claude-sonnet-4-5", "databricks/main.default.my-service"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var mu sync.Mutex
			var gotPath string

			provider, server := newStubProvider(t, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				gotPath = r.URL.Path
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			})

			keys := []schemas.Key{{
				Models: []string{"*"},
				Value:  *schemas.NewSecretVar("dapi-test"),
				DatabricksKeyConfig: &schemas.DatabricksKeyConfig{
					WorkspaceURL: *schemas.NewSecretVar(serverHost(t, server)),
					APIFormat:    tt.apiFormat,
				},
			}}

			ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			response, bErr := provider.ListModels(ctx, keys, &schemas.BifrostListModelsRequest{Provider: schemas.Databricks})
			if bErr != nil {
				t.Fatalf("ListModels returned an error: %v", bErr)
			}

			mu.Lock()
			if gotPath != "" {
				t.Errorf("unexpected Databricks model inventory request to %q", gotPath)
			}
			mu.Unlock()

			if len(response.Data) != 0 {
				t.Fatalf("ListModels returned live models instead of deferring to the datasheet: %#v", response.Data)
			}
		})
	}
}

// TestDatabricksUnfilteredListModelsDoesNotCallWorkspaceAPIs covers the unfiltered path used
// by GET /api/models and ensures it also leaves model selection entirely to the datasheet.
func TestDatabricksUnfilteredListModelsDoesNotCallWorkspaceAPIs(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	gotPaths := make(map[string]int)
	provider, server := newStubProvider(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPaths[r.URL.Path]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/2.0/serving-endpoints":
			_, _ = w.Write([]byte(`{"endpoints":[{"name":"databricks-gpt-oss-120b","state":{"ready":"READY"}}]}`))
		case "/api/2.1/unity-catalog/model-services":
			_, _ = w.Write([]byte(`{"model_services":[{"full_name":"model-services/main.default.my-service"}]}`))
		default:
			http.NotFound(w, r)
		}
	})

	keys := []schemas.Key{{
		Models: []string{"*"},
		Value:  *schemas.NewSecretVar("dapi-test"),
		DatabricksKeyConfig: &schemas.DatabricksKeyConfig{
			WorkspaceURL: *schemas.NewSecretVar(serverHost(t, server)),
			APIFormat:    schemas.DatabricksAPIFormatModelServing,
		},
	}}
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	response, bErr := provider.ListModels(ctx, keys, &schemas.BifrostListModelsRequest{
		Provider:   schemas.Databricks,
		Unfiltered: true,
	})
	if bErr != nil {
		t.Fatalf("ListModels returned an error: %v", bErr)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(gotPaths) != 0 {
		t.Errorf("unexpected Databricks model inventory requests: %v", gotPaths)
	}
	if len(response.Data) != 0 {
		t.Fatalf("ListModels returned live models instead of deferring to the datasheet: %#v", response.Data)
	}
}

// TestDatabricksUnfilteredListModelsIgnoresWorkspaceResponses ensures even a reachable
// inventory endpoint cannot leak live resource names into the datasheet-only catalog.
func TestDatabricksUnfilteredListModelsIgnoresWorkspaceResponses(t *testing.T) {
	t.Parallel()

	provider, server := newStubProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/2.0/serving-endpoints" {
			_, _ = w.Write([]byte(`{"endpoints":[{"name":"databricks-gpt-oss-120b"}]}`))
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error_code":"PERMISSION_DENIED","message":"AI Gateway unavailable"}`))
	})

	keys := []schemas.Key{{
		Models: []string{"*"},
		Value:  *schemas.NewSecretVar("dapi-test"),
		DatabricksKeyConfig: &schemas.DatabricksKeyConfig{
			WorkspaceURL: *schemas.NewSecretVar(serverHost(t, server)),
		},
	}}
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	response, bErr := provider.ListModels(ctx, keys, &schemas.BifrostListModelsRequest{
		Provider:   schemas.Databricks,
		Unfiltered: true,
	})
	if bErr != nil {
		t.Fatalf("ListModels returned an error despite one successful surface: %v", bErr)
	}
	if len(response.Data) != 0 {
		t.Fatalf("ListModels returned live models instead of deferring to the datasheet: %#v", response.Data)
	}
}

// TestDatabricksListModelsIgnoresWorkspaceErrors verifies that an unavailable inventory API
// cannot mark an otherwise usable Databricks key as failed.
func TestDatabricksListModelsIgnoresWorkspaceErrors(t *testing.T) {
	t.Parallel()

	provider, server := newStubProvider(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("Databricks model inventory API was called")
	})

	keys := []schemas.Key{{
		Models: []string{"*"},
		Value:  *schemas.NewSecretVar("dapi-test"),
		DatabricksKeyConfig: &schemas.DatabricksKeyConfig{
			WorkspaceURL: *schemas.NewSecretVar(serverHost(t, server)),
		},
	}}

	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	response, bErr := provider.ListModels(ctx, keys, &schemas.BifrostListModelsRequest{Provider: schemas.Databricks})
	if bErr != nil {
		t.Fatalf("ListModels returned an error: %v", bErr)
	}
	if len(response.Data) != 0 {
		t.Fatalf("ListModels returned live models instead of deferring to the datasheet: %#v", response.Data)
	}
}

// TestDatabricksMissingCredentials covers the two misconfigurations the provider must reject
// locally rather than sending an unauthenticated request upstream.
func TestDatabricksMissingCredentials(t *testing.T) {
	t.Parallel()

	provider, server := newStubProvider(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream was called; the request should have been rejected locally")
	})
	host := serverHost(t, server)

	tests := []struct {
		name string
		key  schemas.Key
	}{
		{
			name: "no workspace url",
			key: schemas.Key{
				Models: []string{"*"},
				Value:  *schemas.NewSecretVar("dapi-test"),
			},
		},
		{
			name: "no token and no service principal",
			key: schemas.Key{
				Models:              []string{"*"},
				DatabricksKeyConfig: &schemas.DatabricksKeyConfig{WorkspaceURL: *schemas.NewSecretVar(host)},
			},
		},
		{
			name: "half configured service principal",
			key: schemas.Key{
				Models: []string{"*"},
				DatabricksKeyConfig: &schemas.DatabricksKeyConfig{
					WorkspaceURL: *schemas.NewSecretVar(host),
					ClientID:     schemas.NewSecretVar("sp-client-id"),
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			if _, bErr := provider.ChatCompletion(ctx, tt.key, &schemas.BifrostChatRequest{
				Provider: schemas.Databricks,
				Model:    "databricks-gpt-oss-120b",
				Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}}},
			}); bErr == nil {
				t.Fatal("ChatCompletion: got nil error, want a configuration error")
			}
		})
	}
}

// TestDatabricksGatewayTagsDoNotMutateCallerHeaders pins that forwarding governance tags
// never writes into the caller-owned extra-headers map on the context. That map is shared
// with every other reader of the context (a fallback provider, concurrent goroutines), so
// the tag header must travel with the Databricks request alone.
func TestDatabricksGatewayTagsDoNotMutateCallerHeaders(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var gotTags, gotCaller string

	provider, server := newStubProvider(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotTags = r.Header.Get("Databricks-Ai-Gateway-Request-Tags")
		gotCaller = r.Header.Get("X-Caller")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(stubChatResponse))
	})

	key := schemas.Key{
		Models: []string{"*"},
		Value:  *schemas.NewSecretVar("dapi-test"),
		DatabricksKeyConfig: &schemas.DatabricksKeyConfig{
			WorkspaceURL:       *schemas.NewSecretVar(serverHost(t, server)),
			ForwardGatewayTags: true,
		},
	}

	callerHeaders := map[string][]string{"X-Caller": {"1"}}

	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ctx.SetValue(schemas.BifrostContextKeyGovernanceTeamName, "platform")
	ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, callerHeaders)

	if _, bErr := provider.ChatCompletion(ctx, key, &schemas.BifrostChatRequest{
		Provider: schemas.Databricks,
		Model:    "system.ai.claude-sonnet-4-5",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}}},
	}); bErr != nil {
		t.Fatalf("ChatCompletion returned an error: %v", bErr)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotTags == "" {
		t.Fatal("request tags header was not sent")
	}
	if gotCaller != "1" {
		t.Errorf("caller header: got %q, want %q", gotCaller, "1")
	}
	if _, leaked := callerHeaders["Databricks-Ai-Gateway-Request-Tags"]; leaked {
		t.Error("the caller-owned extra headers map was mutated with the tag header")
	}
	if len(callerHeaders) != 1 {
		t.Errorf("caller-owned extra headers map has %d entries, want 1", len(callerHeaders))
	}
	if ctxHeaders, _ := ctx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string); len(ctxHeaders) != 1 {
		t.Errorf("context extra headers has %d entries after the request, want the caller's 1", len(ctxHeaders))
	}
}
