package testutil

import (
	"context"
	"os"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunImageGenerationCacheTest tests cache hit/miss scenarios
func RunImageGenerationCacheTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if testConfig.ImageGenerationModel == "" {
		t.Logf("Image generation cache test skipped: not configured for provider %s", testConfig.Provider)
		return
	}

	t.Run("ImageGenerationCache", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		// Use a unique prompt for cache testing
		cacheTestPrompt := "A unique test image for cache validation - " + time.Now().Format("20060102150405")

		request := &schemas.BifrostImageGenerationRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ImageGenerationModel,
			Input: &schemas.ImageGenerationInput{
				Prompt: cacheTestPrompt,
			},
			Params: &schemas.ImageGenerationParameters{
				Size:           bifrost.Ptr("1024x1024"),
				ResponseFormat: bifrost.Ptr("b64_json"),
			},
		}

		// First request - should be a cache miss
		start1 := time.Now()
		response1, err1 := client.ImageGenerationRequest(ctx, request)
		duration1 := time.Since(start1)

		if err1 != nil {
			t.Fatalf("❌ First image generation request failed: %v", GetErrorMessage(err1))
		}

		if response1 == nil || len(response1.Data) == 0 {
			t.Fatal("❌ First request returned no image data")
		}

		// Check cache debug info if available
		cacheHit1 := false
		if response1.ExtraFields.CacheDebug != nil {
			cacheHit1 = response1.ExtraFields.CacheDebug.CacheHit
		}

		if cacheHit1 {
			t.Logf("⚠️  First request was a cache hit (unexpected, but may be valid)")
		} else {
			t.Logf("✅ First request was a cache miss (expected)")
		}

		// Second request with same prompt - should be a cache hit
		start2 := time.Now()
		response2, err2 := client.ImageGenerationRequest(ctx, request)
		duration2 := time.Since(start2)

		if err2 != nil {
			t.Fatalf("❌ Second image generation request failed: %v", GetErrorMessage(err2))
		}

		if response2 == nil || len(response2.Data) == 0 {
			t.Fatal("❌ Second request returned no image data")
		}

		// Check cache debug info
		cacheHit2 := false
		if response2.ExtraFields.CacheDebug != nil {
			cacheHit2 = response2.ExtraFields.CacheDebug.CacheHit
		}

		if cacheHit2 {
			t.Logf("✅ Second request was a cache hit (expected)")

			// Cache hit should be faster
			if duration2 < duration1 {
				t.Logf("✅ Cache hit was faster: %v vs %v", duration2, duration1)
			} else {
				t.Logf("⚠️  Cache hit was not faster: %v vs %v (may be due to network variance)", duration2, duration1)
			}

			// Validate cached response matches original
			if len(response1.Data) == len(response2.Data) {
				// Compare image data (should be identical for cache hit)
				if response1.Data[0].B64JSON != "" && response2.Data[0].B64JSON != "" {
					if response1.Data[0].B64JSON == response2.Data[0].B64JSON {
						t.Logf("✅ Cached image data matches original")
					} else {
						t.Errorf("❌ Cached image data does not match original")
					}
				}
			}
		} else {
			t.Logf("⚠️  Second request was a cache miss (cache may not be enabled or TTL expired)")
		}

		// Test with different prompt - should be cache miss
		request2 := &schemas.BifrostImageGenerationRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ImageGenerationModel,
			Input: &schemas.ImageGenerationInput{
				Prompt: "A different prompt for cache miss test",
			},
			Params: &schemas.ImageGenerationParameters{
				Size:           bifrost.Ptr("1024x1024"),
				ResponseFormat: bifrost.Ptr("b64_json"),
			},
		}

		response3, err3 := client.ImageGenerationRequest(ctx, request2)
		if err3 != nil {
			t.Fatalf("❌ Third image generation request failed: %v", GetErrorMessage(err3))
		}

		cacheHit3 := false
		if response3.ExtraFields.CacheDebug != nil {
			cacheHit3 = response3.ExtraFields.CacheDebug.CacheHit
		}

		if cacheHit3 {
			t.Logf("⚠️  Different prompt was a cache hit (unexpected)")
		} else {
			t.Logf("✅ Different prompt was a cache miss (expected)")
		}

		t.Logf("✅ Cache test completed: First=%v, Second=%v, Different=%v", cacheHit1, cacheHit2, cacheHit3)
	})
}
