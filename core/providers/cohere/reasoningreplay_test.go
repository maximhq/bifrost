package cohere

import (
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// Replayed reasoning must survive translation into Cohere thinking blocks.
//
// Two independent bugs made it vanish instead, both found by auditing the same
// defect class as the Bedrock reasoning-replay 400:
//
//  1. `if Summary != nil` is true for an EMPTY but non-nil slice, so the loop ran
//     zero times and the encrypted-content fallback below it was unreachable --
//     dead code. Every construction site of schemas.ResponsesReasoning in this
//     codebase sets `Summary: []schemas.ResponsesReasoningSummary{}`, and the
//     field has no omitempty, so the empty-non-nil state survives a JSON round
//     trip. That branch had never once executed.
//
//  2. The outer `else if` on Content meant a non-nil but empty ContentBlocks --
//     or one holding only non-reasoning blocks -- emitted nothing and never fell
//     through to ResponsesReasoning.
//
// Both drop data silently: no error, no warning, the model simply loses its
// prior reasoning. Cohere has no encrypted-reasoning field, so the replay lands
// in a marked thinking block; losing it outright is strictly worse.
func TestConvertBifrostReasoningToCohereThinking(t *testing.T) {
	const encrypted = "EqQBCgIYAhIM...fixture"

	tests := []struct {
		name      string
		msg       *schemas.ResponsesMessage
		wantCount int
		wantAll   []string // substrings that must appear across the emitted blocks
	}{
		{
			// Bug 1. The shape every provider's ingress produces.
			name: "empty summary plus encrypted content",
			msg: &schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
				ResponsesReasoning: &schemas.ResponsesReasoning{
					Summary:          []schemas.ResponsesReasoningSummary{},
					EncryptedContent: schemas.Ptr(encrypted),
				},
			},
			wantCount: 1,
			wantAll:   []string{encrypted},
		},
		{
			// Bug 2. Non-nil but empty content blocks must not shadow the fallback.
			name: "empty content blocks does not shadow encrypted content",
			msg: &schemas.ResponsesMessage{
				Type:    schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
				Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{}},
				ResponsesReasoning: &schemas.ResponsesReasoning{
					Summary:          []schemas.ResponsesReasoningSummary{},
					EncryptedContent: schemas.Ptr(encrypted),
				},
			},
			wantCount: 1,
			wantAll:   []string{encrypted},
		},
		{
			// Bug 2 again, via content blocks that carry no reasoning.
			name: "content blocks without reasoning falls back to summary",
			msg: &schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
				Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{
					{Type: schemas.ResponsesOutputMessageContentTypeText, Text: schemas.Ptr("not reasoning")},
				}},
				ResponsesReasoning: &schemas.ResponsesReasoning{
					Summary: []schemas.ResponsesReasoningSummary{{Text: "thinking about it"}},
				},
			},
			wantCount: 1,
			wantAll:   []string{"thinking about it"},
		},
		{
			// A summary and an encrypted payload are independent facts; keeping
			// only one of them loses replay data the next turn needs.
			name: "summary and encrypted content both survive",
			msg: &schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
				ResponsesReasoning: &schemas.ResponsesReasoning{
					Summary:          []schemas.ResponsesReasoningSummary{{Text: "visible reasoning"}},
					EncryptedContent: schemas.Ptr(encrypted),
				},
			},
			wantCount: 2,
			wantAll:   []string{"visible reasoning", encrypted},
		},
		{
			name: "content blocks with reasoning",
			msg: &schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
				Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{
					{Type: schemas.ResponsesOutputMessageContentTypeReasoning, Text: schemas.Ptr("step by step")},
				}},
			},
			wantCount: 1,
			wantAll:   []string{"step by step"},
		},
		{
			name: "summary only",
			msg: &schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
				ResponsesReasoning: &schemas.ResponsesReasoning{
					Summary: []schemas.ResponsesReasoningSummary{{Text: "first"}, {Text: "second"}},
				},
			},
			wantCount: 2,
			wantAll:   []string{"first", "second"},
		},
		{
			name: "empty encrypted content emits nothing",
			msg: &schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
				ResponsesReasoning: &schemas.ResponsesReasoning{
					Summary:          []schemas.ResponsesReasoningSummary{},
					EncryptedContent: schemas.Ptr(""),
				},
			},
			wantCount: 0,
		},
		{
			name:      "no reasoning at all",
			msg:       &schemas.ResponsesMessage{Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning)},
			wantCount: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			blocks := convertBifrostReasoningToCohereThinking(tc.msg)
			require.Len(t, blocks, tc.wantCount)

			var combined strings.Builder
			for i, block := range blocks {
				require.Equal(t, CohereContentBlockTypeThinking, block.Type, "block %d", i)
				require.NotNil(t, block.Thinking, "block %d: nil Thinking", i)
				combined.WriteString(*block.Thinking)
				combined.WriteString("\n")
			}
			for _, want := range tc.wantAll {
				require.Contains(t, combined.String(), want,
					"replayed reasoning was dropped: %q missing from the emitted thinking blocks", want)
			}
		})
	}
}

// The encrypted replay token has to survive a FULL round trip, not just egress.
//
// Cohere has no encrypted-reasoning field, so egress smuggles the token out
// inside a marked thinking block. That is only half a round trip: on the way
// back in, every thinking block was mapped to ordinary reasoning text, so the
// marker reached clients as visible prose and was replayed as if the model had
// literally thought "[ENCRYPTED_REASONING: ...]". The token that was supposed
// to be restored to EncryptedContent was, from the provider's point of view,
// gone - and the replay it exists to enable could never happen.
func TestEncryptedReasoningSurvivesCohereRoundTrip(t *testing.T) {
	const token = "ErcBCkYIBRgCKkDd2xLm...opaque"

	out := convertBifrostReasoningToCohereThinking(&schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
		ResponsesReasoning: &schemas.ResponsesReasoning{
			Summary:          []schemas.ResponsesReasoningSummary{},
			EncryptedContent: schemas.Ptr(token),
		},
	})
	require.NotEmpty(t, out, "egress dropped the encrypted reasoning entirely")

	// Feed exactly what egress produced back through ingress, which is what a
	// client replaying the turn actually does.
	back := convertSingleCohereMessageToBifrostMessages(&CohereMessage{
		Role:    "assistant",
		Content: &CohereMessageContent{BlocksContent: out},
	}, true)
	require.NotEmpty(t, back, "ingress produced no messages")

	var reasoning *schemas.ResponsesMessage
	for i := range back {
		if back[i].Type != nil && *back[i].Type == schemas.ResponsesMessageTypeReasoning {
			reasoning = &back[i]
			break
		}
	}
	require.NotNil(t, reasoning, "no reasoning message came back")
	require.NotNil(t, reasoning.ResponsesReasoning, "reasoning message carries no ResponsesReasoning")
	require.NotNil(t, reasoning.ResponsesReasoning.EncryptedContent,
		"the replay token was not restored to EncryptedContent, so the next turn cannot replay it")
	require.Equal(t, token, *reasoning.ResponsesReasoning.EncryptedContent)

	// And it must not ALSO surface as visible reasoning text: the marker is a
	// transport detail, and leaking it to clients means replaying it as content.
	if reasoning.Content != nil {
		for _, b := range reasoning.Content.ContentBlocks {
			if b.Text != nil {
				require.NotContains(t, *b.Text, "ENCRYPTED_REASONING",
					"the transport marker leaked into visible reasoning text")
			}
		}
	}
}

// The combined shape - content-block reasoning AND an encrypted token on the
// same message - is not hypothetical: it is exactly what this file's own ingress
// path now produces, since restoring EncryptedContent leaves the visible
// reasoning blocks in place. Returning early once the content blocks yielded
// reasoning drops the token on the very next turn, which is the turn the token
// exists for. The token-only round-trip test above stayed green throughout,
// because it never exercised both at once.
func TestEncryptedTokenSurvivesAlongsideContentBlockReasoning(t *testing.T) {
	const token = "ErcBCkYIBRgCKkDd2xLm...combined"

	blocks := convertBifrostReasoningToCohereThinking(&schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
		Content: &schemas.ResponsesMessageContent{
			ContentBlocks: []schemas.ResponsesMessageContentBlock{{
				Type: schemas.ResponsesOutputMessageContentTypeReasoning,
				Text: schemas.Ptr("Thinking it through."),
			}},
		},
		ResponsesReasoning: &schemas.ResponsesReasoning{
			Summary:          []schemas.ResponsesReasoningSummary{},
			EncryptedContent: schemas.Ptr(token),
		},
	})

	var sawText, sawMarker bool
	for _, b := range blocks {
		if b.Thinking == nil {
			continue
		}
		if *b.Thinking == "Thinking it through." {
			sawText = true
		}
		if enc, ok := parseEncryptedReasoning(*b.Thinking); ok && enc == token {
			sawMarker = true
		}
	}

	require.True(t, sawText, "visible reasoning text was dropped")
	require.True(t, sawMarker,
		"the encrypted replay token was dropped because the content blocks already yielded reasoning")
}

// The full replay path, not just one half of it.
//
// The combined-shape test above starts from a hand-built message and only proves egress emits
// both parts. What actually happens on turn two of a replay is: a Cohere response is INGESTED
// into a Bifrost message, and that message is then converted back out. Only the round trip shows
// whether the two halves agree - an ingress that restores the token into a field egress skips, or
// an egress that drops what ingress carefully preserved, both look correct in isolation.
func TestMixedReasoningSurvivesIngressThenEgress(t *testing.T) {
	const token = "ErcBCkYIBRgCKkDd2xLm...mixed"
	const visible = "Working through the constraints."

	// What Cohere sends back when the previous turn carried both: ordinary thinking plus the
	// marked block Bifrost wrote on the way out.
	marker := formatEncryptedReasoning(token)
	visibleText := visible
	incoming := []CohereContentBlock{
		{Type: CohereContentBlockTypeThinking, Thinking: &visibleText},
		{Type: CohereContentBlockTypeThinking, Thinking: &marker},
	}

	back := convertSingleCohereMessageToBifrostMessages(&CohereMessage{
		Role:    "assistant",
		Content: &CohereMessageContent{BlocksContent: incoming},
	}, true)

	var reasoning *schemas.ResponsesMessage
	for i := range back {
		if back[i].Type != nil && *back[i].Type == schemas.ResponsesMessageTypeReasoning {
			reasoning = &back[i]
			break
		}
	}
	require.NotNil(t, reasoning, "ingress produced no reasoning message")
	require.NotNil(t, reasoning.ResponsesReasoning, "ingress dropped ResponsesReasoning")
	require.NotNil(t, reasoning.ResponsesReasoning.EncryptedContent, "ingress did not restore the replay token")
	require.Equal(t, token, *reasoning.ResponsesReasoning.EncryptedContent)

	// Now send that exact message back out, which is what the next turn does.
	out := convertBifrostReasoningToCohereThinking(reasoning)

	var sawVisible, sawMarker bool
	for _, b := range out {
		if b.Thinking == nil {
			continue
		}
		if *b.Thinking == visible {
			sawVisible = true
		}
		if enc, ok := parseEncryptedReasoning(*b.Thinking); ok && enc == token {
			sawMarker = true
		}
	}
	require.True(t, sawVisible, "the visible reasoning text was lost across the round trip")
	require.True(t, sawMarker,
		"the replay token was lost across the round trip - ingress restored it but egress did not send it back")
}
