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
