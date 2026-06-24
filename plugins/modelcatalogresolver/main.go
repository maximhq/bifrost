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
	provider, model, existingFallbacks := req.GetRequestFields()
	if provider != "" || model == "" {
		return nil
	}

	selected, candidates := ResolveProviderFromCatalog(ctx, p.catalog, model)
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

	// Populate fallbacks from the remaining catalog candidates so the request gets
	// cross-provider resilience automatically — matches the governance and load
	// balancing plugins, which both promote unselected candidates to fallbacks
	// when the caller didn't configure any. Only fires when the caller passed
	// none; an explicit fallback list (even an empty one set deliberately) is
	// always respected. Model refinement is not needed here: GetProvidersForModel
	// only returns providers that already serve this exact model string.
	if len(existingFallbacks) == 0 && len(candidates) > 1 {
		fallbacks := make([]schemas.Fallback, 0, len(candidates)-1)
		for _, prov := range candidates {
			if prov == selected {
				continue
			}
			fallbacks = append(fallbacks, schemas.Fallback{Provider: prov, Model: model})
		}
		if len(fallbacks) > 0 {
			req.SetFallbacks(fallbacks)
			fallbackStrs := make([]string, len(fallbacks))
			for i, fb := range fallbacks {
				fallbackStrs[i] = string(fb.Provider)
			}
			ctx.AppendRoutingEngineLog(schemas.RoutingEngineModelCatalog, schemas.LogLevelInfo, fmt.Sprintf(
				"Added %d catalog fallback provider(s) for model %s: [%s]",
				len(fallbacks), model, strings.Join(fallbackStrs, ", "),
			))
		}
	}

	schemas.AppendToContextList(ctx, schemas.BifrostContextKeyRoutingEnginesUsed, schemas.RoutingEngineModelCatalog)
	return nil
}

// ResolveProviderFromCatalog performs the deterministic, integration-aware provider pick
// that PreRequestHook does, exposed for transport paths that can't run through
// PreRequestHook (realtime client_secrets, WebRTC). Returns the selected provider plus the
// candidate list (post-allowlist when an allowlist is in effect). Returns ("", nil) when
// the catalog has no match for the model, or when an allowlist excludes every candidate.
//
// The integration hint (BifrostContextKeyIntegrationType, when present and mapped) biases
// the pick toward the integration's canonical provider if it is in the candidate set;
// otherwise selection falls back to the alphabetically-first candidate for determinism.
//
// For requests routed through the openai integration whose user-agent identifies an Azure
// OpenAI SDK (BifrostContextKeyIsAzureUserAgent), schemas.Azure is preferred over
// schemas.OpenAI when Azure is in the candidate list — the openai-format converters no
// longer apply this default inline.
//
// When BifrostContextKeyRoutingAllowedProviders is set on ctx by an earlier plugin (e.g.,
// governance VK config), the candidate list is intersected with the allowlist before
// selection — emitting routing-engine logs visible to callers when the allowlist prunes
// candidates. Side effect: routing-engine logs are written to ctx when allowlist filtering
// is applied (nil ctx skips logging).
func ResolveProviderFromCatalog(ctx *schemas.BifrostContext, catalog *modelcatalog.ModelCatalog, model string) (schemas.ModelProvider, []schemas.ModelProvider) {
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

	var integrationType string
	var isAzureUser bool
	var allowed []schemas.ModelProvider
	allowlistSet := false
	if ctx != nil {
		integrationType, _ = ctx.Value(schemas.BifrostContextKeyIntegrationType).(string)
		isAzureUser, _ = ctx.Value(schemas.BifrostContextKeyIsAzureUserAgent).(bool)
		allowed, allowlistSet = ctx.Value(schemas.BifrostContextKeyRoutingAllowedProviders).([]schemas.ModelProvider)
	}

	// Respect the routing-allowlist set by an earlier plugin (e.g., governance VK config):
	// intersect catalog candidates with the allowlist so the VK's provider restrictions hold
	// even when no earlier routing plugin set req.Provider. Emit observability logs for both
	// the partial-prune and all-pruned cases — the two-level enforcement (cooperative here +
	// hard core enforcement) is only useful if the cooperative pruning is visible in routing
	// engine logs when it fires.
	if allowlistSet {
		preFilterCount := len(providers)
		preFilterStrs := make([]string, preFilterCount)
		for i, prov := range providers {
			preFilterStrs[i] = string(prov)
		}
		allowedStrs := make([]string, len(allowed))
		for i, prov := range allowed {
			allowedStrs[i] = string(prov)
		}
		filtered := make([]schemas.ModelProvider, 0, preFilterCount)
		excluded := make([]schemas.ModelProvider, 0)
		for _, prov := range providers {
			if slices.Contains(allowed, prov) {
				filtered = append(filtered, prov)
			} else {
				excluded = append(excluded, prov)
			}
		}
		if len(excluded) > 0 && ctx != nil {
			filteredStrs := make([]string, len(filtered))
			for i, prov := range filtered {
				filteredStrs[i] = string(prov)
			}
			ctx.AppendRoutingEngineLog(schemas.RoutingEngineModelCatalog, schemas.LogLevelInfo, fmt.Sprintf(
				"Catalog returned %d candidate provider(s) for model %s: [%s]; provider allowlist is [%s], so excluded %d; remaining providers are [%s]",
				preFilterCount, model, strings.Join(preFilterStrs, ", "),
				strings.Join(allowedStrs, ", "),
				len(excluded),
				strings.Join(filteredStrs, ", "),
			))
		}
		providers = filtered
		if len(providers) == 0 {
			if ctx != nil {
				ctx.AppendRoutingEngineLog(schemas.RoutingEngineModelCatalog, schemas.LogLevelInfo, fmt.Sprintf(
					"Catalog returned %d candidate provider(s) for model %s: [%s]; provider allowlist [%s] excluded all of them; leaving req.Provider empty",
					preFilterCount, model, strings.Join(preFilterStrs, ", "),
					strings.Join(allowedStrs, ", "),
				))
			}
			return "", nil
		}
	}

	selected := providers[0]
	if integrationType != "" {
		if integrationDefault, mapped := integrationTypeToDefaultProvider[integrationType]; mapped && integrationDefault != "" {
			preferred := integrationDefault
			if integrationType == "openai" && isAzureUser {
				preferred = schemas.Azure
			}
			if slices.Contains(providers, preferred) {
				selected = preferred
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
