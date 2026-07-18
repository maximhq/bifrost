package anthropic

import (
	"context"
	"testing"
	"time"

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
				Model:       "claude-haiku-4-5",
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

// TestToAnthropicChatResponse_NilChatNonStreamResponseChoiceDoesNotPanic guards
// against a nil-pointer panic flagged in review: choice.Message is a field
// promoted from the embedded *ChatNonStreamResponseChoice, so a bare
// "choice.Message != nil" check dereferences that pointer and panics when a
// streaming-shaped choice (ChatNonStreamResponseChoice == nil) is passed in.
func TestToAnthropicChatResponse_NilChatNonStreamResponseChoiceDoesNotPanic(t *testing.T) {
	t.Parallel()

	bifrostResp := &schemas.BifrostChatResponse{
		ID:    "chatcmpl_test",
		Model: "claude-haiku-4-5",
		Choices: []schemas.BifrostResponseChoice{
			{
				Index:        0,
				FinishReason: schemas.Ptr("stop"),
				// ChatNonStreamResponseChoice deliberately nil; only the stream
				// variant is set, as a streaming choice would be.
				ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
					Delta: &schemas.ChatStreamResponseChoiceDelta{},
				},
			},
		},
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ToAnthropicChatResponse panicked: %v", r)
		}
	}()

	anthropicResp := ToAnthropicChatResponse(bifrostResp)
	if anthropicResp.StopReason != AnthropicStopReasonEndTurn {
		t.Errorf("StopReason = %q, want %q", anthropicResp.StopReason, AnthropicStopReasonEndTurn)
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
		Model: "claude-haiku-4-5",
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
		Model:      "claude-haiku-4-5",
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
		Model: "claude-haiku-4-5",
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

// TestToBifrostResponsesStream_RefusalMessageDelta verifies that a streaming
// message_delta event carrying stop_reason "refusal" populates Status and
// IncompleteDetails on the emitted message_delta response — the streaming
// counterpart of TestToBifrostResponsesResponse_Refusal.
func TestToBifrostResponsesStream_RefusalMessageDelta(t *testing.T) {
	t.Parallel()

	ctx := schemas.NewBifrostContext(nil, time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyIntegrationType, "anthropic")

	state := AcquireAnthropicResponsesStreamState()
	defer ReleaseAnthropicResponsesStreamState(state)

	explanation := "This request involves prohibited content."
	chunk := &AnthropicStreamEvent{
		Type: AnthropicStreamEventTypeMessageDelta,
		Delta: &AnthropicStreamDelta{
			StopReason: schemas.Ptr(AnthropicStopReasonRefusal),
			StopDetails: &AnthropicStopDetails{
				Type:        "refusal",
				Explanation: &explanation,
			},
		},
	}

	responses, err, isLast := chunk.ToBifrostResponsesStream(ctx, 0, state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isLast {
		t.Error("should not be last chunk")
	}
	if len(responses) != 1 {
		t.Fatalf("expected 1 response for message_delta, got %d", len(responses))
	}

	resp := responses[0].Response
	if resp == nil {
		t.Fatal("expected non-nil Response on message_delta event")
	}
	if resp.Status == nil || *resp.Status != schemas.ResponsesResponseStatusIncomplete {
		t.Errorf("Status = %v, want %q", resp.Status, schemas.ResponsesResponseStatusIncomplete)
	}
	if resp.IncompleteDetails == nil {
		t.Fatal("expected IncompleteDetails to be set")
	}
	if resp.IncompleteDetails.Reason != schemas.ResponsesResponseIncompleteReasonContentFilter {
		t.Errorf("IncompleteDetails.Reason = %q, want %q", resp.IncompleteDetails.Reason, schemas.ResponsesResponseIncompleteReasonContentFilter)
	}
}

// TestAnthropicResponsesStreamState_StopDetailsResetOnAcquire verifies that
// StopDetails set by a refusal on one pooled stream state does not leak into
// the next request that reuses the same pooled object (sync.Pool). Both
// AcquireAnthropicResponsesStreamState and flush() must clear it, mirroring
// the existing StopReason reset.
func TestAnthropicResponsesStreamState_StopDetailsResetOnAcquire(t *testing.T) {
	t.Parallel()

	state := AcquireAnthropicResponsesStreamState()
	state.StopDetails = &AnthropicStopDetails{Type: "refusal"}
	ReleaseAnthropicResponsesStreamState(state) // exercises flush()

	reused := AcquireAnthropicResponsesStreamState()
	if reused.StopDetails != nil {
		t.Errorf("StopDetails leaked across pooled acquisitions: got %+v, want nil", reused.StopDetails)
	}
	ReleaseAnthropicResponsesStreamState(reused)
}

// TestToBifrostChatCompletionStream_RefusalMessageDelta verifies that a streaming
// Chat Completions message_delta event carrying stop_reason "refusal" surfaces
// the explanation via delta.refusal on the final chunk.
func TestToBifrostChatCompletionStream_RefusalMessageDelta(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		delta           *AnthropicStreamDelta
		expectedRefusal *string
	}{
		{
			name: "refusal with explanation",
			delta: &AnthropicStreamDelta{
				StopReason: schemas.Ptr(AnthropicStopReasonRefusal),
				StopDetails: &AnthropicStopDetails{
					Type:        "refusal",
					Explanation: schemas.Ptr("This request involves prohibited content."),
				},
			},
			expectedRefusal: schemas.Ptr("This request involves prohibited content."),
		},
		{
			name: "refusal without stop_details",
			delta: &AnthropicStreamDelta{
				StopReason: schemas.Ptr(AnthropicStopReasonRefusal),
			},
			expectedRefusal: schemas.Ptr("The model declined to respond."),
		},
		{
			name: "non-refusal stop reason",
			delta: &AnthropicStreamDelta{
				StopReason: schemas.Ptr(AnthropicStopReasonEndTurn),
			},
			expectedRefusal: nil,
		},
		{
			name:            "nil delta",
			delta:           nil,
			expectedRefusal: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Exercises the exact function HandleAnthropicChatCompletionStreaming calls
			// (core/providers/anthropic/anthropic.go) to populate delta.refusal on the
			// terminal chunk — not a hand-rolled duplicate of the production logic.
			got := RefusalExplanationFromStreamDelta(tt.delta)

			if tt.expectedRefusal == nil {
				if got != nil {
					t.Errorf("RefusalExplanationFromStreamDelta() = %q, want nil", *got)
				}
				return
			}
			if got == nil || *got != *tt.expectedRefusal {
				t.Errorf("RefusalExplanationFromStreamDelta() = %v, want %q", got, *tt.expectedRefusal)
			}
		})
	}
}
