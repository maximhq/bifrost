package huggingface

import (
	"testing"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPayloadOrdering_HuggingFaceChatRequest(t *testing.T) {
	req := &HuggingFaceChatRequest{
		Model: "meta-llama/Llama-3-70B-Instruct",
		Messages: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}},
		},
		Temperature: schemas.Ptr(0.7),
		Stream:      schemas.Ptr(true),
		Tools: []schemas.ChatTool{
			{
				Type: "function",
				Function: &schemas.ChatToolFunction{
					Name:        "get_weather",
					Description: schemas.Ptr("Get weather"),
					Parameters: &schemas.ToolFunctionParameters{
						Type: "object",
						Properties: schemas.NewOrderedMapFromPairs(
							schemas.KV("location", map[string]interface{}{"type": "string"}),
						),
						Required: []string{"location"},
					},
				},
			},
		},
	}

	result, err := providerUtils.MarshalSorted(req)
	require.NoError(t, err)

	golden := `{"messages":[{"role":"user","content":"hello"}],"model":"meta-llama/Llama-3-70B-Instruct","stream":true,"temperature":0.7,"tools":[{"type":"function","function":{"name":"get_weather","description":"Get weather","parameters":{"type":"object","properties":{"location":{"type":"string"}},"required":["location"]}}}]}`

	assert.Equal(t, golden, string(result), "payload field ordering changed — if intentional, update the golden string")

	// Determinism: 100 iterations must produce identical bytes
	for i := 0; i < 100; i++ {
		iter, err := providerUtils.MarshalSorted(req)
		require.NoError(t, err)
		assert.Equal(t, string(result), string(iter), "non-deterministic marshal output on iteration %d", i)
	}
}
