package azure

import (
	"bytes"
	"strings"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

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

// resolvePassthroughAlias rewrites a passthrough path and body so the
// user-facing alias name is replaced by the wire deployment (alias model_id)
// resolved for this attempt's key. Azure resolves the deployment from either
// the /openai/deployments/{name} path segment or the top-level "model" body
// field, so both are rewritten. No-op when no alias matched.
func resolvePassthroughAlias(ctx *schemas.BifrostContext, path string, body []byte) (string, []byte) {
	ra := schemas.GetResolvedAlias(ctx)
	if ra == nil || ra.Config == nil || ra.Key == "" || ra.Config.ModelID == "" || ra.Config.ModelID == ra.Key {
		return path, body
	}
	return rewriteDeploymentSegment(path, ra.Key, ra.Config.ModelID),
		rewriteBodyModel(body, ra.Key, ra.Config.ModelID)
}

// rewriteDeploymentSegment replaces the path segment after "/deployments/"
// when it equals the alias (case-insensitive, matching KeyAliases.ResolveConfig).
func rewriteDeploymentSegment(path, alias, modelID string) string {
	const seg = "/deployments/"
	i := strings.Index(path, seg)
	if i == -1 {
		return path
	}
	name := path[i+len(seg):]
	tail := ""
	if j := strings.IndexByte(name, '/'); j != -1 {
		name, tail = name[:j], name[j:]
	}
	if !strings.EqualFold(name, alias) {
		return path
	}
	return path[:i+len(seg)] + modelID + tail
}

// rewriteBodyModel replaces the top-level "model" JSON field when it equals
// the alias (case-insensitive). Non-JSON and modelless bodies pass through
// untouched. ponytail: top-level JSON only — multipart passthrough routes
// carry the deployment in the path, not the body.
func rewriteBodyModel(body []byte, alias, modelID string) []byte {
	// JSON objects only: gjson/sjson do not validate, and a multipart body
	// carrying a "model" form field would otherwise get corrupted.
	if trimmed := bytes.TrimLeft(body, " \t\r\n"); len(trimmed) == 0 || trimmed[0] != '{' {
		return body
	}
	current := gjson.GetBytes(body, "model")
	if current.Type != gjson.String || !strings.EqualFold(current.Str, alias) {
		return body
	}
	out, err := sjson.SetBytes(body, "model", modelID)
	if err != nil {
		return body
	}
	return out
}
