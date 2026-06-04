package semanticcache

import (
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestResponsesAPIBasicFunctionality tests the core caching functionality with Responses API
func TestResponsesAPIBasicFunctionality(t *testing.T) {
	t.Parallel()
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	ctx := CreateContextWithCacheKey(t, "test-responses-basic")

	// Create test request
	testRequest := CreateBasicResponsesRequest(
		"What is Bifrost? Answer in one short sentence.",
		0.7,
		500,
	)

	t.Log("Making first Responses API request (should go to OpenAI and be cached)...")

	// Make first request (will go to OpenAI and be cached) - with retries
	start1 := time.Now()
	response1, err1 := setup.Client.ResponsesRequest(ctx, testRequest)
	duration1 := time.Since(start1)

	if err1 != nil {
		t.Skipf("upstream request error, skipping test: %v", err1)
	}

	if response1 == nil || len(response1.Output) == 0 {
		t.Fatal("First Responses response is invalid")
	}

	t.Logf("First request completed in %v", duration1)
	t.Logf("Response contains %d output messages", len(response1.Output))
	if c := response1.Output[0].Content; c != nil && c.ContentStr != nil {
		t.Logf("Response: %s", *c.ContentStr)
	} else if c != nil && len(c.ContentBlocks) > 0 && c.ContentBlocks[0].Text != nil {
		t.Logf("Response: %s", *c.ContentBlocks[0].Text)
	} else {
		t.Log("Response: <no text>")
	}

	// Wait for cache to be written
	WaitForCache(setup.Plugin)

	t.Log("Making second identical Responses API request (should be served from cache)...")

	// Make second identical request (should be cached)
	start2 := time.Now()
	response2, err2 := setup.Client.ResponsesRequest(ctx, testRequest)
	duration2 := time.Since(start2)

	if err2 != nil {
		t.Fatalf("Second Responses request failed: %v", err2)
	}

	if response2 == nil || len(response2.Output) == 0 {
		t.Fatal("Second Responses response is invalid")
	}
	if response2.Output[0].Content.ContentStr != nil {
		t.Logf("Response: %s", *response2.Output[0].Content.ContentStr)
	} else {
		t.Logf("Response: %v", *response2.Output[0].Content.ContentBlocks[0].Text)
	}

	t.Logf("Second request completed in %v", duration2)

	// Verify cache hit
	AssertCacheHit(t, &schemas.BifrostResponse{ResponsesResponse: response2}, string(CacheTypeDirect))

	// Performance comparison
	t.Logf("Performance Summary:")
	t.Logf("First request (OpenAI):  %v", duration1)
	t.Logf("Second request (Cache):  %v", duration2)

	if duration2 >= duration1 {
		t.Log("⚠️  Cache doesn't seem faster, but this could be due to test environment")
	}

	// Verify provider information is maintained in cached response
	if response2.ExtraFields.Provider != testRequest.Provider {
		t.Errorf("Provider mismatch in cached response: expected %s, got %s",
			testRequest.Provider, response2.ExtraFields.Provider)
	}

	t.Log("✅ Basic Responses API semantic caching test completed successfully!")
}

// TestResponsesAPIDifferentParameters tests that different parameters produce different cache entries
func TestResponsesAPIDifferentParameters(t *testing.T) {
	t.Parallel()
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	ctx := CreateContextWithCacheKey(t, "test-responses-params")
	basePrompt := "Explain quantum computing"

	tests := []struct {
		name        string
		request1    *schemas.BifrostResponsesRequest
		request2    *schemas.BifrostResponsesRequest
		shouldCache bool
	}{
		{
			name:        "Identical Requests",
			request1:    CreateBasicResponsesRequest(basePrompt, 0.5, 500),
			request2:    CreateBasicResponsesRequest(basePrompt, 0.5, 500),
			shouldCache: true,
		},
		{
			name:        "Different Temperature",
			request1:    CreateBasicResponsesRequest(basePrompt, 0.1, 500),
			request2:    CreateBasicResponsesRequest(basePrompt, 0.9, 500),
			shouldCache: false,
		},
		{
			name:        "Different MaxOutputTokens",
			request1:    CreateBasicResponsesRequest(basePrompt, 0.5, 500),
			request2:    CreateBasicResponsesRequest(basePrompt, 0.5, 200),
			shouldCache: false,
		},
		{
			name:        "Different Instructions",
			request1:    CreateResponsesRequestWithInstructions(basePrompt, "Be concise", 0.5, 500),
			request2:    CreateResponsesRequestWithInstructions(basePrompt, "Be detailed", 0.5, 500),
			shouldCache: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear cache for this subtest
			clearTestKeysWithStore(t, setup.Store)

			// Make first request
			_, err1 := setup.Client.ResponsesRequest(ctx, tt.request1)
			if err1 != nil {
				t.Skipf("upstream request error, skipping test: %v", err1)
			}

			WaitForCache(setup.Plugin)

			// Make second request
			response2, err2 := setup.Client.ResponsesRequest(ctx, tt.request2)
			if err2 != nil {
				if err2.Error != nil {
					t.Fatalf("Second request failed: %v", err2.Error.Message)
				} else {
					t.Fatalf("Second request failed: %v", err2)
				}
			}

			if tt.shouldCache {
				AssertCacheHit(t, &schemas.BifrostResponse{ResponsesResponse: response2}, "direct")
				t.Log("✓ Parameters match: cache hit as expected")
			} else {
				AssertNoCacheHit(t, &schemas.BifrostResponse{ResponsesResponse: response2})
				t.Log("✓ Parameters differ: no cache hit as expected")
			}
		})
	}
}

// TestResponsesAPISemanticMatching tests semantic similarity matching with Responses API
func TestResponsesAPISemanticMatching(t *testing.T) {
	t.Parallel()
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	ctx := CreateContextWithCacheKeyAndType(t, "test-responses-semantic", CacheTypeSemantic)

	// First request
	originalRequest := CreateBasicResponsesRequest("What is machine learning?", 0.5, 500)
	t.Log("Making first Responses request with original text...")
	response1, err1 := setup.Client.ResponsesRequest(ctx, originalRequest)
	if err1 != nil {
		t.Skipf("upstream request error, skipping test: %v", err1)
	}

	AssertNoCacheHit(t, &schemas.BifrostResponse{ResponsesResponse: response1})
	WaitForCache(setup.Plugin)

	// Test semantic match with similar but different text
	semanticRequest := CreateBasicResponsesRequest("Can you explain machine learning concepts?", 0.5, 500)
	t.Log("Making semantically similar Responses request...")
	response2, err2 := setup.Client.ResponsesRequest(ctx, semanticRequest)
	if err2 != nil {
		if err2.Error != nil {
			t.Fatalf("Second request failed: %v", err2.Error.Message)
		} else {
			t.Fatalf("Second request failed: %v", err2)
		}
	}

	// This should be a semantic cache hit
	AssertCacheHit(t, &schemas.BifrostResponse{ResponsesResponse: response2}, "semantic")
	t.Log("✓ Semantic cache hit with similar content")
}

// TestResponsesAPIWithInstructions tests caching with system instructions
func TestResponsesAPIWithInstructions(t *testing.T) {
	t.Parallel()
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	ctx := CreateContextWithCacheKey(t, "test-responses-instructions")

	// Create request with instructions
	request1 := CreateResponsesRequestWithInstructions(
		"Explain artificial intelligence",
		"You are a helpful assistant. Be concise and accurate.",
		0.7,
		500,
	)

	t.Log("Making first Responses request with instructions...")
	response1, err1 := setup.Client.ResponsesRequest(ctx, request1)
	if err1 != nil {
		t.Skipf("upstream request error, skipping test: %v", err1)
	}

	AssertNoCacheHit(t, &schemas.BifrostResponse{ResponsesResponse: response1})
	WaitForCache(setup.Plugin)

	// Make identical request
	request2 := CreateResponsesRequestWithInstructions(
		"Explain artificial intelligence",
		"You are a helpful assistant. Be concise and accurate.",
		0.7,
		500,
	)

	t.Log("Making second identical Responses request with instructions...")
	response2, err2 := setup.Client.ResponsesRequest(ctx, request2)
	if err2 != nil {
		if err2.Error != nil {
			t.Fatalf("Second request failed: %v", err2.Error.Message)
		} else {
			t.Fatalf("Second request failed: %v", err2)
		}
	}

	// Should be a cache hit
	AssertCacheHit(t, &schemas.BifrostResponse{ResponsesResponse: response2}, "direct")
	t.Log("✓ Responses API with instructions cached correctly")
}

// TestResponsesAPICacheExpiration tests TTL functionality for Responses API requests
func TestResponsesAPICacheExpiration(t *testing.T) {
	t.Parallel()
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	// Set very short TTL for testing
	shortTTL := 5 * time.Second
	ctx := CreateContextWithCacheKeyAndTTL(t, "test-responses-ttl", shortTTL)

	responsesRequest := CreateBasicResponsesRequest("TTL test for Responses API", 0.5, 500)

	t.Log("Making first Responses request with short TTL...")
	response1, err1 := setup.Client.ResponsesRequest(ctx, responsesRequest)
	if err1 != nil {
		t.Skipf("upstream request error, skipping test: %v", err1)
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ResponsesResponse: response1})

	WaitForCache(setup.Plugin)

	t.Log("Making second Responses request before TTL expiration...")
	response2, err2 := setup.Client.ResponsesRequest(ctx, responsesRequest)
	if err2 != nil {
		if err2.Error != nil {
			t.Fatalf("Second request failed: %v", err2.Error.Message)
		} else {
			t.Fatalf("Second request failed: %v", err2)
		}
	}
	AssertCacheHit(t, &schemas.BifrostResponse{ResponsesResponse: response2}, "direct")

	t.Logf("Waiting for TTL expiration (%v)...", shortTTL)
	time.Sleep(shortTTL + 2*time.Second) // Wait for TTL to expire

	t.Log("Making third Responses request after TTL expiration...")
	response3, err3 := setup.Client.ResponsesRequest(ctx, responsesRequest)
	if err3 != nil {
		t.Skipf("upstream request error, skipping test: %v", err3)
	}
	// Should not be a cache hit since TTL expired
	AssertNoCacheHit(t, &schemas.BifrostResponse{ResponsesResponse: response3})

	t.Log("✅ Responses API requests properly handle TTL expiration")
}

// TestResponsesAPIWithoutCacheKey tests that Responses requests without cache key are not cached
func TestResponsesAPIWithoutCacheKey(t *testing.T) {
	t.Parallel()
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	// Don't set cache key in context. CreateContextWithCacheKey(t, "") would
	// still populate CacheKey from t.Name(); using a base context keeps it
	// unset so we exercise the cache-disabled path.
	ctx := newBaseTestContext()

	responsesRequest := CreateBasicResponsesRequest("Test Responses without cache key", 0.5, 500)

	t.Log("Making first Responses request without cache key...")
	response1, err := setup.Client.ResponsesRequest(ctx, responsesRequest)
	if err != nil {
		t.Skipf("upstream request error, skipping test: %v", err)
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ResponsesResponse: response1})

	WaitForCache(setup.Plugin)

	// A second identical request must also miss — proves the first one
	// was not silently cached against some default key.
	t.Log("Making second identical request — must also miss because nothing was cached...")
	ctx2 := newBaseTestContext()
	response2, err := setup.Client.ResponsesRequest(ctx2, responsesRequest)
	if err != nil {
		t.Skipf("upstream request error, skipping test: %v", err)
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ResponsesResponse: response2})

	t.Log("✅ Responses requests without cache key are properly not cached")
}

// TestResponsesAPINoStoreFlag tests that Responses requests with no-store flag are not cached
func TestResponsesAPINoStoreFlag(t *testing.T) {
	t.Parallel()
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	responsesRequest := CreateBasicResponsesRequest("Test no-store with Responses API", 0.7, 500)
	ctx := CreateContextWithCacheKeyAndNoStore(t, "test-no-store-responses", true)

	t.Log("Testing no-store with Responses API...")
	response1, err1 := setup.Client.ResponsesRequest(ctx, responsesRequest)
	if err1 != nil {
		t.Skipf("upstream request error, skipping test: %v", err1)
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ResponsesResponse: response1})

	WaitForCache(setup.Plugin)

	// Verify not cached
	response2, err2 := setup.Client.ResponsesRequest(ctx, responsesRequest)
	if err2 != nil {
		t.Skipf("upstream request error, skipping test: %v", err2)
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ResponsesResponse: response2}) // Should not be cached

	t.Log("✅ Responses API no-store flag working correctly")
}

// TestResponsesAPIStreaming tests streaming Responses API caching by warming
// the cache with a streaming request and replaying it with a second identical
// streaming request that must be served from cache.
func TestResponsesAPIStreaming(t *testing.T) {
	t.Parallel()
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	ctx := CreateContextWithCacheKey(t, "test-responses-streaming")
	prompt := "Explain the basics of quantum computing in simple terms"

	// Warm the cache with a streaming request — the plugin accumulates the
	// chunks and stores them on the final chunk.
	t.Log("Warming cache with first streaming Responses request...")
	streamRequest := CreateStreamingResponsesRequest(prompt, 0.5, 500)
	stream1, err1 := setup.Client.ResponsesStreamRequest(ctx, streamRequest)
	if err1 != nil {
		t.Skipf("upstream request error, skipping test: %v", err1)
	}
	chunkCount1 := 0
	for streamMsg := range stream1 {
		if streamMsg.BifrostError != nil {
			t.Fatalf("Error in first stream: %v", streamMsg.BifrostError)
		}
		if streamMsg.BifrostResponsesStreamResponse != nil {
			chunkCount1++
		}
	}
	if chunkCount1 == 0 {
		t.Fatal("first streaming request produced no chunks")
	}

	WaitForCache(setup.Plugin)

	// Second identical streaming request — must be served from cache. We
	// require AT LEAST ONE chunk with CacheHit=true (the final chunk gets
	// the cache_debug stamp during replay).
	t.Log("Replaying — second identical streaming request must serve from cache...")
	ctx2 := CreateContextWithCacheKey(t, "test-responses-streaming")
	stream2, err2 := setup.Client.ResponsesStreamRequest(ctx2, streamRequest)
	if err2 != nil {
		t.Fatalf("Second streaming Responses request failed: %v", err2)
	}

	cacheHitFound := false
	chunkCount2 := 0
	for streamMsg := range stream2 {
		if streamMsg.BifrostError != nil {
			t.Fatalf("Error in second stream: %v", streamMsg.BifrostError)
		}
		if streamMsg.BifrostResponsesStreamResponse != nil {
			chunkCount2++
			if cd := streamMsg.BifrostResponsesStreamResponse.ExtraFields.CacheDebug; cd != nil && cd.CacheHit {
				cacheHitFound = true
			}
		}
	}
	if chunkCount2 == 0 {
		t.Fatal("replay produced no chunks")
	}
	if !cacheHitFound {
		t.Fatal("expected at least one chunk with CacheDebug.CacheHit=true on streaming replay")
	}
	t.Log("✅ Streaming Responses API replay served from cache")
}

// TestResponsesAPIComplexParameters tests complex parameter handling
func TestResponsesAPIComplexParameters(t *testing.T) {
	t.Parallel()
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	ctx := CreateContextWithCacheKey(t, "test-responses-complex-params")

	// Create request with various complex parameters
	serviceTier := schemas.BifrostServiceTierDefault
	request := CreateBasicResponsesRequest("Test complex parameters", 0.8, 500)
	request.Params.TopP = PtrFloat64(0.9)
	request.Params.Background = &[]bool{true}[0]
	request.Params.ParallelToolCalls = &[]bool{false}[0]
	request.Params.ServiceTier = &serviceTier
	request.Params.Store = &[]bool{true}[0]

	t.Log("Making first Responses request with complex parameters...")
	response1, err1 := setup.Client.ResponsesRequest(ctx, request)
	if err1 != nil {
		t.Skipf("upstream request error, skipping test: %v", err1)
	}

	AssertNoCacheHit(t, &schemas.BifrostResponse{ResponsesResponse: response1})
	WaitForCache(setup.Plugin)

	// Create identical request
	request2 := CreateBasicResponsesRequest("Test complex parameters", 0.8, 500)
	request2.Params.TopP = PtrFloat64(0.9)
	request2.Params.Background = &[]bool{true}[0]
	request2.Params.ParallelToolCalls = &[]bool{false}[0]
	request2.Params.ServiceTier = &serviceTier
	request2.Params.Store = &[]bool{true}[0]

	t.Log("Making second identical Responses request with complex parameters...")
	response2, err2 := setup.Client.ResponsesRequest(ctx, request2)
	if err2 != nil {
		if err2.Error != nil {
			t.Fatalf("Second request failed: %v", err2.Error.Message)
		} else {
			t.Fatalf("Second request failed: %v", err2)
		}
	}

	// Should be a cache hit
	AssertCacheHit(t, &schemas.BifrostResponse{ResponsesResponse: response2}, "direct")
	t.Log("✓ Responses API with complex parameters cached correctly")
}
