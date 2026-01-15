package testutil

import (
	"context"
	"fmt"
	"os"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunSimpleChatTest executes the simple chat test scenario using dual API testing framework
// This function now supports testing multiple chat models - the test passes only if ALL models pass
func RunSimpleChatTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.SimpleChat {
		t.Logf("Simple chat not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("SimpleChat", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		// Use the generic multi-model test wrapper
		result := RunMultiModelTest(t, client, ctx, testConfig, "SimpleChat", ModelTypeChat, runSimpleChatTestForModel)

		// Assert all models passed - this will fail the test if any model failed
		AssertAllModelsPassed(t, result)
	})
}

// runSimpleChatTestForModel runs the simple chat test for a specific model
// The config passed here will have only ONE model in ChatModels array
func runSimpleChatTestForModel(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) error {
	// Get the single model from the config
	model := GetChatModelOrFirst(testConfig)
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
			"model":    model,
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
			Model:    model,
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
			Model:     model,
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
		return fmt.Errorf("Chat Completions API failed: %s", GetErrorMessage(chatError))
	}
	if responsesError != nil {
		return fmt.Errorf("Responses API failed: %s", GetErrorMessage(responsesError))
	}

	// Log results from both APIs
	if chatResponse != nil {
		chatContent := GetChatContent(chatResponse)
		t.Logf("✅ Chat Completions API result: %s", chatContent)
	}

	if responsesResponse != nil {
		responsesContent := GetResponsesContent(responsesResponse)
		t.Logf("✅ Responses API result: %s", responsesContent)
	}

	t.Logf("🎉 Both Chat Completions and Responses APIs passed SimpleChat test for model: %s!", model)
	return nil
}
