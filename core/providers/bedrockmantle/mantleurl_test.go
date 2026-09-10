package bedrockmantle

import (
	"testing"
)

// TestMantleOpenAIURL pins the base-path split. Mantle answers a given model on
// exactly one of "v1" and "openai/v1" and 400s on the other, so a family landing
// on the wrong path fails every request. The gate names each closed generation
// explicitly, which is what makes a new one easy to miss; bedrock.mantleOpenAIURL
// carries the same table for its own copy of this function.
func TestMantleOpenAIURL(t *testing.T) {
	cases := []struct {
		name   string
		region string
		model  string
		path   string
		want   string
	}{
		{"gpt-oss uses bare v1", "us-east-1", "openai.gpt-oss-120b", "chat/completions",
			"https://bedrock-mantle.us-east-1.api.aws/v1/chat/completions"},
		{"gpt-5.x uses openai/v1", "us-east-1", "openai.gpt-5.6-sol", "responses",
			"https://bedrock-mantle.us-east-1.api.aws/openai/v1/responses"},
		{"gpt-6 uses openai/v1", "us-west-2", "openai.gpt-6-astra", "responses",
			"https://bedrock-mantle.us-west-2.api.aws/openai/v1/responses"},
		{"gpt-6 chat uses openai/v1", "us-west-2", "gpt-6-astra", "chat/completions",
			"https://bedrock-mantle.us-west-2.api.aws/openai/v1/chat/completions"},
		{"gemma-4 uses openai/v1", "us-east-1", "google.gemma-4-31b", "responses",
			"https://bedrock-mantle.us-east-1.api.aws/openai/v1/responses"},
		{"gemma-3 uses bare v1", "us-east-1", "google.gemma-3-12b-it", "chat/completions",
			"https://bedrock-mantle.us-east-1.api.aws/v1/chat/completions"},
		{"grok uses openai/v1", "us-east-1", "xai.grok-4.3", "responses",
			"https://bedrock-mantle.us-east-1.api.aws/openai/v1/responses"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mantleOpenAIURL(nil, tc.region, tc.model, tc.path); got != tc.want {
				t.Errorf("mantleOpenAIURL(%q, %q, %q) = %q, want %q", tc.region, tc.model, tc.path, got, tc.want)
			}
		})
	}
}
