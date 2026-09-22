package groq_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/groq"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

func TestGroq(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("GROQ_API_KEY")) == "" {
		t.Skip("Skipping Groq tests because GROQ_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:  schemas.Groq,
		ChatModel: "qwen/qwen3.8-27b",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Groq, Model: "openai/gpt-oss-120b"},
		},
		TextModel: "qwen/qwen3.8-27b",
		TextCompletionFallbacks: []schemas.Fallback{
			{Provider: schemas.Groq, Model: "openai/gpt-oss-20b"},
		},
		EmbeddingModel:       "", // Groq doesn't support embedding
		ReasoningModel:       "openai/gpt-oss-120b",
		TranscriptionModel:   "whisper-large-v3",
		SpeechSynthesisModel: "canopylabs/orpheus-v1-english",
		Scenarios: llmtests.TestScenarios{
			TextCompletion:             false,
			TextCompletionStream:       false,
			SimpleChat:                 true,
			CompletionStream:           true,
			MultiTurnConversation:      true,
			ToolCalls:                  true,
			ToolCallsStreaming:         true,
			MultipleToolCalls:          true,
			MultipleToolCallsStreaming: true,
			End2EndToolCalling:         true,
			AutomaticFunctionCall:      true,
			ImageURL:                   false,
			ImageBase64:                false,
			MultipleImages:             false,
			FileBase64:                 false, // Not supported
			FileURL:                    false, // Not supported
			CompleteEnd2End:            true,
			Embedding:                  false,
			ListModels:                 true,
			Reasoning:                  true,
			Transcription:              true,
			SpeechSynthesis:            true,
		},
	}
	t.Run("GroqTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

// A retrieve must land on /v1/models/{model} with the key's bearer token, map the
// provider's model object onto the Bifrost shape, and refuse a path-shaping model id
// before anything is dispatched.
func TestGroqModelRetrieve(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var gotPath, gotMethod, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath, gotMethod, gotAuth = r.URL.Path, r.Method, r.Header.Get("Authorization")
		mu.Unlock()
		_, _ = w.Write([]byte(`{"id":"llama-3.3-70b-versatile","object":"model","created":1693721698,"owned_by":"Meta","context_window":131072}`))
	}))
	defer server.Close()

	provider, err := groq.NewGroqProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL, DefaultRequestTimeoutInSeconds: 30},
	}, bifrost.NewNoOpLogger())
	require.NoError(t, err)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: *schemas.NewSecretVar("gsk-test")}

	response, bifrostErr := provider.ModelRetrieve(ctx, key, &schemas.BifrostModelRetrieveRequest{Model: "llama-3.3-70b-versatile"})
	require.Nil(t, bifrostErr)

	mu.Lock()
	path, method, auth := gotPath, gotMethod, gotAuth
	mu.Unlock()
	require.Equal(t, "/v1/models/llama-3.3-70b-versatile", path)
	require.Equal(t, http.MethodGet, method)
	require.Equal(t, "Bearer gsk-test", auth)

	require.Equal(t, "groq/llama-3.3-70b-versatile", response.ID)
	require.Equal(t, schemas.Ptr("Meta"), response.OwnedBy)
	require.Equal(t, schemas.Ptr(131072), response.ContextLength)

	_, bifrostErr = provider.ModelRetrieve(ctx, key, &schemas.BifrostModelRetrieveRequest{Model: "../models"})
	require.NotNil(t, bifrostErr)
	require.Equal(t, schemas.Ptr(http.StatusBadRequest), bifrostErr.StatusCode)
}
