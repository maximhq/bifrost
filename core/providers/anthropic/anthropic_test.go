package anthropic_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/anthropic"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

func TestAnthropic(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")) == "" {
		t.Skip("Skipping Anthropic tests because ANTHROPIC_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:  schemas.Anthropic,
		ChatModel: "claude-sonnet-4-5",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Anthropic, Model: "claude-3-7-sonnet-20250219"},
			{Provider: schemas.Anthropic, Model: "claude-sonnet-4-6"},
		},
		VisionModel:        "claude-sonnet-4-5", // Same model supports vision
		ReasoningModel:     "claude-opus-4-5",
		PromptCachingModel: "claude-sonnet-4-6",
		PassthroughModel:   "claude-sonnet-4-5",
		Scenarios: llmtests.TestScenarios{
			TextCompletion:               false, // Not supported
			SimpleChat:                   true,
			CompletionStream:             true,
			MultiTurnConversation:        true,
			ToolCalls:                    true,
			ToolCallsStreaming:           true,
			MultipleToolCalls:            true,
			MultipleToolCallsStreaming:   true,
			End2EndToolCalling:           true,
			AutomaticFunctionCall:        true,
			WebSearchTool:                true,
			ImageURL:                     true,
			ImageBase64:                  true,
			MultipleImages:               true,
			FileBase64:                   true,
			FileURL:                      true,
			CompleteEnd2End:              true,
			Embedding:                    false,
			Reasoning:                    true,
			PromptCaching:                true,
			ListModels:                   true,
			BatchCreate:                  true,
			BatchList:                    true,
			BatchRetrieve:                true,
			BatchCancel:                  true,
			BatchResults:                 true,
			FileUpload:                   true,
			FileList:                     true,
			FileRetrieve:                 true,
			FileDelete:                   true,
			FileContent:                  false,
			FileBatchInput:               false, // Anthropic batch API only supports inline requests, not file-based input
			CountTokens:                  true,
			StructuredOutputs:            true, // Structured outputs with nullable enum support
			PassthroughAPI:               true,
			Compaction:                   true,
			ToolSearch:                   true,
			InterleavedThinking:          true,
			FastMode:                     false, // Enable when test API key has Opus 4.6 access
			EagerInputStreaming:          true,  // fine-grained-tool-streaming-2025-05-14 (GA on Anthropic)
			ServerToolsViaOpenAIEndpoint: true,  // web_search / web_fetch / code_execution via /v1/chat/completions
		},
	}

	t.Run("AnthropicTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

// A retrieve must send Anthropic's own auth headers, return the dated model an alias
// resolves to, carry created_at as a Unix timestamp, surface an upstream 404, and refuse
// a path-shaping id before anything is dispatched.
func TestAnthropicModelRetrieve(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var gotPath, gotAPIKey, gotVersion, gotAuthorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath, gotAPIKey = r.URL.Path, r.Header.Get("x-api-key")
		gotVersion, gotAuthorization = r.Header.Get("anthropic-version"), r.Header.Get("Authorization")
		mu.Unlock()
		if r.URL.Path == "/v1/models/nope" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"not_found_error","message":"model: nope"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"type":"model","id":"claude-sonnet-4-5-20250929","display_name":"Claude Sonnet 4.5","created_at":"2025-09-29T00:00:00Z","max_input_tokens":200000,"max_tokens":64000,"capabilities":{"batch":{"supported":true}}}`))
	}))
	defer server.Close()

	provider := anthropic.NewAnthropicProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL, DefaultRequestTimeoutInSeconds: 30},
	}, bifrost.NewNoOpLogger())

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: *schemas.NewSecretVar("sk-ant-test")}

	response, bifrostErr := provider.ModelRetrieve(ctx, key, &schemas.BifrostModelRetrieveRequest{Model: "claude-sonnet-4-5"})
	require.Nil(t, bifrostErr)

	mu.Lock()
	path, apiKey, version, authorization := gotPath, gotAPIKey, gotVersion, gotAuthorization
	mu.Unlock()
	require.Equal(t, "/v1/models/claude-sonnet-4-5", path)
	require.Equal(t, "sk-ant-test", apiKey)
	require.Equal(t, "2023-06-01", version)
	require.Empty(t, authorization, "Anthropic authenticates with x-api-key, not a bearer token")

	require.Equal(t, "anthropic/claude-sonnet-4-5-20250929", response.ID)
	require.Equal(t, schemas.Ptr("Claude Sonnet 4.5"), response.Name)
	require.Equal(t, schemas.Ptr(time.Date(2025, 9, 29, 0, 0, 0, 0, time.UTC).Unix()), response.Created)
	require.Equal(t, schemas.Ptr(200000), response.MaxInputTokens)
	require.Equal(t, schemas.Ptr(64000), response.MaxOutputTokens)

	_, bifrostErr = provider.ModelRetrieve(ctx, key, &schemas.BifrostModelRetrieveRequest{Model: "nope"})
	require.NotNil(t, bifrostErr)
	require.Equal(t, schemas.Ptr(http.StatusNotFound), bifrostErr.StatusCode)

	_, bifrostErr = provider.ModelRetrieve(ctx, key, &schemas.BifrostModelRetrieveRequest{Model: "../models"})
	require.NotNil(t, bifrostErr)
	require.Equal(t, schemas.Ptr(http.StatusBadRequest), bifrostErr.StatusCode)
}
