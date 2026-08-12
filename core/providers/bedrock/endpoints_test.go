package bedrock

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestProviderWithBaseURL(t *testing.T, baseURL string) *BedrockProvider {
	t.Helper()
	config := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        baseURL,
			DefaultRequestTimeoutInSeconds: 5,
			InsecureSkipVerify:             true,
		},
	}
	provider, err := NewBedrockProvider(config, noopLogger{})
	require.NoError(t, err)
	return provider
}

func keyWithEndpoints(endpoints *schemas.BedrockEndpoints) schemas.Key {
	return schemas.Key{BedrockKeyConfig: &schemas.BedrockKeyConfig{
		Region:    schemas.NewSecretVar("us-east-1"),
		Endpoints: endpoints,
	}}
}

func clearEndpointEnv(t *testing.T) {
	t.Helper()
	for _, name := range endpointEnvVars {
		t.Setenv(name, "")
	}
	t.Setenv("AWS_ENDPOINT_URL", "")
	t.Setenv("AWS_IGNORE_CONFIGURED_ENDPOINT_URLS", "")
}

func TestEndpointBaseResolution(t *testing.T) {
	provider := &BedrockProvider{networkConfig: schemas.NetworkConfig{BaseURL: "https://runtime.example/prefix"}}
	dnsKey := keyWithEndpoints(&schemas.BedrockEndpoints{DNSSuffix: " .c2s.ic.gov. "})
	overrideKey := keyWithEndpoints(&schemas.BedrockEndpoints{
		Runtime: schemas.NewSecretVar("https://vpce.example/path"),
	})

	assert.Equal(t, "https://runtime.example/prefix", provider.endpointBase(schemas.Key{}, bedrockServiceRuntime, "us-east-1"))
	assert.Equal(t, "https://bedrock.us-east-1.amazonaws.com", provider.endpointBase(schemas.Key{}, bedrockServiceControlPlane, "us-east-1"))
	assert.Equal(t, "https://bedrock-runtime.us-iso-east-1.c2s.ic.gov", (&BedrockProvider{}).endpointBase(dnsKey, bedrockServiceRuntime, "us-iso-east-1"))
	assert.Equal(t, "https://vpce.example", provider.endpointBase(overrideKey, bedrockServiceRuntime, "us-east-1"))
}

func TestS3BucketBase(t *testing.T) {
	provider := &BedrockProvider{}
	overrideKey := keyWithEndpoints(&schemas.BedrockEndpoints{S3: schemas.NewSecretVar("vpce-s3.example")})
	dnsKey := keyWithEndpoints(&schemas.BedrockEndpoints{DNSSuffix: "c2s.ic.gov"})

	assert.Equal(t, "https://bucket.s3.us-east-1.amazonaws.com", provider.s3BucketBase(schemas.Key{}, "us-east-1", "bucket"))
	assert.Equal(t, "https://vpce-s3.example/bucket", provider.s3BucketBase(overrideKey, "us-east-1", "bucket"))
	assert.Equal(t, "https://bucket.s3.us-iso-east-1.c2s.ic.gov", provider.s3BucketBase(dnsKey, "us-iso-east-1", "bucket"))
}

func TestEndpointEnvOverrides(t *testing.T) {
	clearEndpointEnv(t)
	t.Setenv("AWS_ENDPOINT_URL", "https://global.example/")
	t.Setenv("AWS_ENDPOINT_URL_BEDROCK", "https://control.example")
	provider := newTestProviderWithBaseURL(t, "")

	assert.Equal(t, "https://global.example", provider.endpointBase(schemas.Key{}, bedrockServiceRuntime, "us-east-1"))
	assert.Equal(t, "https://control.example", provider.endpointBase(schemas.Key{}, bedrockServiceControlPlane, "us-east-1"))
	assert.Equal(t, "https://global.example/bucket", provider.s3BucketBase(schemas.Key{}, "us-east-1", "bucket"))
}

func TestEndpointEnvOverridesIgnored(t *testing.T) {
	clearEndpointEnv(t)
	t.Setenv("AWS_ENDPOINT_URL", "https://global.example")
	t.Setenv("AWS_IGNORE_CONFIGURED_ENDPOINT_URLS", "TRUE")
	config := &schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{DefaultRequestTimeoutInSeconds: 5}}
	provider, err := NewBedrockProvider(config, noopLogger{})
	require.NoError(t, err)
	assert.Equal(t, "https://bedrock-runtime.us-east-1.amazonaws.com", provider.endpointBase(schemas.Key{}, bedrockServiceRuntime, "us-east-1"))
}

func TestNewBedrockProviderRejectsInvalidBaseURL(t *testing.T) {
	for _, baseURL := range []string{"not a url", "ftp://host", "//host", "https://", "https://host?x=1", "https://host#frag"} {
		t.Run(baseURL, func(t *testing.T) {
			config := &schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: baseURL}}
			provider, err := NewBedrockProvider(config, noopLogger{})
			assert.Error(t, err)
			assert.Nil(t, provider)
		})
	}
}
