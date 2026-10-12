package vertex

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/providers/gemini"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
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

// TestVertexGeminiResponsesNativeToolSubFieldsReachWire mirrors the Vertex Responses body
// build for a Gemini model (ToGeminiResponsesRequestWithImageURLSchemes, the typed and raw
// Vertex strips, then the provider marshal) for a /genai request carrying native tools.
// retrieval.dynamicRetrievalConfig is not a modelled retrieval field: Google, not Bifrost,
// must judge it, so it has to reach the Vertex body instead of collapsing to {"retrieval":{}}.
func TestVertexGeminiResponsesNativeToolSubFieldsReachWire(t *testing.T) {
	retrieval := `{"dynamicRetrievalConfig":{"mode":"MODE_DYNAMIC","dynamicThreshold":0.7}}`
	searchRetrieval := `{"dynamicRetrievalConfig":{"mode":"MODE_DYNAMIC","dynamicThreshold":0.3}}`
	body := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"tools":[{"retrieval":` + retrieval + `},{"googleSearchRetrieval":` + searchRetrieval + `}]}`

	var in gemini.GeminiGenerationRequest
	require.NoError(t, json.Unmarshal([]byte(body), &in))
	in.Model = "vertex/gemini-2.5-flash"
	bifrostReq := in.ToBifrostResponsesRequest(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline))
	require.NotNil(t, bifrostReq)
	bifrostReq.Provider = schemas.Vertex

	reqBody, err := gemini.ToGeminiResponsesRequestWithImageURLSchemes(nil, bifrostReq, "http", "https", "gs")
	require.NoError(t, err)
	stripVertexGeminiUnsupportedFields(reqBody)
	wire, err := providerUtils.MarshalProviderRequest(reqBody)
	require.NoError(t, err)
	wire = stripVertexGeminiUnsupportedFieldsRaw(wire)

	var gotRetrieval, gotSearchRetrieval string
	for _, tool := range gjson.GetBytes(wire, "tools").Array() {
		if v := tool.Get("retrieval"); v.Exists() {
			gotRetrieval = v.Raw
		}
		if v := tool.Get("googleSearchRetrieval"); v.Exists() {
			gotSearchRetrieval = v.Raw
		}
	}
	assert.Equal(t, retrieval, gotRetrieval, "retrieval tool must reach Vertex unchanged; body %s", wire)
	assert.Equal(t, searchRetrieval, gotSearchRetrieval, "googleSearchRetrieval must reach Vertex unchanged; body %s", wire)
}
