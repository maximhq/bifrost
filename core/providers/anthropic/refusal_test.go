package anthropic

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestToBifrostChatResponse_Refusal verifies that an Anthropic refusal stop_reason
// (with or without stop_details.explanation) is surfaced via OpenAI's native
// message.refusal field, and that finish_reason maps to "content_filter".
func TestToBifrostChatResponse_Refusal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		stopDetails     *AnthropicStopDetails
		expectedRefusal string
	}{
		{
			name: "with explanation",
			stopDetails: &AnthropicStopDetails{
				Type:        "refusal",
				Category:    schemas.Ptr("cyber"),
				Explanation: schemas.Ptr("This request involves prohibited cybersecurity content."),
			},
			expectedRefusal: "This request involves prohibited cybersecurity content.",
		},
		{
			name:            "without stop_details",
			stopDetails:     nil,
			expectedRefusal: "The model declined to respond.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
			defer cancel()

			resp := &AnthropicMessageResponse{
				ID:          "msg_refusal_test",
				Type:        "message",
				Role:        "assistant",
				Model:       "claude-fable-5",
				StopReason:  AnthropicStopReasonRefusal,
				StopDetails: tt.stopDetails,
				Content:     []AnthropicContentBlock{},
			}

			bifrostResp := resp.ToBifrostChatResponse(ctx)

			if len(bifrostResp.Choices) != 1 {
				t.Fatalf("expected 1 choice, got %d", len(bifrostResp.Choices))
			}
			choice := bifrostResp.Choices[0]

			if choice.FinishReason == nil || *choice.FinishReason != "content_filter" {
				t.Errorf("FinishReason = %v, want %q", choice.FinishReason, "content_filter")
			}

			msg := choice.Message
			if msg == nil || msg.ChatAssistantMessage == nil || msg.ChatAssistantMessage.Refusal == nil {
				t.Fatal("expected ChatAssistantMessage.Refusal to be set")
			}
			if *msg.ChatAssistantMessage.Refusal != tt.expectedRefusal {
				t.Errorf("Refusal = %q, want %q", *msg.ChatAssistantMessage.Refusal, tt.expectedRefusal)
			}
		})
	}
}

// TestToAnthropicChatResponse_RoundTripsRefusal verifies that a Bifrost response
// carrying ChatAssistantMessage.Refusal round-trips back into Anthropic's
// stop_reason/stop_details.
func TestToAnthropicChatResponse_RoundTripsRefusal(t *testing.T) {
	t.Parallel()

	explanation := "This request involves prohibited content."
	bifrostResp := &schemas.BifrostChatResponse{
		ID:    "chatcmpl_test",
		Model: "claude-fable-5",
		Choices: []schemas.BifrostResponseChoice{
			{
				Index:        0,
				FinishReason: schemas.Ptr("content_filter"),
				ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
					Message: &schemas.ChatMessage{
						Role: schemas.ChatMessageRoleAssistant,
						ChatAssistantMessage: &schemas.ChatAssistantMessage{
							Refusal: &explanation,
						},
					},
				},
			},
		},
	}

	anthropicResp := ToAnthropicChatResponse(bifrostResp)

	if anthropicResp.StopReason != AnthropicStopReasonRefusal {
		t.Errorf("StopReason = %q, want %q", anthropicResp.StopReason, AnthropicStopReasonRefusal)
	}
	if anthropicResp.StopDetails == nil {
		t.Fatal("expected StopDetails to be set")
	}
	if anthropicResp.StopDetails.Explanation == nil || *anthropicResp.StopDetails.Explanation != explanation {
		t.Errorf("StopDetails.Explanation = %v, want %q", anthropicResp.StopDetails.Explanation, explanation)
	}
}

// TestToBifrostResponsesResponse_Refusal verifies the Responses-API-shaped path sets
// status/incomplete_details when Anthropic reports a refusal.
func TestToBifrostResponsesResponse_Refusal(t *testing.T) {
	t.Parallel()

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	resp := &AnthropicMessageResponse{
		ID:         "msg_refusal_responses_test",
		Type:       "message",
		Role:       "assistant",
		Model:      "claude-fable-5",
		StopReason: AnthropicStopReasonRefusal,
		Content:    []AnthropicContentBlock{},
	}

	bifrostResp := resp.ToBifrostResponsesResponse(ctx)

	if bifrostResp.Status == nil || *bifrostResp.Status != schemas.ResponsesResponseStatusIncomplete {
		t.Errorf("Status = %v, want %q", bifrostResp.Status, schemas.ResponsesResponseStatusIncomplete)
	}
	if bifrostResp.IncompleteDetails == nil {
		t.Fatal("expected IncompleteDetails to be set")
	}
	if bifrostResp.IncompleteDetails.Reason != schemas.ResponsesResponseIncompleteReasonContentFilter {
		t.Errorf("IncompleteDetails.Reason = %q, want %q", bifrostResp.IncompleteDetails.Reason, schemas.ResponsesResponseIncompleteReasonContentFilter)
	}
}

// TestToAnthropicResponsesResponse_RoundTripsRefusal verifies the reverse direction:
// an OpenAI-shaped incomplete/content_filter response round-trips back to Anthropic's
// refusal stop_reason.
func TestToAnthropicResponsesResponse_RoundTripsRefusal(t *testing.T) {
	t.Parallel()

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	bifrostResp := &schemas.BifrostResponsesResponse{
		ID:    schemas.Ptr("resp_refusal_test"),
		Model: "claude-fable-5",
		IncompleteDetails: &schemas.ResponsesResponseIncompleteDetails{
			Reason: schemas.ResponsesResponseIncompleteReasonContentFilter,
		},
	}

	anthropicResp := ToAnthropicResponsesResponse(ctx, bifrostResp)

	if anthropicResp.StopReason != AnthropicStopReasonRefusal {
		t.Errorf("StopReason = %q, want %q", anthropicResp.StopReason, AnthropicStopReasonRefusal)
	}
	if anthropicResp.StopDetails == nil {
		t.Fatal("expected StopDetails to be set")
	}
}
