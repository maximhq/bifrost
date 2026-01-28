package llmtests

import (
	"context"
	"os"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunEnd2EndToolCallingTest executes the end-to-end tool calling test scenario
func RunEnd2EndToolCallingTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.End2EndToolCalling {
		t.Logf("End-to-end tool calling not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("End2EndToolCalling", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		// =============================================================================
		// STEP 1: User asks for weather - Test both APIs in parallel
		// =============================================================================

		// Create messages for both APIs
		chatUserMessage := CreateBasicChatMessage("What's the weather in San Francisco? Give answer in Celsius.")
		responsesUserMessage := CreateBasicResponsesMessage("What's the weather in San Francisco? Give answer in Celsius.")

		// Get tools for both APIs
		chatTool := GetSampleChatTool(SampleToolTypeWeather)
		responsesTool := GetSampleResponsesTool(SampleToolTypeWeather)

		// Use specialized tool call retry configuration for first request
		retryConfig := ToolCallRetryConfig(string(SampleToolTypeWeather))
		retryContext := TestRetryContext{
			ScenarioName: "End2EndToolCalling_Step1",
			ExpectedBehavior: map[string]interface{}{
				"expected_tool_name": string(SampleToolTypeWeather),
				"location":           "san francisco",
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
				"model":    testConfig.ChatModel,
				"step":     "tool_call_request",
			},
		}

		// Enhanced tool call validation for first request
		expectations := ToolCallExpectations(string(SampleToolTypeWeather), []string{"location"})
		expectations = ModifyExpectationsForProvider(expectations, testConfig.Provider)
		expectations.ExpectedToolCalls[0].ArgumentTypes = map[string]string{
			"location": "string",
		}

		// Create operations for both APIs
		chatOperation := func() (*schemas.BifrostChatResponse, *schemas.BifrostError) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			chatReq := &schemas.BifrostChatRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.ChatModel,
				Input:    []schemas.ChatMessage{chatUserMessage},
				Params: &schemas.ChatParameters{
					Tools:               []schemas.ChatTool{*chatTool},
					MaxCompletionTokens: bifrost.Ptr(150),
				},
				Fallbacks: testConfig.Fallbacks,
			}
			return client.ChatCompletionRequest(bfCtx, chatReq)
		}

		responsesOperation := func() (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			responsesReq := &schemas.BifrostResponsesRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.ChatModel,
				Input:    []schemas.ResponsesMessage{responsesUserMessage},
				Params: &schemas.ResponsesParameters{
					Tools: []schemas.ResponsesTool{*responsesTool},
				},
			}
			return client.ResponsesRequest(bfCtx, responsesReq)
		}

		// Execute dual API test for Step 1
		result1 := WithDualAPITestRetry(t,
			retryConfig,
			retryContext,
			expectations,
			"End2EndToolCalling_Step1",
			chatOperation,
			responsesOperation)

		// Validate both APIs succeeded
		if !result1.BothSucceeded {
			var errors []string
			if result1.ChatCompletionsError != nil {
				errors = append(errors, "Chat Completions: "+GetErrorMessage(result1.ChatCompletionsError))
			}
			if result1.ResponsesAPIError != nil {
				errors = append(errors, "Responses API: "+GetErrorMessage(result1.ResponsesAPIError))
			}
			if len(errors) == 0 {
				errors = append(errors, "One or both APIs failed validation (see logs above)")
			}
			t.Fatalf("❌ End2EndToolCalling_Step1 dual API test failed: %v", errors)
		}

		// Extract tool calls from both APIs
		chatToolCalls := ExtractChatToolCalls(result1.ChatCompletionsResponse)
		responsesToolCalls := ExtractResponsesToolCalls(result1.ResponsesAPIResponse)

		if len(chatToolCalls) == 0 {
			t.Fatal("Expected at least one tool call in Chat Completions API response for 'weather'")
		}
		if len(responsesToolCalls) == 0 {
			t.Fatal("Expected at least one tool call in Responses API response for 'weather'")
		}

		chatToolCall := chatToolCalls[0]
		responsesToolCall := responsesToolCalls[0]

		t.Logf("✅ Chat Completions API tool call: %s with args: %s", chatToolCall.Name, chatToolCall.Arguments)
		t.Logf("✅ Responses API tool call: %s with args: %s", responsesToolCall.Name, responsesToolCall.Arguments)

		// =============================================================================
		// STEP 2: Simulate tool execution and provide result - Test both APIs
		// =============================================================================

		toolResult := `{"temperature": "22", "unit": "celsius", "description": "Sunny with light clouds", "humidity": "65%"}`

		// Build conversation history for Chat Completions API
		chatConversationMessages := []schemas.ChatMessage{chatUserMessage}
		if result1.ChatCompletionsResponse.Choices != nil {
			for _, choice := range result1.ChatCompletionsResponse.Choices {
				chatConversationMessages = append(chatConversationMessages, *choice.Message)
			}
		}
		chatConversationMessages = append(chatConversationMessages, CreateToolChatMessage(toolResult, chatToolCall.ID))

		// Build conversation history for Responses API
		responsesConversationMessages := []schemas.ResponsesMessage{responsesUserMessage}
		if result1.ResponsesAPIResponse.Output != nil {
			for _, output := range result1.ResponsesAPIResponse.Output {
				responsesConversationMessages = append(responsesConversationMessages, output)
			}
		}
		responsesConversationMessages = append(responsesConversationMessages, CreateToolResponsesMessage(toolResult, responsesToolCall.ID))

		// Use retry framework for second request (conversation continuation)
		// Step 2 validates conversational synthesis of tool results, not tool calling
		retryConfig2 := GetTestRetryConfigForScenario("CompleteEnd2End_Chat", testConfig)
		retryContext2 := TestRetryContext{
			ScenarioName: "End2EndToolCalling_FinalResponse",
			ExpectedBehavior: map[string]interface{}{
				"should_reference_weather": true,
				"should_mention_location":  true,
				"should_use_tool_result":   true,
			},
			TestMetadata: map[string]interface{}{
				"provider":    testConfig.Provider,
				"model":       testConfig.ChatModel,
				"step":        "final_response",
				"tool_result": toolResult,
			},
		}

		// Enhanced validation for final response
		expectations2 := ConversationExpectations([]string{"francisco", "22"})
		expectations2 = ModifyExpectationsForProvider(expectations2, testConfig.Provider)
		expectations2.ShouldContainKeywords = []string{"francisco", "22"}           // Should reference tool results (using "francisco" to match both "San Francisco" and "san francisco")
		expectations2.ShouldNotContainWords = []string{"error", "failed", "cannot"} // Should not contain error terms

		// Create operations for both APIs - Step 2
		chatOperation2 := func() (*schemas.BifrostChatResponse, *schemas.BifrostError) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			chatReq := &schemas.BifrostChatRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.ChatModel,
				Input:    chatConversationMessages,
				Params: &schemas.ChatParameters{
					MaxCompletionTokens: bifrost.Ptr(200),
				},
				Fallbacks: testConfig.Fallbacks,
			}
			return client.ChatCompletionRequest(bfCtx, chatReq)
		}

		responsesOperation2 := func() (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			responsesReq := &schemas.BifrostResponsesRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.ChatModel,
				Input:    responsesConversationMessages,
				Params: &schemas.ResponsesParameters{
					MaxOutputTokens: bifrost.Ptr(200),
				},
			}
			return client.ResponsesRequest(bfCtx, responsesReq)
		}

		// Execute dual API test for Step 2
		result2 := WithDualAPITestRetry(t,
			retryConfig2,
			retryContext2,
			expectations2,
			"End2EndToolCalling_Step2",
			chatOperation2,
			responsesOperation2)

		// Validate both APIs succeeded
		if !result2.BothSucceeded {
			var errors []string
			if result2.ChatCompletionsError != nil {
				errors = append(errors, "Chat Completions: "+GetErrorMessage(result2.ChatCompletionsError))
			}
			if result2.ResponsesAPIError != nil {
				errors = append(errors, "Responses API: "+GetErrorMessage(result2.ResponsesAPIError))
			}
			if len(errors) == 0 {
				errors = append(errors, "One or both APIs failed validation (see logs above)")
			}
			t.Fatalf("❌ End2EndToolCalling_Step2 dual API test failed: %v", errors)
		}

		// Log results from both APIs
		if result2.ChatCompletionsResponse != nil {
			chatContent := GetChatContent(result2.ChatCompletionsResponse)
			t.Logf("✅ Chat Completions API result: %s", chatContent)

			// Additional validation for Chat Completions API
			contentLower := strings.ToLower(chatContent)
			if !strings.Contains(contentLower, "san francisco") {
				t.Logf("⚠️ Warning: Chat Completions response doesn't mention 'San Francisco': %s", chatContent)
			}
			if !strings.Contains(chatContent, "22") {
				t.Logf("⚠️ Warning: Chat Completions response doesn't mention temperature '22': %s", chatContent)
			}
			if !strings.Contains(contentLower, "sunny") {
				t.Logf("⚠️ Warning: Chat Completions response doesn't mention 'sunny': %s", chatContent)
			}
		}

		if result2.ResponsesAPIResponse != nil {
			responsesContent := GetResponsesContent(result2.ResponsesAPIResponse)
			t.Logf("✅ Responses API result: %s", responsesContent)

			// Additional validation for Responses API
			contentLower := strings.ToLower(responsesContent)
			if !strings.Contains(contentLower, "san francisco") {
				t.Logf("⚠️ Warning: Responses API response doesn't mention 'San Francisco': %s", responsesContent)
			}
			if !strings.Contains(responsesContent, "22") {
				t.Logf("⚠️ Warning: Responses API response doesn't mention temperature '22': %s", responsesContent)
			}
			if !strings.Contains(contentLower, "sunny") {
				t.Logf("⚠️ Warning: Responses API response doesn't mention 'sunny': %s", responsesContent)
			}
		}

		t.Logf("🎉 Both Chat Completions and Responses APIs passed End2EndToolCalling test!")
	})
}
