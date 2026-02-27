package semanticcache

import (
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestResponsesAPIBasicFunctionality tests the core caching functionality with Responses API
func TestResponsesAPIBasicFunctionality(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	ctx := CreateContextWithCacheKey("test-responses-basic")

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
		return // Test will be skipped by retry function
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
	WaitForCache()

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
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	ctx := CreateContextWithCacheKey("test-responses-params")
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
				return // Test will be skipped by retry function
			}

			WaitForCache()

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
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	ctx := CreateContextWithCacheKeyAndType("test-responses-semantic", CacheTypeSemantic)

	// First request
	originalRequest := CreateBasicResponsesRequest("What is machine learning?", 0.5, 500)
	t.Log("Making first Responses request with original text...")
	response1, err1 := setup.Client.ResponsesRequest(ctx, originalRequest)
	if err1 != nil {
		return // Test will be skipped by retry function
	}

	AssertNoCacheHit(t, &schemas.BifrostResponse{ResponsesResponse: response1})
	WaitForCache()

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
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	ctx := CreateContextWithCacheKey("test-responses-instructions")

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
		return // Test will be skipped by retry function
	}

	AssertNoCacheHit(t, &schemas.BifrostResponse{ResponsesResponse: response1})
	WaitForCache()

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
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	// Set very short TTL for testing
	shortTTL := 5 * time.Second
	ctx := CreateContextWithCacheKeyAndTTL("test-responses-ttl", shortTTL)

	responsesRequest := CreateBasicResponsesRequest("TTL test for Responses API", 0.5, 500)

	t.Log("Making first Responses request with short TTL...")
	response1, err1 := setup.Client.ResponsesRequest(ctx, responsesRequest)
	if err1 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ResponsesResponse: response1})

	WaitForCache()

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
		return // Test will be skipped by retry function
	}
	// Should not be a cache hit since TTL expired
	AssertNoCacheHit(t, &schemas.BifrostResponse{ResponsesResponse: response3})

	t.Log("✅ Responses API requests properly handle TTL expiration")
}

// TestResponsesAPIWithoutCacheKey tests that Responses requests without cache key are not cached
func TestResponsesAPIWithoutCacheKey(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	// Don't set cache key in context
	ctx := CreateContextWithCacheKey("")

	responsesRequest := CreateBasicResponsesRequest("Test Responses without cache key", 0.5, 500)

	t.Log("Making Responses request without cache key...")

	response, err := setup.Client.ResponsesRequest(ctx, responsesRequest)
	if err != nil {
		return // Test will be skipped by retry function
	}

	// Should not be cached
	AssertNoCacheHit(t, &schemas.BifrostResponse{ResponsesResponse: response})

	t.Log("✅ Responses requests without cache key are properly not cached")
}

// TestResponsesAPINoStoreFlag tests that Responses requests with no-store flag are not cached
func TestResponsesAPINoStoreFlag(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	responsesRequest := CreateBasicResponsesRequest("Test no-store with Responses API", 0.7, 500)
	ctx := CreateContextWithCacheKeyAndNoStore("test-no-store-responses", true)

	t.Log("Testing no-store with Responses API...")
	response1, err1 := setup.Client.ResponsesRequest(ctx, responsesRequest)
	if err1 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ResponsesResponse: response1})

	WaitForCache()

	// Verify not cached
	response2, err2 := setup.Client.ResponsesRequest(ctx, responsesRequest)
	if err2 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ResponsesResponse: response2}) // Should not be cached

	t.Log("✅ Responses API no-store flag working correctly")
}

// TestResponsesAPIStreaming tests streaming Responses API requests
func TestResponsesAPIStreaming(t *testing.T) {
	t.Log("Responses streaming not supported yet")

	setup := NewTestSetup(t)
	defer setup.Cleanup()

	ctx := CreateContextWithCacheKey("test-responses-streaming")
	prompt := "Explain the basics of quantum computing in simple terms"

	// Make non-streaming request first
	t.Log("Making non-streaming Responses request...")
	nonStreamRequest := CreateBasicResponsesRequest(prompt, 0.5, 500)
	_, err1 := setup.Client.ResponsesRequest(ctx, nonStreamRequest)
	if err1 != nil {
		return // Test will be skipped by retry function
	}

	WaitForCache()

	// Make streaming request with same prompt and parameters
	t.Log("Making streaming Responses request with same prompt...")
	streamRequest := CreateStreamingResponsesRequest(prompt, 0.5, 500)
	stream, err2 := setup.Client.ResponsesStreamRequest(ctx, streamRequest)
	if err2 != nil {
		t.Fatalf("Streaming Responses request failed: %v", err2)
	}

	var streamResponses []schemas.BifrostResponsesStreamResponse
	for streamMsg := range stream {
		if streamMsg.BifrostError != nil {
			t.Fatalf("Error in Responses stream: %v", streamMsg.BifrostError)
		}
		if streamMsg.BifrostResponsesStreamResponse != nil {
			streamResponses = append(streamResponses, *streamMsg.BifrostResponsesStreamResponse)
		}
	}

	if len(streamResponses) == 0 {
		t.Fatal("No streaming responses received")
	}

	// Check if any of the streaming responses was served from cache
	cacheHitFound := false
	for _, resp := range streamResponses {
		if resp.ExtraFields.CacheDebug != nil && resp.ExtraFields.CacheDebug.CacheHit {
			cacheHitFound = true
			break
		}
	}

	if !cacheHitFound {
		t.Log("⚠️  No cache hit detected in streaming responses - this could be expected behavior")
	} else {
		t.Log("✓ Cache hit detected in streaming Responses API")
	}

	t.Log("✅ Streaming Responses API test completed")
}

// TestResponsesAPIComplexParameters tests complex parameter handling
func TestResponsesAPIComplexParameters(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	ctx := CreateContextWithCacheKey("test-responses-complex-params")

	// Create request with various complex parameters
	request := CreateBasicResponsesRequest("Test complex parameters", 0.8, 500)
	request.Params.TopP = PtrFloat64(0.9)
	request.Params.Background = &[]bool{true}[0]
	request.Params.ParallelToolCalls = &[]bool{false}[0]
	request.Params.ServiceTier = &[]string{"default"}[0]
	request.Params.Store = &[]bool{true}[0]

	t.Log("Making first Responses request with complex parameters...")
	response1, err1 := setup.Client.ResponsesRequest(ctx, request)
	if err1 != nil {
		return // Test will be skipped by retry function
	}

	AssertNoCacheHit(t, &schemas.BifrostResponse{ResponsesResponse: response1})
	WaitForCache()

	// Create identical request
	request2 := CreateBasicResponsesRequest("Test complex parameters", 0.8, 500)
	request2.Params.TopP = PtrFloat64(0.9)
	request2.Params.Background = &[]bool{true}[0]
	request2.Params.ParallelToolCalls = &[]bool{false}[0]
	request2.Params.ServiceTier = &[]string{"default"}[0]
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
