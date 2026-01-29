package llmtests

import (
	"context"
	"os"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunMultiTurnConversationTest executes the multi-turn conversation test scenario
func RunMultiTurnConversationTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.MultiTurnConversation {
		t.Logf("Multi-turn conversation not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("MultiTurnConversation", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		// First message - introduction
		userMessage1 := CreateBasicChatMessage("Hello, my name is Alice.")
		messages1 := []schemas.ChatMessage{
			userMessage1,
		}

		firstRequest := &schemas.BifrostChatRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ChatModel,
			Input:    messages1,
			Params: &schemas.ChatParameters{
				MaxCompletionTokens: bifrost.Ptr(150),
			},
			Fallbacks: testConfig.Fallbacks,
		}

		// Use retry framework for first request
		retryConfig1 := GetTestRetryConfigForScenario("MultiTurnConversation", testConfig)
		retryContext1 := TestRetryContext{
			ScenarioName: "MultiTurnConversation_Step1",
			ExpectedBehavior: map[string]interface{}{
				"acknowledging_name": true,
				"polite_response":    true,
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
				"model":    testConfig.ChatModel,
				"step":     "introduction",
			},
		}
		chatRetryConfig1 := ChatRetryConfig{
			MaxAttempts: retryConfig1.MaxAttempts,
			BaseDelay:   retryConfig1.BaseDelay,
			MaxDelay:    retryConfig1.MaxDelay,
			Conditions:  []ChatRetryCondition{}, // Add specific chat retry conditions as needed
			OnRetry:     retryConfig1.OnRetry,
			OnFinalFail: retryConfig1.OnFinalFail,
		}

		// Enhanced validation for first response
		// Just check that it acknowledges Alice by name - being less strict about exact wording
		expectations1 := ConversationExpectations([]string{"alice"})
		expectations1 = ModifyExpectationsForProvider(expectations1, testConfig.Provider)

		response1, bifrostErr := WithChatTestRetry(t, chatRetryConfig1, retryContext1, expectations1, "MultiTurnConversation_Step1", func() (*schemas.BifrostChatResponse, *schemas.BifrostError) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			return client.ChatCompletionRequest(bfCtx, firstRequest)
		})

		if bifrostErr != nil {
			t.Fatalf("❌ MultiTurnConversation_Step1 request failed after retries: %v", GetErrorMessage(bifrostErr))
		}

		t.Logf("✅ First turn acknowledged: %s", GetChatContent(response1))

		// Second message with conversation history - memory test
		messages2 := []schemas.ChatMessage{
			userMessage1,
		}

		// Add all choice messages from the first response
		if response1 != nil {
			for _, choice := range response1.Choices {
				if choice.Message != nil {
					messages2 = append(messages2, *choice.Message)
				}
			}
		}

		// Add the follow-up question to test memory
		messages2 = append(messages2, CreateBasicChatMessage("What's my name?"))

		secondRequest := &schemas.BifrostChatRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ChatModel,
			Input:    messages2,
			Params: &schemas.ChatParameters{
				MaxCompletionTokens: bifrost.Ptr(150),
			},
			Fallbacks: testConfig.Fallbacks,
		}

		// Use retry framework for memory recall test
		retryConfig2 := GetTestRetryConfigForScenario("MultiTurnConversation", testConfig)
		retryContext2 := TestRetryContext{
			ScenarioName: "MultiTurnConversation_Step2",
			ExpectedBehavior: map[string]interface{}{
				"should_remember_alice": true,
				"memory_recall":         true,
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
				"model":    testConfig.ChatModel,
				"step":     "memory_test",
				"context":  "name_recall",
			},
		}
		chatRetryConfig2 := ChatRetryConfig{
			MaxAttempts: retryConfig2.MaxAttempts,
			BaseDelay:   retryConfig2.BaseDelay,
			MaxDelay:    retryConfig2.MaxDelay,
			Conditions:  []ChatRetryCondition{},
			OnRetry:     retryConfig2.OnRetry,
			OnFinalFail: retryConfig2.OnFinalFail,
		}

		// Enhanced validation for memory recall response
		expectations2 := ConversationExpectations([]string{"alice"})
		expectations2 = ModifyExpectationsForProvider(expectations2, testConfig.Provider)
		expectations2.ShouldContainKeywords = []string{"alice"}                                  // Case insensitive
		expectations2.ShouldNotContainWords = []string{"don't know", "can't remember", "forgot"} // Memory failure indicators

		response2, bifrostErr := WithChatTestRetry(t, chatRetryConfig2, retryContext2, expectations2, "MultiTurnConversation_Step2", func() (*schemas.BifrostChatResponse, *schemas.BifrostError) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			return client.ChatCompletionRequest(bfCtx, secondRequest)
		})

		if bifrostErr != nil {
			t.Fatalf("❌ MultiTurnConversation_Step2 request failed after retries: %v", GetErrorMessage(bifrostErr))
		}

		// Validation already happened inside WithChatTestRetry via expectations2
		// If we reach here, the model successfully remembered "Alice"
		content := GetChatContent(response2)
		t.Logf("✅ Model successfully remembered the name: %s", content)
		t.Logf("✅ Multi-turn conversation completed successfully")
	})
}
