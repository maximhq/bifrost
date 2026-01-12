package llmtests

import (
	"context"
	"os"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunMultipleImagesTest executes the multiple images test scenario
func RunMultipleImagesTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.MultipleImages {
		t.Logf("Multiple images not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("MultipleImages", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		// Load lion base64 image for comparison
		lionBase64, err := GetLionBase64Image()
		if err != nil {
			t.Fatalf("Failed to load lion base64 image: %v", err)
		}

		messages := []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentBlocks: []schemas.ChatContentBlock{
						{
							Type: schemas.ChatContentBlockTypeText,
							Text: bifrost.Ptr("Compare these two images - what are the similarities and differences? Both are animals, but what are the specific differences between them?"),
						},
						{
							Type: schemas.ChatContentBlockTypeImage,
							ImageURLStruct: &schemas.ChatInputImage{
								URL: TestImageURL, // Ant image
							},
						},
						{
							Type: schemas.ChatContentBlockTypeImage,
							ImageURLStruct: &schemas.ChatInputImage{
								URL: lionBase64, // Lion image
							},
						},
					},
				},
			},
		}

		request := &schemas.BifrostChatRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.VisionModel,
			Input:    messages,
			Params: &schemas.ChatParameters{
				MaxCompletionTokens: bifrost.Ptr(300),
			},
			Fallbacks: testConfig.Fallbacks,
		}

		// Use retry framework for multiple image processing (more complex, can be flaky)
		retryConfig := GetTestRetryConfigForScenario("MultipleImages", testConfig)
		retryContext := TestRetryContext{
			ScenarioName: "MultipleImages",
			ExpectedBehavior: map[string]interface{}{
				"should_compare_images":        true,
				"should_identify_similarities": true,
				"should_identify_differences":  true,
				"multiple_image_processing":    true,
			},
			TestMetadata: map[string]interface{}{
				"provider":          testConfig.Provider,
				"model":             testConfig.VisionModel,
				"image_count":       2,
				"mixed_formats":     true,                                                                                               // URL and base64
				"expected_keywords": []string{"different", "differences", "contrast", "unlike", "comparison", "compare", "both", "two"}, // 🎯 Comparison-specific terms
			},
		}
		chatRetryConfig := ChatRetryConfig{
			MaxAttempts: retryConfig.MaxAttempts,
			BaseDelay:   retryConfig.BaseDelay,
			MaxDelay:    retryConfig.MaxDelay,
			Conditions:  []ChatRetryCondition{}, // Add specific chat retry conditions as needed
			OnRetry:     retryConfig.OnRetry,
			OnFinalFail: retryConfig.OnFinalFail,
		}

		// Enhanced validation for multiple image comparison (ant vs lion)
		expectations := VisionExpectations([]string{"ant", "lion"}) // Basic expectation - should identify both as animals with differences
		expectations = ModifyExpectationsForProvider(expectations, testConfig.Provider)
		expectations.ShouldNotContainWords = append(expectations.ShouldNotContainWords, []string{
			"only see one", "cannot compare", "missing image",
			"single image", "unable to view the second",
		}...) // Failure to process multiple images indicators

		response, bifrostError := WithChatTestRetry(t, chatRetryConfig, retryContext, expectations, "MultipleImages", func() (*schemas.BifrostChatResponse, *schemas.BifrostError) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			return client.ChatCompletionRequest(bfCtx, request)
		})

		// Validation now happens inside WithTestRetry - no need to check again
		if bifrostError != nil {
			t.Fatalf("❌ Multiple images request failed after retries: %v", GetErrorMessage(bifrostError))
		}

		content := GetChatContent(response)

		// Additional validation for ant vs lion comparison
		contentLower := strings.ToLower(content)
		foundAnimalRef := strings.Contains(contentLower, "ant") || strings.Contains(contentLower, "lion") ||
			strings.Contains(contentLower, "insect") || strings.Contains(contentLower, "cat") ||
			strings.Contains(contentLower, "animal")
		foundComparison := strings.Contains(contentLower, "different") || strings.Contains(contentLower, "compare") ||
			strings.Contains(contentLower, "contrast") || strings.Contains(contentLower, "versus")

		if foundAnimalRef && foundComparison {
			t.Logf("✅ Model successfully identified animals and made comparisons: %s", content)
		} else if foundAnimalRef {
			t.Logf("✅ Model identified animals but may not have made clear comparisons")
		} else {
			t.Logf("⚠️ Model may not have clearly identified the animals in the images")
		}

		// Check for substantial response indicating both images were processed
		if len(content) > 50 {
			t.Logf("✅ Generated substantial comparison response (%d chars)", len(content))
		} else {
			t.Logf("⚠️ Comparison response seems brief: %s", content)
		}

		t.Logf("✅ Multiple images comparison completed: %s", content)
	})
}
