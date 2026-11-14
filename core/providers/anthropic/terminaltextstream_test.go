package anthropic

import (
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// terminalTextItemAdded builds a text output-item start for reverse stream conversion.
func terminalTextItemAdded(itemID string, outputIndex int) *schemas.BifrostResponsesStreamResponse {
	return &schemas.BifrostResponsesStreamResponse{
		Type:        schemas.ResponsesStreamResponseTypeOutputItemAdded,
		OutputIndex: schemas.Ptr(outputIndex),
		Item: &schemas.ResponsesMessage{
			ID:   schemas.Ptr(itemID),
			Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
		},
	}
}

// terminalTextDelta builds one normalized output-text delta.
func terminalTextDelta(itemID string, outputIndex int, text string) *schemas.BifrostResponsesStreamResponse {
	return &schemas.BifrostResponsesStreamResponse{
		Type:        schemas.ResponsesStreamResponseTypeOutputTextDelta,
		ItemID:      schemas.Ptr(itemID),
		OutputIndex: schemas.Ptr(outputIndex),
		Delta:       schemas.Ptr(text),
	}
}

// terminalTextDone builds the cumulative normalized output-text terminal event.
func terminalTextDone(itemID string, outputIndex int, text string) *schemas.BifrostResponsesStreamResponse {
	return &schemas.BifrostResponsesStreamResponse{
		Type:        schemas.ResponsesStreamResponseTypeOutputTextDone,
		ItemID:      schemas.Ptr(itemID),
		OutputIndex: schemas.Ptr(outputIndex),
		Text:        schemas.Ptr(text),
	}
}

// terminalTextItemDone builds the output-item terminal event that closes the Anthropic block.
func terminalTextItemDone(itemID string, outputIndex int) *schemas.BifrostResponsesStreamResponse {
	return &schemas.BifrostResponsesStreamResponse{
		Type:        schemas.ResponsesStreamResponseTypeOutputItemDone,
		OutputIndex: schemas.Ptr(outputIndex),
		Item: &schemas.ResponsesMessage{
			ID:   schemas.Ptr(itemID),
			Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
		},
	}
}

// convertTerminalTextEvents runs normalized events through the Bifrost-to-Anthropic converter.
func convertTerminalTextEvents(ctx *schemas.BifrostContext, responses ...*schemas.BifrostResponsesStreamResponse) []*AnthropicStreamEvent {
	var events []*AnthropicStreamEvent
	for _, response := range responses {
		events = append(events, ToAnthropicResponsesStreamResponse(ctx, response)...)
	}
	return events
}

// terminalTextDeltas reconstructs client-visible text from Anthropic text deltas only.
func terminalTextDeltas(events []*AnthropicStreamEvent) string {
	var text string
	for _, event := range events {
		if event.Type == AnthropicStreamEventTypeContentBlockDelta &&
			event.Delta != nil && event.Delta.Type == AnthropicStreamDeltaTypeText && event.Delta.Text != nil {
			text += *event.Delta.Text
		}
	}
	return text
}

// TestTerminalTextStreamEmitsOnlyMissingSuffix verifies held and partially released output is completed once.
func TestTerminalTextStreamEmitsOnlyMissingSuffix(t *testing.T) {
	tests := []struct {
		name         string
		deltas       []string
		aggregate    string
		want         string
		wantTerminal string
	}{
		{name: "short held reply", deltas: []string{"", ""}, aggregate: "OK", want: "OK", wantTerminal: "OK"},
		{name: "partially released reply", deltas: []string{"First sentence. "}, aggregate: "First sentence. Final sentence.", want: "First sentence. Final sentence.", wantTerminal: "Final sentence."},
		{name: "normal stream", deltas: []string{"O", "K"}, aggregate: "OK", want: "OK", wantTerminal: ""},
		{name: "redaction changes byte length", deltas: []string{"Contact [EMAIL]. "}, aggregate: "Contact [EMAIL]. Done.", want: "Contact [EMAIL]. Done.", wantTerminal: "Done."},
		{name: "unicode prefix", deltas: []string{"Hello 世界. "}, aggregate: "Hello 世界. Done.", want: "Hello 世界. Done.", wantTerminal: "Done."},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(nil, time.Time{})
			responses := []*schemas.BifrostResponsesStreamResponse{terminalTextItemAdded("msg_1", 0)}
			for _, delta := range test.deltas {
				responses = append(responses, terminalTextDelta("msg_1", 0, delta))
			}

			beforeTerminal := convertTerminalTextEvents(ctx, responses...)
			terminalEvents := convertTerminalTextEvents(ctx, terminalTextDone("msg_1", 0, test.aggregate))
			allEvents := append(beforeTerminal, terminalEvents...)
			if got := terminalTextDeltas(allEvents); got != test.want {
				t.Fatalf("reconstructed text = %q, want %q", got, test.want)
			}
			if got := terminalTextDeltas(terminalEvents); got != test.wantTerminal {
				t.Fatalf("terminal text delta = %q, want %q", got, test.wantTerminal)
			}
		})
	}
}

// TestTerminalTextStreamTracksBlocksIndependently verifies interleaved text items do not share progress.
func TestTerminalTextStreamTracksBlocksIndependently(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	events := convertTerminalTextEvents(ctx,
		terminalTextItemAdded("msg_1", 0),
		terminalTextDelta("msg_1", 0, "First "),
		terminalTextItemAdded("msg_2", 1),
		terminalTextDelta("msg_2", 1, "Second "),
		terminalTextDone("msg_1", 0, "First done."),
		terminalTextDone("msg_2", 1, "Second done."),
	)

	wantByBlock := map[int]string{0: "First done.", 1: "Second done."}
	gotByBlock := make(map[int]string)
	for _, event := range events {
		if event.Type == AnthropicStreamEventTypeContentBlockDelta && event.Index != nil &&
			event.Delta != nil && event.Delta.Type == AnthropicStreamDeltaTypeText && event.Delta.Text != nil {
			gotByBlock[*event.Index] += *event.Delta.Text
		}
	}
	for index, want := range wantByBlock {
		if got := gotByBlock[index]; got != want {
			t.Errorf("block %d text = %q, want %q", index, got, want)
		}
	}
}

// TestTerminalTextStreamRejectsNonAppendOnlyAggregate verifies unsafe terminal aggregates never synthesize guessed text.
func TestTerminalTextStreamRejectsNonAppendOnlyAggregate(t *testing.T) {
	tests := []struct {
		name      string
		aggregate string
	}{
		{name: "shorter", aggregate: "Fir"},
		{name: "prefix mismatch", aggregate: "Changed sentence. Final."},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(nil, time.Time{})
			convertTerminalTextEvents(ctx,
				terminalTextItemAdded("msg_1", 0),
				terminalTextDelta("msg_1", 0, "First sentence. "),
			)
			if events := convertTerminalTextEvents(ctx, terminalTextDone("msg_1", 0, test.aggregate)); len(events) != 0 {
				t.Fatalf("unsafe aggregate emitted events: %#v", events)
			}
		})
	}

	t.Run("unopened block", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(nil, time.Time{})
		if events := convertTerminalTextEvents(ctx, terminalTextDone("msg_missing", 0, "OK")); len(events) != 0 {
			t.Fatalf("unopened block emitted events: %#v", events)
		}
	})
}

// TestTerminalTextStreamPassthroughNeverSynthesizes verifies raw Claude Code frames remain authoritative.
func TestTerminalTextStreamPassthroughNeverSynthesizes(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	SetResponsesStreamPassthrough(ctx)
	convertTerminalTextEvents(ctx, terminalTextItemAdded("msg_1", 0))

	if events := convertTerminalTextEvents(ctx, terminalTextDone("msg_1", 0, "OK")); len(events) != 0 {
		t.Fatalf("passthrough output_text.done synthesized events: %#v", events)
	}
}

// TestTerminalTextStreamDeltaPrecedesStop verifies the recovered suffix is emitted before its block closes.
func TestTerminalTextStreamDeltaPrecedesStop(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	convertTerminalTextEvents(ctx, terminalTextItemAdded("msg_1", 0))
	terminalEvents := convertTerminalTextEvents(ctx, terminalTextDone("msg_1", 0, "OK"))
	stopEvents := convertTerminalTextEvents(ctx, terminalTextItemDone("msg_1", 0))
	events := append(terminalEvents, stopEvents...)

	if len(events) != 2 || events[0].Type != AnthropicStreamEventTypeContentBlockDelta || events[1].Type != AnthropicStreamEventTypeContentBlockStop {
		t.Fatalf("terminal event order = %#v, want text delta then block stop", events)
	}
	if progress := getOrCreateAnthropicToResponsesStreamState(ctx).emittedTextByBlock; len(progress) != 0 {
		t.Fatalf("emitted text progress leaked after block stop: %#v", progress)
	}
}

// terminalToolItem builds a tool output-item start or stop event for reverse stream conversion.
func terminalToolItem(itemID string, outputIndex int, itemType schemas.ResponsesMessageType, eventType schemas.ResponsesStreamResponseType) *schemas.BifrostResponsesStreamResponse {
	return &schemas.BifrostResponsesStreamResponse{
		Type:        eventType,
		OutputIndex: schemas.Ptr(outputIndex),
		Item: &schemas.ResponsesMessage{
			ID:   schemas.Ptr(itemID),
			Type: schemas.Ptr(itemType),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID:    schemas.Ptr(itemID),
				Name:      schemas.Ptr("Read"),
				Arguments: schemas.Ptr(""),
			},
		},
	}
}

// terminalToolArgsEvent builds a tool-arguments delta or done event.
func terminalToolArgsEvent(itemID string, outputIndex int, eventType schemas.ResponsesStreamResponseType, arguments string) *schemas.BifrostResponsesStreamResponse {
	return &schemas.BifrostResponsesStreamResponse{
		Type:        eventType,
		ItemID:      schemas.Ptr(itemID),
		OutputIndex: schemas.Ptr(outputIndex),
		Arguments:   schemas.Ptr(arguments),
	}
}

// terminalToolInputJSON reconstructs client-visible tool input from Anthropic input_json_delta events.
func terminalToolInputJSON(events []*AnthropicStreamEvent) string {
	var input string
	for _, event := range events {
		if event.Type == AnthropicStreamEventTypeContentBlockDelta &&
			event.Delta != nil && event.Delta.Type == AnthropicStreamDeltaTypeInputJSON && event.Delta.PartialJSON != nil {
			input += *event.Delta.PartialJSON
		}
	}
	return input
}

// TestTerminalToolArgumentsStreamEmitsMissingSuffix verifies function_call and mcp_call terminal argument events.
func TestTerminalToolArgumentsStreamEmitsMissingSuffix(t *testing.T) {
	tests := []struct {
		name         string
		itemType     schemas.ResponsesMessageType
		deltaType    schemas.ResponsesStreamResponseType
		doneType     schemas.ResponsesStreamResponseType
		deltas       []string
		aggregate    string
		want         string
		wantTerminal string
	}{
		{
			name:         "terminal only function_call",
			itemType:     schemas.ResponsesMessageTypeFunctionCall,
			deltaType:    schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
			doneType:     schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone,
			aggregate:    `{"file_path":"/etc/hostname"}`,
			want:         `{"file_path":"/etc/hostname"}`,
			wantTerminal: `{"file_path":"/etc/hostname"}`,
		},
		{
			name:         "partial deltas then done",
			itemType:     schemas.ResponsesMessageTypeFunctionCall,
			deltaType:    schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
			doneType:     schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone,
			deltas:       []string{`{"file_path":`},
			aggregate:    `{"file_path":"/etc/hostname"}`,
			want:         `{"file_path":"/etc/hostname"}`,
			wantTerminal: `"/etc/hostname"}`,
		},
		{
			name:         "complete deltas then done",
			itemType:     schemas.ResponsesMessageTypeFunctionCall,
			deltaType:    schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
			doneType:     schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone,
			deltas:       []string{`{"file_path":`, `"/etc/hostname"}`},
			aggregate:    `{"file_path":"/etc/hostname"}`,
			want:         `{"file_path":"/etc/hostname"}`,
			wantTerminal: "",
		},
		{
			name:         "divergent prefix rejected",
			itemType:     schemas.ResponsesMessageTypeFunctionCall,
			deltaType:    schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
			doneType:     schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone,
			deltas:       []string{`{"file_path":"/etc/hosts"`},
			aggregate:    `{"file_path":"/etc/hostname"}`,
			want:         `{"file_path":"/etc/hosts"`,
			wantTerminal: "",
		},
		{
			name:         "terminal only mcp_call",
			itemType:     schemas.ResponsesMessageTypeMCPCall,
			deltaType:    schemas.ResponsesStreamResponseTypeMCPCallArgumentsDelta,
			doneType:     schemas.ResponsesStreamResponseTypeMCPCallArgumentsDone,
			aggregate:    `{"query":"status"}`,
			want:         `{"query":"status"}`,
			wantTerminal: `{"query":"status"}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(nil, time.Time{})
			responses := []*schemas.BifrostResponsesStreamResponse{
				terminalToolItem("call_1", 0, tc.itemType, schemas.ResponsesStreamResponseTypeOutputItemAdded),
			}
			for _, d := range tc.deltas {
				responses = append(responses, terminalToolArgsEvent("call_1", 0, tc.deltaType, d))
			}
			beforeTerminal := convertTerminalTextEvents(ctx, responses...)
			doneEvent := terminalToolArgsEvent("call_1", 0, tc.doneType, tc.aggregate)
			terminalEvents := convertTerminalTextEvents(ctx, doneEvent)
			allEvents := append(beforeTerminal, terminalEvents...)

			if got := terminalToolInputJSON(allEvents); got != tc.want {
				t.Fatalf("reconstructed input = %q, want %q", got, tc.want)
			}
			if got := terminalToolInputJSON(terminalEvents); got != tc.wantTerminal {
				t.Fatalf("terminal input delta = %q, want %q", got, tc.wantTerminal)
			}
			if dup := convertTerminalTextEvents(ctx, doneEvent); terminalToolInputJSON(dup) != "" {
				t.Fatalf("duplicate terminal event emitted %q", terminalToolInputJSON(dup))
			}

			stopEvents := convertTerminalTextEvents(ctx, terminalToolItem("call_1", 0, tc.itemType, schemas.ResponsesStreamResponseTypeOutputItemDone))
			if len(stopEvents) != 1 || stopEvents[0].Type != AnthropicStreamEventTypeContentBlockStop {
				t.Fatalf("stop events = %#v, want single content_block_stop", stopEvents)
			}
			if progress := getOrCreateAnthropicToResponsesStreamState(ctx).emittedTextByBlock; len(progress) != 0 {
				t.Fatalf("emitted progress leaked after block stop: %#v", progress)
			}
		})
	}

	t.Run("parallel terminal calls and guards", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(nil, time.Time{})
		paths := []string{"/etc/hostname", "/etc/os-release", "/etc/fstab"}
		for i, p := range paths {
			id := ""
			added := terminalToolItem(id, i, schemas.ResponsesMessageTypeFunctionCall, schemas.ResponsesStreamResponseTypeOutputItemAdded)
			done := terminalToolArgsEvent(id, i, schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone, `{"file_path":"`+p+`"}`)
			stop := terminalToolItem(id, i, schemas.ResponsesMessageTypeFunctionCall, schemas.ResponsesStreamResponseTypeOutputItemDone)
			events := convertTerminalTextEvents(ctx, added, done, stop)
			if len(events) != 3 || events[0].Type != AnthropicStreamEventTypeContentBlockStart ||
				events[1].Type != AnthropicStreamEventTypeContentBlockDelta ||
				events[2].Type != AnthropicStreamEventTypeContentBlockStop {
				t.Fatalf("call %d events = %#v", i, events)
			}
			if got := terminalToolInputJSON(events); got != `{"file_path":"`+p+`"}` {
				t.Fatalf("call %d input = %q", i, got)
			}
		}

		// WebSearch skips terminal arguments.done so output_item.done sanitization remains authoritative.
		wsAdded := terminalToolItem("ws_1", 3, schemas.ResponsesMessageTypeFunctionCall, schemas.ResponsesStreamResponseTypeOutputItemAdded)
		wsAdded.Item.ResponsesToolMessage.Name = schemas.Ptr("WebSearch")
		convertTerminalTextEvents(ctx, wsAdded)
		if events := convertTerminalTextEvents(ctx, terminalToolArgsEvent("ws_1", 3, schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone, `{"query":"test"}`)); len(events) != 0 {
			t.Fatalf("WebSearch emitted terminal arguments before output_item.done: %#v", events)
		}

		// Passthrough never synthesizes terminal tool arguments.
		ptCtx := schemas.NewBifrostContext(nil, time.Time{})
		SetResponsesStreamPassthrough(ptCtx)
		convertTerminalTextEvents(ptCtx, terminalToolItem("pt_1", 0, schemas.ResponsesMessageTypeFunctionCall, schemas.ResponsesStreamResponseTypeOutputItemAdded))
		if events := convertTerminalTextEvents(ptCtx, terminalToolArgsEvent("pt_1", 0, schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone, `{}`)); len(events) != 0 {
			t.Fatalf("passthrough emitted terminal tool arguments: %#v", events)
		}
	})
}

// BenchmarkTerminalToolArgumentsStream measures per-call reverse stream conversion with and without deltas.
func BenchmarkTerminalToolArgumentsStream(b *testing.B) {
	added := terminalToolItem("call_1", 0, schemas.ResponsesMessageTypeFunctionCall, schemas.ResponsesStreamResponseTypeOutputItemAdded)
	delta := terminalToolArgsEvent("call_1", 0, schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta, `{"file_path":"/etc/hostname"}`)
	done := terminalToolArgsEvent("call_1", 0, schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone, `{"file_path":"/etc/hostname"}`)
	stop := terminalToolItem("call_1", 0, schemas.ResponsesMessageTypeFunctionCall, schemas.ResponsesStreamResponseTypeOutputItemDone)

	b.Run("with_delta", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			ctx := schemas.NewBifrostContext(nil, time.Time{})
			ToAnthropicResponsesStreamResponse(ctx, added)
			ToAnthropicResponsesStreamResponse(ctx, delta)
			ToAnthropicResponsesStreamResponse(ctx, done)
			ToAnthropicResponsesStreamResponse(ctx, stop)
		}
	})
	b.Run("terminal_only", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			ctx := schemas.NewBifrostContext(nil, time.Time{})
			ToAnthropicResponsesStreamResponse(ctx, added)
			ToAnthropicResponsesStreamResponse(ctx, done)
			ToAnthropicResponsesStreamResponse(ctx, stop)
		}
	})
}
