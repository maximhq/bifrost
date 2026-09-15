package bedrock

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func secret(v string) *schemas.SecretVar {
	return &schemas.SecretVar{Val: v}
}

func TestResolveBedrockHostDefaults(t *testing.T) {
	cases := []struct {
		service bedrockService
		want    string
	}{
		{bedrockServiceRuntime, "bedrock-runtime.eu-west-2.amazonaws.com"},
		{bedrockServiceControlPlane, "bedrock.eu-west-2.amazonaws.com"},
		{bedrockServiceAgentRuntime, "bedrock-agent-runtime.eu-west-2.amazonaws.com"},
		{bedrockServiceS3, "s3.eu-west-2.amazonaws.com"},
		// Mantle is served from api.aws, not amazonaws.com.
		{bedrockServiceMantle, "bedrock-mantle.eu-west-2.api.aws"},
	}
	for _, tc := range cases {
		t.Run(string(tc.service), func(t *testing.T) {
			// A nil endpoints struct and an empty one must both fall back to the public host.
			assert.Equal(t, tc.want, resolveBedrockHost(nil, tc.service, "eu-west-2"))
			assert.Equal(t, tc.want, resolveBedrockHost(&schemas.BedrockEndpoints{}, tc.service, "eu-west-2"))
		})
	}
}

func TestResolveBedrockHostOverridePerService(t *testing.T) {
	endpoints := &schemas.BedrockEndpoints{
		Runtime: secret("vpce-0abc123-x1y2z3.bedrock-runtime.eu-west-2.vpce.amazonaws.com"),
	}

	assert.Equal(t, "vpce-0abc123-x1y2z3.bedrock-runtime.eu-west-2.vpce.amazonaws.com",
		resolveBedrockHost(endpoints, bedrockServiceRuntime, "eu-west-2"))

	// An override for one service must not leak onto the others: a runtime VPC endpoint cannot
	// serve control-plane or agent-runtime calls.
	assert.Equal(t, "bedrock.eu-west-2.amazonaws.com", resolveBedrockHost(endpoints, bedrockServiceControlPlane, "eu-west-2"))
	assert.Equal(t, "bedrock-agent-runtime.eu-west-2.amazonaws.com", resolveBedrockHost(endpoints, bedrockServiceAgentRuntime, "eu-west-2"))
	assert.Equal(t, "s3.eu-west-2.amazonaws.com", resolveBedrockHost(endpoints, bedrockServiceS3, "eu-west-2"))
	assert.Equal(t, "bedrock-mantle.eu-west-2.api.aws", resolveBedrockHost(endpoints, bedrockServiceMantle, "eu-west-2"))
}

func TestResolveBedrockHostNormalizesValue(t *testing.T) {
	const host = "vpce-0abc123-x1y2z3.bedrock-runtime.eu-west-2.vpce.amazonaws.com"
	cases := []struct {
		name       string
		configured string
		want       string
	}{
		{"bare host", host, host},
		{"with scheme", "https://" + host, host},
		{"with scheme and trailing slash", "https://" + host + "/", host},
		{"with scheme and path", "https://" + host + "/model/foo", host},
		{"surrounding whitespace", "  " + host + "  ", host},
		// An empty value must not shadow the public host.
		{"empty", "", "bedrock-runtime.eu-west-2.amazonaws.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			endpoints := &schemas.BedrockEndpoints{Runtime: secret(tc.configured)}
			assert.Equal(t, tc.want, resolveBedrockHost(endpoints, bedrockServiceRuntime, "eu-west-2"))
		})
	}
}

func TestBedrockEndpointsNilKeyConfig(t *testing.T) {
	// The API-key auth path can reach URL construction with no BedrockKeyConfig at all.
	assert.Nil(t, bedrockEndpoints(nil))
	assert.Nil(t, bedrockEndpoints(&schemas.BedrockKeyConfig{}))
}

func TestMantleOpenAIURLHonoursEndpointOverride(t *testing.T) {
	endpoints := &schemas.BedrockEndpoints{
		Mantle: secret("vpce-0abc123-x1y2z3.bedrock-mantle.eu-west-2.vpce.amazonaws.com"),
	}
	assert.Equal(t,
		"https://vpce-0abc123-x1y2z3.bedrock-mantle.eu-west-2.vpce.amazonaws.com/v1/chat/completions",
		mantleOpenAIURL(endpoints, "eu-west-2", "gpt-oss-120b", "chat/completions"))
	assert.Equal(t,
		"https://vpce-0abc123-x1y2z3.bedrock-mantle.eu-west-2.vpce.amazonaws.com/openai/v1/responses",
		mantleOpenAIURL(endpoints, "eu-west-2", "gpt-5", "responses"))
}

// A VPC endpoint host must sign correctly: SigV4 takes the host from the request URI but the
// credential scope from the configured region, so the two stay independent.
func TestSignAWSRequestWithVPCEndpointHost(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctx.Cancel()

	keyCfg := &schemas.BedrockKeyConfig{
		AccessKey: schemas.SecretVar{Val: "AKIAIOSFODNN7EXAMPLE"},
		SecretKey: schemas.SecretVar{Val: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"},
	}

	const vpceHost = "vpce-0abc123-x1y2z3.bedrock-runtime.eu-west-2.vpce.amazonaws.com"
	sign := func(host string) string {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+host+"/model/anthropic.claude-v2/converse", strings.NewReader(`{}`))
		require.NoError(t, err)
		require.Nil(t, signAWSRequest(ctx, req, keyCfg, "eu-west-2", bedrockSigningService))
		return req.Header.Get("Authorization")
	}

	auth := sign(vpceHost)
	require.NotEmpty(t, auth)

	// The credential scope follows the configured region and service, not the hostname — a
	// vpce- host still signs as bedrock/eu-west-2.
	assert.Contains(t, auth, "/eu-west-2/bedrock/aws4_request")
	// Host must be part of SignedHeaders, otherwise AWS rejects the request.
	assert.Contains(t, auth, "SignedHeaders=")
	assert.Contains(t, auth, "host")

	// Signing the same request against the public host must produce a different signature,
	// proving the endpoint host is actually covered by the canonical request.
	assert.NotEqual(t, auth, sign("bedrock-runtime.eu-west-2.amazonaws.com"))
}
