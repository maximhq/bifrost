package scenarios

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
			Params: MergeModelParameters(&schemas.ModelParameters{
				MaxTokens: bifrost.Ptr(250),
			}, testConfig.CustomParams),
			Fallbacks: testConfig.Fallbacks,
		}

		// Test streaming response
		responseChannel, err := client.ChatCompletionStreamRequest(ctx, request)
		require.Nilf(t, err, "Chat completion stream failed: %v", err)
		require.NotNil(t, responseChannel, "Response channel should not be nil")

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

				require.NotNil(t, response, "Streaming response should not be nil")
				lastResponse = response

				// Validate response structure
				assert.Equal(t, testConfig.Provider, response.ExtraFields.Provider, "Provider should match")
				assert.NotEmpty(t, response.ID, "Response ID should not be empty")
				assert.Equal(t, "chat.completion.chunk", response.Object, "Object type should be chat.completion.chunk")
				assert.NotEmpty(t, response.Choices, "Choices should not be empty")

				// Process each choice in the response
				for _, choice := range response.Choices {
					// Validate that this is a stream response
					assert.NotNil(t, choice.BifrostStreamResponseChoice, "Stream response choice should not be nil")
					assert.Nil(t, choice.BifrostNonStreamResponseChoice, "Non-stream response choice should be nil")

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
		// Validate that the last response contains usage information and/or finish reason
		// with empty choices (typical final chunk pattern)
		if lastResponse != nil && lastResponse.BifrostResponse != nil {
			// Check if this is a final metadata chunk (empty choices with usage/finish info)
			if len(lastResponse.Choices) == 0 && lastResponse.Usage != nil {
				assert.Greater(t, lastResponse.Usage.TotalTokens, 0, "Final chunk should have total token count")
				t.Logf("📊 Final metadata chunk - Total tokens: %d", lastResponse.Usage.TotalTokens)
			} else if len(lastResponse.Choices) > 0 {
				// Check if final choice has finish reason
				finalChoice := lastResponse.Choices[0]
				if finalChoice.FinishReason != nil {
					t.Logf("🏁 Stream ended with finish reason: %s", *finalChoice.FinishReason)
				}
			} else {
				t.Fatal("Last response should have choices or usage")
			}
		}

		// Validate the complete response
		assert.Greater(t, responseCount, 0, "Should receive at least one streaming response")

		finalContent := strings.TrimSpace(fullContent.String())
		assert.NotEmpty(t, finalContent, "Final content should not be empty")
		assert.Greater(t, len(finalContent), 10, "Final content should be substantial")

		if lastResponse.BifrostResponse != nil {
			// Validate the last response has usage information
			if len(lastResponse.Choices) > 0 {
				finishReason := lastResponse.Choices[0].FinishReason
				assert.NotNil(t, finishReason, "Finish reason should not be nil")
			} else {
				// This is a metadata-only chunk, which is valid for final chunks
				assert.NotNil(t, lastResponse.Usage, "Usage should not be nil")
			}
		}

		t.Logf("✅ Streaming test completed successfully")
		t.Logf("📝 Final content (%d chars)", len(finalContent))
	})

	// Test streaming with tool calls if supported
	if testConfig.Scenarios.ToolCalls {
		t.Run("ChatCompletionStreamWithTools", func(t *testing.T) {
			messages := []schemas.ChatMessage{
				CreateBasicChatMessage("What's the weather like in San Francisco? Please use the get_weather function."),
			}

			request := &schemas.BifrostRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.ChatModel,
				Input: schemas.RequestInput{
					ChatCompletionInput: &messages,
				},
				Params: MergeModelParameters(&schemas.ModelParameters{
					MaxTokens: bifrost.Ptr(150),
					Tools:     &[]schemas.Tool{WeatherToolDefinition},
				}, testConfig.CustomParams),
				Fallbacks: testConfig.Fallbacks,
			}

			responseChannel, err := client.ChatCompletionStreamRequest(ctx, request)
			require.Nilf(t, err, "Chat completion stream with tools failed: %v", err)
			require.NotNil(t, responseChannel, "Response channel should not be nil")

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

					require.NotNil(t, response, "Streaming response should not be nil")
					responseCount++

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

					if responseCount > 100 {
						goto toolStreamComplete
					}

				case <-streamCtx.Done():
					t.Fatal("Timeout waiting for streaming response with tools")
				}
			}

		toolStreamComplete:
			assert.Greater(t, responseCount, 0, "Should receive at least one streaming response")
			assert.True(t, toolCallDetected, "Should detect tool calls in streaming response")
			t.Logf("✅ Streaming with tools test completed successfully")
		})
	}
}
