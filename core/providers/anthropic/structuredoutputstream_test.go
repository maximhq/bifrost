package anthropic

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// Vertex, Bedrock Mantle and Azure cannot use Anthropic's native structured output
// (output_config.format), so ToBifrostResponsesRequest turns the caller's json_schema
// into a forced tool call and the stream converter reassembles that tool call back
// into an assistant text message.
//
// The reassembly used to emit only output_item.added and output_item.done. Every
// consumer that reads the incremental events rather than the item snapshot -- the
// Gemini /genai stream converter, the OpenAI Responses SDK -- therefore saw a stream
// with no text in it at all:
//
//	POST /genai/v1beta/models/bedrock_mantle/anthropic.claude-opus-4-8:streamGenerateContent
//	  with generationConfig.response_schema
//	→ data: {"candidates":[{"content":{"role":"model"},"finishReason":"STOP"}], ...}
//
// The same request without a schema, and the same schema against native Anthropic,
// both stream text normally -- the tool-based path was the only one missing the
// content_part / output_text events.

const structuredOutputToolName = "bf_so_Greeting"

// structuredOutputToolStream is the Anthropic wire sequence for a turn whose only
// content block is the synthesized structured-output tool call.
func structuredOutputToolStream() []*AnthropicStreamEvent {
	index := 0
	return []*AnthropicStreamEvent{
		{
			Type: AnthropicStreamEventTypeContentBlockStart,
			// The item id is the tool_use id: this is a tool call on the wire, and
			// only Bifrost's converter knows it stands in for a text message.
			Index: &index,
			ContentBlock: &AnthropicContentBlock{
				Type: AnthropicContentBlockTypeToolUse,
				ID:   schemas.Ptr("toolu_bdrk_01Su19x6xQrV44fPpJHzBhVm"),
				Name: schemas.Ptr(structuredOutputToolName),
			},
		},
		{
			Type:  AnthropicStreamEventTypeContentBlockDelta,
			Index: &index,
			Delta: &AnthropicStreamDelta{
				Type:        AnthropicStreamDeltaTypeInputJSON,
				PartialJSON: schemas.Ptr(`{"text":`),
			},
		},
		{
			Type:  AnthropicStreamEventTypeContentBlockDelta,
			Index: &index,
			Delta: &AnthropicStreamDelta{
				Type:        AnthropicStreamDeltaTypeInputJSON,
				PartialJSON: schemas.Ptr(`"hi"}`),
			},
		},
		{
			Type:  AnthropicStreamEventTypeContentBlockStop,
			Index: &index,
		},
	}
}

// collectStructuredOutputStream runs the wire sequence through the converter with the
// structured-output tool name registered, the way HandleAnthropicResponsesStreaming
// seeds it from BifrostContextKeyStructuredOutputToolName.
func collectStructuredOutputStream(t *testing.T) []*schemas.BifrostResponsesStreamResponse {
	t.Helper()

	state := AcquireAnthropicResponsesStreamState()
	defer ReleaseAnthropicResponsesStreamState(state)
	state.StructuredOutputToolName = structuredOutputToolName

	var emitted []*schemas.BifrostResponsesStreamResponse
	for _, event := range structuredOutputToolStream() {
		responses, bifrostErr, _ := event.ToBifrostResponsesStream(context.Background(), len(emitted), state)
		if bifrostErr != nil {
			t.Fatalf("converter returned an error for %s: %v", event.Type, bifrostErr)
		}
		emitted = append(emitted, responses...)
	}
	return emitted
}

func TestStructuredOutputToolStream_EmitsTextEvents(t *testing.T) {
	emitted := collectStructuredOutputStream(t)

	var types []schemas.ResponsesStreamResponseType
	for _, resp := range emitted {
		types = append(types, resp.Type)
	}

	// A message item must announce its content part and its text, not just the item.
	want := []schemas.ResponsesStreamResponseType{
		schemas.ResponsesStreamResponseTypeOutputItemAdded,
		schemas.ResponsesStreamResponseTypeContentPartAdded,
		schemas.ResponsesStreamResponseTypeOutputTextDelta,
		schemas.ResponsesStreamResponseTypeOutputTextDone,
		schemas.ResponsesStreamResponseTypeContentPartDone,
		schemas.ResponsesStreamResponseTypeOutputItemDone,
	}
	if len(types) != len(want) {
		t.Fatalf("expected %d events %v, got %d: %v", len(want), want, len(types), types)
	}
	for i, expected := range want {
		if types[i] != expected {
			t.Errorf("event %d: expected %s, got %s", i, expected, types[i])
		}
	}
}

func TestStructuredOutputToolStream_CarriesTheJSONInEveryTextEvent(t *testing.T) {
	const wantJSON = `{"text":"hi"}`

	byType := map[schemas.ResponsesStreamResponseType]*schemas.BifrostResponsesStreamResponse{}
	for _, resp := range collectStructuredOutputStream(t) {
		byType[resp.Type] = resp
	}

	// The delta carries the whole document in one event. The tool call's arguments
	// are only valid JSON once complete, so partial input_json_delta fragments are
	// not forwarded -- a consumer parsing each delta would choke on `{"text":`.
	delta := byType[schemas.ResponsesStreamResponseTypeOutputTextDelta]
	if delta == nil {
		t.Fatal("no output_text.delta emitted")
	}
	if delta.Delta == nil || *delta.Delta != wantJSON {
		t.Errorf("output_text.delta: expected %q, got %v", wantJSON, delta.Delta)
	}

	done := byType[schemas.ResponsesStreamResponseTypeOutputTextDone]
	if done == nil {
		t.Fatal("no output_text.done emitted")
	}
	if done.Text == nil || *done.Text != wantJSON {
		t.Errorf("output_text.done: expected %q, got %v", wantJSON, done.Text)
	}

	partDone := byType[schemas.ResponsesStreamResponseTypeContentPartDone]
	if partDone == nil || partDone.Part == nil {
		t.Fatal("no content_part.done with a part emitted")
	}
	if partDone.Part.Type != schemas.ResponsesOutputMessageContentTypeText {
		t.Errorf("content_part.done: expected an output_text part, got %s", partDone.Part.Type)
	}
	if partDone.Part.Text == nil || *partDone.Part.Text != wantJSON {
		t.Errorf("content_part.done: expected %q, got %v", wantJSON, partDone.Part.Text)
	}

	// content_part.added opens the part empty, matching the ordinary text path.
	partAdded := byType[schemas.ResponsesStreamResponseTypeContentPartAdded]
	if partAdded == nil || partAdded.Part == nil {
		t.Fatal("no content_part.added with a part emitted")
	}
	if partAdded.Part.Text == nil || *partAdded.Part.Text != "" {
		t.Errorf("content_part.added: expected an empty part, got %v", partAdded.Part.Text)
	}
}

func TestStructuredOutputToolStream_SharesOneItemIDAcrossEvents(t *testing.T) {
	emitted := collectStructuredOutputStream(t)

	// Every event for this item must name it, or a client cannot attach the text to
	// the message it announced. The item id is the tool_use id from the wire.
	const wantItemID = "toolu_bdrk_01Su19x6xQrV44fPpJHzBhVm"
	for _, resp := range emitted {
		switch resp.Type {
		case schemas.ResponsesStreamResponseTypeOutputItemAdded,
			schemas.ResponsesStreamResponseTypeOutputItemDone:
			if resp.Item == nil || resp.Item.ID == nil || *resp.Item.ID != wantItemID {
				t.Errorf("%s: expected item id %q, got %v", resp.Type, wantItemID, resp.Item)
			}
		default:
			if resp.ItemID == nil || *resp.ItemID != wantItemID {
				t.Errorf("%s: expected item_id %q, got %v", resp.Type, wantItemID, resp.ItemID)
			}
		}
	}
}

// An empty tool input still has to produce a parseable document; the converter
// substitutes "{}" rather than emitting an empty message.
func TestStructuredOutputToolStream_EmptyArgumentsBecomeEmptyObject(t *testing.T) {
	state := AcquireAnthropicResponsesStreamState()
	defer ReleaseAnthropicResponsesStreamState(state)
	state.StructuredOutputToolName = structuredOutputToolName

	index := 0
	events := []*AnthropicStreamEvent{
		{
			Type:  AnthropicStreamEventTypeContentBlockStart,
			Index: &index,
			ContentBlock: &AnthropicContentBlock{
				Type: AnthropicContentBlockTypeToolUse,
				ID:   schemas.Ptr("toolu_empty"),
				Name: schemas.Ptr(structuredOutputToolName),
			},
		},
		{Type: AnthropicStreamEventTypeContentBlockStop, Index: &index},
	}

	var emitted []*schemas.BifrostResponsesStreamResponse
	for _, event := range events {
		responses, _, _ := event.ToBifrostResponsesStream(context.Background(), len(emitted), state)
		emitted = append(emitted, responses...)
	}

	for _, resp := range emitted {
		if resp.Type != schemas.ResponsesStreamResponseTypeOutputTextDelta {
			continue
		}
		if resp.Delta == nil || *resp.Delta != "{}" {
			t.Errorf("expected the empty-arguments delta to be {}, got %v", resp.Delta)
		}
		return
	}
	t.Fatal("no output_text.delta emitted for an empty structured-output tool call")
}
