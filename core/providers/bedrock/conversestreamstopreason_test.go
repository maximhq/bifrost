package bedrock

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// messageStopReason extracts the stopReason carried by the single messageStop
// event in the encoded stream, failing the test if none or several are present.
func messageStopReason(t *testing.T, events []BedrockEncodedEvent) string {
	t.Helper()
	var reasons []string
	for _, event := range events {
		if event.EventType != "messageStop" {
			continue
		}
		payload, ok := event.Payload.(BedrockMessageStopEvent)
		if !ok {
			t.Fatalf("messageStop payload has unexpected type %T", event.Payload)
		}
		reasons = append(reasons, payload.StopReason)
	}
	if len(reasons) != 1 {
		t.Fatalf("want exactly one messageStop event, got %d", len(reasons))
	}
	return reasons[0]
}

// toolUseLifecycle returns the Bifrost stream events for a turn where the model
// calls a tool, ending with a response.completed built by an ingress converter.
// completed mirrors what real ingress paths produce: the Anthropic converter
// sets Response.StopReason ("tool_calls") and Response.Output; others may set
// only Output.
func toolUseLifecycle(completed *schemas.BifrostResponsesResponse) []*schemas.BifrostResponsesStreamResponse {
	functionCallType := schemas.ResponsesMessageTypeFunctionCall
	toolItem := &schemas.ResponsesMessage{
		Type: &functionCallType,
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID: schemas.Ptr("call_1"),
			Name:   schemas.Ptr("get_weather"),
		},
	}
	return []*schemas.BifrostResponsesStreamResponse{
		{Type: schemas.ResponsesStreamResponseTypeCreated},
		{
			Type:         schemas.ResponsesStreamResponseTypeOutputItemAdded,
			OutputIndex:  schemas.Ptr(0),
			ContentIndex: schemas.Ptr(0),
			Item:         toolItem,
		},
		{
			Type:         schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
			ContentIndex: schemas.Ptr(0),
			Delta:        schemas.Ptr(`{"location":"Paris"}`),
		},
		{
			Type:         schemas.ResponsesStreamResponseTypeOutputItemDone,
			OutputIndex:  schemas.Ptr(0),
			ContentIndex: schemas.Ptr(0),
			Item:         toolItem,
		},
		{
			Type:     schemas.ResponsesStreamResponseTypeCompleted,
			Response: completed,
		},
	}
}

// TestConverseStreamToolUseStopReason covers a tool-use turn whose completed
// response carries the Bifrost stop reason, as the Anthropic stream ingress
// produces. The Converse egress must report stopReason "tool_use" on
// messageStop, matching the non-streaming Converse path (issue #5206).
func TestConverseStreamToolUseStopReason(t *testing.T) {
	functionCallType := schemas.ResponsesMessageTypeFunctionCall
	chunks := toolUseLifecycle(&schemas.BifrostResponsesResponse{
		StopReason: schemas.Ptr("tool_calls"),
		Output: []schemas.ResponsesMessage{
			{Type: &functionCallType},
		},
		Usage: &schemas.ResponsesResponseUsage{InputTokens: 15, OutputTokens: 8, TotalTokens: 23},
	})

	events := encodeConverseStream(t, chunks)

	if got := messageStopReason(t, events); got != "tool_use" {
		t.Errorf("messageStop stopReason: want %q, got %q", "tool_use", got)
	}
}

// TestConverseStreamToolUseStopReasonFromOutput covers ingress paths that leave
// Response.StopReason unset on completed: a function_call item in the output
// array must still yield stopReason "tool_use", mirroring the tool-use fallback
// of the non-streaming converter.
func TestConverseStreamToolUseStopReasonFromOutput(t *testing.T) {
	functionCallType := schemas.ResponsesMessageTypeFunctionCall
	chunks := toolUseLifecycle(&schemas.BifrostResponsesResponse{
		Output: []schemas.ResponsesMessage{
			{Type: &functionCallType},
		},
		Usage: &schemas.ResponsesResponseUsage{InputTokens: 15, OutputTokens: 8, TotalTokens: 23},
	})

	events := encodeConverseStream(t, chunks)

	if got := messageStopReason(t, events); got != "tool_use" {
		t.Errorf("messageStop stopReason: want %q, got %q", "tool_use", got)
	}
}

// TestConverseStreamEndTurnStopReason ensures a plain text turn still reports
// end_turn, both when the completed response carries StopReason "stop" and when
// it carries no stop reason at all.
func TestConverseStreamEndTurnStopReason(t *testing.T) {
	messageType := schemas.ResponsesMessageTypeMessage
	for name, completed := range map[string]*schemas.BifrostResponsesResponse{
		"explicit stop": {
			StopReason: schemas.Ptr("stop"),
			Output:     []schemas.ResponsesMessage{{Type: &messageType}},
		},
		"no stop reason": {
			Output: []schemas.ResponsesMessage{{Type: &messageType}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			chunks := []*schemas.BifrostResponsesStreamResponse{
				{Type: schemas.ResponsesStreamResponseTypeCreated},
				{
					Type:        schemas.ResponsesStreamResponseTypeOutputItemAdded,
					OutputIndex: schemas.Ptr(0),
					Item:        &schemas.ResponsesMessage{Type: &messageType},
				},
				{
					Type:         schemas.ResponsesStreamResponseTypeOutputTextDelta,
					ContentIndex: schemas.Ptr(0),
					Delta:        schemas.Ptr("Hello"),
				},
				{
					Type:         schemas.ResponsesStreamResponseTypeOutputItemDone,
					OutputIndex:  schemas.Ptr(0),
					ContentIndex: schemas.Ptr(0),
					Item:         &schemas.ResponsesMessage{Type: &messageType},
				},
				{
					Type:     schemas.ResponsesStreamResponseTypeCompleted,
					Response: completed,
				},
			}

			events := encodeConverseStream(t, chunks)

			if got := messageStopReason(t, events); got != "end_turn" {
				t.Errorf("messageStop stopReason: want %q, got %q", "end_turn", got)
			}
		})
	}
}

// TestConverseStreamIncompleteDetailsStopReason covers completed responses that
// carry no StopReason but flag truncation or filtering via IncompleteDetails:
// the reason must be translated to Bedrock's stopReason vocabulary, not passed
// through verbatim.
func TestConverseStreamIncompleteDetailsStopReason(t *testing.T) {
	for reason, want := range map[string]string{
		schemas.ResponsesResponseIncompleteReasonMaxOutputTokens: "max_tokens",
		schemas.ResponsesResponseIncompleteReasonContentFilter:   "content_filtered",
	} {
		t.Run(reason, func(t *testing.T) {
			chunks := []*schemas.BifrostResponsesStreamResponse{
				{Type: schemas.ResponsesStreamResponseTypeCreated},
				{
					Type:         schemas.ResponsesStreamResponseTypeOutputTextDelta,
					ContentIndex: schemas.Ptr(0),
					Delta:        schemas.Ptr("truncat"),
				},
				{
					Type: schemas.ResponsesStreamResponseTypeCompleted,
					Response: &schemas.BifrostResponsesResponse{
						IncompleteDetails: &schemas.ResponsesResponseIncompleteDetails{Reason: reason},
					},
				},
			}

			events := encodeConverseStream(t, chunks)

			if got := messageStopReason(t, events); got != want {
				t.Errorf("messageStop stopReason: want %q, got %q", want, got)
			}
		})
	}
}

// TestConverseStreamLengthStopReason ensures a truncation stop reason maps to
// Bedrock's max_tokens on messageStop.
func TestConverseStreamLengthStopReason(t *testing.T) {
	messageType := schemas.ResponsesMessageTypeMessage
	chunks := []*schemas.BifrostResponsesStreamResponse{
		{Type: schemas.ResponsesStreamResponseTypeCreated},
		{
			Type:         schemas.ResponsesStreamResponseTypeOutputTextDelta,
			ContentIndex: schemas.Ptr(0),
			Delta:        schemas.Ptr("truncat"),
		},
		{
			Type: schemas.ResponsesStreamResponseTypeCompleted,
			Response: &schemas.BifrostResponsesResponse{
				StopReason: schemas.Ptr("length"),
				Output:     []schemas.ResponsesMessage{{Type: &messageType}},
			},
		},
	}

	events := encodeConverseStream(t, chunks)

	if got := messageStopReason(t, events); got != "max_tokens" {
		t.Errorf("messageStop stopReason: want %q, got %q", "max_tokens", got)
	}
}
