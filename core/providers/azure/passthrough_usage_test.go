package azure

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestExtractAzurePassthroughUsage verifies Azure dispatches usage extraction by upstream model:
// Anthropic models use the Anthropic extractor (its usage shape), everything else uses OpenAI's.
func TestExtractAzurePassthroughUsage(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		body   string
		model  string
		check  func(t *testing.T, u *schemas.BifrostPassthroughUsage)
	}{
		{
			name:   "anthropic model routes to anthropic extractor",
			method: "POST",
			path:   "/v1/messages",
			body:   `{"usage":{"input_tokens":66,"output_tokens":26}}`,
			model:  "claude-sonnet-4-5",
			check: func(t *testing.T, u *schemas.BifrostPassthroughUsage) {
				if u == nil || u.LLMUsage == nil || u.LLMUsage.PromptTokens != 66 ||
					u.LLMUsage.CompletionTokens != 26 || u.LLMUsage.TotalTokens != 92 {
					t.Fatalf("anthropic dispatch usage = %+v", u)
				}
			},
		},
		{
			name:   "openai model routes to openai extractor",
			method: "POST",
			path:   "/chat/completions",
			body:   `{"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
			model:  "gpt-4o",
			check: func(t *testing.T, u *schemas.BifrostPassthroughUsage) {
				if u == nil || u.LLMUsage == nil || u.LLMUsage.PromptTokens != 10 ||
					u.LLMUsage.CompletionTokens != 5 || u.LLMUsage.TotalTokens != 15 {
					t.Fatalf("openai dispatch usage = %+v", u)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := extractAzurePassthroughUsage(tt.method, tt.path, nil, []byte(tt.body), tt.model)
			tt.check(t, u)
		})
	}
}
