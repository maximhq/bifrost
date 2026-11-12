package runcrate_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/runcrate"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

func TestRuncrate(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("RUNCRATE_API_KEY")) == "" {
		t.Skip("Skipping Runcrate tests because RUNCRATE_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:  schemas.Runcrate,
		ChatModel: "deepseek-ai/DeepSeek-V4-Flash",
		TextModel: "meta-llama/Llama-3.3-70B-Instruct-Turbo",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Runcrate, Model: "meta-llama/Llama-3.3-70B-Instruct-Turbo"},
		},
		EmbeddingModel: "BAAI/bge-large-en-v1.5",
		Scenarios: llmtests.TestScenarios{
			TextCompletion:        true,
			TextCompletionStream:  false, // Runcrate streams /v1/completions as chat chunks
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              false,
			ImageBase64:           false,
			MultipleImages:        false,
			CompleteEnd2End:       true,
			Embedding:             true,
			ListModels:            true,
		},
	}

	t.Run("RuncrateTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

// seenRequest records what the fake Runcrate endpoint received.
type seenRequest struct {
	path, method, auth, body string
}

func newRuncrateServer(t *testing.T) (*httptest.Server, func() seenRequest) {
	t.Helper()
	var mu sync.Mutex
	var seen seenRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = seenRequest{path: r.URL.Path, method: r.Method, auth: r.Header.Get("Authorization"), body: string(body)}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/chat/completions":
			_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","created":1794113863,"model":"meta-llama/Llama-3.3-70B-Instruct-Turbo","choices":[{"index":0,"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}`))
		case "/v1/embeddings":
			_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.5,0.25]}],"model":"BAAI/bge-large-en-v1.5","usage":{"prompt_tokens":2,"total_tokens":2}}`))
		case "/v1/models":
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"meta-llama/Llama-3.3-70B-Instruct-Turbo","object":"model","owned_by":"meta-llama"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server, func() seenRequest {
		mu.Lock()
		defer mu.Unlock()
		return seen
	}
}

func newRuncrateProvider(t *testing.T, baseURL string) *runcrate.RuncrateProvider {
	t.Helper()
	provider, err := runcrate.NewRuncrateProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: baseURL, DefaultRequestTimeoutInSeconds: 30},
	}, bifrost.NewNoOpLogger())
	require.NoError(t, err)
	return provider
}

func TestRuncrateProviderKey(t *testing.T) {
	t.Parallel()
	provider := newRuncrateProvider(t, "")
	require.Equal(t, schemas.Runcrate, provider.GetProviderKey())
}

func TestRuncrateChatCompletion(t *testing.T) {
	t.Parallel()
	server, seen := newRuncrateServer(t)
	// A trailing slash on the configured base URL must not produce a double slash.
	provider := newRuncrateProvider(t, server.URL+"/")

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: *schemas.NewSecretVar("rc-test")}

	resp, bifrostErr := provider.ChatCompletion(ctx, key, &schemas.BifrostChatRequest{
		Provider: schemas.Runcrate,
		Model:    "meta-llama/Llama-3.3-70B-Instruct-Turbo",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Say hi")},
		}},
	})
	require.Nil(t, bifrostErr)
	require.NotNil(t, resp)
	require.Len(t, resp.Choices, 1)
	require.Equal(t, "Hi", *resp.Choices[0].ChatNonStreamResponseChoice.Message.Content.ContentStr)

	got := seen()
	require.Equal(t, "/v1/chat/completions", got.path)
	require.Equal(t, http.MethodPost, got.method)
	require.Equal(t, "Bearer rc-test", got.auth)
	require.Contains(t, got.body, `"model":"meta-llama/Llama-3.3-70B-Instruct-Turbo"`)
}

func TestRuncrateEmbedding(t *testing.T) {
	t.Parallel()
	server, seen := newRuncrateServer(t)
	provider := newRuncrateProvider(t, server.URL)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: *schemas.NewSecretVar("rc-test")}

	resp, bifrostErr := provider.Embedding(ctx, key, &schemas.BifrostEmbeddingRequest{
		Provider: schemas.Runcrate,
		Model:    "BAAI/bge-large-en-v1.5",
		Input: []schemas.EmbeddingInputItem{{
			Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: schemas.Ptr("hello")}},
		}},
	})
	require.Nil(t, bifrostErr)
	require.NotNil(t, resp)
	require.Len(t, resp.Data, 1)

	got := seen()
	require.Equal(t, "/v1/embeddings", got.path)
	require.Equal(t, "Bearer rc-test", got.auth)
	require.True(t, strings.Contains(got.body, `"BAAI/bge-large-en-v1.5"`), got.body)
}

func TestRuncrateListModels(t *testing.T) {
	t.Parallel()
	server, seen := newRuncrateServer(t)
	provider := newRuncrateProvider(t, server.URL)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: *schemas.NewSecretVar("rc-test"), Models: []string{"*"}}

	resp, bifrostErr := provider.ListModels(ctx, []schemas.Key{key}, &schemas.BifrostListModelsRequest{Provider: schemas.Runcrate})
	require.Nil(t, bifrostErr)
	require.NotNil(t, resp)
	require.Len(t, resp.Data, 1)
	require.Equal(t, "runcrate/meta-llama/Llama-3.3-70B-Instruct-Turbo", resp.Data[0].ID)
	require.Equal(t, "/v1/models", seen().path)
}

func TestRuncrateUnsupportedOperation(t *testing.T) {
	t.Parallel()
	provider := newRuncrateProvider(t, "")
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	_, bifrostErr := provider.Speech(ctx, schemas.Key{}, &schemas.BifrostSpeechRequest{Provider: schemas.Runcrate})
	require.NotNil(t, bifrostErr)

	_, bifrostErr = provider.TextCompletionStream(ctx, nil, nil, schemas.Key{}, &schemas.BifrostTextCompletionRequest{Provider: schemas.Runcrate})
	require.NotNil(t, bifrostErr)
}
