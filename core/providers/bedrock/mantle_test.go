package bedrock

import (
	"context"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

func TestIsMantleModel(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
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
		// Grok → mantle (mantle-only, no Converse endpoint)
		{"xai.grok-4.3", true},
		{"us.xai.grok-4.3", true},
		// Anthropic (Claude) models stay on the Converse path.
		{"claude-opus-4-8", false},
		{"anthropic.claude-3-5-sonnet-20240620-v1:0", false},
		// other families stay on the Converse path
		{"amazon.titan-text-express-v1", false},
	}
	for _, tc := range cases {
		if got := isMantleModel(ctx, tc.model); got != tc.want {
			t.Errorf("isMantleModel(%q) = %v, want %v", tc.model, got, tc.want)
		}
	}
}

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
		{"gpt-oss-safeguard uses bare v1", "us-west-2", "openai.gpt-oss-safeguard-120b", "chat/completions",
			"https://bedrock-mantle.us-west-2.api.aws/v1/chat/completions"},
		{"gpt-5.x uses openai/v1", "us-east-2", "openai.gpt-5.5", "responses",
			"https://bedrock-mantle.us-east-2.api.aws/openai/v1/responses"},
		{"gemma-4 uses openai/v1", "us-east-1", "google.gemma-4-31b", "responses",
			"https://bedrock-mantle.us-east-1.api.aws/openai/v1/responses"},
		{"gemma-3 uses bare v1", "us-east-1", "google.gemma-3-12b-it", "chat/completions",
			"https://bedrock-mantle.us-east-1.api.aws/v1/chat/completions"},
		{"grok uses openai/v1", "us-east-1", "xai.grok-4.3", "responses",
			"https://bedrock-mantle.us-east-1.api.aws/openai/v1/responses"},
		// Mantle answers a frontier model on exactly one path and 400s on the other:
		// "model `openai.gpt-6-astra` isn't supported on this route" (verified us-west-2).
		{"gpt-6 uses openai/v1", "us-west-2", "openai.gpt-6-astra", "responses",
			"https://bedrock-mantle.us-west-2.api.aws/openai/v1/responses"},
		{"gpt-6 chat uses openai/v1", "us-west-2", "gpt-6-astra", "chat/completions",
			"https://bedrock-mantle.us-west-2.api.aws/openai/v1/chat/completions"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mantleOpenAIURL(nil, tc.region, tc.model, tc.path); got != tc.want {
				t.Errorf("mantleOpenAIURL(%q, %q, %q) = %q, want %q", tc.region, tc.model, tc.path, got, tc.want)
			}
		})
	}
}

// The "{region}/" prefix is Bifrost addressing, not part of the AWS identifier:
// resolveBedrockRegion consumes it for the host and signing scope, and AWS 404s
// whatever is left ("The model 'us-west-2/openai.gpt-6-astra' does not exist").
// The OpenAI-compatible handlers put request.Model on the wire themselves, so they
// strip it after the region is resolved.
func TestParseBedrockRegionAndModelStripsForTheWire(t *testing.T) {
	cases := []struct {
		model      string
		wantRegion string
		wantBare   string
	}{
		{"us-west-2/openai.gpt-6-astra", "us-west-2", "openai.gpt-6-astra"},
		{"us-gov-west-1/openai.gpt-5.6-terra", "us-gov-west-1", "openai.gpt-5.6-terra"},
		{"openai.gpt-6-astra", "", "openai.gpt-6-astra"},
		// A cross-region profile is dotted, not slashed, and must survive intact.
		{"us.openai.gpt-5.6-terra", "", "us.openai.gpt-5.6-terra"},
		// A vendor segment is not a region: only awsRegionRegex may strip.
		{"openai/gpt-6-astra", "", "openai/gpt-6-astra"},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			region, bare := parseBedrockRegionAndModel(tc.model)
			if region != tc.wantRegion || bare != tc.wantBare {
				t.Errorf("got (%q, %q), want (%q, %q)", region, bare, tc.wantRegion, tc.wantBare)
			}
		})
	}
}
