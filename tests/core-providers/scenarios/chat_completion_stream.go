package scenarios

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunChatCompletionStreamTest executes the chat completion stream test scenario
func RunChatCompletionStreamTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig config.ComprehensiveTestConfig) {
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
		responseChannel, err := WithStreamRetry(t, retryConfig, retryContext, func() (chan *schemas.BifrostStream, *schemas.BifrostError) {
			return client.ChatCompletionStreamRequest(ctx, request)
		})

		// Enhanced error handling
		RequireNoError(t, err, "Chat completion stream request failed")
		if responseChannel == nil {
			t.Fatal("Response channel should not be nil")
		}

		var fullContent strings.Builder
		var responseCount int
		var lastResponse *schemas.BifrostStream

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
				lastResponse = DeepCopyBifrostStream(response)

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
		expectations.ShouldContainAnyOf = append(expectations.ShouldContainAnyOf, []string{"paris"}...) // Should include story elements
		expectations.MinContentLength = 50                                                              // Should be substantial story
		expectations.MaxContentLength = 2000                                                            // Reasonable upper bound

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
			t.Errorf("❌ Streaming validation failed: %v", validationResult.Errors)
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

			responseChannel, err := client.ChatCompletionStreamRequest(ctx, request)
			RequireNoError(t, err, "Chat completion stream with tools failed")
			if responseChannel == nil {
				t.Fatal("Response channel should not be nil")
			}

			var toolCallDetected bool
			var responseCount int

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
						t.Fatal("Streaming response should not be nil")
					}
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
					t.Fatal("Timeout waiting for streaming response with tools")
				}
			}

		toolStreamComplete:
			if responseCount == 0 {
				t.Fatal("Should receive at least one streaming response")
			}
			if !toolCallDetected {
				t.Fatal("Should detect tool calls in streaming response")
			}
			t.Logf("✅ Streaming with tools test completed successfully")
		})
	}
}
