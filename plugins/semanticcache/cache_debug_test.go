package semanticcache

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestCacheDebugPreservation_DirectHash tests that CacheDebug is preserved for direct hash cache
func TestCacheDebugPreservation_DirectHash(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	// Use direct hash cache (no embeddings needed)
	ctx := CreateContextWithCacheKeyAndType("test-cache-debug-direct", CacheTypeDirect)

	// Use a prompt that has a mock rule
	req := CreateBasicChatRequest("What is the capital of France?", 0.7, 100)

	// First request - should miss cache
	resp1, err1 := setup.Client.ChatCompletionRequest(ctx, req)
	if err1 != nil {
		t.Fatalf("First request failed: %v", err1)
	}
	if resp1 == nil {
		t.Fatalf("First response is nil")
	}

	// Wait for cache to be written
	WaitForCache(setup.Plugin)

	// Second identical request - should hit cache
	resp2, err2 := setup.Client.ChatCompletionRequest(ctx, req)
	if err2 != nil {
		t.Fatalf("Second request failed: %v", err2)
	}
	if resp2 == nil {
		t.Fatalf("Second response is nil (cache hit returned nil)")
	}

	// Check that cache_debug is present
	if resp2.ExtraFields.CacheDebug == nil {
		t.Fatalf("Cache metadata missing 'cache_debug'")
	}

	if !resp2.ExtraFields.CacheDebug.CacheHit {
		t.Fatalf("Expected cache hit, got miss")
	}

	t.Logf("First response cache_debug: %+v", resp1.ExtraFields.CacheDebug)
	t.Logf("Second response cache_debug: %+v", resp2.ExtraFields.CacheDebug)
}

// TestCacheDebugPreservation_SemanticMatch tests that CacheDebug is preserved for semantic similarity cache hits
func TestCacheDebugPreservation_SemanticMatch(t *testing.T) {
	// Use lower threshold to ensure semantic match with MockEmbedder
	setup := NewTestSetupWithConfig(t, &Config{
		Provider:          schemas.OpenAI,
		EmbeddingModel:    "text-embedding-3-small",
		Dimension:         1536,
		Threshold:         0.5, // Lower threshold for MockEmbedder deterministic vectors
		CleanUpOnShutdown: true,
	})
	defer setup.Cleanup()

	// Use semantic cache (with mock embeddings)
	ctx := CreateContextWithCacheKey("test-cache-debug-semantic-match")

	// First request
	req1 := CreateBasicChatRequest("What is Bifrost?", 0.7, 100)
	resp1, err1 := setup.Client.ChatCompletionRequest(ctx, req1)
	if err1 != nil {
		t.Fatalf("First request failed: %v", err1)
	}
	if resp1 == nil {
		t.Fatalf("First response is nil")
	}

	// Wait for cache to be written
	WaitForCache(setup.Plugin)

	// Second request - semantically similar but different text
	// MockEmbedder uses FNV hash, so different strings produce different but deterministic vectors
	req2 := CreateBasicChatRequest("Can you explain Bifrost?", 0.7, 100)
	resp2, err2 := setup.Client.ChatCompletionRequest(ctx, req2)
	if err2 != nil {
		t.Fatalf("Second request failed: %v", err2)
	}
	if resp2 == nil {
		t.Fatalf("Second response is nil (cache hit returned nil)")
	}

	// Check that cache_debug is present
	if resp2.ExtraFields.CacheDebug == nil {
		t.Fatalf("Cache metadata missing 'cache_debug'")
	}

	if !resp2.ExtraFields.CacheDebug.CacheHit {
		t.Fatalf("Expected cache hit, got miss")
	}

	// With semantically similar (but non-identical) requests, we get a semantic match,
	// so HitType must be set and Similarity must be non-nil.
	if resp2.ExtraFields.CacheDebug.HitType == nil {
		t.Fatalf("HitType should be set")
	}
	if resp2.ExtraFields.CacheDebug.Similarity == nil {
		t.Fatalf("Similarity should be non-nil for semantic match")
	}

	t.Logf("First response cache_debug: %+v", resp1.ExtraFields.CacheDebug)
	t.Logf("Second response cache_debug: %+v", resp2.ExtraFields.CacheDebug)
	t.Logf("Hit type: %s, Similarity: %f", *resp2.ExtraFields.CacheDebug.HitType, *resp2.ExtraFields.CacheDebug.Similarity)
}
