package semanticcache

import (
	"os"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestImageGenerationCacheBasicFunctionality tests basic image generation caching
func TestImageGenerationCacheBasicFunctionality(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY not set")
	}
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	ctx := CreateContextWithCacheKey("test-image-gen-value")

	// Create test image generation request
	testRequest := CreateImageGenerationRequest(
		"A serene Japanese garden with cherry blossoms in spring",
		"1024x1024",
		"low",
	)

	t.Log("Making first image generation request (should go to OpenAI and be cached)...")

	// Make first request (will go to OpenAI and be cached)
	start1 := time.Now()
	response1, err1 := setup.Client.ImageGenerationRequest(ctx, testRequest)
	duration1 := time.Since(start1)

	if err1 != nil {
		t.Skipf("First image generation request failed (may be rate limited): %v", err1)
		return
	}

	if response1 == nil || len(response1.Data) == 0 {
		t.Fatal("First response is invalid or has no image data")
	}

	t.Logf("First request completed in %v", duration1)
	t.Logf("Response: ID=%s, Images=%d", response1.ID, len(response1.Data))

	// Wait for cache to be written
	WaitForCache()

	t.Log("Making second identical request (should be served from cache)...")

	// Make second identical request (should be cached)
	start2 := time.Now()
	response2, err2 := setup.Client.ImageGenerationRequest(ctx, testRequest)
	duration2 := time.Since(start2)

	if err2 != nil {
		if err2.Error != nil {
			t.Fatalf("Second request failed: %v", err2.Error.Message)
		} else {
			t.Fatalf("Second request failed: %v", err2)
		}
	}

	if response2 == nil || len(response2.Data) == 0 {
		t.Fatal("Second response is invalid or has no image data")
	}

	t.Logf("Second request completed in %v", duration2)

	// Verify cache hit
	AssertCacheHit(t, &schemas.BifrostResponse{ImageGenerationResponse: response2}, string(CacheTypeDirect))

	// Performance comparison
	t.Logf("Performance Summary:")
	t.Logf("First request (OpenAI):  %v", duration1)
	t.Logf("Second request (Cache):  %v", duration2)

	if duration2 < duration1 {
		if duration2 == 0 {
			t.Errorf("Second request duration too small to compute speedup (duration2=0)")
			return
		}
		speedup := float64(duration1) / float64(duration2)
		t.Logf("Cache speedup: %.2fx faster", speedup)
	} else {
		if duration2 == 0 {
			t.Errorf("Second request duration too small to compute speedup (duration2=0)")
			return
		}
		speedup := float64(duration1) / float64(duration2)
		t.Logf("Cache was slower than original: speedup=%.2fx (this can happen due to system load)", speedup)
		// Only fail if cache is extremely slow (10x+ slower), indicating a real problem
		if duration2 > duration1*10 {
			t.Errorf("Cache is extremely slow compared to original: cache=%v, original=%v (cache may not be working)", duration2, duration1)
		}
	}

	// Verify image data is preserved in cached response
	if len(response2.Data) != len(response1.Data) {
		t.Errorf("Image count differs between cached and original: original=%d, cached=%d",
			len(response1.Data), len(response2.Data))
	}

	// Verify provider information is maintained in cached response
	if response2.ExtraFields.Provider != testRequest.Provider {
		t.Errorf("Provider mismatch in cached response: expected %s, got %s",
			testRequest.Provider, response2.ExtraFields.Provider)
	}

	t.Log("✅ Basic image generation caching test completed successfully!")
}

// TestImageGenerationSemanticSearch tests semantic similarity search for image generation
func TestImageGenerationSemanticSearch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY not set")
	}
	// Initialize test with custom threshold
	config := &Config{
		Provider:       schemas.OpenAI,
		EmbeddingModel: "text-embedding-3-small",
		Dimension:      1536,
		Threshold:      0.5,
		Keys: []schemas.Key{
			{Value: os.Getenv("OPENAI_API_KEY"), Models: []string{}, Weight: 1.0},
		},
	}
	setup := NewTestSetupWithConfig(t, config)
	defer setup.Cleanup()

	ctx := CreateContextWithCacheKey("image-semantic-test-value")

	// First request - this will be cached
	firstRequest := CreateImageGenerationRequest(
		"A beautiful sunset over the ocean with golden clouds",
		"1024x1024",
		"low",
	)

	t.Log("Making first image generation request (should go to OpenAI and be cached)...")
	start1 := time.Now()
	response1, err1 := setup.Client.ImageGenerationRequest(ctx, firstRequest)
	duration1 := time.Since(start1)

	if err1 != nil {
		t.Skipf("First image generation request failed (may be rate limited): %v", err1)
		return
	}

	if response1 == nil || len(response1.Data) == 0 {
		t.Fatal("First response is invalid or has no image data")
	}

	t.Logf("First request completed in %v", duration1)

	// Wait for cache to be written
	WaitForCache()

	// Second request - very similar text to test semantic matching
	secondRequest := CreateImageGenerationRequest(
		"A gorgeous sunset over the sea with orange clouds",
		"1024x1024",
		"low",
	)

	t.Log("Making semantically similar request (should be served from semantic cache)...")
	start2 := time.Now()
	response2, err2 := setup.Client.ImageGenerationRequest(ctx, secondRequest)
	duration2 := time.Since(start2)

	if err2 != nil {
		if err2.Error != nil {
			t.Fatalf("Second request failed: %v", err2.Error.Message)
		} else {
			t.Fatalf("Second request failed: %v", err2)
		}
	}

	if response2 == nil || len(response2.Data) == 0 {
		t.Fatal("Second response is invalid or has no image data")
	}

	t.Logf("Second request completed in %v", duration2)

	// Check if second request was served from semantic cache
	semanticMatch := false

	if response2.ExtraFields.CacheDebug != nil && response2.ExtraFields.CacheDebug.CacheHit {
		if response2.ExtraFields.CacheDebug.HitType != nil && *response2.ExtraFields.CacheDebug.HitType == string(CacheTypeSemantic) {
			semanticMatch = true

			threshold := 0.0
			similarity := 0.0

			if response2.ExtraFields.CacheDebug.Threshold != nil {
				threshold = *response2.ExtraFields.CacheDebug.Threshold
			}
			if response2.ExtraFields.CacheDebug.Similarity != nil {
				similarity = *response2.ExtraFields.CacheDebug.Similarity
			}

			t.Logf("✅ Second request was served from semantic cache! Cache threshold: %f, Cache similarity: %f", threshold, similarity)
		}
	}

	if !semanticMatch {
		t.Error("Semantic match expected but not found")
		return
	}

	// Performance comparison
	t.Logf("Semantic Cache Performance:")
	t.Logf("First request (OpenAI):     %v", duration1)
	t.Logf("Second request (Semantic):  %v", duration2)

	if duration2 < duration1 {
		speedup := float64(duration1) / float64(duration2)
		t.Logf("Semantic cache speedup: %.2fx faster", speedup)
	} else {
		slowdown := float64(duration2) / float64(duration1)
		t.Logf("Semantic cache was slower than original: %.2fx slower (this can happen due to system load)", slowdown)
		// Only fail if cache is extremely slow (10x+ slower), indicating a real problem
		if slowdown > 10 {
			t.Errorf("Semantic cache is extremely slow compared to original: slowdown=%.2fx, cache=%v, original=%v (cache may not be working)", slowdown, duration2, duration1)
		}
	}

	t.Log("✅ Image generation semantic search test completed successfully!")
}

// TestImageGenerationDifferentParameters tests that different parameters are cached separately
func TestImageGenerationDifferentParameters(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY not set")
	}
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	ctx := CreateContextWithCacheKey("image-params-test")

	basePrompt := "A cute cat sitting on a windowsill"

	// First request with 1024x1024
	request1 := CreateImageGenerationRequest(basePrompt, "1024x1024", "low")

	t.Log("Making first request with 1024x1024...")
	_, err1 := setup.Client.ImageGenerationRequest(ctx, request1)
	if err1 != nil {
		t.Skipf("First image generation request failed (may be rate limited): %v", err1)
		return
	}

	WaitForCache()

	// Second request with different size - should NOT be cached
	request2 := CreateImageGenerationRequest(basePrompt, "1024x1536", "low")

	t.Log("Making second request with different size (1024x1536)...")
	response2, err2 := setup.Client.ImageGenerationRequest(ctx, request2)
	if err2 != nil {
		t.Skipf("Second image generation request failed (may be rate limited): %v", err2)
		return
	}

	// Should NOT be cached (different size)
	AssertNoCacheHit(t, &schemas.BifrostResponse{ImageGenerationResponse: response2})

	WaitForCache()

	// Third request with different quality - should NOT be cached
	request3 := CreateImageGenerationRequest(basePrompt, "1024x1024", "high")

	t.Log("Making third request with different quality (high)...")
	response3, err3 := setup.Client.ImageGenerationRequest(ctx, request3)
	if err3 != nil {
		t.Skipf("Third image generation request failed (may be rate limited): %v", err3)
		return
	}

	// Should NOT be cached (different quality)
	AssertNoCacheHit(t, &schemas.BifrostResponse{ImageGenerationResponse: response3})

	t.Log("✅ Image generation different parameters test completed!")
}

// TestImageGenerationStreamCaching tests streaming image generation caching
func TestImageGenerationStreamCaching(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY not set")
	}
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	ctx := CreateContextWithCacheKey("image-stream-test")

	// Create test image generation request
	testRequest := CreateImageGenerationRequest(
		"A futuristic city skyline at night with neon lights",
		"1024x1024",
		"low",
	)

	t.Log("Making first streaming image generation request...")

	// Make first streaming request
	start1 := time.Now()
	stream1, err1 := setup.Client.ImageGenerationStreamRequest(ctx, testRequest)
	if err1 != nil {
		t.Skipf("First streaming request failed (may be rate limited): %v", err1)
		return
	}

	var responses1 []schemas.BifrostImageGenerationStreamResponse
	for streamMsg := range stream1 {
		if streamMsg.BifrostError != nil {
			t.Fatalf("Error in first stream: %v", streamMsg.BifrostError)
		}
		if streamMsg.BifrostImageGenerationStreamResponse != nil {
			responses1 = append(responses1, *streamMsg.BifrostImageGenerationStreamResponse)
		}
	}
	duration1 := time.Since(start1)

	if len(responses1) == 0 {
		t.Fatal("First streaming request returned no responses")
	}

	t.Logf("First streaming request completed in %v with %d chunks", duration1, len(responses1))

	// Wait for cache to be written
	WaitForCache()

	t.Log("Making second identical streaming request (should be served from cache)...")

	// Make second identical streaming request
	start2 := time.Now()
	stream2, err2 := setup.Client.ImageGenerationStreamRequest(ctx, testRequest)
	if err2 != nil {
		t.Fatalf("Second streaming request failed: %v", err2)
	}

	var responses2 []schemas.BifrostImageGenerationStreamResponse
	for streamMsg := range stream2 {
		if streamMsg.BifrostError != nil {
			t.Fatalf("Error in second stream: %v", streamMsg.BifrostError)
		}
		if streamMsg.BifrostImageGenerationStreamResponse != nil {
			responses2 = append(responses2, *streamMsg.BifrostImageGenerationStreamResponse)
		}
	}
	duration2 := time.Since(start2)

	if len(responses2) == 0 {
		t.Fatal("Second streaming request returned no responses")
	}

	t.Logf("Second streaming request completed in %v with %d chunks", duration2, len(responses2))

	// Validate that both streams have the same number of chunks
	if len(responses1) != len(responses2) {
		t.Errorf("Stream chunk count mismatch: original=%d, cached=%d", len(responses1), len(responses2))
	}

	// Validate that the second stream was cached
	// Cache debug info is only on the last chunk for streaming responses
	cached := false
	if len(responses2) > 0 {
		lastResponse := responses2[len(responses2)-1]
		if lastResponse.ExtraFields.CacheDebug != nil && lastResponse.ExtraFields.CacheDebug.CacheHit {
			cached = true
			hitType := "unknown"
			cacheID := "unknown"
			if lastResponse.ExtraFields.CacheDebug.HitType != nil {
				hitType = *lastResponse.ExtraFields.CacheDebug.HitType
			}
			if lastResponse.ExtraFields.CacheDebug.CacheID != nil {
				cacheID = *lastResponse.ExtraFields.CacheDebug.CacheID
			}
			t.Logf("✅ Cache hit confirmed on last chunk: HitType=%s, CacheID=%s", hitType, cacheID)
		} else {
			// Check all chunks for debugging
			for i, response := range responses2 {
				if response.ExtraFields.CacheDebug != nil {
					t.Logf("Chunk %d: CacheDebug present, CacheHit=%v", i, response.ExtraFields.CacheDebug.CacheHit)
				} else {
					t.Logf("Chunk %d: No CacheDebug info", i)
				}
			}
		}
	}

	if !cached {
		t.Fatal("Second streaming request was not served from cache (CacheDebug not found on last chunk)")
	}

	// Performance comparison
	t.Logf("Streaming Performance Summary:")
	t.Logf("First request (OpenAI):  %v", duration1)
	t.Logf("Second request (Cache):  %v", duration2)

	if duration2 < duration1 {
		speedup := float64(duration1) / float64(duration2)
		t.Logf("Streaming cache speedup: %.2fx faster", speedup)
	} else {
		speedup := float64(duration1) / float64(duration2)
		t.Logf("Streaming cache was slower than original: speedup=%.2fx (this can happen due to system load)", speedup)
		// Only fail if cache is extremely slow (10x+ slower), indicating a real problem
		if duration2 > duration1*10 {
			t.Errorf("Streaming cache is extremely slow compared to original: cache=%v, original=%v (cache may not be working)", duration2, duration1)
		}
	}

	t.Log("✅ Image generation streaming cache test completed successfully!")
}
