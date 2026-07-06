package anthropic

import (
	"testing"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPayloadOrdering_AnthropicMessageRequest(t *testing.T) {
	req := &AnthropicMessageRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 1024,
		Messages: []AnthropicMessage{
			{
				Role:    "user",
				Content: AnthropicContent{ContentStr: schemas.Ptr("hello")},
			},
		},
		Temperature: schemas.Ptr(0.7),
		Stream:      schemas.Ptr(true),
		Tools: []AnthropicTool{
			{
				Name:        "get_weather",
				Description: schemas.Ptr("Get weather"),
				InputSchema: &schemas.ToolFunctionParameters{
					Type: "object",
					Properties: schemas.NewOrderedMapFromPairs(
						schemas.KV("location", map[string]interface{}{"type": "string"}),
					),
					Required: []string{"location"},
				},
			},
		},
	}

	result, err := providerUtils.MarshalSorted(req)
	require.NoError(t, err)

	golden := `{"model":"claude-sonnet-4-20250514","max_tokens":1024,"messages":[{"role":"user","content":"hello"}],"temperature":0.7,"stream":true,"tools":[{"name":"get_weather","description":"Get weather","input_schema":{"type":"object","properties":{"location":{"type":"string"}},"required":["location"]}}]}`

	assert.Equal(t, golden, string(result), "payload field ordering changed — if intentional, update the golden string")

	// Determinism: 100 iterations must produce identical bytes
	for i := 0; i < 100; i++ {
		iter, err := providerUtils.MarshalSorted(req)
		require.NoError(t, err)
		assert.Equal(t, string(result), string(iter), "non-deterministic marshal output on iteration %d", i)
	}
}
