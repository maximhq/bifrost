package anthropic

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// visibleThinkingToolUseLifecycle returns the real Anthropic SSE shape of a
// response with extended thinking enabled: a thinking block (text deltas, then
// a signature delta) followed by a tool_use block.
func visibleThinkingToolUseLifecycle(thinkingParts []string, signature string) []*AnthropicStreamEvent {
	stopReason := AnthropicStopReasonToolUse
	events := []*AnthropicStreamEvent{
		{
			Type: AnthropicStreamEventTypeMessageStart,
			Message: &AnthropicMessageResponse{
				ID:    "msg_visible_stream",
				Model: "claude-sonnet-4-5-20250929",
			},
		},
		{
			Type:  AnthropicStreamEventTypeContentBlockStart,
			Index: schemas.Ptr(0),
			ContentBlock: &AnthropicContentBlock{
				Type: AnthropicContentBlockTypeThinking,
			},
		},
	}
	for _, part := range thinkingParts {
		events = append(events, &AnthropicStreamEvent{
			Type:  AnthropicStreamEventTypeContentBlockDelta,
			Index: schemas.Ptr(0),
			Delta: &AnthropicStreamDelta{
				Type:     AnthropicStreamDeltaTypeThinking,
				Thinking: schemas.Ptr(part),
			},
		})
	}
	if signature != "" {
		events = append(events, &AnthropicStreamEvent{
			Type:  AnthropicStreamEventTypeContentBlockDelta,
			Index: schemas.Ptr(0),
			Delta: &AnthropicStreamDelta{
				Type:      AnthropicStreamDeltaTypeSignature,
				Signature: schemas.Ptr(signature),
			},
		})
	}
	events = append(events,
		&AnthropicStreamEvent{Type: AnthropicStreamEventTypeContentBlockStop, Index: schemas.Ptr(0)},
		&AnthropicStreamEvent{
			Type:  AnthropicStreamEventTypeContentBlockStart,
			Index: schemas.Ptr(1),
			ContentBlock: &AnthropicContentBlock{
				Type: AnthropicContentBlockTypeToolUse,
				ID:   schemas.Ptr("toolu_01"),
				Name: schemas.Ptr("get_weather"),
			},
		},
		&AnthropicStreamEvent{
			Type:  AnthropicStreamEventTypeContentBlockDelta,
			Index: schemas.Ptr(1),
			Delta: &AnthropicStreamDelta{
				Type:        AnthropicStreamDeltaTypeInputJSON,
				PartialJSON: schemas.Ptr(`{"location":"Paris"}`),
			},
		},
		&AnthropicStreamEvent{Type: AnthropicStreamEventTypeContentBlockStop, Index: schemas.Ptr(1)},
		&AnthropicStreamEvent{
			Type:  AnthropicStreamEventTypeMessageDelta,
			Delta: &AnthropicStreamDelta{StopReason: &stopReason},
		},
		&AnthropicStreamEvent{Type: AnthropicStreamEventTypeMessageStop},
	)
	return events
}

// reasoningTextBlock returns the reasoning_text content block of a reasoning
// item, or nil when the item has none.
func reasoningTextBlock(item *schemas.ResponsesMessage) *schemas.ResponsesMessageContentBlock {
	if item == nil || item.Content == nil {
		return nil
	}
	for i := range item.Content.ContentBlocks {
		if item.Content.ContentBlocks[i].Type == schemas.ResponsesOutputMessageContentTypeReasoning {
			return &item.Content.ContentBlocks[i]
		}
	}
	return nil
}

// TestToBifrostResponsesStream_VisibleThinkingCompletion replays the streaming
// shape Anthropic uses for a visible thinking block and asserts the block's
// completion events carry the accumulated reasoning. Without the fold at
// content_block_stop, reasoning_summary_text.done carries an empty text,
// output_item.done degrades to an empty "message" shell that contradicts its
// own output_item.added, and response.completed omits the reasoning item.
func TestToBifrostResponsesStream_VisibleThinkingCompletion(t *testing.T) {
	t.Parallel()

	const fullText = "Let me figure out which tool to call."
	const signature = "sig-test-123"
	responses := driveResponsesStream(t, visibleThinkingToolUseLifecycle(
		[]string{"Let me figure out ", "which tool to call."}, signature))

	// reasoning_summary_text.done carries the full accumulated text
	var summaryDone *schemas.BifrostResponsesStreamResponse
	for _, r := range responses {
		if r.Type == schemas.ResponsesStreamResponseTypeReasoningSummaryTextDone {
			summaryDone = r
		}
	}
	if summaryDone == nil {
		t.Fatal("expected a reasoning_summary_text.done event")
	}
	if summaryDone.Text == nil || *summaryDone.Text != fullText {
		t.Errorf("reasoning_summary_text.done text = %v, want %q", summaryDone.Text, fullText)
	}

	// content_part.done for the reasoning block carries the completed part
	var partDone *schemas.BifrostResponsesStreamResponse
	for _, r := range responses {
		if r.Type == schemas.ResponsesStreamResponseTypeContentPartDone && r.OutputIndex != nil && *r.OutputIndex == 0 {
			partDone = r
		}
	}
	if partDone == nil {
		t.Fatal("expected a content_part.done event for the reasoning block")
	}
	if partDone.Part == nil || partDone.Part.Text == nil || *partDone.Part.Text != fullText {
		t.Errorf("content_part.done part text = %+v, want %q", partDone.Part, fullText)
	}
	if partDone.Part != nil && (partDone.Part.Signature == nil || *partDone.Part.Signature != signature) {
		t.Errorf("content_part.done part signature = %v, want %q", partDone.Part.Signature, signature)
	}

	// output_item.done for output index 0 is a reasoning item matching its
	// output_item.added, with the accumulated text and signature
	var itemDone *schemas.ResponsesMessage
	for _, r := range responses {
		if r.Type == schemas.ResponsesStreamResponseTypeOutputItemDone && r.OutputIndex != nil && *r.OutputIndex == 0 {
			itemDone = r.Item
		}
	}
	if itemDone == nil {
		t.Fatal("expected an output_item.done event for the reasoning block")
	}
	if itemDone.Type == nil {
		t.Fatal("output_item.done item type is nil, want reasoning")
	}
	if *itemDone.Type != schemas.ResponsesMessageTypeReasoning {
		t.Fatalf("output_item.done item type = %q, want reasoning", *itemDone.Type)
	}
	block := reasoningTextBlock(itemDone)
	if block == nil {
		t.Fatalf("output_item.done reasoning item has no reasoning_text content block: %+v", itemDone)
	}
	if block.Text == nil || *block.Text != fullText {
		t.Errorf("output_item.done reasoning text = %v, want %q", block.Text, fullText)
	}
	if block.Signature == nil || *block.Signature != signature {
		t.Errorf("output_item.done reasoning signature = %v, want %q", block.Signature, signature)
	}

	// response.completed carries the reasoning item before the function call
	var completed *schemas.BifrostResponsesResponse
	for _, r := range responses {
		if r.Type == schemas.ResponsesStreamResponseTypeCompleted && r.Response != nil {
			completed = r.Response
		}
	}
	if completed == nil {
		t.Fatal("expected a response.completed event")
	}
	if len(completed.Output) != 2 {
		t.Fatalf("response.completed output has %d items, want 2 (reasoning + function_call)", len(completed.Output))
	}
	first := completed.Output[0]
	if first.Type == nil {
		t.Fatal("response.completed output[0] type is nil, want reasoning")
	}
	if *first.Type != schemas.ResponsesMessageTypeReasoning {
		t.Fatalf("response.completed output[0] type = %q, want reasoning", *first.Type)
	}
	if b := reasoningTextBlock(&first); b == nil || b.Text == nil || *b.Text != fullText {
		t.Errorf("response.completed reasoning item text = %+v, want %q", b, fullText)
	}
	if second := completed.Output[1]; second.Type == nil || *second.Type != schemas.ResponsesMessageTypeFunctionCall {
		t.Errorf("response.completed output[1] type = %v, want function_call", second.Type)
	}
}

// TestToAnthropicResponsesRequest_ReplaysStreamedVisibleThinking closes the
// multi-turn loop for visible thinking: the completed reasoning item emitted
// on output_item.done is round-tripped through JSON the way a client echoes
// its history, then converted back into an Anthropic request. The replayed
// assistant turn must carry the thinking block, with its signature, before
// the tool_use block, so the reasoning survives in the echoed conversation
// instead of silently disappearing from the multi-turn history.
func TestToAnthropicResponsesRequest_ReplaysStreamedVisibleThinking(t *testing.T) {
	t.Parallel()

	const fullText = "Let me figure out which tool to call."
	const signature = "sig-test-123"
	responses := driveResponsesStream(t, visibleThinkingToolUseLifecycle(
		[]string{"Let me figure out ", "which tool to call."}, signature))

	var itemDone *schemas.ResponsesMessage
	for _, r := range responses {
		if r.Type == schemas.ResponsesStreamResponseTypeOutputItemDone && r.OutputIndex != nil && *r.OutputIndex == 0 {
			itemDone = r.Item
		}
	}
	if itemDone == nil {
		t.Fatal("expected an output_item.done event for the reasoning block")
	}

	// Client echo: the item leaves and re-enters Bifrost as JSON.
	wire, err := json.Marshal(itemDone)
	if err != nil {
		t.Fatalf("marshal streamed item: %v", err)
	}
	var replayed schemas.ResponsesMessage
	if err := json.Unmarshal(wire, &replayed); err != nil {
		t.Fatalf("unmarshal client echo: %v", err)
	}

	req := &schemas.BifrostResponsesRequest{
		Model: "claude-sonnet-4-5-20250929",
		Input: []schemas.ResponsesMessage{replayed, functionCallItem()},
	}
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	out, err := ToAnthropicResponsesRequest(ctx, req)
	if err != nil {
		t.Fatalf("ToAnthropicResponsesRequest: %v", err)
	}

	for _, msg := range out.Messages {
		if msg.Role != AnthropicMessageRoleAssistant {
			continue
		}
		thinkingIdx := -1
		toolUseIdx := -1
		for i, b := range msg.Content.ContentBlocks {
			switch b.Type {
			case AnthropicContentBlockTypeThinking:
				if b.Thinking != nil && *b.Thinking == fullText {
					thinkingIdx = i
					if b.Signature == nil || *b.Signature != signature {
						t.Errorf("replayed thinking block signature = %v, want %q", b.Signature, signature)
					}
				}
			case AnthropicContentBlockTypeToolUse:
				toolUseIdx = i
			}
		}
		if toolUseIdx == -1 {
			continue
		}
		if thinkingIdx == -1 {
			t.Fatalf("assistant message with tool_use has no thinking block carrying the reasoning: %+v", msg.Content.ContentBlocks)
		}
		if thinkingIdx > toolUseIdx {
			t.Fatalf("thinking (idx %d) must precede tool_use (idx %d)", thinkingIdx, toolUseIdx)
		}
		return
	}
	t.Fatalf("no assistant message with a tool_use block in the outgoing request (%d messages)", len(out.Messages))
}

// TestToBifrostResponsesStream_MultipleVisibleThinkingBlocks interleaves two
// thinking blocks around a tool_use block (the shape interleaved thinking
// produces) and asserts each completed reasoning item carries its own text
// and signature.
func TestToBifrostResponsesStream_MultipleVisibleThinkingBlocks(t *testing.T) {
	t.Parallel()

	stopReason := AnthropicStopReasonToolUse
	events := []*AnthropicStreamEvent{
		{
			Type: AnthropicStreamEventTypeMessageStart,
			Message: &AnthropicMessageResponse{
				ID:    "msg_interleaved_stream",
				Model: "claude-sonnet-4-5-20250929",
			},
		},
		{
			Type:  AnthropicStreamEventTypeContentBlockStart,
			Index: schemas.Ptr(0),
			ContentBlock: &AnthropicContentBlock{
				Type: AnthropicContentBlockTypeThinking,
			},
		},
		{
			Type:  AnthropicStreamEventTypeContentBlockDelta,
			Index: schemas.Ptr(0),
			Delta: &AnthropicStreamDelta{
				Type:     AnthropicStreamDeltaTypeThinking,
				Thinking: schemas.Ptr("first thought"),
			},
		},
		{
			Type:  AnthropicStreamEventTypeContentBlockDelta,
			Index: schemas.Ptr(0),
			Delta: &AnthropicStreamDelta{
				Type:      AnthropicStreamDeltaTypeSignature,
				Signature: schemas.Ptr("sig-first"),
			},
		},
		{Type: AnthropicStreamEventTypeContentBlockStop, Index: schemas.Ptr(0)},
		{
			Type:  AnthropicStreamEventTypeContentBlockStart,
			Index: schemas.Ptr(1),
			ContentBlock: &AnthropicContentBlock{
				Type: AnthropicContentBlockTypeToolUse,
				ID:   schemas.Ptr("toolu_01"),
				Name: schemas.Ptr("get_weather"),
			},
		},
		{
			Type:  AnthropicStreamEventTypeContentBlockDelta,
			Index: schemas.Ptr(1),
			Delta: &AnthropicStreamDelta{
				Type:        AnthropicStreamDeltaTypeInputJSON,
				PartialJSON: schemas.Ptr(`{"location":"Paris"}`),
			},
		},
		{Type: AnthropicStreamEventTypeContentBlockStop, Index: schemas.Ptr(1)},
		{
			Type:  AnthropicStreamEventTypeContentBlockStart,
			Index: schemas.Ptr(2),
			ContentBlock: &AnthropicContentBlock{
				Type: AnthropicContentBlockTypeThinking,
			},
		},
		{
			Type:  AnthropicStreamEventTypeContentBlockDelta,
			Index: schemas.Ptr(2),
			Delta: &AnthropicStreamDelta{
				Type:     AnthropicStreamDeltaTypeThinking,
				Thinking: schemas.Ptr("second thought"),
			},
		},
		{
			Type:  AnthropicStreamEventTypeContentBlockDelta,
			Index: schemas.Ptr(2),
			Delta: &AnthropicStreamDelta{
				Type:      AnthropicStreamDeltaTypeSignature,
				Signature: schemas.Ptr("sig-second"),
			},
		},
		{Type: AnthropicStreamEventTypeContentBlockStop, Index: schemas.Ptr(2)},
		{
			Type:  AnthropicStreamEventTypeMessageDelta,
			Delta: &AnthropicStreamDelta{StopReason: &stopReason},
		},
		{Type: AnthropicStreamEventTypeMessageStop},
	}

	responses := driveResponsesStream(t, events)

	var completed *schemas.BifrostResponsesResponse
	for _, r := range responses {
		if r.Type == schemas.ResponsesStreamResponseTypeCompleted && r.Response != nil {
			completed = r.Response
		}
	}
	if completed == nil {
		t.Fatal("expected a response.completed event")
	}
	if len(completed.Output) != 3 {
		t.Fatalf("response.completed output has %d items, want 3 (reasoning + function_call + reasoning)", len(completed.Output))
	}

	wantBlocks := []struct {
		text      string
		signature string
	}{
		{"first thought", "sig-first"},
		{"", ""}, // output index 1 is the function call
		{"second thought", "sig-second"},
	}
	for i, want := range wantBlocks {
		if want.text == "" {
			continue
		}
		item := completed.Output[i]
		if item.Type == nil {
			t.Fatalf("output[%d] type is nil, want reasoning", i)
		}
		if *item.Type != schemas.ResponsesMessageTypeReasoning {
			t.Fatalf("output[%d] type = %q, want reasoning", i, *item.Type)
		}
		b := reasoningTextBlock(&item)
		if b == nil || b.Text == nil || *b.Text != want.text {
			t.Errorf("output[%d] reasoning text = %+v, want %q", i, b, want.text)
			continue
		}
		if b.Signature == nil || *b.Signature != want.signature {
			t.Errorf("output[%d] reasoning signature = %v, want %q", i, b.Signature, want.signature)
		}
	}
}

// TestToBifrostResponsesStream_VisibleThinkingWithoutSignature covers thinking
// blocks that never receive a signature delta: the completed reasoning item
// still carries the accumulated text, with no signature attached.
func TestToBifrostResponsesStream_VisibleThinkingWithoutSignature(t *testing.T) {
	t.Parallel()

	const fullText = "unsigned reasoning"
	responses := driveResponsesStream(t, visibleThinkingToolUseLifecycle([]string{fullText}, ""))

	var itemDone *schemas.ResponsesMessage
	for _, r := range responses {
		if r.Type == schemas.ResponsesStreamResponseTypeOutputItemDone && r.OutputIndex != nil && *r.OutputIndex == 0 {
			itemDone = r.Item
		}
	}
	if itemDone == nil {
		t.Fatal("expected an output_item.done event for the reasoning block")
	}
	if itemDone.Type == nil {
		t.Fatal("output_item.done item type is nil, want reasoning")
	}
	if *itemDone.Type != schemas.ResponsesMessageTypeReasoning {
		t.Fatalf("output_item.done item type = %q, want reasoning", *itemDone.Type)
	}
	block := reasoningTextBlock(itemDone)
	if block == nil || block.Text == nil || *block.Text != fullText {
		t.Fatalf("output_item.done reasoning text = %+v, want %q", block, fullText)
	}
	if block.Signature != nil {
		t.Errorf("output_item.done reasoning signature = %q, want none", *block.Signature)
	}
}
