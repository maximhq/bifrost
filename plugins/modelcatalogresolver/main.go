// Package modelcatalogresolver provides a built-in PreRequestHook plugin that resolves
// the default provider for an unprefixed model via the model catalog. It is the single
// owner of "if no provider specified, look up which providers serve this model" — the
// transport handlers, integrations router, and realtime handlers no longer do this
// inline. Governance/LB plugins run before this resolver; it only fires as a final
// fallback when no earlier routing plugin picked a provider.
package modelcatalogresolver

import (
	"fmt"
	"slices"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

const PluginName = "model-catalog-resolver"

// integrationTypeToDefaultProvider maps the integration-type ctx value (set by
// transports/bifrost-http/integrations/router.go on integration routes) to the
// integration's canonical provider. When the catalog returns multiple providers
// for an unprefixed model, the resolver prefers the integration's canonical
// provider if it's in the candidate list.
var integrationTypeToDefaultProvider = map[string]schemas.ModelProvider{
	"openai":    schemas.OpenAI,
	"anthropic": schemas.Anthropic,
	"genai":     schemas.Gemini,
	"bedrock":   schemas.Bedrock,
	"cohere":    schemas.Cohere,
}

// Plugin resolves the default provider for unprefixed model strings using the model catalog.
type Plugin struct {
	catalog *modelcatalog.ModelCatalog
	logger  schemas.Logger
}

// Init returns a new resolver plugin. The catalog is required; if nil, the plugin returns
// an error rather than silently no-op'ing — a nil catalog at boot is a misconfiguration.
func Init(catalog *modelcatalog.ModelCatalog, logger schemas.Logger) (*Plugin, error) {
	if catalog == nil {
		return nil, fmt.Errorf("model-catalog-resolver: catalog is required")
	}
	return &Plugin{catalog: catalog, logger: logger}, nil
}

// GetName implements schemas.BasePlugin.
func (p *Plugin) GetName() string { return PluginName }

// Cleanup implements schemas.BasePlugin.
func (p *Plugin) Cleanup() error { return nil }

// PreRequestHook fills in req.Provider from the model catalog when no provider was specified.
// Skips passthrough requests and requests that already have a provider set (e.g., from a model
// string like "openai/gpt-5", or from an earlier routing plugin — governance, LB).
//
// When the catalog returns multiple providers for an unprefixed model, the resolver prefers the
// integration's canonical provider (looked up from BifrostContextKeyIntegrationType set by the
// integration router) if it's in the candidate list. Otherwise it picks the first candidate.
//
// If the catalog returns zero providers, the resolver leaves req.Provider empty — the
// empty-provider validation in handleRequest/handleStreamRequest then returns a clear error.
func (p *Plugin) PreRequestHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) error {
	if req.RequestType == schemas.PassthroughRequest || req.RequestType == schemas.PassthroughStreamRequest {
		return nil
	}
	provider, model, _ := req.GetRequestFields()
	if provider != "" || model == "" {
		return nil
	}

	integrationType, _ := ctx.Value(schemas.BifrostContextKeyIntegrationType).(string)
	selected, candidates := ResolveProviderFromCatalog(p.catalog, model, integrationType)
	if selected == "" {
		return nil
	}
	req.SetProvider(selected)

	candidateStrs := make([]string, len(candidates))
	for i, prov := range candidates {
		candidateStrs[i] = string(prov)
	}
	ctx.AppendRoutingEngineLog(schemas.RoutingEngineModelCatalog, schemas.LogLevelInfo, fmt.Sprintf(
		"No provider specified for model %s, found %d options in model catalog: [%s], selected: %s",
		model, len(candidates), strings.Join(candidateStrs, ", "), selected,
	))
	schemas.AppendToContextList(ctx, schemas.BifrostContextKeyRoutingEnginesUsed, schemas.RoutingEngineModelCatalog)
	return nil
}

// ResolveProviderFromCatalog performs the deterministic, integration-aware provider pick
// that PreRequestHook does, exposed for transport paths that can't run through
// PreRequestHook (realtime client_secrets, WebRTC). Returns the selected provider plus the
// full ordered candidate list. Returns ("", nil) when the catalog has no match for the model.
//
// integrationType (when non-empty and present in integrationTypeToDefaultProvider) biases
// the pick toward the integration's canonical provider if it is in the candidate set;
// otherwise selection falls back to the alphabetically-first candidate for determinism.
func ResolveProviderFromCatalog(catalog *modelcatalog.ModelCatalog, model, integrationType string) (schemas.ModelProvider, []schemas.ModelProvider) {
	if catalog == nil || model == "" {
		return "", nil
	}
	providers := catalog.GetProvidersForModel(model)
	if len(providers) == 0 {
		return "", nil
	}

	// GetProvidersForModel iterates a Go map; the returned order is not stable.
	// Sort alphabetically so the fallback pick (providers[0]) is deterministic across
	// restarts and across processes — critical when no IntegrationType hint is set.
	slices.SortFunc(providers, func(a, b schemas.ModelProvider) int {
		return strings.Compare(string(a), string(b))
	})

	selected := providers[0]
	if integrationType != "" {
		if integrationDefault, mapped := integrationTypeToDefaultProvider[integrationType]; mapped && integrationDefault != "" {
			if slices.Contains(providers, integrationDefault) {
				selected = integrationDefault
			}
		}
	}
	return selected, providers
}

// PreLLMHook implements schemas.LLMPlugin (no-op).
func (p *Plugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	return req, nil, nil
}

// PostLLMHook implements schemas.LLMPlugin (no-op).
func (p *Plugin) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, bifrostErr, nil
}
