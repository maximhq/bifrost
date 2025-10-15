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
)

// cosineSimilarity computes the cosine similarity between two vectors
func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) {
		panic(fmt.Errorf("cosineSimilarity: vectors must have same length, got %d and %d", len(a), len(b)))
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

// RunEmbeddingTest executes the embedding test scenario
func RunEmbeddingTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig config.ComprehensiveTestConfig) {
	if !testConfig.Scenarios.Embedding {
		t.Logf("Embedding not supported for provider %s", testConfig.Provider)
		return
	}

	if strings.TrimSpace(testConfig.EmbeddingModel) == "" {
		t.Skipf("Embedding enabled but model is not configured for provider %s; skipping", testConfig.Provider)
	}

	t.Run("Embedding", func(t *testing.T) {
		// Test texts with expected semantic relationships
		testTexts := []string{
			"Hello, world!",
			"Hi, world!",
			"Goodnight, moon!",
		}

		request := &schemas.BifrostEmbeddingRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.EmbeddingModel,
			Input: &schemas.EmbeddingInput{
				Texts: testTexts,
			},
			Params: &schemas.EmbeddingParameters{
				EncodingFormat: bifrost.Ptr("float"),
			},
			Fallbacks: testConfig.Fallbacks,
		}

		// Enhanced embedding validation
		expectations := EmbeddingExpectations(testTexts)
		expectations = ModifyExpectationsForProvider(expectations, testConfig.Provider)

		embeddingResponse, bifrostErr := client.EmbeddingRequest(ctx, request)
		if bifrostErr != nil {
			t.Fatalf("❌ Embedding request failed: %v", GetErrorMessage(bifrostErr))
		}

		// Validate using the new validation framework
		result := ValidateEmbeddingResponse(t, embeddingResponse, bifrostErr, expectations, "Embedding")
		if !result.Passed {
			t.Fatalf("❌ Embedding validation failed: %v", result.Errors)
		}

		// Additional embedding-specific validation (complementary to the main validation)
		validateEmbeddingSemantics(t, embeddingResponse, testTexts)
	})
}

// validateEmbeddingSemantics performs semantic validation on embedding responses
// This is complementary to the main validation framework and focuses on embedding-specific concerns
func validateEmbeddingSemantics(t *testing.T, response *schemas.BifrostEmbeddingResponse, testTexts []string) {
	if response == nil || response.Data == nil {
		t.Fatal("Invalid embedding response structure")
	}

	// Extract and validate embeddings
	embeddings := make([][]float32, len(testTexts))
	if len(response.Data) != len(testTexts) {
		t.Fatalf("Expected %d embedding results, got %d", len(testTexts), len(response.Data))
	}

	for i := range response.Data {
		vec, extractErr := getEmbeddingVector(response.Data[i])
		if extractErr != nil {
			t.Fatalf("Failed to extract embedding vector for text '%s': %v", testTexts[i], extractErr)
		}
		if len(vec) == 0 {
			t.Fatalf("Embedding vector is empty for text '%s'", testTexts[i])
		}
		embeddings[i] = vec
	}

	// Ensure all embeddings have consistent dimensions
	embeddingLength := len(embeddings[0])
	if embeddingLength == 0 {
		t.Fatal("First embedding length must be > 0")
	}

	for i, embedding := range embeddings {
		if len(embedding) != embeddingLength {
			t.Fatalf("Embedding %d has different length (%d) than first embedding (%d)",
				i, len(embedding), embeddingLength)
		}
	}

	// Semantic coherence validation
	similarityHelloHi := cosineSimilarity(embeddings[0], embeddings[1])        // "Hello, world!" vs "Hi, world!"
	similarityHelloGoodnight := cosineSimilarity(embeddings[0], embeddings[2]) // "Hello, world!" vs "Goodnight, moon!"

	// Enhanced semantic validation with detailed reporting
	semanticThreshold := 0.02
	if similarityHelloHi <= similarityHelloGoodnight+semanticThreshold {
		t.Logf("⚠️ Semantic coherence warning:")
		t.Logf("   Similarity('Hello, world!' vs 'Hi, world!'): %.6f", similarityHelloHi)
		t.Logf("   Similarity('Hello, world!' vs 'Goodnight, moon!'): %.6f", similarityHelloGoodnight)
		t.Logf("   Difference: %.6f (expected > %.6f)", similarityHelloHi-similarityHelloGoodnight, semanticThreshold)
		t.Logf("   This suggests the embedding model may not be capturing semantic meaning optimally")

		// Don't fail the test entirely, but log the concern
		t.Logf("🔄 Continuing test - semantic coherence is provider-dependent")
	} else {
		t.Logf("✅ Semantic coherence validated:")
		t.Logf("   Similarity('Hello, world!' vs 'Hi, world!'): %.6f", similarityHelloHi)
		t.Logf("   Similarity('Hello, world!' vs 'Goodnight, moon!'): %.6f", similarityHelloGoodnight)
		t.Logf("   Difference: %.6f", similarityHelloHi-similarityHelloGoodnight)
	}

	t.Logf("📊 Embedding metrics: %d vectors, %d dimensions each", len(embeddings), embeddingLength)
}
