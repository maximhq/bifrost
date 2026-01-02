package testutil

import (
	"context"
	"os"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunImageGenerationErrorTest tests error handling scenarios
func RunImageGenerationErrorTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if testConfig.ImageGenerationModel == "" {
		t.Logf("Image generation error test skipped: not configured for provider %s", testConfig.Provider)
		return
	}

	t.Run("ImageGenerationErrors", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		// Test 1: Empty prompt (should fail)
		t.Run("EmptyPrompt", func(t *testing.T) {
			request := &schemas.BifrostImageGenerationRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.ImageGenerationModel,
				Input: &schemas.ImageGenerationInput{
					Prompt: "",
				},
			}

			response, bifrostErr := client.ImageGenerationRequest(ctx, request)
			if bifrostErr == nil {
				t.Error("❌ Empty prompt should return an error")
			} else {
				errorMsg := GetErrorMessage(bifrostErr)
				if strings.Contains(strings.ToLower(errorMsg), "prompt") ||
					strings.Contains(strings.ToLower(errorMsg), "required") ||
					strings.Contains(strings.ToLower(errorMsg), "empty") {
					t.Logf("✅ Empty prompt correctly rejected: %s", errorMsg)
				} else {
					t.Logf("⚠️  Empty prompt rejected but with unexpected error: %s", errorMsg)
				}
			}
			if response != nil {
				t.Error("❌ Empty prompt should not return a response")
			}
		})

		// Test 2: Invalid size (should fail or use default)
		t.Run("InvalidSize", func(t *testing.T) {
			request := &schemas.BifrostImageGenerationRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.ImageGenerationModel,
				Input: &schemas.ImageGenerationInput{
					Prompt: "A test image",
				},
				Params: &schemas.ImageGenerationParameters{
					Size: bifrost.Ptr("9999x9999"), // Invalid size
				},
			}

			response, bifrostErr := client.ImageGenerationRequest(ctx, request)
			if bifrostErr != nil {
				errorMsg := GetErrorMessage(bifrostErr)
				if strings.Contains(strings.ToLower(errorMsg), "size") ||
					strings.Contains(strings.ToLower(errorMsg), "invalid") {
					t.Logf("✅ Invalid size correctly rejected: %s", errorMsg)
				} else {
					t.Logf("⚠️  Invalid size rejected but with unexpected error: %s", errorMsg)
				}
			} else {
				// Some providers may accept and use default size
				t.Logf("⚠️  Invalid size was accepted (provider may use default)")
				if response != nil && len(response.Data) > 0 {
					t.Logf("✅ Request succeeded with default size")
				}
			}
		})

		// Test 3: Invalid n parameter (too many images)
		t.Run("InvalidN", func(t *testing.T) {
			request := &schemas.BifrostImageGenerationRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.ImageGenerationModel,
				Input: &schemas.ImageGenerationInput{
					Prompt: "A test image",
				},
				Params: &schemas.ImageGenerationParameters{
					N: bifrost.Ptr(20), // Too many (max is usually 10)
				},
			}

			response, bifrostErr := client.ImageGenerationRequest(ctx, request)
			if bifrostErr != nil {
				errorMsg := GetErrorMessage(bifrostErr)
				if strings.Contains(strings.ToLower(errorMsg), "n") ||
					strings.Contains(strings.ToLower(errorMsg), "invalid") ||
					strings.Contains(strings.ToLower(errorMsg), "maximum") {
					t.Logf("✅ Invalid n parameter correctly rejected: %s", errorMsg)
				} else {
					t.Logf("⚠️  Invalid n rejected but with unexpected error: %s", errorMsg)
				}
			} else {
				// Some providers may cap it
				t.Logf("⚠️  Invalid n was accepted (provider may cap to max)")
				if response != nil {
					actualN := len(response.Data)
					if actualN <= 10 {
						t.Logf("✅ Provider capped n to %d (expected)", actualN)
					}
				}
			}
		})

		// Test 4: Very long prompt (may hit rate limits or content policy)
		t.Run("VeryLongPrompt", func(t *testing.T) {
			longPrompt := strings.Repeat("A beautiful landscape with mountains and rivers. ", 100)
			request := &schemas.BifrostImageGenerationRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.ImageGenerationModel,
				Input: &schemas.ImageGenerationInput{
					Prompt: longPrompt,
				},
			}

			response, bifrostErr := client.ImageGenerationRequest(ctx, request)
			if bifrostErr != nil {
				errorMsg := GetErrorMessage(bifrostErr)
				if strings.Contains(strings.ToLower(errorMsg), "length") ||
					strings.Contains(strings.ToLower(errorMsg), "too long") ||
					strings.Contains(strings.ToLower(errorMsg), "limit") {
					t.Logf("✅ Very long prompt correctly rejected: %s", errorMsg)
				} else if strings.Contains(strings.ToLower(errorMsg), "rate limit") ||
					strings.Contains(strings.ToLower(errorMsg), "quota") {
					t.Logf("⚠️  Rate limit hit (expected for long prompts): %s", errorMsg)
				} else {
					t.Logf("⚠️  Long prompt rejected with unexpected error: %s", errorMsg)
				}
			} else {
				// Some providers may accept long prompts
				t.Logf("✅ Very long prompt was accepted")
				if response != nil && len(response.Data) > 0 {
					t.Logf("✅ Request succeeded with long prompt")
				}
			}
		})

		t.Logf("✅ Error handling tests completed")
	})
}
