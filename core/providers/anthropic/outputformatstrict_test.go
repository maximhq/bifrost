package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// Anthropic structured outputs accept optional properties, but the Responses API
// defaults text.format.strict to true, which requires every property to be
// listed in "required". The converted format must therefore send strict false,
// or the same request routed to OpenAI fails with a 400 (#8159).
func TestAnthropicOutputFormatIsNotStrictOnOpenAI(t *testing.T) {
	schema := `{
		"type": "json_schema",
		"schema": {
			"type": "object",
			"properties": {
				"answer": {"type": "string"},
				"confident": {"type": "boolean"},
				"note": {"type": "string"}
			},
			"required": ["answer", "confident"],
			"additionalProperties": false
		}
	}`

	for _, tc := range []struct {
		name string
		body string
	}{
		{"output_config.format", `{"model":"openai/gpt-6-luna","max_tokens":1024,"output_config":{"format":` + schema + `},` +
			`"messages":[{"role":"user","content":"What is the capital of France?"}]}`},
		{"output_format", `{"model":"openai/gpt-6-luna","max_tokens":1024,"output_format":` + schema + `,` +
			`"messages":[{"role":"user","content":"What is the capital of France?"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var req AnthropicMessageRequest
			require.NoError(t, json.Unmarshal([]byte(tc.body), &req))

			ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
			bifrostReq := req.ToBifrostResponsesRequest(ctx)
			require.NotNil(t, bifrostReq)
			require.Equal(t, schemas.OpenAI, bifrostReq.Provider)

			wire, err := json.Marshal(openai.ToOpenAIResponsesRequest(ctx, bifrostReq))
			require.NoError(t, err)

			var sent struct {
				Text struct {
					Format struct {
						Strict *bool `json:"strict"`
						Schema struct {
							Required []string `json:"required"`
						} `json:"schema"`
					} `json:"format"`
				} `json:"text"`
			}
			require.NoError(t, json.Unmarshal(wire, &sent))
			require.NotNil(t, sent.Text.Format.Strict, "text.format.strict must be sent, it defaults to true upstream: %s", wire)
			require.False(t, *sent.Text.Format.Strict)
			require.Equal(t, []string{"answer", "confident"}, sent.Text.Format.Schema.Required)
		})
	}
}
