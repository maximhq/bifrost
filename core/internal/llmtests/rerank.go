package llmtests

import (
	"context"
	"math"
	"os"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// BasicRerankExpectations validates common rerank invariants for provider tests.
func BasicRerankExpectations(t *testing.T, rerankResponse *schemas.BifrostRerankResponse, documents []schemas.RerankDocument) {
	t.Helper()

	if rerankResponse == nil {
		t.Fatal("❌ Rerank response is nil")
	}

	if len(rerankResponse.Results) == 0 {
		t.Fatal("❌ Rerank results are empty")
	}
	if len(rerankResponse.Results) > len(documents) {
		t.Fatalf("❌ Rerank returned too many results: got %d, max %d", len(rerankResponse.Results), len(documents))
	}

	seenIndices := make(map[int]struct{}, len(rerankResponse.Results))
	for i, result := range rerankResponse.Results {
		if result.Index < 0 || result.Index >= len(documents) {
			t.Fatalf("❌ Result %d has invalid index %d (expected 0-%d)", i, result.Index, len(documents)-1)
		}
		if _, exists := seenIndices[result.Index]; exists {
			t.Fatalf("❌ Result %d has duplicate index %d", i, result.Index)
		}
		seenIndices[result.Index] = struct{}{}

		if math.IsNaN(result.RelevanceScore) || math.IsInf(result.RelevanceScore, 0) {
			t.Fatalf("❌ Result %d has non-finite relevance score %f", i, result.RelevanceScore)
		}

		if result.Document == nil {
			t.Fatalf("❌ Result %d has nil document (return_documents was true)", i)
		}
		if result.Document.Text != documents[result.Index].Text {
			t.Fatalf("❌ Result %d has document text mismatch for index %d", i, result.Index)
		}
	}

	for i := 1; i < len(rerankResponse.Results); i++ {
		if rerankResponse.Results[i].RelevanceScore > rerankResponse.Results[i-1].RelevanceScore {
			t.Fatalf("❌ Results not sorted by descending score at index %d: %f > %f",
				i, rerankResponse.Results[i].RelevanceScore, rerankResponse.Results[i-1].RelevanceScore)
		}
	}
}

// RunRerankTest executes the rerank test scenario
func RunRerankTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.Rerank {
		t.Logf("Rerank not supported for provider %s", testConfig.Provider)
		return
	}

	if strings.TrimSpace(testConfig.RerankModel) == "" {
		t.Skipf("Rerank enabled but model is not configured for provider %s; skipping", testConfig.Provider)
	}

	t.Run("Rerank", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		query := "What is the capital of France?"
		documents := []schemas.RerankDocument{
			{Text: "Paris is the capital and most populous city of France."},
			{Text: "Berlin is the capital of Germany."},
			{Text: "The Eiffel Tower is located in Paris, France."},
			{Text: "London is the capital of England and the United Kingdom."},
			{Text: "France is a country in Western Europe."},
		}

		request := &schemas.BifrostRerankRequest{
			Provider:  testConfig.Provider,
			Model:     testConfig.RerankModel,
			Query:     query,
			Documents: documents,
			Params: &schemas.RerankParameters{
				ReturnDocuments: schemas.Ptr(true),
			},
			Fallbacks: testConfig.RerankFallbacks,
		}

		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		rerankResponse, bifrostErr := client.RerankRequest(bfCtx, request)

		if bifrostErr != nil {
			t.Fatalf("❌ Rerank request failed: %v", GetErrorMessage(bifrostErr))
		}

		if rerankResponse == nil {
			t.Fatal("❌ Rerank response is nil")
		}

		BasicRerankExpectations(t, rerankResponse, documents)

		// Validate that the most relevant document mentions Paris/France
		if len(rerankResponse.Results) > 0 {
			topResult := rerankResponse.Results[0]
			if topResult.Document != nil {
				topText := strings.ToLower(topResult.Document.Text)
				if !strings.Contains(topText, "paris") && !strings.Contains(topText, "capital") {
					t.Logf("⚠️ Top result may not be the most relevant: %q", topResult.Document.Text)
				} else {
					t.Logf("✅ Top result is relevant: %q (score: %f)", topResult.Document.Text, topResult.RelevanceScore)
				}
			}
		}

		t.Logf("✅ Rerank test passed: %d results returned", len(rerankResponse.Results))
		t.Logf("📊 Rerank metrics: model=%s, results=%d", rerankResponse.Model, len(rerankResponse.Results))
		if rerankResponse.Usage != nil {
			t.Logf("📊 Usage: prompt_tokens=%d, total_tokens=%d",
				rerankResponse.Usage.PromptTokens, rerankResponse.Usage.TotalTokens)
		}
	})
}
