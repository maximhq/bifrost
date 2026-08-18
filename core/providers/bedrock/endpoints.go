package bedrock

import (
	"fmt"
	"os"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

const defaultBedrockDNSSuffix = "amazonaws.com"

var endpointEnvVars = map[bedrockService]string{
	bedrockServiceRuntime:      "AWS_ENDPOINT_URL_BEDROCK_RUNTIME",
	bedrockServiceControlPlane: "AWS_ENDPOINT_URL_BEDROCK",
	bedrockServiceAgentRuntime: "AWS_ENDPOINT_URL_BEDROCK_AGENT_RUNTIME",
	bedrockServiceS3:           "AWS_ENDPOINT_URL_S3",
	bedrockServiceMantle:       "AWS_ENDPOINT_URL_BEDROCK_MANTLE",
}

func loadEndpointEnvOverrides() map[bedrockService]string {
	out := make(map[bedrockService]string, len(endpointEnvVars))
	if strings.EqualFold(os.Getenv("AWS_IGNORE_CONFIGURED_ENDPOINT_URLS"), "true") {
		return out
	}
	global := normalizeEndpointBase(os.Getenv("AWS_ENDPOINT_URL"))
	for service, name := range endpointEnvVars {
		if value := normalizeEndpointBase(os.Getenv(name)); value != "" {
			out[service] = value
		} else if global != "" {
			out[service] = global
		}
	}
	return out
}

func normalizeEndpointBase(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

func keyEndpointOverride(key schemas.Key, service bedrockService) string {
	endpoints := bedrockEndpoints(key.BedrockKeyConfig)
	if endpoints == nil {
		return ""
	}
	var endpoint *schemas.SecretVar
	switch service {
	case bedrockServiceRuntime:
		endpoint = endpoints.Runtime
	case bedrockServiceControlPlane:
		endpoint = endpoints.ControlPlane
	case bedrockServiceAgentRuntime:
		endpoint = endpoints.AgentRuntime
	case bedrockServiceS3:
		endpoint = endpoints.S3
	case bedrockServiceMantle:
		endpoint = endpoints.Mantle
	}
	if host := schemas.NormalizeEndpointHost(endpoint); host != "" {
		return "https://" + host
	}
	return ""
}

func dnsSuffixOverride(key schemas.Key) string {
	if endpoints := bedrockEndpoints(key.BedrockKeyConfig); endpoints != nil {
		if suffix := strings.Trim(strings.TrimSpace(endpoints.DNSSuffix), "."); suffix != "" {
			return suffix
		}
	}
	return defaultBedrockDNSSuffix
}

func (provider *BedrockProvider) endpointBase(key schemas.Key, service bedrockService, region string) string {
	if value := keyEndpointOverride(key, service); value != "" {
		return value
	}
	if service == bedrockServiceRuntime && provider.networkConfig.BaseURL != "" {
		return provider.networkConfig.BaseURL
	}
	if value := provider.envEndpoints[service]; value != "" {
		return value
	}
	if service == bedrockServiceMantle {
		return fmt.Sprintf("https://%s.%s.api.aws", service, region)
	}
	if service == bedrockServiceS3 {
		panic("bedrock: endpointBase called with S3 service; use s3BucketBase")
	}
	return fmt.Sprintf("https://%s.%s.%s", service, region, dnsSuffixOverride(key))
}

func (provider *BedrockProvider) s3BucketBase(key schemas.Key, region, bucket string) string {
	if value := provider.s3EndpointOverride(key); value != "" {
		return value + "/" + bucket
	}
	return fmt.Sprintf("https://%s.s3.%s.%s", bucket, region, dnsSuffixOverride(key))
}

func (provider *BedrockProvider) s3EndpointOverride(key schemas.Key) string {
	if value := keyEndpointOverride(key, bedrockServiceS3); value != "" {
		return value
	}
	return provider.envEndpoints[bedrockServiceS3]
}
