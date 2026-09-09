package anthropic

import (
	"context"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// Regression tests for the Anthropic stream egress emitting a content_block_delta
// into a content block whose content_block_start opened it as an incompatible type.
//
// Claude Code's stream assembler enforces this invariant and aborts the whole turn
// when it is violated -- reported as
// "[ERROR] Error in API request: Content block is not a thinking block" by users
// running Claude Code / the Claude Agent SDK against OpenAI models through Bifrost.
// The assembler's rules (de-minified from the shipped CLI) are:
//
//	signature_delta   -> block must be "thinking" (redacted_thinking THROWS)
//	thinking_delta    -> block must be "thinking"; "redacted_thinking" is tolerated
//	                     (silently discarded, so reasoning text is lost)
//	text_delta        -> block must be "text"
//	input_json_delta  -> block must be a tool_use flavour
//
// Bifrost's converter tracked which content-block INDEX an item owned but never
// which TYPE was opened there, so every delta emitter wrote blind.

// blockAcceptsDelta mirrors the client-side rules above. redacted_thinking is
// accepted for thinking_delta because the client tolerates it rather than throwing;
// the separate assertion that reasoning text stays visible is what catches that case.
// mcp_tool_use is grouped with the tool_use flavours: it is a legitimate Anthropic
// block type carrying input_json_delta, and lumping it in keeps this helper from
// failing on a concern unrelated to the bug under test.
func blockAcceptsDelta(blockType AnthropicContentBlockType, deltaType AnthropicStreamDeltaType) bool {
	switch deltaType {
	case AnthropicStreamDeltaTypeSignature:
		return blockType == AnthropicContentBlockTypeThinking
	case AnthropicStreamDeltaTypeThinking:
		return blockType == AnthropicContentBlockTypeThinking ||
			blockType == AnthropicContentBlockTypeRedactedThinking
	case AnthropicStreamDeltaTypeText:
		return blockType == AnthropicContentBlockTypeText
	case AnthropicStreamDeltaTypeInputJSON:
		return blockType == AnthropicContentBlockTypeToolUse ||
			blockType == AnthropicContentBlockTypeServerToolUse ||
			blockType == AnthropicContentBlockTypeMCPToolUse
	default:
		// citations, compaction and the message-level deltas are not part of the
		// client's block-type switch.
		return true
	}
}

const streamTestEncrypted = "ENC_REASONING_PAYLOAD"

// openAIReasoningStreamFrames builds the Bifrost Responses stream an OpenAI-family
// reasoning model produces for a "think, call a tool, answer" turn. The reasoning
// item carries BOTH a visible summary and encrypted_content at output_item.added --
// the shape a replayed or synthesized (non-stream -> stream) item has, and the one
// that tripped the redacted_thinking downgrade.
func openAIReasoningStreamFrames() []*schemas.BifrostResponsesStreamResponse {
	reasoningID := "rs_1"
	toolCallID := "call_1"
	messageID := "msg_1"

	reasoningType := schemas.ResponsesMessageTypeReasoning
	functionType := schemas.ResponsesMessageTypeFunctionCall
	messageType := schemas.ResponsesMessageTypeMessage

	reasoningItem := &schemas.ResponsesMessage{
		ID:   &reasoningID,
		Type: &reasoningType,
		ResponsesReasoning: &schemas.ResponsesReasoning{
			Summary: []schemas.ResponsesReasoningSummary{{
				Type: schemas.ResponsesReasoningContentBlockTypeSummaryText,
				Text: "Ranking the sources by recency.",
			}},
			EncryptedContent: schemas.Ptr(streamTestEncrypted),
		},
	}

	toolItem := &schemas.ResponsesMessage{
		ID:   &toolCallID,
		Type: &functionType,
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID: &toolCallID,
			Name:   schemas.Ptr("lookup_cursor_access"),
		},
	}

	messageItem := &schemas.ResponsesMessage{
		ID:   &messageID,
		Type: &messageType,
	}

	// A response snapshot carrying reasoning output. The function-call branch used to
	// consult this to decide that a first-position tool call was "really" a thinking
	// block, so it has to be present for that regression to be exercised.
	responseWithReasoning := &schemas.BifrostResponsesResponse{
		Output: []schemas.ResponsesMessage{*reasoningItem},
	}

	extra := schemas.BifrostResponseExtraFields{Provider: schemas.OpenAI}

	return []*schemas.BifrostResponsesStreamResponse{
		{Type: schemas.ResponsesStreamResponseTypeOutputItemAdded, OutputIndex: schemas.Ptr(0), Item: reasoningItem, ExtraFields: extra},
		{Type: schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta, OutputIndex: schemas.Ptr(0), ItemID: &reasoningID, Delta: schemas.Ptr("Ranking the sources "), ExtraFields: extra},
		{Type: schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta, OutputIndex: schemas.Ptr(0), ItemID: &reasoningID, Delta: schemas.Ptr("by recency."), ExtraFields: extra},
		{Type: schemas.ResponsesStreamResponseTypeOutputItemDone, OutputIndex: schemas.Ptr(0), Item: reasoningItem, ExtraFields: extra},

		{Type: schemas.ResponsesStreamResponseTypeOutputItemAdded, OutputIndex: schemas.Ptr(1), ContentIndex: schemas.Ptr(0), Item: toolItem, Response: responseWithReasoning, ExtraFields: extra},
		{Type: schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta, OutputIndex: schemas.Ptr(1), ItemID: &toolCallID, Arguments: schemas.Ptr(`{"query":"cursor"}`), ExtraFields: extra},
		{Type: schemas.ResponsesStreamResponseTypeOutputItemDone, OutputIndex: schemas.Ptr(1), Item: toolItem, ExtraFields: extra},

		{Type: schemas.ResponsesStreamResponseTypeOutputItemAdded, OutputIndex: schemas.Ptr(2), Item: messageItem, ExtraFields: extra},
		{Type: schemas.ResponsesStreamResponseTypeOutputTextDelta, OutputIndex: schemas.Ptr(2), ItemID: &messageID, Delta: schemas.Ptr("Submit an IDE Access request."), ExtraFields: extra},
		{Type: schemas.ResponsesStreamResponseTypeOutputItemDone, OutputIndex: schemas.Ptr(2), Item: messageItem, ExtraFields: extra},
	}
}

// driveAnthropicEgress runs frames through the egress converter and returns every
// emitted Anthropic stream event in order, sharing one stream state across the turn.
//
// Note that enforceStreamBlockTypes suppresses an invalid delta on the way out, so
// the emitted stream alone cannot prove a converter branch picked the right block
// type. That is why the assertions below check the shape positively -- that the
// reasoning text actually arrives in a thinking block, that the payload arrives in
// its own redacted_thinking block, that a tool call opens as tool_use -- rather than
// only checking that nothing invalid escaped.
func driveAnthropicEgress(t *testing.T, frames []*schemas.BifrostResponsesStreamResponse) []*AnthropicStreamEvent {
	t.Helper()

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	t.Cleanup(cancel)

	var events []*AnthropicStreamEvent
	for _, frame := range frames {
		events = append(events, ToAnthropicResponsesStreamResponse(ctx, frame)...)
	}
	return events
}

// TestAnthropicStreamEgress_DeltaNeverTargetsMismatchedBlock is the direct
// regression for the reported abort: every content_block_delta must be compatible
// with the type its content_block_start opened.
func TestAnthropicStreamEgress_DeltaNeverTargetsMismatchedBlock(t *testing.T) {
	t.Parallel()

	openBlocks := map[int]AnthropicContentBlockType{}

	for _, event := range driveAnthropicEgress(t, openAIReasoningStreamFrames()) {
		if event == nil || event.Index == nil {
			continue
		}
		index := *event.Index

		switch event.Type {
		case AnthropicStreamEventTypeContentBlockStart:
			if event.ContentBlock == nil {
				t.Fatalf("content_block_start at index %d carried no content_block", index)
			}
			openBlocks[index] = event.ContentBlock.Type

		case AnthropicStreamEventTypeContentBlockDelta:
			if event.Delta == nil {
				continue
			}
			blockType, opened := openBlocks[index]
			if !opened {
				t.Fatalf("content_block_delta (%s) at index %d was never opened by a content_block_start", event.Delta.Type, index)
			}
			if !blockAcceptsDelta(blockType, event.Delta.Type) {
				t.Errorf("%s targets block %d opened as %q; the client aborts the turn on this", event.Delta.Type, index, blockType)
			}

		case AnthropicStreamEventTypeContentBlockStop:
			delete(openBlocks, index)
		}
	}
}

// TestAnthropicStreamEgress_SummaryAndEncryptedSplitIntoTwoBlocks locks the
// streaming shape to the convention the non-streaming converter already documents
// (convertBifrostReasoningToAnthropicThinking): a reasoning item holding both a
// visible summary and encrypted_content becomes a thinking block carrying the
// summary AND a separate redacted_thinking block carrying the payload -- not one
// block whose type is flipped, which silently swallows the reasoning text.
func TestAnthropicStreamEgress_SummaryAndEncryptedSplitIntoTwoBlocks(t *testing.T) {
	t.Parallel()

	var thinkingIndex *int
	var thinkingText strings.Builder
	var redactedPayload string

	for _, event := range driveAnthropicEgress(t, openAIReasoningStreamFrames()) {
		if event == nil {
			continue
		}
		if event.Type == AnthropicStreamEventTypeContentBlockStart && event.ContentBlock != nil {
			switch event.ContentBlock.Type {
			case AnthropicContentBlockTypeThinking:
				if thinkingIndex == nil && event.Index != nil {
					thinkingIndex = schemas.Ptr(*event.Index)
				}
			case AnthropicContentBlockTypeRedactedThinking:
				if event.ContentBlock.Data != nil {
					redactedPayload = *event.ContentBlock.Data
				}
			}
		}
		if event.Type == AnthropicStreamEventTypeContentBlockDelta &&
			event.Delta != nil && event.Delta.Type == AnthropicStreamDeltaTypeThinking &&
			event.Delta.Thinking != nil && thinkingIndex != nil && event.Index != nil &&
			*event.Index == *thinkingIndex {
			thinkingText.WriteString(*event.Delta.Thinking)
		}
	}

	if thinkingIndex == nil {
		t.Fatal("expected a thinking block to carry the visible summary")
	}
	if got := thinkingText.String(); got != "Ranking the sources by recency." {
		t.Errorf("reasoning text lost: thinking block accumulated %q", got)
	}
	if redactedPayload == "" {
		t.Fatal("expected a separate redacted_thinking block carrying encrypted_content")
	}
	if !strings.Contains(redactedPayload, streamTestEncrypted) {
		t.Errorf("redacted_thinking data %q does not carry the encrypted payload", redactedPayload)
	}
}

// TestAnthropicStreamEgress_FunctionCallNeverOpensAsThinking covers the sibling
// defect: a real function call in first position was opened as a thinking block
// whenever the response also carried reasoning, which strands the tool call (the
// client rejects its input_json_delta with "Content block is not a input_json block").
func TestAnthropicStreamEgress_FunctionCallNeverOpensAsThinking(t *testing.T) {
	t.Parallel()

	toolCallID := "call_1"
	functionType := schemas.ResponsesMessageTypeFunctionCall
	reasoningType := schemas.ResponsesMessageTypeReasoning

	frames := []*schemas.BifrostResponsesStreamResponse{{
		Type:         schemas.ResponsesStreamResponseTypeOutputItemAdded,
		OutputIndex:  schemas.Ptr(0),
		ContentIndex: schemas.Ptr(0),
		Item: &schemas.ResponsesMessage{
			ID:   &toolCallID,
			Type: &functionType,
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID: &toolCallID,
				Name:   schemas.Ptr("lookup_cursor_access"),
			},
		},
		Response: &schemas.BifrostResponsesResponse{
			Output: []schemas.ResponsesMessage{{Type: &reasoningType}},
		},
		ExtraFields: schemas.BifrostResponseExtraFields{Provider: schemas.OpenAI},
	}}

	for _, event := range driveAnthropicEgress(t, frames) {
		if event == nil || event.Type != AnthropicStreamEventTypeContentBlockStart || event.ContentBlock == nil {
			continue
		}
		if event.ContentBlock.Type != AnthropicContentBlockTypeToolUse {
			t.Fatalf("function call opened as %q, want tool_use", event.ContentBlock.Type)
		}
		if event.ContentBlock.Name == nil || *event.ContentBlock.Name != "lookup_cursor_access" {
			t.Error("tool_use block lost its name")
		}
	}
}

// reasoningAddedFrame builds the single output_item.added frame that opens a
// reasoning item, with the summary and encrypted_content already attached or not.
// The block type chosen on this frame is the whole decision under test: it is fixed
// the moment the block opens and every later delta has to be legal against it.
func reasoningAddedFrame(summary, encrypted string) *schemas.BifrostResponsesStreamResponse {
	reasoningID := "rs_1"
	reasoningType := schemas.ResponsesMessageTypeReasoning

	reasoning := &schemas.ResponsesReasoning{}
	if summary != "" {
		reasoning.Summary = []schemas.ResponsesReasoningSummary{{
			Type: schemas.ResponsesReasoningContentBlockTypeSummaryText,
			Text: summary,
		}}
	}
	if encrypted != "" {
		reasoning.EncryptedContent = schemas.Ptr(encrypted)
	}

	return &schemas.BifrostResponsesStreamResponse{
		Type:        schemas.ResponsesStreamResponseTypeOutputItemAdded,
		OutputIndex: schemas.Ptr(0),
		Item: &schemas.ResponsesMessage{
			ID:                 &reasoningID,
			Type:               &reasoningType,
			ResponsesReasoning: reasoning,
		},
		ExtraFields: schemas.BifrostResponseExtraFields{Provider: schemas.OpenAI},
	}
}

// firstBlockStart returns the type opened by the first content_block_start, or ""
// when the frames opened no block at all.
func firstBlockStart(events []*AnthropicStreamEvent) AnthropicContentBlockType {
	for _, event := range events {
		if event != nil && event.Type == AnthropicStreamEventTypeContentBlockStart && event.ContentBlock != nil {
			return event.ContentBlock.Type
		}
	}
	return ""
}

// TestAnthropicStreamEgress_ReasoningBlockTypeMatrix pins the block-type decision
// across every combination of (encrypted payload present) x (visible summary
// present). Only the redacted-only quadrant may open as redacted_thinking: that
// block is atomic, its payload rides in `data` on the start frame and no delta ever
// follows it. Every other quadrant is or may become incremental, and the deltas that
// follow are only legal against a thinking block.
//
// The live SDK repro cannot cover this. It only trips the bug when the upstream
// model happens to emit a visible summary, so a green run is indistinguishable from
// a run that never reached the trigger -- which is exactly what 8/8 attempts against
// a local gateway did, every one returning redacted-only reasoning.
func TestAnthropicStreamEgress_ReasoningBlockTypeMatrix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		summary   string
		encrypted string
		want      AnthropicContentBlockType
	}{
		{
			name:      "summary and payload opens thinking so summary deltas are legal",
			summary:   "Ranking the sources by recency.",
			encrypted: streamTestEncrypted,
			want:      AnthropicContentBlockTypeThinking,
		},
		{
			name:      "payload only is atomic and opens redacted_thinking",
			encrypted: streamTestEncrypted,
			want:      AnthropicContentBlockTypeRedactedThinking,
		},
		{
			name:    "summary only opens thinking",
			summary: "Ranking the sources by recency.",
			want:    AnthropicContentBlockTypeThinking,
		},
		{
			name: "empty reasoning opens thinking because deltas may still arrive",
			want: AnthropicContentBlockTypeThinking,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			frames := []*schemas.BifrostResponsesStreamResponse{reasoningAddedFrame(tt.summary, tt.encrypted)}
			if got := firstBlockStart(driveAnthropicEgress(t, frames)); got != tt.want {
				t.Errorf("reasoning item opened as %q, want %q", got, tt.want)
			}
		})
	}
}

// TestAnthropicStreamEgress_LateSummaryNeverLandsOnRedactedBlock covers the ordering
// the block-type matrix cannot: an item that arrives at output_item.added carrying
// encrypted_content and NO summary yet, whose summary text then streams in
// afterwards. Judged on the added frame alone that item looks redacted-only, so
// opening redacted_thinking there is locally reasonable and globally wrong -- the
// summary deltas that follow are thinking_delta, which the client silently discards
// against a redacted block. That failure is quiet: no abort, no error, the user just
// loses the reasoning text.
//
// Passing requires the summary to remain visible, either by opening the block as
// thinking or by routing the late summary into its own thinking block.
func TestAnthropicStreamEgress_LateSummaryNeverLandsOnRedactedBlock(t *testing.T) {
	t.Parallel()

	reasoningID := "rs_1"

	// Added with the payload only; the summary shows up two frames later.
	frames := []*schemas.BifrostResponsesStreamResponse{
		reasoningAddedFrame("", streamTestEncrypted),
		{
			Type:        schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta,
			OutputIndex: schemas.Ptr(0),
			ItemID:      &reasoningID,
			Delta:       schemas.Ptr("Ranking the sources "),
			ExtraFields: schemas.BifrostResponseExtraFields{Provider: schemas.OpenAI},
		},
		{
			Type:        schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta,
			OutputIndex: schemas.Ptr(0),
			ItemID:      &reasoningID,
			Delta:       schemas.Ptr("by recency."),
			ExtraFields: schemas.BifrostResponseExtraFields{Provider: schemas.OpenAI},
		},
	}

	openBlocks := map[int]AnthropicContentBlockType{}
	var visible strings.Builder

	for _, event := range driveAnthropicEgress(t, frames) {
		if event == nil || event.Index == nil {
			continue
		}
		index := *event.Index

		switch event.Type {
		case AnthropicStreamEventTypeContentBlockStart:
			if event.ContentBlock != nil {
				openBlocks[index] = event.ContentBlock.Type
			}
		case AnthropicStreamEventTypeContentBlockDelta:
			if event.Delta == nil || event.Delta.Type != AnthropicStreamDeltaTypeThinking {
				continue
			}
			if blockType := openBlocks[index]; blockType != AnthropicContentBlockTypeThinking {
				t.Errorf("thinking_delta targets block %d opened as %q; the client discards it and the reasoning text is lost", index, blockType)
				continue
			}
			if event.Delta.Thinking != nil {
				visible.WriteString(*event.Delta.Thinking)
			}
		case AnthropicStreamEventTypeContentBlockStop:
			delete(openBlocks, index)
		}
	}

	if got := visible.String(); got != "Ranking the sources by recency." {
		t.Errorf("late summary did not survive into a thinking block: got %q", got)
	}
}

// TestAnthropicStreamEgress_SignatureDeltaOnlyTargetsThinking isolates the single
// delta/block pairing the client throws on rather than tolerates -- signature_delta
// against redacted_thinking -- which is the frame that surfaced to users as
// "Content block is not a thinking block". blockAcceptsDelta folds this in with the
// tolerated mismatches; asserting it separately keeps the hard-abort case from being
// masked if that helper is ever loosened.
func TestAnthropicStreamEgress_SignatureDeltaOnlyTargetsThinking(t *testing.T) {
	t.Parallel()

	openBlocks := map[int]AnthropicContentBlockType{}

	for _, event := range driveAnthropicEgress(t, openAIReasoningStreamFrames()) {
		if event == nil || event.Index == nil {
			continue
		}
		index := *event.Index

		switch event.Type {
		case AnthropicStreamEventTypeContentBlockStart:
			if event.ContentBlock != nil {
				openBlocks[index] = event.ContentBlock.Type
			}
		case AnthropicStreamEventTypeContentBlockDelta:
			if event.Delta == nil || event.Delta.Type != AnthropicStreamDeltaTypeSignature {
				continue
			}
			if blockType := openBlocks[index]; blockType != AnthropicContentBlockTypeThinking {
				t.Errorf("signature_delta targets block %d opened as %q; the client throws \"Content block is not a thinking block\" and aborts the turn", index, blockType)
			}
		case AnthropicStreamEventTypeContentBlockStop:
			delete(openBlocks, index)
		}
	}
}

// TestAnthropicStreamEgress_MisclassifiedReasoningUsesSameBlockRule covers the
// sibling of the reasoning branch: an item typed as a function call that actually
// carries reasoning (providers mislabel these when thinking is enabled). It reaches
// a separate block-type decision that must follow the same rule -- redacted_thinking
// only when the payload is all there is -- because the summary and signature deltas
// that follow are indistinguishable from the ones the reasoning branch emits.
//
// Judging on "has encrypted content" alone reopens the original defect on this path:
// a summary-bearing item opens redacted_thinking, and the signature_delta that
// follows is the frame the client throws "Content block is not a thinking block" on.
func TestAnthropicStreamEgress_MisclassifiedReasoningUsesSameBlockRule(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		summary   string
		encrypted string
		want      AnthropicContentBlockType
	}{
		{
			name:      "summary and payload opens thinking",
			summary:   "Ranking the sources by recency.",
			encrypted: streamTestEncrypted,
			want:      AnthropicContentBlockTypeThinking,
		},
		{
			name:      "payload only is atomic and opens redacted_thinking",
			encrypted: streamTestEncrypted,
			want:      AnthropicContentBlockTypeRedactedThinking,
		},
		{
			name:    "summary only opens thinking",
			summary: "Ranking the sources by recency.",
			want:    AnthropicContentBlockTypeThinking,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			callID := "call_1"
			functionType := schemas.ResponsesMessageTypeFunctionCall

			reasoning := &schemas.ResponsesReasoning{}
			if tt.summary != "" {
				reasoning.Summary = []schemas.ResponsesReasoningSummary{{
					Type: schemas.ResponsesReasoningContentBlockTypeSummaryText,
					Text: tt.summary,
				}}
			}
			if tt.encrypted != "" {
				reasoning.EncryptedContent = schemas.Ptr(tt.encrypted)
			}

			frames := []*schemas.BifrostResponsesStreamResponse{{
				Type:         schemas.ResponsesStreamResponseTypeOutputItemAdded,
				OutputIndex:  schemas.Ptr(0),
				ContentIndex: schemas.Ptr(0),
				Item: &schemas.ResponsesMessage{
					ID:                 &callID,
					Type:               &functionType,
					ResponsesReasoning: reasoning,
				},
				ExtraFields: schemas.BifrostResponseExtraFields{Provider: schemas.OpenAI},
			}}

			if got := firstBlockStart(driveAnthropicEgress(t, frames)); got != tt.want {
				t.Errorf("misclassified reasoning item opened as %q, want %q", got, tt.want)
			}
		})
	}
}
