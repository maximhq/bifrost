package testutil

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"strings"
	"testing"
	"time"

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
					Size: bifrost.Ptr("1024x1024"),
					N:    bifrost.Ptr(1),
				},
				Fallbacks: testConfig.ImageGenerationFallbacks,
			}

			response, err := client.ImageGenerationRequest(schemas.NewBifrostContext(ctx, schemas.NoDeadline), request)
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

			// Validate dimensions are 1024x1024 as requested
			expectedWidth, expectedHeight := 1024, 1024
			if config.Width != expectedWidth || config.Height != expectedHeight {
				t.Errorf("❌ Image dimensions mismatch: got %dx%d, expected %dx%d", config.Width, config.Height, expectedWidth, expectedHeight)
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

// RunImageGenerationStreamTest executes the end-to-end streaming image generation test
func RunImageGenerationStreamTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.ImageGenerationStream {
		t.Logf("Image generation streaming not supported for provider %s", testConfig.Provider)
		return
	}

	if testConfig.ImageGenerationModel == "" {
		t.Logf("Image generation streaming not configured for provider %s", testConfig.Provider)
		return
	}

	t.Run("ImageGenerationStream", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		retryConfig := GetTestRetryConfigForScenario("ImageGenerationStream", testConfig)
		retryContext := TestRetryContext{
			ScenarioName: "ImageGenerationStream",
			ExpectedBehavior: map[string]interface{}{
				"should_generate_images": true,
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
				"model":    testConfig.ImageGenerationModel,
			},
		}

		request := &schemas.BifrostImageGenerationRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ImageGenerationModel,
			Input: &schemas.ImageGenerationInput{
				Prompt: "A futuristic cityscape at sunset with flying cars",
			},
			Params: &schemas.ImageGenerationParameters{
				Size:    bifrost.Ptr("1024x1024"),
				Quality: bifrost.Ptr("low"),
			},
			Fallbacks: testConfig.ImageGenerationFallbacks,
		}
		streamCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()

		validationResult := WithImageGenerationStreamRetry(
			t,
			retryConfig,
			retryContext,
			func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
				return client.ImageGenerationStreamRequest(schemas.NewBifrostContext(streamCtx, schemas.NoDeadline), request)
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

							if imgResp.Type == schemas.ImageGenerationEventTypeCompleted {
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
			t.Fatalf("❌ Image generation stream validation failed: %s", strings.Join(allErrors, "; "))
		}

		if !validationResult.ReceivedData {
			t.Fatal("❌ No stream data received")
		}

		t.Logf("✅ Image generation stream successful: ReceivedData=%v, Errors=%d, StreamErrors=%d",
			validationResult.ReceivedData, len(validationResult.Errors), len(validationResult.StreamErrors))
	})
}
