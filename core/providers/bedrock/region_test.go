package bedrock

import (
	"net/url"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
)

func TestParseBedrockRegionAndModel(t *testing.T) {
	cases := []struct {
		input      string
		wantRegion string
		wantModel  string
	}{
		{"eu-north-1/moonshotai.kimi-k2.5", "eu-north-1", "moonshotai.kimi-k2.5"},
		{"us-east-1/amazon.nova-lite-v1:0", "us-east-1", "amazon.nova-lite-v1:0"},
		{"ap-southeast-2/anthropic.claude-3-5-sonnet-20241022-v2:0", "ap-southeast-2", "anthropic.claude-3-5-sonnet-20241022-v2:0"},
		// GovCloud regions (multi-segment directional part)
		{"us-gov-east-1/amazon.nova-lite-v1:0", "us-gov-east-1", "amazon.nova-lite-v1:0"},
		{"us-gov-west-1/anthropic.claude-v2", "us-gov-west-1", "anthropic.claude-v2"},
		// Commitment-tier format: region is stripped, remainder (including slash) is the bare model ID
		{"eu-central-1/1-month-commitment/anthropic.claude-v1", "eu-central-1", "1-month-commitment/anthropic.claude-v1"},
		{"us-east-1/6-month-commitment/amazon.nova-lite-v1:0", "us-east-1", "6-month-commitment/amazon.nova-lite-v1:0"},
		// Model namespace prefix must be preserved as-is (not treated as a region)
		{"meta-llama/Llama-3.1-8B", "", "meta-llama/Llama-3.1-8B"},
		{"moonshotai.kimi-k2.5", "", "moonshotai.kimi-k2.5"},
		{"anthropic.claude-3-5-sonnet-20241022-v2:0", "", "anthropic.claude-3-5-sonnet-20241022-v2:0"},
		// Commitment strings alone must not match (start with digit)
		{"1-month-commitment/anthropic.claude-v1", "", "1-month-commitment/anthropic.claude-v1"},
		// ARN strings must not be split
		{"arn:aws:bedrock:us-east-1::foundation-model/amazon.nova-lite-v1:0", "", "arn:aws:bedrock:us-east-1::foundation-model/amazon.nova-lite-v1:0"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			gotRegion, gotModel := parseBedrockRegionAndModel(tc.input)
			assert.Equal(t, tc.wantRegion, gotRegion)
			assert.Equal(t, tc.wantModel, gotModel)
		})
	}
}

func TestGetModelPathStripsRegion(t *testing.T) {
	provider := &BedrockProvider{}
	key := schemas.Key{BedrockKeyConfig: &schemas.BedrockKeyConfig{}}

	cases := []struct {
		model    string
		basePath string
		wantPath string
	}{
		{"eu-north-1/moonshotai.kimi-k2.5", "converse", "moonshotai.kimi-k2.5/converse"},
		{"us-east-1/amazon.nova-lite-v1:0", "converse", "amazon.nova-lite-v1:0/converse"},
		{"us-gov-east-1/amazon.nova-lite-v1:0", "converse", "amazon.nova-lite-v1:0/converse"},
		{"eu-central-1/1-month-commitment/anthropic.claude-v1", "converse", "1-month-commitment/anthropic.claude-v1/converse"},
		{"moonshotai.kimi-k2.5", "converse", "moonshotai.kimi-k2.5/converse"},
		{"meta-llama/Llama-3.1-8B", "converse", "meta-llama/Llama-3.1-8B/converse"},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			got, _ := provider.getModelPathAndRegion(nil, tc.basePath, tc.model, key)
			assert.Equal(t, tc.wantPath, got)
		})
	}
}

func TestGetModelPathStripsRegionWithARN(t *testing.T) {
	provider := &BedrockProvider{}
	arn := "arn:aws:bedrock:us-east-1::foundation-model"
	key := schemas.Key{
		BedrockKeyConfig: &schemas.BedrockKeyConfig{
			ARN: schemas.NewSecretVar(arn),
		},
	}

	cases := []struct {
		model    string
		wantPath string
	}{
		{
			// Region prefix must be stripped; only bareModel appears inside the ARN path.
			model:    "eu-north-1/anthropic.claude-v2",
			wantPath: url.PathEscape(arn+"/anthropic.claude-v2") + "/converse",
		},
		{
			// No region prefix — model used as-is inside the ARN path.
			model:    "anthropic.claude-v2",
			wantPath: url.PathEscape(arn+"/anthropic.claude-v2") + "/converse",
		},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			got, _ := provider.getModelPathAndRegion(nil, "converse", tc.model, key)
			assert.Equal(t, tc.wantPath, got)
		})
	}
}

// TestResolveBedrockRegion_AliasOverride verifies the per-alias Region
// override slots between the model-string prefix (highest priority) and the
// key-level Region (lower priority).
func TestResolveBedrockRegion_AliasOverride(t *testing.T) {
	keyRegion := "us-east-1"
	aliasRegion := "us-west-2"
	key := schemas.Key{
		BedrockKeyConfig: &schemas.BedrockKeyConfig{
			Region: schemas.NewSecretVar(keyRegion),
		},
	}

	// Build ctx carrying an alias with Region override.
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{
		Key: "best-claude",
		Config: &schemas.AliasConfig{
			ModelID: "anthropic.claude-3-5-sonnet-20241022-v2:0",
			Region:  schemas.NewSecretVar(aliasRegion),
		},
	})

	// Bare model — alias.Region wins over key.Region.
	if got := resolveBedrockRegion(ctx, key, "anthropic.claude-3-5-sonnet-20241022-v2:0"); got != aliasRegion {
		t.Errorf("alias override should win over key region: got %q, want %q", got, aliasRegion)
	}

	// Model string with explicit region prefix — wins over alias override.
	if got := resolveBedrockRegion(ctx, key, "eu-west-1/anthropic.claude-v2"); got != "eu-west-1" {
		t.Errorf("model-string region should win over alias override: got %q", got)
	}

	// No alias in ctx — falls through to key.Region.
	emptyCtx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	if got := resolveBedrockRegion(emptyCtx, key, "anthropic.claude-v2"); got != keyRegion {
		t.Errorf("no alias: should use key.Region: got %q, want %q", got, keyRegion)
	}
}

// TestResolveBedrockARN_AliasOverride verifies the BedrockAliasCfg
// InferenceProfileARN override takes precedence over key.BedrockKeyConfig.ARN.
func TestResolveBedrockARN_AliasOverride(t *testing.T) {
	keyARN := "arn:aws:bedrock:us-east-1:1234567890:resource-config/default"
	aliasARN := "arn:aws:bedrock:us-east-1:1234567890:inference-profile/us.anthropic.claude-3-7-sonnet"
	key := schemas.Key{
		BedrockKeyConfig: &schemas.BedrockKeyConfig{
			ARN: schemas.NewSecretVar(keyARN),
		},
	}

	// No alias — falls back to key.ARN.
	if got := resolveBedrockARN(nil, key); got != keyARN {
		t.Errorf("nil ctx: got %q, want key ARN %q", got, keyARN)
	}

	// Alias with InferenceProfileARN override wins.
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{
		Key: "best-claude",
		Config: &schemas.AliasConfig{
			ModelID: "anthropic.claude-3-7-sonnet-20250219-v1:0",
			BedrockAliasCfg: &schemas.BedrockAliasCfg{
				InferenceProfileARN: schemas.NewSecretVar(aliasARN),
			},
		},
	})
	if got := resolveBedrockARN(ctx, key); got != aliasARN {
		t.Errorf("alias override should win: got %q, want %q", got, aliasARN)
	}

	// Empty alias ARN — falls through to key.ARN.
	ctx2 := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	ctx2.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{
		Key: "x",
		Config: &schemas.AliasConfig{
			ModelID: "x",
			BedrockAliasCfg: &schemas.BedrockAliasCfg{
				InferenceProfileARN: schemas.NewSecretVar(""),
			},
		},
	})
	if got := resolveBedrockARN(ctx2, key); got != keyARN {
		t.Errorf("empty alias ARN should fall through to key ARN: got %q, want %q", got, keyARN)
	}
}

func TestResolveBedrockRegion(t *testing.T) {
	configuredRegion := "ap-southeast-1"
	key := schemas.Key{
		BedrockKeyConfig: &schemas.BedrockKeyConfig{
			Region: schemas.NewSecretVar(configuredRegion),
		},
	}
	keyNoRegion := schemas.Key{BedrockKeyConfig: &schemas.BedrockKeyConfig{}}

	cases := []struct {
		desc       string
		key        schemas.Key
		model      string
		wantRegion string
	}{
		{"model region overrides key config", key, "eu-north-1/moonshotai.kimi-k2.5", "eu-north-1"},
		{"key config used when no model region", key, "moonshotai.kimi-k2.5", configuredRegion},
		{"default used when neither configured", keyNoRegion, "moonshotai.kimi-k2.5", DefaultBedrockRegion},
		{"model region overrides even when key has region", key, "us-west-2/amazon.nova-lite-v1:0", "us-west-2"},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			got := resolveBedrockRegion(nil, tc.key, tc.model)
			assert.Equal(t, tc.wantRegion, got)
		})
	}
}
