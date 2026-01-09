package semanticcache

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestCacheNoStoreBasicFunctionality tests that CacheNoStoreKey prevents caching
func TestCacheNoStoreBasicFunctionality(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	testRequest := CreateBasicChatRequest("What is artificial intelligence?", 0.7, 100)

	// Test 1: Normal caching (control test)
	ctx1 := CreateContextWithCacheKey("test-no-store-control")
	t.Log("Making normal request (should be cached)...")
	response1, err1 := setup.Client.ChatCompletionRequest(ctx1, testRequest)
	if err1 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1}) // Fresh request

	WaitForCache()

	// Verify it got cached
	t.Log("Verifying normal caching worked...")
	response2, err2 := setup.Client.ChatCompletionRequest(ctx1, testRequest)
	if err2 != nil {
		if err2.Error != nil {
			t.Fatalf("Second request failed: %v", err2.Error.Message)
		} else {
			t.Fatalf("Second request failed: %v", err2)
		}
	}
	AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2}, "direct") // Should be cached

	// Test 2: NoStore = true (should not cache)
	ctx2 := CreateContextWithCacheKeyAndNoStore("test-no-store-disabled", true)
	t.Log("Making request with CacheNoStoreKey=true (should not be cached)...")
	response3, err3 := setup.Client.ChatCompletionRequest(ctx2, testRequest)
	if err3 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response3}) // Fresh request

	WaitForCache()

	// Verify it was NOT cached
	t.Log("Verifying no-store request was not cached...")
	response4, err4 := setup.Client.ChatCompletionRequest(ctx2, testRequest)
	if err4 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response4}) // Should still be fresh (not cached)

	// Test 3: NoStore = false (should cache normally)
	ctx3 := CreateContextWithCacheKeyAndNoStore("test-no-store-enabled", false)
	t.Log("Making request with CacheNoStoreKey=false (should be cached)...")
	response5, err5 := setup.Client.ChatCompletionRequest(ctx3, testRequest)
	if err5 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response5}) // Fresh request

	WaitForCache()

	// Verify it got cached
	t.Log("Verifying no-store=false request was cached...")
	response6, err6 := setup.Client.ChatCompletionRequest(ctx3, testRequest)
	if err6 != nil {
		t.Fatalf("Sixth request failed: %v", err6)
	}
	AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response6}, "direct") // Should be cached

	t.Log("✅ CacheNoStoreKey basic functionality works correctly")
}

// TestCacheNoStoreWithDifferentRequestTypes tests NoStore with various request types
func TestCacheNoStoreWithDifferentRequestTypes(t *testing.T) {
	t.Skip("Skipping Embedding Tests")

	setup := NewTestSetup(t)
	defer setup.Cleanup()

	// Test with chat completion
	chatRequest := CreateBasicChatRequest("Test no-store with chat", 0.7, 50)
	ctx1 := CreateContextWithCacheKeyAndNoStore("test-no-store-chat", true)

	t.Log("Testing no-store with chat completion...")
	response1, err1 := setup.Client.ChatCompletionRequest(ctx1, chatRequest)
	if err1 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1})

	WaitForCache()

	// Verify not cached
	response2, err2 := setup.Client.ChatCompletionRequest(ctx1, chatRequest)
	if err2 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2}) // Should not be cached

	// Test with embedding request
	embeddingRequest := CreateEmbeddingRequest([]string{"Test no-store with embeddings"})
	ctx2 := CreateContextWithCacheKeyAndNoStore("test-no-store-embedding", true)

	t.Log("Testing no-store with embedding request...")
	response3, err3 := setup.Client.EmbeddingRequest(ctx2, embeddingRequest)
	if err3 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{EmbeddingResponse: response3})

	WaitForCache()

	// Verify not cached
	response4, err4 := setup.Client.EmbeddingRequest(ctx2, embeddingRequest)
	if err4 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{EmbeddingResponse: response4}) // Should not be cached

	t.Log("✅ CacheNoStoreKey works with different request types")
}

// TestCacheNoStoreWithConversationHistory tests NoStore with conversation context
func TestCacheNoStoreWithConversationHistory(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	// Create conversation context
	conversation := BuildConversationHistory(
		"You are a helpful assistant",
		[]string{"Hello", "Hi! How can I help?"},
	)
	messages := AddUserMessage(conversation, "What is machine learning?")
	request := CreateConversationRequest(messages, 0.7, 100)

	// Test with no-store enabled
	ctx := CreateContextWithCacheKeyAndNoStore("test-no-store-conversation", true)

	t.Log("Testing no-store with conversation history...")
	response1, err1 := setup.Client.ChatCompletionRequest(ctx, request)
	if err1 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1})

	WaitForCache()

	// Verify not cached (same conversation should not hit cache)
	response2, err2 := setup.Client.ChatCompletionRequest(ctx, request)
	if err2 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2}) // Should not be cached due to no-store

	t.Log("✅ CacheNoStoreKey works with conversation history")
}

// TestCacheNoStoreWithCacheTypes tests NoStore interaction with CacheTypeKey
func TestCacheNoStoreWithCacheTypes(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	testRequest := CreateBasicChatRequest("Test no-store with cache types", 0.7, 50)

	// Test no-store with direct cache type
	ctx1 := CreateContextWithCacheKey("test-no-store-cache-types")
	ctx1 = ctx1.WithValue(CacheNoStoreKey, true)
	ctx1 = ctx1.WithValue(CacheTypeKey, CacheTypeDirect)

	t.Log("Testing no-store with CacheTypeKey=direct...")
	response1, err1 := setup.Client.ChatCompletionRequest(ctx1, testRequest)
	if err1 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1})

	WaitForCache()

	// Should not be cached
	response2, err2 := setup.Client.ChatCompletionRequest(ctx1, testRequest)
	if err2 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2}) // No-store should override cache type

	// Test no-store with semantic cache type
	ctx2 := CreateContextWithCacheKey("test-no-store-cache-types")
	ctx2 = ctx2.WithValue(CacheNoStoreKey, true)
	ctx2 = ctx2.WithValue(CacheTypeKey, CacheTypeSemantic)

	t.Log("Testing no-store with CacheTypeKey=semantic...")
	response3, err3 := setup.Client.ChatCompletionRequest(ctx2, testRequest)
	if err3 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response3})

	WaitForCache()

	// Should not be cached
	response4, err4 := setup.Client.ChatCompletionRequest(ctx2, testRequest)
	if err4 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response4}) // No-store should override cache type

	t.Log("✅ CacheNoStoreKey correctly overrides cache type settings")
}

// TestCacheNoStoreErrorHandling tests error scenarios with NoStore
func TestCacheNoStoreErrorHandling(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	testRequest := CreateBasicChatRequest("Test no-store error handling", 0.7, 50)

	// Test with invalid no-store value (non-boolean)
	ctx1 := CreateContextWithCacheKey("test-no-store-errors")
	ctx1 = ctx1.WithValue(CacheNoStoreKey, "invalid")

	t.Log("Testing no-store with invalid value (should cache normally)...")
	response1, err1 := setup.Client.ChatCompletionRequest(ctx1, testRequest)
	if err1 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1})

	WaitForCache()

	// Should be cached (invalid value should be ignored)
	response2, err2 := setup.Client.ChatCompletionRequest(ctx1, testRequest)
	if err2 != nil {
		if err2.Error != nil {
			t.Fatalf("Second request failed: %v", err2.Error.Message)
		} else {
			t.Fatalf("Second request failed: %v", err2)
		}
	}
	AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2}, "direct") // Should be cached (invalid value ignored)

	// Test with nil value (should cache normally)
	ctx2 := CreateContextWithCacheKey("test-no-store-nil")
	ctx2 = ctx2.WithValue(CacheNoStoreKey, nil)

	t.Log("Testing no-store with nil value (should cache normally)...")
	response3, err3 := setup.Client.ChatCompletionRequest(ctx2, testRequest)
	if err3 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response3})

	WaitForCache()

	// Should be cached (nil should be treated as normal caching)
	response4, err4 := setup.Client.ChatCompletionRequest(ctx2, testRequest)
	if err4 != nil {
		t.Fatalf("Fourth request failed: %v", err4)
	}
	AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response4}, "direct") // Should be cached (nil ignored)

	t.Log("✅ CacheNoStoreKey error handling works correctly")
}

// TestCacheNoStoreReadButNoWrite tests that NoStore allows reading cache but prevents writing
func TestCacheNoStoreReadButNoWrite(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	testRequest := CreateBasicChatRequest("Describe Isaac Newton's three laws of motion", 0.7, 50)

	// Step 1: Cache a response normally
	ctx1 := CreateContextWithCacheKey("test-no-store-read")
	t.Log("Caching response normally...")
	response1, err1 := setup.Client.ChatCompletionRequest(ctx1, testRequest)
	if err1 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1})

	WaitForCache()

	// Step 2: Try to read with no-store enabled (should still read from cache)
	ctx2 := CreateContextWithCacheKeyAndNoStore("test-no-store-read", true)
	t.Log("Reading with no-store enabled (should still hit cache for reads)...")
	response2, err2 := setup.Client.ChatCompletionRequest(ctx2, testRequest)
	if err2 != nil {
		if err2.Error != nil {
			t.Fatalf("Second request failed: %v", err2.Error.Message)
		} else {
			t.Fatalf("Second request failed: %v", err2)
		}
	}
	// The current implementation should still read from cache even with no-store
	// (no-store only affects writing, not reading)
	AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2}, "direct")

	// Step 3: Make a semantically similar request with no-store (strong paraphrase for deterministic semantic hit)
	newRequest := CreateBasicChatRequest("Describe the three laws of motion by Isaac Newton", 0.7, 50)
	t.Log("Making semantically similar request with no-store (should get semantic hit, but not cache response)...")
	response3, err3 := setup.Client.ChatCompletionRequest(ctx2, newRequest)
	if err3 != nil {
		t.Fatalf("Third request failed: %v", err3)
	}
	// Should get semantic cache hit (no-store allows reads, just prevents writes)
	AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response3}, "semantic")

	WaitForCache()

	// Step 4: Repeat similar request with no-store (should still get semantic hit)
	t.Log("Repeating similar request with no-store (should still get semantic hit)...")
	response4, err4 := setup.Client.ChatCompletionRequest(ctx2, newRequest)
	if err4 != nil {
		t.Fatalf("Fourth request failed: %v", err4)
	}
	// Should get semantic cache hit again (consistent behavior)
	AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response4}, "semantic")

	t.Log("✅ CacheNoStoreKey allows reading but prevents writing")
}
