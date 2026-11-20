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
	"azure":     schemas.Azure,
	"typesafe":  schemas.Typesafe,
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
// integration router) if it's in the candidate list. Otherwise it picks the first candidate, except
// for a decision request, which it leaves unresolved with BifrostContextKeyProviderResolutionError
// set so core answers 400 naming the candidates.
//
// If the catalog returns zero providers, the resolver leaves req.Provider empty — the
// empty-provider validation in handleRequest/handleStreamRequest then returns a clear error.
// Exception: a key-selection-bypass request (BifrostContextKeySkipKeySelection) that the catalog
// cannot resolve is pinned to the integration's canonical provider instead — see
// ResolveProviderFromCatalog for why a zero-key OAuth-passthrough provider needs this.
func (p *Plugin) PreRequestHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) error {
	if req.RequestType == schemas.PassthroughRequest || req.RequestType == schemas.PassthroughStreamRequest {
		return nil
	}
	provider, model, existingFallbacks := req.GetRequestFields()
	if provider != "" || model == "" {
		return nil
	}

	selected, candidates, pinned := resolveProvider(ctx, p.catalog, model)
	if selected == "" {
		// ResolveProviderFromCatalog already logged *why* it could not pick
		// (empty catalog vs allowlist pruned everything). This records the
		// consequence: no later plugin runs, so the request is now guaranteed to
		// fail the empty-provider validation in handleRequest. Warn rather than
		// Info because unlike every other branch here, this one ends in an error.
		ctx.AppendRoutingEngineLog(schemas.RoutingEngineModelCatalog, schemas.LogLevelWarn, fmt.Sprintf(
			"No provider could be resolved for model %s; request will fail provider validation", model,
		))
		return nil
	}

	candidateStrs := make([]string, len(candidates))
	for i, prov := range candidates {
		candidateStrs[i] = string(prov)
	}

	// The same decision model id can name a different model on each provider that serves it, and a
	// caller-fault error on the alphabetical pick never reaches the inferred fallbacks. Unless the route the request came in on names its provider, a decision
	// request is left unresolved with a reason core returns as the 400, rather than sent to one of
	// the candidates the caller did not choose. A routing rule that sets the provider runs before
	// this resolver, so it still serves as an alias for the bare id.
	if req.RequestType == schemas.DecisionRequest && len(candidates) > 1 && integrationPreferredProvider(ctx, candidates) == "" {
		reason := fmt.Sprintf(
			"decision model %s is served by more than one provider (%s); send it as provider/model, for example %s/%s, or add a routing rule that sets the provider",
			model, strings.Join(candidateStrs, ", "), candidates[0], model,
		)
		ctx.SetValue(schemas.BifrostContextKeyProviderResolutionError, reason)
		ctx.AppendRoutingEngineLog(schemas.RoutingEngineModelCatalog, schemas.LogLevelWarn, fmt.Sprintf(
			"No provider specified for decision model %s and the model catalog has %d options: [%s]; leaving req.Provider empty",
			model, len(candidates), strings.Join(candidateStrs, ", "),
		))
		return nil
	}
	req.SetProvider(selected)

	// The catalog had no usable provider and key selection is bypassed: the selection is the
	// integration's own provider, not a catalog option, and it gets no catalog fallbacks.
	if pinned {
		ctx.AppendRoutingEngineLog(schemas.RoutingEngineModelCatalog, schemas.LogLevelInfo, fmt.Sprintf(
			"No provider specified for model %s and the model catalog could not resolve one; key selection is bypassed, so pinned to the integration's provider %s",
			model, selected,
		))
		schemas.AppendToContextList(ctx, schemas.BifrostContextKeyRoutingEnginesUsed, schemas.RoutingEngineModelCatalog)
		return nil
	}

	ctx.AppendRoutingEngineLog(schemas.RoutingEngineModelCatalog, schemas.LogLevelInfo, fmt.Sprintf(
		"No provider specified for model %s, found %d options in model catalog: [%s], selected: %s",
		model, len(candidates), strings.Join(candidateStrs, ", "), selected,
	))

	// Populate fallbacks from the remaining catalog candidates so the request gets
	// cross-provider resilience automatically — matches the governance and load
	// balancing plugins, which both promote unselected candidates to fallbacks
	// when the caller didn't configure any. Model refinement is not needed here:
	// GetProvidersForModel only returns providers that already serve this exact
	// model string. They are marked Inferred, so core does not let a provider the
	// caller never named answer a request the primary refused as malformed.
	if len(existingFallbacks) == 0 && len(candidates) > 1 {
		fallbacks := make([]schemas.Fallback, 0, len(candidates)-1)
		for _, prov := range candidates {
			if prov == selected {
				continue
			}
			fallbacks = append(fallbacks, schemas.Fallback{Provider: prov, Model: model, Inferred: true})
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
//
// A key-selection-bypass request (BifrostContextKeySkipKeySelection) that the catalog leaves
// unresolved is pinned to the integration's canonical provider, with no candidates. A zero-key
// OAuth-passthrough provider (`anthropic` with `"keys": []` for Claude Code Max mode) contributes
// nothing to the catalog, so without this its bare-model requests fail provider validation.
// Whenever the catalog does resolve, its pick, candidates and fallbacks stand unchanged:
// isKeySkippingAllowed gates the flag per attempt, so a non-Anthropic fallback uses its own
// stored keys. The pin also respects the routing allowlist; governance would refuse a provider
// the allowlist excludes. On the HTTP routes only checkAnthropicPassthrough sets the flag for a
// request that reaches this resolver: applyPassthroughCallerAuth sets it for generic passthrough,
// which PreRequestHook skips, and the realtime callers pass a nil ctx.
func ResolveProviderFromCatalog(ctx *schemas.BifrostContext, catalog *modelcatalog.ModelCatalog, model string) (schemas.ModelProvider, []schemas.ModelProvider) {
	selected, candidates, _ := resolveProvider(ctx, catalog, model)
	return selected, candidates
}

// resolveProvider is ResolveProviderFromCatalog plus whether the selection was pinned by the
// key-selection bypass rather than picked from the catalog.
func resolveProvider(ctx *schemas.BifrostContext, catalog *modelcatalog.ModelCatalog, model string) (schemas.ModelProvider, []schemas.ModelProvider, bool) {
	selected, candidates := resolveFromCatalog(ctx, catalog, model)
	if selected != "" {
		return selected, candidates, false
	}
	if provider := keySelectionBypassProvider(ctx); provider != "" {
		return provider, nil, true
	}
	return "", candidates, false
}

// keySelectionBypassProvider returns the integration's canonical provider for a request that skips
// key selection, or "" when the flag is unset, the route has no canonical provider, or a routing
// allowlist excludes that provider.
func keySelectionBypassProvider(ctx *schemas.BifrostContext) schemas.ModelProvider {
	if ctx == nil {
		return ""
	}
	if skip, _ := ctx.Value(schemas.BifrostContextKeySkipKeySelection).(bool); !skip {
		return ""
	}
	integrationType, _ := ctx.Value(schemas.BifrostContextKeyIntegrationType).(string)
	provider := integrationTypeToDefaultProvider[integrationType]
	if provider == "" {
		return ""
	}
	if allowed, ok := ctx.Value(schemas.BifrostContextKeyRoutingAllowedProviders).([]schemas.ModelProvider); ok && !slices.Contains(allowed, provider) {
		return ""
	}
	return provider
}

// resolveFromCatalog is the catalog-only pick behind ResolveProviderFromCatalog.
func resolveFromCatalog(ctx *schemas.BifrostContext, catalog *modelcatalog.ModelCatalog, model string) (schemas.ModelProvider, []schemas.ModelProvider) {
	if catalog == nil || model == "" {
		return "", nil
	}
	providers := catalog.GetProvidersForModel(model)
	if len(providers) == 0 {
		// Logged because this is one of only two ways the resolver can leave
		// req.Provider empty, and the caller sees the same generic "could not auto
		// resolve a provider" error either way. The other way is the allowlist
		// pruning every candidate, logged below — the two need to be
		// distinguishable in routing engine logs to be diagnosable at all.
		if ctx != nil {
			ctx.AppendRoutingEngineLog(schemas.RoutingEngineModelCatalog, schemas.LogLevelInfo, fmt.Sprintf(
				"Model catalog has no provider serving model %s; leaving req.Provider empty", model,
			))
		}
		return "", nil
	}

	// GetProvidersForModel iterates a Go map; the returned order is not stable.
	// Sort alphabetically so the fallback pick (providers[0]) is deterministic across
	// restarts and across processes — critical when no IntegrationType hint is set.
	slices.SortFunc(providers, func(a, b schemas.ModelProvider) int {
		return strings.Compare(string(a), string(b))
	})

	var allowed []schemas.ModelProvider
	allowlistSet := false
	if ctx != nil {
		allowed, allowlistSet = ctx.Value(schemas.BifrostContextKeyRoutingAllowedProviders).([]schemas.ModelProvider)
	}

	// Respect the routing-allowlist set by an earlier plugin (e.g., governance VK config):
	// intersect catalog candidates with the allowlist so the resolver does not pick a provider
	// the VK disallows when no earlier routing plugin set req.Provider. Core does not check the
	// allowlist; governance's PreLLMHook refuses a disallowed provider on every routed LLM
	// attempt, so pruning here is what keeps the pick valid. Emit observability logs for both the
	// partial-prune and all-pruned cases, so the pruning is visible in routing engine logs when it
	// fires.
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

	if preferred := integrationPreferredProvider(ctx, providers); preferred != "" {
		return preferred, providers
	}
	return providers[0], providers
}

// integrationPreferredProvider returns the canonical provider of the integration route the request
// came in on (azure for an Azure OpenAI SDK on the openai route) when it is among providers, and ""
// when there is no route hint or the hinted provider is not a candidate.
func integrationPreferredProvider(ctx *schemas.BifrostContext, providers []schemas.ModelProvider) schemas.ModelProvider {
	if ctx == nil {
		return ""
	}
	integrationType, _ := ctx.Value(schemas.BifrostContextKeyIntegrationType).(string)
	preferred := integrationTypeToDefaultProvider[integrationType]
	if preferred == "" {
		return ""
	}
	if isAzureUser, _ := ctx.Value(schemas.BifrostContextKeyIsAzureUserAgent).(bool); integrationType == "openai" && isAzureUser {
		preferred = schemas.Azure
	}
	if !slices.Contains(providers, preferred) {
		return ""
	}
	return preferred
}

// PreLLMHook implements schemas.LLMPlugin (no-op).
func (p *Plugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	return req, nil, nil
}

// PostLLMHook implements schemas.LLMPlugin (no-op).
func (p *Plugin) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, bifrostErr, nil
}
