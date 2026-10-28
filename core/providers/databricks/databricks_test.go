package databricks_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
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
			Reasoning:             true,
			// listModelsByKey intentionally returns nothing; the datasheet is the catalog.
			ListModels: false,
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

const stubEmbeddingResponse = `{
	"object": "list",
	"data": [{"object": "embedding", "index": 0, "embedding": [0.1, 0.2]}],
	"model": "m",
	"usage": {"prompt_tokens": 1, "total_tokens": 1}
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
		{"auto routes a short model name to the ai gateway", "claude-opus-5", "", "/ai-gateway/mlflow/v1/chat/completions"},
		{"auto routes a versioned short name to the ai gateway", "gpt-5.5", "", "/ai-gateway/mlflow/v1/chat/completions"},
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

// TestDatabricksAIGatewayModelPrefix covers the model name Databricks receives. A bare name
// bound for the Unity AI Gateway gains the system.ai. catalog prefix; names that already
// carry a catalog, names bound for Model Serving, and names resolved from a key alias are
// sent exactly as given.
func TestDatabricksAIGatewayModelPrefix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		model     string
		apiFormat schemas.DatabricksAPIFormat
		alias     *schemas.ResolvedAlias
		wantModel string
	}{
		{"bare name on the ai gateway gains the system.ai prefix", "gpt-5.5", schemas.DatabricksAPIFormatAIGateway, nil, "system.ai.gpt-5.5"},
		{"bare name without a version dot on the ai gateway gains the prefix", "claude-opus-5", schemas.DatabricksAPIFormatAIGateway, nil, "system.ai.claude-opus-5"},
		{"version dot under auto routes to the ai gateway and gains the prefix", "gpt-5.5", "", nil, "system.ai.gpt-5.5"},
		{"short name under auto routes to the ai gateway and gains the prefix", "claude-opus-5", "", nil, "system.ai.claude-opus-5"},
		{
			"bare alias model_id under auto stays on model serving unchanged",
			"my-pt-endpoint",
			"",
			&schemas.ResolvedAlias{Key: "gpt-5.5", Config: &schemas.AliasConfig{ModelID: "my-pt-endpoint"}},
			"my-pt-endpoint",
		},
		{"system.ai name on the ai gateway is unchanged", "system.ai.gpt-5.5", schemas.DatabricksAPIFormatAIGateway, nil, "system.ai.gpt-5.5"},
		{"unity catalog fqn on the ai gateway is unchanged", "main.default.my-service", schemas.DatabricksAPIFormatAIGateway, nil, "main.default.my-service"},
		{"bare name under auto is a serving endpoint and is unchanged", "databricks-claude-sonnet-4-5", "", nil, "databricks-claude-sonnet-4-5"},
		{"bare name pinned to model serving is unchanged", "gpt-5.5", schemas.DatabricksAPIFormatModelServing, nil, "gpt-5.5"},
		{
			"alias model_id on the ai gateway is sent verbatim",
			"my-gpt-deployment",
			schemas.DatabricksAPIFormatAIGateway,
			&schemas.ResolvedAlias{Key: "gpt-5.5", Config: &schemas.AliasConfig{ModelID: "my-gpt-deployment"}},
			"my-gpt-deployment",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var mu sync.Mutex
			var gotModels []string

			provider, server := newStubProvider(t, func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode request body: %v", err)
				}
				mu.Lock()
				gotModels = append(gotModels, fmt.Sprint(body["model"]))
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasSuffix(r.URL.Path, "/embeddings"):
					_, _ = w.Write([]byte(stubEmbeddingResponse))
				case strings.HasSuffix(r.URL.Path, "/responses"):
					_, _ = w.Write([]byte(stubResponsesResponse))
				default:
					_, _ = w.Write([]byte(stubChatResponse))
				}
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
			if tt.alias != nil {
				ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, tt.alias)
			}

			chatRequest := &schemas.BifrostChatRequest{
				Provider: schemas.Databricks,
				Model:    tt.model,
				Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}}},
			}
			if _, bErr := provider.ChatCompletion(ctx, key, chatRequest); bErr != nil {
				t.Fatalf("ChatCompletion returned an error: %v", bErr)
			}
			if chatRequest.Model != tt.model {
				t.Errorf("ChatCompletion mutated the caller's model: got %q, want %q", chatRequest.Model, tt.model)
			}

			embeddingRequest := &schemas.BifrostEmbeddingRequest{
				Provider: schemas.Databricks,
				Model:    tt.model,
				Input:    []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: schemas.Ptr("hi")}}}},
			}
			if _, bErr := provider.Embedding(ctx, key, embeddingRequest); bErr != nil {
				t.Fatalf("Embedding returned an error: %v", bErr)
			}
			if embeddingRequest.Model != tt.model {
				t.Errorf("Embedding mutated the caller's model: got %q, want %q", embeddingRequest.Model, tt.model)
			}

			// Responses is emulated through chat on the AI Gateway and served natively on
			// Model Serving; the wire model must be rewritten on both routes.
			responsesRequest := &schemas.BifrostResponsesRequest{
				Provider: schemas.Databricks,
				Model:    tt.model,
				Input:    []schemas.ResponsesMessage{{Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser), Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")}}},
			}
			if _, bErr := provider.Responses(ctx, key, responsesRequest); bErr != nil {
				t.Fatalf("Responses returned an error: %v", bErr)
			}
			if responsesRequest.Model != tt.model {
				t.Errorf("Responses mutated the caller's model: got %q, want %q", responsesRequest.Model, tt.model)
			}

			mu.Lock()
			defer mu.Unlock()
			if len(gotModels) != 3 {
				t.Fatalf("expected 3 upstream calls, got %d", len(gotModels))
			}
			for i, got := range gotModels {
				if got != tt.wantModel {
					t.Errorf("call %d: wire model got %q, want %q", i, got, tt.wantModel)
				}
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

// TestDatabricksChatSendsLegacyMaxTokens pins the output ceiling field. Llama, gpt-oss and Qwen
// endpoints answer max_completion_tokens with 400 "unknown field", while every chat endpoint
// checked on a live workspace (GPT, Claude, Gemini, Grok, GLM, Kimi included) accepts max_tokens,
// so the ceiling always goes out under the legacy name, unary and streaming.
func TestDatabricksChatSendsLegacyMaxTokens(t *testing.T) {
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
		if stream, _ := body["stream"].(bool); stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			return
		}
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
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for _, model := range []string{"databricks-meta-llama-3-3-70b-instruct", "databricks-gpt-oss-120b", "system.ai.qwen35-122b-a10b", "databricks-gpt-5-5"} {
		for _, stream := range []bool{false, true} {
			name := model + "/unary"
			if stream {
				name = model + "/stream"
			}
			t.Run(name, func(t *testing.T) {
				request := &schemas.BifrostChatRequest{
					Provider: schemas.Databricks,
					Model:    model,
					Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}}},
					Params:   &schemas.ChatParameters{MaxCompletionTokens: schemas.Ptr(321)},
				}
				if stream {
					ch, bErr := provider.ChatCompletionStream(ctx, noopPostHook, nil, key, request)
					if bErr != nil {
						t.Fatalf("ChatCompletionStream returned an error: %v", bErr)
					}
					for range ch {
					}
				} else if _, bErr := provider.ChatCompletion(ctx, key, request); bErr != nil {
					t.Fatalf("ChatCompletion returned an error: %v", bErr)
				}
				mu.Lock()
				defer mu.Unlock()
				if _, exists := gotBody["max_completion_tokens"]; exists {
					t.Errorf("max_completion_tokens reached Databricks: %#v", gotBody["max_completion_tokens"])
				}
				if gotBody["max_tokens"] != float64(321) {
					t.Errorf("max_tokens: got %#v, want 321", gotBody["max_tokens"])
				}
				if request.Params.MaxCompletionTokens == nil || *request.Params.MaxCompletionTokens != 321 {
					t.Error("converting the wire request mutated the original request")
				}
			})
		}
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
// provider-wide rule. A model the datasheet does not describe keeps every field except
// parallel_tool_calls, which both surfaces reject and so is only sent when a row opts in.
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
			name:        "no datasheet row keeps every field but parallel_tool_calls",
			model:       "databricks-unknown-endpoint",
			wantDropped: []string{"parallel_tool_calls"},
		},
		{
			name:  "supports_parallel_function_calling true keeps parallel_tool_calls",
			model: "databricks-parallel-tools-endpoint",
			caps:  &schemas.ModelCapabilities{SupportsParallelFunctionCalling: schemas.Ptr(true)},
		},
		{
			// The shape of the real databricks/databricks-claude-opus-5 row: adaptive-only
			// thinking, which rejects the sampling knobs with a 400.
			name:        "supports_sampling_params false drops temperature, top_p and top_k",
			model:       "databricks-adaptive-endpoint",
			caps:        &schemas.ModelCapabilities{SupportsSamplingParams: schemas.Ptr(false)},
			wantDropped: []string{"temperature", "top_p", "top_k", "parallel_tool_calls"},
		},
		{
			name:        "supports_tool_choice false drops the tool_choice pin",
			model:       "databricks-no-tool-choice-endpoint",
			caps:        &schemas.ModelCapabilities{SupportsToolChoice: schemas.Ptr(false)},
			wantDropped: []string{"tool_choice", "parallel_tool_calls"},
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
			wantDropped: []string{"response_format", "parallel_tool_calls"},
		},
		{
			name:  "unsupported_fields drops stop and the penalties",
			model: "databricks-narrow-endpoint",
			caps: &schemas.ModelCapabilities{UnsupportedFields: map[string]bool{
				schemas.FieldStop:             true,
				schemas.FieldPresencePenalty:  true,
				schemas.FieldFrequencyPenalty: true,
			}},
			wantDropped: []string{"stop", "presence_penalty", "frequency_penalty", "parallel_tool_calls"},
		},
		{
			name:        "unsupported_fields top_p drops the sampling knobs",
			model:       "databricks-no-top-p-endpoint",
			caps:        &schemas.ModelCapabilities{UnsupportedFields: map[string]bool{schemas.FieldTopP: true}},
			wantDropped: []string{"temperature", "top_p", "top_k", "parallel_tool_calls"},
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

// TestDatabricksGeminiSingleSystemPrompt pins the one-system-prompt rule for Gemini-backed
// endpoints. Databricks maps chat messages onto Gemini's single system_instruction and
// answers anything more with "Gemini models only support one system prompt" (400). Claude
// Code sends its system prompt as several blocks, which reach the wire as one system message
// with several text parts, and that alone trips the rule. Other model families take the
// parts as sent.
func TestDatabricksGeminiSingleSystemPrompt(t *testing.T) {
	t.Parallel()

	text := func(s string) *schemas.ChatMessageContent {
		return &schemas.ChatMessageContent{ContentStr: schemas.Ptr(s)}
	}
	parts := func(texts ...string) *schemas.ChatMessageContent {
		blocks := make([]schemas.ChatContentBlock, len(texts))
		for i, s := range texts {
			blocks[i] = schemas.ChatContentBlock{Type: schemas.ChatContentBlockTypeText, Text: schemas.Ptr(s)}
		}
		return &schemas.ChatMessageContent{ContentBlocks: blocks}
	}
	user := schemas.ChatMessage{Role: schemas.ChatMessageRoleUser, Content: text("hey")}

	tests := []struct {
		name  string
		model string
		input []schemas.ChatMessage
		// want is the JSON the messages array must marshal to on the wire.
		want string
	}{
		{
			name:  "system parts are joined into one string",
			model: "databricks-gemini-3-8-flash",
			input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleSystem, Content: parts("rules", "tools")}, user},
			want:  `[{"role":"system","content":"rules\n\ntools"},{"role":"user","content":"hey"}]`,
		},
		{
			name:  "system and developer messages merge into the first",
			model: "databricks-gemini-3-8-flash",
			input: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleSystem, Content: text("rules")},
				{Role: schemas.ChatMessageRoleDeveloper, Content: parts("tools", "env")},
				user,
				{Role: schemas.ChatMessageRoleSystem, Content: text("reminder")},
			},
			want: `[{"role":"system","content":"rules\n\ntools\n\nenv\n\nreminder"},{"role":"user","content":"hey"}]`,
		},
		{
			name:  "AI Gateway model name is matched too",
			model: "system.ai.gemini-3-8-flash",
			input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleSystem, Content: parts("rules", "tools")}, user},
			want:  `[{"role":"system","content":"rules\n\ntools"},{"role":"user","content":"hey"}]`,
		},
		{
			name:  "a single string system prompt is sent unchanged",
			model: "databricks-gemini-3-8-flash",
			input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleSystem, Content: text("rules")}, user},
			want:  `[{"role":"system","content":"rules"},{"role":"user","content":"hey"}]`,
		},
		{
			name:  "non-Gemini models keep the parts",
			model: "databricks-claude-sonnet-4-5",
			input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleSystem, Content: parts("rules", "tools")}, user},
			want:  `[{"role":"system","content":[{"type":"text","text":"rules"},{"type":"text","text":"tools"}]},{"role":"user","content":"hey"}]`,
		},
	}

	for _, stream := range []bool{false, true} {
		for _, tt := range tests {
			name := tt.name
			if stream {
				name += " (stream)"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				bodyCh := make(chan map[string]json.RawMessage, 1)
				provider, server := newStubProvider(t, func(w http.ResponseWriter, r *http.Request) {
					var body map[string]json.RawMessage
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Errorf("decode request body: %v", err)
					}
					bodyCh <- body
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = w.Write([]byte("data: [DONE]\n\n"))
						return
					}
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
				original, err := json.Marshal(tt.input)
				if err != nil {
					t.Fatalf("marshal input: %v", err)
				}
				request := &schemas.BifrostChatRequest{Provider: schemas.Databricks, Model: tt.model, Input: tt.input}

				ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if stream {
					postHookRunner := func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, bErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
						return result, bErr
					}
					ch, bErr := provider.ChatCompletionStream(ctx, postHookRunner, nil, key, request)
					if bErr != nil {
						t.Fatalf("ChatCompletionStream returned an error: %v", bErr)
					}
					for range ch {
					}
				} else if _, bErr := provider.ChatCompletion(ctx, key, request); bErr != nil {
					t.Fatalf("ChatCompletion returned an error: %v", bErr)
				}

				select {
				case body := <-bodyCh:
					if got := string(body["messages"]); got != tt.want {
						t.Errorf("messages on the wire:\n got %s\nwant %s", got, tt.want)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("mock server did not receive the request")
				}
				after, err := json.Marshal(request.Input)
				if err != nil {
					t.Fatalf("marshal input: %v", err)
				}
				if string(after) != string(original) {
					t.Errorf("merging system prompts mutated the original request:\n got %s\nwant %s", after, original)
				}
			})
		}
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

// databricksDecisionResponseBody is an ai_decide reply for newDatabricksDecisionRequest,
// as returned live: answers under response, the noul value as probability, no usage.
const databricksDecisionResponseBody = `{"response":{"answers":{
	"escalate":{"type":"noul","probability":0.83},
	"team":{"type":"choice","choice":"engineering","probabilities":{"billing":0.0,"engineering":1.0},"confidence":0.99},
	"severity":{"type":"score","score":1.82,"legend":{"0":"Minor","1":"Major","2":"Critical"},"probabilities":{"0":0.02,"1":0.14,"2":0.84},"confidence":0.82}
}},"metadata":{"version":"1.0"}}`

func newDatabricksDecisionRequest(input schemas.DecisionInput) *schemas.BifrostDecisionRequest {
	return &schemas.BifrostDecisionRequest{
		Provider: schemas.Databricks,
		Model:    "ai_decide",
		Input:    input,
		Questions: []schemas.DecisionQuestion{
			{Type: schemas.DecisionTypePredicate, Name: schemas.Ptr("escalate"), Instructions: schemas.NewDecisionText("Does this ticket need escalation?")},
			{
				Type:         schemas.DecisionTypeChoice,
				Name:         schemas.Ptr("team"),
				Instructions: schemas.NewDecisionText("Which team should handle this ticket?"),
				Choices: []schemas.DecisionChoice{
					{Value: schemas.DecisionScalar{Str: schemas.Ptr("billing")}, Description: schemas.NewDecisionText("Charges and refunds")},
					{Value: schemas.DecisionScalar{Str: schemas.Ptr("engineering")}, Description: schemas.NewDecisionText("Bugs and outages")},
				},
			},
			{
				Type:         schemas.DecisionTypeScore,
				Name:         schemas.Ptr("severity"),
				Instructions: schemas.NewDecisionText("How severe is this incident?"),
				Levels: []schemas.DecisionLevel{
					{Label: "0", Description: schemas.NewDecisionText("Minor")},
					{Label: "1", Description: schemas.NewDecisionText("Major")},
					{Label: "2", Description: schemas.NewDecisionText("Critical")},
				},
			},
		},
	}
}

func newDatabricksDecisionKey(t *testing.T, server *httptest.Server) schemas.Key {
	t.Helper()
	return schemas.Key{
		Models:              []string{"*"},
		Value:               *schemas.NewSecretVar("dapi-secret-token"),
		DatabricksKeyConfig: &schemas.DatabricksKeyConfig{WorkspaceURL: *schemas.NewSecretVar(serverHost(t, server))},
	}
}

// TestDatabricksDecisionSendsAIDecideBody pins the native call: the body is Typesafe's
// systemone request without model and with the function version pinned, it goes to the
// workspace's /api/2.0/ai-functions/ai-decide with the PAT, and the reply is unwrapped from
// response with each noul probability read as the predicate's probability.
func TestDatabricksDecisionSendsAIDecideBody(t *testing.T) {
	t.Parallel()

	type capturedRequest struct {
		path, auth string
		body       []byte
	}
	// The handler runs on the server's goroutine, so the request is handed over on a channel.
	captured := make(chan capturedRequest, 1)
	provider, server := newStubProvider(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captured <- capturedRequest{path: r.URL.Path, auth: r.Header.Get("Authorization"), body: body}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-request-id", "req-1")
		_, _ = w.Write([]byte(databricksDecisionResponseBody))
	})

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bErr := provider.Decision(ctx, newDatabricksDecisionKey(t, server), newDatabricksDecisionRequest(schemas.DecisionInput{Text: schemas.Ptr("Checkout is down for every customer.")}))
	if bErr != nil {
		t.Fatalf("Decision returned an error: %v", bErr.Error)
	}
	sent := <-captured

	if sent.path != "/api/2.0/ai-functions/ai-decide" {
		t.Errorf("path = %q, want /api/2.0/ai-functions/ai-decide", sent.path)
	}
	if sent.auth != "Bearer dapi-secret-token" {
		t.Errorf("Authorization = %q, want the PAT as Bearer", sent.auth)
	}
	var gotBody, wantBody map[string]any
	if err := json.Unmarshal(sent.body, &gotBody); err != nil {
		t.Fatalf("decode sent body: %v", err)
	}
	if err := json.Unmarshal([]byte(`{
		"state": "Checkout is down for every customer.",
		"questions": {
			"escalate": {"type": "noul", "instructions": "Does this ticket need escalation?"},
			"team": {"type": "choice", "instructions": "Which team should handle this ticket?", "criteria": {"billing": "Charges and refunds", "engineering": "Bugs and outages"}},
			"severity": {"type": "score", "instructions": "How severe is this incident?", "criteria": ["Minor", "Major", "Critical"]}
		},
		"options": {"version": "1.0"}
	}`), &wantBody); err != nil {
		t.Fatalf("decode want body: %v", err)
	}
	if !reflect.DeepEqual(gotBody, wantBody) {
		t.Errorf("sent body = %s, want no model and the pinned version", sent.body)
	}

	if resp.Model != "ai_decide" {
		t.Errorf("model = %q, want the requested ai_decide", resp.Model)
	}
	if len(resp.Answers) != 3 {
		t.Fatalf("answers = %+v, want 3", resp.Answers)
	}
	if p := resp.Answers[0].Probability; p == nil || *p != 0.83 {
		t.Errorf("escalate probability = %v, want 0.83 from the noul probability", p)
	}
	if c := resp.Answers[1].Choice; c == nil || c.Str == nil || *c.Str != "engineering" {
		t.Errorf("team choice = %+v, want engineering", c)
	}
	if s := resp.Answers[2].Score; s == nil || *s != 1.82 {
		t.Errorf("severity score = %v, want 1.82", s)
	}
	if resp.Usage != nil && resp.Usage.TotalTokens != 0 {
		t.Errorf("usage = %+v, want none: ai_decide reports no usage", resp.Usage)
	}
}

// databricksSystemOneResponseBody is a Unity Gateway systemone reply for
// newDatabricksDecisionRequest: Typesafe's answers map, the model as the gateway reports
// it, and usage. The shape is as returned live; the values are synthetic.
const databricksSystemOneResponseBody = `{"model":"databricks-openjev-qwen35-4b","answers":{
	"escalate":{"type":"noul","noul":0.83},
	"team":{"type":"choice","choice":"engineering","confidence":0.99,"probabilities":{"billing":0.0,"engineering":1.0}},
	"severity":{"type":"score","score":1.82,"confidence":0.82,"legend":{"0":"Minor","1":"Major","2":"Critical"},"probabilities":{"0":0.02,"1":0.14,"2":0.84}}
},"usage":{"input_tokens":66,"output_tokens":0}}`

// TestDatabricksDecisionSendsSystemOneBody pins the System One call: an OpenJev model goes
// to the workspace's Unity Gateway /ai-gateway/typesafe/v1/systemone with the PAT, the body
// is Typesafe's systemone request with the Unity Catalog name as the model, a text-only
// request carries no images header, and the reply's answers and usage are read as Typesafe's.
func TestDatabricksDecisionSendsSystemOneBody(t *testing.T) {
	t.Parallel()

	type capturedRequest struct {
		path, auth, images string
		body               []byte
	}
	// The handler runs on the server's goroutine, so the request is handed over on a channel.
	captured := make(chan capturedRequest, 1)
	provider, server := newStubProvider(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captured <- capturedRequest{path: r.URL.Path, auth: r.Header.Get("Authorization"), images: r.Header.Get("TypeSafe-Danger-Zone-Images"), body: body}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(databricksSystemOneResponseBody))
	})

	request := newDatabricksDecisionRequest(schemas.DecisionInput{Text: schemas.Ptr("Checkout is down for every customer.")})
	request.Model = "system.ai.openjev-qwen35-4b"
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bErr := provider.Decision(ctx, newDatabricksDecisionKey(t, server), request)
	if bErr != nil {
		t.Fatalf("Decision returned an error: %v", bErr.Error)
	}
	sent := <-captured

	if sent.path != "/ai-gateway/typesafe/v1/systemone" {
		t.Errorf("path = %q, want /ai-gateway/typesafe/v1/systemone", sent.path)
	}
	if sent.auth != "Bearer dapi-secret-token" {
		t.Errorf("Authorization = %q, want the PAT as Bearer", sent.auth)
	}
	if sent.images != "" {
		t.Errorf("TypeSafe-Danger-Zone-Images = %q, want none on a text-only request", sent.images)
	}
	var gotBody, wantBody map[string]any
	if err := json.Unmarshal(sent.body, &gotBody); err != nil {
		t.Fatalf("decode sent body: %v", err)
	}
	if err := json.Unmarshal([]byte(`{
		"model": "system.ai.openjev-qwen35-4b",
		"state": "Checkout is down for every customer.",
		"questions": {
			"escalate": {"type": "noul", "instructions": "Does this ticket need escalation?"},
			"team": {"type": "choice", "instructions": "Which team should handle this ticket?", "criteria": {"billing": "Charges and refunds", "engineering": "Bugs and outages"}},
			"severity": {"type": "score", "instructions": "How severe is this incident?", "criteria": ["Minor", "Major", "Critical"]}
		}
	}`), &wantBody); err != nil {
		t.Fatalf("decode want body: %v", err)
	}
	if !reflect.DeepEqual(gotBody, wantBody) {
		t.Errorf("sent body = %s, want Typesafe's systemone request with the Unity Catalog model", sent.body)
	}

	if len(resp.Answers) != 3 {
		t.Fatalf("answers = %+v, want 3", resp.Answers)
	}
	if p := resp.Answers[0].Probability; p == nil || *p != 0.83 {
		t.Errorf("escalate probability = %v, want 0.83", p)
	}
	if c := resp.Answers[1].Choice; c == nil || c.Str == nil || *c.Str != "engineering" {
		t.Errorf("team choice = %+v, want engineering", c)
	}
	if s := resp.Answers[2].Score; s == nil || *s != 1.82 {
		t.Errorf("severity score = %v, want 1.82", s)
	}
	if resp.Usage == nil || resp.Usage.PromptTokens != 66 {
		t.Errorf("usage = %+v, want 66 input tokens", resp.Usage)
	}
}

// TestDatabricksDecisionSystemOneSendsImagesByReference pins images on a System One model:
// an image part with an id reaches the gateway as a Typesafe asset, and the request carries
// Typesafe's images header, which Jev requires (live, Jev 1.14.0 rejects images without it).
func TestDatabricksDecisionSystemOneSendsImagesByReference(t *testing.T) {
	t.Parallel()

	type capturedRequest struct {
		images string
		body   []byte
	}
	captured := make(chan capturedRequest, 1)
	provider, server := newStubProvider(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captured <- capturedRequest{images: r.Header.Get("TypeSafe-Danger-Zone-Images"), body: body}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"jev-1.14.0","answers":{"red":{"type":"noul","noul":0.99}},"usage":{"input_tokens":1066,"output_tokens":54}}`))
	})

	request := &schemas.BifrostDecisionRequest{
		Provider: schemas.Databricks,
		Model:    "system.ai.typesafeai-jev-1-14-0",
		Input: schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{{Role: "user", Content: schemas.DecisionInputContent{Parts: []schemas.DecisionInputPart{
			{Type: schemas.DecisionInputPartTypeText, Text: schemas.NewDecisionText("The photo is img1.")},
			{Type: schemas.DecisionInputPartTypeImage, ID: schemas.Ptr("img1"), Data: schemas.DecisionData{File: []byte("\x89PNG\r\n\x1a\nIMG")}},
		}}}}},
		Questions: []schemas.DecisionQuestion{{Type: schemas.DecisionTypePredicate, Name: schemas.Ptr("red"), Instructions: schemas.NewDecisionText("Is img1 mostly red?")}},
	}
	resp, bErr := provider.Decision(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), newDatabricksDecisionKey(t, server), request)
	if bErr != nil {
		t.Fatalf("Decision returned an error: %v", bErr.Error)
	}
	sent := <-captured

	if sent.images != "true" {
		t.Errorf("TypeSafe-Danger-Zone-Images = %q, want true on a request with an image", sent.images)
	}
	var gotBody, wantBody map[string]any
	if err := json.Unmarshal(sent.body, &gotBody); err != nil {
		t.Fatalf("decode sent body: %v", err)
	}
	if err := json.Unmarshal([]byte(`{
		"model": "system.ai.typesafeai-jev-1-14-0",
		"state": "The photo is img1.",
		"assets": {"img1": {"type": "image", "format": "png", "data_base64": "iVBORw0KGgpJTUc="}},
		"questions": {"red": {"type": "noul", "instructions": "Is img1 mostly red?"}}
	}`), &wantBody); err != nil {
		t.Fatalf("decode want body: %v", err)
	}
	if !reflect.DeepEqual(gotBody, wantBody) {
		t.Errorf("sent body = %s, want the image as a Typesafe asset", sent.body)
	}
	if len(resp.Answers) != 1 {
		t.Fatalf("answers = %+v, want 1", resp.Answers)
	}
	if p := resp.Answers[0].Probability; p == nil || *p != 0.99 {
		t.Errorf("red probability = %v, want 0.99", p)
	}
}

// TestDatabricksDecisionSystemOnePassesDatabricksErrors pins that the Unity Gateway's own
// error reaches the caller as Databricks sent it: status, error_code and message. The body is
// as captured live for a score question with 11 levels.
func TestDatabricksDecisionSystemOnePassesDatabricksErrors(t *testing.T) {
	t.Parallel()

	provider, server := newStubProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error_code":"INTERNAL_ERROR","message":"Bad Request"}`))
	})

	request := newDatabricksDecisionRequest(schemas.DecisionInput{Text: schemas.Ptr("hi")})
	request.Model = "system.ai.openjev-qwen35-4b"
	_, bErr := provider.Decision(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), newDatabricksDecisionKey(t, server), request)
	if bErr == nil {
		t.Fatal("expected an error")
	}
	if bErr.StatusCode == nil || *bErr.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status = %v, want 422", bErr.StatusCode)
	}
	if bErr.Error == nil || bErr.Error.Code == nil || *bErr.Error.Code != "INTERNAL_ERROR" || bErr.Error.Message != "Bad Request" {
		t.Errorf("error = %+v, want Databricks' INTERNAL_ERROR and message", bErr.Error)
	}
}

// TestDatabricksDecisionRejectsLocally pins the requests answered without calling Databricks:
// a model other than ai_decide is unsupported (compat emulates it), an empty input is a 400
// because ai_decide answers a null state with no answers, and an image part is a 400 because
// ai_decide, like a System One model's systemone endpoint, reads it as JSON text.
func TestDatabricksDecisionRejectsLocally(t *testing.T) {
	t.Parallel()

	image := schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{{
		Role: "user",
		Content: schemas.DecisionInputContent{Parts: []schemas.DecisionInputPart{
			{Type: schemas.DecisionInputPartTypeImage, ImageURL: schemas.Ptr("data:image/png;base64,AAAA")},
		}},
	}}}
	cases := map[string]struct {
		model      string
		input      schemas.DecisionInput
		wantStatus int
	}{
		"serving endpoint model":           {model: "databricks-claude-sonnet-4", input: schemas.DecisionInput{Text: schemas.Ptr("hi")}},
		"empty input":                      {model: "ai_decide", wantStatus: http.StatusBadRequest},
		"image part":                       {model: "ai_decide", input: image, wantStatus: http.StatusBadRequest},
		"image part on a System One model": {model: "system.ai.openjev-qwen35-4b", input: image, wantStatus: http.StatusBadRequest},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			provider, server := newStubProvider(t, func(w http.ResponseWriter, r *http.Request) {
				t.Errorf("unexpected call to %s", r.URL.Path)
			})
			request := newDatabricksDecisionRequest(tc.input)
			request.Model = tc.model
			_, bErr := provider.Decision(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), newDatabricksDecisionKey(t, server), request)
			if bErr == nil {
				t.Fatal("expected an error")
			}
			if tc.wantStatus == 0 {
				if bErr.Error == nil || bErr.Error.Code == nil || *bErr.Error.Code != "unsupported_operation" {
					t.Errorf("error = %+v, want unsupported_operation", bErr.Error)
				}
				return
			}
			if bErr.StatusCode == nil || *bErr.StatusCode != tc.wantStatus {
				t.Errorf("status = %v, want %d (%+v)", bErr.StatusCode, tc.wantStatus, bErr.Error)
			}
		})
	}
}

// TestDatabricksDecisionPassesDatabricksErrorsThrough pins that ai_decide's own validation
// errors reach the caller with their status, code and message, and that its response headers
// are kept on the context for error replies too.
func TestDatabricksDecisionPassesDatabricksErrorsThrough(t *testing.T) {
	t.Parallel()

	provider, server := newStubProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-request-id", "req-400")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error_code":"BAD_REQUEST","message":"Invalid decision request: question 's' of type 'score' requires 'criteria' to be an ordered array of 2 to 10 level descriptions, but got an array of 11 level(s)."}`))
	})

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bErr := provider.Decision(ctx, newDatabricksDecisionKey(t, server), newDatabricksDecisionRequest(schemas.DecisionInput{Text: schemas.Ptr("hi")}))
	if bErr == nil {
		t.Fatal("expected an error")
	}
	if bErr.StatusCode == nil || *bErr.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %v, want 400", bErr.StatusCode)
	}
	if bErr.Error == nil || bErr.Error.Code == nil || *bErr.Error.Code != "BAD_REQUEST" || !strings.Contains(bErr.Error.Message, "2 to 10 level descriptions") {
		t.Errorf("error = %+v, want Databricks' BAD_REQUEST and message", bErr.Error)
	}
	headers, _ := ctx.Value(schemas.BifrostContextKeyProviderResponseHeaders).(map[string]string)
	found := false
	for k, v := range headers {
		if strings.EqualFold(k, "x-request-id") && v == "req-400" {
			found = true
		}
	}
	if !found {
		t.Errorf("context headers = %v, want x-request-id from the error reply", headers)
	}
}

// TestDatabricksDecisionWithoutAnswersFails pins that a 200 carrying "response": null, which
// ai_decide returns for a null state, is an error rather than an empty decision.
func TestDatabricksDecisionWithoutAnswersFails(t *testing.T) {
	t.Parallel()

	resp, bErr := databricks.ToBifrostDecisionResponse(&databricks.DatabricksDecisionResponse{}, newDatabricksDecisionRequest(schemas.DecisionInput{Text: schemas.Ptr("hi")}))
	if bErr == nil || bErr.Error == nil || bErr.Error.Message != "databricks returned no answers" {
		t.Errorf("got %+v, %+v; want the no-answers error", resp, bErr)
	}
}
