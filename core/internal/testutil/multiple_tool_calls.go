package testutil

import (
	"context"
	"fmt"
	"os"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// getKeysFromMap returns the keys of a map[string]bool as a slice
func getKeysFromMap(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// RunMultipleToolCallsTest executes the multiple tool calls test scenario using dual API testing framework
// This function now supports testing multiple chat models - the test passes only if ALL models pass
func RunMultipleToolCallsTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.MultipleToolCalls {
		t.Logf("Multiple tool calls not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("MultipleToolCalls", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		// Use the generic multi-model test wrapper
		result := RunMultiModelTest(t, client, ctx, testConfig, "MultipleToolCalls", ModelTypeChat, runMultipleToolCallsTestForModel)

		// Assert all models passed - this will fail the test if any model failed
		AssertAllModelsPassed(t, result)
	})
}

// runMultipleToolCallsTestForModel runs the multiple tool calls test for a specific model
// The config passed here will have only ONE model in ChatModels array
func runMultipleToolCallsTestForModel(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) error {
	// Get the single model from the config
	model := GetChatModelOrFirst(testConfig)
	chatMessages := []schemas.ChatMessage{
		CreateBasicChatMessage("I need to know the weather in London and also calculate 15 * 23. Can you help with both in a single request?"),
	}
	responsesMessages := []schemas.ResponsesMessage{
		CreateBasicResponsesMessage("I need to know the weather in London and also calculate 15 * 23. Can you help with both in a single request?"),
	}

	// Get tools for both APIs using the new GetSampleTool function
	chatWeatherTool := GetSampleChatTool(SampleToolTypeWeather)                // Chat Completions API
	chatCalculatorTool := GetSampleChatTool(SampleToolTypeCalculate)           // Chat Completions API
	responsesWeatherTool := GetSampleResponsesTool(SampleToolTypeWeather)      // Responses API
	responsesCalculatorTool := GetSampleResponsesTool(SampleToolTypeCalculate) // Responses API

	// Use specialized multi-tool retry configuration
	retryConfig := MultiToolRetryConfig(2, []string{"weather", "calculate"})
	retryContext := TestRetryContext{
		ScenarioName: "MultipleToolCalls",
		ExpectedBehavior: map[string]interface{}{
			"expected_tool_count": 2,
			"should_handle_both":  true,
		},
		TestMetadata: map[string]interface{}{
			"provider": testConfig.Provider,
			"model":    model,
		},
	}

	// Enhanced multi-tool validation (same for both APIs)
	expectedTools := []string{"weather", "calculate"}
	expectations := MultipleToolExpectations(expectedTools, [][]string{{"location"}, {"expression"}})
	expectations = ModifyExpectationsForProvider(expectations, testConfig.Provider)

	// Add additional validation for the specific tools
	expectations.ExpectedToolCalls[0].ArgumentTypes = map[string]string{
		"location": "string",
	}
	expectations.ExpectedToolCalls[1].ArgumentTypes = map[string]string{
		"expression": "string",
	}
	expectations.ExpectedChoiceCount = 0 // to remove the check

	// Create operations for both Chat Completions and Responses API
	chatOperation := func() (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		chatReq := &schemas.BifrostChatRequest{
			Provider: testConfig.Provider,
			Model:    model,
			Params: &schemas.ChatParameters{
				Tools: []schemas.ChatTool{*chatWeatherTool, *chatCalculatorTool},
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
				Tools: []schemas.ResponsesTool{*responsesWeatherTool, *responsesCalculatorTool},
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
		"MultipleToolCalls",
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
		return fmt.Errorf("MultipleToolCalls dual API test failed: %v", errors)
	}

	// Verify we got the expected tools using universal tool extraction
	validateChatMultipleToolCalls := func(response *schemas.BifrostChatResponse, apiName string) error {
		toolCalls := ExtractChatToolCalls(response)
		toolsFound := make(map[string]bool)
		toolCallCount := len(toolCalls)

		for _, toolCall := range toolCalls {
			if toolCall.Name != "" {
				toolsFound[toolCall.Name] = true
				t.Logf("✅ %s found tool call: %s with args: %s", apiName, toolCall.Name, toolCall.Arguments)
			}
		}

		// Validate that we got both expected tools
		for _, expectedTool := range expectedTools {
			if !toolsFound[expectedTool] {
				return fmt.Errorf("%s API expected tool '%s' not found. Found tools: %v", apiName, expectedTool, getKeysFromMap(toolsFound))
			}
		}

		if toolCallCount < 2 {
			return fmt.Errorf("%s API expected at least 2 tool calls, got %d", apiName, toolCallCount)
		}

		t.Logf("✅ %s API successfully found %d tool calls: %v", apiName, toolCallCount, getKeysFromMap(toolsFound))
		return nil
	}

	validateResponsesMultipleToolCalls := func(response *schemas.BifrostResponsesResponse, apiName string) error {
		toolCalls := ExtractResponsesToolCalls(response)
		toolsFound := make(map[string]bool)
		toolCallCount := len(toolCalls)

		for _, toolCall := range toolCalls {
			if toolCall.Name != "" {
				toolsFound[toolCall.Name] = true
				t.Logf("✅ %s found tool call: %s with args: %s", apiName, toolCall.Name, toolCall.Arguments)
			}
		}

		// Validate that we got both expected tools
		for _, expectedTool := range expectedTools {
			if !toolsFound[expectedTool] {
				return fmt.Errorf("%s API expected tool '%s' not found. Found tools: %v", apiName, expectedTool, getKeysFromMap(toolsFound))
			}
		}

		if toolCallCount < 2 {
			return fmt.Errorf("%s API expected at least 2 tool calls, got %d", apiName, toolCallCount)
		}

		t.Logf("✅ %s API successfully found %d tool calls: %v", apiName, toolCallCount, getKeysFromMap(toolsFound))
		return nil
	}

	// Validate both API responses
	if result.ChatCompletionsResponse != nil {
		if err := validateChatMultipleToolCalls(result.ChatCompletionsResponse, "Chat Completions"); err != nil {
			return err
		}
	}

	if result.ResponsesAPIResponse != nil {
		if err := validateResponsesMultipleToolCalls(result.ResponsesAPIResponse, "Responses"); err != nil {
			return err
		}
	}

	t.Logf("🎉 Both Chat Completions and Responses APIs passed MultipleToolCalls test for model: %s!", model)
	return nil
}
