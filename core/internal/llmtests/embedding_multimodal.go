package llmtests

import (
	"context"
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
func makeTextContent(text string) schemas.EmbeddingContent {
	t := text
	return schemas.EmbeddingContent{{
		Type: schemas.EmbeddingContentPartTypeText,
		Text: &t,
	}}
}

// makeImageDataContent returns a single-image (inline data URI) EmbeddingContent.
func makeImageDataContent(dataURI string) schemas.EmbeddingContent {
	d := dataURI
	return schemas.EmbeddingContent{{
		Type:  schemas.EmbeddingContentPartTypeImage,
		Image: &schemas.EmbeddingMediaPart{Data: &d},
	}}
}

// makeMultimodalContent returns a EmbeddingContent with both text and image parts,
// producing a single aggregated multimodal embedding.
func makeMultimodalContent(text, imageDataURI string) schemas.EmbeddingContent {
	t := text
	d := imageDataURI
	return schemas.EmbeddingContent{
		{Type: schemas.EmbeddingContentPartTypeText, Text: &t},
		{Type: schemas.EmbeddingContentPartTypeImage, Image: &schemas.EmbeddingMediaPart{Data: &d}},
	}
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
		t.Skipf("MultimodalEmbedding enabled but MultimodalEmbeddingModel not set for %s; skipping", testConfig.Provider)
	}

	t.Run("MultimodalEmbedding", func(t *testing.T) {
		// Vertex Gemini path does not support batch for multimodal inputs.
		vertexNoBatch := testConfig.Provider == schemas.Vertex

		run := func(name string, req *schemas.BifrostEmbeddingRequest, wantCount int) {
			t.Run(name, func(t *testing.T) {
				if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
					t.Parallel()
				}

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
			Input: []schemas.EmbeddingContent{
					makeTextContent("The quick brown fox jumps over the lazy dog."),
				},
			Params: &schemas.EmbeddingParameters{EncodingFormat: bifrost.Ptr("float")},
		}, 1)

		// ── 2. Batch text ─────────────────────────────────────────────────────────
		if vertexNoBatch {
			t.Logf("⏭  Skipping BatchText for Vertex (Gemini embedding path rejects multiple contents)")
		} else {
		run("BatchText", &schemas.BifrostEmbeddingRequest{
			Provider: testConfig.Provider,
			Model:    model,
			Input: []schemas.EmbeddingContent{
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
			Input: []schemas.EmbeddingContent{
					makeImageDataContent(testImageDataURI),
				},
			Params: &schemas.EmbeddingParameters{EncodingFormat: bifrost.Ptr("float")},
		}, 1)

		// ── 4. Single multimodal content (text + image → 1 aggregated embedding) ─
		run("SingleMultimodal", &schemas.BifrostEmbeddingRequest{
			Provider: testConfig.Provider,
			Model:    model,
			Input: []schemas.EmbeddingContent{
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
			Input: []schemas.EmbeddingContent{
					makeImageDataContent(testImageDataURI),
					makeImageDataContent(testImageDataURI),
				},
			Params: &schemas.EmbeddingParameters{EncodingFormat: bifrost.Ptr("float")},
		}, 2)

		// ── 6. Batch multimodal (text+image per entry) ────────────────────────────
		run("BatchMultimodal", &schemas.BifrostEmbeddingRequest{
			Provider: testConfig.Provider,
			Model:    model,
			Input: []schemas.EmbeddingContent{
					makeMultimodalContent("First image description.", testImageDataURI),
					makeMultimodalContent("Second image description.", testImageDataURI),
				},
			Params: &schemas.EmbeddingParameters{EncodingFormat: bifrost.Ptr("float")},
		}, 2)
	}) // end t.Run("MultimodalEmbedding")
}
