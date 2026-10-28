package sagemaker

import (
	"context"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/providers/bedrock"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

func TestParseModelID(t *testing.T) {
	cases := []struct {
		model string
		want  modelID
	}{
		{"qwen3-4b-ep", modelID{endpoint: "qwen3-4b-ep"}},
		{"us-east-1/qwen3-4b-ep", modelID{region: "us-east-1", endpoint: "qwen3-4b-ep"}},
		{"qwen3-4b-ep/qwen3-ic", modelID{endpoint: "qwen3-4b-ep", ic: "qwen3-ic"}},
		{"us-gov-west-1/ep/ic", modelID{region: "us-gov-west-1", endpoint: "ep", ic: "ic"}},
		// An endpoint literally named like a region is only reachable with the key-level region.
		{"us-east-1", modelID{endpoint: "us-east-1"}},
	}
	for _, tc := range cases {
		got, bErr := parseModelID(tc.model)
		require.Nil(t, bErr, tc.model)
		require.Equal(t, tc.want, got, tc.model)
	}

	for _, bad := range []string{"", "ep/ic/extra", "-leading-dash", "ep_underscore", "us-east-1/", "ep/", "a/b/c/d"} {
		_, bErr := parseModelID(bad)
		require.NotNil(t, bErr, "expected %q to be rejected", bad)
		require.Contains(t, bErr.Error.Message, "[region/]endpoint-name[/inference-component-name]")
	}
}

func TestResolveRegion(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctx.Cancel()
	key := schemas.Key{SageMakerKeyConfig: &schemas.SageMakerKeyConfig{Region: schemas.NewSecretVar("eu-west-1")}}

	region, bErr := resolveRegion(ctx, key, modelID{region: "us-west-2", endpoint: "ep"})
	require.Nil(t, bErr)
	require.Equal(t, "us-west-2", region, "a model-id prefix wins over the key region")

	region, bErr = resolveRegion(ctx, key, modelID{endpoint: "ep"})
	require.Nil(t, bErr)
	require.Equal(t, "eu-west-1", region)

	_, bErr = resolveRegion(ctx, schemas.Key{SageMakerKeyConfig: &schemas.SageMakerKeyConfig{}}, modelID{endpoint: "ep"})
	require.NotNil(t, bErr, "SageMaker endpoints are regional, so a missing region must be an error, not a default")
	require.Contains(t, bErr.Error.Message, "sagemaker_key_config.region is required")
}

func TestOpenAIURL(t *testing.T) {
	require.Equal(t,
		"https://runtime.sagemaker.us-east-1.amazonaws.com/endpoints/ep/openai/v1/chat/completions",
		openAIURL(runtimeHost(nil, "us-east-1"), modelID{endpoint: "ep"}, "chat/completions"))
	require.Equal(t,
		"https://runtime.sagemaker.us-east-1.amazonaws.com/endpoints/ep/inference-components/ic/openai/v1/chat/completions",
		openAIURL(runtimeHost(nil, "us-east-1"), modelID{endpoint: "ep", ic: "ic"}, "chat/completions"))
	require.Equal(t, "runtime.sagemaker.cn-north-1.amazonaws.com.cn", runtimeHost(nil, "cn-north-1"))

	cfg := &schemas.SageMakerKeyConfig{Endpoints: &schemas.SageMakerEndpoints{
		Runtime: schemas.NewSecretVar("https://vpce-0abc.runtime.sagemaker.us-east-1.vpce.amazonaws.com/"),
	}}
	require.Equal(t, "vpce-0abc.runtime.sagemaker.us-east-1.vpce.amazonaws.com", runtimeHost(cfg, "us-east-1"))
}

func TestNameValidatorsMatchSageMakerPatterns(t *testing.T) {
	for _, ok := range []string{"us-east-1", "eu-north-1", "ap-southeast-2", "us-gov-west-1", "cn-north-1"} {
		require.True(t, bedrock.AWSRegionRegex.MatchString(ok), ok)
	}
	for _, bad := range []string{"us-east", "useast-1", "u-east-1", "usaa-east-1", "us--east-1", "US-east-1", "us-east-1a", "ep", ""} {
		require.False(t, bedrock.AWSRegionRegex.MatchString(bad), bad)
	}
	for _, ok := range []string{"a", "qwen3-4b", "a--b", "ABC123", strings.Repeat("a", 63)} {
		require.True(t, isResourceName(ok), ok)
	}
	for _, bad := range []string{"", "-a", "a-", "a_b", "a.b", strings.Repeat("a", 64)} {
		require.False(t, isResourceName(bad), bad)
	}
}

func BenchmarkParseModelID(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if _, bErr := parseModelID("qwen3-4b-endpoint"); bErr != nil {
			b.Fatal(bErr.Error.Message)
		}
	}
}

func BenchmarkParseModelIDWithRegionAndIC(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if _, bErr := parseModelID("us-east-1/shared-endpoint/qwen3-4b-ic"); bErr != nil {
			b.Fatal(bErr.Error.Message)
		}
	}
}

func BenchmarkOpenAIURL(b *testing.B) {
	id := modelID{endpoint: "qwen3-4b-endpoint", ic: "qwen3-4b-ic"}
	b.ReportAllocs()
	for b.Loop() {
		_ = openAIURL("runtime.sagemaker.us-east-1.amazonaws.com", id, "chat/completions")
	}
}
