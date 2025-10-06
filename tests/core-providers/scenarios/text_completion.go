package scenarios

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunTextCompletionTest tests text completion functionality
func RunTextCompletionTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig config.ComprehensiveTestConfig) {
	if !testConfig.Scenarios.TextCompletion || testConfig.TextModel == "" {
		t.Logf("⏭️ Text completion not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("TextCompletion", func(t *testing.T) {
		prompt := "In fruits, A is for apple and B is for"
		request := &schemas.BifrostTextCompletionRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.TextModel,
			Input: &schemas.TextCompletionInput{
				PromptStr: &prompt,
			},
			Fallbacks: testConfig.Fallbacks,
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
				"model":    testConfig.TextModel,
				"prompt":   prompt,
			},
		}

		// Enhanced validation expectations
		expectations := GetExpectationsForScenario("TextCompletion", testConfig, map[string]interface{}{})
		expectations = ModifyExpectationsForProvider(expectations, testConfig.Provider)
		expectations.ShouldContainKeywords = []string{"banana"}                                                                    // Should continue the AI theme
		expectations.ShouldNotContainWords = append(expectations.ShouldNotContainWords, []string{"error", "failed", "invalid"}...) // Should not contain error terms

		response, bifrostErr := WithTestRetry(t, retryConfig, retryContext, expectations, "TextCompletion", func() (*schemas.BifrostResponse, *schemas.BifrostError) {
			return client.TextCompletionRequest(ctx, request)
		})

		if bifrostErr != nil {
			t.Fatalf("❌ TextCompletion request failed after retries: %v", GetErrorMessage(bifrostErr))
		}

		content := GetResultContent(response)
		t.Logf("✅ Text completion result: %s", content)
	})
}
