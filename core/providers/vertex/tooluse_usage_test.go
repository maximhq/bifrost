package vertex

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestVertexNormalizeBatchUsage_ToolUsePromptTokens verifies that vertexNormalizeBatchUsage
// accounts for tool-use prompt tokens (e.g. from URL context) by adding toolUsePromptTokenCount
// to prompt_tokens.
func TestVertexNormalizeBatchUsage_ToolUsePromptTokens(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                 string
		body                 map[string]any
		wantPromptTokens     int
		wantCompletionTokens int
		wantTotalTokens      int
		wantReasoningTokens  int
	}{
		{
			name: "documented URL-context example with thinking tokens",
			body: map[string]any{
				"usageMetadata": map[string]any{
					"promptTokenCount":        27,
					"toolUsePromptTokenCount": 10309,
					"thoughtsTokenCount":      31,
					"candidatesTokenCount":    45,
					"totalTokenCount":         10412,
				},
			},
			wantPromptTokens:     10336, // 27 + 10309
			wantCompletionTokens: 76,    // 45 + 31
			wantTotalTokens:      10412,
			wantReasoningTokens:  31,
		},
		{
			name: "zero tool-use tokens leaves prompt tokens unchanged",
			body: map[string]any{
				"usageMetadata": map[string]any{
					"promptTokenCount":        100,
					"toolUsePromptTokenCount": 0,
					"candidatesTokenCount":    50,
					"totalTokenCount":         150,
				},
			},
			wantPromptTokens:     100,
			wantCompletionTokens: 50,
			wantTotalTokens:      150,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			normalized := vertexNormalizeBatchUsage(tt.body)
			usage, ok := normalized["usage"].(map[string]any)
			require.True(t, ok)
			assert.EqualValues(t, tt.wantPromptTokens, usage["prompt_tokens"])
			assert.EqualValues(t, tt.wantCompletionTokens, usage["completion_tokens"])
			assert.EqualValues(t, tt.wantTotalTokens, usage["total_tokens"])

			if tt.wantReasoningTokens > 0 {
				details, ok := usage["completion_tokens_details"].(map[string]any)
				require.True(t, ok, "completion_tokens_details should be present")
				assert.EqualValues(t, tt.wantReasoningTokens, details["reasoning_tokens"])
			}
		})
	}
}
