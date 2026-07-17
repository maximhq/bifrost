// This file contains the Bifrost-private envelope used to carry a provider's encrypted
// reasoning item through content blocks that have no item id field (e.g. Anthropic
// redacted_thinking, which only carries type and data). OpenAI validates encrypted
// reasoning content against the exact item id it was issued with, so the id must survive
// the round trip through the id-less block. The envelope is decoded only at the final
// destination-provider conversion (deferred decoding), keeping provenance intact for
// fallbacks.
package utils

import (
	"encoding/base64"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

const (
	// reasoningEnvelopeMagic identifies the envelope; any other value in the _bifrost
	// field means the data is raw provider payload and must be passed through untouched.
	reasoningEnvelopeMagic = "openai.responses.reasoning"
	// reasoningEnvelopeVersion is the only version this build reads or writes.
	reasoningEnvelopeVersion = 1
	// maxReasoningEnvelopeSize caps the ENCODED envelope string on both sides: Wrap
	// refuses to produce a larger envelope (returning the payload unwrapped, which
	// degrades to the pre-envelope behavior), and Unwrap refuses to decode a larger
	// input (treating it as raw payload). Defining the limit on the encoded string
	// keeps the two sides symmetric.
	maxReasoningEnvelopeSize = 10 * 1024 * 1024
)

// reasoningEnvelope is the JSON shape carried (base64-encoded) inside the block's data field.
type reasoningEnvelope struct {
	Magic    string `json:"_bifrost"`
	Version  int    `json:"v"`
	Provider string `json:"provider"`
	ItemID   string `json:"item_id"`
	Payload  string `json:"payload"`
}

// isKnownReasoningEnvelopeProvider gates the provider field to values this build knows
// how to route; only OpenAI wraps encrypted reasoning today. Unknown values would let a
// crafted envelope smuggle an item id through ingress restoration, so they are rejected.
func isKnownReasoningEnvelopeProvider(provider string) bool {
	return provider == string(schemas.OpenAI)
}

// WrapEncryptedReasoning packs a provider's encrypted reasoning item id and payload into
// a base64-encoded envelope suitable for an id-less content block's data field. Payloads
// that are already an envelope are returned unchanged (wrapping is idempotent), and
// payloads whose encoded envelope would exceed the size cap are returned unwrapped.
func WrapEncryptedReasoning(provider, itemID, payload string) string {
	if _, _, _, ok := UnwrapEncryptedReasoning(payload); ok {
		return payload
	}
	raw, err := sonic.Marshal(reasoningEnvelope{
		Magic:    reasoningEnvelopeMagic,
		Version:  reasoningEnvelopeVersion,
		Provider: provider,
		ItemID:   itemID,
		Payload:  payload,
	})
	if err != nil {
		// Marshaling a struct of plain strings cannot fail; keep the payload usable anyway.
		return payload
	}
	encoded := base64.StdEncoding.EncodeToString(raw)
	if len(encoded) > maxReasoningEnvelopeSize {
		return payload
	}
	return encoded
}

// UnwrapEncryptedReasoning decodes an envelope produced by WrapEncryptedReasoning.
// It is strict: the magic value and version must match exactly, the provider must be a
// known value, item_id and payload must be non-empty, and item_id must not contain
// control characters. Item ids are otherwise opaque strings and are not format-validated.
// Any failure returns ok=false, meaning the data is raw (legacy) provider payload and
// must be treated byte-identically as before.
func UnwrapEncryptedReasoning(data string) (provider, itemID, payload string, ok bool) {
	if data == "" || len(data) > maxReasoningEnvelopeSize {
		return "", "", "", false
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return "", "", "", false
	}
	var env reasoningEnvelope
	if err := sonic.Unmarshal(raw, &env); err != nil {
		return "", "", "", false
	}
	if env.Magic != reasoningEnvelopeMagic || env.Version != reasoningEnvelopeVersion ||
		!isKnownReasoningEnvelopeProvider(env.Provider) || env.ItemID == "" || env.Payload == "" {
		return "", "", "", false
	}
	for _, r := range env.ItemID {
		if r < 0x20 || r == 0x7f {
			return "", "", "", false
		}
	}
	return env.Provider, env.ItemID, env.Payload, true
}
