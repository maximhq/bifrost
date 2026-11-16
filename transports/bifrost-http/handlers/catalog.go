package handlers

import (
	"net/url"
	"slices"
	"strings"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// RegisterCatalogRoutes serves client catalog exports with inference authentication,
// separately from the provider management API's administrator authentication.
func (h *ProviderHandler) RegisterCatalogRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET("/catalog/codex", lib.ChainMiddlewares(h.catalog, middlewares...))
	r.GET("/catalog/claude", lib.ChainMiddlewares(h.catalog, middlewares...))
	r.GET("/catalog/pi", lib.ChainMiddlewares(h.catalog, middlewares...))
	r.GET("/catalog/opencode", lib.ChainMiddlewares(h.catalog, middlewares...))
}

type codexReasoningLevel struct {
	Effort      string `json:"effort"`
	Description string `json:"description"`
}

type codexCatalogModel struct {
	Slug                              string                `json:"slug"`
	DisplayName                       string                `json:"display_name"`
	Description                       string                `json:"description"`
	BaseInstructions                  string                `json:"base_instructions"`
	DefaultReasoningLevel             *string               `json:"default_reasoning_level,omitempty"`
	SupportedReasoningLevels          []codexReasoningLevel `json:"supported_reasoning_levels"`
	ShellType                         string                `json:"shell_type"`
	Visibility                        string                `json:"visibility"`
	SupportedInAPI                    bool                  `json:"supported_in_api"`
	Priority                          int                   `json:"priority"`
	SupportVerbosity                  bool                  `json:"support_verbosity"`
	SupportsReasoningSummaryParameter bool                  `json:"supports_reasoning_summary_parameter"`
	ContextWindow                     *int                  `json:"context_window,omitempty"`
	InputModalities                   []string              `json:"input_modalities"`
	ExperimentalSupportedTools        []string              `json:"experimental_supported_tools"`
	TruncationPolicy                  codexTruncationPolicy `json:"truncation_policy"`
}

type codexTruncationPolicy struct {
	Mode  string `json:"mode"`
	Limit int    `json:"limit"`
}

type claudeCatalogModel struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	DisplayName string `json:"display_name"`
	Description string `json:"description,omitempty"`
	CreatedAt   string `json:"created_at"`
}

type claudeCatalogResponse struct {
	Data    []claudeCatalogModel `json:"data"`
	HasMore bool                 `json:"has_more"`
	FirstID *string              `json:"first_id"`
	LastID  *string              `json:"last_id"`
}

// catalog reads the same configured model pool as the management listing, but
// never bypasses pool restrictions or truncates an export to a UI page size.
// All allocations and capability lookups are confined to these export requests;
// no additional work or retained state is added to inference requests.
func (h *ProviderHandler) catalog(ctx *fasthttp.RequestCtx) {
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.inMemoryStore)
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "failed to convert context")
		return
	}
	query := modelListQuery{
		Provider:       schemas.ModelProvider(string(ctx.QueryArgs().Peek("provider"))),
		Query:          string(ctx.QueryArgs().Peek("query")),
		HideDeprecated: string(ctx.QueryArgs().Peek("include_deprecated")) != "true",
	}
	access, err := h.modelsManager.ResolveAccess(bifrostCtx)
	if err != nil {
		SendError(ctx, fasthttp.StatusUnauthorized, "failed to resolve catalog access")
		return
	}
	if access != nil {
		query.HasVKFilter = true
		query.Access = access
	}
	models, _, err := h.listManagementModels(query)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to list catalog models")
		return
	}
	format := string(ctx.Path())
	var pi *piCatalogResponse
	var openCode *openCodeCatalogResponse
	switch format {
	case "/catalog/pi", "/catalog/opencode":
		baseURL := string(ctx.QueryArgs().Peek("base_url"))
		if baseURL == "" {
			scheme := "http"
			if ctx.IsTLS() {
				scheme = "https"
			}
			baseURL = scheme + "://" + string(ctx.Host()) + "/openai/v1"
		}
		u, err := url.Parse(baseURL)
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			SendError(ctx, fasthttp.StatusBadRequest, "base_url must be an HTTP(S) inference URL without credentials, query or fragment")
			return
		}
		if format == "/catalog/pi" {
			pi = newPiCatalog(baseURL)
		} else {
			openCode = newOpenCodeCatalog(baseURL)
		}
	}
	codexModels := make([]codexCatalogModel, 0)
	claudeModels := make([]claudeCatalogModel, 0)
	for _, model := range models {
		var entry *modelcatalog.PricingEntry
		if h.inMemoryStore.ModelCatalog != nil {
			entry = h.inMemoryStore.ModelCatalog.GetModelCapabilityEntryForModel(model.Name, model.Provider)
		}
		if entry != nil && entry.Mode != "" && entry.Mode != "chat" && entry.Mode != "responses" {
			continue
		}
		id := string(model.Provider) + "/" + model.Name
		description := ""
		if entry != nil {
			description = entry.AdditionalAttributes["description"]
		}
		var caps *schemas.ModelCapabilities
		if format != "/catalog/claude" {
			caps = h.inMemoryStore.ModelCatalog.GetModelCapabilities(model.Provider, h.aliasTarget(model.Provider, model.Name))
		}
		switch format {
		case "/catalog/pi":
			pi.Providers.Bifrost.Models = append(pi.Providers.Bifrost.Models, makePiCatalogModel(id, entry, caps))
		case "/catalog/opencode":
			openCode.Providers.Bifrost.Models[id] = makeOpenCodeCatalogModel(id, entry, caps)
		case "/catalog/codex":
			codexModels = append(codexModels, makeCodexCatalogModel(id, description, entry, caps, len(codexModels)))
		case "/catalog/claude":
			// The catalog has no creation timestamps; use a stable RFC3339 epoch
			// rather than pretending each download creates a new model.
			claudeModels = append(claudeModels, claudeCatalogModel{ID: id, Type: "model", DisplayName: id, Description: description, CreatedAt: "1970-01-01T00:00:00Z"})
		}
	}
	switch format {
	case "/catalog/pi":
		SendJSON(ctx, pi)
	case "/catalog/opencode":
		SendJSON(ctx, openCode)
	case "/catalog/codex":
		SendJSON(ctx, struct {
			Models []codexCatalogModel `json:"models"`
		}{Models: codexModels})
	case "/catalog/claude":
		response := claudeCatalogResponse{Data: claudeModels}
		if len(claudeModels) > 0 {
			response.FirstID = &claudeModels[0].ID
			response.LastID = &claudeModels[len(claudeModels)-1].ID
		}
		SendJSON(ctx, response)
	}
}

func makeCodexCatalogModel(id, description string, entry *modelcatalog.PricingEntry, caps *schemas.ModelCapabilities, priority int) codexCatalogModel {
	model := codexCatalogModel{
		Slug: id, DisplayName: id, Description: description,
		BaseInstructions:         "You are a coding assistant.",
		SupportedReasoningLevels: []codexReasoningLevel{},
		ShellType:                "shell_command", Visibility: "list", SupportedInAPI: true, Priority: priority,
		InputModalities: []string{"text"}, ExperimentalSupportedTools: []string{},
		TruncationPolicy: codexTruncationPolicy{Mode: "tokens", Limit: 10000},
	}
	if entry != nil {
		model.ContextWindow = entry.ContextLength
		if model.ContextWindow == nil {
			model.ContextWindow = entry.MaxInputTokens
		}
		if entry.Architecture != nil {
			for _, modality := range []string{"image", "audio"} {
				if slices.Contains(entry.Architecture.InputModalities, modality) {
					model.InputModalities = append(model.InputModalities, modality)
				}
			}
		}
	}
	if caps != nil {
		for _, effort := range caps.ReasoningEffortLevels {
			if strings.TrimSpace(effort) != "" {
				model.SupportedReasoningLevels = append(model.SupportedReasoningLevels, codexReasoningLevel{Effort: effort, Description: effort})
			}
		}
		if caps.DefaultReasoningEffort != nil && slices.Contains(caps.ReasoningEffortLevels, *caps.DefaultReasoningEffort) {
			model.DefaultReasoningLevel = caps.DefaultReasoningEffort
		}
	}
	return model
}
