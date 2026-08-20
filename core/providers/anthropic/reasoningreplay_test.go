package anthropic

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// convertBifrostReasoningToAnthropicThinking's INNER branches are already
// hardened against this defect class -- they gate on len(Summary) > 0 rather
// than nil, and the summary and encrypted arms are deliberately independent ifs
// so a reasoning item carrying both survives whole. The comments there spell
// that reasoning out.
//
// The outer `else if` defeated it. `msg.Content != nil && msg.Content.ContentBlocks
// != nil` is true for a NON-NIL BUT EMPTY slice, so such a message took the
// content-block branch, emitted nothing, and never reached the hardened code
// below. Same shape as the Bedrock and Cohere defects: data dropped silently,
// no error, the model simply loses its prior reasoning.
func TestConvertBifrostReasoningToAnthropicThinkingFallsThrough(t *testing.T) {
	const encrypted = "EqQBCgIYAhIM...fixture"
	ctx := &schemas.BifrostContext{}

	tests := []struct {
		name           string
		msg            *schemas.ResponsesMessage
		wantThinking   []string // Thinking texts expected, in order
		wantRedacted   []string // redacted_thinking Data expected
		wantTotalCount int
	}{
		{
			// The regression: empty-but-non-nil content blocks must not shadow
			// the encrypted payload sitting in ResponsesReasoning.
			name: "empty content blocks does not shadow encrypted content",
			msg: &schemas.ResponsesMessage{
				Type:    schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
				Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{}},
				ResponsesReasoning: &schemas.ResponsesReasoning{
					Summary:          []schemas.ResponsesReasoningSummary{},
					EncryptedContent: schemas.Ptr(encrypted),
				},
			},
			wantRedacted:   []string{encrypted},
			wantTotalCount: 1,
		},
		{
			// Content blocks present but none of them reasoning: the summary is
			// the only reasoning available and must not be lost.
			name: "content blocks without reasoning falls back to summary",
			msg: &schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
				Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{
					{Type: schemas.ResponsesOutputMessageContentTypeText, Text: schemas.Ptr("not reasoning")},
				}},
				ResponsesReasoning: &schemas.ResponsesReasoning{
					Summary: []schemas.ResponsesReasoningSummary{{Text: "visible reasoning"}},
				},
			},
			wantThinking:   []string{"visible reasoning"},
			wantTotalCount: 1,
		},
		{
			// Already correct today: the inner independent-ifs keep both halves.
			name: "summary and encrypted content both survive",
			msg: &schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
				ResponsesReasoning: &schemas.ResponsesReasoning{
					Summary:          []schemas.ResponsesReasoningSummary{{Text: "visible reasoning"}},
					EncryptedContent: schemas.Ptr(encrypted),
				},
			},
			wantThinking:   []string{"visible reasoning"},
			wantRedacted:   []string{encrypted},
			wantTotalCount: 2,
		},
		{
			// Already correct today: real content blocks take precedence and the
			// fallback must NOT also fire, or the reasoning would be duplicated.
			name: "content blocks with reasoning do not also emit the fallback",
			msg: &schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
				Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{
					{Type: schemas.ResponsesOutputMessageContentTypeReasoning, Text: schemas.Ptr("step by step")},
				}},
				ResponsesReasoning: &schemas.ResponsesReasoning{
					Summary:          []schemas.ResponsesReasoningSummary{{Text: "should not appear"}},
					EncryptedContent: schemas.Ptr(encrypted),
				},
			},
			wantThinking:   []string{"step by step"},
			wantTotalCount: 1,
		},
		{
			name:           "no reasoning at all",
			msg:            &schemas.ResponsesMessage{Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning)},
			wantTotalCount: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			blocks := convertBifrostReasoningToAnthropicThinking(ctx, tc.msg, schemas.OpenAI, "gpt-5.5")
			require.Len(t, blocks, tc.wantTotalCount)

			var thinking, redacted []string
			for i, block := range blocks {
				switch block.Type {
				case AnthropicContentBlockTypeThinking:
					require.NotNil(t, block.Thinking, "block %d: thinking block with nil text", i)
					// The Agent SDK requires the signature field to be present on
					// every thinking block, even when there is nothing to embed.
					require.NotNil(t, block.Signature, "block %d: thinking block with nil signature", i)
					thinking = append(thinking, *block.Thinking)
				case AnthropicContentBlockTypeRedactedThinking:
					require.NotNil(t, block.Data, "block %d: redacted block with nil data", i)
					redacted = append(redacted, *block.Data)
				}
			}
			require.Equal(t, tc.wantThinking, nilIfEmpty(thinking), "thinking blocks")
			require.Equal(t, tc.wantRedacted, nilIfEmpty(redacted), "redacted_thinking blocks")
		})
	}
}

func nilIfEmpty(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}
