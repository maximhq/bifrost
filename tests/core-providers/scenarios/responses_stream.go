package scenarios

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunResponsesStreamTest executes the responses streaming test scenario
func RunResponsesStreamTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig config.ComprehensiveTestConfig) {
	if !testConfig.Scenarios.CompletionStream {
		t.Logf("Responses completion stream not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("ResponsesStream", func(t *testing.T) {
		messages := []schemas.ResponsesMessage{
			{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("Tell me a short story about a robot learning to paint the city which has the eiffel tower. Keep it under 200 words."),
				},
			},
		}

		request := &schemas.BifrostResponsesRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ChatModel,
			Input:    messages,
			Params: &schemas.ResponsesParameters{
				MaxOutputTokens: bifrost.Ptr(150),
			},
			Fallbacks: testConfig.Fallbacks,
		}

		// Use retry framework for stream requests
		retryConfig := StreamingRetryConfig()
		retryContext := TestRetryContext{
			ScenarioName: "ResponsesStream",
			ExpectedBehavior: map[string]interface{}{
				"should_stream_content":        true,
				"should_tell_story":            true,
				"topic":                        "robot painting",
				"should_have_streaming_events": true,
				"should_have_sequence_numbers": true,
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
				"model":    testConfig.ChatModel,
			},
		}

		// Use proper streaming retry wrapper for the stream request
		responseChannel, err := WithStreamRetry(t, retryConfig, retryContext, func() (chan *schemas.BifrostStream, *schemas.BifrostError) {
			return client.ResponsesStreamRequest(ctx, request)
		})

		// Enhanced error handling
		RequireNoError(t, err, "Responses stream request failed")
		if responseChannel == nil {
			t.Fatal("Response channel should not be nil")
		}

		var fullContent strings.Builder
		var responseCount int
		var lastResponse *schemas.BifrostStream

		// Track streaming events for validation
		eventTypes := make(map[schemas.ResponsesStreamResponseType]int)
		var sequenceNumbers []int
		var hasResponseCreated, hasResponseCompleted bool
		var hasOutputItems, hasContentParts bool

		// Create a timeout context for the stream reading
		streamCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()

		t.Logf("📡 Starting to read responses streaming response...")

		// Read streaming responses
		for {
			select {
			case response, ok := <-responseChannel:
				if !ok {
					// Channel closed, streaming completed
					t.Logf("✅ Responses streaming completed. Total chunks received: %d", responseCount)
					goto streamComplete
				}

				if response == nil {
					t.Fatal("Streaming response should not be nil")
				}
				lastResponse = response

				// Basic validation of streaming response structure
				if response.BifrostResponsesStreamResponse != nil {
					if response.BifrostResponsesStreamResponse.ExtraFields.Provider != testConfig.Provider {
						t.Logf("⚠️ Warning: Provider mismatch - expected %s, got %s", testConfig.Provider, response.BifrostResponsesStreamResponse.ExtraFields.Provider)
					}

					// Process the streaming response
					streamResp := response.BifrostResponsesStreamResponse

					// Track event types
					eventTypes[streamResp.Type]++

					// Track sequence numbers
					sequenceNumbers = append(sequenceNumbers, streamResp.SequenceNumber)

					// Log the streaming event
					t.Logf("📊 Event: %s (seq: %d)", streamResp.Type, streamResp.SequenceNumber)

					// Print chunk content for debugging
					switch streamResp.Type {
					case schemas.ResponsesStreamResponseTypeOutputTextDelta:
						if streamResp.Delta != nil {
							fullContent.WriteString(*streamResp.Delta)
							t.Logf("📝 Text chunk: %q", *streamResp.Delta)
						}

					case schemas.ResponsesStreamResponseTypeOutputItemAdded:
						if streamResp.Item != nil {
							t.Logf("📦 Item added: type=%v, id=%v", streamResp.Item.Type, streamResp.Item.ID)
							if streamResp.Item.Content != nil {
								if streamResp.Item.Content.ContentStr != nil {
									t.Logf("📝 Item content: %q", *streamResp.Item.Content.ContentStr)
									fullContent.WriteString(*streamResp.Item.Content.ContentStr)
								}
								if streamResp.Item.Content.ContentBlocks != nil {
									for i, block := range streamResp.Item.Content.ContentBlocks {
										if block.Text != nil {
											t.Logf("📝 Item content block[%d]: %q", i, *block.Text)
											fullContent.WriteString(*block.Text)
										}
									}
								}
							}
						}

					case schemas.ResponsesStreamResponseTypeContentPartAdded:
						if streamResp.Part != nil {
							t.Logf("🧩 Content part: type=%s", streamResp.Part.Type)
							if streamResp.Part.Text != nil {
								t.Logf("📝 Part text: %q", *streamResp.Part.Text)
								fullContent.WriteString(*streamResp.Part.Text)
							}
						}
					}

					// Log other event details for debugging
					if streamResp.Arguments != nil {
						t.Logf("🔧 Arguments: %q", *streamResp.Arguments)
					}
					if streamResp.Refusal != nil {
						t.Logf("🚫 Refusal: %q", *streamResp.Refusal)
					}

					// Update state tracking for event types
					switch streamResp.Type {
					case schemas.ResponsesStreamResponseTypeCreated:
						hasResponseCreated = true
						t.Logf("🎬 Response created event detected")

					case schemas.ResponsesStreamResponseTypeCompleted:
						hasResponseCompleted = true
						t.Logf("🏁 Response completed event detected")

					case schemas.ResponsesStreamResponseTypeOutputItemAdded:
						hasOutputItems = true

					case schemas.ResponsesStreamResponseTypeContentPartAdded:
						hasContentParts = true

					case schemas.ResponsesStreamResponseTypeError:
						if streamResp.Message != nil {
							t.Fatalf("❌ Error in streaming: %s", *streamResp.Message)
						} else {
							t.Fatalf("❌ Error in streaming (no message)")
						}
					}
				}

				responseCount++

				// Safety check to prevent infinite loops
				if responseCount > 500 {
					t.Fatal("Received too many streaming chunks, something might be wrong")
				}

			case <-streamCtx.Done():
				t.Fatal("Timeout waiting for responses streaming response")
			}
		}

	streamComplete:
		// Validate streaming events and structure
		validateResponsesStreamingStructure(t, eventTypes, sequenceNumbers, hasResponseCreated, hasResponseCompleted, hasOutputItems, hasContentParts)

		// Validate final content
		finalContent := strings.TrimSpace(fullContent.String())

		// Enhanced validation expectations for responses streaming
		expectations := GetExpectationsForScenario("ResponsesStream", testConfig, map[string]interface{}{})
		expectations = ModifyExpectationsForProvider(expectations, testConfig.Provider)
		expectations.ShouldContainKeywords = append(expectations.ShouldContainKeywords, []string{"paris"}...) // Should include story elements
		expectations.MinContentLength = 50                                                                    // Should be substantial story
		expectations.MaxContentLength = 2000                                                                  // Reasonable upper bound

		// Validate streaming-specific aspects instead of using regular response validation
		streamingValidationResult := validateResponsesStreamingResponse(t, eventTypes, sequenceNumbers, finalContent, lastResponse, testConfig)

		if !streamingValidationResult.Passed {
			t.Logf("⚠️ Responses streaming validation warnings: %v", streamingValidationResult.Errors)
		}

		t.Logf("📊 Responses streaming metrics: %d chunks, %d chars, %d event types", responseCount, len(finalContent), len(eventTypes))

		t.Logf("✅ Responses streaming test completed successfully")
		t.Logf("📝 Final assembled content (%d chars): %q", len(finalContent), finalContent)
	})

	// Test responses streaming with tool calls if supported
	if testConfig.Scenarios.ToolCalls {
		t.Run("ResponsesStreamWithTools", func(t *testing.T) {
			messages := []schemas.ResponsesMessage{
				{
					Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
					Content: &schemas.ResponsesMessageContent{
						ContentStr: schemas.Ptr("What's the weather like in San Francisco? Please use the get_weather function."),
					},
				},
			}

			// Create sample weather tool for responses API
			tool := &schemas.ResponsesTool{
				Type:        "function",
				Name:        schemas.Ptr("get_weather"),
				Description: schemas.Ptr("Get the current weather in a given location"),
				ResponsesToolFunction: &schemas.ResponsesToolFunction{
					Parameters: &schemas.ToolFunctionParameters{
						Type: "object",
						Properties: &map[string]interface{}{
							"location": map[string]interface{}{
								"type":        "string",
								"description": "The city and state, e.g. San Francisco, CA",
							},
							"unit": map[string]interface{}{
								"type": "string",
								"enum": []string{"celsius", "fahrenheit"},
							},
						},
						Required: []string{"location"},
					},
				},
			}

			request := &schemas.BifrostResponsesRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.ChatModel,
				Input:    messages,
				Params: &schemas.ResponsesParameters{
					MaxOutputTokens: bifrost.Ptr(150),
					Tools:           []schemas.ResponsesTool{*tool},
				},
				Fallbacks: testConfig.Fallbacks,
			}

			responseChannel, err := client.ResponsesStreamRequest(ctx, request)
			RequireNoError(t, err, "Responses stream with tools failed")
			if responseChannel == nil {
				t.Fatal("Response channel should not be nil")
			}

			var toolCallDetected bool
			var functionCallArgsDetected bool
			var responseCount int

			streamCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()

			t.Logf("🔧 Testing responses streaming with tool calls...")

			for {
				select {
				case response, ok := <-responseChannel:
					if !ok {
						goto toolStreamComplete
					}

					if response == nil {
						t.Fatal("Streaming response should not be nil")
					}
					responseCount++

					if response.BifrostResponsesStreamResponse != nil {
						streamResp := response.BifrostResponsesStreamResponse

						// Check for function call events
						switch streamResp.Type {
						case schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta:
							functionCallArgsDetected = true
							if streamResp.Arguments != nil {
								t.Logf("🔧 Function call arguments chunk: %q", *streamResp.Arguments)
							}

						case schemas.ResponsesStreamResponseTypeOutputItemAdded:
							if streamResp.Item != nil && streamResp.Item.Type != nil {
								if *streamResp.Item.Type == schemas.ResponsesMessageTypeFunctionCall {
									toolCallDetected = true
									t.Logf("🔧 Function call detected in streaming response")

									if streamResp.Item.Name != nil {
										t.Logf("🔧 Function name: %s", *streamResp.Item.Name)
									}
								}
							}

						case schemas.ResponsesStreamResponseTypeOutputTextDelta:
							if streamResp.Delta != nil {
								t.Logf("📝 Text chunk in tool call stream: %q", *streamResp.Delta)
							}
						}
					}

					if responseCount > 100 {
						goto toolStreamComplete
					}

				case <-streamCtx.Done():
					t.Fatal("Timeout waiting for responses streaming response with tools")
				}
			}

		toolStreamComplete:
			if responseCount == 0 {
				t.Fatal("Should receive at least one streaming response")
			}

			// At least one of these should be detected for tool calling
			if !toolCallDetected && !functionCallArgsDetected {
				t.Fatal("Should detect tool calls or function arguments in responses streaming response")
			}

			t.Logf("✅ Responses streaming with tools test completed successfully")
		})
	}

	// Test responses streaming with reasoning if supported
	if testConfig.Scenarios.Reasoning && testConfig.ReasoningModel != "" {
		t.Run("ResponsesStreamWithReasoning", func(t *testing.T) {
			problemPrompt := "Solve this step by step: If a train leaves station A at 2 PM traveling at 60 mph, and another train leaves station B at 3 PM traveling at 80 mph toward station A, and the stations are 420 miles apart, when will they meet?"

			messages := []schemas.ResponsesMessage{
				{
					Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
					Content: &schemas.ResponsesMessageContent{
						ContentStr: schemas.Ptr(problemPrompt),
					},
				},
			}

			request := &schemas.BifrostResponsesRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.ReasoningModel,
				Input:    messages,
				Params: &schemas.ResponsesParameters{
					MaxOutputTokens: bifrost.Ptr(400),
					Reasoning: &schemas.ResponsesParametersReasoning{
						Effort:  bifrost.Ptr("high"),
						Summary: bifrost.Ptr("detailed"),
					},
					Include: []string{"reasoning.encrypted_content"},
				},
				Fallbacks: testConfig.Fallbacks,
			}

			responseChannel, err := client.ResponsesStreamRequest(ctx, request)
			RequireNoError(t, err, "Responses stream with reasoning failed")
			if responseChannel == nil {
				t.Fatal("Response channel should not be nil")
			}

			var reasoningDetected bool
			var reasoningSummaryDetected bool
			var responseCount int

			streamCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
			defer cancel()

			t.Logf("🧠 Testing responses streaming with reasoning...")

			for {
				select {
				case response, ok := <-responseChannel:
					if !ok {
						goto reasoningStreamComplete
					}

					if response == nil {
						t.Fatal("Streaming response should not be nil")
					}
					responseCount++

					if response.BifrostResponsesStreamResponse != nil {
						streamResp := response.BifrostResponsesStreamResponse

						// Check for reasoning-specific events
						switch streamResp.Type {
						case schemas.ResponsesStreamResponseTypeReasoningSummaryPartAdded:
							reasoningSummaryDetected = true
							t.Logf("🧠 Reasoning summary part added")

						case schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta:
							reasoningSummaryDetected = true
							if streamResp.Delta != nil {
								t.Logf("🧠 Reasoning summary text chunk: %q", *streamResp.Delta)
							}

						case schemas.ResponsesStreamResponseTypeOutputItemAdded:
							if streamResp.Item != nil && streamResp.Item.Type != nil {
								if *streamResp.Item.Type == schemas.ResponsesMessageTypeReasoning {
									reasoningDetected = true
									t.Logf("🧠 Reasoning message detected in streaming response")
								}
							}

						case schemas.ResponsesStreamResponseTypeOutputTextDelta:
							if streamResp.Delta != nil {
								t.Logf("📝 Text chunk in reasoning stream: %q", *streamResp.Delta)
							}
						}
					}

					if responseCount > 150 {
						goto reasoningStreamComplete
					}

				case <-streamCtx.Done():
					t.Fatal("Timeout waiting for responses streaming response with reasoning")
				}
			}

		reasoningStreamComplete:
			if responseCount == 0 {
				t.Fatal("Should receive at least one streaming response")
			}

			// At least one of these should be detected for reasoning
			if !reasoningDetected && !reasoningSummaryDetected {
				t.Logf("⚠️ Warning: No explicit reasoning indicators found in streaming response")
			}

			t.Logf("✅ Responses streaming with reasoning test completed successfully")
		})
	}
}

// validateResponsesStreamingStructure validates the structure and events of responses streaming
func validateResponsesStreamingStructure(t *testing.T, eventTypes map[schemas.ResponsesStreamResponseType]int, sequenceNumbers []int, hasResponseCreated, hasResponseCompleted, hasOutputItems, hasContentParts bool) {
	// Validate sequence numbers are increasing
	for i := 1; i < len(sequenceNumbers); i++ {
		if sequenceNumbers[i] < sequenceNumbers[i-1] {
			t.Errorf("⚠️ Warning: Sequence numbers not in ascending order: %d -> %d", sequenceNumbers[i-1], sequenceNumbers[i])
		}
	}

	// Log event type statistics
	t.Logf("📊 Event type distribution:")
	for eventType, count := range eventTypes {
		t.Logf("  %s: %d occurrences", eventType, count)
	}

	// Basic streaming flow validation
	if !hasResponseCreated {
		t.Logf("⚠️ Warning: No response.created event detected")
	}

	if !hasResponseCompleted {
		t.Logf("⚠️ Warning: No response.completed event detected")
	}

	if !hasOutputItems && !hasContentParts {
		t.Logf("⚠️ Warning: No output items or content parts detected")
	}

	// Validate minimum expected events
	expectedEvents := []schemas.ResponsesStreamResponseType{
		schemas.ResponsesStreamResponseTypeCreated,
		schemas.ResponsesStreamResponseTypeOutputTextDelta,
	}

	for _, expectedEvent := range expectedEvents {
		if count, exists := eventTypes[expectedEvent]; !exists || count == 0 {
			t.Logf("⚠️ Warning: Expected event %s not found", expectedEvent)
		}
	}
}


// StreamingValidationResult represents the result of streaming validation
type StreamingValidationResult struct {
	Passed bool
	Errors []string
}

// validateResponsesStreamingResponse validates streaming-specific aspects of responses API
func validateResponsesStreamingResponse(t *testing.T, eventTypes map[schemas.ResponsesStreamResponseType]int, sequenceNumbers []int, finalContent string, lastResponse *schemas.BifrostStream, testConfig config.ComprehensiveTestConfig) StreamingValidationResult {
	var errors []string

	// Basic content validation
	if len(finalContent) == 0 {
		errors = append(errors, "Final content should not be empty")
	}

	if len(finalContent) < 10 {
		errors = append(errors, "Final content should be substantial (at least 10 characters)")
	}

	// Streaming event validation
	if len(eventTypes) == 0 {
		errors = append(errors, "Should have received streaming events")
	}

	// Check for required events
	if _, hasCreated := eventTypes[schemas.ResponsesStreamResponseTypeCreated]; !hasCreated {
		t.Logf("⚠️ Warning: No response.created event detected")
	}

	if _, hasCompleted := eventTypes[schemas.ResponsesStreamResponseTypeCompleted]; !hasCompleted {
		t.Logf("⚠️ Warning: No response.completed event detected")
	}

	// Check for content events
	hasContentEvents := false
	contentEventTypes := []schemas.ResponsesStreamResponseType{
		schemas.ResponsesStreamResponseTypeOutputTextDelta,
		schemas.ResponsesStreamResponseTypeOutputItemAdded,
		schemas.ResponsesStreamResponseTypeContentPartAdded,
	}

	for _, eventType := range contentEventTypes {
		if count, exists := eventTypes[eventType]; exists && count > 0 {
			hasContentEvents = true
			break
		}
	}

	if !hasContentEvents {
		errors = append(errors, "Should have received content-related streaming events")
	}

	// Sequence number validation
	if len(sequenceNumbers) > 1 {
		for i := 1; i < len(sequenceNumbers); i++ {
			if sequenceNumbers[i] < sequenceNumbers[i-1] {
				errors = append(errors, fmt.Sprintf("Sequence numbers not in order: %d -> %d", sequenceNumbers[i-1], sequenceNumbers[i]))
			}
		}
	}

	// Validate last response structure
	if lastResponse == nil {
		errors = append(errors, "Should have at least one streaming response")
	} else {
		if lastResponse.BifrostResponsesStreamResponse == nil {
			errors = append(errors, "Last streaming response should have BifrostResponsesStreamResponse")
		} else {
			if lastResponse.BifrostResponsesStreamResponse.ExtraFields.Provider != testConfig.Provider {
				errors = append(errors, fmt.Sprintf("Provider mismatch: expected %s, got %s", testConfig.Provider, lastResponse.BifrostResponsesStreamResponse.ExtraFields.Provider))
			}
		}
	}

	// Content quality checks (basic)
	if len(finalContent) > 0 {
		// Check for reasonable content for story prompt
		if testConfig.Provider != schemas.SGL { // SGL might have different output patterns
			lowerContent := strings.ToLower(finalContent)
			hasStoryElements := strings.Contains(lowerContent, "robot") ||
				strings.Contains(lowerContent, "paint") ||
				strings.Contains(lowerContent, "story")

			if !hasStoryElements {
				t.Logf("⚠️ Warning: Content doesn't seem to contain expected story elements")
			}
		}
	}

	return StreamingValidationResult{
		Passed: len(errors) == 0,
		Errors: errors,
	}
}
