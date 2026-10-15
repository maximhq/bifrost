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

// Converse takes a guardrail as a guardrailConfig body field; the OpenAI-compatible
// endpoints take it as headers and ignore the body field, so the two renderings are not
// interchangeable. Mantle is deliberately not wired: it accepts the headers and enforces
// nothing, so there is no rendering that works there.
func TestWithGuardrailHeaders(t *testing.T) {
	base := map[string]string{"X-Existing": "keep"}

	t.Run("renders the headers and consumes the extra param", func(t *testing.T) {
		extra := map[string]any{
			"guardrailConfig": map[string]any{
				"guardrailIdentifier": "gr-123",
				"guardrailVersion":    "DRAFT",
				"trace":               "ENABLED",
			},
			"other": "untouched",
		}
		got := withGuardrailHeaders(base, extra)

		if got[guardrailIdentifierHeader] != "gr-123" || got[guardrailVersionHeader] != "DRAFT" {
			t.Errorf("guardrail headers = %v", got)
		}
		if got[guardrailTraceHeader] != "ENABLED" {
			t.Errorf("trace header = %q", got[guardrailTraceHeader])
		}
		if got["X-Existing"] != "keep" {
			t.Error("existing headers must survive")
		}
		// Read, never consumed: core reuses one request across retry attempts, so
		// removing it would drop the guardrail on every attempt after the first.
		if _, still := extra["guardrailConfig"]; !still {
			t.Error("guardrailConfig must survive for the next retry attempt")
		}
		if extra["other"] != "untouched" {
			t.Error("unrelated extra params must be left alone")
		}
		// The shared networkConfig map must never be written through.
		if _, leaked := base[guardrailIdentifierHeader]; leaked {
			t.Error("base header map was mutated")
		}
	})

	t.Run("no guardrail config returns base unchanged", func(t *testing.T) {
		if got := withGuardrailHeaders(base, map[string]any{"other": 1}); len(got) != 1 {
			t.Errorf("expected base untouched, got %v", got)
		}
	})

	// Both fields are required upstream; half a config is left alone rather than sent.
	t.Run("identifier without version is not sent", func(t *testing.T) {
		extra := map[string]any{"guardrailConfig": map[string]any{"guardrailIdentifier": "gr-123"}}
		got := withGuardrailHeaders(base, extra)
		if _, ok := got[guardrailIdentifierHeader]; ok {
			t.Error("a half-formed guardrail config must not be sent")
		}
		if _, still := extra["guardrailConfig"]; !still {
			t.Error("an unused config must be left in place")
		}
	})

	// SetExtraHeaders canonicalises keys and keeps the first it reaches; Go map order is
	// random, so a differently-cased static header must be replaced, not merely shadowed.
	t.Run("a differently-cased static header is replaced, not doubled", func(t *testing.T) {
		static := map[string]string{
			"x-amzn-bedrock-guardrailidentifier": "gr-static",
			"X-Other":                            "keep",
		}
		extra := map[string]any{"guardrailConfig": map[string]any{
			"guardrailIdentifier": "gr-request", "guardrailVersion": "1"}}
		got := withGuardrailHeaders(static, extra)

		seen := 0
		for k, v := range got {
			if strings.EqualFold(k, guardrailIdentifierHeader) {
				seen++
				if v != "gr-request" {
					t.Errorf("per-request value should win, got %q", v)
				}
			}
		}
		if seen != 1 {
			t.Errorf("expected exactly one identifier header, found %d in %v", seen, got)
		}
		if got["X-Other"] != "keep" {
			t.Error("unrelated static headers must survive")
		}
		if static["x-amzn-bedrock-guardrailidentifier"] != "gr-static" {
			t.Error("the caller's map was mutated")
		}
	})

	// The same request object is handed to every retry attempt, so repeated calls must
	// render the same headers rather than degrade after the first.
	t.Run("repeated calls are stable across retry attempts", func(t *testing.T) {
		extra := map[string]any{"guardrailConfig": map[string]any{
			"guardrailIdentifier": "gr-1", "guardrailVersion": "DRAFT"}}
		first := withGuardrailHeaders(base, extra)
		second := withGuardrailHeaders(base, extra)
		if second[guardrailIdentifierHeader] != first[guardrailIdentifierHeader] ||
			second[guardrailVersionHeader] != first[guardrailVersionHeader] {
			t.Errorf("second attempt lost the guardrail: %v then %v", first, second)
		}
	})

	t.Run("nil base map is handled", func(t *testing.T) {
		extra := map[string]any{"guardrailConfig": map[string]any{
			"guardrailIdentifier": "gr-1", "guardrailVersion": "1"}}
		if got := withGuardrailHeaders(nil, extra); got[guardrailIdentifierHeader] != "gr-1" {
			t.Errorf("got %v", got)
		}
	})
}
