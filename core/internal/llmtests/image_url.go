package llmtests

import (
	"context"
	"os"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunImageURLTest executes the image URL test scenario using dual API testing framework
func RunImageURLTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.ImageURL {
		t.Logf("Image URL not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("ImageURL", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		// Create messages for both APIs using the isResponsesAPI flag
		chatMessages := []schemas.ChatMessage{
			CreateImageChatMessage("What do you see in this image?", TestImageURL),
		}
		responsesMessages := []schemas.ResponsesMessage{
			CreateImageResponsesMessage("What do you see in this image?", TestImageURL),
		}

		// Use retry framework for vision requests (can be flaky)
		retryConfig := GetTestRetryConfigForScenario("ImageURL", testConfig)
		retryContext := TestRetryContext{
			ScenarioName: "ImageURL",
			ExpectedBehavior: map[string]interface{}{
				"should_describe_image":  true,
				"should_identify_object": "ant or insect",
				"vision_processing":      true,
			},
			TestMetadata: map[string]interface{}{
				"provider":          testConfig.Provider,
				"model":             testConfig.VisionModel,
				"image_type":        "url",
				"test_image":        TestImageURL,
				"expected_keywords": []string{"ant", "insect", "bug", "arthropod"}, // 🎯 Test-specific retry keywords
			},
		}

		// Enhanced validation for vision responses - should identify ant OR insect (same for both APIs)
		expectations := VisionExpectations([]string{}) // Start with base vision expectations
		expectations = ModifyExpectationsForProvider(expectations, testConfig.Provider)
		expectations.ShouldContainKeywords = nil                                                                                                 // Clear strict keyword requirement
		expectations.ShouldContainAnyOf = []string{"ant", "insect", "bug", "arthropod"}                                                          // Accept any valid identification
		expectations.ShouldNotContainWords = append(expectations.ShouldNotContainWords, []string{"cannot see", "unable to view", "no image"}...) // Vision failure indicators

		// Create operations for both Chat Completions and Responses API
		chatOperation := func() (*schemas.BifrostChatResponse, *schemas.BifrostError) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			chatReq := &schemas.BifrostChatRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.VisionModel,
				Params: &schemas.ChatParameters{
					MaxCompletionTokens: bifrost.Ptr(200),
				},
				Fallbacks: testConfig.Fallbacks,
			}
			chatReq.Input = chatMessages
			return client.ChatCompletionRequest(bfCtx, chatReq)
		}

		responsesOperation := func() (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			responsesReq := &schemas.BifrostResponsesRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.VisionModel,
				Params: &schemas.ResponsesParameters{
					MaxOutputTokens: bifrost.Ptr(200),
				},
				Fallbacks: testConfig.Fallbacks,
			}
			responsesReq.Input = responsesMessages
			return client.ResponsesRequest(bfCtx, responsesReq)
		}

		// Execute dual API test - passes only if BOTH APIs succeed
		result := WithDualAPITestRetry(t,
			retryConfig,
			retryContext,
			expectations,
			"ImageURL",
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
			t.Fatalf("❌ ImageURL dual API test failed: %v", errors)
		}
		defer func() {
			if result.ChatCompletionsResponse != nil {
				result.ChatCompletionsResponse.Release()
			}
			if result.ResponsesAPIResponse != nil {
				result.ResponsesAPIResponse.Release()
			}
		}()

		// Additional vision-specific validation using universal content extraction
		validateChatImageProcessing := func(response *schemas.BifrostChatResponse, apiName string) {
			content := GetChatContent(response)
			validateImageProcessingContent(t, content, apiName)
		}

		validateResponsesImageProcessing := func(response *schemas.BifrostResponsesResponse, apiName string) {
			content := GetResponsesContent(response)
			validateImageProcessingContent(t, content, apiName)
		}

		// Validate both API responses
		if result.ChatCompletionsResponse != nil {
			validateChatImageProcessing(result.ChatCompletionsResponse, "Chat Completions")
		}

		if result.ResponsesAPIResponse != nil {
			validateResponsesImageProcessing(result.ResponsesAPIResponse, "Responses")
		}

		t.Logf("🎉 Both Chat Completions and Responses APIs passed ImageURL test!")
	})
}

func validateImageProcessingContent(t *testing.T, content string, apiName string) {
	lowerContent := strings.ToLower(content)
	foundObjectIdentification := strings.Contains(lowerContent, "ant") || strings.Contains(lowerContent, "insect")

	if foundObjectIdentification {
		t.Logf("✅ %s vision model successfully identified the object in image: %s", apiName, content)
	} else {
		// Log warning but don't fail immediately - some models might describe differently
		t.Logf("⚠️ %s vision model may not have explicitly identified 'ant' or 'insect': %s", apiName, content)

		// Check for other possible valid descriptions
		if strings.Contains(lowerContent, "small") ||
			strings.Contains(lowerContent, "creature") ||
			strings.Contains(lowerContent, "animal") ||
			strings.Contains(lowerContent, "bug") {
			t.Logf("✅ But %s model provided a reasonable description of the image", apiName)
		} else {
			t.Logf("❌ %s model may have failed to properly process the image", apiName)
		}
	}
}
