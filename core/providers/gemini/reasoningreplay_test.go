package gemini

import (
	"encoding/base64"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Gemini's streaming and non-streaming egress disagreed about whether
// encrypted_content is already base64.
//
// encrypted_content is a base64 STRING; Part.ThoughtSignature is a []byte that
// Part.MarshalJSON base64-encodes on the way out. The non-streaming converters
// decoded first, so the wire carried base64(signature). The streaming converter
// assigned []byte(encryptedContent) straight through, so the wire carried
// base64(base64(signature)) -- a value Gemini cannot verify, for the same
// conversation, depending only on whether the client streamed.
//
// Both paths now go through thoughtSignatureFromEncryptedContent, so they cannot
// drift apart again.
func TestThoughtSignatureFromEncryptedContent(t *testing.T) {
	raw := []byte{0x01, 0x02, 0xff, 0xfe, 0x7f}
	encoded := base64.StdEncoding.EncodeToString(raw)

	t.Run("decodes to the original bytes", func(t *testing.T) {
		require.Equal(t, raw, thoughtSignatureFromEncryptedContent(&encoded))
	})

	t.Run("serialises to single-encoded base64", func(t *testing.T) {
		part := &Part{ThoughtSignature: thoughtSignatureFromEncryptedContent(&encoded)}
		data, err := part.MarshalJSON()
		require.NoError(t, err)

		onWire := gjson.GetBytes(data, "thoughtSignature").String()
		require.Equal(t, encoded, onWire,
			"thoughtSignature must round-trip to the value the client sent, not a re-encoding of it")

		// The specific corruption this guards against: passing the base64 string
		// through as bytes yields base64(base64(sig)), which is strictly longer
		// and decodes to the base64 TEXT rather than the signature.
		doubled := base64.StdEncoding.EncodeToString([]byte(encoded))
		require.NotEqual(t, doubled, onWire, "signature was double-encoded")
	})

	t.Run("rejects unusable values rather than corrupting them", func(t *testing.T) {
		require.Nil(t, thoughtSignatureFromEncryptedContent(nil))
		require.Nil(t, thoughtSignatureFromEncryptedContent(ptr("")))
		// Not valid base64: dropping it beats shipping a signature Gemini will
		// reject, and beats a partial decode.
		require.Nil(t, thoughtSignatureFromEncryptedContent(ptr("not!valid!base64!")))
	})
}

func ptr(s string) *string { return &s }

// Gemini 3 carries thoughtSignature on the thought part itself, and requires it
// back on replay -- there is a dedicated finish reason for its absence
// (FinishReasonMissingThoughtSignature). The ingress converter read only
// part.Text, so the signature never reached the client and could never be
// replayed; a signature-only thought part produced no message at all.
func TestThoughtPartIngressPreservesSignature(t *testing.T) {
	raw := []byte{0x0a, 0x0b, 0x0c, 0xff}
	encoded := base64.StdEncoding.EncodeToString(raw)

	t.Run("signature survives alongside text", func(t *testing.T) {
		msgs := responsesMessagesForThoughtPart(t, &Part{
			Thought: true, Text: "step by step", ThoughtSignature: raw,
		})
		require.Len(t, msgs, 1)
		require.NotNil(t, msgs[0].ResponsesReasoning, "signature dropped: no reasoning payload")
		require.NotNil(t, msgs[0].ResponsesReasoning.EncryptedContent)
		require.Equal(t, encoded, *msgs[0].ResponsesReasoning.EncryptedContent)
		// Round trip: what egress decodes must equal what Gemini sent.
		require.Equal(t, raw, thoughtSignatureFromEncryptedContent(msgs[0].ResponsesReasoning.EncryptedContent))
	})

	t.Run("signature-only thought part is not dropped", func(t *testing.T) {
		msgs := responsesMessagesForThoughtPart(t, &Part{Thought: true, ThoughtSignature: raw})
		require.Len(t, msgs, 1, "a signature-only thought part must still produce a message")
		require.NotNil(t, msgs[0].ResponsesReasoning)
		require.Equal(t, encoded, *msgs[0].ResponsesReasoning.EncryptedContent)
	})

	t.Run("thought part with neither text nor signature emits nothing", func(t *testing.T) {
		require.Empty(t, responsesMessagesForThoughtPart(t, &Part{Thought: true}))
	})
}

// responsesMessagesForThoughtPart runs the real ingress converter over a single
// candidate holding one part, and returns the reasoning messages it produced.
func responsesMessagesForThoughtPart(t *testing.T, part *Part) []schemas.ResponsesMessage {
	t.Helper()
	out := convertGeminiCandidatesToResponsesOutput([]*Candidate{
		{Content: &Content{Parts: []*Part{part}}},
	})
	var reasoning []schemas.ResponsesMessage
	for _, msg := range out {
		if msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeReasoning {
			reasoning = append(reasoning, msg)
		}
	}
	return reasoning
}

// Gemini requires thought blocks back verbatim on replay: "You MUST always
// resend all thought blocks exactly as they were received from the model" and
// "You should NOT remove or modify thought blocks from the history"
// (https://ai.google.dev/gemini-api/docs/thinking). Keeping a block's signature
// while discarding its text IS modifying it.
//
// Both converters did exactly that, in two different ways.

// Egress: a reasoning item that follows a function call is scanned for its
// signature and then marked consumed, so the main loop skips it before its
// summary text can be emitted as a thought part. A reasoning item carrying BOTH
// text and a signature therefore reached Gemini as signature-only - and only
// when it happened to sit after a function call, so the same item survived
// intact in any other position.
func TestEgressKeepsThoughtTextAlongsideSignature(t *testing.T) {
	raw := []byte{0x11, 0x22, 0x33}
	encoded := base64.StdEncoding.EncodeToString(raw)
	const summary = "I should call get_time for Tokyo."

	resp := &schemas.BifrostResponsesResponse{
		Output: []schemas.ResponsesMessage{
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					Name:      schemas.Ptr("get_time"),
					CallID:    schemas.Ptr("call_1"),
					Arguments: schemas.Ptr(`{"timezone":"Asia/Tokyo"}`),
				},
			},
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
				ResponsesReasoning: &schemas.ResponsesReasoning{
					Summary:          []schemas.ResponsesReasoningSummary{{Text: summary}},
					EncryptedContent: &encoded,
				},
			},
		},
	}

	out := ToGeminiResponsesResponse(resp)
	require.NotNil(t, out)

	// Counted, not just observed. A boolean would be satisfied by a converter
	// that emits the text twice or the signature twice, and duplication is the
	// specific hazard here: the reasoning branch further down emits its own
	// signature-only part, so carrying the signature in both places would put
	// the same value on the wire twice - which Gemini would see as a modified
	// history just as surely as a missing one.
	var signatures, thoughtTexts int
	for _, cand := range out.Candidates {
		if cand == nil || cand.Content == nil {
			continue
		}
		for _, p := range cand.Content.Parts {
			if p == nil {
				continue
			}
			if len(p.ThoughtSignature) > 0 {
				signatures++
				require.Equal(t, raw, p.ThoughtSignature)
			}
			if p.Thought && p.Text == summary {
				thoughtTexts++
			}
		}
	}

	require.Equal(t, 1, signatures, "the signature must appear exactly once, on the function call")
	require.Equal(t, 1, thoughtTexts,
		"the thought TEXT must appear exactly once - dropped means the block reached Gemini modified, duplicated means it was rewritten")
}

// Ingress: every standalone reasoning message was skipped outright, so reasoning
// text never reached Gemini on this path at all - independent of any look-ahead,
// and independent of whether a signature was present.
func TestIngressSendsThoughtTextForStandaloneReasoning(t *testing.T) {
	const summary = "Breaking the problem into two steps."
	encoded := base64.StdEncoding.EncodeToString([]byte{0x44, 0x55})

	contents, _, err := convertResponsesMessagesToGeminiContents([]schemas.ResponsesMessage{
		{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Solve it.")},
		},
		{
			Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
			ResponsesReasoning: &schemas.ResponsesReasoning{
				Summary:          []schemas.ResponsesReasoningSummary{{Text: summary}},
				EncryptedContent: &encoded,
			},
		},
	}, "gemini-2.5-flash", schemas.Gemini)
	require.NoError(t, err)

	var thoughtTexts, signatures int
	for _, c := range contents {
		for _, p := range c.Parts {
			if p == nil {
				continue
			}
			if p.Thought && p.Text == summary {
				thoughtTexts++
			}
			if len(p.ThoughtSignature) > 0 {
				signatures++
			}
		}
	}
	require.Equal(t, 1, thoughtTexts,
		"a standalone reasoning message must contribute its thought block exactly once")
	// No function call precedes this item, so nothing else can be carrying its
	// signature - dropping it here loses the value Gemini needs back.
	require.Equal(t, 1, signatures,
		"the standalone reasoning item's signature was lost: no preceding function call consumed it")
}
