package bedrock

import "testing"

func TestIsMantleModel(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		// gpt-oss family → mantle
		{"gpt-oss-120b", true},
		{"openai.gpt-oss-20b", true},
		{"gpt-oss-safeguard-120b", true},
		{"us.openai.gpt-oss-120b", true},
		// closed gpt-5.x → mantle
		{"gpt-5.5", true},
		{"openai.gpt-5.4", true},
		// Gemma 4 → mantle (mantle-only, no Converse endpoint)
		{"gemma-4-31b", true},
		{"google.gemma-4-e2b", true},
		{"gemma-4-26b-a4b", true},
		// Gemma 3 → NOT mantle: it has a Converse fallback that serves both APIs,
		// while mantle only supports Chat (so Responses would break there).
		{"gemma-3-12b-it", false},
		{"google.gemma-3-27b-it", false},
		{"gemma-3-4b-it", false},
		// other families stay on the Converse path
		{"claude-opus-4-8", false},
		{"anthropic.claude-3-5-sonnet-20240620-v1:0", false},
		{"amazon.titan-text-express-v1", false},
	}
	for _, tc := range cases {
		if got := isMantleModel(tc.model); got != tc.want {
			t.Errorf("isMantleModel(%q) = %v, want %v", tc.model, got, tc.want)
		}
	}
}

func TestMantleURL(t *testing.T) {
	cases := []struct {
		name   string
		region string
		model  string
		path   string
		want   string
	}{
		{"gpt-oss uses bare v1", "us-east-1", "openai.gpt-oss-120b", "chat/completions",
			"https://bedrock-mantle.us-east-1.api.aws/v1/chat/completions"},
		{"gpt-oss-safeguard uses bare v1", "us-west-2", "openai.gpt-oss-safeguard-120b", "chat/completions",
			"https://bedrock-mantle.us-west-2.api.aws/v1/chat/completions"},
		{"gpt-5.x uses openai/v1", "us-east-2", "openai.gpt-5.5", "responses",
			"https://bedrock-mantle.us-east-2.api.aws/openai/v1/responses"},
		{"gemma-4 uses openai/v1", "us-east-1", "google.gemma-4-31b", "responses",
			"https://bedrock-mantle.us-east-1.api.aws/openai/v1/responses"},
		{"gemma-3 uses bare v1", "us-east-1", "google.gemma-3-12b-it", "chat/completions",
			"https://bedrock-mantle.us-east-1.api.aws/v1/chat/completions"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mantleURL(tc.region, tc.model, tc.path); got != tc.want {
				t.Errorf("mantleURL(%q, %q, %q) = %q, want %q", tc.region, tc.model, tc.path, got, tc.want)
			}
		})
	}
}
