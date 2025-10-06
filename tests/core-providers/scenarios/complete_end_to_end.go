package scenarios

import (
	"context"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunCompleteEnd2EndTest executes the complete end-to-end test scenario
func RunCompleteEnd2EndTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig config.ComprehensiveTestConfig) {
	if !testConfig.Scenarios.CompleteEnd2End {
		t.Logf("Complete end-to-end not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("CompleteEnd2End", func(t *testing.T) {
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
		chatOperation1 := func() (*schemas.BifrostResponse, *schemas.BifrostError) {
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
			return client.ChatCompletionRequest(ctx, chatReq)
		}

		responsesOperation1 := func() (*schemas.BifrostResponse, *schemas.BifrostError) {
			responsesReq := &schemas.BifrostResponsesRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.ChatModel,
				Input:    []schemas.ResponsesMessage{responsesUserMessage1},
				Params: &schemas.ResponsesParameters{
					Tools: []schemas.ResponsesTool{*responsesTool},
				},
			}
			return client.ResponsesRequest(ctx, responsesReq)
		}

		// Execute dual API test for Step 1
		result1 := WithDualAPITestRetry(t,
			retryConfig1,
			retryContext1,
			expectations1,
			"CompleteEnd2End_Step1",
			chatOperation1,
			responsesOperation1)

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

		t.Logf("✅ Chat Completions API first response: %s", GetResultContent(result1.ChatCompletionsResponse))
		t.Logf("✅ Responses API first response: %s", GetResultContent(result1.ResponsesAPIResponse))

		// Build conversation histories for both APIs and extract tool calls if present
		chatConversationHistory := []schemas.ChatMessage{chatUserMessage1}
		responsesConversationHistory := []schemas.ResponsesMessage{responsesUserMessage1}

		// Add all choice messages to Chat Completions conversation history
		if result1.ChatCompletionsResponse.Choices != nil {
			for _, choice := range result1.ChatCompletionsResponse.Choices {
				chatConversationHistory = append(chatConversationHistory, choice.Message)
			}
		}

		// Add all output messages to Responses API conversation history
		if result1.ResponsesAPIResponse.ResponsesResponse != nil {
			for _, output := range result1.ResponsesAPIResponse.ResponsesResponse.Output {
				responsesConversationHistory = append(responsesConversationHistory, output)
			}
		}

		// Extract tool calls from both APIs
		chatToolCalls := ExtractToolCalls(result1.ChatCompletionsResponse)
		responsesToolCalls := ExtractToolCalls(result1.ResponsesAPIResponse)

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

			toolResult := `{"temperature": "18", "unit": "celsius", "description": "Partly cloudy", "humidity": "70%"}`
			toolMessage := CreateToolResponsesMessage(toolResult, responsesToolCall.ID)
			responsesConversationHistory = append(responsesConversationHistory, toolMessage)
			t.Logf("✅ Added tool result to Responses API conversation history")
		} else {
			t.Logf("⚠️ No weather tool call found in Responses API response, continuing without tool result")
		}

		// =============================================================================
		// STEP 2: Continue with follow-up (multimodal if supported) - Test both APIs
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
			chatFollowUpMessage = CreateBasicChatMessage("Thanks! Now can you tell me about this travel location?")
			responsesFollowUpMessage = CreateBasicResponsesMessage("Thanks! Now can you tell me about this travel location?")
		}

		chatConversationHistory = append(chatConversationHistory, chatFollowUpMessage)
		responsesConversationHistory = append(responsesConversationHistory, responsesFollowUpMessage)

		model := testConfig.ChatModel
		if isVisionStep {
			model = testConfig.VisionModel
		}

		// Use appropriate retry config for final step
		var retryConfig2 TestRetryConfig
		var expectations2 ResponseExpectations

		if isVisionStep {
			retryConfig2 = GetTestRetryConfigForScenario("CompleteEnd2End_Vision", testConfig)
			expectations2 = VisionExpectations([]string{"paris", "river"})
		} else {
			retryConfig2 = GetTestRetryConfigForScenario("CompleteEnd2End_Chat", testConfig)
			expectations2 = ConversationExpectations([]string{"paris", "cloudy"})
		}

		// Prepare expected keywords to match expectations exactly
		expectedKeywords := []string{"paris", "river"}
		if isVisionStep {
			expectedKeywords = []string{"paris", "river"} // Must match VisionExpectations exactly
		}

		retryContext2 := TestRetryContext{
			ScenarioName: "CompleteEnd2End_Step2",
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
		expectations2 = ModifyExpectationsForProvider(expectations2, testConfig.Provider)
		expectations2.MinContentLength = 20  // Should provide some meaningful response
		expectations2.MaxContentLength = 800 // End-to-end can be verbose
		expectations2.ShouldNotContainWords = []string{
			"cannot help", "don't understand", "confused",
			"start over", "reset conversation",
		} // Context loss indicators

		// Create operations for both APIs - Step 2
		chatOperation2 := func() (*schemas.BifrostResponse, *schemas.BifrostError) {
			chatReq := &schemas.BifrostChatRequest{
				Provider: testConfig.Provider,
				Model:    model,
				Input:    chatConversationHistory,
				Params: &schemas.ChatParameters{
					MaxCompletionTokens: bifrost.Ptr(200),
				},
				Fallbacks: testConfig.Fallbacks,
			}
			return client.ChatCompletionRequest(ctx, chatReq)
		}

		responsesOperation2 := func() (*schemas.BifrostResponse, *schemas.BifrostError) {
			responsesReq := &schemas.BifrostResponsesRequest{
				Provider: testConfig.Provider,
				Model:    model,
				Input:    responsesConversationHistory,
				Params: &schemas.ResponsesParameters{
					MaxOutputTokens: bifrost.Ptr(200),
				},
			}
			return client.ResponsesRequest(ctx, responsesReq)
		}

		// Execute dual API test for Step 2
		result2 := WithDualAPITestRetry(t,
			retryConfig2,
			retryContext2,
			expectations2,
			"CompleteEnd2End_Step2",
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
			t.Fatalf("❌ CompleteEnd2End_Step2 dual API test failed: %v", errors)
		}

		// Log and validate results from both APIs
		if result2.ChatCompletionsResponse != nil {
			chatFinalContent := GetResultContent(result2.ChatCompletionsResponse)

			// Additional validation for conversation context
			if len(chatToolCalls) > 0 && strings.Contains(strings.ToLower(chatFinalContent), "weather") {
				t.Logf("✅ Chat Completions API maintained weather context from previous step")
			}

			if isVisionStep && len(chatFinalContent) > 30 {
				t.Logf("✅ Chat Completions API processed vision request with substantial response")
			}

			t.Logf("✅ Chat Completions API final result: %s", chatFinalContent)
		}

		if result2.ResponsesAPIResponse != nil {
			responsesFinalContent := GetResultContent(result2.ResponsesAPIResponse)

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
