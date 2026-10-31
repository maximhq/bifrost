package handlers

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// Bounds for the per-provider size of the newest-models listing.
const (
	defaultNewestModelsPerProvider = 3
	maxNewestModelsPerProvider     = 50
)

// NewestModel is one model in the newest-models listing.
type NewestModel struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Created  int64  `json:"created"` // provider-reported creation time in Unix seconds; zero when unknown
}

// ListNewestModelsResponse is the body of GET /api/models/newest.
type ListNewestModelsResponse struct {
	Models []NewestModel `json:"models"`
}

// listNewestModels handles GET /api/models/newest - each provider's newest models.
// Query parameters:
//   - providers: Comma-separated providers to list, in the order they are returned (required)
//   - limit: Models per provider, 1 to 50 (default: 3)
//   - keys: Comma-separated provider key UUIDs to filter models accessible by those keys
//
// Models are ordered by the creation time each provider last reported in
// list-models, read from the cached model catalog; providers are never polled.
// Times are compared only within one provider, because providers do not report
// them on a shared scale. Models without a time come last, and deprecated
// models are left out. A virtual key on the request scopes the listing the
// same way it scopes GET /api/models.
func (h *ProviderHandler) listNewestModels(ctx *fasthttp.RequestCtx) {
	providers := parseNewestModelsProviders(string(ctx.QueryArgs().Peek("providers")))
	if len(providers) == 0 {
		SendError(ctx, fasthttp.StatusBadRequest, "providers query parameter is required")
		return
	}
	limit := defaultNewestModelsPerProvider
	if raw := string(ctx.QueryArgs().Peek("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxNewestModelsPerProvider {
			SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("limit must be between 1 and %d", maxNewestModelsPerProvider))
			return
		}
		limit = parsed
	}

	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.inMemoryStore)
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}
	query, ok := h.parseModelListQuery(ctx, bifrostCtx, 0)
	if !ok {
		return
	}

	out := make([]NewestModel, 0, len(providers)*limit)
	for _, provider := range providers {
		if query.HasVKFilter && !slices.ContainsFunc(query.Access.GrantedProvidersForModel(""), func(granted string) bool {
			return strings.EqualFold(granted, string(provider))
		}) {
			continue
		}
		out = append(out, h.newestModelsForProvider(provider, query, limit)...)
	}
	SendJSON(ctx, ListNewestModelsResponse{Models: out})
}

// newestModelsForProvider returns up to limit of one provider's non-deprecated
// models, newest cached creation time first; ties keep catalog order.
func (h *ProviderHandler) newestModelsForProvider(provider schemas.ModelProvider, query modelListQuery, limit int) []NewestModel {
	var created map[string]int64
	if h.inMemoryStore != nil {
		created = h.inMemoryStore.ModelCatalog.GetModelCreatedTimes(provider, query.Unfiltered)
	}
	models := make([]NewestModel, 0)
	for _, model := range h.listManagementModelsForProvider(provider, query) {
		if h.isModelDeprecated(model.Name, provider) {
			continue
		}
		models = append(models, NewestModel{Name: model.Name, Provider: string(provider), Created: created[model.Name]})
	}
	slices.SortStableFunc(models, func(a, b NewestModel) int { return cmp.Compare(b.Created, a.Created) })
	return models[:min(len(models), limit)]
}

// parseNewestModelsProviders splits the providers parameter, dropping blanks
// and repeats while keeping the caller's order.
func parseNewestModelsProviders(raw string) []schemas.ModelProvider {
	var out []schemas.ModelProvider
	for _, part := range strings.Split(raw, ",") {
		provider := schemas.ModelProvider(strings.TrimSpace(part))
		if provider != "" && !slices.Contains(out, provider) {
			out = append(out, provider)
		}
	}
	return out
}
