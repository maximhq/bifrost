package cohere_test

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
	"github.com/maximhq/bifrost/core/providers/cohere"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

func TestCohere(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("COHERE_API_KEY")) == "" {
		t.Skip("Skipping Cohere tests because COHERE_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:                 schemas.Cohere,
		ChatModel:                "command-a-03-2025",
		VisionModel:              "command-a-vision-07-2025", // Cohere's latest vision model
		TextModel:                "",                         // Cohere focuses on chat
		EmbeddingModel:           "embed-v4.0",
		MultimodalEmbeddingModel: "embed-v4.0",
		RerankModel:              "rerank-v3.5",
		ReasoningModel:           "command-a-reasoning-08-2025",
		Scenarios: llmtests.TestScenarios{
			TextCompletion:             false, // Not typical for Cohere
			SimpleChat:                 true,
			CompletionStream:           true,
			MultiTurnConversation:      true,
			ToolCalls:                  true,
			ToolCallsStreaming:         true,
			MultipleToolCalls:          true,
			MultipleToolCallsStreaming: true,
			End2EndToolCalling:         true,
			AutomaticFunctionCall:      true,  // May not support automatic
			ImageURL:                   false, // Supported by c4ai-aya-vision-8b model
			ImageBase64:                true,  // Supported by c4ai-aya-vision-8b model
			MultipleImages:             false, // Supported by c4ai-aya-vision-8b model
			FileBase64:                 false, // Not supported
			FileURL:                    false, // Not supported
			CompleteEnd2End:            false,
			Embedding:                  true,
			MultimodalEmbedding:        true,
			Rerank:                     true,
			Reasoning:                  true,
			ListModels:                 true,
			CountTokens:                true,
		},
	}

	t.Run("CohereTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

// A retrieve must land on /v1/models/{model} with the key's bearer token, carry the
// deprecation flag through, surface an upstream 404, and refuse a path-shaping id.
func TestCohereModelRetrieve(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var gotPath, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		mu.Unlock()
		if r.URL.Path == "/v1/models/nope" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"model 'nope' not found"}`))
			return
		}
		_, _ = w.Write([]byte(`{"name":"command-r-08-2024","is_deprecated":true,"endpoints":["chat","generate"],"finetuned":false,"context_length":128000,"tokenizer_url":"https://example.invalid/tokenizer.json","default_endpoints":["chat"],"features":["tools","json_mode"]}`))
	}))
	defer server.Close()

	provider, err := cohere.NewCohereProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL, DefaultRequestTimeoutInSeconds: 30},
	}, bifrost.NewNoOpLogger())
	require.NoError(t, err)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: *schemas.NewSecretVar("co-test")}

	response, bifrostErr := provider.ModelRetrieve(ctx, key, &schemas.BifrostModelRetrieveRequest{Model: "command-r-08-2024"})
	require.Nil(t, bifrostErr)

	mu.Lock()
	path, auth := gotPath, gotAuth
	mu.Unlock()
	require.Equal(t, "/v1/models/command-r-08-2024", path)
	require.Equal(t, "Bearer co-test", auth)

	require.Equal(t, "cohere/command-r-08-2024", response.ID)
	require.Equal(t, schemas.Ptr(128000), response.ContextLength)
	require.True(t, response.IsDeprecated)
	require.Equal(t, []string{"chat", "generate"}, response.SupportedMethods)

	_, bifrostErr = provider.ModelRetrieve(ctx, key, &schemas.BifrostModelRetrieveRequest{Model: "nope"})
	require.NotNil(t, bifrostErr)
	require.Equal(t, schemas.Ptr(http.StatusNotFound), bifrostErr.StatusCode)

	_, bifrostErr = provider.ModelRetrieve(ctx, key, &schemas.BifrostModelRetrieveRequest{Model: "../models"})
	require.NotNil(t, bifrostErr)
	require.Equal(t, schemas.Ptr(http.StatusBadRequest), bifrostErr.StatusCode)
}
