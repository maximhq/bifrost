package scenarios

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// cosineSimilarity computes the cosine similarity between two vectors
func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) {
		return 0.0
	}

	var dotProduct float64
	var normA float64
	var normB float64

	for i := 0; i < len(a); i++ {
		dotProduct += float64(a[i] * b[i])
		normA += float64(a[i] * a[i])
		normB += float64(b[i] * b[i])
	}

	if normA == 0 || normB == 0 {
		return 0.0
	}

	return dotProduct / (math.Sqrt(normA) * math.Sqrt(normB))
}

// getEmbeddingVector extracts the embedding vector from BifrostEmbeddingResponse
func getEmbeddingVector(embedding schemas.BifrostEmbeddingResponse) ([]float32, error) {
	if embedding.EmbeddingArray != nil {
		return *embedding.EmbeddingArray, nil
	}
	if embedding.Embedding2DArray != nil && len(*embedding.Embedding2DArray) > 0 {
		return (*embedding.Embedding2DArray)[0], nil
	}
	return nil, fmt.Errorf("no valid embedding vector found")
}

// RunEmbeddingTest executes the embedding test scenario
func RunEmbeddingTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig config.ComprehensiveTestConfig) {
	if !testConfig.Scenarios.Embedding {
		t.Logf("Embedding not supported for provider %s", testConfig.Provider)
		return
	}

	if strings.TrimSpace(testConfig.EmbeddingModel) == "" {
		t.Skipf("Embedding enabled but model is not configured for provider %s; skipping", testConfig.Provider)
	}

	t.Run(fmt.Sprintf("Embedding/%s/%s", testConfig.Provider, testConfig.EmbeddingModel), func(t *testing.T) {
		// Test texts with expected semantic relationships
		testTexts := []string{
			"Hello, world!",
			"Hi, world!",
			"Goodnight, moon!",
		}

		// Get embeddings for all test texts
		embeddings := make([][]float32, len(testTexts))
		request := &schemas.BifrostRequest{
		Provider: testConfig.Provider,
		Model:    testConfig.EmbeddingModel,
			Input: schemas.RequestInput{
				EmbeddingInput: &schemas.EmbeddingInput{
					Texts: testTexts,
				},
			},
			Params: MergeModelParameters(&schemas.ModelParameters{
				EncodingFormat: bifrost.Ptr("float"),
			}, testConfig.CustomParams),
			Fallbacks: testConfig.Fallbacks,
		}
					
		response, err := client.EmbeddingRequest(ctx, request)
		require.Nilf(t, err, "Embedding request failed: %v", err)
		require.NotNil(t, response)
		require.Lenf(t, response.Data, len(testTexts), "expected %d results", len(testTexts))
		for i := range response.Data {
			vec, extractErr := getEmbeddingVector(response.Data[i].Embedding)
			require.NoErrorf(t, extractErr, "Failed to extract embedding vector for text '%s': %v", testTexts[i], extractErr)
			require.NotEmptyf(t, vec, "Embedding vector is empty for text '%s'", testTexts[i])
			embeddings[i] = vec
		}

		// Ensure all embeddings have the same length
		embeddingLength := len(embeddings[0])
		for i, embedding := range embeddings {
			require.Equalf(t, embeddingLength, len(embedding),
				"Embedding %d has different length (%d) than first embedding (%d)",
				i, len(embedding), embeddingLength)
		}

		// Compute pairwise similarities
		similarityHelloHi := cosineSimilarity(embeddings[0], embeddings[1])        // "Hello, world!" vs "Hi, world!"
		similarityHelloGoodnight := cosineSimilarity(embeddings[0], embeddings[2]) // "Hello, world!" vs "Goodnight, moon!"

		// Assert semantic coherence: similar phrases should be more similar than dissimilar ones
		require.Greaterf(t, similarityHelloHi, similarityHelloGoodnight+0.02,
			"Semantic coherence test failed: similarity('Hello, world!' vs 'Hi, world!') = %.6f should be greater than similarity('Hello, world!' vs 'Goodnight, moon!') = %.6f. This suggests the embedding model may not be capturing semantic meaning correctly.",
			similarityHelloHi, similarityHelloGoodnight)

		t.Logf("✅ Semantic coherence validated:")
		t.Logf("   Similarity('Hello, world!' vs 'Hi, world!'): %.6f", similarityHelloHi)
		t.Logf("   Similarity('Hello, world!' vs 'Goodnight, moon!'): %.6f", similarityHelloGoodnight)
		t.Logf("   Difference: %.6f", similarityHelloHi-similarityHelloGoodnight)
	})
}
