package testutil

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// Test schema with nullable enum and multi-type fields (the problematic cases that were fixed)
var structuredOutputSchema = map[string]interface{}{
	"type": "object",
	"properties": map[string]interface{}{
		"action": map[string]interface{}{
			"type":        "string",
			"enum":        []string{"continue", "transition"},
			"description": "The action to take",
		},
		"target_node_id": map[string]interface{}{
			"type":        []interface{}{"string", "null"},
			"description": "The ID of the node to transition to. Required when action is 'transition', null/empty when action is 'continue'",
			"enum":        []string{"NODE-0", "NODE-1", "NODE-2", ""},
		},
		"priority": map[string]interface{}{
			"type":        []interface{}{"string", "integer"},
			"description": "Priority level - can be a number (1-10) or a string label (low/medium/high)",
			"enum":        []interface{}{"low", "medium", "high", 1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
		},
		"reason": map[string]interface{}{
			"type":        "string",
			"description": "Explanation for the decision",
		},
	},
	"required":             []string{"action", "target_node_id", "priority", "reason"},
	"additionalProperties": false,
}

// RunStructuredOutputChatTest tests structured outputs with Chat Completions API (non-streaming)
func RunStructuredOutputChatTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.StructuredOutputs {
		t.Logf("Structured outputs not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("StructuredOutputChat", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		// Test Case 1: target_node_id should have a string value
		t.Run("WithTargetNode", func(t *testing.T) {
			testStructuredOutputChatWithValue(t, client, ctx, testConfig, true)
		})

		// Test Case 2: target_node_id should be null
		t.Run("WithNullTargetNode", func(t *testing.T) {
			testStructuredOutputChatWithValue(t, client, ctx, testConfig, false)
		})
	})
}

func testStructuredOutputChatWithValue(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig, expectValue bool) {
	var chatMessages []schemas.ChatMessage
	if expectValue {
		chatMessages = []schemas.ChatMessage{
			CreateBasicChatMessage("You are a workflow manager. User says: 'Transition to NODE-1'. Analyze this and return: action='transition', target_node_id='NODE-1' (NOT null or empty), and priority as number 5. Provide reasoning."),
		}
	} else {
		chatMessages = []schemas.ChatMessage{
			CreateBasicChatMessage("You are a workflow manager. User says: 'Continue with current task'. Analyze this and return: action='continue', target_node_id=null (must be null, not a string), and priority='medium'. Provide reasoning."),
		}
	}

	// Use retry framework
	retryConfig := GetTestRetryConfigForScenario("StructuredOutputChat", testConfig)
	retryContext := TestRetryContext{
		ScenarioName: "StructuredOutputChat",
		ExpectedBehavior: map[string]interface{}{
			"should_return_valid_json": true,
			"should_match_schema":      true,
		},
		TestMetadata: map[string]interface{}{
			"provider": testConfig.Provider,
			"model":    testConfig.ChatModel,
		},
	}

	chatRetryConfig := ChatRetryConfig{
		MaxAttempts: retryConfig.MaxAttempts,
		BaseDelay:   retryConfig.BaseDelay,
		MaxDelay:    retryConfig.MaxDelay,
		Conditions:  []ChatRetryCondition{},
		OnRetry:     retryConfig.OnRetry,
		OnFinalFail: retryConfig.OnFinalFail,
	}

	chatOperation := func() (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		// Add Anthropic beta header for structured outputs if model contains "claude"
		reqCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		if strings.Contains(strings.ToLower(testConfig.ChatModel), "claude") && testConfig.Provider != schemas.Vertex {
			extraHeaders := map[string][]string{
				"anthropic-beta": {"structured-outputs-2025-11-13"},
			}
			reqCtx.SetValue(schemas.BifrostContextKeyExtraHeaders, extraHeaders)
		}

		chatReq := &schemas.BifrostChatRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ChatModel,
			Input:    chatMessages,
			Params: &schemas.ChatParameters{
				MaxCompletionTokens: bifrost.Ptr(5000),
				ResponseFormat: func() *interface{} {
					var format interface{} = map[string]interface{}{
						"type": "json_schema",
						"json_schema": map[string]interface{}{
							"name":   "decision_schema",
							"strict": true,
							"schema": structuredOutputSchema,
						},
					}
					return &format
				}(),
			},
			Fallbacks: testConfig.Fallbacks,
		}
		return client.ChatCompletionRequest(reqCtx, chatReq)
	}

	expectations := GetExpectationsForScenario("StructuredOutputChat", testConfig, map[string]interface{}{})
	expectations = ModifyExpectationsForProvider(expectations, testConfig.Provider)

	chatResponse, chatError := WithChatTestRetry(t, chatRetryConfig, retryContext, expectations, "StructuredOutputChat", chatOperation)

	if chatError != nil {
		t.Fatalf("❌ Chat Completions API with structured output failed: %s", GetErrorMessage(chatError))
	}

	// Validate the response is valid JSON matching our schema
	if chatResponse != nil {
		content := GetChatContent(chatResponse)
		t.Logf("📝 Structured output response: %s", content)

		// Assert content is non-empty
		if content == "" {
			t.Fatalf("❌ Content should not be empty for structured output")
		}

		// For Bedrock: verify no tool calls leaked through (response_format was properly converted)
		if testConfig.Provider == schemas.Bedrock {
			if len(chatResponse.Choices) > 0 {
				choice := chatResponse.Choices[0]
				if choice.ChatNonStreamResponseChoice != nil && choice.Message != nil && choice.Message.ChatAssistantMessage != nil {
					if len(choice.Message.ChatAssistantMessage.ToolCalls) > 0 {
						t.Fatalf("❌ Bedrock: structured output should not contain tool calls, got %d tool calls", len(choice.Message.ChatAssistantMessage.ToolCalls))
					}
				}
			}
			t.Logf("✅ Bedrock: no tool calls in response (response_format properly converted)")
		}

		// Parse and validate the JSON
		var result map[string]interface{}
		if err := json.Unmarshal([]byte(content), &result); err != nil {
			t.Fatalf("❌ Failed to parse structured output as JSON: %v", err)
		}

		// Validate required fields
		if action, ok := result["action"].(string); !ok || action == "" {
			t.Fatalf("❌ Missing or invalid 'action' field in structured output")
		} else {
			t.Logf("✅ Action: %s", action)
		}

		if reason, ok := result["reason"].(string); !ok || reason == "" {
			t.Fatalf("❌ Missing or invalid 'reason' field in structured output")
		} else {
			t.Logf("✅ Reason: %s", reason)
		}

		// target_node_id can be string or null - validate based on expectation
		targetNodeID, hasTargetNode := result["target_node_id"]
		if !hasTargetNode {
			t.Fatalf("❌ Missing 'target_node_id' field in structured output")
		}

		if expectValue {
			// Should be a non-empty string
			if targetStr, ok := targetNodeID.(string); !ok || targetStr == "" {
				t.Fatalf("❌ Expected 'target_node_id' to be a non-empty string, got: %v (type: %T)", targetNodeID, targetNodeID)
			} else {
				t.Logf("✅ Target Node ID has value: %s", targetStr)
			}
		} else {
			// Should be null
			if targetNodeID != nil {
				t.Logf("⚠️  Expected 'target_node_id' to be null, got: %v (type: %T) - this is acceptable if provider returns empty string", targetNodeID, targetNodeID)
			} else {
				t.Logf("✅ Target Node ID is null (as expected)")
			}
		}

		// priority can be string or integer
		if priority, ok := result["priority"]; ok {
			t.Logf("✅ Priority: %v (type: %T)", priority, priority)
		} else {
			t.Fatalf("❌ Missing 'priority' field in structured output")
		}

		t.Logf("🎉 Chat Completions API with structured output test passed!")
	}
}

// RunStructuredOutputChatStreamTest tests structured outputs with Chat Completions API (streaming)
func RunStructuredOutputChatStreamTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.StructuredOutputs || !testConfig.Scenarios.CompletionStream {
		t.Logf("Structured outputs streaming not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("StructuredOutputChatStream", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		// Test with null target_node_id
		chatMessages := []schemas.ChatMessage{
			CreateBasicChatMessage("You are a workflow manager. User says: 'Continue with current task'. Analyze this and return: action='continue', target_node_id=null (must be null), and priority=3 (as integer). Provide reasoning."),
		}

		// Add Anthropic beta header for structured outputs if model contains "claude"
		reqCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		if strings.Contains(strings.ToLower(testConfig.ChatModel), "claude") && testConfig.Provider != schemas.Vertex {
			extraHeaders := map[string][]string{
				"anthropic-beta": {"structured-outputs-2025-11-13"},
			}
			reqCtx.SetValue(schemas.BifrostContextKeyExtraHeaders, extraHeaders)
		}

		request := &schemas.BifrostChatRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ChatModel,
			Input:    chatMessages,
			Params: &schemas.ChatParameters{
				MaxCompletionTokens: bifrost.Ptr(5000),
				ResponseFormat: func() *interface{} {
					var format interface{} = map[string]interface{}{
						"type": "json_schema",
						"json_schema": map[string]interface{}{
							"name":   "decision_schema",
							"strict": true,
							"schema": structuredOutputSchema,
						},
					}
					return &format
				}(),
			},
			Fallbacks: testConfig.Fallbacks,
		}

		retryConfig := StreamingRetryConfig()
		retryContext := TestRetryContext{
			ScenarioName: "StructuredOutputChatStream",
			ExpectedBehavior: map[string]interface{}{
				"should_stream_json":  true,
				"should_match_schema": true,
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
				"model":    testConfig.ChatModel,
			},
		}

		responseChannel, err := WithStreamRetry(t, retryConfig, retryContext, func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
			return client.ChatCompletionStreamRequest(reqCtx, request)
		})

		RequireNoError(t, err, "Chat streaming with structured output failed")
		if responseChannel == nil {
			t.Fatal("Response channel should not be nil")
		}

		var fullContent strings.Builder
		var responseCount int
		var toolCallCount int // Track tool calls for Bedrock assertion

		streamCtx, cancel := context.WithTimeout(ctx, 200*time.Second)
		defer cancel()

		t.Logf("📡 Starting to read structured output streaming response...")

		for {
			select {
			case response, ok := <-responseChannel:
				if !ok {
					goto streamComplete
				}

				if response == nil {
					t.Fatal("❌ Streaming response should not be nil")
				}
				responseCount++

				if response.BifrostChatResponse != nil {
					if len(response.BifrostChatResponse.Choices) > 0 {
						choice := response.BifrostChatResponse.Choices[0]
						if choice.Delta != nil && choice.Delta.Content != nil {
							fullContent.WriteString(*choice.Delta.Content)
						}
						// Track tool calls for Bedrock assertion
						if choice.Delta != nil && len(choice.Delta.ToolCalls) > 0 {
							toolCallCount += len(choice.Delta.ToolCalls)
						}
					}
				}

				if responseCount > 500 {
					goto streamComplete
				}

			case <-streamCtx.Done():
				t.Fatal("❌ Timeout waiting for structured output streaming response")
			}
		}

	streamComplete:
		if responseCount == 0 {
			t.Fatal("❌ Should receive at least one streaming response")
		}

		finalContent := strings.TrimSpace(fullContent.String())
		t.Logf("📝 Assembled structured output (%d chars): %s", len(finalContent), finalContent)

		// Assert content is non-empty
		if finalContent == "" {
			t.Fatalf("❌ Content should not be empty for structured output")
		}

		// For Bedrock: verify no tool calls leaked through (response_format was properly converted)
		if testConfig.Provider == schemas.Bedrock {
			if toolCallCount > 0 {
				t.Fatalf("❌ Bedrock: structured output streaming should not contain tool calls, got %d tool call deltas", toolCallCount)
			}
			t.Logf("✅ Bedrock: no tool calls in streaming response (response_format properly converted)")
		}

		// Validate the assembled content is valid JSON matching our schema
		var result map[string]interface{}
		if err := json.Unmarshal([]byte(finalContent), &result); err != nil {
			t.Fatalf("❌ Failed to parse assembled structured output as JSON: %v", err)
		}

		// Validate required fields
		if action, ok := result["action"].(string); !ok || action == "" {
			t.Fatalf("❌ Missing or invalid 'action' field in structured output")
		} else {
			t.Logf("✅ Action: %s", action)
		}

		if reason, ok := result["reason"].(string); !ok || reason == "" {
			t.Fatalf("❌ Missing or invalid 'reason' field in structured output")
		} else {
			t.Logf("✅ Reason: %s", reason)
		}

		// target_node_id validation - should be null for "continue" action
		targetNodeID, hasTargetNode := result["target_node_id"]
		if !hasTargetNode {
			t.Fatalf("❌ Missing 'target_node_id' field in structured output")
		}
		if targetNodeID != nil {
			t.Logf("⚠️  Expected 'target_node_id' to be null, got: %v (type: %T)", targetNodeID, targetNodeID)
		} else {
			t.Logf("✅ Target Node ID is null (as expected)")
		}

		// priority can be string or integer (from JSON unmarshaling, numbers become float64)
		if priority, ok := result["priority"]; ok {
			t.Logf("✅ Priority: %v (type: %T)", priority, priority)
		} else {
			t.Fatalf("❌ Missing 'priority' field in structured output")
		}

		t.Logf("🎉 Chat streaming with structured output test passed!")
	})
}

// RunStructuredOutputResponsesTest tests structured outputs with Responses API (non-streaming)
func RunStructuredOutputResponsesTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.StructuredOutputs {
		t.Logf("Structured outputs not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("StructuredOutputResponses", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		// Test with string value for target_node_id
		responsesMessages := []schemas.ResponsesMessage{
			CreateBasicResponsesMessage("You are a workflow manager. User says: 'Transition to the first node'. Analyze this and return: action='transition', target_node_id='NODE-0' (NOT null), priority='high' (as string). Provide reasoning."),
		}

		// Add Anthropic beta header for structured outputs if model contains "claude"
		reqCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		if strings.Contains(strings.ToLower(testConfig.ChatModel), "claude") && testConfig.Provider != schemas.Vertex {
			extraHeaders := map[string][]string{
				"anthropic-beta": {"structured-outputs-2025-11-13"},
			}
			reqCtx.SetValue(schemas.BifrostContextKeyExtraHeaders, extraHeaders)
		}

		retryConfig := GetTestRetryConfigForScenario("StructuredOutputResponses", testConfig)
		retryContext := TestRetryContext{
			ScenarioName: "StructuredOutputResponses",
			ExpectedBehavior: map[string]interface{}{
				"should_return_valid_json": true,
				"should_match_schema":      true,
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
				"model":    testConfig.ChatModel,
			},
		}

		responsesRetryConfig := ResponsesRetryConfig{
			MaxAttempts: retryConfig.MaxAttempts,
			BaseDelay:   retryConfig.BaseDelay,
			MaxDelay:    retryConfig.MaxDelay,
			Conditions:  []ResponsesRetryCondition{},
			OnRetry:     retryConfig.OnRetry,
			OnFinalFail: retryConfig.OnFinalFail,
		}

		responsesOperation := func() (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
			typeStr := "object"
			props := structuredOutputSchema["properties"].(map[string]interface{})
			additionalProps := structuredOutputSchema["additionalProperties"].(bool)
			responsesReq := &schemas.BifrostResponsesRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.ChatModel,
				Input:    responsesMessages,
				Params: &schemas.ResponsesParameters{
					MaxOutputTokens: bifrost.Ptr(5000),
					Text: &schemas.ResponsesTextConfig{
						Format: &schemas.ResponsesTextConfigFormat{
							Type: "json_schema",
							Name: bifrost.Ptr("decision_schema"),
							JSONSchema: &schemas.ResponsesTextConfigFormatJSONSchema{
								Type:       &typeStr,
								Properties: &props,
								Required:   structuredOutputSchema["required"].([]string),
								AdditionalProperties: &schemas.AdditionalPropertiesStruct{
									AdditionalPropertiesBool: &additionalProps,
								},
							},
						},
					},
				},
				Fallbacks: testConfig.Fallbacks,
			}
			return client.ResponsesRequest(reqCtx, responsesReq)
		}

		expectations := GetExpectationsForScenario("StructuredOutputResponses", testConfig, map[string]interface{}{})
		expectations = ModifyExpectationsForProvider(expectations, testConfig.Provider)

		responsesResponse, responsesError := WithResponsesTestRetry(t, responsesRetryConfig, retryContext, expectations, "StructuredOutputResponses", responsesOperation)

		if responsesError != nil {
			t.Fatalf("❌ Responses API with structured output failed: %s", GetErrorMessage(responsesError))
		}

		// Validate the response is valid JSON matching our schema
		if responsesResponse != nil {
			content := GetResponsesContent(responsesResponse)
			t.Logf("📝 Structured output response: %s", content)

			// Assert content is non-empty
			if content == "" {
				t.Fatalf("❌ Content should not be empty for structured output")
			}

			// For Bedrock: verify no function_call items leaked through (response_format was properly converted)
			if testConfig.Provider == schemas.Bedrock {
				for _, outputItem := range responsesResponse.Output {
					if outputItem.Type != nil && *outputItem.Type == schemas.ResponsesMessageTypeFunctionCall {
						t.Fatalf("❌ Bedrock: structured output should not contain function_call items")
					}
				}
				t.Logf("✅ Bedrock: no function_call items in response (response_format properly converted)")
			}

			// Parse and validate the JSON
			var result map[string]interface{}
			if err := json.Unmarshal([]byte(content), &result); err != nil {
				t.Fatalf("❌ Failed to parse structured output as JSON: %v", err)
			}

			// Validate required fields
			if action, ok := result["action"].(string); !ok || action == "" {
				t.Fatalf("❌ Missing or invalid 'action' field in structured output")
			} else {
				t.Logf("✅ Action: %s", action)
			}

			if reason, ok := result["reason"].(string); !ok || reason == "" {
				t.Fatalf("❌ Missing or invalid 'reason' field in structured output")
			} else {
				t.Logf("✅ Reason: %s", reason)
			}

			// target_node_id validation - should be a string value for "transition" action
			targetNodeID, hasTargetNode := result["target_node_id"]
			if !hasTargetNode {
				t.Fatalf("❌ Missing 'target_node_id' field in structured output")
			}
			if targetStr, ok := targetNodeID.(string); !ok || targetStr == "" {
				t.Fatalf("❌ Expected 'target_node_id' to be a non-empty string, got: %v (type: %T)", targetNodeID, targetNodeID)
			} else {
				t.Logf("✅ Target Node ID has value: %s", targetStr)
			}

			// priority can be string or integer
			if priority, ok := result["priority"]; ok {
				t.Logf("✅ Priority: %v (type: %T)", priority, priority)
			} else {
				t.Fatalf("❌ Missing 'priority' field in structured output")
			}

			t.Logf("🎉 Responses API with structured output test passed!")
		}
	})
}

// RunStructuredOutputResponsesStreamTest tests structured outputs with Responses API (streaming)
func RunStructuredOutputResponsesStreamTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.StructuredOutputs || !testConfig.Scenarios.CompletionStream {
		t.Logf("Structured outputs streaming not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("StructuredOutputResponsesStream", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		// Test with null target_node_id
		responsesMessages := []schemas.ResponsesMessage{
			{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("You are a workflow manager. User says: 'Continue current task'. Analyze this and return: action='continue', target_node_id=null (must be null), priority=7 (as integer). Provide reasoning."),
				},
			},
		}

		// Add Anthropic beta header for structured outputs if model contains "claude"
		reqCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		if strings.Contains(strings.ToLower(testConfig.ChatModel), "claude") && testConfig.Provider != schemas.Vertex {
			extraHeaders := map[string][]string{
				"anthropic-beta": {"structured-outputs-2025-11-13"},
			}
			reqCtx.SetValue(schemas.BifrostContextKeyExtraHeaders, extraHeaders)
		}

		typeStr := "object"
		props := structuredOutputSchema["properties"].(map[string]interface{})
		additionalProps := structuredOutputSchema["additionalProperties"].(bool)
		request := &schemas.BifrostResponsesRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ChatModel,
			Input:    responsesMessages,
			Params: &schemas.ResponsesParameters{
				MaxOutputTokens: bifrost.Ptr(5000),
				Text: &schemas.ResponsesTextConfig{
					Format: &schemas.ResponsesTextConfigFormat{
						Type: "json_schema",
						Name: bifrost.Ptr("decision_schema"),
						JSONSchema: &schemas.ResponsesTextConfigFormatJSONSchema{
							Type:       &typeStr,
							Properties: &props,
							Required:   structuredOutputSchema["required"].([]string),
							AdditionalProperties: &schemas.AdditionalPropertiesStruct{
								AdditionalPropertiesBool: &additionalProps,
							},
						},
					},
				},
			},
			Fallbacks: testConfig.Fallbacks,
		}

		retryConfig := StreamingRetryConfig()
		retryContext := TestRetryContext{
			ScenarioName: "StructuredOutputResponsesStream",
			ExpectedBehavior: map[string]interface{}{
				"should_stream_json":  true,
				"should_match_schema": true,
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
				"model":    testConfig.ChatModel,
			},
		}

		// Use validation retry wrapper
		validationResult := WithResponsesStreamValidationRetry(t, retryConfig, retryContext,
			func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
				return client.ResponsesStreamRequest(reqCtx, request)
			},
			func(responseChannel chan *schemas.BifrostStreamChunk) ResponsesStreamValidationResult {
				var fullContent strings.Builder
				var responseCount int
				var functionCallEventCount int // Track function call events for Bedrock assertion

				streamCtx, cancel := context.WithTimeout(ctx, 200*time.Second)
				defer cancel()

				t.Logf("📡 Starting to read structured output streaming response...")

				for {
					select {
					case response, ok := <-responseChannel:
						if !ok {
							if responseCount == 0 {
								return ResponsesStreamValidationResult{
									Passed:       false,
									Errors:       []string{"❌ Stream closed without receiving any data"},
									ReceivedData: false,
								}
							}
							goto streamComplete
						}

						if response == nil {
							return ResponsesStreamValidationResult{
								Passed: false,
								Errors: []string{"❌ Streaming response should not be nil"},
							}
						}
						responseCount++

						if response.BifrostResponsesStreamResponse != nil {
							streamResp := response.BifrostResponsesStreamResponse

							// Track function call events for Bedrock assertion
							if streamResp.Type == schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta ||
								streamResp.Type == schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone {
								functionCallEventCount++
							}

							switch streamResp.Type {
							case schemas.ResponsesStreamResponseTypeOutputTextDelta:
								if streamResp.Delta != nil {
									fullContent.WriteString(*streamResp.Delta)
								}

							case schemas.ResponsesStreamResponseTypeOutputItemAdded:
								if streamResp.Item != nil && streamResp.Item.Content != nil {
									// Check ContentBlocks first
									if len(streamResp.Item.Content.ContentBlocks) > 0 {
										for _, block := range streamResp.Item.Content.ContentBlocks {
											if block.Type == schemas.ResponsesOutputMessageContentTypeText && block.Text != nil {
												fullContent.WriteString(*block.Text)
											}
										}
									} else if streamResp.Item.Content.ContentStr != nil {
										// Fallback to ContentStr
										fullContent.WriteString(*streamResp.Item.Content.ContentStr)
									}
								}
								// Track function call output items for Bedrock assertion
								if streamResp.Item != nil && streamResp.Item.Type != nil && *streamResp.Item.Type == schemas.ResponsesMessageTypeFunctionCall {
									functionCallEventCount++
								}

							case schemas.ResponsesStreamResponseTypeContentPartAdded:
								if streamResp.Part != nil && streamResp.Part.Text != nil {
									fullContent.WriteString(*streamResp.Part.Text)
								}

							case schemas.ResponsesStreamResponseTypeError:
								errorMsg := "unknown error"
								if streamResp.Message != nil {
									errorMsg = *streamResp.Message
								}
								return ResponsesStreamValidationResult{
									Passed: false,
									Errors: []string{fmt.Sprintf("❌ Error in streaming: %s", errorMsg)},
								}
							}
						}

						if responseCount > 500 {
							goto streamComplete
						}

					case <-streamCtx.Done():
						return ResponsesStreamValidationResult{
							Passed:       false,
							Errors:       []string{"❌ Timeout waiting for structured output streaming response"},
							ReceivedData: responseCount > 0,
						}
					}
				}

			streamComplete:
				finalContent := strings.TrimSpace(fullContent.String())
				t.Logf("📝 Assembled structured output (%d chars): %s", len(finalContent), finalContent)

				// Assert content is non-empty
				if finalContent == "" {
					return ResponsesStreamValidationResult{
						Passed:       false,
						Errors:       []string{"❌ Content should not be empty for structured output"},
						ReceivedData: responseCount > 0,
					}
				}

				// For Bedrock: verify no function_call events leaked through (response_format was properly converted)
				if testConfig.Provider == schemas.Bedrock {
					if functionCallEventCount > 0 {
						return ResponsesStreamValidationResult{
							Passed:       false,
							Errors:       []string{fmt.Sprintf("❌ Bedrock: structured output streaming should not contain function_call events, got %d", functionCallEventCount)},
							ReceivedData: responseCount > 0,
						}
					}
					t.Logf("✅ Bedrock: no function_call events in streaming response (response_format properly converted)")
				}

				// Validate the assembled content is valid JSON matching our schema
				var result map[string]interface{}
				if err := json.Unmarshal([]byte(finalContent), &result); err != nil {
					return ResponsesStreamValidationResult{
						Passed: false,
						Errors: []string{fmt.Sprintf("❌ Failed to parse assembled structured output as JSON: %v", err)},
					}
				}

				// Validate required fields
				var validationErrors []string

				if action, ok := result["action"].(string); !ok || action == "" {
					validationErrors = append(validationErrors, "❌ Missing or invalid 'action' field in structured output")
				} else {
					t.Logf("✅ Action: %s", action)
				}

				if reason, ok := result["reason"].(string); !ok || reason == "" {
					validationErrors = append(validationErrors, "❌ Missing or invalid 'reason' field in structured output")
				} else {
					t.Logf("✅ Reason: %s", reason)
				}

				// target_node_id validation - should be null for "continue" action
				targetNodeID, hasTargetNode := result["target_node_id"]
				if !hasTargetNode {
					validationErrors = append(validationErrors, "❌ Missing 'target_node_id' field in structured output")
				} else {
					if targetNodeID != nil {
						t.Logf("⚠️  Expected 'target_node_id' to be null, got: %v (type: %T)", targetNodeID, targetNodeID)
					} else {
						t.Logf("✅ Target Node ID is null (as expected)")
					}
				}

				if priority, ok := result["priority"]; !ok {
					validationErrors = append(validationErrors, "❌ Missing 'priority' field in structured output")
				} else {
					t.Logf("✅ Priority: %v (type: %T)", priority, priority)
				}

				if len(validationErrors) > 0 {
					return ResponsesStreamValidationResult{
						Passed:       false,
						Errors:       validationErrors,
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
			errorMsg := strings.Join(allErrors, "; ")
			if !strings.Contains(errorMsg, "❌") {
				errorMsg = fmt.Sprintf("❌ %s", errorMsg)
			}
			t.Fatalf("❌ Responses streaming with structured output validation failed: %s", errorMsg)
		}

		t.Logf("🎉 Responses streaming with structured output test passed!")
	})
}
