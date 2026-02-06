package llmtests

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"strings"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// createMaskImageForAzureOpenAI creates a PNG mask image with transparent background for Azure and OpenAI
// Creates a white rectangle in the center on transparent background (typical inpainting mask pattern)
// PNG format with alpha channel is required by Azure and OpenAI
func createMaskImageForAzureOpenAI(width, height int) ([]byte, error) {
	// Create an RGBA image with alpha channel support
	img := image.NewRGBA(image.Rect(0, 0, width, height))

	// Create a white rectangle in the center (typical mask pattern for inpainting)
	// White areas with full alpha indicate regions to edit
	// Transparent areas indicate regions to preserve
	centerX, centerY := width/2, height/2
	maskWidth, maskHeight := width/3, height/3

	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			// Check if pixel is within the mask rectangle
			if x >= centerX-maskWidth/2 && x < centerX+maskWidth/2 &&
				y >= centerY-maskHeight/2 && y < centerY+maskHeight/2 {
				// White with full alpha = edit area
				img.Set(x, y, color.RGBA{R: 255, G: 255, B: 255, A: 255})
			} else {
				// Transparent (alpha=0) = preserve area
				img.Set(x, y, color.RGBA{R: 0, G: 0, B: 0, A: 0})
			}
		}
	}

	// Encode as PNG to preserve alpha channel (required by Azure and OpenAI)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("failed to encode mask image: %w", err)
	}

	return buf.Bytes(), nil
}

// createSimpleMaskImage creates a simple JPEG mask image for testing (no transparency)
// Creates a white rectangle in the center on black background (typical inpainting mask pattern)
// JPEG format doesn't support transparency, so this works with providers that don't require alpha channel
func createSimpleMaskImage(width, height int) ([]byte, error) {
	// Create an RGB image (no alpha channel)
	img := image.NewRGBA(image.Rect(0, 0, width, height))

	// Create a white rectangle in the center (typical mask pattern for inpainting)
	// White areas indicate regions to edit, black areas are preserved
	centerX, centerY := width/2, height/2
	maskWidth, maskHeight := width/3, height/3

	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			// Check if pixel is within the mask rectangle
			if x >= centerX-maskWidth/2 && x < centerX+maskWidth/2 &&
				y >= centerY-maskHeight/2 && y < centerY+maskHeight/2 {
				img.Set(x, y, color.RGBA{R: 255, G: 255, B: 255, A: 255}) // White (edit area)
			} else {
				img.Set(x, y, color.RGBA{R: 0, G: 0, B: 0, A: 255}) // Black (preserve area)
			}
		}
	}

	// Encode as JPEG (no transparency support, so it works with all providers)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		return nil, fmt.Errorf("failed to encode mask image: %w", err)
	}

	return buf.Bytes(), nil
}

// createMaskImage creates a mask image based on the provider requirements
// Azure and OpenAI require PNG with transparent background (alpha channel)
// Other providers use JPEG with opaque background
func createMaskImage(provider schemas.ModelProvider, width, height int) ([]byte, error) {
	if provider == schemas.Azure || provider == schemas.OpenAI {
		return createMaskImageForAzureOpenAI(width, height)
	}
	return createSimpleMaskImage(width, height)
}

// convertImageToPNG converts any image format to PNG (supports transparency)
// This ensures compatibility with providers that require PNG format
// Returns the converted image bytes and its dimensions
func convertImageToPNG(imageBytes []byte) ([]byte, int, int, error) {
	// Decode the image (supports PNG, JPEG, GIF, etc.)
	img, format, err := image.Decode(bytes.NewReader(imageBytes))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("failed to decode image: %w", err)
	}

	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	// If it's already PNG, return as-is
	if format == "png" {
		return imageBytes, width, height, nil
	}

	// Convert to RGBA to preserve color information
	rgbaImg := image.NewRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			rgbaImg.Set(x, y, img.At(x, y))
		}
	}

	// Encode as PNG (supports transparency)
	var buf bytes.Buffer
	if err := png.Encode(&buf, rgbaImg); err != nil {
		return nil, 0, 0, fmt.Errorf("failed to encode image as PNG: %w", err)
	}

	return buf.Bytes(), width, height, nil
}

// convertImageToJPEG converts any image format to JPEG (no transparency)
// This ensures compatibility with providers that don't support transparency
// Returns the converted image bytes and its dimensions
func convertImageToJPEG(imageBytes []byte) ([]byte, int, int, error) {
	// Decode the image (supports PNG, JPEG, GIF, etc.)
	img, format, err := image.Decode(bytes.NewReader(imageBytes))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("failed to decode image: %w", err)
	}

	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	// If it's already JPEG, return as-is
	if format == "jpeg" || format == "jpg" {
		return imageBytes, width, height, nil
	}

	// Convert to RGBA to ensure no transparency
	rgbaImg := image.NewRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			rgbaImg.Set(x, y, img.At(x, y))
		}
	}

	// Encode as JPEG (no transparency support)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, rgbaImg, &jpeg.Options{Quality: 95}); err != nil {
		return nil, 0, 0, fmt.Errorf("failed to encode image as JPEG: %w", err)
	}

	return buf.Bytes(), width, height, nil
}

// convertImageForProvider converts an image to the appropriate format based on provider requirements
// OpenAI requires PNG format, other providers use JPEG
// Returns the converted image bytes and its dimensions
func convertImageForProvider(provider schemas.ModelProvider, imageBytes []byte) ([]byte, int, int, error) {
	if provider == schemas.OpenAI {
		return convertImageToPNG(imageBytes)
	}
	return convertImageToJPEG(imageBytes)
}

// decodeBase64ImageToBytes converts a base64 data URL string to []byte
// Handles both "data:image/png;base64,<data>" and plain base64 strings
func decodeBase64ImageToBytes(base64Str string) ([]byte, error) {
	// Remove data URL prefix if present
	if strings.HasPrefix(base64Str, "data:") {
		parts := strings.SplitN(base64Str, ",", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid data URL format")
		}
		base64Str = parts[1]
	}

	// Decode base64 string
	decoded, err := base64.StdEncoding.DecodeString(base64Str)
	if err != nil {
		return nil, fmt.Errorf("failed to decode base64: %w", err)
	}

	if len(decoded) == 0 {
		return nil, fmt.Errorf("decoded image data is empty")
	}

	return decoded, nil
}

// RunImageEditTest executes the end-to-end image edit test (non-streaming)
func RunImageEditTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if testConfig.ImageEditModel == "" {
		t.Logf("Image edit not configured for provider %s", testConfig.Provider)
		return
	}

	if !testConfig.Scenarios.ImageEdit {
		t.Logf("Image edit not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("ImageEdit", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		retryConfig := GetTestRetryConfigForScenario("ImageEdit", testConfig)
		retryContext := TestRetryContext{
			ScenarioName:     "ImageEdit",
			ExpectedBehavior: map[string]interface{}{},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
				"model":    testConfig.ImageEditModel,
			},
		}

		expectations := GetExpectationsForScenario("ImageEdit", testConfig, map[string]interface{}{
			"min_images":    1,
			"expected_size": "1024x1024",
		})

		imageEditRetryConfig := ImageGenerationRetryConfig{
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

		// Convert input image to JPEG (no transparency) to avoid provider compatibility issues
		imageBytes, imgWidth, imgHeight, err := convertImageToJPEG(imageBytes)
		if err != nil {
			t.Fatalf("Failed to convert image to JPEG: %v", err)
		}

		// Create mask image based on provider requirements
		// Azure and OpenAI require PNG with transparent background, others use JPEG
		maskBytes, err := createMaskImage(testConfig.Provider, imgWidth, imgHeight)
		if err != nil {
			t.Fatalf("Failed to create mask image: %v", err)
		}

		// Test basic image edit (inpainting)
		imageEditOperation := func() (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
			request := &schemas.BifrostImageEditRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.ImageEditModel,
				Input: &schemas.ImageEditInput{
					Images: []schemas.ImageInput{
						{
							Image: imageBytes,
						},
					},
					Prompt: "Add a beautiful sunset in the background",
				},
				Params: &schemas.ImageEditParameters{
					Size: bifrost.Ptr("1024x1024"),
					N:    bifrost.Ptr(1),
					Type: bifrost.Ptr("inpainting"),
					Mask: maskBytes,
				},
				Fallbacks: testConfig.ImageEditFallbacks,
			}

			response, err := client.ImageEditRequest(schemas.NewBifrostContext(ctx, schemas.NoDeadline), request)
			if err != nil {
				return nil, err
			}
			if response != nil {
				return response, nil
			}
			return nil, &schemas.BifrostError{
				IsBifrostError: true,
				Error: &schemas.ErrorField{
					Message: "No image edit response returned",
				},
			}
		}

		imageEditResponse, imageEditError := WithImageGenerationRetry(t, imageEditRetryConfig, retryContext, expectations, "ImageEdit", imageEditOperation)

		if imageEditError != nil {
			t.Fatalf("❌ Image edit failed: %v", GetErrorMessage(imageEditError))
		}

		// Validate response
		if imageEditResponse == nil {
			t.Fatal("❌ Image edit returned nil response")
		}

		if len(imageEditResponse.Data) == 0 {
			t.Fatal("❌ Image edit returned no image data")
		}

		// Validate first image
		imageData := imageEditResponse.Data[0]
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
		if imageEditResponse.Usage != nil {
			if imageEditResponse.Usage.TotalTokens == 0 {
				t.Logf("⚠️  Usage total_tokens is 0 (may be provider-specific)")
			}
		}

		// Validate extra fields
		if imageEditResponse.ExtraFields.Provider == "" {
			t.Error("❌ ExtraFields.Provider is empty")
		}

		if imageEditResponse.ExtraFields.ModelRequested == "" {
			t.Error("❌ ExtraFields.ModelRequested is empty")
		}

		// Validate RequestType is ImageEditRequest
		if imageEditResponse.ExtraFields.RequestType != schemas.ImageEditRequest {
			t.Errorf("❌ ExtraFields.RequestType mismatch: got %s, expected %s", imageEditResponse.ExtraFields.RequestType, schemas.ImageEditRequest)
		}

		t.Logf("✅ Image edit successful: ID=%s, Provider=%s, Model=%s, Images=%d",
			imageEditResponse.ID, imageEditResponse.ExtraFields.Provider, imageEditResponse.ExtraFields.ModelRequested, len(imageEditResponse.Data))
	})
}

// RunImageEditStreamTest executes the end-to-end streaming image edit test
func RunImageEditStreamTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.ImageEditStream {
		t.Logf("Image edit streaming not supported for provider %s", testConfig.Provider)
		return
	}

	if testConfig.ImageEditModel == "" {
		t.Logf("Image edit streaming not configured for provider %s", testConfig.Provider)
		return
	}

	t.Run("ImageEditStream", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		retryConfig := GetTestRetryConfigForScenario("ImageEditStream", testConfig)
		retryContext := TestRetryContext{
			ScenarioName: "ImageEditStream",
			ExpectedBehavior: map[string]interface{}{
				"should_generate_images": true,
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
				"model":    testConfig.ImageEditModel,
			},
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

		// Convert input image to JPEG (no transparency) to avoid provider compatibility issues
		imageBytes, imgWidth, imgHeight, err := convertImageToJPEG(imageBytes)
		if err != nil {
			t.Fatalf("Failed to convert image to JPEG: %v", err)
		}

		// Create mask image based on provider requirements
		// Azure and OpenAI require PNG with transparent background, others use JPEG
		maskBytes, err := createMaskImage(testConfig.Provider, imgWidth, imgHeight)
		if err != nil {
			t.Fatalf("Failed to create mask image: %v", err)
		}

		request := &schemas.BifrostImageEditRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ImageEditModel,
			Input: &schemas.ImageEditInput{
				Images: []schemas.ImageInput{
					{
						Image: imageBytes,
					},
				},
				Prompt: "Add a futuristic cityscape in the background",
			},
			Params: &schemas.ImageEditParameters{
				Size:    bifrost.Ptr("1024x1024"),
				Quality: bifrost.Ptr("low"),
				Type:    bifrost.Ptr("inpainting"),
				Mask:    maskBytes,
			},
			Fallbacks: testConfig.ImageEditFallbacks,
		}
		streamCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()

		validationResult := WithImageGenerationStreamRetry(
			t,
			retryConfig,
			retryContext,
			func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
				return client.ImageEditStreamRequest(schemas.NewBifrostContext(streamCtx, schemas.NoDeadline), request)
			},
			func(responseChannel chan *schemas.BifrostStreamChunk) ImageGenerationStreamValidationResult {
				// Validate stream content
				var receivedData bool
				var streamErrors []string
				var validationErrors []string
				hasCompleted := false

				for {
					select {
					case response, ok := <-responseChannel:
						if !ok {
							goto streamComplete
						}

						if response == nil {
							streamErrors = append(streamErrors, "Received nil stream response")
							continue
						}

						if response.BifrostError != nil {
							streamErrors = append(streamErrors, fmt.Sprintf("Error in stream: %s", GetErrorMessage(response.BifrostError)))
							continue
						}

						if response.BifrostImageGenerationStreamResponse != nil {
							receivedData = true
							imgResp := response.BifrostImageGenerationStreamResponse

							// Check for completion event (can be ImageGenerationEventTypeCompleted or ImageEditEventTypeCompleted)
							if imgResp.Type == schemas.ImageGenerationEventTypeCompleted || imgResp.Type == schemas.ImageEditEventTypeCompleted {
								hasCompleted = true
								// Validate that completed images have actual data
								if imgResp.URL == "" && imgResp.B64JSON == "" {
									validationErrors = append(validationErrors, "Completion chunk received but image has no URL or B64JSON data")
								}
							}
						}
					case <-streamCtx.Done():
						validationErrors = append(validationErrors, "Stream validation timed out")
						drainCtx, drainCancel := context.WithTimeout(context.Background(), 5*time.Second)
						go func() {
							defer drainCancel()
							for {
								select {
								case _, ok := <-responseChannel:
									if !ok {
										return
									}
								case <-drainCtx.Done():
									return
								}
							}
						}()
						goto streamComplete
					}
				}
			streamComplete:

				// Stream errors should cause the test to fail - convert them to validation errors
				if len(streamErrors) > 0 {
					validationErrors = append(validationErrors, fmt.Sprintf("Stream errors encountered: %s", strings.Join(streamErrors, "; ")))
				}

				// Test passes only if: data received, completion received, and no errors (including stream errors)
				passed := receivedData && hasCompleted && len(validationErrors) == 0
				if !receivedData {
					validationErrors = append(validationErrors, "No stream data received")
				}
				if !hasCompleted {
					validationErrors = append(validationErrors, "No completion chunk received")
				}

				return ImageGenerationStreamValidationResult{
					Passed:       passed,
					Errors:       validationErrors,
					ReceivedData: receivedData,
					StreamErrors: streamErrors,
				}
			},
		)

		if !validationResult.Passed {
			allErrors := append(validationResult.Errors, validationResult.StreamErrors...)
			t.Fatalf("❌ Image edit stream validation failed: %s", strings.Join(allErrors, "; "))
		}

		if !validationResult.ReceivedData {
			t.Fatal("❌ No stream data received")
		}

		t.Logf("✅ Image edit stream successful: ReceivedData=%v, Errors=%d, StreamErrors=%d",
			validationResult.ReceivedData, len(validationResult.Errors), len(validationResult.StreamErrors))
	})
}
