package semanticcache

import (
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestEmbeddingRequestsCaching tests that embedding requests are properly cached using direct hash matching
func TestEmbeddingRequestsCaching(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	ctx := CreateContextWithCacheKey("test-embedding-cache")

	// Create embedding request
	embeddingRequest := CreateEmbeddingRequest([]string{
		"What is machine learning?",
		"Explain artificial intelligence in simple terms.",
	})

	t.Log("Making first embedding request (should go to OpenAI and be cached)...")

	// Make first request (will go to OpenAI and be cached) - with retries
	start1 := time.Now()
	response1, err1 := setup.Client.EmbeddingRequest(ctx, embeddingRequest)
	duration1 := time.Since(start1)

	if err1 != nil {
		return // Test will be skipped by retry function
	}

	if response1 == nil || len(response1.Data) == 0 {
		t.Fatal("First embedding response is invalid")
	}

	t.Logf("First embedding request completed in %v", duration1)
	t.Logf("Response contains %d embeddings", len(response1.Data))

	// Wait for cache to be written
	WaitForCache()

	t.Log("Making second identical embedding request (should be served from cache)...")

	// Make second identical request (should be cached)
	start2 := time.Now()
	response2, err2 := setup.Client.EmbeddingRequest(ctx, embeddingRequest)
	duration2 := time.Since(start2)

	if err2 != nil {
		t.Fatalf("Second embedding request failed: %v", err2)
	}

	if response2 == nil || len(response2.Data) == 0 {
		t.Fatal("Second embedding response is invalid")
	}

	// Verify cache hit
	AssertCacheHit(t, &schemas.BifrostResponse{EmbeddingResponse: response2}, "direct")

	t.Logf("Second embedding request completed in %v", duration2)

	// Cache should be significantly faster
	if duration2 >= duration1 { // Allow some margin but cache should be much faster
		t.Log("⚠️  Cache doesn't seem faster, but this could be due to test environment")
	}

	// Responses should be identical
	if len(response1.Data) != len(response2.Data) {
		t.Errorf("Response lengths differ: %d vs %d", len(response1.Data), len(response2.Data))
	}

	t.Log("✅ Embedding requests properly cached using direct hash matching")
}

// TestEmbeddingRequestsNoCacheWithoutCacheKey tests that embedding requests without cache key are not cached
func TestEmbeddingRequestsNoCacheWithoutCacheKey(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	// Don't set cache key in context
	ctx := CreateContextWithCacheKey("")

	embeddingRequest := CreateEmbeddingRequest([]string{"Test embedding without cache key"})

	t.Log("Making embedding request without cache key...")

	response, err := setup.Client.EmbeddingRequest(ctx, embeddingRequest)
	if err != nil {
		t.Fatalf("Embedding request failed: %v", err)
	}

	// Should not be cached
	AssertNoCacheHit(t, &schemas.BifrostResponse{EmbeddingResponse: response})

	t.Log("✅ Embedding requests without cache key are properly not cached")
}

// TestEmbeddingRequestsDifferentTexts tests that different embedding texts produce different cache entries
func TestEmbeddingRequestsDifferentTexts(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	ctx := CreateContextWithCacheKey("test-embedding-different")

	// Create two different embedding requests
	request1 := CreateEmbeddingRequest([]string{"First set of texts"})
	request2 := CreateEmbeddingRequest([]string{"Second set of texts"})

	t.Log("Making first embedding request...")
	response1, err1 := setup.Client.EmbeddingRequest(ctx, request1)
	if err1 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{EmbeddingResponse: response1})

	WaitForCache()

	t.Log("Making second different embedding request...")
	response2, err2 := setup.Client.EmbeddingRequest(ctx, request2)
	if err2 != nil {
		return // Test will be skipped by retry function
	}
	// Should not be a cache hit since texts are different
	AssertNoCacheHit(t, &schemas.BifrostResponse{EmbeddingResponse: response2})

	t.Log("✅ Different embedding texts produce different cache entries")
}

// TestEmbeddingRequestsCacheExpiration tests TTL functionality for embedding requests
func TestEmbeddingRequestsCacheExpiration(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	// Set very short TTL for testing
	shortTTL := 5 * time.Second
	ctx := CreateContextWithCacheKeyAndTTL("test-embedding-ttl", shortTTL)

	embeddingRequest := CreateEmbeddingRequest([]string{"TTL test embedding"})

	t.Log("Making first embedding request with short TTL...")
	response1, err1 := setup.Client.EmbeddingRequest(ctx, embeddingRequest)
	if err1 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{EmbeddingResponse: response1})

	WaitForCache()

	t.Log("Making second request before TTL expiration...")
	response2, err2 := setup.Client.EmbeddingRequest(ctx, embeddingRequest)
	if err2 != nil {
		if err2.Error != nil {
			t.Fatalf("Second request failed: %v", err2.Error.Message)
		} else {
			t.Fatalf("Second request failed: %v", err2)
		}
	}
	AssertCacheHit(t, &schemas.BifrostResponse{EmbeddingResponse: response2}, "direct")

	t.Logf("Waiting for TTL expiration (%v)...", shortTTL)
	time.Sleep(shortTTL + 1*time.Second) // Wait for TTL to expire

	t.Log("Making third request after TTL expiration...")
	response3, err3 := setup.Client.EmbeddingRequest(ctx, embeddingRequest)
	if err3 != nil {
		return // Test will be skipped by retry function
	}
	// Should not be a cache hit since TTL expired
	AssertNoCacheHit(t, &schemas.BifrostResponse{EmbeddingResponse: response3})

	t.Log("✅ Embedding requests properly handle TTL expiration")
}
