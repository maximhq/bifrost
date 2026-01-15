package testutil

import (
	"context"
	"fmt"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunImageBase64Test executes the image base64 test scenario using dual API testing framework
// This function now supports testing multiple vision models - the test passes only if ALL models pass
func RunImageBase64Test(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.ImageBase64 {
		t.Logf("Image base64 not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("ImageBase64", func(t *testing.T) {
		WrapTestScenario(t, client, ctx, testConfig, "ImageBase64", ModelTypeVision, runImageBase64TestForModel)
	})
}

// runImageBase64TestForModel runs the image base64 test for a specific model
// The config passed here will have only ONE model in VisionModels array
func runImageBase64TestForModel(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) error {
	// Get the single model from the config
	model := GetVisionModelOrFirst(testConfig)

	// Load lion base64 image for testing
	lionBase64, err := GetLionBase64Image()
	if err != nil {
		return fmt.Errorf("Failed to load lion base64 image: %v", err)
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
			"model":             model,
			"image_type":        "base64",
			"encoding":          "base64",
			"test_animal":       "lion",
			"expected_keywords": []string{"lion", "animal", "cat", "feline", "big cat"}, // Lion-specific terms
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
			Model:    model,
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
			Model:    model,
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
		return fmt.Errorf("ImageBase64 dual API test failed: %v", errors)
	}

	// Additional validation for base64 lion image processing using universal content extraction
	validateChatBase64ImageProcessing := func(response *schemas.BifrostChatResponse, apiName string) error {
		content := GetChatContent(response)
		return validateBase64ImageContent(t, content, apiName)
	}

	validateResponsesBase64ImageProcessing := func(response *schemas.BifrostResponsesResponse, apiName string) error {
		content := GetResponsesContent(response)
		return validateBase64ImageContent(t, content, apiName)
	}

	// Validate both API responses
	if result.ChatCompletionsResponse != nil {
		if err := validateChatBase64ImageProcessing(result.ChatCompletionsResponse, "Chat Completions"); err != nil {
			return err
		}
	}

	if result.ResponsesAPIResponse != nil {
		if err := validateResponsesBase64ImageProcessing(result.ResponsesAPIResponse, "Responses"); err != nil {
			return err
		}
	}

	t.Logf("🎉 Both Chat Completions and Responses APIs passed ImageBase64 test for model: %s!", model)
	return nil
}

func validateBase64ImageContent(t *testing.T, content string, apiName string) error {
	lowerContent := strings.ToLower(content)
	foundAnimal := strings.Contains(lowerContent, "lion") || strings.Contains(lowerContent, "animal") ||
		strings.Contains(lowerContent, "cat") || strings.Contains(lowerContent, "feline")

	if len(content) < 10 {
		return fmt.Errorf("%s response too short for image description: %s", apiName, content)
	}

	if !foundAnimal {
		return fmt.Errorf("%s vision model failed to identify any animal in base64 image: %s", apiName, content)
	}

	t.Logf("✅ %s vision model successfully identified animal in base64 image", apiName)
	t.Logf("✅ %s lion base64 image processing completed: %s", apiName, content)
	return nil
}
