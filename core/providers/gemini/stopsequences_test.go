package gemini

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestToGeminiResponsesRequest_StopSequencesFromAnthropicIngress is a regression test:
// the Anthropic (and Bedrock) ingress store stop sequences under
// ExtraParams["stop"], but the Gemini Responses converter only read
// "stop_sequences", so stop_sequences sent to /anthropic/v1/messages never
// reached generationConfig.stopSequences on Gemini/Vertex.
func TestToGeminiResponsesRequest_StopSequencesFromAnthropicIngress(t *testing.T) {
	for _, provider := range []schemas.ModelProvider{schemas.Gemini, schemas.Vertex} {
		t.Run(string(provider), func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			in := &anthropic.AnthropicMessageRequest{
				Model:         "gemini-2.5-pro",
				MaxTokens:     1024,
				Messages:      []anthropic.AnthropicMessage{anthropicTextMessage(anthropic.AnthropicMessageRoleUser, "Count to ten.")},
				StopSequences: []string{"5", "END"},
			}

			bifrostReq := in.ToBifrostResponsesRequest(ctx)
			require.NotNil(t, bifrostReq)
			bifrostReq.Provider = provider

			out, err := ToGeminiResponsesRequest(ctx, bifrostReq)
			require.NoError(t, err)
			require.NotNil(t, out)

			assert.Equal(t, []string{"5", "END"}, out.GenerationConfig.StopSequences)
			assert.NotContains(t, out.GetExtraParams(), "stop", "consumed key must not be forwarded as an unknown wire field")
		})
	}
}

// TestToGeminiResponsesRequest_StopSequencesExtraParamKeys verifies that stop sequences are
// read from "stop" first, falling back to legacy "stop_sequences" when "stop" is absent or
// not a string list, and that neither key is forwarded as an extra wire parameter.
func TestToGeminiResponsesRequest_StopSequencesExtraParamKeys(t *testing.T) {
	tests := []struct {
		name        string
		extraParams map[string]interface{}
		want        []string
	}{
		{
			name:        "stop as []string",
			extraParams: map[string]interface{}{"stop": []string{"END"}},
			want:        []string{"END"},
		},
		{
			name:        "stop as []interface{} (JSON-decoded)",
			extraParams: map[string]interface{}{"stop": []interface{}{"END", "STOP"}},
			want:        []string{"END", "STOP"},
		},
		{
			name:        "legacy stop_sequences still honored",
			extraParams: map[string]interface{}{"stop_sequences": []string{"END"}},
			want:        []string{"END"},
		},
		{
			name: "stop takes precedence over stop_sequences",
			extraParams: map[string]interface{}{
				"stop":           []string{"A"},
				"stop_sequences": []string{"B"},
			},
			want: []string{"A"},
		},
		{
			name:        "invalid stop falls back to stop_sequences",
			extraParams: map[string]interface{}{"stop": 42, "stop_sequences": []string{"B"}},
			want:        []string{"B"},
		},
		{
			name:        "absent",
			extraParams: map[string]interface{}{"custom_passthrough": "keep-me"},
			want:        nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bifrostReq := &schemas.BifrostResponsesRequest{
				Provider: schemas.Gemini,
				Model:    "gemini-2.5-pro",
				Params:   &schemas.ResponsesParameters{ExtraParams: tt.extraParams},
			}

			out, err := ToGeminiResponsesRequest(nil, bifrostReq)
			require.NoError(t, err)
			require.NotNil(t, out)

			assert.Equal(t, tt.want, out.GenerationConfig.StopSequences)
			wire := out.GetExtraParams()
			assert.NotContains(t, wire, "stop")
			assert.NotContains(t, wire, "stop_sequences")
		})
	}
}
