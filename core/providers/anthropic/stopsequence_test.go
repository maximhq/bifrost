package anthropic

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// A turn that ended on a requested stop sequence must reach Anthropic-compatible
// clients as stop_reason "stop_sequence" with the matched string, not end_turn/null,
// after the Anthropic -> Bifrost Responses -> Anthropic round trip.

// assertStopFields checks that a converted stop_reason and stop_sequence match the
// expected pair, treating a nil wantSeq as requiring a null stop_sequence.
func assertStopFields(t *testing.T, gotReason AnthropicStopReason, gotSeq *string, wantReason AnthropicStopReason, wantSeq *string) {
	t.Helper()
	if gotReason != wantReason {
		t.Errorf("stop_reason = %q, want %q", gotReason, wantReason)
	}
	switch {
	case wantSeq == nil && gotSeq != nil:
		t.Errorf("stop_sequence = %q, want null", *gotSeq)
	case wantSeq != nil && (gotSeq == nil || *gotSeq != *wantSeq):
		t.Errorf("stop_sequence = %v, want %q", gotSeq, *wantSeq)
	}
}

// TestStopSequence_NonStreamingRoundTrip verifies that a non-streaming Anthropic message
// keeps its stop_reason and matched stop_sequence through the Responses round trip, and
// that a stray sequence on a non-stop_sequence reason is dropped.
func TestStopSequence_NonStreamingRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		reason     AnthropicStopReason
		sequence   *string
		wantReason AnthropicStopReason
		wantSeq    *string
	}{
		{"stop_sequence keeps reason and match", AnthropicStopReasonStopSequence, schemas.Ptr("###"), AnthropicStopReasonStopSequence, schemas.Ptr("###")},
		{"end_turn stays end_turn", AnthropicStopReasonEndTurn, nil, AnthropicStopReasonEndTurn, nil},
		{"stray sequence on end_turn is dropped", AnthropicStopReasonEndTurn, schemas.Ptr("###"), AnthropicStopReasonEndTurn, nil},
		{"max_tokens unaffected", AnthropicStopReasonMaxTokens, nil, AnthropicStopReasonMaxTokens, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
			defer cancel()

			anthropicResp := &AnthropicMessageResponse{
				ID:           "msg_stopseq",
				Type:         "message",
				Role:         "assistant",
				Model:        "claude-sonnet-4-5",
				StopReason:   tt.reason,
				StopSequence: tt.sequence,
				Content:      []AnthropicContentBlock{{Type: AnthropicContentBlockTypeText, Text: schemas.Ptr("1 2 3")}},
			}
			result := ToAnthropicResponsesResponse(ctx, anthropicResp.ToBifrostResponsesResponse(ctx))
			assertStopFields(t, result.StopReason, result.StopSequence, tt.wantReason, tt.wantSeq)
		})
	}
}

// TestStopSequence_BifrostStopWithoutSequenceIsEndTurn verifies that the ambiguous "stop"
// OpenAI-style providers report with no matched sequence keeps mapping to end_turn
// rather than guessing stop_sequence.
func TestStopSequence_BifrostStopWithoutSequenceIsEndTurn(t *testing.T) {
	t.Parallel()
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	result := ToAnthropicResponsesResponse(ctx, &schemas.BifrostResponsesResponse{
		Model:      "gpt-5.1",
		StopReason: schemas.Ptr(string(schemas.BifrostFinishReasonStop)),
	})
	assertStopFields(t, result.StopReason, result.StopSequence, AnthropicStopReasonEndTurn, nil)
}

// TestStopSequence_StreamingRoundTrip verifies that stop_reason and stop_sequence survive
// streaming conversion, both on the relayed message_delta and on the message_delta
// synthesized from response.completed.
func TestStopSequence_StreamingRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		reason     AnthropicStopReason
		sequence   *string
		wantReason AnthropicStopReason
		wantSeq    *string
	}{
		{"stop_sequence", AnthropicStopReasonStopSequence, schemas.Ptr("END"), AnthropicStopReasonStopSequence, schemas.Ptr("END")},
		{"end_turn", AnthropicStopReasonEndTurn, nil, AnthropicStopReasonEndTurn, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ingressCtx := context.WithValue(context.Background(), schemas.BifrostContextKeyIntegrationType, "anthropic")
			state := newFallbackStreamState()

			delta := &AnthropicStreamEvent{
				Type:  AnthropicStreamEventTypeMessageDelta,
				Delta: &AnthropicStreamDelta{StopReason: schemas.Ptr(tt.reason), StopSequence: tt.sequence},
				Usage: &AnthropicUsage{OutputTokens: 3},
			}
			deltaResps, bErr, _ := delta.ToBifrostResponsesStream(ingressCtx, 1, state)
			if bErr != nil || len(deltaResps) != 1 || deltaResps[0].Response == nil {
				t.Fatalf("unexpected message_delta conversion: %v %+v", bErr, deltaResps)
			}
			stop := &AnthropicStreamEvent{Type: AnthropicStreamEventTypeMessageStop}
			stopResps, bErr, _ := stop.ToBifrostResponsesStream(ingressCtx, 2, state)
			if bErr != nil || len(stopResps) != 1 || stopResps[0].Response == nil {
				t.Fatalf("unexpected message_stop conversion: %v %+v", bErr, stopResps)
			}

			// Egress of the relayed message_delta event.
			egressCtx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
			defer cancel()
			egressCtx.SetValue(schemas.BifrostContextKeyIntegrationType, "anthropic")
			events := ToAnthropicResponsesStreamResponse(egressCtx, deltaResps[0])
			if len(events) == 0 || events[0].Delta == nil || events[0].Delta.StopReason == nil {
				t.Fatalf("message_delta egress missing stop_reason: %+v", events)
			}
			assertStopFields(t, *events[0].Delta.StopReason, events[0].Delta.StopSequence, tt.wantReason, tt.wantSeq)

			// Egress when message_delta is synthesized from response.completed.
			completedCtx, cancel2 := schemas.NewBifrostContextWithCancel(context.Background())
			defer cancel2()
			events = ToAnthropicResponsesStreamResponse(completedCtx, stopResps[0])
			if len(events) != 2 || events[0].Delta == nil || events[0].Delta.StopReason == nil {
				t.Fatalf("completed egress missing message_delta: %+v", events)
			}
			assertStopFields(t, *events[0].Delta.StopReason, events[0].Delta.StopSequence, tt.wantReason, tt.wantSeq)
		})
	}
}

// TestStopSequence_ChatResponseEgress verifies that a chat response's StopString becomes
// stop_reason "stop_sequence" with the matched string, while a plain stop maps to end_turn.
func TestStopSequence_ChatResponseEgress(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		stopString *string
		wantReason AnthropicStopReason
		wantSeq    *string
	}{
		{"matched sequence", schemas.Ptr("###"), AnthropicStopReasonStopSequence, schemas.Ptr("###")},
		{"plain stop", nil, AnthropicStopReasonEndTurn, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := ToAnthropicChatResponse(&schemas.BifrostChatResponse{
				Model: "claude-sonnet-4-5",
				Choices: []schemas.BifrostResponseChoice{{
					FinishReason: schemas.Ptr(string(schemas.BifrostFinishReasonStop)),
					ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
						Message: &schemas.ChatMessage{
							Role:    schemas.ChatMessageRoleAssistant,
							Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("1 2 3")},
						},
						StopString: tt.stopString,
					},
				}},
			})
			assertStopFields(t, result.StopReason, result.StopSequence, tt.wantReason, tt.wantSeq)
		})
	}
}
