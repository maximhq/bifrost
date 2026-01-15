package testutil

import (
	"context"
	"fmt"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunTextCompletionTest tests text completion functionality
// This function now supports testing multiple text models - the test passes only if ALL models pass
func RunTextCompletionTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.TextCompletion || GetTextModelOrFirst(testConfig) == "" {
		t.Logf("⏭️ Text completion not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("TextCompletion", func(t *testing.T) {
		WrapTestScenario(t, client, ctx, testConfig, "TextCompletion", ModelTypeText, runTextCompletionTestForModel)
	})
}

// runTextCompletionTestForModel runs the text completion test for a specific model
// The config passed here will have only ONE model in TextModels array
func runTextCompletionTestForModel(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) error {
	// Get the single model from the config
	model := GetTextModelOrFirst(testConfig)

	prompt := "In fruits, A is for apple and B is for"
	request := &schemas.BifrostTextCompletionRequest{
		Provider: testConfig.Provider,
		Model:    model,
		Input: &schemas.TextCompletionInput{
			PromptStr: &prompt,
		},
		Params: &schemas.TextCompletionParameters{
			MaxTokens: bifrost.Ptr(100),
		},
		Fallbacks: testConfig.TextCompletionFallbacks,
	}

	// Use retry framework with enhanced validation
	retryConfig := GetTestRetryConfigForScenario("TextCompletion", testConfig)
	retryContext := TestRetryContext{
		ScenarioName: "TextCompletion",
		ExpectedBehavior: map[string]interface{}{
			"should_continue_prompt": true,
			"should_be_coherent":     true,
		},
		TestMetadata: map[string]interface{}{
			"provider": testConfig.Provider,
			"model":    model,
			"prompt":   prompt,
		},
	}

	// Enhanced validation expectations
	expectations := GetExpectationsForScenario("TextCompletion", testConfig, map[string]interface{}{})
	expectations = ModifyExpectationsForProvider(expectations, testConfig.Provider)
	// Note: Removed strict keyword checks as LLMs are non-deterministic
	// Tests focus on functionality, not exact content

	// Create TextCompletion retry config
	textCompletionRetryConfig := TextCompletionRetryConfig{
		MaxAttempts: retryConfig.MaxAttempts,
		BaseDelay:   retryConfig.BaseDelay,
		MaxDelay:    retryConfig.MaxDelay,
		Conditions:  []TextCompletionRetryCondition{}, // Add specific text completion retry conditions as needed
		OnRetry:     retryConfig.OnRetry,
		OnFinalFail: retryConfig.OnFinalFail,
	}

	response, bifrostErr := WithTextCompletionTestRetry(t, textCompletionRetryConfig, retryContext, expectations, "TextCompletion", func() (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		return client.TextCompletionRequest(bfCtx, request)
	})

	if bifrostErr != nil {
		return fmt.Errorf("TextCompletion request failed after retries: %v", GetErrorMessage(bifrostErr))
	}

	content := GetTextCompletionContent(response)
	t.Logf("✅ Text completion result: %s", content)

	return nil
}
