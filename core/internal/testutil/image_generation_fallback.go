package testutil

import (
	"context"
	"os"
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

		// If request succeeded, check if fallback was used
		if bifrostErr == nil {
			if response == nil {
				t.Fatalf("❌ Image generation returned nil response")
			}

			// Check if response came from fallback provider
			usedFallback := false
			for _, fallback := range testConfig.ImageGenerationFallbacks {
				if response.ExtraFields.Provider == fallback.Provider {
					usedFallback = true
					break
				}
			}

			if !usedFallback && response.ExtraFields.Provider == testConfig.Provider {
				t.Logf("✅ Image generation succeeded via primary provider: %s", response.ExtraFields.Provider)
			} else if usedFallback {
				t.Logf("✅ Fallback mechanism worked - response from fallback provider: %s", response.ExtraFields.Provider)
			} else {
				t.Errorf("❌ Response provider %s doesn't match primary %s or any fallback providers", response.ExtraFields.Provider, testConfig.Provider)
			}

			if len(response.Data) > 0 {
				t.Logf("✅ Received %d image(s) from provider %s", len(response.Data), response.ExtraFields.Provider)
			}
			return
		}

		// Request failed - check if fallback was attempted
		if bifrostErr.ExtraFields.Provider != "" {
			// Check if error came from fallback attempt
			for _, fallback := range testConfig.ImageGenerationFallbacks {
				if bifrostErr.ExtraFields.Provider == fallback.Provider {
					t.Logf("✅ Fallback mechanism attempted - error from fallback provider: %s", bifrostErr.ExtraFields.Provider)
					return
				}
			}
			// Error from primary provider - fallback should have been attempted if primary truly failed
			if bifrostErr.ExtraFields.Provider == testConfig.Provider {
				t.Fatalf("❌ Primary provider failed but no fallback attempt detected. Error: %v", bifrostErr)
			}
		} else {
			if bifrostErr.Error != nil {
				t.Logf("⚠️  Request failed: %s (fallback detection inconclusive)", bifrostErr.Error.Message)
			}
		}
	})
}
