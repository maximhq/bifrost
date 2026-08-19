package bedrock

import (
	"encoding/json"
	"testing"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPayloadOrdering_BedrockConverseRequest(t *testing.T) {
	req := &BedrockConverseRequest{
		Messages: []BedrockMessage{
			{
				Role: "user",
				Content: []BedrockContentBlock{
					{Text: schemas.Ptr("hello")},
				},
			},
		},
		InferenceConfig: &BedrockInferenceConfig{
			Temperature: schemas.Ptr(0.7),
			MaxTokens:   schemas.Ptr(1024),
		},
		ToolConfig: &BedrockToolConfig{
			Tools: []BedrockTool{
				{
					ToolSpec: &BedrockToolSpec{
						Name:        "get_weather",
						Description: schemas.Ptr("Get weather"),
						InputSchema: BedrockToolInputSchema{
							JSON: json.RawMessage(`{"type":"object","properties":{"location":{"type":"string"}},"required":["location"]}`),
						},
					},
				},
			},
		},
	}

	result, err := providerUtils.MarshalSorted(req)
	require.NoError(t, err)

	golden := `{"messages":[{"role":"user","content":[{"text":"hello"}]}],"inferenceConfig":{"maxTokens":1024,"temperature":0.7},"toolConfig":{"tools":[{"toolSpec":{"name":"get_weather","description":"Get weather","inputSchema":{"json":{"type":"object","properties":{"location":{"type":"string"}},"required":["location"]}}}}]}}`

	assert.Equal(t, golden, string(result), "payload field ordering changed — if intentional, update the golden string")

	// Determinism: 100 iterations must produce identical bytes
	for i := 0; i < 100; i++ {
		iter, err := providerUtils.MarshalSorted(req)
		require.NoError(t, err)
		assert.Equal(t, string(result), string(iter), "non-deterministic marshal output on iteration %d", i)
	}
}
