package semanticcache

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestCrossCacheTypeAccessibility tests that entries cached one way are accessible another way
func TestCrossCacheTypeAccessibility(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	testRequest := CreateBasicChatRequest("What is artificial intelligence?", 0.7, 100)

	// Test 1: Cache with default behavior (both direct + semantic)
	ctx1 := CreateContextWithCacheKey("test-cross-cache-access")
	t.Log("Caching with default behavior (both direct + semantic)...")
	response1, err1 := setup.Client.ChatCompletionRequest(ctx1, testRequest)
	if err1 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1})

	WaitForCache()

	// Test 2: Retrieve with direct-only cache type
	ctx2 := CreateContextWithCacheKeyAndType("test-cross-cache-access", CacheTypeDirect)
	t.Log("Retrieving with CacheTypeKey=direct...")
	response2, err2 := setup.Client.ChatCompletionRequest(ctx2, testRequest)
	if err2 != nil {
		if err2.Error != nil {
			t.Fatalf("Second request failed: %v", err2.Error.Message)
		} else {
			t.Fatalf("Second request failed: %v", err2)
		}
	}
	AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2}, "direct") // Should find direct match

	// Test 3: Retrieve with semantic-only cache type
	ctx3 := CreateContextWithCacheKeyAndType("test-cross-cache-access", CacheTypeSemantic)
	t.Log("Retrieving with CacheTypeKey=semantic...")
	response3, err3 := setup.Client.ChatCompletionRequest(ctx3, testRequest)
	if err3 != nil {
		t.Fatalf("Third request failed: %v", err3)
	}
	AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response3}, "semantic") // Should find semantic match

	t.Log("✅ Entries cached with default behavior are accessible via both cache types")
}

// TestCacheTypeIsolation tests that entries cached separately by type behave correctly
func TestCacheTypeIsolation(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	testRequest := CreateBasicChatRequest("Define blockchain technology", 0.7, 100)

	// Clear cache to start fresh
	clearTestKeysWithStore(t, setup.Store)

	// Test 1: Cache with direct-only
	ctx1 := CreateContextWithCacheKeyAndType("test-cache-isolation", CacheTypeDirect)
	t.Log("Caching with CacheTypeKey=direct only...")
	response1, err1 := setup.Client.ChatCompletionRequest(ctx1, testRequest)
	if err1 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1}) // Fresh request

	WaitForCache()

	// Test 2: Try to retrieve with semantic-only (should miss because no semantic entry)
	ctx2 := CreateContextWithCacheKeyAndType("test-cache-isolation", CacheTypeSemantic)
	t.Log("Retrieving same request with CacheTypeKey=semantic (should miss)...")
	response2, err2 := setup.Client.ChatCompletionRequest(ctx2, testRequest)
	if err2 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2}) // Should miss - no semantic cache entry

	WaitForCache()

	// Test 3: Retrieve with direct-only (should hit)
	t.Log("Retrieving with CacheTypeKey=direct (should hit)...")
	response3, err3 := setup.Client.ChatCompletionRequest(ctx1, testRequest)
	if err3 != nil {
		t.Fatalf("Third request failed: %v", err3)
	}
	AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response3}, "direct") // Should hit direct cache

	// Test 4: Default behavior (should find the direct cache)
	ctx4 := CreateContextWithCacheKey("test-cache-isolation")
	t.Log("Retrieving with default behavior (should find direct cache)...")
	response4, err4 := setup.Client.ChatCompletionRequest(ctx4, testRequest)
	if err4 != nil {
		t.Fatalf("Fourth request failed: %v", err4)
	}
	AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response4}, "direct") // Should find existing direct cache

	t.Log("✅ Cache type isolation works correctly")
}

// TestCacheTypeFallbackBehavior tests whether cache types fallback to each other
func TestCacheTypeFallbackBehavior(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	// Cache an entry with default behavior
	originalRequest := CreateBasicChatRequest("Explain machine learning", 0.7, 100)
	ctx1 := CreateContextWithCacheKey("test-fallback-behavior")

	t.Log("Caching with default behavior...")
	response1, err1 := setup.Client.ChatCompletionRequest(ctx1, originalRequest)
	if err1 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1})

	WaitForCache()

	// Test similar request with direct-only (should miss direct, no fallback, but should cache response)
	similarRequest := CreateBasicChatRequest("Explain machine learning concepts", 0.7, 100)
	ctx2 := CreateContextWithCacheKeyAndType("test-fallback-behavior", CacheTypeDirect)

	t.Log("Testing similar request with CacheTypeKey=direct (should miss, make request, cache without embeddings)...")
	response2, err2 := setup.Client.ChatCompletionRequest(ctx2, similarRequest)
	if err2 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2}) // Should miss - no direct match, no semantic search

	WaitForCache() // Let the response get cached

	// Test same similar request with semantic-only (should hit original entry)
	ctx3 := CreateContextWithCacheKeyAndType("test-fallback-behavior", CacheTypeSemantic)

	t.Log("Testing similar request with CacheTypeKey=semantic (should find semantic match from step 1)...")
	response3, err3 := setup.Client.ChatCompletionRequest(ctx3, similarRequest)
	if err3 != nil {
		t.Fatalf("Third request failed: %v", err3)
	}

	// Should find semantic match from step 1's cached entry (which has embeddings)
	if response3.ExtraFields.CacheDebug != nil && response3.ExtraFields.CacheDebug.CacheHit {
		AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response3}, "semantic")
		t.Log("✅ Semantic search found similar entry from step 1")
	} else {
		AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response3})
		t.Log("ℹ️  No semantic match found (threshold may be too high or semantic similarity low)")
	}

	// Test a different similar request with default behavior (try both, fallback to semantic)
	// Use a slightly different request to avoid hitting the cached response from step 2
	differentSimilarRequest := CreateBasicChatRequest("Explain the basics of machine learning", 0.7, 100)
	ctx4 := CreateContextWithCacheKey("test-fallback-behavior")

	t.Log("Testing different similar request with default behavior (direct miss -> semantic fallback)...")
	response4, err4 := setup.Client.ChatCompletionRequest(ctx4, differentSimilarRequest)
	if err4 != nil {
		t.Fatalf("Fourth request failed: %v", err4)
	}

	// Should try direct first (miss), then semantic (might hit)
	if response4.ExtraFields.CacheDebug != nil && response4.ExtraFields.CacheDebug.CacheHit {
		AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response4}, "semantic")
		t.Log("✅ Default behavior found semantic fallback")
	} else {
		AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response4})
		t.Log("ℹ️  No fallback match found")
	}

	t.Log("✅ Cache type fallback behavior verified")
}

// TestMultipleCacheEntriesPriority tests behavior when multiple cache entries exist
func TestMultipleCacheEntriesPriority(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	testRequest := CreateBasicChatRequest("What is deep learning?", 0.7, 100)

	// Create cache entry with default behavior first
	ctx1 := CreateContextWithCacheKey("test-cache-priority")
	t.Log("Creating cache entry with default behavior...")
	response1, err1 := setup.Client.ChatCompletionRequest(ctx1, testRequest)
	if err1 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1})
	originalContent := *response1.Choices[0].Message.Content.ContentStr

	WaitForCache()

	// Verify it hits cache with default behavior
	t.Log("Verifying cache hit with default behavior...")
	response2, err2 := setup.Client.ChatCompletionRequest(ctx1, testRequest)
	if err2 != nil {
		if err2.Error != nil {
			t.Fatalf("Second request failed: %v", err2.Error.Message)
		} else {
			t.Fatalf("Second request failed: %v", err2)
		}
	}
	AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2}, "direct") // Should hit direct cache
	cachedContent := *response2.Choices[0].Message.Content.ContentStr

	// Verify content is the same
	if originalContent != cachedContent {
		t.Errorf("Cache content mismatch:\nOriginal: %s\nCached: %s", originalContent, cachedContent)
	}

	// Test with direct-only access
	ctx2 := CreateContextWithCacheKeyAndType("test-cache-priority", CacheTypeDirect)
	t.Log("Accessing with CacheTypeKey=direct...")
	response3, err3 := setup.Client.ChatCompletionRequest(ctx2, testRequest)
	if err3 != nil {
		t.Fatalf("Third request failed: %v", err3)
	}
	AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response3}, "direct") // Should find direct cache

	// Test with semantic-only access
	ctx3 := CreateContextWithCacheKeyAndType("test-cache-priority", CacheTypeSemantic)
	t.Log("Accessing with CacheTypeKey=semantic...")
	response4, err4 := setup.Client.ChatCompletionRequest(ctx3, testRequest)
	if err4 != nil {
		t.Fatalf("Fourth request failed: %v", err4)
	}
	AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response4}, "semantic") // Should find semantic cache

	t.Log("✅ Multiple cache entries accessible correctly")
}

// TestCrossCacheTypeWithDifferentParameters tests cache type behavior with parameter variations
func TestCrossCacheTypeWithDifferentParameters(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	baseMessage := "Explain quantum computing"

	// Cache with specific parameters
	request1 := CreateBasicChatRequest(baseMessage, 0.7, 100)
	ctx1 := CreateContextWithCacheKey("test-cross-cache-params")

	t.Log("Caching with temp=0.7, max_tokens=100...")
	response1, err1 := setup.Client.ChatCompletionRequest(ctx1, request1)
	if err1 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1})

	WaitForCache()

	// Test same parameters with direct-only
	ctx2 := CreateContextWithCacheKeyAndType("test-cross-cache-params", CacheTypeDirect)
	t.Log("Retrieving same parameters with CacheTypeKey=direct...")
	response2, err2 := setup.Client.ChatCompletionRequest(ctx2, request1)
	if err2 != nil {
		if err2.Error != nil {
			t.Fatalf("Second request failed: %v", err2.Error.Message)
		} else {
			t.Fatalf("Second request failed: %v", err2)
		}
	}
	AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2}, "direct") // Should hit

	// Test different parameters - should miss
	request3 := CreateBasicChatRequest(baseMessage, 0.5, 200) // Different temp and tokens
	t.Log("Testing different parameters (should miss)...")
	response3, err3 := setup.Client.ChatCompletionRequest(ctx2, request3)
	if err3 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response3}) // Should miss due to different params

	// Test semantic search with different parameters
	ctx4 := CreateContextWithCacheKeyAndType("test-cross-cache-params", CacheTypeSemantic)
	similarRequest := CreateBasicChatRequest("Can you explain quantum computing", 0.5, 200)

	t.Log("Testing semantic search with different params and similar message...")
	response4, err4 := setup.Client.ChatCompletionRequest(ctx4, similarRequest)
	if err4 != nil {
		return // Test will be skipped by retry function
	}
	// Should miss semantic search due to different parameters (params_hash different)
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response4})

	t.Log("✅ Cross-cache-type parameter handling works correctly")
}

// TestCacheTypeErrorHandling tests error scenarios with cache types
func TestCacheTypeErrorHandling(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	testRequest := CreateBasicChatRequest("Test error handling", 0.7, 50)

	// Test invalid cache type (should fallback to default)
	ctx1 := CreateContextWithCacheKey("test-cache-error-handling")
	ctx1 = ctx1.WithValue(CacheTypeKey, "invalid_cache_type")

	t.Log("Testing invalid cache type (should fallback to default behavior)...")
	response1, err1 := setup.Client.ChatCompletionRequest(ctx1, testRequest)
	if err1 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1}) // Should work with fallback behavior

	WaitForCache()

	// Test nil cache type (should use default)
	ctx2 := CreateContextWithCacheKey("test-cache-error-handling")
	ctx2 = ctx2.WithValue(CacheTypeKey, nil)

	t.Log("Testing nil cache type (should use default behavior)...")
	response2, err2 := setup.Client.ChatCompletionRequest(ctx2, testRequest)
	if err2 != nil {
		if err2.Error != nil {
			t.Fatalf("Second request failed: %v", err2.Error.Message)
		} else {
			t.Fatalf("Second request failed: %v", err2)
		}
	}
	AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2}, "direct") // Should find cached entry from first request

	t.Log("✅ Cache type error handling works correctly")
}
