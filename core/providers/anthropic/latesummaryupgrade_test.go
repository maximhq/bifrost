package anthropic

import (
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// A reasoning item that looked redacted-only at output_item.added and then streamed
// summary text is reopened as a thinking block mid-stream
// (upgradeLateSummaryToThinkingBlock). That reopened block reads as "opened as
// thinking" to the split at output_item.done, which would otherwise emit the
// encrypted payload a second time -- once in `data` on the block that was closed,
// once as a fresh redacted_thinking block.
//
// Duplicating it is not cosmetic: the client replays both halves next turn, so the
// same signed reasoning state is submitted twice under one item.
//
// This drives the full lifecycle (added -> deltas -> done), which
// TestAnthropicStreamEgress_LateSummaryNeverLandsOnRedactedBlock deliberately does
// not: that test isolates the delta/block pairing.
func TestAnthropicStreamEgress_LateSummaryDoesNotDuplicateEncryptedPayload(t *testing.T) {
	t.Parallel()

	reasoningID := "rs_1"
	reasoningType := schemas.ResponsesMessageTypeReasoning

	summaryDelta := func(text string) *schemas.BifrostResponsesStreamResponse {
		return &schemas.BifrostResponsesStreamResponse{
			Type:        schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta,
			OutputIndex: schemas.Ptr(0),
			ItemID:      &reasoningID,
			Delta:       schemas.Ptr(text),
			ExtraFields: schemas.BifrostResponseExtraFields{Provider: schemas.OpenAI},
		}
	}

	frames := []*schemas.BifrostResponsesStreamResponse{
		reasoningAddedFrame("", streamTestEncrypted),
		summaryDelta("Ranking the sources "),
		summaryDelta("by recency."),
		{
			Type:        schemas.ResponsesStreamResponseTypeOutputItemDone,
			OutputIndex: schemas.Ptr(0),
			Item: &schemas.ResponsesMessage{
				ID:   &reasoningID,
				Type: &reasoningType,
				ResponsesReasoning: &schemas.ResponsesReasoning{
					Summary: []schemas.ResponsesReasoningSummary{{
						Type: schemas.ResponsesReasoningContentBlockTypeSummaryText,
						Text: "Ranking the sources by recency.",
					}},
					EncryptedContent: schemas.Ptr(streamTestEncrypted),
				},
			},
			ExtraFields: schemas.BifrostResponseExtraFields{Provider: schemas.OpenAI},
		},
	}

	var redactedPayloads []string
	openBlocks := map[int]AnthropicContentBlockType{}
	var visible strings.Builder

	for _, event := range driveAnthropicEgress(t, frames) {
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
			if event.ContentBlock.Type == AnthropicContentBlockTypeRedactedThinking && event.ContentBlock.Data != nil {
				redactedPayloads = append(redactedPayloads, *event.ContentBlock.Data)
			}
		case AnthropicStreamEventTypeContentBlockDelta:
			if event.Delta == nil || event.Delta.Type != AnthropicStreamDeltaTypeThinking {
				continue
			}
			if blockType := openBlocks[index]; blockType != AnthropicContentBlockTypeThinking {
				t.Errorf("thinking_delta targets block %d opened as %q", index, blockType)
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
		t.Errorf("late summary lost: thinking block accumulated %q", got)
	}
	if len(redactedPayloads) != 1 {
		t.Fatalf("encrypted payload must be delivered exactly once, got %d redacted_thinking blocks: %q", len(redactedPayloads), redactedPayloads)
	}
	if !strings.Contains(redactedPayloads[0], streamTestEncrypted) {
		t.Errorf("redacted_thinking data %q does not carry the encrypted payload", redactedPayloads[0])
	}
	if len(openBlocks) != 0 {
		t.Errorf("every content_block_start must be closed, left open: %v", openBlocks)
	}
}
