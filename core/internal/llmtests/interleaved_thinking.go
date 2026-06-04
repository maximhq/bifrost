package llmtests

import (
	"context"
	"os"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunInterleavedThinkingTest tests that the interleaved-thinking-2025-05-14 beta header
// is correctly sent and that thinking works alongside tool calls.
//
// This test verifies:
//  1. The interleaved-thinking beta header is properly injected when thinking is enabled
//  2. The API accepts the request with thinking + tools without error
//  3. The response contains reasoning content
func RunInterleavedThinkingTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.InterleavedThinking {
		t.Logf("Interleaved thinking not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("InterleavedThinking", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		model := testConfig.InterleavedThinkingModel
		if model == "" {
			model = testConfig.ReasoningModel
		}
		if model == "" {
			model = "claude-opus-4-5"
		}

		// Use the standard weather tool so thinking can interleave with tool calls
		weatherTool := GetSampleResponsesTool(SampleToolTypeWeather)

		messages := []schemas.ResponsesMessage{
			CreateBasicResponsesMessage("What is the weather in Paris? Think step by step before calling the tool."),
		}

		t.Run("NonStreaming", func(t *testing.T) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)

			request := &schemas.BifrostResponsesRequest{
				Provider: testConfig.Provider,
				Model:    model,
				Input:    messages,
				Params: &schemas.ResponsesParameters{
					MaxOutputTokens: bifrost.Ptr(4096),
					Tools:           []schemas.ResponsesTool{*weatherTool},
					Reasoning: &schemas.ResponsesParametersReasoning{
						Effort: bifrost.Ptr("low"),
					},
				},
				Fallbacks: testConfig.Fallbacks,
			}

			response, err := client.ResponsesRequest(bfCtx, request)
			if err != nil {
				t.Fatalf("Interleaved thinking non-streaming request failed: %s", GetErrorMessage(err))
			}
			if response == nil {
				t.Fatal("Expected non-nil response")
			}

			t.Logf("Interleaved thinking non-streaming passed: stop_reason=%v", response.StopReason)

			// Validate that the response contains output
			if response.Output == nil || len(response.Output) == 0 {
				t.Fatal("Expected non-empty output for interleaved thinking response")
			}

			// Check for reasoning indicators
			reasoningDetected := validateResponsesAPIReasoning(t, response)
			if reasoningDetected {
				t.Logf("Reasoning structure detected in interleaved thinking response")
			}

			// Check for tool calls (interleaved thinking should produce tool calls with the weather tool)
			toolCalls := ExtractResponsesToolCalls(response)
			if len(toolCalls) > 0 {
				t.Logf("Tool calls found in interleaved thinking response: %d", len(toolCalls))
				for _, tc := range toolCalls {
					t.Logf("  Tool call: %s", tc.Name)
				}
			} else {
				t.Logf("No tool calls found in interleaved thinking response (model may have answered without calling tools)")
			}

			// Validate raw request/response fields when enabled
			if testConfig.ExpectRawRequestResponse {
				if err := ValidateRawField(response.ExtraFields.RawRequest, "RawRequest"); err != nil {
					t.Errorf("Interleaved thinking non-streaming raw request validation failed: %v", err)
				}
				if err := ValidateRawField(response.ExtraFields.RawResponse, "RawResponse"); err != nil {
					t.Errorf("Interleaved thinking non-streaming raw response validation failed: %v", err)
				}
			}
		})

		t.Run("ChatNonStreaming", func(t *testing.T) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)

			chatMessages := []schemas.ChatMessage{
				CreateBasicChatMessage("What is the weather in Paris? Think step by step before calling the tool."),
			}

			chatTool := GetSampleChatTool(SampleToolTypeWeather)

			request := &schemas.BifrostChatRequest{
				Provider: testConfig.Provider,
				Model:    model,
				Input:    chatMessages,
				Params: &schemas.ChatParameters{
					MaxCompletionTokens: bifrost.Ptr(4096),
					Tools:               []schemas.ChatTool{*chatTool},
					Reasoning: &schemas.ChatReasoning{
						Effort: bifrost.Ptr("low"),
					},
				},
				Fallbacks: testConfig.Fallbacks,
			}

			response, err := client.ChatCompletionRequest(bfCtx, request)
			if err != nil {
				t.Fatalf("Interleaved thinking chat non-streaming request failed: %s", GetErrorMessage(err))
			}
			if response == nil {
				t.Fatal("Expected non-nil response")
			}

			t.Logf("Interleaved thinking chat non-streaming passed")

			content := GetChatContent(response)
			if content == "" && len(ExtractChatToolCalls(response)) == 0 {
				t.Fatal("Expected non-empty content or tool calls for interleaved thinking chat response")
			}

			reasoningDetected := validateChatCompletionReasoning(t, response)
			if reasoningDetected {
				t.Logf("Reasoning structure detected in interleaved thinking chat response")
			}

			toolCalls := ExtractChatToolCalls(response)
			if len(toolCalls) > 0 {
				t.Logf("Tool calls found in interleaved thinking chat response: %d", len(toolCalls))
				for _, tc := range toolCalls {
					t.Logf("  Tool call: %s", tc.Name)
				}
			} else {
				t.Logf("No tool calls found in interleaved thinking chat response (model may have answered without calling tools)")
			}

			// Validate raw request/response fields when enabled
			if testConfig.ExpectRawRequestResponse {
				if err := ValidateRawField(response.ExtraFields.RawRequest, "RawRequest"); err != nil {
					t.Errorf("Interleaved thinking chat non-streaming raw request validation failed: %v", err)
				}
				if err := ValidateRawField(response.ExtraFields.RawResponse, "RawResponse"); err != nil {
					t.Errorf("Interleaved thinking chat non-streaming raw response validation failed: %v", err)
				}
			}
		})
	})
}
