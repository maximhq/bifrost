package bedrock

import (
	"reflect"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// encodeConverseStream runs each Bifrost stream event through the Converse stream
// converter and flattens the results into wire-ready encoded events, in order,
// the same way the HTTP transport does before AWS Event Stream encoding.
func encodeConverseStream(t *testing.T, chunks []*schemas.BifrostResponsesStreamResponse) []BedrockEncodedEvent {
	t.Helper()
	var events []BedrockEncodedEvent
	for i, chunk := range chunks {
		bedrockEvent, err := ToBedrockConverseStreamResponse(chunk)
		if err != nil {
			t.Fatalf("chunk %d (%s): unexpected error: %v", i, chunk.Type, err)
		}
		if bedrockEvent == nil {
			continue
		}
		events = append(events, bedrockEvent.ToEncodedEvents()...)
	}
	return events
}

func encodedEventTypes(events []BedrockEncodedEvent) []string {
	types := make([]string, 0, len(events))
	for _, event := range events {
		types = append(types, event.EventType)
	}
	return types
}

func contentBlockStopPayloads(t *testing.T, events []BedrockEncodedEvent) []string {
	t.Helper()
	var payloads []string
	for _, event := range events {
		if event.EventType != "contentBlockStop" {
			continue
		}
		data, err := sonic.Marshal(event.Payload)
		if err != nil {
			t.Fatalf("marshal contentBlockStop payload: %v", err)
		}
		payloads = append(payloads, string(data))
	}
	return payloads
}

// TestConverseStreamTextBlockEmitsContentBlockStop replays the stream lifecycle of a
// plain text response and asserts the Converse egress closes the text block with a
// contentBlockStop event before messageStop, as the Bedrock ConverseStream contract
// requires (issue #4262). Text blocks have no contentBlockStart (the ContentBlockStart
// union has no text member), so the block is opened implicitly by its first delta.
func TestConverseStreamTextBlockEmitsContentBlockStop(t *testing.T) {
	messageType := schemas.ResponsesMessageTypeMessage
	chunks := []*schemas.BifrostResponsesStreamResponse{
		{Type: schemas.ResponsesStreamResponseTypeCreated},
		{Type: schemas.ResponsesStreamResponseTypeInProgress},
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
			Type:         schemas.ResponsesStreamResponseTypeOutputTextDelta,
			ContentIndex: schemas.Ptr(0),
			Delta:        schemas.Ptr(" world"),
		},
		{
			Type:         schemas.ResponsesStreamResponseTypeOutputTextDone,
			ContentIndex: schemas.Ptr(0),
			Text:         schemas.Ptr("Hello world"),
		},
		{
			Type:         schemas.ResponsesStreamResponseTypeContentPartDone,
			ContentIndex: schemas.Ptr(0),
		},
		{
			Type:         schemas.ResponsesStreamResponseTypeOutputItemDone,
			OutputIndex:  schemas.Ptr(0),
			ContentIndex: schemas.Ptr(0),
			Item:         &schemas.ResponsesMessage{Type: &messageType},
		},
		{
			Type: schemas.ResponsesStreamResponseTypeCompleted,
			Response: &schemas.BifrostResponsesResponse{
				Usage: &schemas.ResponsesResponseUsage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12},
			},
		},
	}

	events := encodeConverseStream(t, chunks)

	want := []string{"messageStart", "contentBlockDelta", "contentBlockDelta", "contentBlockStop", "messageStop", "metadata"}
	if got := encodedEventTypes(events); !reflect.DeepEqual(got, want) {
		t.Fatalf("event sequence mismatch:\n  want %v\n  got  %v", want, got)
	}

	stops := contentBlockStopPayloads(t, events)
	if len(stops) != 1 || stops[0] != `{"contentBlockIndex":0}` {
		t.Errorf(`contentBlockStop payloads: want [{"contentBlockIndex":0}], got %v`, stops)
	}
}

// TestConverseStreamToolUseBlockEmitsContentBlockStop covers a tool use block: the
// contentBlockStart(toolUse) opened at OutputItemAdded must be closed by a
// contentBlockStop carrying the same block index, before messageStop.
func TestConverseStreamToolUseBlockEmitsContentBlockStop(t *testing.T) {
	functionCallType := schemas.ResponsesMessageTypeFunctionCall
	toolItem := &schemas.ResponsesMessage{
		Type: &functionCallType,
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID: schemas.Ptr("call_1"),
			Name:   schemas.Ptr("get_weather"),
		},
	}
	chunks := []*schemas.BifrostResponsesStreamResponse{
		{Type: schemas.ResponsesStreamResponseTypeCreated},
		{
			Type:         schemas.ResponsesStreamResponseTypeOutputItemAdded,
			OutputIndex:  schemas.Ptr(1),
			ContentIndex: schemas.Ptr(1),
			Item:         toolItem,
		},
		{
			Type:         schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
			ContentIndex: schemas.Ptr(1),
			Delta:        schemas.Ptr(`{"location":"Paris"}`),
		},
		{
			Type:         schemas.ResponsesStreamResponseTypeOutputItemDone,
			OutputIndex:  schemas.Ptr(1),
			ContentIndex: schemas.Ptr(1),
			Item:         toolItem,
		},
		{
			Type: schemas.ResponsesStreamResponseTypeCompleted,
			Response: &schemas.BifrostResponsesResponse{
				Usage: &schemas.ResponsesResponseUsage{InputTokens: 15, OutputTokens: 8, TotalTokens: 23},
			},
		},
	}

	events := encodeConverseStream(t, chunks)

	want := []string{"messageStart", "contentBlockStart", "contentBlockDelta", "contentBlockStop", "messageStop", "metadata"}
	if got := encodedEventTypes(events); !reflect.DeepEqual(got, want) {
		t.Fatalf("event sequence mismatch:\n  want %v\n  got  %v", want, got)
	}

	stops := contentBlockStopPayloads(t, events)
	if len(stops) != 1 || stops[0] != `{"contentBlockIndex":1}` {
		t.Errorf(`contentBlockStop payloads: want [{"contentBlockIndex":1}], got %v`, stops)
	}
}

func TestConverseStreamToolUseSetsToolUseStopReason(t *testing.T) {
	functionCallType := schemas.ResponsesMessageTypeFunctionCall
	toolItem := &schemas.ResponsesMessage{
		Type: &functionCallType,
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID: schemas.Ptr("call_1"),
			Name:   schemas.Ptr("get_weather"),
		},
	}
	chunks := []*schemas.BifrostResponsesStreamResponse{
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
			Type: schemas.ResponsesStreamResponseTypeCompleted,
			Response: &schemas.BifrostResponsesResponse{
				StopReason: schemas.Ptr(string(schemas.BifrostFinishReasonToolCalls)),
			},
		},
	}

	events := encodeConverseStream(t, chunks)
	for _, event := range events {
		if event.EventType != "messageStop" {
			continue
		}
		messageStop, ok := event.Payload.(BedrockMessageStopEvent)
		if !ok {
			t.Fatalf("messageStop payload has unexpected type %T", event.Payload)
		}
		if messageStop.StopReason != "tool_use" {
			t.Fatalf("messageStop stopReason: want %q, got %q", "tool_use", messageStop.StopReason)
		}
		return
	}

	t.Fatal("messageStop event not found")
}

// TestConverseStreamReasoningBlockEmitsContentBlockStop covers a reasoning block:
// reasoning deltas stream as contentBlockDelta(reasoningContent) and the block is
// closed by the contentBlockStop emitted on the reasoning item's OutputItemDone.
func TestConverseStreamReasoningBlockEmitsContentBlockStop(t *testing.T) {
	reasoningType := schemas.ResponsesMessageTypeReasoning
	chunks := []*schemas.BifrostResponsesStreamResponse{
		{
			Type:         schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta,
			ContentIndex: schemas.Ptr(0),
			Delta:        schemas.Ptr("thinking"),
		},
		{
			Type:         schemas.ResponsesStreamResponseTypeReasoningSummaryTextDone,
			ContentIndex: schemas.Ptr(0),
		},
		{
			Type:         schemas.ResponsesStreamResponseTypeOutputItemDone,
			OutputIndex:  schemas.Ptr(0),
			ContentIndex: schemas.Ptr(0),
			Item:         &schemas.ResponsesMessage{Type: &reasoningType},
		},
	}

	events := encodeConverseStream(t, chunks)

	want := []string{"contentBlockDelta", "contentBlockStop"}
	if got := encodedEventTypes(events); !reflect.DeepEqual(got, want) {
		t.Fatalf("event sequence mismatch:\n  want %v\n  got  %v", want, got)
	}

	stops := contentBlockStopPayloads(t, events)
	if len(stops) != 1 || stops[0] != `{"contentBlockIndex":0}` {
		t.Errorf(`contentBlockStop payloads: want [{"contentBlockIndex":0}], got %v`, stops)
	}
}
