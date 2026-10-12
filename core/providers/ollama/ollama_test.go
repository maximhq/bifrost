package ollama_test

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
	"github.com/maximhq/bifrost/core/providers/ollama"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

func TestOllama(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("OLLAMA_BASE_URL")) == "" {
		t.Skip("Skipping Ollama tests because OLLAMA_BASE_URL is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:       schemas.Ollama,
		ChatModel:      "llama3.1:latest",
		TextModel:      "", // Ollama doesn't support text completion in newer models
		EmbeddingModel: "", // Ollama doesn't support embedding
		Scenarios: llmtests.TestScenarios{
			TextCompletion:             false, // Not supported
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
			FileBase64:                 false,
			FileURL:                    false,
			CompleteEnd2End:            true,
			Embedding:                  false,
			ListModels:                 true,
		},
	}

	t.Run("OllamaTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

// A retrieve must use the key's own Ollama URL over the provider-level one, keep a
// tagged model name ("llama3.2:3b") in one path segment, and refuse a path-shaping id.
func TestOllamaModelRetrieve(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var gotPath string
	keyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath = r.URL.Path
		mu.Unlock()
		_, _ = w.Write([]byte(`{"id":"llama3.2:3b","object":"model","created":1727740800,"owned_by":"library"}`))
	}))
	defer keyServer.Close()
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("request reached the provider-level base URL instead of the key's: %s", r.URL.Path)
	}))
	defer providerServer.Close()

	provider, err := ollama.NewOllamaProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: providerServer.URL, DefaultRequestTimeoutInSeconds: 30},
	}, bifrost.NewNoOpLogger())
	require.NoError(t, err)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{OllamaKeyConfig: &schemas.OllamaKeyConfig{URL: *schemas.NewSecretVar(keyServer.URL)}}

	response, bifrostErr := provider.ModelRetrieve(ctx, key, &schemas.BifrostModelRetrieveRequest{Model: "llama3.2:3b"})
	require.Nil(t, bifrostErr)

	mu.Lock()
	path := gotPath
	mu.Unlock()
	require.Equal(t, "/v1/models/llama3.2:3b", path)
	require.Equal(t, "ollama/llama3.2:3b", response.ID)
	require.Equal(t, schemas.Ptr("library"), response.OwnedBy)

	_, bifrostErr = provider.ModelRetrieve(ctx, key, &schemas.BifrostModelRetrieveRequest{Model: "../api/tags"})
	require.NotNil(t, bifrostErr)
	require.Equal(t, schemas.Ptr(http.StatusBadRequest), bifrostErr.StatusCode)
}
