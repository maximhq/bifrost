package llmtests

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	_ "image/png"
	"os"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunImageVariationTest executes the end-to-end image variation test (non-streaming)
func RunImageVariationTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if testConfig.ImageVariationModel == "" {
		t.Logf("Image variation not configured for provider %s", testConfig.Provider)
		return
	}

	if !testConfig.Scenarios.ImageVariation {
		t.Logf("Image variation not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("ImageVariation", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		retryConfig := GetTestRetryConfigForScenario("ImageVariation", testConfig)
		retryContext := TestRetryContext{
			ScenarioName:     "ImageVariation",
			ExpectedBehavior: map[string]interface{}{},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
				"model":    testConfig.ImageVariationModel,
			},
		}

		expectations := GetExpectationsForScenario("ImageVariation", testConfig, map[string]interface{}{
			"min_images":    1,
			"expected_size": "1024x1024",
		})

		imageVariationRetryConfig := ImageGenerationRetryConfig{
			MaxAttempts: retryConfig.MaxAttempts,
			BaseDelay:   retryConfig.BaseDelay,
			MaxDelay:    retryConfig.MaxDelay,
			Conditions:  []ImageGenerationRetryCondition{},
			OnRetry:     retryConfig.OnRetry,
			OnFinalFail: retryConfig.OnFinalFail,
		}

		// Load test image
		lionBase64, err := GetLionBase64Image()
		if err != nil {
			t.Fatalf("Failed to load test image: %v", err)
		}

		// Convert base64 to bytes
		imageBytes, err := decodeBase64ImageToBytes(lionBase64)
		if err != nil {
			t.Fatalf("Failed to decode image: %v", err)
		}

		// Convert input image based on provider requirements
		// OpenAI requires PNG format, other providers use JPEG
		imageBytes, _, _, err = convertImageForProvider(testConfig.Provider, imageBytes)
		if err != nil {
			t.Fatalf("Failed to convert image: %v", err)
		}

		// Test basic image variation
		imageVariationOperation := func() (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
			request := &schemas.BifrostImageVariationRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.ImageVariationModel,
				Input: &schemas.ImageVariationInput{
					Image: schemas.ImageInput{
						Image: imageBytes,
					},
				},
				Params: &schemas.ImageVariationParameters{
					Size: bifrost.Ptr("1024x1024"),
					N:    bifrost.Ptr(2), // Generate 2 variations
				},
				Fallbacks: testConfig.ImageVariationFallbacks,
			}

			response, err := client.ImageVariationRequest(schemas.NewBifrostContext(ctx, schemas.NoDeadline), request)
			if err != nil {
				return nil, err
			}
			if response != nil {
				return response, nil
			}
			return nil, &schemas.BifrostError{
				IsBifrostError: true,
				Error: &schemas.ErrorField{
					Message: "No image variation response returned",
				},
			}
		}

		imageVariationResponse, imageVariationError := WithImageGenerationRetry(t, imageVariationRetryConfig, retryContext, expectations, "ImageVariation", imageVariationOperation)

		if imageVariationError != nil {
			t.Fatalf("❌ Image variation failed: %v", GetErrorMessage(imageVariationError))
		}

		// Validate response
		if imageVariationResponse == nil {
			t.Fatal("❌ Image variation returned nil response")
		}

		if len(imageVariationResponse.Data) == 0 {
			t.Fatal("❌ Image variation returned no image data")
		}

		// Validate first image
		imageData := imageVariationResponse.Data[0]
		if imageData.B64JSON == "" && imageData.URL == "" {
			t.Fatal("❌ Image data missing both b64_json and URL")
		}

		// Validate base64 if present
		if imageData.B64JSON != "" {
			// Decode base64 image data
			decoded, err := base64.StdEncoding.DecodeString(imageData.B64JSON)
			if err != nil {
				t.Fatalf("❌ Failed to decode base64 image data: %v", err)
			}
			if len(decoded) == 0 {
				t.Fatalf("❌ Decoded image data is empty")
			}

			// Decode image config to validate dimensions
			reader := bytes.NewReader(decoded)
			config, format, err := image.DecodeConfig(reader)
			if err != nil {
				t.Fatalf("❌ Failed to decode image config: %v (format: %s)", err, format)
			}

			// Validate dimensions are reasonable (at least 256x256)
			if config.Width < 256 || config.Height < 256 {
				t.Errorf("❌ Image dimensions too small: got %dx%d, expected at least 256x256", config.Width, config.Height)
			}
		}

		// Validate usage if present
		if imageVariationResponse.Usage != nil {
			if imageVariationResponse.Usage.TotalTokens == 0 {
				t.Logf("⚠️  Usage total_tokens is 0 (may be provider-specific)")
			}
		}

		// Validate extra fields
		if imageVariationResponse.ExtraFields.Provider == "" {
			t.Error("❌ ExtraFields.Provider is empty")
		}

		if imageVariationResponse.ExtraFields.ModelRequested == "" {
			t.Error("❌ ExtraFields.ModelRequested is empty")
		}

		// Validate RequestType is ImageVariationRequest
		if imageVariationResponse.ExtraFields.RequestType != schemas.ImageVariationRequest {
			t.Errorf("❌ ExtraFields.RequestType mismatch: got %s, expected %s", imageVariationResponse.ExtraFields.RequestType, schemas.ImageVariationRequest)
		}

		t.Logf("✅ Image variation successful: ID=%s, Provider=%s, Model=%s, Images=%d",
			imageVariationResponse.ID, imageVariationResponse.ExtraFields.Provider, imageVariationResponse.ExtraFields.ModelRequested, len(imageVariationResponse.Data))
	})
}

// RunImageVariationStreamTest executes the end-to-end streaming image variation test
// Note: Currently, streaming image variation is not supported by any provider
func RunImageVariationStreamTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.ImageVariationStream {
		t.Logf("Image variation streaming not supported for provider %s", testConfig.Provider)
		return
	}

	if testConfig.ImageVariationModel == "" {
		t.Logf("Image variation streaming not configured for provider %s", testConfig.Provider)
		return
	}

	// Currently, no providers support streaming image variation
	// This test is a placeholder for future support
	t.Run("ImageVariationStream", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		t.Skip("Image variation streaming is not currently supported by any provider")
	})
}
