package azure

import (
	"strings"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/schemas"
)

// getRequestBodyForAnthropicResponses serializes a BifrostResponsesRequest into the Anthropic wire format for Azure.
// It delegates to BuildAnthropicResponsesRequestBody with the Azure provider and the target deployment name.
func getRequestBodyForAnthropicResponses(ctx *schemas.BifrostContext, request *schemas.BifrostResponsesRequest, deployment string, isStreaming bool, shouldSendBackRawRequest bool, shouldSendBackRawResponse bool) ([]byte, *schemas.BifrostError) {
	return anthropic.BuildAnthropicResponsesRequestBody(ctx, request, anthropic.AnthropicRequestBuildConfig{
		Provider:                  schemas.Azure,
		Deployment:                deployment,
		IsStreaming:               isStreaming,
		ValidateTools:             true,
		ShouldSendBackRawRequest:  shouldSendBackRawRequest,
		ShouldSendBackRawResponse: shouldSendBackRawResponse,
	})
}

// getAzureScopes returns the configured scopes or the default scope if none are valid.
// It filters out empty/whitespace-only strings.
func getAzureScopes(configuredScopes []string) []string {
	scopes := []string{DefaultAzureScope}
	if len(configuredScopes) > 0 {
		cleaned := make([]string, 0, len(configuredScopes))
		for _, s := range configuredScopes {
			if strings.TrimSpace(s) != "" {
				cleaned = append(cleaned, strings.TrimSpace(s))
			}
		}
		if len(cleaned) > 0 {
			scopes = cleaned
		}
	}
	return scopes
}

// resolveAnthropicVersion returns the anthropic-version header value for the
// current attempt. Uses the AzureAliasCfg.AnthropicVersion override from the
// resolved alias when present, otherwise the Azure default.
func resolveAnthropicVersion(ctx *schemas.BifrostContext) string {
	if ra := schemas.GetResolvedAlias(ctx); ra != nil && ra.Config != nil && ra.Config.AzureAliasCfg != nil && ra.Config.AzureAliasCfg.AnthropicVersion != nil && *ra.Config.AzureAliasCfg.AnthropicVersion != "" {
		return *ra.Config.AzureAliasCfg.AnthropicVersion
	}
	return AzureAnthropicAPIVersionDefault
}

// resolveAPIVersion returns the Azure api-version query parameter value for
// the current attempt. Uses the AzureAliasCfg.APIVersion override from the
// resolved alias when present, otherwise the provided default. Different
// Azure routes have different defaults (DefaultAzureAPIVersion for classic
// /openai/deployments/, AzureAPIVersionPreview for /openai/v1/responses);
// callers pass the route's default so the override can take precedence
// without losing the route-specific fallback.
func resolveAPIVersion(ctx *schemas.BifrostContext, defaultVersion string) string {
	if ra := schemas.GetResolvedAlias(ctx); ra != nil && ra.Config != nil && ra.Config.AzureAliasCfg != nil && ra.Config.AzureAliasCfg.APIVersion != nil && *ra.Config.AzureAliasCfg.APIVersion != "" {
		return *ra.Config.AzureAliasCfg.APIVersion
	}
	return defaultVersion
}

// resolveAzureEndpoint returns the Azure cognitive-services endpoint URL for
// the current attempt. Uses the AzureAliasCfg.Endpoint override from the
// resolved alias when present, otherwise the key-level endpoint. Lets one
// Azure credential (ClientID/Secret/TenantID or API key) span deployments
// hosted on different Azure resources (e.g. OpenAI on east-us, Anthropic on
// west-us2).
func resolveAzureEndpoint(ctx *schemas.BifrostContext, key schemas.Key) string {
	if ra := schemas.GetResolvedAlias(ctx); ra != nil && ra.Config != nil && ra.Config.AzureAliasCfg != nil && ra.Config.AzureAliasCfg.Endpoint != nil {
		if v := ra.Config.AzureAliasCfg.Endpoint.GetValue(); v != "" {
			return v
		}
	}
	if key.AzureKeyConfig != nil {
		return key.AzureKeyConfig.Endpoint.GetValue()
	}
	return ""
}
