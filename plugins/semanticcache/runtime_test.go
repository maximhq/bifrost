package semanticcache

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/stretchr/testify/require"
)

// TestRuntimePanicScenario reproduces the exact runtime panic:
// First request works, second request (cache hit) panics with nil client
func TestRuntimePanicScenario(t *testing.T) {
	ctx := context.Background()

	// Setup vector store (Qdrant in-memory)
	vectorConfig := &schemas.VectorStoreConfig{
		Provider: schemas.VectorStoreProviderQdrant,
		Qdrant: &schemas.QdrantConfig{
			URL:        "http://localhost:6333",
			Collection: "test_cache",
		},
	}
	vectorStore, err := vectorstore.NewVectorStore(ctx, vectorConfig)
	require.NoError(t, err)
	defer vectorStore.Close()

	// Initialize plugin (simulating server startup)
	pluginConfig := &Config{
		Provider:       schemas.ModelProviderOpenAI,
		EmbeddingModel: "text-embedding-3-small",
		Threshold:      0.95,
	}
	logger := &schemas.NoOpLogger{}
	plugin, err := Init(ctx, pluginConfig, logger, vectorConfig)
	require.NoError(t, err)

	// Create bifrost client (simulating server startup)
	bifrostConfig := &schemas.BifrostConfig{
		Providers: []schemas.ProviderConfig{
			{
				Provider: schemas.ModelProviderOpenAI,
				Keys: []schemas.Key{
					{Value: "test-key"},
				},
			},
		},
	}
	bifrostClient, err := core.NewBifrost(bifrostConfig, logger)
	require.NoError(t, err)
	defer bifrostClient.Shutdown()

	// THIS IS THE CRITICAL STEP - simulating what server.go does
	bifrostClient.Init()

	// Verify plugin received client
	require.NotNil(t, plugin.client, "plugin.client should be set after bifrost.Init()")

	// First request - should work
	req1 := &schemas.BifrostChatRequest{
		Provider: schemas.ModelProviderOpenAI,
		Model:    "gpt-4",
		Messages: []schemas.Message{
			{Role: "user", Content: schemas.NewStringContent("Hello")},
		},
	}
	bfCtx1 := schemas.NewBifrostContext(ctx, 0)
	_, err = plugin.PreLLMHook(bfCtx1, req1)
	require.NoError(t, err, "First request should succeed")

	// Verify client is still set
	require.NotNil(t, plugin.client, "plugin.client should still be set after first request")

	// Second request - THIS IS WHERE THE PANIC OCCURS IN PRODUCTION
	req2 := &schemas.BifrostChatRequest{
		Provider: schemas.ModelProviderOpenAI,
		Model:    "gpt-4",
		Messages: []schemas.Message{
			{Role: "user", Content: schemas.NewStringContent("Hello")},
		},
	}
	bfCtx2 := schemas.NewBifrostContext(ctx, 0)
	_, err = plugin.PreLLMHook(bfCtx2, req2)
	require.NoError(t, err, "Second request should succeed (cache hit)")

	// Verify client is STILL set
	require.NotNil(t, plugin.client, "plugin.client should STILL be set after second request")
}
