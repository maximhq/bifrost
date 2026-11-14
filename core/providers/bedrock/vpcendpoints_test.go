package bedrock

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
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

// newBaseURLTestProvider builds a Bedrock provider whose network_config.base_url points at a
// local TLS server, and returns the paths that server received.
func newBaseURLTestProvider(t *testing.T) (*BedrockProvider, string, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.EscapedPath())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output":{"message":{"role":"assistant","content":[{"text":"ok"}]}},"stopReason":"end_turn","usage":{"inputTokens":1,"outputTokens":1,"totalTokens":2}}`))
	}))
	t.Cleanup(srv.Close)

	provider, err := NewBedrockProvider(&schemas.ProviderConfig{
		NetworkConfig:            schemas.NetworkConfig{BaseURL: srv.URL + "/", DefaultRequestTimeoutInSeconds: 10, InsecureSkipVerify: true},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{Concurrency: 1, BufferSize: 1},
	}, noopLogger{})
	require.NoError(t, err)
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	return provider, u.Host, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), paths...)
	}
}

// network_config.base_url is the provider-wide bedrock-runtime host (issue #5272): a Converse
// call must dial it instead of the public bedrock-runtime.<region>.amazonaws.com host.
func TestNetworkConfigBaseURLRoutesConverse(t *testing.T) {
	provider, _, received := newBaseURLTestProvider(t)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctx.Cancel()

	key := schemas.Key{Value: *schemas.NewSecretVar("test-bearer-token"), BedrockKeyConfig: &schemas.BedrockKeyConfig{Region: secret("eu-west-2")}}
	msg := "hi"
	_, bifrostErr := provider.ChatCompletion(ctx, key, &schemas.BifrostChatRequest{
		Provider: schemas.Bedrock,
		Model:    "anthropic.claude-3-haiku-20240307-v1:0",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &msg}}},
	})
	if bifrostErr != nil && bifrostErr.Error != nil {
		t.Logf("chat error: %s", bifrostErr.Error.Message)
	}

	paths := received()
	require.Len(t, paths, 1, "the Converse request must reach network_config.base_url, not the public AWS host")
	assert.True(t, strings.HasPrefix(paths[0], "/model/") && strings.HasSuffix(paths[0], "/converse"), "unexpected path %q", paths[0])
}

// Every bedrock-runtime URL builder honours network_config.base_url, and the per-key
// endpoints.runtime override still wins over it.
func TestNetworkConfigBaseURLRuntimeURLBuilders(t *testing.T) {
	provider, host, _ := newBaseURLTestProvider(t)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctx.Cancel()

	key := schemas.Key{Value: *schemas.NewSecretVar("test-bearer-token"), BedrockKeyConfig: &schemas.BedrockKeyConfig{Region: secret("eu-west-2")}}

	invokeURL, _ := provider.invokeURL(ctx, key, "anthropic.claude-3-haiku-20240307-v1:0", "invoke")
	assert.True(t, strings.HasPrefix(invokeURL, "https://"+host+"/model/"), "InvokeModel URL %q", invokeURL)

	assert.Equal(t, "https://"+host+"/openai/v1/responses", provider.runtimeOpenAIURL(bedrockEndpoints(key.BedrockKeyConfig), "eu-west-2", "responses"))

	req, _, bifrostErr := provider.buildPassthroughRequest(ctx, key, &schemas.BifrostPassthroughRequest{
		Method: http.MethodPost,
		Path:   "/guardrail/abc123/version/1/apply",
		Body:   []byte(`{}`),
	})
	require.Nil(t, bifrostErr)
	assert.Equal(t, host, req.URL.Host, "ApplyGuardrail passthrough targets bedrock-runtime")

	// Non-runtime services keep their public hosts: a single base URL cannot serve them.
	req, _, bifrostErr = provider.buildPassthroughRequest(ctx, schemas.Key{BedrockKeyConfig: &schemas.BedrockKeyConfig{
		Region: secret("eu-west-2"), AccessKey: schemas.SecretVar{Val: "AKIAIOSFODNN7EXAMPLE"}, SecretKey: schemas.SecretVar{Val: "secret"},
	}}, &schemas.BifrostPassthroughRequest{Method: http.MethodPost, Path: "/knowledgebases/KB123/retrieve", Body: []byte(`{}`)})
	require.Nil(t, bifrostErr)
	assert.Equal(t, "bedrock-agent-runtime.eu-west-2.amazonaws.com", req.URL.Host)

	const vpce = "vpce-0abc123-x1y2z3.bedrock-runtime.eu-west-2.vpce.amazonaws.com"
	key.BedrockKeyConfig.Endpoints = &schemas.BedrockEndpoints{Runtime: secret(vpce)}
	invokeURL, _ = provider.invokeURL(ctx, key, "anthropic.claude-3-haiku-20240307-v1:0", "invoke")
	assert.True(t, strings.HasPrefix(invokeURL, "https://"+vpce+"/model/"), "per-key endpoints.runtime must win over base_url, got %q", invokeURL)
}

func TestResolveBedrockHostRuntimeDefaultPrecedence(t *testing.T) {
	const base = "bedrock.internal.example:8443"
	const vpce = "vpce-0abc123-x1y2z3.bedrock-runtime.eu-west-2.vpce.amazonaws.com"
	cases := []struct {
		name      string
		endpoints *schemas.BedrockEndpoints
		service   bedrockService
		want      string
	}{
		{"base_url used for runtime", nil, bedrockServiceRuntime, base},
		{"empty per-key runtime does not shadow base_url", &schemas.BedrockEndpoints{Runtime: secret("")}, bedrockServiceRuntime, base},
		{"per-key runtime wins over base_url", &schemas.BedrockEndpoints{Runtime: secret(vpce)}, bedrockServiceRuntime, vpce},
		{"control plane ignores base_url", nil, bedrockServiceControlPlane, "bedrock.eu-west-2.amazonaws.com"},
		{"agent runtime ignores base_url", nil, bedrockServiceAgentRuntime, "bedrock-agent-runtime.eu-west-2.amazonaws.com"},
		{"s3 ignores base_url", nil, bedrockServiceS3, "s3.eu-west-2.amazonaws.com"},
		{"mantle ignores base_url", nil, bedrockServiceMantle, "bedrock-mantle.eu-west-2.api.aws"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, resolveBedrockHostWithRuntimeDefault(tc.endpoints, tc.service, "eu-west-2", base))
		})
	}
}

func TestNewBedrockProviderNormalizesBaseURL(t *testing.T) {
	cases := map[string]string{
		"":                                       "",
		"https://bedrock.internal.example":       "bedrock.internal.example",
		"https://bedrock.internal.example:8443/": "bedrock.internal.example:8443",
		"bedrock.internal.example/some/path":     "bedrock.internal.example",
	}
	for baseURL, want := range cases {
		provider, err := NewBedrockProvider(&schemas.ProviderConfig{
			NetworkConfig:            schemas.NetworkConfig{BaseURL: baseURL},
			ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{Concurrency: 1, BufferSize: 1},
		}, noopLogger{})
		require.NoError(t, err)
		assert.Equal(t, want, provider.runtimeHost, "base_url %q", baseURL)
	}
}

func BenchmarkBedrockRuntimeHost(b *testing.B) {
	endpoints := &schemas.BedrockEndpoints{}
	b.Run("no_base_url", func(b *testing.B) {
		provider := &BedrockProvider{}
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = provider.serviceHost(endpoints, bedrockServiceRuntime, "eu-west-2")
		}
	})
	b.Run("base_url", func(b *testing.B) {
		provider := &BedrockProvider{runtimeHost: "bedrock.internal.example"}
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = provider.serviceHost(endpoints, bedrockServiceRuntime, "eu-west-2")
		}
	})
}
