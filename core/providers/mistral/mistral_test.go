package mistral_test

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
	"github.com/maximhq/bifrost/core/providers/mistral"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

func TestMistral(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("MISTRAL_API_KEY")) == "" {
		t.Skip("Skipping Mistral tests because MISTRAL_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:  schemas.Mistral,
		ChatModel: "ministral-8b-latest",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Mistral, Model: "ministral-3b-latest"},
		},
		VisionModel:         "pixtral-12b-latest",
		EmbeddingModel:      "codestral-embed",
		TranscriptionModel:  "voxtral-mini-latest", // Mistral's audio transcription model
		ExternalTTSProvider: schemas.OpenAI,
		ExternalTTSModel:    "gpt-4o-mini-tts",
		Scenarios: llmtests.TestScenarios{
			TextCompletion:        false, // Not supported
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
			MultipleImages:        true,
			FileBase64:            false, // supports documents url
			FileURL:               false, // bifrost limitation: native mistral api converter needed
			CompleteEnd2End:       true,
			Embedding:             true,
			Transcription:         true,
			TranscriptionStream:   true,
			ListModels:            true,
			Reasoning:             false, // Not supported right now because we are not using native mistral converters
		},
	}

	t.Run("MistralTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

// A retrieve must land on /v1/models/{model} with the key's bearer token, map Mistral's
// model card onto the Bifrost shape, surface an upstream 404, and refuse a path-shaping id.
func TestMistralModelRetrieve(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var gotPath, gotMethod, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath, gotMethod, gotAuth = r.URL.Path, r.Method, r.Header.Get("Authorization")
		mu.Unlock()
		if r.URL.Path == "/v1/models/nope" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"object":"error","message":"Invalid model: nope","type":"invalid_model","code":"1500"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"mistral-large-2411","object":"model","created":1731974400,"owned_by":"mistralai","name":"Mistral Large","description":"Top-tier reasoning model","max_context_length":131072,"aliases":["mistral-large-latest"],"capabilities":{"completion_chat":true,"function_calling":true},"type":"base"}`))
	}))
	defer server.Close()

	provider := mistral.NewMistralProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL, DefaultRequestTimeoutInSeconds: 30},
	}, bifrost.NewNoOpLogger())

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: *schemas.NewSecretVar("mistral-test")}

	// Retrieving an alias returns the concrete model it points at.
	response, bifrostErr := provider.ModelRetrieve(ctx, key, &schemas.BifrostModelRetrieveRequest{Model: "mistral-large-latest"})
	require.Nil(t, bifrostErr)

	mu.Lock()
	path, method, auth := gotPath, gotMethod, gotAuth
	mu.Unlock()
	require.Equal(t, "/v1/models/mistral-large-latest", path)
	require.Equal(t, http.MethodGet, method)
	require.Equal(t, "Bearer mistral-test", auth)

	require.Equal(t, "mistral/mistral-large-2411", response.ID)
	require.Equal(t, schemas.Ptr("Mistral Large"), response.Name)
	require.Equal(t, schemas.Ptr(131072), response.ContextLength)
	require.Equal(t, schemas.Ptr(int64(1731974400)), response.Created)
	require.Equal(t, schemas.Ptr("mistralai"), response.OwnedBy)

	_, bifrostErr = provider.ModelRetrieve(ctx, key, &schemas.BifrostModelRetrieveRequest{Model: "nope"})
	require.NotNil(t, bifrostErr)
	require.Equal(t, schemas.Ptr(http.StatusNotFound), bifrostErr.StatusCode)

	_, bifrostErr = provider.ModelRetrieve(ctx, key, &schemas.BifrostModelRetrieveRequest{Model: "../models"})
	require.NotNil(t, bifrostErr)
	require.Equal(t, schemas.Ptr(http.StatusBadRequest), bifrostErr.StatusCode)
}
