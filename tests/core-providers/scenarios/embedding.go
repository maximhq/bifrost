package scenarios

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// RunEmbeddingTest executes the embedding test scenario
func RunEmbeddingTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig config.ComprehensiveTestConfig) {
	if !testConfig.Scenarios.Embedding {
		t.Logf("Embedding not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("Embedding", func(t *testing.T) {
		request := &schemas.BifrostRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.EmbeddingModel,
			Input: schemas.RequestInput{
				EmbeddingInput: &schemas.EmbeddingInput{
					Texts: []string{"Hello, world!"},
				},
			},
			Params: MergeModelParameters(&schemas.ModelParameters{
				MaxTokens: bifrost.Ptr(150),
			}, testConfig.CustomParams),
			Fallbacks: testConfig.Fallbacks,
		}

		response, err := client.EmbeddingRequest(ctx, request)
		require.Nilf(t, err, "Embedding failed: %v", err)
		require.NotNil(t, response)
		require.NotEmpty(t, response.Data)

	})
}
