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

// Issue #7601: Anthropic, Gemini, OpenAI and Bedrock itself end a truncated or filtered
// Responses stream with response.incomplete. The Converse egress handled only
// response.completed, so that terminal was dropped: no messageStop, no stopReason, no
// usage. It must close the stream with the Bedrock-vocabulary stop reason instead.
func TestConverseStreamIncompleteEmitsMessageStop(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response *schemas.BifrostResponsesResponse
		want     string
	}{
		{"stop reason length", &schemas.BifrostResponsesResponse{
			StopReason:        schemas.Ptr("length"),
			IncompleteDetails: &schemas.ResponsesResponseIncompleteDetails{Reason: schemas.ResponsesResponseIncompleteReasonMaxOutputTokens},
		}, "max_tokens"},
		{"incomplete_details only: max_output_tokens", &schemas.BifrostResponsesResponse{
			IncompleteDetails: &schemas.ResponsesResponseIncompleteDetails{Reason: schemas.ResponsesResponseIncompleteReasonMaxOutputTokens},
		}, "max_tokens"},
		{"incomplete_details only: content_filter", &schemas.BifrostResponsesResponse{
			IncompleteDetails: &schemas.ResponsesResponseIncompleteDetails{Reason: schemas.ResponsesResponseIncompleteReasonContentFilter},
		}, "content_filtered"},
		// An Anthropic refusal carries both fields; Bedrock has no "refusal" stop reason.
		{"anthropic refusal: stop reason and content_filter details", &schemas.BifrostResponsesResponse{
			StopReason:        schemas.Ptr("refusal"),
			IncompleteDetails: &schemas.ResponsesResponseIncompleteDetails{Reason: schemas.ResponsesResponseIncompleteReasonContentFilter},
		}, "content_filtered"},
		{"anthropic refusal: stop reason only", &schemas.BifrostResponsesResponse{
			StopReason: schemas.Ptr("refusal"),
		}, "content_filtered"},
		// An unrecognized reason must never leak into messageStop; fall back to a valid value.
		{"unrecognized incomplete reason, no stop reason", &schemas.BifrostResponsesResponse{
			IncompleteDetails: &schemas.ResponsesResponseIncompleteDetails{Reason: "quota_exceeded"},
		}, "end_turn"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event, err := ToBedrockConverseStreamResponse(&schemas.BifrostResponsesStreamResponse{
				Type:     schemas.ResponsesStreamResponseTypeIncomplete,
				Response: tc.response,
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if event == nil || event.StopReason == nil {
				t.Fatalf("response.incomplete produced no messageStop (event=%+v)", event)
			}
			if *event.StopReason != tc.want {
				t.Errorf("stopReason = %q, want %q", *event.StopReason, tc.want)
			}
		})
	}
}

// driveConverseStopStream replays a ConverseStream text answer that ends with the given
// messageStop payload through the upstream converter, finalizes it, and returns the
// /bedrock converse-stream egress events.
func driveConverseStopStream(tb testing.TB, messageStop string) []BedrockEncodedEvent {
	tb.Helper()
	state := acquireBedrockResponsesStreamState()
	defer releaseBedrockResponsesStreamState(state)
	state.Model = schemas.Ptr("global.anthropic.claude-opus-4-7")

	var stopEvent BedrockStreamEvent
	if err := sonic.Unmarshal([]byte(messageStop), &stopEvent); err != nil {
		tb.Fatalf("decode messageStop: %v", err)
	}
	upstream := []*BedrockStreamEvent{
		{Role: schemas.Ptr("assistant")},
		{ContentBlockIndex: schemas.Ptr(0), Delta: &BedrockContentBlockDelta{Text: schemas.Ptr("one, two, ")}},
		{ContentBlockIndex: schemas.Ptr(0), ContentBlockStop: true},
		&stopEvent,
	}
	var chunks []*schemas.BifrostResponsesStreamResponse
	seq := 0
	for _, event := range upstream {
		responses, bErr, _ := event.ToBifrostResponsesStream(seq, state)
		if bErr != nil {
			tb.Fatalf("upstream event produced an error: %+v", bErr)
		}
		chunks = append(chunks, responses...)
		seq += len(responses)
	}
	usage := &schemas.ResponsesResponseUsage{InputTokens: 20, OutputTokens: 6, TotalTokens: 26}
	chunks = append(chunks, FinalizeBedrockStream(state, seq, usage, nil)...)

	var events []BedrockEncodedEvent
	for _, chunk := range chunks {
		bedrockEvent, err := ToBedrockConverseStreamResponse(chunk)
		if err != nil {
			tb.Fatalf("chunk %s: %v", chunk.Type, err)
		}
		if bedrockEvent != nil {
			events = append(events, bedrockEvent.ToEncodedEvents()...)
		}
	}
	return events
}

func messageStopPayload(t *testing.T, events []BedrockEncodedEvent) string {
	t.Helper()
	for _, event := range events {
		if event.EventType == "messageStop" {
			data, err := sonic.Marshal(event.Payload)
			if err != nil {
				t.Fatalf("marshal messageStop: %v", err)
			}
			return string(data)
		}
	}
	t.Fatalf("no messageStop event in %v", encodedEventTypes(events))
	return ""
}

// TestConverseStreamStopSequenceRoundTrip pins that converse-stream returns the native
// stopReason and additionalModelResponseFields from Bedrock's messageStop event instead of
// folding stop_sequence into end_turn and dropping the fields.
func TestConverseStreamStopSequenceRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name        string
		messageStop string
		want        string
	}{
		{"stop_sequence with fields", `{"stopReason":"stop_sequence","additionalModelResponseFields":{"stop_sequence":"three"}}`, `{"stopReason":"stop_sequence","additionalModelResponseFields":{"stop_sequence":"three"}}`},
		{"stop_sequence without fields", `{"stopReason":"stop_sequence"}`, `{"stopReason":"stop_sequence"}`},
		{"end_turn unchanged", `{"stopReason":"end_turn"}`, `{"stopReason":"end_turn"}`},
		{"end_turn with fields", `{"stopReason":"end_turn","additionalModelResponseFields":{"stop_sequence":null}}`, `{"stopReason":"end_turn","additionalModelResponseFields":{"stop_sequence":null}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := messageStopPayload(t, driveConverseStopStream(t, tc.messageStop))
			if got != tc.want {
				t.Errorf("messageStop = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestConverseStreamAnthropicStopSequence pins that a stop-sequence finish from an
// Anthropic upstream (StopReason "stop" plus StopSequence) reaches converse-stream as stop_sequence.
func TestConverseStreamAnthropicStopSequence(t *testing.T) {
	event, err := ToBedrockConverseStreamResponse(&schemas.BifrostResponsesStreamResponse{
		Type:     schemas.ResponsesStreamResponseTypeCompleted,
		Response: &schemas.BifrostResponsesResponse{StopReason: schemas.Ptr("stop"), StopSequence: schemas.Ptr("three")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if event == nil || event.StopReason == nil || *event.StopReason != "stop_sequence" {
		t.Fatalf("stopReason = %v, want stop_sequence", event)
	}
}

// BenchmarkConverseStreamEndTurn measures a short end_turn ConverseStream from upstream
// events to converse-stream egress events.
func BenchmarkConverseStreamEndTurn(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		driveConverseStopStream(b, `{"stopReason":"end_turn"}`)
	}
}
