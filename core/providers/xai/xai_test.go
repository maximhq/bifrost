package xai_test

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
	"github.com/maximhq/bifrost/core/providers/xai"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

func TestXAI(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("XAI_API_KEY")) == "" {
		t.Skip("Skipping XAI tests because XAI_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:                schemas.XAI,
		ChatModel:               "grok-4-0709",
		ReasoningModel:          "grok-3-mini",
		TextModel:               "", // xAI dropped raw sampling (/v1/completions); all current models are reasoning models
		VisionModel:             "grok-4-1-fast-reasoning",
		EmbeddingModel:          "", // XAI doesn't support embedding
		ImageGenerationModel:    "grok-imagine-image",
		ExternalCompactionModel: "grok-4.3",
		Scenarios: llmtests.TestScenarios{
			TextCompletion:             true,
			SimpleChat:                 true,
			CompletionStream:           true,
			MultiTurnConversation:      true,
			ToolCalls:                  true,
			ToolCallsStreaming:         true,
			MultipleToolCalls:          true,
			MultipleToolCallsStreaming: true,
			End2EndToolCalling:         true,
			AutomaticFunctionCall:      true,
			ImageURL:                   true,
			ImageBase64:                true,
			ImageGeneration:            true,
			ImageGenerationStream:      false,
			FileBase64:                 false,
			FileURL:                    false,
			MultipleImages:             true,
			CompleteEnd2End:            true,
			Reasoning:                  true,
			Embedding:                  false,
			ListModels:                 true,
			ExternalCompaction:         true,
		},
	}

	t.Run("XAITests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

// A retrieve must land on /v1/models/{model} with the key's bearer token, map the
// provider's model object onto the Bifrost shape, and refuse a path-shaping model id
// before anything is dispatched.
func TestXAIModelRetrieve(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var gotPath, gotMethod, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath, gotMethod, gotAuth = r.URL.Path, r.Method, r.Header.Get("Authorization")
		mu.Unlock()
		_, _ = w.Write([]byte(`{"id":"grok-4","object":"model","created":1743724800,"owned_by":"xai"}`))
	}))
	defer server.Close()

	provider, err := xai.NewXAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL, DefaultRequestTimeoutInSeconds: 30},
	}, bifrost.NewNoOpLogger())
	require.NoError(t, err)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: *schemas.NewSecretVar("xai-test")}

	response, bifrostErr := provider.ModelRetrieve(ctx, key, &schemas.BifrostModelRetrieveRequest{Model: "grok-4"})
	require.Nil(t, bifrostErr)

	mu.Lock()
	path, method, auth := gotPath, gotMethod, gotAuth
	mu.Unlock()
	require.Equal(t, "/v1/models/grok-4", path)
	require.Equal(t, http.MethodGet, method)
	require.Equal(t, "Bearer xai-test", auth)

	require.Equal(t, "xai/grok-4", response.ID)
	require.Equal(t, schemas.Ptr("xai"), response.OwnedBy)
	require.Equal(t, schemas.Ptr(int64(1743724800)), response.Created)

	_, bifrostErr = provider.ModelRetrieve(ctx, key, &schemas.BifrostModelRetrieveRequest{Model: "../models"})
	require.NotNil(t, bifrostErr)
	require.Equal(t, schemas.Ptr(http.StatusBadRequest), bifrostErr.StatusCode)
}
