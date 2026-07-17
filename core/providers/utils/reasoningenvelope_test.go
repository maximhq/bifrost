package utils

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// The envelope carries an OpenAI encrypted reasoning (item_id, payload) pair through
// id-less content blocks (e.g. Anthropic redacted_thinking). Unwrap must be strict: any
// data that is not a well-formed, known-provider envelope is raw provider payload and has
// to be passed through byte-identically, so every rejection case below matters.

// encodeEnvelopeFields hand-builds an envelope-shaped JSON object so negative cases can
// vary individual fields.
func encodeEnvelopeFields(t *testing.T, fields map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal envelope fields: %v", err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func TestReasoningEnvelopeCodec_RoundTrip(t *testing.T) {
	t.Parallel()

	wrapped := WrapEncryptedReasoning("openai", "rs_orig_123", "CIPHERTEXT_X")
	provider, itemID, payload, ok := UnwrapEncryptedReasoning(wrapped)
	if !ok {
		t.Fatal("unwrap of a freshly wrapped envelope must succeed")
	}
	if provider != "openai" || itemID != "rs_orig_123" || payload != "CIPHERTEXT_X" {
		t.Errorf("round trip = (%q, %q, %q), want (openai, rs_orig_123, CIPHERTEXT_X)", provider, itemID, payload)
	}
}

func TestReasoningEnvelopeCodec_WrapIsIdempotent(t *testing.T) {
	t.Parallel()

	once := WrapEncryptedReasoning("openai", "rs_orig_123", "CIPHERTEXT_X")
	twice := WrapEncryptedReasoning("openai", "rs_other_456", once)
	if twice != once {
		t.Errorf("wrapping an existing envelope must return it unchanged, got a new envelope")
	}
}

func TestReasoningEnvelopeCodec_WrapRefusesOversizedEnvelope(t *testing.T) {
	t.Parallel()

	// A payload this large base64-expands past the encoded-envelope cap, so Wrap must
	// return it unwrapped instead of producing an envelope Unwrap would then reject.
	payload := strings.Repeat("A", 8*1024*1024)
	if got := WrapEncryptedReasoning("openai", "rs_orig_123", payload); got != payload {
		t.Error("oversized encoded envelope must degrade to the raw payload")
	}
}

func TestReasoningEnvelopeCodec_UnwrapRejects(t *testing.T) {
	t.Parallel()

	valid := map[string]any{
		"_bifrost": "openai.responses.reasoning",
		"v":        1,
		"provider": "openai",
		"item_id":  "rs_orig_123",
		"payload":  "CIPHERTEXT_X",
	}
	withField := func(key string, value any) map[string]any {
		out := make(map[string]any, len(valid))
		for k, v := range valid {
			out[k] = v
		}
		out[key] = value
		return out
	}

	tests := map[string]string{
		"wrong magic":            encodeEnvelopeFields(t, withField("_bifrost", "someone.else.entirely")),
		"wrong version":          encodeEnvelopeFields(t, withField("v", 2)),
		"empty provider":         encodeEnvelopeFields(t, withField("provider", "")),
		"unknown provider":       encodeEnvelopeFields(t, withField("provider", "gemini")),
		"empty item_id":          encodeEnvelopeFields(t, withField("item_id", "")),
		"empty payload":          encodeEnvelopeFields(t, withField("payload", "")),
		"control char in id":     encodeEnvelopeFields(t, withField("item_id", "rs_\norig")),
		"not base64":             "%%%not-base64%%%",
		"base64 of non-JSON":     base64.StdEncoding.EncodeToString([]byte("just some bytes")),
		"base64 of foreign JSON": base64.StdEncoding.EncodeToString([]byte(`{"foo":1}`)),
		"raw legacy payload":     "EmwKAhgBEgy3vaFTgeKrzXhpwEr_TEST_PAYLOAD",
		"empty data":             "",
		"oversized data":         strings.Repeat("A", 10*1024*1024+4),
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, _, _, ok := UnwrapEncryptedReasoning(data); ok {
				t.Errorf("UnwrapEncryptedReasoning(%s) = ok, want raw/legacy (ok=false)", name)
			}
		})
	}
}
