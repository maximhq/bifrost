package anthropic

import (
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestToBifrostChatResponse_ThinkingTokens verifies output_tokens_details.thinking_tokens
// is propagated into the common ReasoningTokens field, and omitted when zero/absent.
func TestToBifrostChatResponse_ThinkingTokens(t *testing.T) {
	t.Run("propagates thinking tokens", func(t *testing.T) {
		response := &AnthropicMessageResponse{
			ID: "msg_1", Type: "message", Role: "assistant", Model: "claude-opus-4-6-20250514",
			StopReason: "end_turn",
			Usage: &AnthropicUsage{
				InputTokens:  10,
				OutputTokens: 50,
				OutputTokensDetails: &AnthropicOutputTokensDetails{
					ThinkingTokens: 30,
				},
			},
		}
		result := response.ToBifrostChatResponse(nil)
		if result.Usage == nil || result.Usage.CompletionTokensDetails == nil {
			t.Fatalf("expected CompletionTokensDetails to be set, got %+v", result.Usage)
		}
		if result.Usage.CompletionTokensDetails.ReasoningTokens != 30 {
			t.Fatalf("ReasoningTokens = %d, want 30", result.Usage.CompletionTokensDetails.ReasoningTokens)
		}
	})

	t.Run("zero thinking tokens does not allocate details", func(t *testing.T) {
		response := &AnthropicMessageResponse{
			ID: "msg_2", Type: "message", Role: "assistant", Model: "claude-opus-4-6-20250514",
			StopReason: "end_turn",
			Usage: &AnthropicUsage{
				InputTokens:  10,
				OutputTokens: 5,
				OutputTokensDetails: &AnthropicOutputTokensDetails{
					ThinkingTokens: 0,
				},
			},
		}
		result := response.ToBifrostChatResponse(nil)
		if result.Usage.CompletionTokensDetails != nil {
			t.Fatalf("expected nil CompletionTokensDetails for zero thinking tokens, got %+v", result.Usage.CompletionTokensDetails)
		}
	})

	t.Run("absent OutputTokensDetails does not allocate details", func(t *testing.T) {
		response := &AnthropicMessageResponse{
			ID: "msg_3", Type: "message", Role: "assistant", Model: "claude-opus-4-6-20250514",
			StopReason: "end_turn",
			Usage:      &AnthropicUsage{InputTokens: 10, OutputTokens: 5},
		}
		result := response.ToBifrostChatResponse(nil)
		if result.Usage.CompletionTokensDetails != nil {
			t.Fatalf("expected nil CompletionTokensDetails, got %+v", result.Usage.CompletionTokensDetails)
		}
	})
}

// TestToAnthropicChatResponse_ThinkingTokens verifies the reverse direction: Bifrost's
// ReasoningTokens surfaces as Anthropic's output_tokens_details.thinking_tokens, and is
// omitted (not just zero-valued) when there is nothing to report.
func TestToAnthropicChatResponse_ThinkingTokens(t *testing.T) {
	t.Run("propagates reasoning tokens", func(t *testing.T) {
		bifrostResp := &schemas.BifrostChatResponse{
			Usage: &schemas.BifrostLLMUsage{
				PromptTokens:     10,
				CompletionTokens: 50,
				CompletionTokensDetails: &schemas.ChatCompletionTokensDetails{
					ReasoningTokens: 30,
				},
			},
		}
		result := ToAnthropicChatResponse(bifrostResp)
		if result.Usage.OutputTokensDetails == nil || result.Usage.OutputTokensDetails.ThinkingTokens != 30 {
			t.Fatalf("OutputTokensDetails = %+v, want ThinkingTokens=30", result.Usage.OutputTokensDetails)
		}
	})

	t.Run("zero reasoning tokens omits details", func(t *testing.T) {
		bifrostResp := &schemas.BifrostChatResponse{
			Usage: &schemas.BifrostLLMUsage{
				PromptTokens:     10,
				CompletionTokens: 5,
				CompletionTokensDetails: &schemas.ChatCompletionTokensDetails{
					ReasoningTokens: 0,
				},
			},
		}
		result := ToAnthropicChatResponse(bifrostResp)
		if result.Usage.OutputTokensDetails != nil {
			t.Fatalf("expected nil OutputTokensDetails, got %+v", result.Usage.OutputTokensDetails)
		}
	})
}

// TestConvertAnthropicUsageToBifrostUsage_ThinkingTokens covers the Responses API converter,
// including the >0 guard that prevents allocating a spurious OutputTokensDetails on turns
// with no thinking tokens.
func TestConvertAnthropicUsageToBifrostUsage_ThinkingTokens(t *testing.T) {
	t.Run("propagates thinking tokens", func(t *testing.T) {
		anthropicUsage := &AnthropicUsage{
			InputTokens:  10,
			OutputTokens: 50,
			OutputTokensDetails: &AnthropicOutputTokensDetails{
				ThinkingTokens: 30,
			},
		}
		result := ConvertAnthropicUsageToBifrostUsage(anthropicUsage)
		if result.OutputTokensDetails == nil || result.OutputTokensDetails.ReasoningTokens != 30 {
			t.Fatalf("OutputTokensDetails = %+v, want ReasoningTokens=30", result.OutputTokensDetails)
		}
	})

	t.Run("zero thinking tokens does not allocate details", func(t *testing.T) {
		anthropicUsage := &AnthropicUsage{
			InputTokens:  10,
			OutputTokens: 5,
			OutputTokensDetails: &AnthropicOutputTokensDetails{
				ThinkingTokens: 0,
			},
		}
		result := ConvertAnthropicUsageToBifrostUsage(anthropicUsage)
		if result.OutputTokensDetails != nil {
			t.Fatalf("expected nil OutputTokensDetails for zero thinking tokens, got %+v", result.OutputTokensDetails)
		}
	})
}

// TestConvertBifrostUsageToAnthropicUsage_ThinkingTokens covers the reverse Responses API
// converter.
func TestConvertBifrostUsageToAnthropicUsage_ThinkingTokens(t *testing.T) {
	t.Run("propagates reasoning tokens", func(t *testing.T) {
		bifrostUsage := &schemas.ResponsesResponseUsage{
			InputTokens:  10,
			OutputTokens: 50,
			OutputTokensDetails: &schemas.ResponsesResponseOutputTokens{
				ReasoningTokens: 30,
			},
		}
		result := ConvertBifrostUsageToAnthropicUsage(bifrostUsage)
		if result.OutputTokensDetails == nil || result.OutputTokensDetails.ThinkingTokens != 30 {
			t.Fatalf("OutputTokensDetails = %+v, want ThinkingTokens=30", result.OutputTokensDetails)
		}
	})

	t.Run("zero reasoning tokens omits details", func(t *testing.T) {
		bifrostUsage := &schemas.ResponsesResponseUsage{
			InputTokens:  10,
			OutputTokens: 5,
			OutputTokensDetails: &schemas.ResponsesResponseOutputTokens{
				ReasoningTokens: 0,
			},
		}
		result := ConvertBifrostUsageToAnthropicUsage(bifrostUsage)
		if result.OutputTokensDetails != nil {
			t.Fatalf("expected nil OutputTokensDetails, got %+v", result.OutputTokensDetails)
		}
	})
}

// TestToAnthropicChatStreamResponse_ThinkingTokens verifies the outbound chat-stream converter
// (used when serving an Anthropic-format SSE stream for a cross-provider response) forwards
// ReasoningTokens as output_tokens_details.thinking_tokens, and omits it when absent/zero.
func TestToAnthropicChatStreamResponse_ThinkingTokens(t *testing.T) {
	t.Run("propagates reasoning tokens into SSE usage", func(t *testing.T) {
		bifrostResp := &schemas.BifrostChatResponse{
			ID: "msg_1",
			Usage: &schemas.BifrostLLMUsage{
				PromptTokens:     10,
				CompletionTokens: 50,
				CompletionTokensDetails: &schemas.ChatCompletionTokensDetails{
					ReasoningTokens: 30,
				},
			},
		}
		sse := ToAnthropicChatStreamResponse(bifrostResp)
		if !strings.Contains(sse, `"thinking_tokens":30`) {
			t.Fatalf("expected thinking_tokens=30 in SSE output, got: %s", sse)
		}
	})

	t.Run("zero reasoning tokens omits output_tokens_details", func(t *testing.T) {
		bifrostResp := &schemas.BifrostChatResponse{
			ID: "msg_2",
			Usage: &schemas.BifrostLLMUsage{
				PromptTokens:     10,
				CompletionTokens: 5,
				CompletionTokensDetails: &schemas.ChatCompletionTokensDetails{
					ReasoningTokens: 0,
				},
			},
		}
		sse := ToAnthropicChatStreamResponse(bifrostResp)
		if strings.Contains(sse, "output_tokens_details") {
			t.Fatalf("expected no output_tokens_details in SSE output, got: %s", sse)
		}
	})
}

// TestAccumulateAnthropicResponsesUsage_ThinkingTokensMaxMerge verifies the native Responses
// SSE accumulator max-merges thinking tokens across events, mirroring the existing cache-token
// merge behavior.
func TestAccumulateAnthropicResponsesUsage_ThinkingTokensMaxMerge(t *testing.T) {
	responseUsage := &schemas.ResponsesResponseUsage{}
	billedUsage := &schemas.BifrostLLMUsage{}

	accumulateAnthropicResponsesUsage(responseUsage, billedUsage, &AnthropicUsage{
		InputTokens: 1, OutputTokens: 5,
		OutputTokensDetails: &AnthropicOutputTokensDetails{ThinkingTokens: 12},
	})
	accumulateAnthropicResponsesUsage(responseUsage, billedUsage, &AnthropicUsage{
		InputTokens: 1, OutputTokens: 40,
		OutputTokensDetails: &AnthropicOutputTokensDetails{ThinkingTokens: 30},
	})

	if responseUsage.OutputTokensDetails == nil || responseUsage.OutputTokensDetails.ReasoningTokens != 30 {
		t.Fatalf("response ReasoningTokens = %+v, want 30 (max of 12 and 30)", responseUsage.OutputTokensDetails)
	}
	if billedUsage.CompletionTokensDetails == nil || billedUsage.CompletionTokensDetails.ReasoningTokens != 30 {
		t.Fatalf("billed ReasoningTokens = %+v, want 30", billedUsage.CompletionTokensDetails)
	}
}
