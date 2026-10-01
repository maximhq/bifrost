package cohere

import (
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// TestToCohereChatRequestToolChoice pins how OpenAI tool_choice values map onto
// Cohere, which only accepts REQUIRED and NONE and lets the model decide when
// tool_choice is omitted. "auto" must therefore be omitted, not forced.
func TestToCohereChatRequestToolChoice(t *testing.T) {
	required := ToolChoiceRequired
	none := ToolChoiceNone

	tests := []struct {
		name       string
		toolChoice *schemas.ChatToolChoice
		want       *CohereToolChoice
	}{
		{
			name:       "auto string is omitted",
			toolChoice: &schemas.ChatToolChoice{ChatToolChoiceStr: schemas.Ptr("auto")},
			want:       nil,
		},
		{
			name:       "none string",
			toolChoice: &schemas.ChatToolChoice{ChatToolChoiceStr: schemas.Ptr("none")},
			want:       &none,
		},
		{
			name:       "required string",
			toolChoice: &schemas.ChatToolChoice{ChatToolChoiceStr: schemas.Ptr("required")},
			want:       &required,
		},
		{
			name:       "any string",
			toolChoice: &schemas.ChatToolChoice{ChatToolChoiceStr: schemas.Ptr("any")},
			want:       &required,
		},
		{
			name: "named function",
			toolChoice: &schemas.ChatToolChoice{ChatToolChoiceStruct: &schemas.ChatToolChoiceStruct{
				Type:     schemas.ChatToolChoiceTypeFunction,
				Function: &schemas.ChatToolChoiceFunction{Name: "get_weather"},
			}},
			want: &required,
		},
		{
			name: "allowed tools in auto mode is omitted",
			toolChoice: &schemas.ChatToolChoice{ChatToolChoiceStruct: &schemas.ChatToolChoiceStruct{
				Type:         schemas.ChatToolChoiceTypeAllowedTools,
				AllowedTools: &schemas.ChatToolChoiceAllowedTools{Mode: "auto"},
			}},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := ToCohereChatCompletionRequest(&schemas.BifrostChatRequest{
				Provider: schemas.Cohere,
				Model:    "command-a-03-2025",
				Input: []schemas.ChatMessage{{
					Role:    schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("what is the weather in Paris?")},
				}},
				Params: &schemas.ChatParameters{ToolChoice: tt.toolChoice},
			})
			require.NoError(t, err)
			require.Equal(t, tt.want, out.ToolChoice)
		})
	}
}
