package handlers

import (
	"sort"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	governanceplugin "github.com/maximhq/bifrost/plugins/governance"
	"github.com/valyala/fasthttp"
)

// listModelsFromCatalog serves GET /v1/models (without a ?provider= scope) from
// the in-memory ModelCatalog instead of a live provider fan-out.
//
// Rationale: ListAllModels issues a runtime list-models call per provider and
// waits for all of them. Those calls share the provider's inference worker
// queue, so on a provider with low concurrency (e.g. a local llama.cpp slot
// reservation) the listing blocks behind in-flight generations — measured at
// 700s+ on a busy GPU, which every OpenAI-compatible client experiences as an
// empty model dropdown. The ModelCatalog already holds the same model set
// (populated by boot-time discovery and refreshed on provider/key changes),
// so the aggregate listing can be answered from memory.
//
// Returns nil when the catalog cannot answer the request, in which case the
// caller falls back to the live fan-out. That happens when:
//   - the catalog is not configured,
//   - the client asked for live results (?source=live),
//   - the client uses pagination (page_token/page_size), which the catalog
//     does not model.
func (h *CompletionHandler) listModelsFromCatalog(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext) *schemas.BifrostListModelsResponse {
	if h.config == nil || h.config.ModelCatalog == nil {
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(string(ctx.QueryArgs().Peek("source"))), "live") {
		return nil
	}
	if len(ctx.QueryArgs().Peek("page_token")) > 0 || len(ctx.QueryArgs().Peek("page_size")) > 0 {
		return nil
	}
	// Any other query parameter is a provider-specific list-models feature
	// (search, ordering, …) that only the live path understands.
	for k := range ctx.QueryArgs().All() {
		switch string(k) {
		case "provider", "source", "page_token", "page_size":
		default:
			return nil
		}
	}

	providers := h.catalogProvidersForRequest(bifrostCtx)
	if providers == nil {
		return nil
	}

	modelFilter, ok := h.virtualKeyModelFilter(ctx)
	if !ok {
		// Virtual-key resolution hit a store error; let the live path handle
		// (and surface) it instead of answering 200 with a misleading list.
		return nil
	}

	data := make([]schemas.Model, 0, 64)
	for _, provider := range providers {
		for _, model := range h.config.ModelCatalog.GetModelsForProvider(provider) {
			if modelFilter != nil && !modelFilter(provider, model) {
				continue
			}
			data = append(data, schemas.Model{ID: string(provider) + "/" + model})
		}
	}
	if len(data) == 0 {
		// An empty catalog usually means boot-time discovery has not (yet)
		// populated it; let the live fan-out answer rather than serving an
		// empty 200 that reads as "no models".
		return nil
	}
	sort.Slice(data, func(i, j int) bool { return data[i].ID < data[j].ID })

	return &schemas.BifrostListModelsResponse{Data: data}
}

// catalogProvidersForRequest resolves the provider set the catalog listing
// should cover: the virtual key's providers when the request carries one
// (stamped on the context by applyListModelsVirtualKeyProviderFilter),
// otherwise every configured provider. Returns nil when the configured
// provider set cannot be resolved, signalling the caller to fall back.
func (h *CompletionHandler) catalogProvidersForRequest(bifrostCtx *schemas.BifrostContext) []schemas.ModelProvider {
	if bifrostCtx != nil {
		if raw := bifrostCtx.Value(schemas.BifrostContextKeyAvailableProviders); raw != nil {
			if available, ok := raw.([]schemas.ModelProvider); ok && len(available) > 0 {
				return available
			}
			return nil
		}
	}
	providers, err := h.client.GetConfiguredProviders()
	if err != nil {
		return nil
	}
	return providers
}

// virtualKeyModelFilter returns a predicate applying the virtual key's
// per-provider allowed_models to catalog entries, mirroring the governance
// plugin's filtering on the live path (which this handler bypasses). A nil
// (nil, true) means "no filtering" — the request carries no virtual key at
// all; (nil, false) means the caller must fall back to the live path (store
// lookup failed, or the key is unknown/inactive and the live path's
// governance enforcement should answer).
// Wildcard semantics follow schemas.WhiteList/BlackList: allowed ["*"] permits
// every catalog model of that provider, an empty allowed list denies all
// (deny-by-default), and blacklisted entries are excluded first.
func (h *CompletionHandler) virtualKeyModelFilter(ctx *fasthttp.RequestCtx) (func(schemas.ModelProvider, string) bool, bool) {
	vkValue := governanceplugin.ParseVirtualKeyFromFastHTTPRequest(ctx)
	if vkValue == nil || strings.TrimSpace(*vkValue) == "" {
		return nil, true
	}
	if h.config == nil || h.config.ConfigStore == nil {
		// A keyed request we cannot verify: fall back to the live path rather
		// than serving the full catalog unfiltered.
		return nil, false
	}
	vk, err := h.config.ConfigStore.GetVirtualKeyByValue(ctx, strings.TrimSpace(*vkValue))
	if err != nil {
		// Store error: signal the caller to fall back to the live path, which
		// surfaces the failure instead of answering 200 with a misleading list.
		return nil, false
	}
	if vk == nil || vk.IsActive == nil || !*vk.IsActive {
		// Unknown or inactive key: defer to the live path so its governance
		// enforcement (per-provider rejection) applies unchanged, instead of
		// answering with the full unfiltered catalog.
		return nil, false
	}

	type modelScope struct {
		allowed     schemas.WhiteList
		blacklisted schemas.BlackList
	}
	scopeByProvider := make(map[schemas.ModelProvider]modelScope, len(vk.ProviderConfigs))
	for _, pc := range vk.ProviderConfigs {
		provider := schemas.ModelProvider(strings.TrimSpace(pc.Provider))
		if provider == "" {
			continue
		}
		scopeByProvider[provider] = modelScope{allowed: pc.AllowedModels, blacklisted: pc.BlacklistedModels}
	}

	return func(provider schemas.ModelProvider, model string) bool {
		scope, ok := scopeByProvider[provider]
		if !ok {
			return false
		}
		if scope.blacklisted.IsBlocked(model) {
			return false
		}
		return scope.allowed.IsAllowed(model)
	}, true
}
