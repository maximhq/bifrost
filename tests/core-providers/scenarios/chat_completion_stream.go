package scenarios

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/tests/core-providers/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunChatCompletionStreamTest executes the chat completion stream test scenario
func RunChatCompletionStreamTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig config.ComprehensiveTestConfig) {
	if !testConfig.Scenarios.ChatCompletionStream {
		t.Logf("Chat completion stream not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("ChatCompletionStream", func(t *testing.T) {
		messages := []schemas.ChatMessage{
			CreateBasicChatMessage("Tell me a short story about a robot learning to paint. Keep it under 200 words."),
		}

		request := &schemas.BifrostRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ChatModel,
			Input: schemas.RequestInput{
				ChatCompletionInput: &messages,
			},
			Params:    testConfig.CustomParams,
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
		var hasReceivedUsage bool

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
				lastResponse = response

				// Basic validation of streaming response structure
				if response.BifrostResponse != nil {
					if response.ExtraFields.Provider != testConfig.Provider {
						t.Logf("⚠️ Warning: Provider mismatch - expected %s, got %s", testConfig.Provider, response.ExtraFields.Provider)
					}
					if response.ID == "" {
						t.Logf("⚠️ Warning: Response ID is empty")
					}
				}

				// Process each choice in the response
				if response.ChatCompletionsExtendedResponse != nil {
					for _, choice := range response.ChatCompletionsExtendedResponse.Choices {
						// Validate that this is a stream response
						if choice.BifrostStreamResponseChoice == nil {
							t.Logf("⚠️ Warning: Stream response choice is nil for choice %d", choice.Index)
							continue
						}
						if choice.BifrostNonStreamResponseChoice != nil {
							t.Logf("⚠️ Warning: Non-stream response choice should be nil in streaming response")
						}

						// Get content from delta
						if choice.BifrostStreamResponseChoice != nil {
							delta := choice.BifrostStreamResponseChoice.Delta
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

				// Check if this response contains usage information
				if response.Usage != nil {
					hasReceivedUsage = true
					t.Logf("📊 Token usage received - Prompt: %d, Completion: %d, Total: %d",
						response.Usage.PromptTokens,
						response.Usage.CompletionTokens,
						response.Usage.TotalTokens)

					// Validate token counts
					assert.Greater(t, response.Usage.PromptTokens, 0, "Prompt tokens should be greater than 0")
					assert.Greater(t, response.Usage.CompletionTokens, 0, "Completion tokens should be greater than 0")
					assert.Greater(t, response.Usage.TotalTokens, 0, "Total tokens should be greater than 0")
					assert.Equal(t, response.Usage.PromptTokens+response.Usage.CompletionTokens,
						response.Usage.TotalTokens, "Total tokens should equal prompt + completion tokens")
				}

				// Check if this response contains usage information
				if response.Usage != nil {
					hasReceivedUsage = true
					t.Logf("📊 Token usage received - Prompt: %d, Completion: %d, Total: %d",
						response.Usage.PromptTokens,
						response.Usage.CompletionTokens,
						response.Usage.TotalTokens)

					// Validate token counts
					assert.Greater(t, response.Usage.PromptTokens, 0, "Prompt tokens should be greater than 0")
					assert.Greater(t, response.Usage.CompletionTokens, 0, "Completion tokens should be greater than 0")
					assert.Greater(t, response.Usage.TotalTokens, 0, "Total tokens should be greater than 0")
					assert.Equal(t, response.Usage.PromptTokens+response.Usage.CompletionTokens,
						response.Usage.TotalTokens, "Total tokens should equal prompt + completion tokens")
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
		// Validate that we received usage information at some point in the stream
		assert.True(t, hasReceivedUsage, "Should have received token usage information during streaming")

		// Validate final streaming response
		finalContent := strings.TrimSpace(fullContent.String())

		// Create a consolidated response for validation
		consolidatedResponse := &schemas.BifrostResponse{
			Choices: []schemas.BifrostChatResponseChoice{
				{
					Index: 0,
					BifrostNonStreamResponseChoice: &schemas.BifrostNonStreamResponseChoice{
						Message: schemas.ChatMessage{
							Role: schemas.ChatMessageRoleAssistant,
							Content: schemas.ChatMessageContent{
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
		if lastResponse != nil && lastResponse.BifrostResponse != nil {
			consolidatedResponse.Usage = lastResponse.Usage
			consolidatedResponse.Model = lastResponse.Model
			consolidatedResponse.ID = lastResponse.ID
			consolidatedResponse.Created = lastResponse.Created

			// Copy finish reason from last choice if available
			if len(lastResponse.Choices) > 0 && lastResponse.Choices[0].FinishReason != nil {
				consolidatedResponse.Choices[0].FinishReason = lastResponse.Choices[0].FinishReason
			}
		}

		// Enhanced validation expectations for streaming
		expectations := GetExpectationsForScenario("ChatCompletionStream", testConfig, map[string]interface{}{})
		expectations = ModifyExpectationsForProvider(expectations, testConfig.Provider)
		expectations.ShouldContainKeywords = []string{"robot", "paint"} // Should include story elements
		expectations.MinContentLength = 50                              // Should be substantial story
		expectations.MaxContentLength = 2000                            // Reasonable upper bound

		// Validate the consolidated streaming response
		validationResult := ValidateResponse(t, consolidatedResponse, nil, expectations, "ChatCompletionStream")

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
			t.Logf("⚠️ Streaming validation warnings: %v", validationResult.Errors)
		}

		t.Logf("📊 Streaming metrics: %d chunks, %d chars", responseCount, len(finalContent))

		t.Logf("✅ Streaming test completed successfully")
		t.Logf("📝 Final content (%d chars)", len(finalContent))
	})

	// Test streaming with tool calls if supported
	if testConfig.Scenarios.ToolCalls {
		t.Run("ChatCompletionStreamWithTools", func(t *testing.T) {
			messages := []schemas.ChatMessage{
				CreateBasicChatMessage("What's the weather like in San Francisco? Please use the get_weather function."),
			}

			tool := GetSampleTool(SampleToolTypeWeather, false)

			request := &schemas.BifrostRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.ChatModel,
				Input: schemas.RequestInput{
					ChatCompletionInput: &messages,
				},
				Params: MergeModelParameters(&schemas.ModelParameters{
					MaxTokens: bifrost.Ptr(150),
					Tools:     &[]schemas.Tool{*tool},
				}, testConfig.CustomParams),
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

					if response == nil {
						t.Fatal("Streaming response should not be nil")
					}
					require.NotNil(t, response, "Streaming response should not be nil")
					lastResponseWithTools = response
					responseCount++

					// Check for usage information in tool call streaming
					if response.Usage != nil {
						hasReceivedUsageWithTools = true
						t.Logf("📊 Tool stream usage - Prompt: %d, Completion: %d, Total: %d",
							response.Usage.PromptTokens,
							response.Usage.CompletionTokens,
							response.Usage.TotalTokens)

						// Validate token counts for tool calls
						assert.Greater(t, response.Usage.PromptTokens, 0, "Tool stream should have prompt tokens")
						assert.Greater(t, response.Usage.CompletionTokens, 0, "Tool stream should have completion tokens")
						assert.Greater(t, response.Usage.TotalTokens, 0, "Tool stream should have total tokens")
						assert.Equal(t, response.Usage.PromptTokens+response.Usage.CompletionTokens,
							response.Usage.TotalTokens, "Total should equal prompt + completion for tool stream")
					}

					// Check for usage information in tool call streaming
					if response.Usage != nil {
						hasReceivedUsageWithTools = true
						t.Logf("📊 Tool stream usage - Prompt: %d, Completion: %d, Total: %d",
							response.Usage.PromptTokens,
							response.Usage.CompletionTokens,
							response.Usage.TotalTokens)

						// Validate token counts for tool calls
						assert.Greater(t, response.Usage.PromptTokens, 0, "Tool stream should have prompt tokens")
						assert.Greater(t, response.Usage.CompletionTokens, 0, "Tool stream should have completion tokens")
						assert.Greater(t, response.Usage.TotalTokens, 0, "Tool stream should have total tokens")
						assert.Equal(t, response.Usage.PromptTokens+response.Usage.CompletionTokens,
							response.Usage.TotalTokens, "Total should equal prompt + completion for tool stream")
					}

					if response.Choices != nil {
						for _, choice := range response.Choices {
							if choice.BifrostStreamResponseChoice != nil {
								delta := choice.BifrostStreamResponseChoice.Delta

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
