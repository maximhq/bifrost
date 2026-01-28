package llmtests

import (
	"context"
	"os"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunImageBase64Test executes the image base64 test scenario using dual API testing framework
func RunImageBase64Test(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.ImageBase64 {
		t.Logf("Image base64 not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("ImageBase64", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

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
		expectations.ShouldNotContainWords = append(expectations.ShouldNotContainWords, []string{
			"cannot process", "invalid format", "decode error",
			"unable to view", "no image", "corrupted",
		}...) // Base64 processing failure indicators

		// Create operations for both Chat Completions and Responses API
		chatOperation := func() (*schemas.BifrostChatResponse, *schemas.BifrostError) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			chatReq := &schemas.BifrostChatRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.VisionModel,
				Input:    chatMessages,
				Params: &schemas.ChatParameters{
					MaxCompletionTokens: bifrost.Ptr(500),
				},
				Fallbacks: testConfig.Fallbacks,
			}
			return client.ChatCompletionRequest(bfCtx, chatReq)
		}

		responsesOperation := func() (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			responsesReq := &schemas.BifrostResponsesRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.VisionModel,
				Input:    responsesMessages,
				Params: &schemas.ResponsesParameters{
					MaxOutputTokens: bifrost.Ptr(500),
				},
				Fallbacks: testConfig.Fallbacks,
			}
			return client.ResponsesRequest(bfCtx, responsesReq)
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
		validateChatBase64ImageProcessing := func(response *schemas.BifrostChatResponse, apiName string) {
			content := GetChatContent(response)
			validateBase64ImageContent(t, content, apiName)
		}

		validateResponsesBase64ImageProcessing := func(response *schemas.BifrostResponsesResponse, apiName string) {
			content := GetResponsesContent(response)
			validateBase64ImageContent(t, content, apiName)
		}

		// Validate both API responses
		if result.ChatCompletionsResponse != nil {
			validateChatBase64ImageProcessing(result.ChatCompletionsResponse, "Chat Completions")
		}

		if result.ResponsesAPIResponse != nil {
			validateResponsesBase64ImageProcessing(result.ResponsesAPIResponse, "Responses")
		}

		t.Logf("🎉 Both Chat Completions and Responses APIs passed ImageBase64 test!")
	})
}

func validateBase64ImageContent(t *testing.T, content string, apiName string) {
	lowerContent := strings.ToLower(content)
	foundAnimal := strings.Contains(lowerContent, "lion") || strings.Contains(lowerContent, "animal") ||
		strings.Contains(lowerContent, "cat") || strings.Contains(lowerContent, "feline")

	if len(content) < 10 {
		t.Fatalf("❌ %s response too short for image description: %s", apiName, content)
	}

	if !foundAnimal {
		t.Fatalf("❌ %s vision model failed to identify any animal in base64 image: %s", apiName, content)
	}

	t.Logf("✅ %s vision model successfully identified animal in base64 image", apiName)
	t.Logf("✅ %s lion base64 image processing completed: %s", apiName, content)
}
