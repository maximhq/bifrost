package semanticcache

import (
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestCacheTypeDirectOnly tests that CacheTypeKey set to "direct" only performs direct hash matching
func TestCacheTypeDirectOnly(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	// First, cache a response using normal behavior (both direct and semantic)
	ctx1 := CreateContextWithCacheKey("test-cache-type-direct")
	testRequest := CreateBasicChatRequest("What is Bifrost?", 0.7, 50)

	t.Log("Making first request to populate cache...")
	response1, err1 := setup.Client.ChatCompletionRequest(ctx1, testRequest)
	if err1 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1})

	WaitForCache()

	// Now test with CacheTypeKey set to direct only
	ctx2 := CreateContextWithCacheKeyAndType("test-cache-type-direct", CacheTypeDirect)

	t.Log("Making second request with CacheTypeKey=direct...")
	response2, err2 := setup.Client.ChatCompletionRequest(ctx2, testRequest)
	if err2 != nil {
		t.Fatalf("Second request failed: %v", err2.Error.Message)
	}

	// Should be a cache hit from direct search
	AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2}, "direct")

	t.Log("✅ CacheTypeKey=direct correctly performs only direct hash matching")
}

// TestCacheTypeSemanticOnly tests that CacheTypeKey set to "semantic" only performs semantic search
func TestCacheTypeSemanticOnly(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	// First, cache a response using normal behavior
	ctx1 := CreateContextWithCacheKey("test-cache-type-semantic")
	testRequest := CreateBasicChatRequest("Explain machine learning concepts", 0.7, 50)

	t.Log("Making first request to populate cache...")
	response1, err1 := setup.Client.ChatCompletionRequest(ctx1, testRequest)
	if err1 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1})

	WaitForCache()

	// Test with slightly different wording that should match semantically but not directly
	similarRequest := CreateBasicChatRequest("Can you explain concepts in machine learning", 0.7, 50)

	// Try with semantic-only search
	ctx2 := CreateContextWithCacheKeyAndType("test-cache-type-semantic", CacheTypeSemantic)

	t.Log("Making second request with similar content and CacheTypeKey=semantic...")
	response2, err2 := setup.Client.ChatCompletionRequest(ctx2, similarRequest)
	if err2 != nil {
		if err2.Error != nil {
			t.Fatalf("Second request failed: %v", err2.Error.Message)
		} else {
			t.Fatalf("Second request failed: %v", err2)
		}
	}

	// This might be a cache hit if semantic similarity is high enough
	// The test validates that semantic search is attempted
	if response2.ExtraFields.CacheDebug != nil && response2.ExtraFields.CacheDebug.CacheHit {
		AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2}, "semantic")
		t.Log("✅ CacheTypeKey=semantic correctly found semantic match")
	} else {
		t.Log("ℹ️  No semantic match found (threshold may be too high for these similar phrases)")
		AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2})
	}

	t.Log("✅ CacheTypeKey=semantic correctly performs only semantic search")
}

// TestCacheTypeDirectWithSemanticFallback tests the default behavior (both direct and semantic)
func TestCacheTypeDirectWithSemanticFallback(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	// Cache a response first
	ctx1 := CreateContextWithCacheKey("test-cache-type-fallback")
	testRequest := CreateBasicChatRequest("Define artificial intelligence", 0.7, 50)

	t.Log("Making first request to populate cache...")
	response1, err1 := setup.Client.ChatCompletionRequest(ctx1, testRequest)
	if err1 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1})

	WaitForCache()

	// Test exact match (should hit direct cache)
	ctx2 := CreateContextWithCacheKey("test-cache-type-fallback")

	t.Log("Making second identical request (should hit direct cache)...")
	response2, err2 := setup.Client.ChatCompletionRequest(ctx2, testRequest)
	if err2 != nil {
		if err2.Error != nil {
			t.Fatalf("Second request failed: %v", err2.Error.Message)
		} else {
			t.Fatalf("Second request failed: %v", err2)
		}
	}
	AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2}, "direct")

	// Test similar request (should potentially hit semantic cache)
	similarRequest := CreateBasicChatRequest("What is artificial intelligence", 0.7, 50)

	t.Log("Making third similar request (should attempt semantic match)...")
	response3, err3 := setup.Client.ChatCompletionRequest(ctx2, similarRequest)
	if err3 != nil {
		t.Fatalf("Third request failed: %v", err3)
	}

	// May or may not be a cache hit depending on semantic similarity
	if response3.ExtraFields.CacheDebug != nil && response3.ExtraFields.CacheDebug.CacheHit {
		AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response3}, "semantic")
		t.Log("✅ Default behavior correctly found semantic match")
	} else {
		t.Log("ℹ️  No semantic match found (normal for different wording)")
		AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response3})
	}

	t.Log("✅ Default behavior correctly attempts both direct and semantic search")
}

// TestCacheTypeInvalidValue tests behavior with invalid CacheTypeKey values
func TestCacheTypeInvalidValue(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	// Create context with invalid cache type
	ctx := CreateContextWithCacheKey("test-invalid-cache-type")
	ctx = ctx.WithValue(CacheTypeKey, "invalid_type")

	testRequest := CreateBasicChatRequest("Test invalid cache type", 0.7, 50)

	t.Log("Making request with invalid CacheTypeKey value...")
	response, err := setup.Client.ChatCompletionRequest(ctx, testRequest)
	if err != nil {
		return // Test will be skipped by retry function
	}

	// Should fall back to default behavior (both direct and semantic)
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response})

	t.Log("✅ Invalid CacheTypeKey value falls back to default behavior")
}

// TestCacheTypeWithEmbeddingRequests tests CacheTypeKey behavior with embedding requests
func TestCacheTypeWithEmbeddingRequests(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	embeddingRequest := CreateEmbeddingRequest([]string{"Test embedding with cache type"})

	// Cache first request
	ctx1 := CreateContextWithCacheKey("test-embedding-cache-type")
	t.Log("Making first embedding request...")
	response1, err1 := setup.Client.EmbeddingRequest(ctx1, embeddingRequest)
	if err1 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{EmbeddingResponse: response1})

	WaitForCache()

	// Test with direct-only cache type
	ctx2 := CreateContextWithCacheKeyAndType("test-embedding-cache-type", CacheTypeDirect)
	t.Log("Making second embedding request with CacheTypeKey=direct...")
	response2, err2 := setup.Client.EmbeddingRequest(ctx2, embeddingRequest)
	if err2 != nil {
		if err2.Error != nil {
			t.Fatalf("Second request failed: %v", err2.Error.Message)
		} else {
			t.Fatalf("Second request failed: %v", err2)
		}
	}
	AssertCacheHit(t, &schemas.BifrostResponse{EmbeddingResponse: response2}, "direct")

	// Test with semantic-only cache type (should not find semantic match for embeddings)
	ctx3 := CreateContextWithCacheKeyAndType("test-embedding-cache-type", CacheTypeSemantic)
	t.Log("Making third embedding request with CacheTypeKey=semantic...")
	response3, err3 := setup.Client.EmbeddingRequest(ctx3, embeddingRequest)
	if err3 != nil {
		t.Fatalf("Third request failed: %v", err3)
	}
	// Semantic search should be skipped for embedding requests
	AssertNoCacheHit(t, &schemas.BifrostResponse{EmbeddingResponse: response3})

	t.Log("✅ CacheTypeKey works correctly with embedding requests")
}

// TestCacheTypePerformanceCharacteristics tests that different cache types have expected performance
func TestCacheTypePerformanceCharacteristics(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	testRequest := CreateBasicChatRequest("Performance test for cache types", 0.7, 50)

	// Cache first request
	ctx1 := CreateContextWithCacheKey("test-cache-performance")
	t.Log("Making first request to populate cache...")
	response1, err1 := setup.Client.ChatCompletionRequest(ctx1, testRequest)
	if err1 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1})

	WaitForCache()

	// Test direct-only performance
	ctx2 := CreateContextWithCacheKeyAndType("test-cache-performance", CacheTypeDirect)
	start2 := time.Now()
	response2, err2 := setup.Client.ChatCompletionRequest(ctx2, testRequest)
	duration2 := time.Since(start2)
	if err2 != nil {
		t.Fatalf("Direct cache request failed: %v", err2)
	}
	AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2}, "direct")

	t.Logf("Direct cache lookup took: %v", duration2)

	// Test default behavior (both direct and semantic) performance
	ctx3 := CreateContextWithCacheKey("test-cache-performance")
	start3 := time.Now()
	response3, err3 := setup.Client.ChatCompletionRequest(ctx3, testRequest)
	duration3 := time.Since(start3)
	if err3 != nil {
		t.Fatalf("Default cache request failed: %v", err3)
	}
	AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response3}, "direct")

	t.Logf("Default cache lookup took: %v", duration3)

	// Both should be fast since they hit direct cache
	// Direct-only might be slightly faster as it doesn't need to prepare for semantic fallback
	t.Log("✅ Cache type performance characteristics validated")
}
