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
		// Multi-step conversation with tools and images
		userMessage1 := CreateBasicChatMessage("Hi, I'm planning a trip. Can you help me get the weather in Paris?")

		tool := GetSampleTool(SampleToolTypeWeather, false)

		request1 := &schemas.BifrostRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ChatModel,
			Input: schemas.RequestInput{
				ChatCompletionInput: &[]schemas.ChatMessage{userMessage1},
			},
			Params: MergeModelParameters(&schemas.ModelParameters{
				Tools:     &[]schemas.Tool{*tool},
				MaxTokens: bifrost.Ptr(150),
			}, testConfig.CustomParams),
			Fallbacks: testConfig.Fallbacks,
		}

		// Use retry framework for first step (tool calling)
		retryConfig1 := ToolCallRetryConfig("get_weather")
		retryContext1 := TestRetryContext{
			ScenarioName: "CompleteEnd2End_Step1",
			ExpectedBehavior: map[string]interface{}{
				"expected_tool_name": "get_weather",
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
		expectations1 := ToolCallExpectations("get_weather", []string{"location"})
		expectations1 = ModifyExpectationsForProvider(expectations1, testConfig.Provider)
		expectations1.ExpectedToolCalls[0].ArgumentTypes = map[string]string{
			"location": "string",
		}

		response1, bifrostErr := WithTestRetry(t, retryConfig1, retryContext1, expectations1, "CompleteEnd2End_Step1", func() (*schemas.BifrostResponse, *schemas.BifrostError) {
			return client.ChatCompletionRequest(ctx, request1)
		})

		if bifrostErr != nil {
			t.Fatalf("❌ Complete end-to-end step 1 request failed after retries: %v", GetErrorMessage(bifrostErr))
		}

		t.Logf("✅ First response: %s", GetResultContent(response1))

		// Build conversation history and extract tool call if present
		var conversationHistory []schemas.ChatMessage
		conversationHistory = append(conversationHistory, userMessage1)

		// Add all choice messages to conversation history
		if response1.Choices != nil {
			for _, choice := range response1.Choices {
				conversationHistory = append(conversationHistory, choice.Message)
			}
		}

		// Find any choice with tool calls for processing
		var selectedToolCall *schemas.ChatAssistantMessageToolCall
		if response1.Choices != nil {
			for _, choice := range response1.Choices {
				message := choice.Message
				if message.ChatAssistantMessage != nil && message.ChatAssistantMessage.ToolCalls != nil {
					toolCalls := *message.ChatAssistantMessage.ToolCalls
					// Look for a valid weather tool call
					for _, toolCall := range toolCalls {
						if toolCall.Function.Name != nil && *toolCall.Function.Name == "get_weather" {
							selectedToolCall = &toolCall
							t.Logf("✅ Found weather tool call: %s with args: %s", *toolCall.Function.Name, toolCall.Function.Arguments)
							break
						}
					}
					if selectedToolCall != nil {
						break
					}
				}
			}
		}

		// If a tool call was found, simulate the result
		if selectedToolCall != nil {
			// Simulate tool result
			toolResult := `{"temperature": "18", "unit": "celsius", "description": "Partly cloudy", "humidity": "70%"}`
			toolCallID := ""
			if selectedToolCall.ID != nil {
				toolCallID = *selectedToolCall.ID
			} else if selectedToolCall.Function.Name != nil {
				toolCallID = *selectedToolCall.Function.Name
			}

			if toolCallID == "" {
				t.Fatal("toolCallID must not be empty – provider did not return ID or Function.Name")
			}

			toolMessage := CreateToolMessage(toolResult, toolCallID, false)
			conversationHistory = append(conversationHistory, toolMessage)
			t.Logf("✅ Added tool result to conversation history")
		} else {
			t.Logf("⚠️ No weather tool call found in response, continuing without tool result")
		}

		// Continue with follow-up (multimodal if supported)
		followUpMessage := CreateBasicChatMessage("Thanks! Now can you tell me about this travel image?")
		isVisionStep := false

		if testConfig.Scenarios.ImageURL {
			followUpMessage = CreateImageMessage("Thanks! Now can you tell me what you see in this travel-related image? Please provide some travel advice about this destination.", TestImageURL2, false)
			isVisionStep = true
		}
		conversationHistory = append(conversationHistory, followUpMessage)

		model := testConfig.ChatModel
		if isVisionStep {
			model = testConfig.VisionModel
		}

		finalRequest := &schemas.BifrostRequest{
			Provider: testConfig.Provider,
			Model:    model,
			Input: schemas.RequestInput{
				ChatCompletionInput: &conversationHistory,
			},
			Params: MergeModelParameters(&schemas.ModelParameters{
				MaxTokens: bifrost.Ptr(200),
			}, testConfig.CustomParams),
			Fallbacks: testConfig.Fallbacks,
		}

		// Use appropriate retry config for final step
		var retryConfig2 TestRetryConfig
		var expectations2 ResponseExpectations

		if isVisionStep {
			retryConfig2 = GetTestRetryConfigForScenario("CompleteEnd2End_Vision", testConfig)
			expectations2 = VisionExpectations([]string{"image", "see", "travel"})
		} else {
			retryConfig2 = GetTestRetryConfigForScenario("CompleteEnd2End_Chat", testConfig)
			expectations2 = ConversationExpectations([]string{"travel", "image"})
		}

		retryContext2 := TestRetryContext{
			ScenarioName: "CompleteEnd2End_Step2",
			ExpectedBehavior: map[string]interface{}{
				"continue_conversation": true,
				"acknowledge_context":   true,
				"vision_processing":     isVisionStep,
			},
			TestMetadata: map[string]interface{}{
				"provider":            testConfig.Provider,
				"model":               model,
				"step":                "final_response",
				"has_vision":          isVisionStep,
				"conversation_length": len(conversationHistory),
				"expected_keywords":   []string{"travel", "see"}, // 🎯 Must match VisionExpectations exactly
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

		finalResponse, bifrostErr := WithTestRetry(t, retryConfig2, retryContext2, expectations2, "CompleteEnd2End_Step2", func() (*schemas.BifrostResponse, *schemas.BifrostError) {
			return client.ChatCompletionRequest(ctx, finalRequest)
		})

		if bifrostErr != nil {
			t.Fatalf("❌ Complete end-to-end step 2 request failed after retries: %v", GetErrorMessage(bifrostErr))
		}

		finalContent := GetResultContent(finalResponse)

		// Additional validation for conversation context
		if selectedToolCall != nil && strings.Contains(strings.ToLower(finalContent), "weather") {
			t.Logf("✅ Model maintained weather context from previous step")
		}

		if isVisionStep && len(finalContent) > 30 {
			t.Logf("✅ Model processed vision request with substantial response")
		}

		t.Logf("✅ Complete end-to-end test completed successfully")
		t.Logf("Final result: %s", finalContent)
	})
}
