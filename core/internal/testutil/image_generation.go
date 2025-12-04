package testutil

import (
	"context"
	"os"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunImageGenerationTest executes the end-to-end image generation test (non-streaming)
func RunImageGenerationTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if testConfig.ImageGenerationModel == "" {
		t.Logf("Image generation not configured for provider %s", testConfig.Provider)
		return
	}

	t.Run("ImageGeneration", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		// Test basic image generation
		request := &schemas.BifrostImageGenerationRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ImageGenerationModel,
			Input: &schemas.ImageGenerationInput{
				Prompt: "A serene Japanese garden with cherry blossoms in spring",
			},
			Params: &schemas.ImageGenerationParameters{
				Size:           bifrost.Ptr("1024x1024"),
				Quality:        bifrost.Ptr("standard"),
				ResponseFormat: bifrost.Ptr("b64_json"),
				N:              bifrost.Ptr(1),
			},
			Fallbacks: testConfig.ImageGenerationFallbacks,
		}

		response, bifrostErr := client.ImageGenerationRequest(ctx, request)
		if bifrostErr != nil {
			t.Fatalf("❌ Image generation failed: %v", GetErrorMessage(bifrostErr))
		}

		// Validate response
		if response == nil {
			t.Fatal("❌ Image generation returned nil response")
		}

		if len(response.Data) == 0 {
			t.Fatal("❌ Image generation returned no image data")
		}

		// Validate first image
		imageData := response.Data[0]
		if imageData.B64JSON == "" && imageData.URL == "" {
			t.Fatal("❌ Image data missing both b64_json and URL")
		}

		// Validate base64 if present
		if imageData.B64JSON != "" {
			if len(imageData.B64JSON) < 100 {
				t.Errorf("❌ Base64 image data too short: %d bytes", len(imageData.B64JSON))
			}
		}

		// Validate usage if present
		if response.Usage != nil {
			if response.Usage.TotalTokens == 0 {
				t.Logf("⚠️  Usage total_tokens is 0 (may be provider-specific)")
			}
		}

		// Validate extra fields
		if response.ExtraFields.Provider == "" {
			t.Error("❌ ExtraFields.Provider is empty")
		}

		if response.ExtraFields.ModelRequested == "" {
			t.Error("❌ ExtraFields.ModelRequested is empty")
		}

		t.Logf("✅ Image generation successful: ID=%s, Provider=%s, Model=%s, Images=%d",
			response.ID, response.ExtraFields.Provider, response.ExtraFields.ModelRequested, len(response.Data))
	})
}
