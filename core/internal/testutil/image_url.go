package testutil

import (
	"context"
	"fmt"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunImageURLTest executes the image URL test scenario using dual API testing framework
// This function now supports testing multiple vision models - the test passes only if ALL models pass
func RunImageURLTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.ImageURL {
		t.Logf("Image URL not supported for provider %s", testConfig.Provider)
		return
	}

	// Use WrapTestScenario to handle multi-model testing with parallel execution
	t.Run("ImageURL", func(t *testing.T) {
		WrapTestScenario(t, client, ctx, testConfig, "ImageURL", ModelTypeVision, runImageURLTestForModel)
	})
}

// runImageURLTestForModel runs the image URL test for a specific model
// The config passed here will have only ONE model in VisionModels array
func runImageURLTestForModel(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) error {
	// Get the single model from the config
	model := GetVisionModelOrFirst(testConfig)

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
			"model":             model,
			"image_type":        "url",
			"test_image":        TestImageURL,
			"expected_keywords": []string{"ant", "insect", "bug", "arthropod"},
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
			Model:    model,
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
			Model:    model,
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
		return fmt.Errorf("ImageURL dual API test failed: %v", errors)
	}

	// Additional vision-specific validation using universal content extraction
	if result.ChatCompletionsResponse != nil {
		content := GetChatContent(result.ChatCompletionsResponse)
		if err := validateImageProcessingContent(content, "Chat Completions"); err != nil {
			return err
		}
	}

	if result.ResponsesAPIResponse != nil {
		content := GetResponsesContent(result.ResponsesAPIResponse)
		if err := validateImageProcessingContent(content, "Responses"); err != nil {
			return err
		}
	}

	t.Logf("🎉 Both Chat Completions and Responses APIs passed ImageURL test for model: %s!", model)
	return nil
}

func validateImageProcessingContent(content string, apiName string) error {
	lowerContent := strings.ToLower(content)
	foundObjectIdentification := strings.Contains(lowerContent, "ant") || strings.Contains(lowerContent, "insect")

	if foundObjectIdentification {
		return nil // Successfully identified the object
	}

	// Check for other possible valid descriptions
	if strings.Contains(lowerContent, "small") ||
		strings.Contains(lowerContent, "creature") ||
		strings.Contains(lowerContent, "animal") ||
		strings.Contains(lowerContent, "bug") {
		return nil // Provided a reasonable description
	}

	return fmt.Errorf("%s model may have failed to properly process the image: %s", apiName, content)
}
