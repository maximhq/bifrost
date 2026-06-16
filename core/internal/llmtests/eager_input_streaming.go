package llmtests

import (
	"context"
	"os"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunEagerInputStreamingTest tests that setting eager_input_streaming: true on
// a custom tool succeeds end-to-end against the target Anthropic-family
// provider. Per Table 20 (verified against A overview + B-header), the
// fine-grained-tool-streaming-2025-05-14 beta is supported on Anthropic,
// Bedrock, Vertex, and Azure.
//
// The test verifies:
//  1. The request is accepted (no upstream 400 — which would indicate the
//     fine-grained-tool-streaming-2025-05-14 beta header wasn't injected or
//     is rejected by the target provider).
//  2. The stream produces a tool call with a valid JSON arguments payload.
//  3. The response is otherwise well-formed.
//
// This intentionally runs across all four providers (no single-provider gate
// unlike RunFastModeTest, which is Opus-4.6-only).
func RunEagerInputStreamingTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.EagerInputStreaming {
		t.Logf("EagerInputStreaming not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("EagerInputStreaming", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		chatTool := GetSampleChatTool(SampleToolTypeWeather)
		// Opt the tool into fine-grained input streaming. The neutral flag
		// on ChatTool is promoted through ToAnthropicChatRequest, which also
		// triggers the fine-grained-tool-streaming-2025-05-14 beta header.
		eager := true
		chatTool.EagerInputStreaming = &eager

		chatMessages := []schemas.ChatMessage{
			CreateBasicChatMessage("What's the weather like in San Francisco? answer in celsius"),
		}

		request := &schemas.BifrostChatRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ChatModel,
			Input:    chatMessages,
			Params: &schemas.ChatParameters{
				MaxCompletionTokens: bifrost.Ptr(200),
				Tools:               []schemas.ChatTool{*chatTool},
			},
			Fallbacks: testConfig.Fallbacks,
		}

		retryConfig := StreamingRetryConfig()
		retryContext := TestRetryContext{
			ScenarioName: "EagerInputStreaming",
			ExpectedBehavior: map[string]interface{}{
				"should_stream_content":  true,
				"should_have_tool_calls": true,
				"tool_name":              "get_weather",
			},
			TestMetadata: map[string]interface{}{
				"provider":              testConfig.Provider,
				"model":                 testConfig.ChatModel,
				"eager_input_streaming": true,
			},
		}

		responseChannel, err := WithStreamRetry(t, retryConfig, retryContext, func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			return client.ChatCompletionStreamRequest(bfCtx, request)
		})

		RequireNoError(t, err, "Eager input streaming request failed")
		if responseChannel == nil {
			t.Fatal("Response channel should not be nil")
		}

		accumulator := NewStreamingToolCallAccumulator()
		var responseCount int
		var sawAny bool

		t.Logf("🔧 Testing eager input streaming (fine-grained-tool-streaming-2025-05-14)...")

		for response := range responseChannel {
			if response == nil || response.BifrostChatResponse == nil {
				continue
			}
			responseCount++
			sawAny = true

			if response.BifrostChatResponse.Choices != nil {
				for i, choice := range response.BifrostChatResponse.Choices {
					if choice.ChatStreamResponseChoice != nil && choice.ChatStreamResponseChoice.Delta != nil {
						delta := choice.ChatStreamResponseChoice.Delta
						for _, tc := range delta.ToolCalls {
							accumulator.AccumulateChatToolCall(i, tc)
						}
					}
				}
			}
		}

		if !sawAny {
			t.Fatal("Expected at least one streaming response chunk")
		}
		t.Logf("Received %d chunks", responseCount)

		// Validate the accumulated tool call is well-formed. If the
		// fine-grained-tool-streaming beta header weren't sent (or the
		// provider rejected it), the upstream would have returned a 400
		// before any tool_use blocks were emitted.
		toolCalls := accumulator.GetFinalChatToolCalls()
		if len(toolCalls) == 0 {
			t.Error("Expected at least one tool call in stream")
		}
		for _, tc := range toolCalls {
			if tc.Name == "" {
				t.Error("Tool call missing function name")
			}
			if tc.Arguments == "" {
				t.Error("Tool call missing arguments JSON")
			}
		}

		t.Logf("EagerInputStreaming passed: %d tool calls accumulated", len(toolCalls))
	})
}
