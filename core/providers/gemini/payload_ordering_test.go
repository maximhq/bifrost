package gemini

import (
	"testing"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPayloadOrdering_GeminiGenerationRequest(t *testing.T) {
	req := &GeminiGenerationRequest{
		Model: "gemini-2.0-flash",
		Contents: []Content{
			{
				Parts: []*Part{{Text: "hello"}},
				Role:  "user",
			},
		},
		GenerationConfig: GenerationConfig{
			Temperature: schemas.Ptr(float64(0.7)),
		},
		Tools: []Tool{
			{
				FunctionDeclarations: []*FunctionDeclaration{
					{
						Name:        "get_weather",
						Description: "Get weather",
						Parameters: &Schema{
							Type: "OBJECT",
							Properties: map[string]*Schema{
								"location": {Type: "STRING"},
							},
							Required: []string{"location"},
						},
					},
				},
			},
		},
	}

	result, err := providerUtils.MarshalSorted(req)
	require.NoError(t, err)

	golden := `{"model":"gemini-2.0-flash","contents":[{"parts":[{"text":"hello"}],"role":"user"}],"generationConfig":{"temperature":0.7},"tools":[{"functionDeclarations":[{"description":"Get weather","name":"get_weather","parameters":{"properties":{"location":{"type":"STRING"}},"required":["location"],"type":"OBJECT"}}]}]}`

	assert.Equal(t, golden, string(result), "payload field ordering changed — if intentional, update the golden string")

	// Determinism: 100 iterations must produce identical bytes
	for i := 0; i < 100; i++ {
		iter, err := providerUtils.MarshalSorted(req)
		require.NoError(t, err)
		assert.Equal(t, string(result), string(iter), "non-deterministic marshal output on iteration %d", i)
	}
}
