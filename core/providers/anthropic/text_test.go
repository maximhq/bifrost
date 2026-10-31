package anthropic

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// The legacy /v1/complete route takes a prompt the caller already framed as
// "\n\nHuman: ...\n\nAssistant:" (the API rejects anything else), so egress must
// not wrap it a second time; a bare prompt from /v1/completions still gets the
// frame. top_k from the native body must reach the wire as the top-level field.
func TestToAnthropicTextCompletionRequestFraming(t *testing.T) {
	cases := []struct {
		name   string
		prompt string
		want   string
	}{
		{"already framed", "\n\nHuman: Say hi\n\nAssistant:", "\n\nHuman: Say hi\n\nAssistant:"},
		{"already framed with trailing space", "\n\nHuman: Say hi\n\nAssistant: ", "\n\nHuman: Say hi\n\nAssistant: "},
		{"multi-turn framed", "\n\nHuman: a\n\nAssistant: b\n\nHuman: c\n\nAssistant:", "\n\nHuman: a\n\nAssistant: b\n\nHuman: c\n\nAssistant:"},
		{"framed without assistant turn", "\n\nHuman: Say hi", "\n\nHuman: Say hi\n\nAssistant:"},
		{"bare prompt", "Say hi", "\n\nHuman: Say hi\n\nAssistant:"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			topK := 5
			native := &AnthropicTextRequest{Model: "anthropic/claude-2.1", Prompt: tc.prompt, MaxTokensToSample: 16, TopK: &topK}
			out := ToAnthropicTextCompletionRequest(native.ToBifrostTextCompletionRequest(nil))
			require.Equal(t, tc.want, out.Prompt)
			require.NotNil(t, out.TopK, "top_k was dropped before the wire")
			require.Equal(t, 5, *out.TopK)
			require.NotContains(t, out.ExtraParams, "top_k", "top_k must not be sent twice")
		})
	}
}

func TestToAnthropicTextCompletionRequestPromptArray(t *testing.T) {
	out := ToAnthropicTextCompletionRequest(&schemas.BifrostTextCompletionRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-2.1",
		Input:    &schemas.TextCompletionInput{PromptArray: []string{"one", "two"}},
	})
	require.Equal(t, "\n\nHuman: one\n\ntwo\n\nAssistant:", out.Prompt)
}
