package vertex

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStripVertexGeminiUnsupportedFieldsRawPreservesOrdering(t *testing.T) {
	raw := []byte(`{"contents":[{"role":"user","parts":[{"functionCall":{"name":"lookup","id":"call-1","args":{"z":1,"a":2}}},{"functionResponse":{"name":"lookup","id":"call-1","response":{"output":{"z":1,"a":2}}}}]}],"generationConfig":{"temperature":0.2}}`)

	got := stripVertexGeminiUnsupportedFieldsRaw(raw)

	assert.Equal(t, `{"contents":[{"role":"user","parts":[{"functionCall":{"name":"lookup","args":{"z":1,"a":2}}},{"functionResponse":{"name":"lookup","response":{"output":{"z":1,"a":2}}}}]}],"generationConfig":{"temperature":0.2}}`, string(got))
}

func TestStripVertexCountTokensUnsupportedFields(t *testing.T) {
	t.Run("keeps fields that contribute to the count", func(t *testing.T) {
		raw := []byte(`{"model":"gemini-3.6-flash","contents":[{"role":"user","parts":[{"text":"hi"}]}],"systemInstruction":{"parts":[{"text":"be terse"}]},"tools":[{"functionDeclarations":[{"name":"probe"}]}],"generationConfig":{"temperature":0.2}}`)

		got := stripVertexCountTokensUnsupportedFields(raw)

		assert.Equal(t, string(raw), string(got))
	})

	t.Run("drops fields countTokens rejects", func(t *testing.T) {
		raw := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"systemInstruction":{"parts":[{"text":"be terse"}]},"toolConfig":{"functionCallingConfig":{"mode":"AUTO"}},"safetySettings":[{"category":"HARM_CATEGORY_HARASSMENT"}],"cachedContent":"cachedContents/abc","serviceTier":"SERVICE_TIER_STANDARD","labels":{"a":"b"}}`)

		got := stripVertexCountTokensUnsupportedFields(raw)

		assert.JSONEq(t, `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"systemInstruction":{"parts":[{"text":"be terse"}]}}`, string(got))
	})

	t.Run("handles empty body", func(t *testing.T) {
		assert.Empty(t, stripVertexCountTokensUnsupportedFields(nil))
	})
}
