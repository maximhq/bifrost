package deepseek_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/internal/llmtests"
	deepseekprovider "github.com/maximhq/bifrost/core/providers/deepseek"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeepSeek(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY")) == "" {
		t.Skip("Skipping DeepSeek tests because DEEPSEEK_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:  schemas.DeepSeek,
		ChatModel: "deepseek-v4-flash",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.DeepSeek, Model: "deepseek-v4-pro"},
		},
		Scenarios: llmtests.TestScenarios{
			TextCompletion:             false,
			TextCompletionStream:       false,
			SimpleChat:                 true,
			CompletionStream:           true,
			MultiTurnConversation:      true,
			ToolCalls:                  true,
			ToolCallsStreaming:         true,
			MultipleToolCalls:          false,
			MultipleToolCallsStreaming: false,
			End2EndToolCalling:         true,
			AutomaticFunctionCall:      true,
			ImageURL:                   false,
			ImageBase64:                false,
			MultipleImages:             false,
			CompleteEnd2End:            true,
			Embedding:                  false,
			Reasoning:                  false,
			ListModels:                 true,
		},
	}

	t.Run("DeepSeekTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

func newTestDeepSeekProvider(t *testing.T, baseURL string) *deepseekprovider.DeepSeekProvider {
	t.Helper()

	provider, err := deepseekprovider.NewDeepSeekProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        baseURL,
			DefaultRequestTimeoutInSeconds: 30,
		},
	}, bifrost.NewNoOpLogger())
	require.NoError(t, err)
	return provider
}

func TestDeepSeekChatCompletion_UsesRootChatPath(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/chat/completions", r.URL.Path)
		assert.Equal(t, "Bearer test-api-key", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"chatcmpl-test","object":"chat.completion","created":1234567890,"model":"deepseek-v4-pro","choices":[{"index":0,"message":{"role":"assistant","content":"Hello from DeepSeek"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":4,"total_tokens":9}}`)
	}))
	defer server.Close()

	provider := newTestDeepSeekProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	content := "Hello"

	resp, bifrostErr := provider.ChatCompletion(ctx, schemas.Key{Value: schemas.EnvVar{Val: "test-api-key"}}, &schemas.BifrostChatRequest{
		Provider: schemas.DeepSeek,
		Model:    "deepseek-v4-pro",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: &content},
		}},
	})

	require.Nil(t, bifrostErr)
	require.NotNil(t, resp)
	require.Len(t, resp.Choices, 1)
	require.Equal(t, "Hello from DeepSeek", *resp.Choices[0].ChatNonStreamResponseChoice.Message.Content.ContentStr)
}

func TestDeepSeekListModels_UsesRootModelsPath(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/models", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"list","data":[{"id":"deepseek-v4-pro","object":"model","owned_by":"deepseek"}]}`)
	}))
	defer server.Close()

	provider := newTestDeepSeekProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	resp, bifrostErr := provider.ListModels(ctx, []schemas.Key{{Value: schemas.EnvVar{Val: "test-api-key"}}}, &schemas.BifrostListModelsRequest{Unfiltered: true})

	require.Nil(t, bifrostErr)
	require.NotNil(t, resp)
	require.Len(t, resp.Data, 1)
	require.Equal(t, "deepseek/deepseek-v4-pro", resp.Data[0].ID)
}

func TestDeepSeekTextCompletion_IsUnsupportedInFirstPass(t *testing.T) {
	t.Parallel()

	provider := newTestDeepSeekProvider(t, "https://api.deepseek.com")
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	resp, bifrostErr := provider.TextCompletion(ctx, schemas.Key{Value: schemas.EnvVar{Val: "test-api-key"}}, &schemas.BifrostTextCompletionRequest{})

	require.Nil(t, resp)
	require.NotNil(t, bifrostErr)
	require.NotNil(t, bifrostErr.Error)
	require.Equal(t, "unsupported_operation", *bifrostErr.Error.Code)
}
