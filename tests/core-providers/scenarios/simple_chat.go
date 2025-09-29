package scenarios

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunSimpleChatTest executes the simple chat test scenario using dual API testing framework
func RunSimpleChatTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig config.ComprehensiveTestConfig) {
	if !testConfig.Scenarios.SimpleChat {
		t.Logf("Simple chat not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("SimpleChat", func(t *testing.T) {
		chatMessages := []schemas.ChatMessage{
			CreateBasicChatMessage("Hello! What's the capital of France?"),
		}
		responsesMessages := []schemas.ResponsesMessage{
			CreateBasicResponsesMessage("Hello! What's the capital of France?"),
		}

		// Use retry framework with enhanced validation
		retryConfig := GetTestRetryConfigForScenario("SimpleChat", testConfig)
		retryContext := TestRetryContext{
			ScenarioName: "SimpleChat",
			ExpectedBehavior: map[string]interface{}{
				"should_mention_paris": true,
				"should_be_factual":    true,
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
				"model":    testConfig.ChatModel,
			},
		}

		// Enhanced validation expectations (same for both APIs)
		expectations := GetExpectationsForScenario("SimpleChat", testConfig, map[string]interface{}{})
		expectations = ModifyExpectationsForProvider(expectations, testConfig.Provider)
		expectations.ShouldContainKeywords = append(expectations.ShouldContainKeywords, "paris")                                   // Should mention Paris as the capital
		expectations.ShouldNotContainWords = append(expectations.ShouldNotContainWords, []string{"berlin", "london", "madrid"}...) // Common wrong answers

		// Create operations for both Chat Completions and Responses API
		chatOperation := func() (*schemas.BifrostResponse, *schemas.BifrostError) {
			chatReq := &schemas.BifrostChatRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.ChatModel,
				Input:    chatMessages,
				Params: &schemas.ChatParameters{
					MaxCompletionTokens: bifrost.Ptr(150),
				},
			}
			return client.ChatCompletionRequest(ctx, chatReq)
		}

		responsesOperation := func() (*schemas.BifrostResponse, *schemas.BifrostError) {
			responsesReq := &schemas.BifrostResponsesRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.ChatModel,
				Input:    responsesMessages,
				Params: &schemas.ResponsesParameters{
					MaxOutputTokens: bifrost.Ptr(150),
				},
			}
			return client.ResponsesRequest(ctx, responsesReq)
		}

		// Execute dual API test - passes only if BOTH APIs succeed
		result := WithDualAPITestRetry(t,
			retryConfig,
			retryContext,
			expectations,
			"SimpleChat",
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
			t.Fatalf("❌ SimpleChat dual API test failed: %v", errors)
		}

		// Log results from both APIs
		if result.ChatCompletionsResponse != nil {
			chatContent := GetResultContent(result.ChatCompletionsResponse)
			t.Logf("✅ Chat Completions API result: %s", chatContent)
		}

		if result.ResponsesAPIResponse != nil {
			responsesContent := GetResultContent(result.ResponsesAPIResponse)
			t.Logf("✅ Responses API result: %s", responsesContent)
		}

		t.Logf("🎉 Both Chat Completions and Responses APIs passed SimpleChat test!")
	})
}
