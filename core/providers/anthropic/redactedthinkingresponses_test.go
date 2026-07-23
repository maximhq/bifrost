package anthropic

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// driveResponsesStream replays a sequence of Anthropic SSE events through
// ToBifrostResponsesStream against a fresh stream state and returns every
// Bifrost stream response emitted, in order.
func driveResponsesStream(t *testing.T, events []*AnthropicStreamEvent) []*schemas.BifrostResponsesStreamResponse {
	t.Helper()

	state := AcquireAnthropicResponsesStreamState()
	defer ReleaseAnthropicResponsesStreamState(state)

	var out []*schemas.BifrostResponsesStreamResponse
	seq := 0
	for _, event := range events {
		responses, err, _ := event.ToBifrostResponsesStream(context.Background(), seq, state)
		if err != nil {
			t.Fatalf("unexpected error converting %s: %v", event.Type, err)
		}
		out = append(out, responses...)
		seq += len(responses)
	}
	return out
}

// redactedThinkingLifecycle returns the real Anthropic SSE shape of a response
// whose only content block is a redacted_thinking block: the block arrives
// complete in content_block_start (no deltas follow).
func redactedThinkingLifecycle(data string) []*AnthropicStreamEvent {
	stopReason := AnthropicStopReasonEndTurn
	return []*AnthropicStreamEvent{
		{
			Type: AnthropicStreamEventTypeMessageStart,
			Message: &AnthropicMessageResponse{
				ID:    "msg_redacted_stream",
				Model: "claude-sonnet-4-5-20250929",
			},
		},
		{
			Type:  AnthropicStreamEventTypeContentBlockStart,
			Index: schemas.Ptr(0),
			ContentBlock: &AnthropicContentBlock{
				Type: AnthropicContentBlockTypeRedactedThinking,
				Data: schemas.Ptr(data),
			},
		},
		{
			Type:  AnthropicStreamEventTypeContentBlockStop,
			Index: schemas.Ptr(0),
		},
		{
			Type:  AnthropicStreamEventTypeMessageDelta,
			Delta: &AnthropicStreamDelta{StopReason: &stopReason},
		},
		{
			Type: AnthropicStreamEventTypeMessageStop,
		},
	}
}

func findReasoningItemsWithEncryptedContent(responses []*schemas.BifrostResponsesStreamResponse, respType schemas.ResponsesStreamResponseType) []*schemas.ResponsesMessage {
	var items []*schemas.ResponsesMessage
	for _, r := range responses {
		if r.Type != respType || r.Item == nil {
			continue
		}
		if r.Item.Type == nil || *r.Item.Type != schemas.ResponsesMessageTypeReasoning {
			continue
		}
		if r.Item.ResponsesReasoning == nil || r.Item.ResponsesReasoning.EncryptedContent == nil {
			continue
		}
		items = append(items, r.Item)
	}
	return items
}

// TestToBifrostResponsesStream_RedactedThinkingLifecycle replays the streaming
// shape Anthropic uses for redacted thinking and asserts the encrypted payload
// survives into output_item.added, output_item.done, and response.completed.
// Without the redacted_thinking case in the content_block_start handler, the
// block contributes nothing to the converted stream.
func TestToBifrostResponsesStream_RedactedThinkingLifecycle(t *testing.T) {
	t.Parallel()

	const encrypted = "EmwKAhgBEgy3vaFTgeKrzXhpwEr_TEST_PAYLOAD"
	responses := driveResponsesStream(t, redactedThinkingLifecycle(encrypted))

	// output_item.added carries the reasoning item with the encrypted payload
	added := findReasoningItemsWithEncryptedContent(responses, schemas.ResponsesStreamResponseTypeOutputItemAdded)
	if len(added) != 1 {
		t.Fatalf("want 1 output_item.added reasoning item with encrypted content, got %d", len(added))
	}
	if got := *added[0].ResponsesReasoning.EncryptedContent; got != encrypted {
		t.Errorf("output_item.added encrypted content = %q, want %q", got, encrypted)
	}
	if added[0].ID == nil || *added[0].ID == "" {
		t.Error("output_item.added reasoning item should carry a stable ID")
	}

	// content_block_stop emits the matching output_item.done
	done := findReasoningItemsWithEncryptedContent(responses, schemas.ResponsesStreamResponseTypeOutputItemDone)
	if len(done) != 1 {
		t.Fatalf("want 1 output_item.done reasoning item with encrypted content, got %d", len(done))
	}
	if got := *done[0].ResponsesReasoning.EncryptedContent; got != encrypted {
		t.Errorf("output_item.done encrypted content = %q, want %q", got, encrypted)
	}

	// response.completed carries the item in its Output array
	var completed *schemas.BifrostResponsesResponse
	for _, r := range responses {
		if r.Type == schemas.ResponsesStreamResponseTypeCompleted && r.Response != nil {
			completed = r.Response
		}
	}
	if completed == nil {
		t.Fatal("expected a response.completed event")
	}
	foundInOutput := false
	for _, item := range completed.Output {
		if item.Type != nil && *item.Type == schemas.ResponsesMessageTypeReasoning &&
			item.ResponsesReasoning != nil &&
			item.ResponsesReasoning.EncryptedContent != nil &&
			*item.ResponsesReasoning.EncryptedContent == encrypted {
			foundInOutput = true
		}
	}
	if !foundInOutput {
		t.Errorf("response.completed output should contain the redacted reasoning item, got %d items", len(completed.Output))
	}
}

// TestToBifrostResponsesStream_RedactedThinkingWithoutDataSkipped mirrors the
// chat-surface guard: a redacted_thinking block with no data contributes
// nothing at all. That includes its content_block_stop, which otherwise falls
// into the generic done path and synthesizes an orphan output_item.done with
// an empty message shell.
func TestToBifrostResponsesStream_RedactedThinkingWithoutDataSkipped(t *testing.T) {
	t.Parallel()

	for name, data := range map[string]*string{
		"nil data":   nil,
		"empty data": schemas.Ptr(""),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			state := AcquireAnthropicResponsesStreamState()
			defer ReleaseAnthropicResponsesStreamState(state)

			start := &AnthropicStreamEvent{
				Type:  AnthropicStreamEventTypeContentBlockStart,
				Index: schemas.Ptr(0),
				ContentBlock: &AnthropicContentBlock{
					Type: AnthropicContentBlockTypeRedactedThinking,
					Data: data,
				},
			}
			responses, err, isLast := start.ToBifrostResponsesStream(context.Background(), 0, state)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if isLast {
				t.Error("should not be last chunk")
			}
			if len(responses) != 0 {
				t.Errorf("expected 0 responses for a redacted_thinking start without data, got %d", len(responses))
			}
			if len(state.OutputItems) != 0 {
				t.Errorf("expected no stored output items, got %d", len(state.OutputItems))
			}

			stop := &AnthropicStreamEvent{
				Type:  AnthropicStreamEventTypeContentBlockStop,
				Index: schemas.Ptr(0),
			}
			responses, err, _ = stop.ToBifrostResponsesStream(context.Background(), 0, state)
			if err != nil {
				t.Fatalf("unexpected error on stop: %v", err)
			}
			if len(responses) != 0 {
				t.Errorf("expected the data-less block's stop to emit nothing, got %d responses (orphan output_item.done)", len(responses))
			}
			if len(state.ContentIndexToBlockType) != 0 {
				t.Errorf("expected block-type tracking to be cleaned up on stop, got %d entries", len(state.ContentIndexToBlockType))
			}
			if state.CurrentOutputIndex != 0 {
				t.Errorf("expected no output index to be reserved for a data-less block, got CurrentOutputIndex=%d", state.CurrentOutputIndex)
			}

			// A following block must keep its position: the skipped block did not
			// shift output indices.
			textStart := &AnthropicStreamEvent{
				Type:  AnthropicStreamEventTypeContentBlockStart,
				Index: schemas.Ptr(1),
				ContentBlock: &AnthropicContentBlock{
					Type: AnthropicContentBlockTypeText,
					Text: schemas.Ptr(""),
				},
			}
			responses, err, _ = textStart.ToBifrostResponsesStream(context.Background(), 0, state)
			if err != nil {
				t.Fatalf("unexpected error on following text start: %v", err)
			}
			if len(responses) == 0 || responses[0].OutputIndex == nil || *responses[0].OutputIndex != 0 {
				t.Errorf("expected the following text block to take output index 0, got %+v", responses)
			}
		})
	}
}

// TestToBifrostResponsesStream_MixedRedactedAndVisibleThinking streams a
// redacted block, a visible thinking block, and a text block in one response
// and asserts each keeps its own output item: the redacted payload lands on
// its own reasoning item and does not bleed into the visible reasoning or the
// text message.
func TestToBifrostResponsesStream_MixedRedactedAndVisibleThinking(t *testing.T) {
	t.Parallel()

	const encrypted = "ENC_MIXED_PAYLOAD"
	stopReason := AnthropicStopReasonEndTurn
	events := []*AnthropicStreamEvent{
		{
			Type: AnthropicStreamEventTypeMessageStart,
			Message: &AnthropicMessageResponse{
				ID:    "msg_mixed_stream",
				Model: "claude-sonnet-4-5-20250929",
			},
		},
		{
			Type:  AnthropicStreamEventTypeContentBlockStart,
			Index: schemas.Ptr(0),
			ContentBlock: &AnthropicContentBlock{
				Type: AnthropicContentBlockTypeRedactedThinking,
				Data: schemas.Ptr(encrypted),
			},
		},
		{Type: AnthropicStreamEventTypeContentBlockStop, Index: schemas.Ptr(0)},
		{
			Type:  AnthropicStreamEventTypeContentBlockStart,
			Index: schemas.Ptr(1),
			ContentBlock: &AnthropicContentBlock{
				Type: AnthropicContentBlockTypeThinking,
			},
		},
		{
			Type:  AnthropicStreamEventTypeContentBlockDelta,
			Index: schemas.Ptr(1),
			Delta: &AnthropicStreamDelta{
				Type:     AnthropicStreamDeltaTypeThinking,
				Thinking: schemas.Ptr("visible reasoning"),
			},
		},
		{
			Type:  AnthropicStreamEventTypeContentBlockDelta,
			Index: schemas.Ptr(1),
			Delta: &AnthropicStreamDelta{
				Type:      AnthropicStreamDeltaTypeSignature,
				Signature: schemas.Ptr("sig-1"),
			},
		},
		{Type: AnthropicStreamEventTypeContentBlockStop, Index: schemas.Ptr(1)},
		{
			Type:  AnthropicStreamEventTypeContentBlockStart,
			Index: schemas.Ptr(2),
			ContentBlock: &AnthropicContentBlock{
				Type: AnthropicContentBlockTypeText,
				Text: schemas.Ptr(""),
			},
		},
		{
			Type:  AnthropicStreamEventTypeContentBlockDelta,
			Index: schemas.Ptr(2),
			Delta: &AnthropicStreamDelta{
				Type: AnthropicStreamDeltaTypeText,
				Text: schemas.Ptr("final answer"),
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

	// Exactly one added reasoning item carries the encrypted payload, on its own output index.
	encryptedAdded := findReasoningItemsWithEncryptedContent(responses, schemas.ResponsesStreamResponseTypeOutputItemAdded)
	if len(encryptedAdded) != 1 {
		t.Fatalf("want exactly 1 added reasoning item with encrypted content, got %d", len(encryptedAdded))
	}
	if got := *encryptedAdded[0].ResponsesReasoning.EncryptedContent; got != encrypted {
		t.Errorf("encrypted content = %q, want %q", got, encrypted)
	}

	// The visible thinking block still produces its own reasoning item, without the payload.
	var visibleReasoningAdded int
	var addedOutputIndices []int
	for _, r := range responses {
		if r.Type != schemas.ResponsesStreamResponseTypeOutputItemAdded || r.Item == nil {
			continue
		}
		if r.OutputIndex != nil {
			addedOutputIndices = append(addedOutputIndices, *r.OutputIndex)
		}
		if r.Item.Type != nil && *r.Item.Type == schemas.ResponsesMessageTypeReasoning &&
			(r.Item.ResponsesReasoning == nil || r.Item.ResponsesReasoning.EncryptedContent == nil) {
			visibleReasoningAdded++
		}
	}
	if visibleReasoningAdded != 1 {
		t.Errorf("want exactly 1 added reasoning item without encrypted content (the visible one), got %d", visibleReasoningAdded)
	}

	// Every added item keeps a distinct output index.
	seen := map[int]bool{}
	for _, idx := range addedOutputIndices {
		if seen[idx] {
			t.Errorf("output index %d used by more than one output_item.added", idx)
		}
		seen[idx] = true
	}

	// The text delta still flows through untouched.
	textSeen := false
	for _, r := range responses {
		if r.Type == schemas.ResponsesStreamResponseTypeOutputTextDelta && r.Delta != nil && *r.Delta == "final answer" {
			textSeen = true
		}
	}
	if !textSeen {
		t.Error("expected the text block's output_text.delta to be emitted")
	}
}

// TestToAnthropicResponsesStreamResponse_RedactedThinkingEgress feeds the
// output_item.added event produced by the inbound converter back through the
// Bifrost→Anthropic stream converter and asserts the passthrough client gets a
// redacted_thinking content_block_start with the payload, closing the loop for
// the Anthropic integration route.
func TestToAnthropicResponsesStreamResponse_RedactedThinkingEgress(t *testing.T) {
	t.Parallel()

	const encrypted = "ENC_EGRESS_PAYLOAD"
	responses := driveResponsesStream(t, redactedThinkingLifecycle(encrypted))

	var addedEvent *schemas.BifrostResponsesStreamResponse
	for _, r := range responses {
		if r.Type == schemas.ResponsesStreamResponseTypeOutputItemAdded {
			addedEvent = r
			break
		}
	}
	if addedEvent == nil {
		t.Fatal("expected an output_item.added event to feed the egress converter")
	}

	ctx := schemas.NewBifrostContext(nil, time.Time{})
	frames := ToAnthropicResponsesStreamResponse(ctx, addedEvent)

	found := false
	for _, frame := range frames {
		if frame == nil || frame.ContentBlock == nil {
			continue
		}
		if frame.Type == AnthropicStreamEventTypeContentBlockStart &&
			frame.ContentBlock.Type == AnthropicContentBlockTypeRedactedThinking &&
			frame.ContentBlock.Data != nil && *frame.ContentBlock.Data == encrypted {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a content_block_start frame with a redacted_thinking block carrying the payload on the anthropic egress, got %d frames", len(frames))
	}
}

// assertRedactedThinkingPrecedesToolUse asserts the outgoing Anthropic request
// contains an assistant message whose first content block is the replayed
// redacted_thinking block, followed by the tool_use block, which is the shape
// Anthropic requires on multi-turn tool use with thinking.
func assertRedactedThinkingPrecedesToolUse(t *testing.T, req *AnthropicMessageRequest, encrypted string) {
	t.Helper()

	for _, msg := range req.Messages {
		if msg.Role != AnthropicMessageRoleAssistant {
			continue
		}
		toolUseIdx := -1
		redactedIdx := -1
		for i, block := range msg.Content.ContentBlocks {
			switch block.Type {
			case AnthropicContentBlockTypeToolUse:
				toolUseIdx = i
			case AnthropicContentBlockTypeRedactedThinking:
				if block.Data != nil && *block.Data == encrypted {
					redactedIdx = i
				}
			}
		}
		if toolUseIdx == -1 {
			continue
		}
		if redactedIdx == -1 {
			t.Fatalf("assistant message with tool_use has no redacted_thinking block carrying the payload: %+v", msg.Content.ContentBlocks)
		}
		if redactedIdx > toolUseIdx {
			t.Fatalf("redacted_thinking (idx %d) must precede tool_use (idx %d)", redactedIdx, toolUseIdx)
		}
		return
	}
	t.Fatalf("no assistant message with a tool_use block in the outgoing request (%d messages)", len(req.Messages))
}

func functionCallItem() schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID:    schemas.Ptr("toolu_replay_01"),
			Name:      schemas.Ptr("get_weather"),
			Arguments: schemas.Ptr(`{"city":"Paris"}`),
		},
	}
}

// TestToAnthropicResponsesRequest_ReplaysStreamedRedactedThinking closes the
// multi-turn loop: the reasoning item emitted by the streaming ingress is
// round-tripped through JSON the way a client echoes its history, then
// converted back into an Anthropic request. The replayed assistant turn must
// carry the redacted_thinking block before the tool_use block. The streamed
// item carries an empty (non-nil) summary list next to encrypted_content, so
// a nil-check on the summary list drops the payload from the request.
func TestToAnthropicResponsesRequest_ReplaysStreamedRedactedThinking(t *testing.T) {
	t.Parallel()

	const encrypted = "ENC_REPLAY_PAYLOAD"
	responses := driveResponsesStream(t, redactedThinkingLifecycle(encrypted))
	added := findReasoningItemsWithEncryptedContent(responses, schemas.ResponsesStreamResponseTypeOutputItemAdded)
	if len(added) != 1 {
		t.Fatalf("want 1 streamed reasoning item to replay, got %d", len(added))
	}

	// Client echo: the item leaves and re-enters Bifrost as JSON.
	wire, err := json.Marshal(added[0])
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
	assertRedactedThinkingPrecedesToolUse(t, out, encrypted)
}

// TestToAnthropicResponsesRequest_ReplaysRedactedItemWithEmptySummaryList pins
// the request-converter half of the fix in isolation, using the exact item
// shape the non-streaming converter produces for a redacted_thinking block
// (empty non-nil summary list plus encrypted_content).
func TestToAnthropicResponsesRequest_ReplaysRedactedItemWithEmptySummaryList(t *testing.T) {
	t.Parallel()

	const encrypted = "ENC_NONSTREAM_SHAPE"
	item := schemas.ResponsesMessage{
		ID:   schemas.Ptr("rs_nonstream_shape"),
		Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
		ResponsesReasoning: &schemas.ResponsesReasoning{
			Summary:          []schemas.ResponsesReasoningSummary{},
			EncryptedContent: schemas.Ptr(encrypted),
		},
	}

	req := &schemas.BifrostResponsesRequest{
		Model: "claude-sonnet-4-5-20250929",
		Input: []schemas.ResponsesMessage{item, functionCallItem()},
	}
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	out, err := ToAnthropicResponsesRequest(ctx, req)
	if err != nil {
		t.Fatalf("ToAnthropicResponsesRequest: %v", err)
	}
	assertRedactedThinkingPrecedesToolUse(t, out, encrypted)
}
