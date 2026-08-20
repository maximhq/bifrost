package gemini

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resultNeedsGeminiNormalization decides whether the client's raw schema bytes can be
// forwarded verbatim. It flagged ANY array-valued "type", but normalizeSchemaForGemini
// only rewrites when the array holds more than one non-null type (building anyOf). The
// common nullable shape ["string","null"] is reproduced identically, so routing it through
// decode/re-encode buys nothing -- and costs the exact numeric literals elsewhere in the
// same schema, which is what this PR set out to preserve.
func TestNullableTypeKeepsRawSchemaFastPath(t *testing.T) {
	// Defined once so the assertion can compare the forwarded bytes against exactly what
	// the client sent, rather than against a re-typed copy.
	const rawSchema = `{
				"type": "object",
				"properties": {
					"note": {"type": ["string", "null"]},
					"threshold": {"type": "number", "default": 1.50}
				}
			}`

	const body = `{
		"type": "json_schema",
		"json_schema": {
			"name": "reading",
			"schema": ` + rawSchema + `
		}
	}`

	t.Run("nullable alone takes the fast path", func(t *testing.T) {
		raw := []byte(`{"type":"object","properties":{"note":{"type":["string","null"]}}}`)
		assert.False(t, schemaNeedsGeminiNormalization(raw),
			`["string","null"] is reproduced identically by the normalizer, so it must not force a rewrite`)
	})

	t.Run("multiple non-null types still normalize", func(t *testing.T) {
		raw := []byte(`{"type":"object","properties":{"x":{"type":["string","integer"]}}}`)
		assert.True(t, schemaNeedsGeminiNormalization(raw),
			"two non-null types become anyOf, which is a real rewrite")
	})

	t.Run("all-null array still normalizes", func(t *testing.T) {
		raw := []byte(`{"type":"object","properties":{"x":{"type":["null","null"]}}}`)
		assert.True(t, schemaNeedsGeminiNormalization(raw),
			`["null","null"] is collapsed to "null", which is a real rewrite`)
	})

	t.Run("numeric literal survives beside a nullable field", func(t *testing.T) {
		var params schemas.ChatParameters
		require.NoError(t, schemas.Unmarshal([]byte(`{"response_format":`+body+`}`), &params))

		got := extractSchemaMapFromResponseFormat(params.ResponseFormat)
		require.NotNil(t, got)

		// Require the fast path's own return type. Accepting anything that implements
		// MarshalJSON would also accept a normalized *OrderedMap, so the test would pass
		// on a re-encoded schema and prove nothing about which branch ran.
		rawBytes, ok := got.(json.RawMessage)
		require.True(t, ok, "the raw-bytes fast path must be taken, got %T", got)

		// Byte identity with what the client sent, not merely a substring match: this is
		// the whole contract the fast path exists to keep.
		var wantSchema, gotSchema bytes.Buffer
		require.NoError(t, json.Compact(&wantSchema, []byte(rawSchema)))
		require.NoError(t, json.Compact(&gotSchema, rawBytes))
		assert.Equal(t, wantSchema.String(), gotSchema.String(),
			"the client's schema bytes must be forwarded unchanged")

		assert.Contains(t, string(rawBytes), "1.50",
			"the exact numeric literal 1.50 must survive; got %s", string(rawBytes))
	})
}
