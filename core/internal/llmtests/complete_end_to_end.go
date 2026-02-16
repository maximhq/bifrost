package llmtests

import (
	"context"
	"os"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunCompleteEnd2EndTest executes the complete end-to-end test scenario
func RunCompleteEnd2EndTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.CompleteEnd2End {
		t.Logf("Complete end-to-end not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("CompleteEnd2End", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		// =============================================================================
		// STEP 1: Multi-step conversation with tools - Test both APIs in parallel
		// =============================================================================

		// Create messages for both APIs
		chatUserMessage1 := CreateBasicChatMessage("Hi, I'm planning a trip. Can you help me get the weather in Paris?")
		responsesUserMessage1 := CreateBasicResponsesMessage("Hi, I'm planning a trip. Can you help me get the weather in Paris?")

		// Get tools for both APIs
		chatTool := GetSampleChatTool(SampleToolTypeWeather)
		responsesTool := GetSampleResponsesTool(SampleToolTypeWeather)

		// Use retry framework for first step (tool calling)
		retryConfig1 := ToolCallRetryConfig(string(SampleToolTypeWeather))
		retryContext1 := TestRetryContext{
			ScenarioName: "CompleteEnd2End_Step1",
			ExpectedBehavior: map[string]interface{}{
				"expected_tool_name": string(SampleToolTypeWeather),
				"location":           "paris",
				"travel_context":     true,
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
				"model":    testConfig.ChatModel,
				"step":     "tool_call_weather",
				"scenario": "complete_end_to_end",
			},
		}

		// Enhanced validation for first step
		expectations1 := ToolCallExpectations(string(SampleToolTypeWeather), []string{"location"})
		expectations1 = ModifyExpectationsForProvider(expectations1, testConfig.Provider)
		expectations1.ExpectedToolCalls[0].ArgumentTypes = map[string]string{
			"location": "string",
		}

		// Create operations for both APIs
		chatOperation1 := func() (*schemas.BifrostChatResponse, *schemas.BifrostError) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			chatReq := &schemas.BifrostChatRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.ChatModel,
				Input:    []schemas.ChatMessage{chatUserMessage1},
				Params: &schemas.ChatParameters{
					Tools: []schemas.ChatTool{*chatTool},
					ToolChoice: &schemas.ChatToolChoice{
						ChatToolChoiceStr: bifrost.Ptr(string(schemas.ChatToolChoiceTypeRequired)),
					},
					MaxCompletionTokens: bifrost.Ptr(150),
				},
				Fallbacks: testConfig.Fallbacks,
			}
			return client.ChatCompletionRequest(bfCtx, chatReq)
		}

		responsesOperation1 := func() (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			responsesReq := &schemas.BifrostResponsesRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.ChatModel,
				Input:    []schemas.ResponsesMessage{responsesUserMessage1},
				Params: &schemas.ResponsesParameters{
					Tools: []schemas.ResponsesTool{*responsesTool},
					ToolChoice: &schemas.ResponsesToolChoice{
						ResponsesToolChoiceStr: bifrost.Ptr(string(schemas.ResponsesToolChoiceTypeRequired)),
					},
					MaxOutputTokens: bifrost.Ptr(150),
				},
			}
			return client.ResponsesRequest(bfCtx, responsesReq)
		}

		// Execute dual API test for Step 1
		result1 := WithDualAPITestRetry(t,
			retryConfig1,
			retryContext1,
			expectations1,
			"CompleteEnd2End_Step1",
			chatOperation1,
			responsesOperation1)
		defer func() {
			if result1.ChatCompletionsResponse != nil {
				result1.ChatCompletionsResponse.Release()
			}
			if result1.ResponsesAPIResponse != nil {
				result1.ResponsesAPIResponse.Release()
			}
		}()

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
			t.Fatalf("❌ CompleteEnd2End_Step1 dual API test failed: %v", errors)
		}

		t.Logf("✅ Chat Completions API first response: %s", GetChatContent(result1.ChatCompletionsResponse))
		t.Logf("✅ Responses API first response: %s", GetResponsesContent(result1.ResponsesAPIResponse))

		// Build conversation histories for both APIs and extract tool calls if present
		chatConversationHistory := []schemas.ChatMessage{chatUserMessage1}
		responsesConversationHistory := []schemas.ResponsesMessage{responsesUserMessage1}

		// Add all choice messages to Chat Completions conversation history
		if result1.ChatCompletionsResponse.Choices != nil {
			for _, choice := range result1.ChatCompletionsResponse.Choices {
				chatConversationHistory = append(chatConversationHistory, *choice.Message)
			}
		}

		// Add all output messages to Responses API conversation history
		if result1.ResponsesAPIResponse != nil && result1.ResponsesAPIResponse.Output != nil {
			responsesConversationHistory = append(responsesConversationHistory, result1.ResponsesAPIResponse.Output...)
		}

		// Extract tool calls from both APIs
		chatToolCalls := ExtractChatToolCalls(result1.ChatCompletionsResponse)
		responsesToolCalls := ExtractResponsesToolCalls(result1.ResponsesAPIResponse)

		// If tool calls were found, simulate the results for both APIs
		if len(chatToolCalls) > 0 {
			chatToolCall := chatToolCalls[0]
			t.Logf("✅ Chat Completions API weather tool call: %s with args: %s", chatToolCall.Name, chatToolCall.Arguments)

			toolResult := `{"temperature": "18", "unit": "celsius", "description": "Partly cloudy", "humidity": "70%"}`
			toolMessage := CreateToolChatMessage(toolResult, chatToolCall.ID)
			chatConversationHistory = append(chatConversationHistory, toolMessage)
			t.Logf("✅ Added tool result to Chat Completions conversation history")
		} else {
			t.Logf("⚠️ No weather tool call found in Chat Completions response, continuing without tool result")
		}

		if len(responsesToolCalls) > 0 {
			responsesToolCall := responsesToolCalls[0]
			t.Logf("✅ Responses API weather tool call: %s with args: %s", responsesToolCall.Name, responsesToolCall.Arguments)

			toolResult := `{"temperature": "18", "unit": "celsius", "description": "cloudy", "humidity": "70%"}`
			toolMessage := CreateToolResponsesMessage(toolResult, responsesToolCall.ID)
			responsesConversationHistory = append(responsesConversationHistory, toolMessage)
			t.Logf("✅ Added tool result to Responses API conversation history")
		} else {
			t.Logf("⚠️ No weather tool call found in Responses API response, continuing without tool result")
		}

		// =============================================================================
		// STEP 2: Send this tool call result to the model again
		// =============================================================================

		// Use retry framework for step 2 (processing tool results)
		retryConfig2 := GetTestRetryConfigForScenario("CompleteEnd2End_ToolResult", testConfig)
		retryContext2 := TestRetryContext{
			ScenarioName: "CompleteEnd2End_Step2",
			ExpectedBehavior: map[string]interface{}{
				"process_tool_result":   true,
				"acknowledge_weather":   true,
				"continue_conversation": true,
			},
			TestMetadata: map[string]interface{}{
				"provider":                      testConfig.Provider,
				"model":                         testConfig.ChatModel,
				"step":                          "process_tool_result",
				"scenario":                      "complete_end_to_end",
				"chat_conversation_length":      len(chatConversationHistory),
				"responses_conversation_length": len(responsesConversationHistory),
			},
		}

		// Enhanced validation for step 2 - should acknowledge tool results
		expectations2 := ConversationExpectations([]string{"weather", "temperature"})
		expectations2 = ModifyExpectationsForProvider(expectations2, testConfig.Provider)
		expectations2.ShouldNotContainWords = []string{
			"cannot help", "don't understand", "no information",
			"unable to process", "invalid tool result",
		} // Should not indicate confusion about tool results

		// Create operations for both APIs - Step 2 (processing tool results)
		chatOperation2 := func() (*schemas.BifrostChatResponse, *schemas.BifrostError) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			chatReq := &schemas.BifrostChatRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.ChatModel,
				Input:    chatConversationHistory,
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
				Input:    responsesConversationHistory,
				Params: &schemas.ResponsesParameters{
					MaxOutputTokens: bifrost.Ptr(200),
				},
			}
			return client.ResponsesRequest(bfCtx, responsesReq)
		}

		// Execute dual API test for Step 2 (processing tool results)
		result2 := WithDualAPITestRetry(t,
			retryConfig2,
			retryContext2,
			expectations2,
			"CompleteEnd2End_Step2",
			chatOperation2,
			responsesOperation2)
		defer func() {
			if result2.ChatCompletionsResponse != nil {
				result2.ChatCompletionsResponse.Release()
			}
			if result2.ResponsesAPIResponse != nil {
				result2.ResponsesAPIResponse.Release()
			}
		}()

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
			t.Fatalf("❌ CompleteEnd2End_Step2 dual API test failed: %v", errors)
		}

		t.Logf("✅ Chat Completions API tool result response: %s", GetChatContent(result2.ChatCompletionsResponse))
		t.Logf("✅ Responses API tool result response: %s", GetResponsesContent(result2.ResponsesAPIResponse))

		// Add Step 2 responses to conversation histories for Step 3
		if result2.ChatCompletionsResponse.Choices != nil {
			for _, choice := range result2.ChatCompletionsResponse.Choices {
				chatConversationHistory = append(chatConversationHistory, *choice.Message)
			}
		}

		if result2.ResponsesAPIResponse != nil && result2.ResponsesAPIResponse.Output != nil {
			responsesConversationHistory = append(responsesConversationHistory, result2.ResponsesAPIResponse.Output...)
		}

		// =============================================================================
		// STEP 3: Continue with follow-up (multimodal if supported) - Test both APIs
		// =============================================================================

		// Determine if we're doing a vision step
		isVisionStep := testConfig.Scenarios.ImageURL

		// Create follow-up messages for both APIs
		var chatFollowUpMessage schemas.ChatMessage
		var responsesFollowUpMessage schemas.ResponsesMessage

		if isVisionStep {
			chatFollowUpMessage = CreateImageChatMessage("Thanks! Now can you tell me what you see in this travel-related image? Please provide some travel advice about this destination.", TestImageURL2)
			responsesFollowUpMessage = CreateImageResponsesMessage("Thanks! Now can you tell me what you see in this travel-related image? Please provide some travel advice about this destination.", TestImageURL2)
		} else {
			chatFollowUpMessage = CreateBasicChatMessage("Thanks for the weather info! Given that it's cloudy in Paris, can you tell me more about this travel location?")
			responsesFollowUpMessage = CreateBasicResponsesMessage("Thanks for the weather info! Given that it's cloudy in Paris, can you tell me more about this travel location?")
		}

		chatConversationHistory = append(chatConversationHistory, chatFollowUpMessage)
		responsesConversationHistory = append(responsesConversationHistory, responsesFollowUpMessage)

		model := testConfig.ChatModel
		if isVisionStep {
			model = testConfig.VisionModel
		}

		// Use appropriate retry config for final step
		var retryConfig3 TestRetryConfig
		var expectations3 ResponseExpectations

		if isVisionStep {
			retryConfig3 = GetTestRetryConfigForScenario("CompleteEnd2End_Vision", testConfig)
			expectations3 = VisionExpectations([]string{"paris", "river"})
		} else {
			retryConfig3 = GetTestRetryConfigForScenario("CompleteEnd2End_Chat", testConfig)
			expectations3 = ConversationExpectations([]string{"paris", "cloudy"})
		}

		// Prepare expected keywords to match expectations exactly
		var expectedKeywords []string
		if isVisionStep {
			expectedKeywords = []string{"paris", "river"} // Must match VisionExpectations exactly
		} else {
			expectedKeywords = []string{"paris", "cloudy"} // Must match ConversationExpectations exactly
		}

		retryContext3 := TestRetryContext{
			ScenarioName: "CompleteEnd2End_Step3",
			ExpectedBehavior: map[string]interface{}{
				"continue_conversation": true,
				"acknowledge_context":   true,
				"vision_processing":     isVisionStep,
			},
			TestMetadata: map[string]interface{}{
				"provider":                      testConfig.Provider,
				"model":                         model,
				"step":                          "final_response",
				"has_vision":                    isVisionStep,
				"chat_conversation_length":      len(chatConversationHistory),
				"responses_conversation_length": len(responsesConversationHistory),
				"expected_keywords":             expectedKeywords, // 🎯 Must match VisionExpectations exactly
			},
		}

		// Enhanced validation for final response
		expectations3 = ModifyExpectationsForProvider(expectations3, testConfig.Provider)
		expectations3.ShouldNotContainWords = []string{
			"cannot help", "don't understand", "confused",
			"start over", "reset conversation",
		} // Context loss indicators

		// Create operations for both APIs - Step 3
		chatOperation3 := func() (*schemas.BifrostChatResponse, *schemas.BifrostError) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			chatReq := &schemas.BifrostChatRequest{
				Provider: testConfig.Provider,
				Model:    model,
				Input:    chatConversationHistory,
				Params: &schemas.ChatParameters{
					MaxCompletionTokens: bifrost.Ptr(200),
				},
				Fallbacks: testConfig.Fallbacks,
			}
			return client.ChatCompletionRequest(bfCtx, chatReq)
		}

		responsesOperation3 := func() (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			responsesReq := &schemas.BifrostResponsesRequest{
				Provider: testConfig.Provider,
				Model:    model,
				Input:    responsesConversationHistory,
				Params: &schemas.ResponsesParameters{
					MaxOutputTokens: bifrost.Ptr(200),
				},
			}
			return client.ResponsesRequest(bfCtx, responsesReq)
		}

		// Execute dual API test for Step 3
		result3 := WithDualAPITestRetry(t,
			retryConfig3,
			retryContext3,
			expectations3,
			"CompleteEnd2End_Step3",
			chatOperation3,
			responsesOperation3)
		defer func() {
			if result3.ChatCompletionsResponse != nil {
				result3.ChatCompletionsResponse.Release()
			}
			if result3.ResponsesAPIResponse != nil {
				result3.ResponsesAPIResponse.Release()
			}
		}()

		// Validate both APIs succeeded
		if !result3.BothSucceeded {
			var errors []string
			if result3.ChatCompletionsError != nil {
				errors = append(errors, "Chat Completions: "+GetErrorMessage(result3.ChatCompletionsError))
			}
			if result3.ResponsesAPIError != nil {
				errors = append(errors, "Responses API: "+GetErrorMessage(result3.ResponsesAPIError))
			}
			if len(errors) == 0 {
				errors = append(errors, "One or both APIs failed validation (see logs above)")
			}
			t.Fatalf("❌ CompleteEnd2End_Step3 dual API test failed: %v", errors)
		}

		// Log and validate results from both APIs
		if result3.ChatCompletionsResponse != nil {
			chatFinalContent := GetChatContent(result3.ChatCompletionsResponse)

			// Additional validation for conversation context
			if len(chatToolCalls) > 0 && strings.Contains(strings.ToLower(chatFinalContent), "weather") {
				t.Logf("✅ Chat Completions API maintained weather context from previous step")
			}

			if isVisionStep && len(chatFinalContent) > 30 {
				t.Logf("✅ Chat Completions API processed vision request with substantial response")
			}

			t.Logf("✅ Chat Completions API final result: %s", chatFinalContent)
		}

		if result3.ResponsesAPIResponse != nil {
			responsesFinalContent := GetResponsesContent(result3.ResponsesAPIResponse)

			// Additional validation for conversation context
			if len(responsesToolCalls) > 0 && strings.Contains(strings.ToLower(responsesFinalContent), "weather") {
				t.Logf("✅ Responses API maintained weather context from previous step")
			}

			if isVisionStep && len(responsesFinalContent) > 30 {
				t.Logf("✅ Responses API processed vision request with substantial response")
			}

			t.Logf("✅ Responses API final result: %s", responsesFinalContent)
		}

		t.Logf("🎉 Both Chat Completions and Responses APIs passed CompleteEnd2End test!")
	})
}
