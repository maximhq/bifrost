package llmtests

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

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
func RunMultipleToolCallsTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.MultipleToolCalls {
		t.Logf("Multiple tool calls not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("MultipleToolCalls", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

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
				"model":    testConfig.ChatModel,
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
				Model:    testConfig.ChatModel,
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
				Model:    testConfig.ChatModel,
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
			t.Fatalf("❌ MultipleToolCalls dual API test failed: %v", errors)
		}

		// Verify we got the expected tools using universal tool extraction
		validateChatMultipleToolCalls := func(response *schemas.BifrostChatResponse, apiName string) {
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
					t.Fatalf("%s API expected tool '%s' not found. Found tools: %v", apiName, expectedTool, getKeysFromMap(toolsFound))
				}
			}

			if toolCallCount < 2 {
				t.Fatalf("%s API expected at least 2 tool calls, got %d", apiName, toolCallCount)
			}

			t.Logf("✅ %s API successfully found %d tool calls: %v", apiName, toolCallCount, getKeysFromMap(toolsFound))
		}

		validateResponsesMultipleToolCalls := func(response *schemas.BifrostResponsesResponse, apiName string) {
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
					t.Fatalf("%s API expected tool '%s' not found. Found tools: %v", apiName, expectedTool, getKeysFromMap(toolsFound))
				}
			}

			if toolCallCount < 2 {
				t.Fatalf("%s API expected at least 2 tool calls, got %d", apiName, toolCallCount)
			}

			t.Logf("✅ %s API successfully found %d tool calls: %v", apiName, toolCallCount, getKeysFromMap(toolsFound))
		}

		// Validate both API responses
		if result.ChatCompletionsResponse != nil {
			validateChatMultipleToolCalls(result.ChatCompletionsResponse, "Chat Completions")
		}

		if result.ResponsesAPIResponse != nil {
			validateResponsesMultipleToolCalls(result.ResponsesAPIResponse, "Responses")
		}

		t.Logf("🎉 Both Chat Completions and Responses APIs passed MultipleToolCalls test!")
	})

	// Streaming Chat Completions with multiple tool calls (validates sequential indices 0, 1, 2, ...)
	t.Run("MultipleToolCallsStreamingChatCompletions", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		chatMessages := []schemas.ChatMessage{
			CreateBasicChatMessage("I need to know the weather in London and also calculate 15 * 23. Can you help with both in a single request?"),
		}
		chatWeatherTool := GetSampleChatTool(SampleToolTypeWeather)
		chatCalculatorTool := GetSampleChatTool(SampleToolTypeCalculate)

		request := &schemas.BifrostChatRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ChatModel,
			Input:    chatMessages,
			Params: &schemas.ChatParameters{
				MaxCompletionTokens: bifrost.Ptr(200),
				Tools:               []schemas.ChatTool{*chatWeatherTool, *chatCalculatorTool},
			},
			Fallbacks: testConfig.Fallbacks,
		}

		retryConfig := MultiToolRetryConfig(2, []string{"weather", "calculate"})
		retryContext := TestRetryContext{
			ScenarioName: "MultipleToolCallsStreamingChatCompletions",
			ExpectedBehavior: map[string]interface{}{
				"expected_tool_count": 2,
				"should_handle_both":  true,
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
				"model":    testConfig.ChatModel,
			},
		}

		responseChannel, err := WithStreamRetry(t, retryConfig, retryContext, func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			return client.ChatCompletionStreamRequest(bfCtx, request)
		})

		RequireNoError(t, err, "Chat completion stream with multiple tools failed")
		if responseChannel == nil {
			t.Fatal("Response channel should not be nil")
		}

		accumulator := NewStreamingToolCallAccumulator()
		var responseCount int

		for response := range responseChannel {
			if response == nil || response.BifrostChatResponse == nil {
				t.Fatal("Streaming response should not be nil")
			}
			responseCount++

			if response.BifrostChatResponse.Choices != nil {
				for _, choice := range response.BifrostChatResponse.Choices {
					if choice.ChatStreamResponseChoice != nil && choice.ChatStreamResponseChoice.Delta != nil {
						delta := choice.ChatStreamResponseChoice.Delta
						if len(delta.ToolCalls) > 0 {
							for _, toolCall := range delta.ToolCalls {
								accumulator.AccumulateChatToolCall(choice.Index, toolCall)
							}
						}
					}
				}
			}

			if responseCount > 500 {
				break
			}
		}

		if responseCount == 0 {
			t.Fatal("Should receive at least one streaming response")
		}

		finalToolCalls := accumulator.GetFinalChatToolCalls()

		if len(finalToolCalls) == 0 {
			t.Fatal("❌ No tool calls found in streaming response")
		}

		if len(finalToolCalls) < 2 {
			t.Fatalf("❌ Expected at least 2 tool calls, got %d", len(finalToolCalls))
		}

		toolsFound := make(map[string]bool)
		for i, tc := range finalToolCalls {
			if tc.Index != i {
				t.Fatalf("❌ Tool call %d has index %d, expected %d", i, tc.Index, i)
			}
			toolsFound[tc.Name] = true
		}

		for _, expected := range []string{"weather", "calculate"} {
			if !toolsFound[expected] {
				t.Fatalf("❌ Expected tool '%s' not found. Found: %v", expected, getKeysFromMap(toolsFound))
			}
		}

		if err := validateStreamingToolCalls(finalToolCalls, "Chat Completions"); err != nil {
			t.Fatalf("❌ %v", err)
		}
		t.Logf("✅ MultipleToolCallsStreamingChatCompletions passed with %d tool calls", len(finalToolCalls))
	})

	// Streaming Responses API with multiple tool calls
	t.Run("MultipleToolCallsStreamingResponses", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		responsesMessages := []schemas.ResponsesMessage{
			CreateBasicResponsesMessage("I need to know the weather in London and also calculate 15 * 23. Can you help with both in a single request?"),
		}
		responsesWeatherTool := GetSampleResponsesTool(SampleToolTypeWeather)
		responsesCalculatorTool := GetSampleResponsesTool(SampleToolTypeCalculate)

		request := &schemas.BifrostResponsesRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ChatModel,
			Input:    responsesMessages,
			Params: &schemas.ResponsesParameters{
				Tools: []schemas.ResponsesTool{*responsesWeatherTool, *responsesCalculatorTool},
			},
			Fallbacks: testConfig.Fallbacks,
		}

		retryConfig := MultiToolRetryConfig(2, []string{"weather", "calculate"})
		retryContext := TestRetryContext{
			ScenarioName: "MultipleToolCallsStreamingResponses",
			ExpectedBehavior: map[string]interface{}{
				"expected_tool_count": 2,
				"should_handle_both":  true,
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
				"model":    testConfig.ChatModel,
			},
		}

		validationResult := WithResponsesStreamValidationRetry(t, retryConfig, retryContext,
			func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
				bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
				return client.ResponsesStreamRequest(bfCtx, request)
			},
			func(responseChannel chan *schemas.BifrostStreamChunk) ResponsesStreamValidationResult {
				accumulator := NewStreamingToolCallAccumulator()
				var responseCount int
				streamCtx, cancel := context.WithTimeout(ctx, 200*time.Second)
				defer cancel()

				for {
					select {
					case response, ok := <-responseChannel:
						if !ok {
							goto streamComplete
						}
						if response == nil {
							return ResponsesStreamValidationResult{
								Passed: false,
								Errors: []string{"❌ Streaming response should not be nil"},
							}
						}
						responseCount++

						if response.BifrostResponsesStreamResponse == nil {
							errMsg := fmt.Sprintf("❌ Unexpected non-response chunk at chunk %d", responseCount)
							if response.BifrostError != nil {
								errMsg += fmt.Sprintf(" - error: %s", GetErrorMessage(response.BifrostError))
							}
							return ResponsesStreamValidationResult{
								Passed: false,
								Errors: []string{errMsg},
							}
						}

						streamResp := response.BifrostResponsesStreamResponse
						switch streamResp.Type {
						case schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta:
							var arguments *string
							if streamResp.Delta != nil {
								arguments = streamResp.Delta
							} else if streamResp.Arguments != nil {
								arguments = streamResp.Arguments
							}
							if arguments != nil {
								var callID, name, itemID *string
								if streamResp.ItemID != nil {
									itemID = streamResp.ItemID
								}
								if streamResp.Item != nil && streamResp.Item.ResponsesToolMessage != nil {
									callID = streamResp.Item.ResponsesToolMessage.CallID
									name = streamResp.Item.ResponsesToolMessage.Name
								}
								if streamResp.Item != nil && streamResp.Item.ID != nil {
									itemID = streamResp.Item.ID
								}
								accumulator.AccumulateResponsesToolCall(callID, name, arguments, itemID)
							}
						case schemas.ResponsesStreamResponseTypeOutputItemAdded:
							if streamResp.Item != nil && streamResp.Item.Type != nil &&
								*streamResp.Item.Type == schemas.ResponsesMessageTypeFunctionCall {
								var callID, name, itemID *string
								if streamResp.Item.ID != nil {
									itemID = streamResp.Item.ID
								}
								if streamResp.Item.ResponsesToolMessage != nil {
									callID = streamResp.Item.ResponsesToolMessage.CallID
									name = streamResp.Item.ResponsesToolMessage.Name
									if streamResp.Item.ResponsesToolMessage.Arguments != nil {
										accumulator.AccumulateResponsesToolCall(callID, name, streamResp.Item.ResponsesToolMessage.Arguments, itemID)
									}
								}
								if streamResp.Item.ResponsesToolMessage == nil || streamResp.Item.ResponsesToolMessage.Arguments == nil {
									accumulator.AccumulateResponsesToolCall(callID, name, nil, itemID)
								}
							}
						case schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone:
							if streamResp.Arguments != nil {
								var callID, name, itemID *string
								if streamResp.ItemID != nil {
									itemID = streamResp.ItemID
								}
								if streamResp.Item != nil && streamResp.Item.ResponsesToolMessage != nil {
									callID = streamResp.Item.ResponsesToolMessage.CallID
									name = streamResp.Item.ResponsesToolMessage.Name
								}
								if streamResp.Item != nil && streamResp.Item.ID != nil {
									itemID = streamResp.Item.ID
								}
								accumulator.AccumulateResponsesToolCall(callID, name, streamResp.Arguments, itemID)
							}
						}

						if responseCount > 500 {
							return ResponsesStreamValidationResult{
								Passed: false,
								Errors: []string{"❌ Received too many streaming chunks"},
							}
						}

					case <-streamCtx.Done():
						return ResponsesStreamValidationResult{
							Passed:       false,
							Errors:       []string{"❌ Timeout waiting for responses streaming response"},
							ReceivedData: responseCount > 0,
						}
					}
				}

			streamComplete:
				if responseCount == 0 {
					return ResponsesStreamValidationResult{
						Passed:       false,
						Errors:       []string{"❌ Stream closed without receiving any data"},
						ReceivedData: false,
					}
				}

				finalToolCalls := accumulator.GetFinalResponsesToolCalls()

				if len(finalToolCalls) == 0 {
					return ResponsesStreamValidationResult{
						Passed:       false,
						Errors:       []string{"❌ No tool calls found in streaming response"},
						ReceivedData: responseCount > 0,
					}
				}

				if len(finalToolCalls) < 2 {
					return ResponsesStreamValidationResult{
						Passed:       false,
						Errors:       []string{fmt.Sprintf("❌ Expected at least 2 tool calls, got %d", len(finalToolCalls))},
						ReceivedData: responseCount > 0,
					}
				}

				toolsFound := make(map[string]bool)
				var validationErrors []string
				for i, tc := range finalToolCalls {
					if tc.Name == "" || tc.Arguments == "" {
						validationErrors = append(validationErrors, fmt.Sprintf("Tool call %d missing required fields", i))
					}
					toolsFound[tc.Name] = true
				}

				for _, expected := range []string{"weather", "calculate"} {
					if !toolsFound[expected] {
						validationErrors = append(validationErrors, fmt.Sprintf("Expected tool '%s' not found. Found: %v", expected, getKeysFromMap(toolsFound)))
					}
				}

				if len(validationErrors) > 0 {
					return ResponsesStreamValidationResult{
						Passed:       false,
						Errors:       validationErrors,
						ReceivedData: responseCount > 0,
					}
				}

				if err := validateStreamingToolCalls(finalToolCalls, "Responses API"); err != nil {
					return ResponsesStreamValidationResult{
						Passed:       false,
						Errors:       []string{fmt.Sprintf("❌ %v", err)},
						ReceivedData: responseCount > 0,
					}
				}
				return ResponsesStreamValidationResult{
					Passed:       true,
					ReceivedData: responseCount > 0,
				}
			})

		if !validationResult.Passed {
			allErrors := append(validationResult.Errors, validationResult.StreamErrors...)
			t.Fatalf("❌ MultipleToolCallsStreamingResponses failed: %s", strings.Join(allErrors, "; "))
		}

		t.Logf("✅ MultipleToolCallsStreamingResponses passed")
	})
}
