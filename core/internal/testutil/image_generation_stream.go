package testutil

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
)

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

		validationResult := WithImageGenerationStreamRetry(
			t,
			retryConfig,
			retryContext,
			func() (chan *schemas.BifrostStream, *schemas.BifrostError) {
				return client.ImageGenerationStreamRequest(ctx, request)
			},
			func(responseChannel chan *schemas.BifrostStream) ImageGenerationStreamValidationResult {
				// Validate stream content
				var receivedData bool
				var streamErrors []string
				var validationErrors []string
				hasCompleted := false

				streamCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
				defer cancel()

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

							if imgResp.Type == string(openai.ImageGenerationCompleted) {
								hasCompleted = true
							}
						}
					case <-streamCtx.Done():
						validationErrors = append(validationErrors, "Stream validation timed out")
						goto streamComplete
					}
				}
			streamComplete:

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
