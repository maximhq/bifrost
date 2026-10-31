package bedrock

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// Claude text completions on Bedrock reuse the Anthropic prompt framing, so a
// prompt the caller already framed must not be wrapped a second time.
func TestToBedrockTextCompletionRequestClaudeFraming(t *testing.T) {
	for _, tc := range []struct{ prompt, want string }{
		{"\n\nHuman: Say hi\n\nAssistant:", "\n\nHuman: Say hi\n\nAssistant:"},
		{"Say hi", "\n\nHuman: Say hi\n\nAssistant:"},
	} {
		out := ToBedrockTextCompletionRequest(&schemas.BifrostTextCompletionRequest{
			Provider: schemas.Bedrock,
			Model:    "anthropic.claude-v2:1",
			Input:    &schemas.TextCompletionInput{PromptStr: schemas.Ptr(tc.prompt)},
		})
		require.Equal(t, tc.want, out.Prompt)
	}
}
