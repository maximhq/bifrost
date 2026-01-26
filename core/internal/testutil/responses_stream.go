package testutil

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

// RunResponsesStreamTest executes the responses streaming test scenario
func RunResponsesStreamTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.CompletionStream {
		t.Logf("Responses completion stream not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("ResponsesStream", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

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

		// Use retry framework with validation retry for stream requests
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

		// Use validation retry wrapper that validates stream content and retries on validation failures
		validationResult := WithResponsesStreamValidationRetry(t, retryConfig, retryContext,
			func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
				bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
				return client.ResponsesStreamRequest(bfCtx, request)
			},
			func(responseChannel chan *schemas.BifrostStreamChunk) ResponsesStreamValidationResult {
				var fullContent strings.Builder
				var responseCount int
				var lastResponse *schemas.BifrostStreamChunk

				// Track streaming events for validation
				eventTypes := make(map[schemas.ResponsesStreamResponseType]int)
				var sequenceNumbers []int
				var hasResponseCreated, hasResponseCompleted bool
				var hasOutputItems, hasContentParts bool

				// Chunk timing tracking for batch detection
				var chunkTimings []chunkTiming
				var lastChunkTime time.Time

				// Create a timeout context for the stream reading
				streamCtx, cancel := context.WithTimeout(ctx, 200*time.Second)
				defer cancel()

				t.Logf("📡 Starting to read responses streaming response...")

				// Read streaming responses
				for {
					select {
					case response, ok := <-responseChannel:
						if !ok {
							// Channel closed, streaming completed
							t.Logf("✅ Responses streaming completed. Total chunks received: %d", responseCount)
							// If no data was received, this is a retryable error
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

						// Record chunk timing
						now := time.Now()
						var timeSincePrev time.Duration
						if responseCount > 0 {
							timeSincePrev = now.Sub(lastChunkTime)
						}
						chunkTimings = append(chunkTimings, chunkTiming{
							index:         responseCount,
							arrivalTime:   now,
							timeSincePrev: timeSincePrev,
						})
						lastChunkTime = now

						lastResponse = DeepCopyBifrostStreamChunk(response)

						// Basic validation of streaming response structure
						if response.BifrostResponsesStreamResponse != nil {
							if response.BifrostResponsesStreamResponse.ExtraFields.Provider != testConfig.Provider {
								t.Logf("⚠️ Warning: Provider mismatch - expected %s, got %s", testConfig.Provider, response.BifrostResponsesStreamResponse.ExtraFields.Provider)
							}

							// Log latency for each chunk (can be 0 for inter-chunks)
							t.Logf("📊 Chunk %d latency: %d ms", responseCount+1, response.BifrostResponsesStreamResponse.ExtraFields.Latency)

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

						responseCount++

						// Safety check to prevent infinite loops
						if responseCount > 500 {
							return ResponsesStreamValidationResult{
								Passed: false,
								Errors: []string{"❌ Received too many streaming chunks, something might be wrong"},
							}
						}

					case <-streamCtx.Done():
						return ResponsesStreamValidationResult{
							Passed: false,
							Errors: []string{"❌ Timeout waiting for responses streaming response"},
						}
					}
				}

			streamComplete:
				// Check for batched streaming
				if isBatched, batchMsg := detectBatchedStream(chunkTimings, 5); isBatched {
					return ResponsesStreamValidationResult{
						Passed:       false,
						Errors:       []string{fmt.Sprintf("❌ Streaming validation failed: %s", batchMsg)},
						ReceivedData: responseCount > 0,
					}
				}

				// Validate streaming events and structure
				structureErrors := validateResponsesStreamingStructure(t, eventTypes, sequenceNumbers, hasResponseCreated, hasResponseCompleted, hasOutputItems, hasContentParts)

				// Validate final content
				finalContent := strings.TrimSpace(fullContent.String())

				// Enhanced validation expectations for responses streaming
				expectations := GetExpectationsForScenario("ResponsesStream", testConfig, map[string]interface{}{})
				expectations = ModifyExpectationsForProvider(expectations, testConfig.Provider)
				expectations.ShouldContainKeywords = append(expectations.ShouldContainKeywords, []string{"paris"}...) // Should include story elements

				// Validate streaming-specific aspects
				streamingValidationResult := validateResponsesStreamingResponse(t, eventTypes, sequenceNumbers, finalContent, lastResponse, testConfig)

				t.Logf("📊 Responses streaming metrics: %d chunks, %d chars, %d event types", responseCount, len(finalContent), len(eventTypes))
				t.Logf("📝 Final assembled content (%d chars): %q", len(finalContent), finalContent)

				// Combine structure errors with streaming validation errors
				allErrors := append(structureErrors, streamingValidationResult.Errors...)
				passed := len(allErrors) == 0 && streamingValidationResult.Passed

				// Convert to ResponsesStreamValidationResult
				return ResponsesStreamValidationResult{
					Passed:       passed,
					Errors:       allErrors,
					ReceivedData: responseCount > 0,
					LastLatency:  0, // Can be extracted from lastResponse if needed
				}
			})

		// Check validation result and fail test if validation failed after all retries
		if !validationResult.Passed {
			allErrors := append(validationResult.Errors, validationResult.StreamErrors...)
			errorMsg := strings.Join(allErrors, "; ")
			if !strings.Contains(errorMsg, "❌") {
				errorMsg = fmt.Sprintf("❌ %s", errorMsg)
			}
			t.Fatalf("❌ Responses streaming validation failed after retries: %s", errorMsg)
		}

		t.Logf("✅ Responses streaming test completed successfully")
	})

	// Test responses streaming with tool calls if supported
	if testConfig.Scenarios.ToolCalls {
		t.Run("ResponsesStreamWithTools", func(t *testing.T) {
			if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
				t.Parallel()
			}

			messages := []schemas.ResponsesMessage{
				{
					Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
					Content: &schemas.ResponsesMessageContent{
						ContentStr: schemas.Ptr("What's the weather like in San Francisco in celsius? Please use the get_weather function."),
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
						Properties: &schemas.OrderedMap{
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

			// Use retry framework for stream requests with tools
			retryConfig := StreamingRetryConfig()
			retryContext := TestRetryContext{
				ScenarioName: "ResponsesStreamWithTools",
				ExpectedBehavior: map[string]interface{}{
					"should_stream_content":  true,
					"should_have_tool_calls": true,
					"tool_name":              "get_weather",
				},
				TestMetadata: map[string]interface{}{
					"provider": testConfig.Provider,
					"model":    testConfig.ChatModel,
					"tools":    true,
				},
			}

			// Use proper streaming retry wrapper for the stream request
			responseChannel, err := WithStreamRetry(t, retryConfig, retryContext, func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
				bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
				return client.ResponsesStreamRequest(bfCtx, request)
			})

			RequireNoError(t, err, "Responses stream with tools failed")
			if responseChannel == nil {
				t.Fatal("Response channel should not be nil")
			}

			var toolCallDetected bool
			var functionCallArgsDetected bool
			var responseCount int

			// Chunk timing tracking for batch detection
			var chunkTimings []chunkTiming
			var lastChunkTime time.Time

			streamCtx, cancel := context.WithTimeout(ctx, 200*time.Second)
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

					// Record chunk timing
					now := time.Now()
					var timeSincePrev time.Duration
					if responseCount > 0 {
						timeSincePrev = now.Sub(lastChunkTime)
					}
					chunkTimings = append(chunkTimings, chunkTiming{
						index:         responseCount,
						arrivalTime:   now,
						timeSincePrev: timeSincePrev,
					})
					lastChunkTime = now

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
			// Check for batched streaming
			if isBatched, batchMsg := detectBatchedStream(chunkTimings, 5); isBatched {
				t.Fatalf("❌ Streaming validation failed: %s", batchMsg)
			}

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
			if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
				t.Parallel()
			}

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
					MaxOutputTokens: bifrost.Ptr(1800),
					Reasoning: &schemas.ResponsesParametersReasoning{
						Effort: bifrost.Ptr("high"),
						// Summary: bifrost.Ptr("detailed"),
					},
					Include: []string{"reasoning.encrypted_content"},
				},
				Fallbacks: testConfig.Fallbacks,
			}

			// Use retry framework for stream requests with reasoning
			retryConfig := StreamingRetryConfig()
			retryContext := TestRetryContext{
				ScenarioName: "ResponsesStreamWithReasoning",
				ExpectedBehavior: map[string]interface{}{
					"should_stream_reasoning":      true,
					"should_have_reasoning_events": true,
					"problem_type":                 "mathematical",
				},
				TestMetadata: map[string]interface{}{
					"provider":  testConfig.Provider,
					"model":     testConfig.ReasoningModel,
					"reasoning": true,
				},
			}

			// Use proper streaming retry wrapper for the stream request
			responseChannel, err := WithStreamRetry(t, retryConfig, retryContext, func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
				bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
				return client.ResponsesStreamRequest(bfCtx, request)
			})

			RequireNoError(t, err, "Responses stream with reasoning failed")
			if responseChannel == nil {
				t.Fatal("Response channel should not be nil")
			}

			var reasoningDetected bool
			var reasoningSummaryDetected bool
			var responseCount int

			// Chunk timing tracking for batch detection
			var chunkTimings []chunkTiming
			var lastChunkTime time.Time

			streamCtx, cancel := context.WithTimeout(ctx, 200*time.Second)
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

					// Record chunk timing
					now := time.Now()
					var timeSincePrev time.Duration
					if responseCount > 0 {
						timeSincePrev = now.Sub(lastChunkTime)
					}
					chunkTimings = append(chunkTimings, chunkTiming{
						index:         responseCount,
						arrivalTime:   now,
						timeSincePrev: timeSincePrev,
					})
					lastChunkTime = now

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
			// Check for batched streaming
			if isBatched, batchMsg := detectBatchedStream(chunkTimings, 5); isBatched {
				t.Fatalf("❌ Streaming validation failed: %s", batchMsg)
			}

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

	// Test responses streaming lifecycle events
	t.Run("ResponsesStreamLifecycle", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		messages := []schemas.ResponsesMessage{
			{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("Say hello in exactly 5 words."),
				},
			},
		}

		request := &schemas.BifrostResponsesRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ChatModel,
			Input:    messages,
			Params: &schemas.ResponsesParameters{
				MaxOutputTokens: bifrost.Ptr(50),
			},
			Fallbacks: testConfig.Fallbacks,
		}

		// Use retry framework for stream requests lifecycle test
		retryConfig := StreamingRetryConfig()
		retryContext := TestRetryContext{
			ScenarioName: "ResponsesStreamLifecycle",
			ExpectedBehavior: map[string]interface{}{
				"should_have_lifecycle_events": true,
				"should_have_sequence_numbers": true,
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
				"model":    testConfig.ChatModel,
			},
		}

		// Use validation retry wrapper that validates lifecycle events and retries on validation failures
		validationResult := WithResponsesStreamValidationRetry(t, retryConfig, retryContext,
			func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
				bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
				return client.ResponsesStreamRequest(bfCtx, request)
			},
			func(responseChannel chan *schemas.BifrostStreamChunk) ResponsesStreamValidationResult {
				// Track lifecycle events
				var hasResponseCreated, hasResponseInProgress, hasResponseCompleted bool
				var hasOutputItemAdded bool
				var hasContentPartAdded, hasContentPartDone bool
				var hasOutputTextDelta, hasOutputTextDone bool
				var hasOutputItemDone bool

				var outputItemAddedSeq, contentPartAddedSeq, firstTextDeltaSeq int
				var outputTextDoneSeq, contentPartDoneSeq, outputItemDoneSeq int
				var textDeltaCount int

				// Chunk timing tracking for batch detection
				var chunkTimings []chunkTiming
				var lastChunkTime time.Time

				streamCtx, cancel := context.WithTimeout(ctx, 200*time.Second)
				defer cancel()

				t.Logf("🔄 Testing responses streaming lifecycle events...")

				responseCount := 0
				for {
					select {
					case response, ok := <-responseChannel:
						if !ok {
							// Channel closed, streaming completed
							goto lifecycleComplete
						}

						if response == nil {
							return ResponsesStreamValidationResult{
								Passed: false,
								Errors: []string{"❌ Streaming response should not be nil"},
							}
						}

						// Record chunk timing
						now := time.Now()
						var timeSincePrev time.Duration
						if responseCount > 0 {
							timeSincePrev = now.Sub(lastChunkTime)
						}
						chunkTimings = append(chunkTimings, chunkTiming{
							index:         responseCount,
							arrivalTime:   now,
							timeSincePrev: timeSincePrev,
						})
						lastChunkTime = now

						responseCount++

						if response.BifrostResponsesStreamResponse != nil {
							streamResp := response.BifrostResponsesStreamResponse
							seqNum := streamResp.SequenceNumber

							switch streamResp.Type {
							case schemas.ResponsesStreamResponseTypeCreated:
								hasResponseCreated = true
								t.Logf("✅ Event %d: response.created", seqNum)

							case schemas.ResponsesStreamResponseTypeInProgress:
								hasResponseInProgress = true
								t.Logf("✅ Event %d: response.in_progress", seqNum)

							case schemas.ResponsesStreamResponseTypeOutputItemAdded:
								hasOutputItemAdded = true
								outputItemAddedSeq = seqNum
								t.Logf("✅ Event %d: response.output_item.added", seqNum)

							case schemas.ResponsesStreamResponseTypeContentPartAdded:
								hasContentPartAdded = true
								contentPartAddedSeq = seqNum
								t.Logf("✅ Event %d: response.content_part.added", seqNum)

							case schemas.ResponsesStreamResponseTypeOutputTextDelta:
								hasOutputTextDelta = true
								if textDeltaCount == 0 {
									firstTextDeltaSeq = seqNum
								}
								textDeltaCount++
								if streamResp.Delta != nil {
									t.Logf("✅ Event %d: response.output_text.delta (chunk %d): %q", seqNum, textDeltaCount, *streamResp.Delta)
								}

							case schemas.ResponsesStreamResponseTypeOutputTextDone:
								hasOutputTextDone = true
								outputTextDoneSeq = seqNum
								t.Logf("✅ Event %d: response.output_text.done", seqNum)

							case schemas.ResponsesStreamResponseTypeContentPartDone:
								hasContentPartDone = true
								contentPartDoneSeq = seqNum
								t.Logf("✅ Event %d: response.content_part.done", seqNum)

							case schemas.ResponsesStreamResponseTypeOutputItemDone:
								hasOutputItemDone = true
								outputItemDoneSeq = seqNum
								t.Logf("✅ Event %d: response.output_item.done", seqNum)

							case schemas.ResponsesStreamResponseTypeCompleted:
								hasResponseCompleted = true
								t.Logf("✅ Event %d: response.completed", seqNum)

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

						// Safety check to prevent infinite loops
						if responseCount > 300 {
							goto lifecycleComplete
						}

					case <-streamCtx.Done():
						return ResponsesStreamValidationResult{
							Passed:       false,
							Errors:       []string{"❌ Timeout waiting for responses streaming lifecycle events"},
							ReceivedData: responseCount > 0,
						}
					}
				}

			lifecycleComplete:
				if responseCount == 0 {
					return ResponsesStreamValidationResult{
						Passed:       false,
						Errors:       []string{"❌ Stream closed without receiving any data"},
						ReceivedData: false,
					}
				}

				// Check for batched streaming
				if isBatched, batchMsg := detectBatchedStream(chunkTimings, 5); isBatched {
					return ResponsesStreamValidationResult{
						Passed:       false,
						Errors:       []string{fmt.Sprintf("❌ Streaming validation failed: %s", batchMsg)},
						ReceivedData: responseCount > 0,
					}
				}

				// Validate lifecycle events are present
				t.Logf("\n📋 Lifecycle Event Validation:")
				t.Logf("  response.created: %v", hasResponseCreated)
				t.Logf("  response.in_progress: %v", hasResponseInProgress)
				t.Logf("  response.output_item.added: %v (seq: %d)", hasOutputItemAdded, outputItemAddedSeq)
				t.Logf("  response.content_part.added: %v (seq: %d)", hasContentPartAdded, contentPartAddedSeq)
				t.Logf("  response.output_text.delta: %v (count: %d, first seq: %d)", hasOutputTextDelta, textDeltaCount, firstTextDeltaSeq)
				t.Logf("  response.output_text.done: %v (seq: %d)", hasOutputTextDone, outputTextDoneSeq)
				t.Logf("  response.content_part.done: %v (seq: %d)", hasContentPartDone, contentPartDoneSeq)
				t.Logf("  response.output_item.done: %v (seq: %d)", hasOutputItemDone, outputItemDoneSeq)
				t.Logf("  response.completed: %v", hasResponseCompleted)

				// Collect validation errors
				var validationErrors []string

				// Validate required lifecycle events
				if !hasResponseCreated {
					validationErrors = append(validationErrors, "❌ Missing required event: response.created")
				}
				if !hasResponseInProgress {
					validationErrors = append(validationErrors, "❌ Missing required event: response.in_progress")
				}
				if !hasOutputItemAdded {
					validationErrors = append(validationErrors, "❌ Missing required event: response.output_item.added")
				}
				if !hasContentPartAdded {
					validationErrors = append(validationErrors, "❌ Missing required event: response.content_part.added")
				}
				if !hasOutputTextDelta {
					validationErrors = append(validationErrors, "❌ Missing required event: response.output_text.delta")
				}
				if !hasOutputTextDone {
					validationErrors = append(validationErrors, "❌ Missing required event: response.output_text.done")
				}
				if !hasContentPartDone {
					validationErrors = append(validationErrors, "❌ Missing required event: response.content_part.done")
				}
				if !hasOutputItemDone {
					validationErrors = append(validationErrors, "❌ Missing required event: response.output_item.done")
				}
				if !hasResponseCompleted {
					validationErrors = append(validationErrors, "❌ Missing required event: response.completed")
				}

				// Validate event ordering
				if hasOutputItemAdded && hasContentPartAdded {
					if contentPartAddedSeq > outputItemAddedSeq {
						t.Logf("✅ Event ordering: output_item.added (%d) -> content_part.added (%d)", outputItemAddedSeq, contentPartAddedSeq)
					} else {
						validationErrors = append(validationErrors, fmt.Sprintf("❌ Invalid event ordering: content_part.added (%d) should come after output_item.added (%d)", contentPartAddedSeq, outputItemAddedSeq))
					}
				}

				if hasContentPartAdded && hasOutputTextDelta {
					if firstTextDeltaSeq > contentPartAddedSeq {
						t.Logf("✅ Event ordering: content_part.added (%d) -> output_text.delta (%d)", contentPartAddedSeq, firstTextDeltaSeq)
					} else {
						validationErrors = append(validationErrors, fmt.Sprintf("❌ Invalid event ordering: output_text.delta (%d) should come after content_part.added (%d)", firstTextDeltaSeq, contentPartAddedSeq))
					}
				}

				if hasOutputTextDone && hasContentPartDone && hasOutputItemDone {
					if outputTextDoneSeq < contentPartDoneSeq && contentPartDoneSeq < outputItemDoneSeq {
						t.Logf("✅ Event ordering: output_text.done (%d) -> content_part.done (%d) -> output_item.done (%d)", outputTextDoneSeq, contentPartDoneSeq, outputItemDoneSeq)
					} else {
						validationErrors = append(validationErrors, fmt.Sprintf("❌ Invalid event ordering: expected output_text.done (%d) -> content_part.done (%d) -> output_item.done (%d)", outputTextDoneSeq, contentPartDoneSeq, outputItemDoneSeq))
					}
				}

				// Final validation
				allEventsPresent := hasResponseCreated && hasResponseInProgress && hasOutputItemAdded &&
					hasContentPartAdded && hasOutputTextDelta && hasOutputTextDone &&
					hasContentPartDone && hasOutputItemDone && hasResponseCompleted

				if allEventsPresent {
					t.Logf("✅ All required lifecycle events are present and properly ordered")
				} else {
					// Errors already collected above
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

		// Check validation result and fail test if validation failed after all retries
		if !validationResult.Passed {
			allErrors := append(validationResult.Errors, validationResult.StreamErrors...)
			errorMsg := strings.Join(allErrors, "; ")
			if !strings.Contains(errorMsg, "❌") {
				errorMsg = fmt.Sprintf("❌ %s", errorMsg)
			}
			t.Fatalf("❌ Responses streaming lifecycle validation failed after retries: %s", errorMsg)
		}

		t.Logf("✅ Responses streaming lifecycle test completed")
	})
}

// validateResponsesStreamingStructure validates the structure and events of responses streaming
// Returns a list of validation errors (empty if validation passes)
func validateResponsesStreamingStructure(t *testing.T, eventTypes map[schemas.ResponsesStreamResponseType]int, sequenceNumbers []int, hasResponseCreated, hasResponseCompleted, hasOutputItems, hasContentParts bool) []string {
	var errors []string

	// Validate sequence numbers are increasing
	for i := 1; i < len(sequenceNumbers); i++ {
		if sequenceNumbers[i] < sequenceNumbers[i-1] {
			errorMsg := fmt.Sprintf("⚠️ Warning: Sequence numbers not in ascending order: %d -> %d", sequenceNumbers[i-1], sequenceNumbers[i])
			t.Logf("%s", errorMsg)
			errors = append(errors, errorMsg)
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

	return errors
}

// StreamingValidationResult represents the result of streaming validation
type StreamingValidationResult struct {
	Passed bool
	Errors []string
}

// validateResponsesStreamingResponse validates streaming-specific aspects of responses API
func validateResponsesStreamingResponse(t *testing.T, eventTypes map[schemas.ResponsesStreamResponseType]int, sequenceNumbers []int, finalContent string, lastResponse *schemas.BifrostStreamChunk, testConfig ComprehensiveTestConfig) StreamingValidationResult {
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

	// Validate latency is present in the last chunk (total latency)
	if lastResponse != nil && lastResponse.BifrostResponsesStreamResponse != nil {
		if lastResponse.BifrostResponsesStreamResponse.ExtraFields.Latency <= 0 {
			errors = append(errors, fmt.Sprintf("Last streaming chunk missing latency information (got %d ms)", lastResponse.BifrostResponsesStreamResponse.ExtraFields.Latency))
		} else {
			t.Logf("✅ Total streaming latency: %d ms", lastResponse.BifrostResponsesStreamResponse.ExtraFields.Latency)
		}
	}

	return StreamingValidationResult{
		Passed: len(errors) == 0,
		Errors: errors,
	}
}
