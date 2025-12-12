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

		retryConfig := GetTestRetryConfigForScenario("ImageGeneration", testConfig)
		retryContext := TestRetryContext{
			ScenarioName:     "ImageGeneration",
			ExpectedBehavior: map[string]interface{}{},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
				"model":    testConfig.ImageGenerationModel,
			},
		}

		expectations := GetExpectationsForScenario("ImageGeneration", testConfig, map[string]interface{}{
			"min_images":    1,
			"expected_size": "1024x1024",
		})

		imageGenerationRetryConfig := ImageGenerationRetryConfig{
			MaxAttempts: retryConfig.MaxAttempts,
			BaseDelay:   retryConfig.BaseDelay,
			MaxDelay:    retryConfig.MaxDelay,
			Conditions:  []ImageGenerationRetryCondition{},
			OnRetry:     retryConfig.OnRetry,
			OnFinalFail: retryConfig.OnFinalFail,
		}
		// Test basic image generation
		imageGenerationOperation := func() (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
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

			response, err := client.ImageGenerationRequest(ctx, request)
			if err != nil {
				return nil, err
			}
			if response != nil {
				return response, nil
			}
			return nil, &schemas.BifrostError{
				IsBifrostError: true,
				Error: &schemas.ErrorField{
					Message: "No image generation response returned",
				},
			}
		}

		imageGenerationResponse, imageGenerationError := WithImageGenerationRetry(t, imageGenerationRetryConfig, retryContext, expectations, "ImageGeneration", imageGenerationOperation)

		if imageGenerationError != nil {
			t.Fatalf("❌ Image generation failed: %v", GetErrorMessage(imageGenerationError))
		}

		// Validate response
		if imageGenerationResponse == nil {
			t.Fatal("❌ Image generation returned nil response")
		}

		if len(imageGenerationResponse.Data) == 0 {
			t.Fatal("❌ Image generation returned no image data")
		}

		// Validate first image
		imageData := imageGenerationResponse.Data[0]
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
		if imageGenerationResponse.Usage != nil {
			if imageGenerationResponse.Usage.TotalTokens == 0 {
				t.Logf("⚠️  Usage total_tokens is 0 (may be provider-specific)")
			}
		}

		// Validate extra fields
		if imageGenerationResponse.ExtraFields.Provider == "" {
			t.Error("❌ ExtraFields.Provider is empty")
		}

		if imageGenerationResponse.ExtraFields.ModelRequested == "" {
			t.Error("❌ ExtraFields.ModelRequested is empty")
		}

		t.Logf("✅ Image generation successful: ID=%s, Provider=%s, Model=%s, Images=%d",
			imageGenerationResponse.ID, imageGenerationResponse.ExtraFields.Provider, imageGenerationResponse.ExtraFields.ModelRequested, len(imageGenerationResponse.Data))
	})
}
