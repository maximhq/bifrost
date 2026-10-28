package sagemaker

import (
	"strings"

	"github.com/maximhq/bifrost/core/providers/bedrock"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// isResourceName reports whether s is a valid SageMaker endpoint or inference-component name:
// ^[a-zA-Z0-9](-*[a-zA-Z0-9]){0,62}$, i.e. at most 63 characters, alphanumeric at both ends.
func isResourceName(s string) bool {
	if len(s) == 0 || len(s) > 63 || s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '-' && (c < '0' || c > '9') && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return false
		}
	}
	return true
}

// parseModelID splits a model id into its optional region prefix, endpoint and optional
// inference component. "/" cannot appear in SageMaker resource names, so it is unambiguous.
func parseModelID(model string) (modelID, *schemas.BifrostError) {
	var id modelID
	rest := model
	if i := strings.IndexByte(rest, '/'); i > 0 && bedrock.AWSRegionRegex.MatchString(rest[:i]) {
		id.region, rest = rest[:i], rest[i+1:]
	}
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		id.endpoint, id.ic = rest[:i], rest[i+1:]
		if !isResourceName(id.ic) {
			return modelID{}, invalidModelIDError(model)
		}
	} else {
		id.endpoint = rest
	}
	if !isResourceName(id.endpoint) {
		return modelID{}, invalidModelIDError(model)
	}
	return id, nil
}

func invalidModelIDError(model string) *schemas.BifrostError {
	return providerUtils.NewConfigurationError("invalid sagemaker model id " + `"` + model + `"` + ": expected [region/]endpoint-name[/inference-component-name]")
}

// resolveRegion picks the region for a request: model-id prefix > alias Region > key Region.
// SageMaker endpoints are regional resources, so there is no default.
func resolveRegion(ctx *schemas.BifrostContext, key schemas.Key, id modelID) (string, *schemas.BifrostError) {
	if id.region != "" {
		return id.region, nil
	}
	if ra := schemas.GetResolvedAlias(ctx); ra != nil && ra.Config != nil && ra.Config.Region != nil {
		if v := ra.Config.Region.GetValue(); v != "" {
			return v, nil
		}
	}
	if cfg := key.SageMakerKeyConfig; cfg != nil && cfg.Region != nil {
		if v := cfg.Region.GetValue(); v != "" {
			return v, nil
		}
	}
	return "", providerUtils.NewConfigurationError("sagemaker_key_config.region is required (or prefix the model id with a region)")
}

// runtimeHost returns the runtime host: the configured override, else the regional public host.
func runtimeHost(cfg *schemas.SageMakerKeyConfig, region string) string {
	if cfg != nil && cfg.Endpoints != nil {
		if host := schemas.NormalizeEndpointHost(cfg.Endpoints.Runtime); host != "" {
			return host
		}
	}
	suffix := "amazonaws.com"
	if strings.HasPrefix(region, "cn-") {
		suffix = "amazonaws.com.cn"
	}
	return "runtime.sagemaker." + region + "." + suffix
}

// openAIURL builds https://{host}/endpoints/{endpoint}[/inference-components/{ic}]/openai/v1/{path}.
func openAIURL(host string, id modelID, path string) string {
	var b strings.Builder
	b.Grow(len("https:///endpoints//inference-components//openai/v1/") + len(host) + len(id.endpoint) + len(id.ic) + len(path))
	b.WriteString("https://")
	b.WriteString(host)
	b.WriteString("/endpoints/")
	b.WriteString(id.endpoint)
	if id.ic != "" {
		b.WriteString("/inference-components/")
		b.WriteString(id.ic)
	}
	b.WriteString("/openai/v1/")
	b.WriteString(path)
	return b.String()
}

// withBodyModel returns a shallow copy of request with an empty body "model" (SageMaker routes by
// URL; vLLM rejects an endpoint name but accepts an empty model), leaving the caller's
// request.Model (the endpoint id) intact for reuse, e.g. multi-turn loops in the Go SDK.
func withBodyModel(request *schemas.BifrostChatRequest) *schemas.BifrostChatRequest {
	body := *request
	body.Model = ""
	return &body
}

// bedrockKeyConfig maps the SageMaker credential fields onto the Bedrock config the shared
// AWS credential resolver takes.
func bedrockKeyConfig(cfg *schemas.SageMakerKeyConfig) *schemas.BedrockKeyConfig {
	if cfg == nil {
		return nil
	}
	return &schemas.BedrockKeyConfig{
		AccessKey:       cfg.AccessKey,
		SecretKey:       cfg.SecretKey,
		SessionToken:    cfg.SessionToken,
		RoleARN:         cfg.RoleARN,
		ExternalID:      cfg.ExternalID,
		RoleSessionName: cfg.RoleSessionName,
	}
}
