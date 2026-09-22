package llmtests

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// testImageDataURI is a 1×1 red pixel PNG encoded as a data URI.
// Used as a lightweight inline image for multimodal embedding tests —
// no external network dependency.
const testImageDataURI = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAACAAAAAgCAIAAAD8GO2jAAAAOklEQVR4nO3RQREAQAjDwHKS8C8AWSchfPhlBZSZUNOdS+90PR5Y8AfIRMhEyETIRMhEyETIRMhEIR/EvwFs/VkrpgAAAABJRU5ErkJggg=="

// makeTextContent returns a single-text EmbeddingContent for the given string.
func makeTextContent(text string) schemas.EmbeddingInputItem {
	t := text
	return schemas.EmbeddingInputItem{Content: schemas.EmbeddingContent{{
		Type: schemas.EmbeddingContentPartTypeText,
		Text: &t,
	}}}
}

// makeImageDataContent returns a single-image (inline data URI) EmbeddingContent.
func makeImageDataContent(dataURI string) schemas.EmbeddingInputItem {
	d := dataURI
	return schemas.EmbeddingInputItem{Content: schemas.EmbeddingContent{{
		Type:  schemas.EmbeddingContentPartTypeImage,
		Image: &schemas.EmbeddingMediaPart{Data: &d},
	}}}
}

// makeMultimodalContent returns a EmbeddingContent with both text and image parts,
// producing a single aggregated multimodal embedding.
func makeMultimodalContent(text, imageDataURI string) schemas.EmbeddingInputItem {
	t := text
	d := imageDataURI
	return schemas.EmbeddingInputItem{Content: schemas.EmbeddingContent{
		{Type: schemas.EmbeddingContentPartTypeText, Text: &t},
		{Type: schemas.EmbeddingContentPartTypeImage, Image: &schemas.EmbeddingMediaPart{Data: &d}},
	}}
}

// cosineSimilarity computes the cosine similarity between two vectors
func cosineSimilarity(a, b []float64) float64 {
	if len(a) != len(b) {
		panic(fmt.Errorf("cosineSimilarity: vectors must have same length, got %d and %d", len(a), len(b)))
	}

	var dotProduct float64
	var normA float64
	var normB float64

	for i := 0; i < len(a); i++ {
		dotProduct += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}

	if normA == 0 || normB == 0 {
		return 0.0
	}

	return dotProduct / (math.Sqrt(normA) * math.Sqrt(normB))
}

// RunEmbeddingTest executes the embedding test scenario
func RunEmbeddingTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.Embedding {
		t.Logf("Embedding not supported for provider %s", testConfig.Provider)
		return
	}

	if strings.TrimSpace(testConfig.EmbeddingModel) == "" {
		t.Skipf("Embedding enabled but model is not configured for provider %s; skipping", testConfig.Provider)
	}

	t.Run("Embedding", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		// Test texts with expected semantic relationships
		testTexts := []string{
			"Hello, world!",
			"Hi, world!",
			"Goodnight, moon!",
		}

		// Use retry framework with enhanced validation
		retryConfig := GetTestRetryConfigForScenario("Embedding", testConfig)
		retryContext := TestRetryContext{
			ScenarioName: "Embedding",
			ExpectedBehavior: map[string]interface{}{
				"should_return_embeddings":  true,
				"should_have_valid_vectors": true,
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
				"model":    testConfig.EmbeddingModel,
			},
		}

		// Create Embedding retry config
		embeddingRetryConfig := EmbeddingRetryConfig{
			MaxAttempts: retryConfig.MaxAttempts,
			BaseDelay:   retryConfig.BaseDelay,
			MaxDelay:    retryConfig.MaxDelay,
			Conditions:  []EmbeddingRetryCondition{}, // Add specific embedding retry conditions as needed
			OnRetry:     retryConfig.OnRetry,
			OnFinalFail: retryConfig.OnFinalFail,
		}

		// One request per text: some embedding endpoints (e.g. Vertex :predict) accept a single input.
		embeddingResponse := &schemas.BifrostEmbeddingResponse{Data: make([]schemas.EmbeddingData, 0, len(testTexts))}
		for i, text := range testTexts {
			request := &schemas.BifrostEmbeddingRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.EmbeddingModel,
				Input: []schemas.EmbeddingInputItem{
					{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &text}}},
				},
				Params: &schemas.EmbeddingParameters{
					EncodingFormat: bifrost.Ptr("float"),
				},
				Fallbacks: testConfig.EmbeddingFallbacks,
			}

			expectations := EmbeddingExpectations([]string{text})
			expectations = ModifyExpectationsForProvider(expectations, testConfig.Provider)

			resp, bifrostErr := WithEmbeddingTestRetry(t, embeddingRetryConfig, retryContext, expectations, "Embedding", func() (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
				bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
				return client.EmbeddingRequest(bfCtx, request)
			})

			if bifrostErr != nil {
				t.Fatalf("❌ Embedding request failed after retries for text '%s': %v", text, GetErrorMessage(bifrostErr))
			}
			if resp == nil || len(resp.Data) != 1 {
				t.Fatalf("Expected 1 embedding result for text '%s'", text)
			}

			data := resp.Data[0]
			data.Index = i
			embeddingResponse.Data = append(embeddingResponse.Data, data)
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
	embeddings := make([][]float64, len(testTexts))
	responseDataLength := len(response.Data)
	if responseDataLength != len(testTexts) {
		t.Fatalf("Expected %d embedding results, got %d", len(testTexts), responseDataLength)
	}

	for i := range responseDataLength {
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
		t.Logf("Continuing test - semantic coherence is provider-dependent")
	} else {
		t.Logf("✅ Semantic coherence validated:")
		t.Logf("   Similarity('Hello, world!' vs 'Hi, world!'): %.6f", similarityHelloHi)
		t.Logf("   Similarity('Hello, world!' vs 'Goodnight, moon!'): %.6f", similarityHelloGoodnight)
		t.Logf("   Difference: %.6f", similarityHelloHi-similarityHelloGoodnight)
	}

	t.Logf("📊 Embedding metrics: %d vectors, %d dimensions each", len(embeddings), embeddingLength)
}

// RunMultimodalEmbeddingTest runs all multimodal embedding sub-scenarios for
// providers that declare MultimodalEmbedding support.
//
// Scenarios covered:
//  1. Single text input → 1 embedding
//  2. Batch text inputs → N embeddings
//  3. Single image input (inline data URI) → 1 embedding
//  4. Single multimodal content (text + image) → 1 aggregated embedding
//  5. Batch images → N embeddings           (skipped for Vertex: no batch on multimodal path)
//  6. Batch multimodal (text+image per entry) → N embeddings  (same skip)
func RunMultimodalEmbeddingTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.MultimodalEmbedding {
		t.Logf("MultimodalEmbedding not enabled for provider %s", testConfig.Provider)
		return
	}

	model := testConfig.MultimodalEmbeddingModel
	if strings.TrimSpace(model) == "" {
		t.Logf("MultimodalEmbedding enabled but MultimodalEmbeddingModel not set for %s; skipping", testConfig.Provider)
		return
	}

	t.Run("MultimodalEmbedding", func(t *testing.T) {
		// Vertex Gemini path does not support batch for multimodal inputs.
		vertexNoBatch := testConfig.Provider == schemas.Vertex

		run := func(name string, req *schemas.BifrostEmbeddingRequest, wantCount int) {
			t.Run(name, func(t *testing.T) {
				ShouldRunParallel(t, testConfig, "MultimodalEmbedding")

				retryConfig := GetTestRetryConfigForScenario("MultimodalEmbedding", testConfig)
				retryContext := TestRetryContext{
					ScenarioName: name,
					ExpectedBehavior: map[string]interface{}{
						"should_return_embeddings":  true,
						"should_have_valid_vectors": true,
					},
					TestMetadata: map[string]interface{}{
						"provider":   testConfig.Provider,
						"model":      model,
						"want_count": wantCount,
					},
				}

				// Build a dummy string slice of the right length for EmbeddingExpectations.
				dummyTexts := make([]string, wantCount)
				expectations := EmbeddingExpectations(dummyTexts)
				expectations = ApplyRawExpectations(expectations, testConfig, false)
				expectations = ModifyExpectationsForProvider(expectations, testConfig.Provider)

				embeddingRetryConfig := EmbeddingRetryConfig{
					MaxAttempts: retryConfig.MaxAttempts,
					BaseDelay:   retryConfig.BaseDelay,
					MaxDelay:    retryConfig.MaxDelay,
					Conditions:  []EmbeddingRetryCondition{},
					OnRetry:     retryConfig.OnRetry,
					OnFinalFail: retryConfig.OnFinalFail,
				}

				resp, bifrostErr := WithEmbeddingTestRetry(t, embeddingRetryConfig, retryContext, expectations, name, func() (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
					bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
					return client.EmbeddingRequest(bfCtx, req)
				})

				if bifrostErr != nil {
					t.Fatalf("❌ %s multimodal embedding request failed after retries: %v", name, GetErrorMessage(bifrostErr))
				}

				validateEmbeddingCount(t, resp, wantCount)
				validateNonEmptyVectors(t, resp)
			})
		}

		// ── 1. Single text ────────────────────────────────────────────────────────
		run("SingleText", &schemas.BifrostEmbeddingRequest{
			Provider: testConfig.Provider,
			Model:    model,
			Input: []schemas.EmbeddingInputItem{
				makeTextContent("The quick brown fox jumps over the lazy dog."),
			},
			Params: &schemas.EmbeddingParameters{EncodingFormat: bifrost.Ptr("float")},
		}, 1)

		// ── 2. Batch text ─────────────────────────────────────────────────────────
		// Every Vertex embedding path rejects multiple input items, text included.
		if !vertexNoBatch {
			run("BatchText", &schemas.BifrostEmbeddingRequest{
				Provider: testConfig.Provider,
				Model:    model,
				Input: []schemas.EmbeddingInputItem{
					makeTextContent("Cats are great pets."),
					makeTextContent("Dogs are loyal companions."),
					makeTextContent("The sky is blue."),
				},
				Params: &schemas.EmbeddingParameters{EncodingFormat: bifrost.Ptr("float")},
			}, 3)
		}

		// ── 3. Single image (inline data URI) ────────────────────────────────────
		run("SingleImage", &schemas.BifrostEmbeddingRequest{
			Provider: testConfig.Provider,
			Model:    model,
			Input: []schemas.EmbeddingInputItem{
				makeImageDataContent(testImageDataURI),
			},
			Params: &schemas.EmbeddingParameters{EncodingFormat: bifrost.Ptr("float")},
		}, 1)

		// ── 4. Single multimodal content (text + image → 1 aggregated embedding) ─
		run("SingleMultimodal", &schemas.BifrostEmbeddingRequest{
			Provider: testConfig.Provider,
			Model:    model,
			Input: []schemas.EmbeddingInputItem{
				makeMultimodalContent("A red pixel.", testImageDataURI),
			},
			Params: &schemas.EmbeddingParameters{EncodingFormat: bifrost.Ptr("float")},
		}, 1)

		if vertexNoBatch {
			t.Logf("⏭  Skipping batch multimodal scenarios for Vertex (single-content only on Gemini embedding path)")
			return
		}

		// ── 5. Batch images ───────────────────────────────────────────────────────
		run("BatchImages", &schemas.BifrostEmbeddingRequest{
			Provider: testConfig.Provider,
			Model:    model,
			Input: []schemas.EmbeddingInputItem{
				makeImageDataContent(testImageDataURI),
				makeImageDataContent(testImageDataURI),
			},
			Params: &schemas.EmbeddingParameters{EncodingFormat: bifrost.Ptr("float")},
		}, 2)

		// ── 6. Batch multimodal (text+image per entry) ────────────────────────────
		run("BatchMultimodal", &schemas.BifrostEmbeddingRequest{
			Provider: testConfig.Provider,
			Model:    model,
			Input: []schemas.EmbeddingInputItem{
				makeMultimodalContent("First image description.", testImageDataURI),
				makeMultimodalContent("Second image description.", testImageDataURI),
			},
			Params: &schemas.EmbeddingParameters{EncodingFormat: bifrost.Ptr("float")},
		}, 2)
	}) // end t.Run("MultimodalEmbedding")
}

// validateEmbeddingCount asserts that the response contains exactly wantCount embeddings.
func validateEmbeddingCount(t *testing.T, resp *schemas.BifrostEmbeddingResponse, wantCount int) {
	t.Helper()
	if resp == nil {
		t.Fatal("embedding response is nil")
	}
	got := len(resp.Data)
	if got != wantCount {
		t.Fatalf("expected %d embeddings, got %d", wantCount, got)
	}
}

// validateNonEmptyVectors asserts every embedding vector is non-empty and all
// share the same dimension.
func validateNonEmptyVectors(t *testing.T, resp *schemas.BifrostEmbeddingResponse) {
	t.Helper()
	if resp == nil || len(resp.Data) == 0 {
		t.Fatal("embedding response has no data")
	}

	var dim int
	for i, item := range resp.Data {
		vec, err := getEmbeddingVector(item)
		if err != nil {
			t.Fatalf("embedding[%d]: failed to extract vector: %v", i, err)
		}
		if len(vec) == 0 {
			t.Fatalf("embedding[%d]: vector is empty", i)
		}
		if dim == 0 {
			dim = len(vec)
		} else if len(vec) != dim {
			t.Fatalf("embedding[%d]: dimension mismatch: got %d, expected %d", i, len(vec), dim)
		}
	}
	t.Logf("✅ %d embedding vector(s), %d dimensions each", len(resp.Data), dim)
}
