package cohere

import (
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// TestToCohereResponsesRequestToolChoice pins the Responses-side tool_choice
// mapping. Cohere only accepts REQUIRED and NONE, so "auto" must be omitted
// rather than sent as an unsupported "AUTO" value.
func TestToCohereResponsesRequestToolChoice(t *testing.T) {
	required := ToolChoiceRequired
	none := ToolChoiceNone

	tests := []struct {
		name       string
		toolChoice string
		want       *CohereToolChoice
	}{
		{name: "auto is omitted", toolChoice: "auto", want: nil},
		{name: "none", toolChoice: "none", want: &none},
		{name: "required", toolChoice: "required", want: &required},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := ToCohereResponsesRequest(&schemas.BifrostResponsesRequest{
				Provider: schemas.Cohere,
				Model:    "command-a-03-2025",
				Input: []schemas.ResponsesMessage{{
					Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
					Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("what is the weather in Paris?")},
				}},
				Params: &schemas.ResponsesParameters{
					ToolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStr: schemas.Ptr(tt.toolChoice)},
				},
			})
			require.NoError(t, err)
			require.Equal(t, tt.want, out.ToolChoice)
		})
	}
}
