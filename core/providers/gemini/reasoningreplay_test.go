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
