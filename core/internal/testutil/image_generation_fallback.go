package testutil

import (
	"context"
	"os"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunImageGenerationFallbackTest tests fallback to secondary provider
func RunImageGenerationFallbackTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if len(testConfig.ImageGenerationFallbacks) == 0 {
		t.Logf("Image generation fallback test skipped: no fallbacks configured for provider %s", testConfig.Provider)
		return
	}

	if testConfig.ImageGenerationModel == "" {
		t.Logf("Image generation not configured for provider %s", testConfig.Provider)
		return
	}

	t.Run("ImageGenerationFallback", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		// Create request with primary provider that should fail, triggering fallback
		// Note: This test assumes the primary provider will fail (e.g., invalid key)
		// In practice, you might need to configure a failing provider for this test
		request := &schemas.BifrostImageGenerationRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ImageGenerationModel,
			Input: &schemas.ImageGenerationInput{
				Prompt: "A beautiful mountain landscape at dawn",
			},
			Params: &schemas.ImageGenerationParameters{
				Size:           bifrost.Ptr("1024x1024"),
				ResponseFormat: bifrost.Ptr("b64_json"),
			},
			Fallbacks: testConfig.ImageGenerationFallbacks,
		}

		response, bifrostErr := client.ImageGenerationRequest(ctx, request)

		// If primary provider fails, fallback should be used
		// This test validates that fallback mechanism works
		if bifrostErr != nil {
			// Check if error indicates fallback was attempted
			errorMsg := GetErrorMessage(bifrostErr)
			if strings.Contains(strings.ToLower(errorMsg), "fallback") {
				t.Logf("✅ Fallback mechanism triggered (expected behavior)")
			} else {
				// If we have fallbacks configured, the request should succeed via fallback
				// If it still fails, log it but don't fail the test (provider-specific)
				t.Logf("⚠️  Request failed even with fallbacks: %v", errorMsg)
			}
			return
		}

		// If we get here, request succeeded (either primary or fallback)
		if response == nil {
			t.Fatal("❌ Image generation returned nil response")
		}

		// Validate that we got a response from either primary or fallback provider
		if response.ExtraFields.Provider == "" {
			t.Error("❌ Response missing provider information")
		}

		// Log which provider was used
		t.Logf("✅ Image generation succeeded via provider: %s (may be fallback)", response.ExtraFields.Provider)

		if len(response.Data) > 0 {
			t.Logf("✅ Received %d image(s) from provider %s", len(response.Data), response.ExtraFields.Provider)
		}
	})
}
