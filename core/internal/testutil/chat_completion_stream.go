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

// chunkTiming tracks the arrival time of each streaming chunk
type chunkTiming struct {
	index         int
	arrivalTime   time.Time
	timeSincePrev time.Duration
}

// detectBatchedStream checks if chunks arrived in a batched manner rather than streaming individually
// Returns true if streaming appears batched, with an error message
func detectBatchedStream(chunkTimings []chunkTiming, minChunks int) (bool, string) {
	if len(chunkTimings) < minChunks {
		return false, "" // Not enough data to determine
	}

	var nearInstantCount int
	threshold := 1 * time.Millisecond

	// Start from index 1 (skip first chunk - no previous reference)
	for i := 1; i < len(chunkTimings); i++ {
		if chunkTimings[i].timeSincePrev < threshold {
			nearInstantCount++
		}
	}

	totalIntervals := len(chunkTimings) - 1
	ratio := float64(nearInstantCount) / float64(totalIntervals)

	// Threshold: >80% of chunks arriving near-instantly indicates batching
	if ratio > 0.8 {
		return true, fmt.Sprintf(
			"chunks appear batched: %d/%d (%.0f%%) arrived within %v of each other",
			nearInstantCount, totalIntervals, ratio*100, threshold,
		)
	}

	return false, ""
}

// RunChatCompletionStreamTest executes the chat completion stream test scenario
func RunChatCompletionStreamTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.CompletionStream {
		t.Logf("Chat completion stream not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("ChatCompletionStream", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		messages := []schemas.ChatMessage{
			CreateBasicChatMessage("Tell me a short story about a robot learning to paint the city which has the eiffel tower. Keep it under 200 words and include the city's name."),
		}

		request := &schemas.BifrostChatRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ChatModel,
			Input:    messages,
			Params: &schemas.ChatParameters{
				MaxCompletionTokens: bifrost.Ptr(150),
			},
			Fallbacks: testConfig.Fallbacks,
		}

		// Use retry framework for stream requests
		retryConfig := StreamingRetryConfig()
		retryContext := TestRetryContext{
			ScenarioName: "ChatCompletionStream",
			ExpectedBehavior: map[string]interface{}{
				"should_stream_content": true,
				"should_tell_story":     true,
				"topic":                 "robot painting",
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
				"model":    testConfig.ChatModel,
			},
		}

		// Use proper streaming retry wrapper for the stream request
		responseChannel, err := WithStreamRetry(t, retryConfig, retryContext, func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			return client.ChatCompletionStreamRequest(bfCtx, request)
		})

		// Enhanced error handling
		RequireNoError(t, err, "Chat completion stream request failed")
		if responseChannel == nil {
			t.Fatal("Response channel should not be nil")
		}

		var fullContent strings.Builder
		var responseCount int
		var lastResponse *schemas.BifrostStreamChunk

		// Chunk timing tracking for batch detection
		var chunkTimings []chunkTiming
		var lastChunkTime time.Time

		// Create a timeout context for the stream reading
		streamCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()

		t.Logf("📡 Starting to read streaming response...")

		// Read streaming responses
		for {
			select {
			case response, ok := <-responseChannel:
				if !ok {
					// Channel closed, streaming completed
					t.Logf("✅ Streaming completed. Total chunks received: %d", responseCount)
					goto streamComplete
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

				lastResponse = DeepCopyBifrostStreamChunk(response)

				// Basic validation of streaming response structure
				if response.BifrostChatResponse != nil {
					if response.BifrostChatResponse.ExtraFields.Provider != testConfig.Provider {
						t.Logf("⚠️ Warning: Provider mismatch - expected %s, got %s", testConfig.Provider, response.BifrostChatResponse.ExtraFields.Provider)
					}
					if response.BifrostChatResponse.ID == "" {
						t.Logf("⚠️ Warning: Response ID is empty")
					}

					// Log latency for each chunk (can be 0 for inter-chunks)
					t.Logf("📊 Chunk %d latency: %d ms", responseCount+1, response.BifrostChatResponse.ExtraFields.Latency)

					// Process each choice in the response
					for _, choice := range response.BifrostChatResponse.Choices {
						// Validate that this is a stream response
						if choice.ChatStreamResponseChoice == nil {
							t.Logf("⚠️ Warning: Stream response choice is nil for choice %d", choice.Index)
							continue
						}
						if choice.ChatNonStreamResponseChoice != nil {
							t.Logf("⚠️ Warning: Non-stream response choice should be nil in streaming response")
						}

						// Get content from delta
						if choice.ChatStreamResponseChoice != nil && choice.ChatStreamResponseChoice.Delta != nil {
							delta := choice.ChatStreamResponseChoice.Delta
							if delta.Content != nil {
								fullContent.WriteString(*delta.Content)
							}

							// Log role if present (usually in first chunk)
							if delta.Role != nil {
								t.Logf("🤖 Role: %s", *delta.Role)
							}

							// Check finish reason if present
							if choice.FinishReason != nil {
								t.Logf("🏁 Finish reason: %s", *choice.FinishReason)
							}
						}
					}
				}

				responseCount++

				// Safety check to prevent infinite loops in case of issues
				if responseCount > 500 {
					t.Fatal("Received too many streaming chunks, something might be wrong")
				}

			case <-streamCtx.Done():
				t.Fatal("Timeout waiting for streaming response")
			}
		}

	streamComplete:
		// Check for batched streaming
		if isBatched, batchMsg := detectBatchedStream(chunkTimings, 5); isBatched {
			t.Fatalf("❌ Streaming validation failed: %s", batchMsg)
		}

		// Validate final streaming response
		finalContent := strings.TrimSpace(fullContent.String())

		// Create a consolidated response for validation
		consolidatedResponse := &schemas.BifrostChatResponse{
			Choices: []schemas.BifrostResponseChoice{
				{
					Index: 0,
					ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
						Message: &schemas.ChatMessage{
							Role: schemas.ChatMessageRoleAssistant,
							Content: &schemas.ChatMessageContent{
								ContentStr: &finalContent,
							},
						},
					},
				},
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				Provider: testConfig.Provider,
			},
		}

		// Copy usage and other metadata from last response if available
		if lastResponse != nil && lastResponse.BifrostChatResponse != nil {
			consolidatedResponse.Usage = lastResponse.BifrostChatResponse.Usage
			consolidatedResponse.Model = lastResponse.BifrostChatResponse.Model
			consolidatedResponse.ID = lastResponse.BifrostChatResponse.ID
			consolidatedResponse.Created = lastResponse.BifrostChatResponse.Created

			// Copy finish reason from last choice if available
			if len(lastResponse.BifrostChatResponse.Choices) > 0 && lastResponse.BifrostChatResponse.Choices[0].FinishReason != nil {
				consolidatedResponse.Choices[0].FinishReason = lastResponse.BifrostChatResponse.Choices[0].FinishReason
			}
			consolidatedResponse.ExtraFields.Latency = lastResponse.BifrostChatResponse.ExtraFields.Latency
		}

		// Enhanced validation expectations for streaming
		expectations := GetExpectationsForScenario("ChatCompletionStream", testConfig, map[string]interface{}{})
		expectations = ModifyExpectationsForProvider(expectations, testConfig.Provider)
		expectations.ShouldContainAnyOf = append(expectations.ShouldContainAnyOf, []string{"paris"}...) // Should include story elements                                                         // Reasonable upper bound

		// Validate the consolidated streaming response
		validationResult := ValidateChatResponse(t, consolidatedResponse, nil, expectations, "ChatCompletionStream")

		// Basic streaming validation
		if responseCount == 0 {
			t.Fatal("Should receive at least one streaming response")
		}

		if finalContent == "" {
			t.Fatal("Final content should not be empty")
		}

		if len(finalContent) < 10 {
			t.Fatal("Final content should be substantial")
		}

		if !validationResult.Passed {
			t.Fatalf("❌ Streaming validation failed: %v", validationResult.Errors)
		}

		t.Logf("📊 Streaming metrics: %d chunks, %d chars", responseCount, len(finalContent))

		t.Logf("✅ Streaming test completed successfully")
		t.Logf("📝 Final content (%d chars)", len(finalContent))
	})

	// Test streaming with tool calls if supported
	if testConfig.Scenarios.ToolCalls {
		t.Run("ChatCompletionStreamWithTools", func(t *testing.T) {
			if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
				t.Parallel()
			}

			messages := []schemas.ChatMessage{
				CreateBasicChatMessage("What's the weather like in San Francisco in celsius? Please use the get_weather function."),
			}

			tool := GetSampleChatTool(SampleToolTypeWeather)

			request := &schemas.BifrostChatRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.ChatModel,
				Input:    messages,
				Params: &schemas.ChatParameters{
					MaxCompletionTokens: bifrost.Ptr(150),
					Tools:               []schemas.ChatTool{*tool},
				},
				Fallbacks: testConfig.Fallbacks,
			}

			// Use retry framework for stream requests with tools
			retryConfig := StreamingRetryConfig()
			retryContext := TestRetryContext{
				ScenarioName: "ChatCompletionStreamWithTools",
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

			// Use validation retry wrapper that includes stream reading and validation
			validationResult := WithChatStreamValidationRetry(
				t,
				retryConfig,
				retryContext,
				func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
					bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
					return client.ChatCompletionStreamRequest(bfCtx, request)
				},
				func(responseChannel chan *schemas.BifrostStreamChunk) ChatStreamValidationResult {
					var toolCallDetected bool
					var responseCount int
					var streamErrors []string

					// Chunk timing tracking for batch detection
					var chunkTimings []chunkTiming
					var lastChunkTime time.Time

					streamCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
					defer cancel()

					t.Logf("🔧 Testing streaming with tool calls...")

					for {
						select {
						case response, ok := <-responseChannel:
							if !ok {
								goto toolStreamComplete
							}

							if response == nil || response.BifrostChatResponse == nil {
								streamErrors = append(streamErrors, "❌ Streaming response should not be nil")
								continue
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

							if response.BifrostChatResponse.Choices != nil {
								for _, choice := range response.BifrostChatResponse.Choices {
									if choice.ChatStreamResponseChoice != nil && choice.ChatStreamResponseChoice.Delta != nil {
										delta := choice.ChatStreamResponseChoice.Delta

										// Check for tool calls in delta
										if len(delta.ToolCalls) > 0 {
											toolCallDetected = true
											t.Logf("🔧 Tool call detected in streaming response")

											for _, toolCall := range delta.ToolCalls {
												if toolCall.Function.Name != nil {
													t.Logf("🔧 Tool: %s", *toolCall.Function.Name)
													if toolCall.Function.Arguments != "" {
														t.Logf("🔧 Args: %s", toolCall.Function.Arguments)
													}
												}
											}
										}
									}
								}
							}

							if responseCount > 100 {
								goto toolStreamComplete
							}

						case <-streamCtx.Done():
							streamErrors = append(streamErrors, "❌ Timeout waiting for streaming response with tools")
							goto toolStreamComplete
						}
					}

				toolStreamComplete:
					var errors []string
					if responseCount == 0 {
						errors = append(errors, "❌ Should receive at least one streaming response")
					}
					if !toolCallDetected {
						errors = append(errors, fmt.Sprintf("❌ Should detect tool calls in streaming response (received %d chunks but no tool calls)", responseCount))
					}
					// Check for batched streaming
					if isBatched, batchMsg := detectBatchedStream(chunkTimings, 5); isBatched {
						errors = append(errors, fmt.Sprintf("❌ Streaming validation failed: %s", batchMsg))
					}
					if len(streamErrors) > 0 {
						errors = append(errors, streamErrors...)
					}

					return ChatStreamValidationResult{
						Passed:           len(errors) == 0,
						Errors:           errors,
						ReceivedData:     responseCount > 0,
						StreamErrors:     streamErrors,
						ToolCallDetected: toolCallDetected,
						ResponseCount:    responseCount,
					}
				},
			)

			// Check validation result
			if !validationResult.Passed {
				allErrors := append(validationResult.Errors, validationResult.StreamErrors...)
				t.Fatalf("❌ Chat completion stream with tools validation failed after retries: %s", strings.Join(allErrors, "; "))
			}

			if validationResult.ResponseCount == 0 {
				t.Fatalf("❌ Should receive at least one streaming response")
			}
			if !validationResult.ToolCallDetected {
				t.Fatalf("❌ Should detect tool calls in streaming response (received %d chunks but no tool calls)", validationResult.ResponseCount)
			}
			t.Logf("✅ Streaming with tools test completed successfully")
		})
	}

	// Test chat completion streaming with reasoning if supported
	if testConfig.Scenarios.Reasoning && testConfig.ReasoningModel != "" {
		t.Run("ChatCompletionStreamWithReasoning", func(t *testing.T) {
			if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
				t.Parallel()
			}

			problemPrompt := "Solve this step by step: If a train leaves station A at 2 PM traveling at 60 mph, and another train leaves station B at 3 PM traveling at 80 mph toward station A, and the stations are 420 miles apart, when will they meet?"

			messages := []schemas.ChatMessage{
				CreateBasicChatMessage(problemPrompt),
			}

			request := &schemas.BifrostChatRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.ReasoningModel,
				Input:    messages,
				Params: &schemas.ChatParameters{
					MaxCompletionTokens: bifrost.Ptr(1800),
					Reasoning: &schemas.ChatReasoning{
						Effort:    bifrost.Ptr("high"),
						MaxTokens: bifrost.Ptr(1500),
					},
				},
				Fallbacks: testConfig.Fallbacks,
			}

			// Use retry framework for stream requests with reasoning
			retryConfig := StreamingRetryConfig()
			retryContext := TestRetryContext{
				ScenarioName: "ChatCompletionStreamWithReasoning",
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
				return client.ChatCompletionStreamRequest(bfCtx, request)
			})

			RequireNoError(t, err, "Chat completion stream with reasoning failed")
			if responseChannel == nil {
				t.Fatal("Response channel should not be nil")
			}

			var reasoningDetected bool
			var reasoningDetailsDetected bool
			var reasoningTokensDetected bool
			var responseCount int

			// Chunk timing tracking for batch detection
			var chunkTimings []chunkTiming
			var lastChunkTime time.Time

			streamCtx, cancel := context.WithTimeout(ctx, 200*time.Second)
			defer cancel()

			t.Logf("🧠 Testing chat completion streaming with reasoning...")

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

					if response.BifrostChatResponse != nil {
						chatResp := response.BifrostChatResponse

						// Check for reasoning in choices
						if len(chatResp.Choices) > 0 {
							for _, choice := range chatResp.Choices {
								if choice.ChatStreamResponseChoice != nil && choice.ChatStreamResponseChoice.Delta != nil {
									delta := choice.ChatStreamResponseChoice.Delta

									// Check for reasoning content in delta
									if delta.Reasoning != nil && *delta.Reasoning != "" {
										reasoningDetected = true
										t.Logf("🧠 Reasoning content detected: %q", *delta.Reasoning)
									}

									// Check for reasoning details in delta
									if len(delta.ReasoningDetails) > 0 {
										reasoningDetailsDetected = true
										t.Logf("🧠 Reasoning details detected: %d entries", len(delta.ReasoningDetails))

										for _, detail := range delta.ReasoningDetails {
											t.Logf("  - Type: %s, Index: %d", detail.Type, detail.Index)
											switch detail.Type {
											case schemas.BifrostReasoningDetailsTypeText:
												if detail.Text != nil && *detail.Text != "" {
													maxLen := 100
													text := *detail.Text
													if len(text) < maxLen {
														maxLen = len(text)
													}
													t.Logf("    Text preview: %q", text[:maxLen])
												}
											case schemas.BifrostReasoningDetailsTypeSummary:
												if detail.Summary != nil {
													t.Logf("    Summary length: %d", len(*detail.Summary))
												}
											case schemas.BifrostReasoningDetailsTypeEncrypted:
												if detail.Data != nil {
													t.Logf("    Encrypted data length: %d", len(*detail.Data))
												}
											}
										}
									}
								}
							}
						}

						// Check for reasoning tokens in usage (usually in final chunk)
						if chatResp.Usage != nil && chatResp.Usage.CompletionTokensDetails != nil {
							if chatResp.Usage.CompletionTokensDetails.ReasoningTokens > 0 {
								reasoningTokensDetected = true
								t.Logf("🔢 Reasoning tokens used: %d", chatResp.Usage.CompletionTokensDetails.ReasoningTokens)
							}
						}
					}

					if responseCount > 150 {
						goto reasoningStreamComplete
					}

				case <-streamCtx.Done():
					t.Fatal("Timeout waiting for chat completion streaming response with reasoning")
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
			if !reasoningDetected && !reasoningDetailsDetected && !reasoningTokensDetected {
				t.Logf("⚠️ Warning: No explicit reasoning indicators found in streaming response")
			} else {
				t.Logf("✅ Reasoning indicators detected:")
				if reasoningDetected {
					t.Logf("  - Reasoning content found")
				}
				if reasoningDetailsDetected {
					t.Logf("  - Reasoning details found")
				}
				if reasoningTokensDetected {
					t.Logf("  - Reasoning tokens reported")
				}
			}

			t.Logf("✅ Chat completion streaming with reasoning test completed successfully")
		})

		// Additional test with full validation and retry support
		t.Run("ChatCompletionStreamWithReasoningValidated", func(t *testing.T) {
			if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
				t.Parallel()
			}

			if testConfig.Provider == schemas.OpenAI || testConfig.Provider == schemas.Groq {
				// OpenAI and Groq because reasoning for them in stream is extremely flaky
				t.Skip("Skipping ChatCompletionStreamWithReasoningValidated test for OpenAI and Groq")
				return
			}

			problemPrompt := "A farmer has 100 chickens and 50 cows. Each chicken lays 5 eggs per week, and each cow produces 20 liters of milk per day. If the farmer sells eggs for $0.25 each and milk for $1.50 per liter, and it costs $2 per week to feed each chicken and $15 per week to feed each cow, what is the farmer's weekly profit?"
			if testConfig.Provider == schemas.Cerebras {
				problemPrompt = "Hello how are you, can you search hackernews news regarding maxim ai for me? use your tools for this"
			}

			messages := []schemas.ChatMessage{
				CreateBasicChatMessage(problemPrompt),
			}

			request := &schemas.BifrostChatRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.ReasoningModel,
				Input:    messages,
				Params: &schemas.ChatParameters{
					MaxCompletionTokens: bifrost.Ptr(1800),
					Reasoning: &schemas.ChatReasoning{
						Effort:    bifrost.Ptr("high"),
						MaxTokens: bifrost.Ptr(1500),
					},
				},
				Fallbacks: testConfig.Fallbacks,
			}

			// Use retry framework for stream requests with reasoning and validation
			retryConfig := StreamingRetryConfig()
			retryContext := TestRetryContext{
				ScenarioName: "ChatCompletionStreamWithReasoningValidated",
				ExpectedBehavior: map[string]interface{}{
					"should_stream_reasoning":          true,
					"should_have_reasoning_indicators": true,
					"problem_type":                     "mathematical",
				},
				TestMetadata: map[string]interface{}{
					"provider":  testConfig.Provider,
					"model":     testConfig.ReasoningModel,
					"reasoning": true,
					"validated": true,
				},
			}

			// Use validation retry wrapper that includes stream reading and validation
			validationResult := WithChatStreamValidationRetry(
				t,
				retryConfig,
				retryContext,
				func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
					bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
					return client.ChatCompletionStreamRequest(bfCtx, request)
				},
				func(responseChannel chan *schemas.BifrostStreamChunk) ChatStreamValidationResult {
					var reasoningDetected bool
					var reasoningDetailsDetected bool
					var reasoningTokensDetected bool
					var responseCount int
					var streamErrors []string
					var fullContent strings.Builder

					// Chunk timing tracking for batch detection
					var chunkTimings []chunkTiming
					var lastChunkTime time.Time

					streamCtx, cancel := context.WithTimeout(ctx, 200*time.Second)
					defer cancel()

					t.Logf("🧠 Testing validated chat completion streaming with reasoning...")

					for {
						select {
						case response, ok := <-responseChannel:
							if !ok {
								goto validatedReasoningStreamComplete
							}

							if response == nil {
								streamErrors = append(streamErrors, "❌ Streaming response should not be nil")
								continue
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

							if response.BifrostChatResponse != nil {
								chatResp := response.BifrostChatResponse

								// Check for reasoning in choices
								if len(chatResp.Choices) > 0 {
									for _, choice := range chatResp.Choices {
										if choice.ChatStreamResponseChoice != nil && choice.ChatStreamResponseChoice.Delta != nil {
											delta := choice.ChatStreamResponseChoice.Delta

											// Accumulate content
											if delta.Content != nil {
												fullContent.WriteString(*delta.Content)
												t.Logf("📝 Content chunk received (length: %d, total so far: %d)", len(*delta.Content), fullContent.Len())
											}

											// Check for reasoning content in delta
											if delta.Reasoning != nil && *delta.Reasoning != "" {
												reasoningDetected = true
												t.Logf("🧠 Reasoning content detected (length: %d)", len(*delta.Reasoning))
											}

											// Check for reasoning details in delta
											if len(delta.ReasoningDetails) > 0 {
												reasoningDetailsDetected = true
												t.Logf("🧠 Reasoning details detected: %d entries", len(delta.ReasoningDetails))
											}
										}
									}
								}

								// Check for reasoning tokens in usage
								if chatResp.Usage != nil && chatResp.Usage.CompletionTokensDetails != nil {
									if chatResp.Usage.CompletionTokensDetails.ReasoningTokens > 0 {
										reasoningTokensDetected = true
										t.Logf("🔢 Reasoning tokens: %d", chatResp.Usage.CompletionTokensDetails.ReasoningTokens)
									}
								}
							}

							if responseCount > 150 {
								goto validatedReasoningStreamComplete
							}

						case <-streamCtx.Done():
							streamErrors = append(streamErrors, "❌ Timeout waiting for streaming response with reasoning")
							goto validatedReasoningStreamComplete
						}
					}

				validatedReasoningStreamComplete:
					var errors []string
					if responseCount == 0 {
						errors = append(errors, "❌ Should receive at least one streaming response")
					}

					// Check for batched streaming
					if isBatched, batchMsg := detectBatchedStream(chunkTimings, 5); isBatched {
						errors = append(errors, fmt.Sprintf("❌ Streaming validation failed: %s", batchMsg))
					}

					// Check if at least one reasoning indicator is present
					hasAnyReasoningIndicator := reasoningDetected || reasoningDetailsDetected || reasoningTokensDetected
					if !hasAnyReasoningIndicator {
						errors = append(errors, fmt.Sprintf("❌ No reasoning indicators found in streaming response (received %d chunks)", responseCount))
					}

					// Check content - for reasoning models, content may come after reasoning or may not be present
					// If reasoning is detected, we consider it a valid response even without content
					content := strings.TrimSpace(fullContent.String())
					if content == "" && !hasAnyReasoningIndicator {
						// Only require content if no reasoning indicators were found
						errors = append(errors, "❌ No content received in streaming response and no reasoning indicators found")
					} else if content == "" && hasAnyReasoningIndicator {
						// Log a warning but don't fail if reasoning is present
						t.Logf("⚠️ Warning: Reasoning detected but no content chunks received (this may be expected for some reasoning models)")
					}

					if len(streamErrors) > 0 {
						errors = append(errors, streamErrors...)
					}

					return ChatStreamValidationResult{
						Passed:           len(errors) == 0,
						Errors:           errors,
						ReceivedData:     responseCount > 0 && (content != "" || hasAnyReasoningIndicator),
						StreamErrors:     streamErrors,
						ToolCallDetected: false, // Not testing tool calls here
						ResponseCount:    responseCount,
					}
				},
			)

			// Check validation result
			if !validationResult.Passed {
				allErrors := append(validationResult.Errors, validationResult.StreamErrors...)
				t.Fatalf("❌ Chat completion stream with reasoning validation failed after retries: %s", strings.Join(allErrors, "; "))
			}

			if validationResult.ResponseCount == 0 {
				t.Fatalf("❌ Should receive at least one streaming response")
			}

			t.Logf("✅ Validated chat completion streaming with reasoning test completed successfully")
		})
	}
}
