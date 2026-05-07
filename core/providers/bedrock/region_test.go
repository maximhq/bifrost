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
			got, _ := provider.getModelPathAndRegion(tc.basePath, tc.model, key)
			assert.Equal(t, tc.wantPath, got)
		})
	}
}

func TestGetModelPathStripsRegionWithARN(t *testing.T) {
	provider := &BedrockProvider{}
	arn := "arn:aws:bedrock:us-east-1::foundation-model"
	key := schemas.Key{
		BedrockKeyConfig: &schemas.BedrockKeyConfig{
			ARN: schemas.NewEnvVar(arn),
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
			got, _ := provider.getModelPathAndRegion("converse", tc.model, key)
			assert.Equal(t, tc.wantPath, got)
		})
	}
}

func TestResolveBedrockRegion(t *testing.T) {
	configuredRegion := "ap-southeast-1"
	key := schemas.Key{
		BedrockKeyConfig: &schemas.BedrockKeyConfig{
			Region: schemas.NewEnvVar(configuredRegion),
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
			got := resolveBedrockRegion(tc.key, tc.model)
			assert.Equal(t, tc.wantRegion, got)
		})
	}
}
