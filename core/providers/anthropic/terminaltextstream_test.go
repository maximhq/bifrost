package anthropic

import (
	"encoding/json"
	"fmt"
	"strings"
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
		addedArgs    string
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
			name:         "empty deltas then done",
			itemType:     schemas.ResponsesMessageTypeFunctionCall,
			deltaType:    schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
			doneType:     schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone,
			deltas:       []string{"", ""},
			aggregate:    `{"file_path":"/etc/hostname"}`,
			want:         `{"file_path":"/etc/hostname"}`,
			wantTerminal: `{"file_path":"/etc/hostname"}`,
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
			name:         "prior non-empty delta suppresses done",
			itemType:     schemas.ResponsesMessageTypeFunctionCall,
			deltaType:    schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
			doneType:     schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone,
			deltas:       []string{`{"file_path":"/etc/hosts"}`},
			aggregate:    `{"file_path":"/etc/hostname"}`,
			want:         `{"file_path":"/etc/hosts"}`,
			wantTerminal: "",
		},
		{
			name:         "inline arguments on added suppress done",
			itemType:     schemas.ResponsesMessageTypeFunctionCall,
			deltaType:    schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
			doneType:     schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone,
			addedArgs:    `{"file_path":"/etc/hostname"}`,
			aggregate:    `{"file_path":"/etc/hostname"}`,
			want:         `{"file_path":"/etc/hostname"}`,
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
			added := terminalToolItem("call_1", 0, tc.itemType, schemas.ResponsesStreamResponseTypeOutputItemAdded)
			if tc.addedArgs != "" {
				added.Item.Arguments = schemas.Ptr(tc.addedArgs)
			}
			responses := []*schemas.BifrostResponsesStreamResponse{added}
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
			state := getOrCreateAnthropicToResponsesStreamState(ctx)
			if len(state.emittedTextByBlock) != 0 || state.emittedInputJSONMask != 0 || len(state.emittedInputJSONOverflow) != 0 {
				t.Fatalf("emitted progress leaked after block stop: text=%#v mask=%#x overflow=%#v", state.emittedTextByBlock, state.emittedInputJSONMask, state.emittedInputJSONOverflow)
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

		for i := 3; i < 66; i++ {
			id := fmt.Sprintf("call_%d", i)
			added := terminalToolItem(id, i, schemas.ResponsesMessageTypeFunctionCall, schemas.ResponsesStreamResponseTypeOutputItemAdded)
			done := terminalToolArgsEvent(id, i, schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone, `{"idx":1}`)
			stop := terminalToolItem(id, i, schemas.ResponsesMessageTypeFunctionCall, schemas.ResponsesStreamResponseTypeOutputItemDone)
			events := convertTerminalTextEvents(ctx, added, done, stop)
			if got := terminalToolInputJSON(events); got != `{"idx":1}` {
				t.Fatalf("call %d input = %q, want {\"idx\":1}", i, got)
			}
		}
		if state := getOrCreateAnthropicToResponsesStreamState(ctx); state.emittedInputJSONMask != 0 || len(state.emittedInputJSONOverflow) != 0 {
			t.Fatalf("overflow progress leaked: mask=%#x overflow=%#v", state.emittedInputJSONMask, state.emittedInputJSONOverflow)
		}

		// WebSearch skips terminal arguments.done so output_item.done sanitization remains the single source.
		wsAdded := terminalToolItem("ws_1", 70, schemas.ResponsesMessageTypeFunctionCall, schemas.ResponsesStreamResponseTypeOutputItemAdded)
		wsAdded.Item.ResponsesToolMessage.Name = schemas.Ptr("WebSearch")
		convertTerminalTextEvents(ctx, wsAdded)
		if events := convertTerminalTextEvents(ctx, terminalToolArgsEvent("ws_1", 70, schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone, `{"query":"test"}`)); len(events) != 0 {
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

func TestTerminalToolArgumentsStreamWebSearchOutputIndexKey(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	added := terminalToolItem("", 0, schemas.ResponsesMessageTypeFunctionCall, schemas.ResponsesStreamResponseTypeOutputItemAdded)
	added.Item.ResponsesToolMessage.Name = schemas.Ptr("WebSearch")
	delta := terminalToolArgsEvent("", 0, schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta, `{"query":"test"`)
	delta.ItemID = nil
	done := terminalToolArgsEvent("", 0, schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone, `{"query":"test"}`)
	stop := terminalToolItem("", 0, schemas.ResponsesMessageTypeFunctionCall, schemas.ResponsesStreamResponseTypeOutputItemDone)
	stop.Item.ResponsesToolMessage.Name = schemas.Ptr("WebSearch")
	stop.Item.ResponsesToolMessage.Arguments = schemas.Ptr(`{"query":"test"}`)

	events := convertTerminalTextEvents(ctx, added, delta, done, stop)

	input := terminalToolInputJSON(events)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(input), &parsed); err != nil {
		t.Fatalf("tool input is not a single JSON object: %v (input %q)", err, input)
	}
	if parsed["query"] != "test" {
		t.Fatalf("query = %v, want %q", parsed["query"], "test")
	}
	state := getOrCreateAnthropicToResponsesStreamState(ctx)
	if len(state.webSearchItemIDs) != 0 {
		t.Fatalf("WebSearch tracking leaked after output_item.done: %#v", state.webSearchItemIDs)
	}
	if len(state.emittedTextByBlock) != 0 || state.emittedInputJSONMask != 0 || len(state.emittedInputJSONOverflow) != 0 {
		t.Fatalf("block progress leaked after WebSearch output_item.done: text=%#v mask=%#x overflow=%#v", state.emittedTextByBlock, state.emittedInputJSONMask, state.emittedInputJSONOverflow)
	}
}

func benchToolJSONPayload(size int) string {
	const prefix = `{"payload":"`
	const suffix = `"}`
	if size <= len(prefix)+len(suffix) {
		return `{"a":"` + strings.Repeat("x", max(0, size-8)) + `"}`
	}
	return prefix + strings.Repeat("a", size-len(prefix)-len(suffix)) + suffix
}

func benchSplitChunks(s string, chunkSize int) []string {
	var out []string
	for i := 0; i < len(s); i += chunkSize {
		out = append(out, s[i:min(len(s), i+chunkSize)])
	}
	return out
}

func benchToolStreamFrames(callIdx int, fullArgs string, streamedArgs string, deltaSize int, itemType schemas.ResponsesMessageType) []*schemas.BifrostResponsesStreamResponse {
	id := fmt.Sprintf("call_%d", callIdx)
	deltaType := schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta
	doneType := schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone
	if itemType == schemas.ResponsesMessageTypeMCPCall {
		deltaType = schemas.ResponsesStreamResponseTypeMCPCallArgumentsDelta
		doneType = schemas.ResponsesStreamResponseTypeMCPCallArgumentsDone
	}
	frames := []*schemas.BifrostResponsesStreamResponse{
		terminalToolItem(id, callIdx, itemType, schemas.ResponsesStreamResponseTypeOutputItemAdded),
	}
	for _, chunk := range benchSplitChunks(streamedArgs, deltaSize) {
		frames = append(frames, terminalToolArgsEvent(id, callIdx, deltaType, chunk))
	}
	if fullArgs != "" {
		frames = append(frames, terminalToolArgsEvent(id, callIdx, doneType, fullArgs))
	}
	if itemType == schemas.ResponsesMessageTypeMCPCall {
		frames = append(frames, &schemas.BifrostResponsesStreamResponse{
			Type:        schemas.ResponsesStreamResponseTypeMCPCallCompleted,
			ItemID:      schemas.Ptr(id),
			OutputIndex: schemas.Ptr(callIdx),
		})
	} else {
		frames = append(frames, terminalToolItem(id, callIdx, itemType, schemas.ResponsesStreamResponseTypeOutputItemDone))
	}
	return frames
}

func runBenchStreamFrames(b *testing.B, frames []*schemas.BifrostResponsesStreamResponse) {
	b.Helper()
	b.ReportAllocs()
	for b.Loop() {
		ctx := schemas.NewBifrostContext(nil, time.Time{})
		for _, f := range frames {
			_ = ToAnthropicResponsesStreamResponse(ctx, f)
		}
	}
}

func BenchmarkTerminalToolArgumentsStream(b *testing.B) {
	args256 := benchToolJSONPayload(256)
	for _, n := range []int{1, 8, 32} {
		var frames []*schemas.BifrostResponsesStreamResponse
		for i := range n {
			frames = append(frames, benchToolStreamFrames(i, args256, args256, 16, schemas.ResponsesMessageTypeFunctionCall)...)
		}
		b.Run(fmt.Sprintf("tool_calls/%d", n), func(b *testing.B) {
			runBenchStreamFrames(b, frames)
		})
	}

	for _, sc := range []struct {
		label     string
		bytes     int
		deltaSize int
	}{
		{"1KB", 1 << 10, 64},
		{"64KB", 64 << 10, 64},
		{"1MB", 1 << 20, 64},
		{"10MB", 10 << 20, 4096},
		{"50MB", 50 << 20, 4096},
	} {
		payload := benchToolJSONPayload(sc.bytes)
		frames := benchToolStreamFrames(0, payload, payload, sc.deltaSize, schemas.ResponsesMessageTypeFunctionCall)
		b.Run("deltas_and_done/"+sc.label, func(b *testing.B) {
			runBenchStreamFrames(b, frames)
		})
	}

	payload1KB := benchToolJSONPayload(1 << 10)
	payload1MB := benchToolJSONPayload(1 << 20)

	b.Run("done_only/1KB", func(b *testing.B) {
		runBenchStreamFrames(b, benchToolStreamFrames(0, payload1KB, "", 64, schemas.ResponsesMessageTypeFunctionCall))
	})
	b.Run("done_only/1MB", func(b *testing.B) {
		runBenchStreamFrames(b, benchToolStreamFrames(0, payload1MB, "", 64, schemas.ResponsesMessageTypeFunctionCall))
	})
	b.Run("mcp_deltas_and_done/1KB", func(b *testing.B) {
		runBenchStreamFrames(b, benchToolStreamFrames(0, payload1KB, payload1KB, 64, schemas.ResponsesMessageTypeMCPCall))
	})
	b.Run("mcp_done_only/1KB", func(b *testing.B) {
		runBenchStreamFrames(b, benchToolStreamFrames(0, payload1KB, "", 64, schemas.ResponsesMessageTypeMCPCall))
	})
	b.Run("websearch/1KB", func(b *testing.B) {
		wsQuery := `{"query":"` + strings.Repeat("q", 1000) + `"}`
		added := terminalToolItem("ws_1", 0, schemas.ResponsesMessageTypeFunctionCall, schemas.ResponsesStreamResponseTypeOutputItemAdded)
		added.Item.Name = schemas.Ptr("WebSearch")
		frames := []*schemas.BifrostResponsesStreamResponse{added}
		for _, chunk := range benchSplitChunks(wsQuery, 64) {
			frames = append(frames, terminalToolArgsEvent("ws_1", 0, schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta, chunk))
		}
		frames = append(frames, terminalToolArgsEvent("ws_1", 0, schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone, wsQuery))
		stop := terminalToolItem("ws_1", 0, schemas.ResponsesMessageTypeFunctionCall, schemas.ResponsesStreamResponseTypeOutputItemDone)
		stop.Item.Name = schemas.Ptr("WebSearch")
		stop.Item.ResponsesToolMessage.Arguments = schemas.Ptr(wsQuery)
		frames = append(frames, stop)
		runBenchStreamFrames(b, frames)
	})
	b.Run("text_only/1KB", func(b *testing.B) {
		frames := []*schemas.BifrostResponsesStreamResponse{terminalTextItemAdded("msg_0", 0)}
		for _, chunk := range benchSplitChunks(payload1KB, 64) {
			frames = append(frames, terminalTextDelta("msg_0", 0, chunk))
		}
		frames = append(frames, terminalTextDone("msg_0", 0, payload1KB), terminalTextItemDone("msg_0", 0))
		runBenchStreamFrames(b, frames)
	})
	b.Run("text_only/1MB", func(b *testing.B) {
		frames := []*schemas.BifrostResponsesStreamResponse{terminalTextItemAdded("msg_0", 0)}
		for _, chunk := range benchSplitChunks(payload1MB, 64) {
			frames = append(frames, terminalTextDelta("msg_0", 0, chunk))
		}
		frames = append(frames, terminalTextDone("msg_0", 0, payload1MB), terminalTextItemDone("msg_0", 0))
		runBenchStreamFrames(b, frames)
	})
}
