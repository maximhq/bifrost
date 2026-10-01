package anthropic

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// Bedrock repeats a streamed signature in the completed item's encrypted_content.
// It is the same signed thinking block, not another redacted thinking block.
func TestAnthropicStreamEgress_SignatureReplay(t *testing.T) {
	for _, tc := range []struct {
		name         string
		payload      string
		wantRedacted int
	}{
		{"same signature", "signed-thinking", 0},
		{"distinct encrypted payload", "opaque-reasoning", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frames := []*schemas.BifrostResponsesStreamResponse{reasoningAddedFrame("", "")}
			frames = append(frames, &schemas.BifrostResponsesStreamResponse{
				Type:        schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta,
				OutputIndex: schemas.Ptr(0), ItemID: schemas.Ptr("rs_1"), Delta: schemas.Ptr("Read the file."),
			})
			for _, part := range []string{"signed-", "thinking"} {
				frames = append(frames, &schemas.BifrostResponsesStreamResponse{
					Type:        schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta,
					OutputIndex: schemas.Ptr(0), ItemID: schemas.Ptr("rs_1"), Signature: schemas.Ptr(part),
				})
			}
			frames = append(frames, &schemas.BifrostResponsesStreamResponse{
				Type: schemas.ResponsesStreamResponseTypeOutputItemDone, OutputIndex: schemas.Ptr(0),
				Item: &schemas.ResponsesMessage{
					ID: schemas.Ptr("rs_1"), Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
					ResponsesReasoning: &schemas.ResponsesReasoning{
						Summary:          []schemas.ResponsesReasoningSummary{{Type: schemas.ResponsesReasoningContentBlockTypeSummaryText, Text: "Read the file."}},
						EncryptedContent: schemas.Ptr(tc.payload),
					},
				},
			})
			for _, frame := range frames {
				frame.ExtraFields.Provider = schemas.Bedrock
			}
			redacted, thinking, signature := 0, "", ""
			for _, event := range driveAnthropicEgress(t, frames) {
				if event.ContentBlock != nil && event.ContentBlock.Type == AnthropicContentBlockTypeRedactedThinking {
					redacted++
				}
				if event.Delta != nil {
					if event.Delta.Thinking != nil {
						thinking += *event.Delta.Thinking
					}
					if event.Delta.Signature != nil {
						signature += *event.Delta.Signature
					}
				}
			}
			if thinking != "Read the file." || signature != "signed-thinking" {
				t.Fatalf("thinking/signature changed: %q / %q", thinking, signature)
			}
			if redacted != tc.wantRedacted {
				t.Fatalf("got %d redacted blocks, want %d: streamed signature must not be replayed twice", redacted, tc.wantRedacted)
			}
		})
	}
}
