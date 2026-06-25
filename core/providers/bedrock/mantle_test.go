package bedrock

import (
	"context"
	"strings"
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
		// Anthropic (Claude) models now route via the mantle native-Anthropic endpoint.
		{"claude-opus-4-8", true},
		{"anthropic.claude-3-5-sonnet-20240620-v1:0", true},
		// other families stay on the Converse path
		{"amazon.titan-text-express-v1", false},
	}
	for _, tc := range cases {
		if got := isMantleModel(ctx, tc.model); got != tc.want {
			t.Errorf("isMantleModel(%q) = %v, want %v", tc.model, got, tc.want)
		}
	}
}

func TestResolveBedrockUseClaudeMessagesAPI(t *testing.T) {
	famAnthropic := schemas.ModelFamilyAnthropic
	cases := []struct {
		name  string
		key   schemas.Key
		alias *schemas.AliasConfig
		want  bool
	}{
		{"default: no config -> false", schemas.Key{}, nil, false},
		{"key toggle on", schemas.Key{BedrockKeyConfig: &schemas.BedrockKeyConfig{UseAnthropicMessagesAPI: schemas.Ptr(true)}}, nil, true},
		{"key toggle off", schemas.Key{BedrockKeyConfig: &schemas.BedrockKeyConfig{UseAnthropicMessagesAPI: schemas.Ptr(false)}}, nil, false},
		{"alias on overrides key off",
			schemas.Key{BedrockKeyConfig: &schemas.BedrockKeyConfig{UseAnthropicMessagesAPI: schemas.Ptr(false)}},
			&schemas.AliasConfig{ModelFamily: &famAnthropic, BedrockAliasCfg: &schemas.BedrockAliasCfg{UseAnthropicMessagesAPI: schemas.Ptr(true)}}, true},
		{"alias off overrides key on",
			schemas.Key{BedrockKeyConfig: &schemas.BedrockKeyConfig{UseAnthropicMessagesAPI: schemas.Ptr(true)}},
			&schemas.AliasConfig{ModelFamily: &famAnthropic, BedrockAliasCfg: &schemas.BedrockAliasCfg{UseAnthropicMessagesAPI: schemas.Ptr(false)}}, false},
		{"alias present but unset -> falls through to key",
			schemas.Key{BedrockKeyConfig: &schemas.BedrockKeyConfig{UseAnthropicMessagesAPI: schemas.Ptr(true)}},
			&schemas.AliasConfig{ModelFamily: &famAnthropic}, true},
	}
	for _, tc := range cases {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		if tc.alias != nil {
			ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{Config: tc.alias})
		}
		if got := resolveBedrockUseClaudeMessagesAPI(ctx, tc.key); got != tc.want {
			t.Errorf("%s: resolveBedrockUseClaudeMessagesAPI = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestShouldRouteToMantle(t *testing.T) {
	provider := &BedrockProvider{}
	claudeOn := schemas.Key{BedrockKeyConfig: &schemas.BedrockKeyConfig{UseAnthropicMessagesAPI: schemas.Ptr(true)}}
	noToggle := schemas.Key{}
	cases := []struct {
		name  string
		key   schemas.Key
		model string
		alias *schemas.AliasConfig // resolved alias stashed in ctx, if any
		want  bool
	}{
		// OpenAI-family and Gemma 4 always route to mantle, regardless of the toggle.
		{"gpt-oss always mantle", noToggle, "openai.gpt-oss-120b", nil, true},
		{"gemma-4 always mantle", noToggle, "google.gemma-4-31b", nil, true},
		// Claude defaults to the Converse path (toggle off).
		{"claude default -> converse", noToggle, "anthropic.claude-3-5-sonnet-20240620-v1:0", nil, false},
		{"claude bare default -> converse", noToggle, "claude-opus-4-8", nil, false},
		// Claude with the toggle on routes to the native-Anthropic mantle path.
		{"claude toggle on -> mantle", claudeOn, "anthropic.claude-3-5-sonnet-20240620-v1:0", nil, true},
		// Non-mantle families stay on Converse; the Claude toggle is irrelevant to them.
		{"gemma-3 -> converse", claudeOn, "google.gemma-3-27b-it", nil, false},
		{"titan -> converse", claudeOn, "amazon.titan-text-express-v1", nil, false},
		// Alias whose literal name lacks the "gemma-4" substring still routes to mantle because the
		// decision is made on the canonical (alias-resolved) model id. Guards the canonicalization fix.
		{"aliased gemma-4 -> mantle", noToggle, "my-gemma",
			&schemas.AliasConfig{ModelID: "google.gemma-4-31b"}, true},
		// Aliased Claude is gated by the toggle just like a bare Claude id.
		{"aliased claude default -> converse", noToggle, "my-claude",
			&schemas.AliasConfig{ModelID: "anthropic.claude-3-5-sonnet-20240620-v1:0"}, false},
		{"aliased claude toggle on -> mantle", claudeOn, "my-claude",
			&schemas.AliasConfig{ModelID: "anthropic.claude-3-5-sonnet-20240620-v1:0"}, true},
	}
	for _, tc := range cases {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		if tc.alias != nil {
			ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{Key: tc.model, Config: tc.alias})
		}
		if got := provider.shouldRouteToMantle(ctx, tc.key, tc.model); got != tc.want {
			t.Errorf("%s: shouldRouteToMantle(%q) = %v, want %v", tc.name, tc.model, got, tc.want)
		}
	}
}

func TestMantleAnthropicHeadersAuth(t *testing.T) {
	provider := &BedrockProvider{}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	url := "https://bedrock-mantle.us-east-1.api.aws/anthropic/v1/messages"

	// A Bedrock API key authenticates via Authorization: Bearer, not the
	// Anthropic x-api-key scheme.
	t.Run("api key -> Bearer", func(t *testing.T) {
		key := schemas.Key{Value: *schemas.NewSecretVar("test-api-key")}
		headers, bErr := provider.mantleAnthropicHeaders(ctx, []byte("{}"), url, "application/json", key, "us-east-1")
		if bErr != nil {
			t.Fatalf("unexpected error: %v", bErr)
		}
		if got := headers["Authorization"]; got != "Bearer test-api-key" {
			t.Errorf("Authorization = %q, want %q", got, "Bearer test-api-key")
		}
		if _, ok := headers["x-api-key"]; ok {
			t.Errorf("x-api-key must not be sent on the mantle path, got %q", headers["x-api-key"])
		}
		if _, ok := headers["X-Amz-Date"]; ok {
			t.Errorf("SigV4 headers must not be present when an API key is used")
		}
	})

	// Without an API key, the request is SigV4-signed for the bedrock-mantle service.
	t.Run("no api key -> SigV4", func(t *testing.T) {
		key := schemas.Key{
			BedrockKeyConfig: &schemas.BedrockKeyConfig{
				AccessKey: *schemas.NewSecretVar("AKIAEXAMPLE"),
				SecretKey: *schemas.NewSecretVar("secretexamplekey"),
			},
		}
		headers, bErr := provider.mantleAnthropicHeaders(ctx, []byte("{}"), url, "application/json", key, "us-east-1")
		if bErr != nil {
			t.Fatalf("unexpected error: %v", bErr)
		}
		auth := headers["Authorization"]
		if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256") {
			t.Errorf("expected SigV4 Authorization, got %q", auth)
		}
		if _, ok := headers["X-Amz-Date"]; !ok {
			t.Errorf("expected SigV4 X-Amz-Date header to be present")
		}
	})
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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mantleOpenAIURL(tc.region, tc.model, tc.path); got != tc.want {
				t.Errorf("mantleOpenAIURL(%q, %q, %q) = %q, want %q", tc.region, tc.model, tc.path, got, tc.want)
			}
		})
	}
}
