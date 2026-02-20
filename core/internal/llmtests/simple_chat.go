package llmtests

import (
	"context"
	"os"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunSimpleChatTest executes the simple chat test scenario using dual API testing framework
func RunSimpleChatTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.SimpleChat {
		t.Logf("Simple chat not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("SimpleChat", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

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

		// Create Chat Completions API retry config
		chatRetryConfig := ChatRetryConfig{
			MaxAttempts: retryConfig.MaxAttempts,
			BaseDelay:   retryConfig.BaseDelay,
			MaxDelay:    retryConfig.MaxDelay,
			Conditions:  []ChatRetryCondition{}, // Add specific chat retry conditions as needed
			OnRetry:     retryConfig.OnRetry,
			OnFinalFail: retryConfig.OnFinalFail,
		}

		// Create Responses API retry config
		responsesRetryConfig := ResponsesRetryConfig{
			MaxAttempts: retryConfig.MaxAttempts,
			BaseDelay:   retryConfig.BaseDelay,
			MaxDelay:    retryConfig.MaxDelay,
			Conditions:  []ResponsesRetryCondition{}, // Add specific responses retry conditions as needed
			OnRetry:     retryConfig.OnRetry,
			OnFinalFail: retryConfig.OnFinalFail,
		}

		// Test Chat Completions API
		chatOperation := func() (*schemas.BifrostChatResponse, *schemas.BifrostError) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			chatReq := &schemas.BifrostChatRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.ChatModel,
				Input:    chatMessages,
				Params: &schemas.ChatParameters{
					MaxCompletionTokens: bifrost.Ptr(150),
				},
				Fallbacks: testConfig.Fallbacks,
			}
			response, err := client.ChatCompletionRequest(bfCtx, chatReq)
			if err != nil {
				return nil, err
			}
			if response != nil {
				return response, nil
			}
			return nil, &schemas.BifrostError{
				IsBifrostError: true,
				Error: &schemas.ErrorField{
					Message: "No chat response returned",
				},
			}
		}

		chatResponse, chatError := WithChatTestRetry(t, chatRetryConfig, retryContext, expectations, "SimpleChat_Chat", chatOperation)

		// Test Responses API
		responsesOperation := func() (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			responsesReq := &schemas.BifrostResponsesRequest{
				Provider:  testConfig.Provider,
				Model:     testConfig.ChatModel,
				Input:     responsesMessages,
				Fallbacks: testConfig.Fallbacks,
			}
			response, err := client.ResponsesRequest(bfCtx, responsesReq)
			if err != nil {
				return nil, err
			}
			if response != nil {
				return response, nil
			}
			return nil, &schemas.BifrostError{
				IsBifrostError: true,
				Error: &schemas.ErrorField{
					Message: "No responses response returned",
				},
			}
		}

		responsesResponse, responsesError := WithResponsesTestRetry(t, responsesRetryConfig, retryContext, expectations, "SimpleChat_Responses", responsesOperation)

		// Check that both APIs succeeded
		if chatError != nil {
			t.Fatalf("❌ Chat Completions API failed: %s", GetErrorMessage(chatError))
		}
		if responsesError != nil {
			t.Fatalf("❌ Responses API failed: %s", GetErrorMessage(responsesError))
		}
		defer chatResponse.Release()
		defer responsesResponse.Release()

		// Log results from both APIs
		if chatResponse != nil {
			chatContent := GetChatContent(chatResponse)
			t.Logf("✅ Chat Completions API result: %s", chatContent)
		}

		if responsesResponse != nil {
			responsesContent := GetResponsesContent(responsesResponse)
			t.Logf("✅ Responses API result: %s", responsesContent)
		}

		// Fail test if either API failed
		if chatError != nil || responsesError != nil {
			t.Fatalf("❌ SimpleChat test failed - one or both APIs failed")
		}

		t.Logf("🎉 Both Chat Completions and Responses APIs passed SimpleChat test!")
	})
}
