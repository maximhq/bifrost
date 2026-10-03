package anthropic

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// Translated upstreams report a natural stop on tool-call turns: Gemini maps
// finishReason STOP to "stop", and OpenAI-shaped Responses providers send no stop
// reason at all. Anthropic clients dispatch tools on stop_reason == "tool_use", so
// the Anthropic egress must report tool_use for such turns, both streaming and not,
// without overriding truncation or refusal.

func stopReasonTestFunctionCall() schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		ID:   schemas.Ptr("call_1"),
		Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID:    schemas.Ptr("call_1"),
			Name:      schemas.Ptr("get_weather"),
			Arguments: schemas.Ptr(`{"city":"Paris"}`),
		},
	}
}

func stopReasonTestText() schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		ID:   schemas.Ptr("msg_1"),
		Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
		Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
		Content: &schemas.ResponsesMessageContent{
			ContentBlocks: []schemas.ResponsesMessageContentBlock{{
				Type: schemas.ResponsesOutputMessageContentTypeText,
				Text: schemas.Ptr("Let me check."),
			}},
		},
	}
}

func maxOutputTokensDetails() *schemas.ResponsesResponseIncompleteDetails {
	return &schemas.ResponsesResponseIncompleteDetails{Reason: schemas.ResponsesResponseIncompleteReasonMaxOutputTokens}
}

func TestToAnthropicResponsesResponse_ToolCallTurnReportsToolUse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		stopReason *string
		incomplete *schemas.ResponsesResponseIncompleteDetails
		output     []schemas.ResponsesMessage
		want       AnthropicStopReason
	}{
		{
			name:       "gemini-shaped: explicit stop with function call",
			stopReason: schemas.Ptr("stop"),
			output:     []schemas.ResponsesMessage{stopReasonTestText(), stopReasonTestFunctionCall()},
			want:       AnthropicStopReasonToolUse,
		},
		{
			name:   "openai-shaped: no stop reason with function call",
			output: []schemas.ResponsesMessage{stopReasonTestFunctionCall()},
			want:   AnthropicStopReasonToolUse,
		},
		{
			name:       "explicit stop without tool call stays end_turn",
			stopReason: schemas.Ptr("stop"),
			output:     []schemas.ResponsesMessage{stopReasonTestText()},
			want:       AnthropicStopReasonEndTurn,
		},
		{
			name:       "length with function call stays max_tokens",
			stopReason: schemas.Ptr("length"),
			output:     []schemas.ResponsesMessage{stopReasonTestFunctionCall()},
			want:       AnthropicStopReasonMaxTokens,
		},
		{
			name:       "incomplete max_output_tokens with function call stays max_tokens",
			incomplete: maxOutputTokensDetails(),
			output:     []schemas.ResponsesMessage{stopReasonTestFunctionCall()},
			want:       AnthropicStopReasonMaxTokens,
		},
		{
			name:       "pause_turn with function call is preserved",
			stopReason: schemas.Ptr(string(AnthropicStopReasonPauseTurn)),
			output:     []schemas.ResponsesMessage{stopReasonTestFunctionCall()},
			want:       AnthropicStopReasonPauseTurn,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
			defer cancel()

			result := ToAnthropicResponsesResponse(ctx, &schemas.BifrostResponsesResponse{
				ID:                schemas.Ptr("resp_1"),
				Model:             "gemini-2.5-pro",
				StopReason:        tt.stopReason,
				IncompleteDetails: tt.incomplete,
				Output:            tt.output,
			})
			if result.StopReason != tt.want {
				t.Errorf("stop_reason = %q, want %q", result.StopReason, tt.want)
			}
		})
	}
}

func TestToAnthropicResponsesStreamResponse_ToolCallTurnReportsToolUse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		provider   schemas.ModelProvider
		streamTool bool // stream a function_call item before response.completed
		stopReason *string
		incomplete *schemas.ResponsesResponseIncompleteDetails
		output     []schemas.ResponsesMessage // Output carried on response.completed
		want       AnthropicStopReason
	}{
		{
			name:       "gemini-shaped: streamed function call, completed with stop",
			provider:   schemas.Gemini,
			streamTool: true,
			stopReason: schemas.Ptr("stop"),
			want:       AnthropicStopReasonToolUse,
		},
		{
			name:       "openai-shaped: streamed function call, completed without stop reason",
			provider:   schemas.OpenAI,
			streamTool: true,
			want:       AnthropicStopReasonToolUse,
		},
		{
			name:     "openai-shaped: function call only in completed output",
			provider: schemas.OpenAI,
			output:   []schemas.ResponsesMessage{stopReasonTestFunctionCall()},
			want:     AnthropicStopReasonToolUse,
		},
		{
			name:       "text-only turn stays end_turn",
			provider:   schemas.Gemini,
			stopReason: schemas.Ptr("stop"),
			output:     []schemas.ResponsesMessage{stopReasonTestText()},
			want:       AnthropicStopReasonEndTurn,
		},
		{
			name:       "length with streamed function call stays max_tokens",
			provider:   schemas.Gemini,
			streamTool: true,
			stopReason: schemas.Ptr("length"),
			want:       AnthropicStopReasonMaxTokens,
		},
		{
			name:       "incomplete max_output_tokens with streamed function call stays max_tokens",
			provider:   schemas.OpenAI,
			streamTool: true,
			incomplete: maxOutputTokensDetails(),
			want:       AnthropicStopReasonMaxTokens,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			extra := schemas.BifrostResponseExtraFields{Provider: tt.provider}
			var frames []*schemas.BifrostResponsesStreamResponse
			if tt.streamTool {
				item := stopReasonTestFunctionCall()
				item.Arguments = nil
				frames = append(frames,
					&schemas.BifrostResponsesStreamResponse{
						Type:        schemas.ResponsesStreamResponseTypeOutputItemAdded,
						OutputIndex: schemas.Ptr(0),
						Item:        &item,
						ExtraFields: extra,
					},
					&schemas.BifrostResponsesStreamResponse{
						Type:        schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
						OutputIndex: schemas.Ptr(0),
						ItemID:      item.ID,
						Delta:       schemas.Ptr(`{"city":"Paris"}`),
						ExtraFields: extra,
					},
					&schemas.BifrostResponsesStreamResponse{
						Type:        schemas.ResponsesStreamResponseTypeOutputItemDone,
						OutputIndex: schemas.Ptr(0),
						Item:        &item,
						ExtraFields: extra,
					},
				)
			}
			frames = append(frames, &schemas.BifrostResponsesStreamResponse{
				Type: schemas.ResponsesStreamResponseTypeCompleted,
				Response: &schemas.BifrostResponsesResponse{
					ID:                schemas.Ptr("resp_1"),
					StopReason:        tt.stopReason,
					IncompleteDetails: tt.incomplete,
					Output:            tt.output,
				},
				ExtraFields: extra,
			})

			var messageDelta *AnthropicStreamEvent
			for _, event := range driveAnthropicEgress(t, frames) {
				if event != nil && event.Type == AnthropicStreamEventTypeMessageDelta {
					messageDelta = event
				}
			}
			if messageDelta == nil || messageDelta.Delta == nil || messageDelta.Delta.StopReason == nil {
				t.Fatalf("expected a message_delta carrying stop_reason, got %#v", messageDelta)
			}
			if got := *messageDelta.Delta.StopReason; got != tt.want {
				t.Errorf("stop_reason = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestToAnthropicResponsesStreamResponse_CompletedStopSequenceIsNull guards the
// message_delta wire shape: with no stop sequence matched, Anthropic sends
// "stop_sequence": null, never an empty string.
func TestToAnthropicResponsesStreamResponse_CompletedStopSequenceIsNull(t *testing.T) {
	t.Parallel()

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	events := ToAnthropicResponsesStreamResponse(ctx, &schemas.BifrostResponsesStreamResponse{
		Type:     schemas.ResponsesStreamResponseTypeCompleted,
		Response: &schemas.BifrostResponsesResponse{ID: schemas.Ptr("resp_1")},
	})
	if len(events) != 2 || events[0].Type != AnthropicStreamEventTypeMessageDelta {
		t.Fatalf("expected message_delta + message_stop, got %#v", events)
	}
	raw, err := json.Marshal(events[0].Delta)
	if err != nil {
		t.Fatalf("marshal message_delta delta: %v", err)
	}
	if !strings.Contains(string(raw), `"stop_sequence":null`) {
		t.Errorf("message_delta delta = %s, want \"stop_sequence\":null", raw)
	}
}
