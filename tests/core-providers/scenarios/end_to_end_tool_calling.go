package scenarios

import (
	"context"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunEnd2EndToolCallingTest executes the end-to-end tool calling test scenario
func RunEnd2EndToolCallingTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig config.ComprehensiveTestConfig) {
	if !testConfig.Scenarios.End2EndToolCalling {
		t.Logf("End-to-end tool calling not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("End2EndToolCalling", func(t *testing.T) {
		// Step 1: User asks for weather
		userMessage := CreateBasicChatMessage("What's the weather in San Francisco?")

		params := MergeModelParameters(&schemas.ModelParameters{
			Tools:     &[]schemas.Tool{*GetSampleTool(SampleToolTypeWeather, false)},
			MaxTokens: bifrost.Ptr(150),
		}, testConfig.CustomParams)

		request := &schemas.BifrostRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ChatModel,
			Input: schemas.RequestInput{
				ChatCompletionInput: &[]schemas.ChatMessage{userMessage},
			},
			Params:    params,
			Fallbacks: testConfig.Fallbacks,
		}

		// Use specialized tool call retry configuration for first request
		retryConfig := ToolCallRetryConfig("get_weather")
		retryContext := TestRetryContext{
			ScenarioName: "End2EndToolCalling_Step1",
			ExpectedBehavior: map[string]interface{}{
				"expected_tool_name": "get_weather",
				"location":           "san francisco",
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
				"model":    testConfig.ChatModel,
				"step":     "tool_call_request",
			},
		}

		// Enhanced tool call validation for first request
		expectations := ToolCallExpectations("get_weather", []string{"location"})
		expectations = ModifyExpectationsForProvider(expectations, testConfig.Provider)
		expectations.ExpectedToolCalls[0].ArgumentTypes = map[string]string{
			"location": "string",
		}

		firstResponse, bifrostErr := WithTestRetry(t, retryConfig, retryContext, expectations, "End2EndToolCalling_Step1", func() (*schemas.BifrostResponse, *schemas.BifrostError) {
			return client.ChatCompletionRequest(ctx, request)
		})

		if bifrostErr != nil {
			t.Fatalf("❌ End2EndToolCalling_Step1 request failed after retries: %v", GetErrorMessage(bifrostErr))
		}

		// Extract the tool call for the next step
		var toolCall schemas.ToolCall
		foundValidChoice := false

		for _, choice := range firstResponse.Choices {
			if choice.Message.AssistantMessage != nil &&
				choice.Message.AssistantMessage.ToolCalls != nil &&
				len(*choice.Message.AssistantMessage.ToolCalls) > 0 {

				firstToolCall := (*choice.Message.AssistantMessage.ToolCalls)[0]
				if firstToolCall.Function.Name != nil && *firstToolCall.Function.Name == "get_weather" {
					toolCall = firstToolCall
					foundValidChoice = true
					t.Logf("✅ Found valid tool call: %s with args: %s", *firstToolCall.Function.Name, firstToolCall.Function.Arguments)
					break
				}
			}
		}

		if !foundValidChoice {
			t.Fatal("Expected at least one choice to have valid tool call for 'get_weather'")
		}

		// Step 2: Simulate tool execution and provide result
		toolResult := `{"temperature": "22", "unit": "celsius", "description": "Sunny with light clouds", "humidity": "65%"}`

		toolCallID := ""
		if toolCall.ID != nil {
			toolCallID = *toolCall.ID
		} else {
			toolCallID = *toolCall.Function.Name
		}

		if toolCallID == "" {
			t.Fatal("toolCallID must not be empty")
		}

		// Build conversation history with all choice messages from first response
		conversationMessages := []schemas.ChatMessage{
			userMessage,
		}

		// Add all choice messages from the first response
		for _, choice := range firstResponse.Choices {
			conversationMessages = append(conversationMessages, choice.Message)
		}

		// Add the tool result message
		conversationMessages = append(conversationMessages, CreateToolMessage(toolResult, toolCallID, false))

		secondRequest := &schemas.BifrostRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ChatModel,
			Input: schemas.RequestInput{
				ChatCompletionInput: &conversationMessages,
			},
			Params: MergeModelParameters(&schemas.ModelParameters{
				MaxTokens: bifrost.Ptr(200),
			}, testConfig.CustomParams),
			Fallbacks: testConfig.Fallbacks,
		}

		// Use retry framework for second request (conversation continuation)
		retryConfig2 := GetTestRetryConfigForScenario("End2EndToolCalling", testConfig)
		retryContext2 := TestRetryContext{
			ScenarioName: "End2EndToolCalling_Step2",
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
		expectations2 := ConversationExpectations([]string{"san francisco", "22", "sunny"})
		expectations2 = ModifyExpectationsForProvider(expectations2, testConfig.Provider)
		expectations2.ShouldContainKeywords = []string{"san francisco", "22", "sunny"} // Should reference tool results
		expectations2.ShouldNotContainWords = []string{"error", "failed", "cannot"}    // Should not contain error terms
		expectations2.MinContentLength = 30                                            // Should be a substantial response

		finalResponse, bifrostErr := WithTestRetry(t, retryConfig2, retryContext2, expectations2, "End2EndToolCalling_Step2", func() (*schemas.BifrostResponse, *schemas.BifrostError) {
			return client.ChatCompletionRequest(ctx, secondRequest)
		})

		if bifrostErr != nil {
			t.Fatalf("❌ End2EndToolCalling_Step2 request failed after retries: %v", GetErrorMessage(bifrostErr))
		}

		content := GetResultContent(finalResponse)
		t.Logf("✅ End-to-end tool calling result: %s", content)

		// Additional validation for end-to-end flow
		contentLower := strings.ToLower(content)
		if !strings.Contains(contentLower, "san francisco") {
			t.Logf("⚠️ Warning: Response doesn't mention 'San Francisco': %s", content)
		}
		if !strings.Contains(content, "22") {
			t.Logf("⚠️ Warning: Response doesn't mention temperature '22': %s", content)
		}
		if !strings.Contains(contentLower, "sunny") {
			t.Logf("⚠️ Warning: Response doesn't mention 'sunny': %s", content)
		}
	})
}
