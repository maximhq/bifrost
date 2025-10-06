package scenarios

import (
	"context"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunImageBase64Test executes the image base64 test scenario using dual API testing framework
func RunImageBase64Test(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig config.ComprehensiveTestConfig) {
	if !testConfig.Scenarios.ImageBase64 {
		t.Logf("Image base64 not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("ImageBase64", func(t *testing.T) {
		// Load lion base64 image for testing
		lionBase64, err := GetLionBase64Image()
		if err != nil {
			t.Fatalf("Failed to load lion base64 image: %v", err)
		}

		// Create messages for both APIs using the isResponsesAPI flag
		chatMessages := []schemas.ChatMessage{
			CreateImageChatMessage("Describe this image briefly. What animal do you see?", lionBase64),
		}
		responsesMessages := []schemas.ResponsesMessage{
			CreateImageResponsesMessage("Describe this image briefly. What animal do you see?", lionBase64),
		}

		// Use retry framework for vision requests with base64 data
		retryConfig := GetTestRetryConfigForScenario("ImageBase64", testConfig)
		retryContext := TestRetryContext{
			ScenarioName: "ImageBase64",
			ExpectedBehavior: map[string]interface{}{
				"should_process_base64":  true,
				"should_describe_image":  true,
				"should_identify_animal": "lion or animal",
				"vision_processing":      true,
			},
			TestMetadata: map[string]interface{}{
				"provider":          testConfig.Provider,
				"model":             testConfig.VisionModel,
				"image_type":        "base64",
				"encoding":          "base64",
				"test_animal":       "lion",
				"expected_keywords": []string{"lion", "animal", "cat", "feline", "big cat"}, // 🦁 Lion-specific terms
			},
		}

		// Enhanced validation for base64 lion image processing (same for both APIs)
		expectations := VisionExpectations([]string{"lion"}) // Should identify it as a lion (more specific than just "animal")
		expectations = ModifyExpectationsForProvider(expectations, testConfig.Provider)
		expectations.MinContentLength = 15  // Should provide some description
		expectations.MaxContentLength = 600 // Base64 processing can be resource intensive
		expectations.ShouldNotContainWords = append(expectations.ShouldNotContainWords, []string{
			"cannot process", "invalid format", "decode error",
			"unable to view", "no image", "corrupted",
		}...) // Base64 processing failure indicators

		// Create operations for both Chat Completions and Responses API
		chatOperation := func() (*schemas.BifrostResponse, *schemas.BifrostError) {
			chatReq := &schemas.BifrostChatRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.VisionModel,
				Input:    chatMessages,
				Params: &schemas.ChatParameters{
					MaxCompletionTokens: bifrost.Ptr(200),
				},
				Fallbacks: testConfig.Fallbacks,
			}
			return client.ChatCompletionRequest(ctx, chatReq)
		}

		responsesOperation := func() (*schemas.BifrostResponse, *schemas.BifrostError) {
			responsesReq := &schemas.BifrostResponsesRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.VisionModel,
				Input:    responsesMessages,
				Params: &schemas.ResponsesParameters{
					MaxOutputTokens: bifrost.Ptr(200),
				},
				Fallbacks: testConfig.Fallbacks,
			}
			return client.ResponsesRequest(ctx, responsesReq)
		}

		// Execute dual API test - passes only if BOTH APIs succeed
		result := WithDualAPITestRetry(t,
			retryConfig,
			retryContext,
			expectations,
			"ImageBase64",
			chatOperation,
			responsesOperation)

		// Validate both APIs succeeded
		if !result.BothSucceeded {
			var errors []string
			if result.ChatCompletionsError != nil {
				errors = append(errors, "Chat Completions: "+GetErrorMessage(result.ChatCompletionsError))
			}
			if result.ResponsesAPIError != nil {
				errors = append(errors, "Responses API: "+GetErrorMessage(result.ResponsesAPIError))
			}
			if len(errors) == 0 {
				errors = append(errors, "One or both APIs failed validation (see logs above)")
			}
			t.Fatalf("❌ ImageBase64 dual API test failed: %v", errors)
		}

		// Additional validation for base64 lion image processing using universal content extraction
		validateBase64ImageProcessing := func(response *schemas.BifrostResponse, apiName string) {
			content := GetResultContent(response)
			contentLower := strings.ToLower(content)
			foundAnimal := strings.Contains(contentLower, "lion") || strings.Contains(contentLower, "animal") ||
				strings.Contains(contentLower, "cat") || strings.Contains(contentLower, "feline")

			if len(content) < 10 {
				t.Logf("⚠️ %s response seems quite short for image description: %s", apiName, content)
			} else if foundAnimal {
				t.Logf("✅ %s vision model successfully identified animal in base64 image", apiName)
			} else {
				t.Logf("✅ %s vision model processed base64 image but may not have clearly identified the animal", apiName)
			}

			t.Logf("✅ %s lion base64 image processing completed: %s", apiName, content)
		}

		// Validate both API responses
		if result.ChatCompletionsResponse != nil {
			validateBase64ImageProcessing(result.ChatCompletionsResponse, "Chat Completions")
		}

		if result.ResponsesAPIResponse != nil {
			validateBase64ImageProcessing(result.ResponsesAPIResponse, "Responses")
		}

		t.Logf("🎉 Both Chat Completions and Responses APIs passed ImageBase64 test!")
	})
}
