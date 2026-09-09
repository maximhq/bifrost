package gemini

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every field on Part is omitempty, so a Part carrying no payload marshals to
// exactly `{}`. The provider harness observed one reach a client: asking
// gemini-2.5-pro to transcribe a synthetic tone returned
// {"candidates":[{"content":{"parts":[{}],"role":"model"}}]}.
//
// The producer of that particular part was not identified -- the two candidate
// sources in this package (thoughtSignatureFromEncryptedContent and
// convertContentBlockToGeminiPart) both already refuse to emit one, so the empty
// part may well have come from Google and been relayed. Either way a payload-free
// part is never a valid answer: it is noise a client will try to read, and it masks
// the contentless case by making the parts slice look non-empty. Filtering at the
// point the candidate is assembled covers both origins.
func TestDropEmptyGeminiParts(t *testing.T) {
	t.Run("a zero part marshals to the empty object it is filtered for", func(t *testing.T) {
		encoded, err := json.Marshal(&Part{})
		require.NoError(t, err)
		assert.Equal(t, "{}", string(encoded),
			"if this ever stops holding, the filter below is testing the wrong thing")
	})

	t.Run("drops payload-free parts", func(t *testing.T) {
		got := dropEmptyGeminiParts([]*Part{
			{Text: "keep me"},
			{},
			nil,
			{Thought: true},
			{ThoughtSignature: []byte{0x01}},
		})

		require.Len(t, got, 3)
		assert.Equal(t, "keep me", got[0].Text)
		assert.True(t, got[1].Thought, "a thought marker is a payload")
		assert.Equal(t, []byte{0x01}, got[2].ThoughtSignature)
	})

	t.Run("leaves a fully populated slice untouched", func(t *testing.T) {
		parts := []*Part{{Text: "a"}, {Text: "b"}}
		assert.Equal(t, parts, dropEmptyGeminiParts(parts))
	})

	t.Run("nil and empty input stay empty", func(t *testing.T) {
		assert.Empty(t, dropEmptyGeminiParts(nil))
		assert.Empty(t, dropEmptyGeminiParts([]*Part{}))
		assert.Empty(t, dropEmptyGeminiParts([]*Part{{}, nil, {}}))
	})

	// A zero-length signature is not the same value as a nil one, so comparing
	// against the zero Part keeps such a part -- but Part.MarshalJSON writes
	// ThoughtSignature through a `string` alias with omitempty, so an empty slice
	// base64-encodes to "" and the key disappears. The part therefore reaches the
	// wire as exactly the `{}` this filter exists to remove, having passed straight
	// through it. Emptiness has to be judged on what marshals, not on struct equality.
	t.Run("a zero-length thought signature counts as absent", func(t *testing.T) {
		encoded, err := json.Marshal(&Part{ThoughtSignature: []byte{}})
		require.NoError(t, err)
		require.Equal(t, "{}", string(encoded),
			"if an empty signature stops marshalling to {} this case no longer describes a real hazard")

		assert.Empty(t, dropEmptyGeminiParts([]*Part{{ThoughtSignature: []byte{}}}),
			"a part whose only field is a zero-length signature carries no payload")
	})

	t.Run("a non-empty thought signature is still kept", func(t *testing.T) {
		got := dropEmptyGeminiParts([]*Part{{ThoughtSignature: []byte{0x01}}})
		require.Len(t, got, 1)
		assert.Equal(t, []byte{0x01}, got[0].ThoughtSignature)
	})
}
