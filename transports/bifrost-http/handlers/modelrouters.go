// This file contains the model router endpoints. A model router takes the
// requests its routing matches, after the routing rules; classifies each into
// a complexity tier; and sends it to the model it names for that tier. A
// router's classifier, targets and routing are saved on separate endpoints,
// so saving one never changes the others.

package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/fasthttp/router"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/plugins/routing"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/maximhq/bifrost/plugins/routing/complexity/suggest"
	"github.com/maximhq/bifrost/plugins/routing/rules"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// ModelRouterTargetsRequest is the body that replaces a router's targets.
type ModelRouterTargetsRequest struct {
	Targets []configstoreTables.TableModelRouterTarget `json:"targets"`
}

// ModelRouterDefaultsResponse is what a new router starts from: the built-in
// config, and the guidance the decision model and llm classifier send while
// nothing is edited.
type ModelRouterDefaultsResponse struct {
	Config           configstore.ComplexityAnalyzerConfig           `json:"config"`
	DecisionDefaults configstore.ComplexityDecisionGuidanceDefaults `json:"decision_defaults"`
	LLMDefaultPrompt string                                         `json:"llm_default_prompt"`
}

// registerModelRouterRoutes registers the model router routes. They never
// shipped under /api/governance, so they have no legacy alias.
func (h *RoutingHandler) registerModelRouterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	handle := func(method, path string, handler fasthttp.RequestHandler) {
		r.Handle(method, path, lib.ChainMiddlewares(h.visibleModelRouter(path, handler), middlewares...))
	}
	handle(fasthttp.MethodGet, "/api/routing/model-routers", h.listModelRouters)
	handle(fasthttp.MethodPost, "/api/routing/model-routers", h.createModelRouter)
	handle(fasthttp.MethodGet, "/api/routing/model-routers/defaults", h.getModelRouterDefaults)
	handle(fasthttp.MethodGet, "/api/routing/model-routers/{router_id}", h.getModelRouter)
	handle(fasthttp.MethodPut, "/api/routing/model-routers/{router_id}", h.updateModelRouter)
	handle(fasthttp.MethodDelete, "/api/routing/model-routers/{router_id}", h.deleteModelRouter)
	handle(fasthttp.MethodPost, "/api/routing/model-routers/{router_id}/reset", h.resetModelRouter)
	handle(fasthttp.MethodPut, "/api/routing/model-routers/{router_id}/targets", h.setModelRouterTargets)
	handle(fasthttp.MethodPut, "/api/routing/model-routers/{router_id}/routing", h.setModelRouterRouting)
	handle(fasthttp.MethodGet, "/api/routing/model-routers/{router_id}/status", h.getModelRouterStatus)
	handle(fasthttp.MethodPost, "/api/routing/model-routers/{router_id}/status/retry", h.retryModelRouterWarmup)
	handle(fasthttp.MethodPost, "/api/routing/model-routers/{router_id}/classify", h.classifyWithModelRouter)

}

// visibleModelRouter wraps the handler of a route that names a router
// ({router_id}) so it answers 404 when the config store does not return that
// router to the caller. In OSS the store returns every saved router. Behind
// the enterprise data access control it does not return one whose routing is
// outside the caller's scope, and several routes would otherwise act on such a
// router anyway: status, warmup retry, classify and the suggestion routes act
// on the in-memory router, and delete, reset and add phrases write without
// reading it first. Any other failed read is answered 500 without running the
// handler, as getRoutingRule answers it, since it cannot say whether the caller
// may see the router. A router found but stored in a form this version cannot
// read still reaches the handler, which reports it (an unreadable router can
// still be deleted). Routes that name no router are returned unchanged.
func (h *RoutingHandler) visibleModelRouter(path string, handler fasthttp.RequestHandler) fasthttp.RequestHandler {
	if !strings.Contains(path, "{router_id}") {
		return handler
	}
	return func(ctx *fasthttp.RequestCtx) {
		if h.configStore != nil {
			id := pathID(ctx, "router_id")
			if _, err := h.configStore.GetModelRouter(ctx, id); err != nil && !errors.Is(err, configstore.ErrConfigUnreadable) {
				sendModelRouterLoadError(ctx, id, err)
				return
			}
		}
		handler(ctx)
	}
}

// pathID reads a route parameter.
func pathID(ctx *fasthttp.RequestCtx, name string) string {
	id, _ := ctx.UserValue(name).(string)
	return strings.TrimSpace(id)
}

// sendModelRouterLoadError maps a failed router read to its status.
func sendModelRouterLoadError(ctx *fasthttp.RequestCtx, id string, err error) {
	switch {
	case errors.Is(err, configstore.ErrNotFound):
		SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("model router %q not found", id))
	case errors.Is(err, configstore.ErrConfigUnreadable):
		SendError(ctx, fasthttp.StatusConflict, fmt.Sprintf("model router %q is stored in a form this version cannot read; save or reset it: %v", id, err))
	default:
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to get model router: %v", err))
	}
}

// decodeModelRouter reads a router body strictly, like the complexity config
// endpoint, so a misspelled setting is reported instead of dropped. Targets in
// the body are ignored: a router's targets are replaced on their own endpoint,
// so saving its config never changes which model a tier gets. Routing is kept
// for the caller: create applies it, update ignores it.
func decodeModelRouter(ctx *fasthttp.RequestCtx) (*configstore.ModelRouter, bool) {
	var payload configstore.ModelRouter
	if !decodeStrictJSON(ctx, &payload) {
		return nil, false
	}
	payload.Targets = nil
	return &payload, true
}

// decodeStrictJSON decodes the body into payload, refusing unknown fields and
// trailing values so a misspelled setting is an error rather than dropped. It
// answers 400 and returns false on failure.
func decodeStrictJSON(ctx *fasthttp.RequestCtx, payload any) bool {
	decoder := json.NewDecoder(bytes.NewReader(ctx.PostBody()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(payload); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request payload: %v", err))
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid request format: multiple JSON values")
		return false
	}
	return true
}

// prepareModelRouter validates a router body for saving: its identity, its
// config, and the runtime dependencies that config needs.
func (h *RoutingHandler) prepareModelRouter(ctx *fasthttp.RequestCtx, router *configstore.ModelRouter) bool {
	if err := configstore.ValidateModelRouterIdentity(router); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return false
	}
	normalized, err := complexity.ValidateAndNormalize(&router.ComplexityAnalyzerConfig)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return false
	}
	if err := h.routingManager.ValidateComplexityAnalyzerConfig(ctx, normalized); err != nil {
		if errors.Is(err, ErrRoutingPluginUnavailable) {
			SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
			return false
		}
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return false
	}
	router.ComplexityAnalyzerConfig = *normalized
	return true
}

// reloadModelRouter serves a saved router, reporting a failure the way the
// other routing endpoints do.
func (h *RoutingHandler) reloadModelRouter(ctx *fasthttp.RequestCtx, id string) bool {
	if err := h.routingManager.ReloadModelRouter(ctx, id); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to reload model router in memory: %v, please restart bifrost to sync with the database", err))
		return false
	}
	return true
}

// listModelRouters returns every router, oldest first.
func (h *RoutingHandler) listModelRouters(ctx *fasthttp.RequestCtx) {
	routers, err := h.configStore.GetModelRouters(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to list model routers: %v", err))
		return
	}
	if routers == nil {
		routers = []configstore.ModelRouter{}
	}
	SendJSON(ctx, map[string]any{"routers": routers, "count": len(routers)})
}

// getModelRouterDefaults returns what a new router starts from, so the
// dashboard can show the built-in phrases and guidance before saving anything.
func (h *RoutingHandler) getModelRouterDefaults(ctx *fasthttp.RequestCtx) {
	SendJSON(ctx, ModelRouterDefaultsResponse{
		Config:           complexity.DefaultAnalyzerConfig(),
		DecisionDefaults: configstore.DefaultComplexityDecisionGuidance(),
		LLMDefaultPrompt: complexity.DefaultLLMClassifierGuidance(),
	})
}

// getModelRouter returns one router.
func (h *RoutingHandler) getModelRouter(ctx *fasthttp.RequestCtx) {
	id := pathID(ctx, "router_id")
	router, err := h.configStore.GetModelRouter(ctx, id)
	if err != nil {
		sendModelRouterLoadError(ctx, id, err)
		return
	}
	SendJSON(ctx, router)
}

// createModelRouter stores and serves a new router. An omitted id is
// generated. A body without routing creates the router off, global, and
// after every other global router.
func (h *RoutingHandler) createModelRouter(ctx *fasthttp.RequestCtx) {
	router, ok := decodeModelRouter(ctx)
	if !ok {
		return
	}
	if strings.TrimSpace(router.ID) == "" {
		router.ID = uuid.NewString()
	}
	if !h.prepareModelRouter(ctx, router) {
		return
	}
	if router.Routing != nil && !h.prepareModelRouterRouting(ctx, router.Routing, true) {
		return
	}
	if err := h.configStore.CreateModelRouter(ctx, router); err != nil {
		switch {
		case errors.Is(err, configstore.ErrModelRouterPriorityTaken):
			SendError(ctx, fasthttp.StatusConflict, fmt.Sprintf("%v; use a different priority", err))
		case errors.Is(err, configstore.ErrAlreadyExists):
			SendError(ctx, fasthttp.StatusConflict, err.Error())
		default:
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to create model router: %v", err))
		}
		return
	}
	if !h.reloadModelRouter(ctx, router.ID) {
		return
	}
	saved, err := h.configStore.GetModelRouter(ctx, router.ID)
	if err != nil {
		sendModelRouterLoadError(ctx, router.ID, err)
		return
	}
	lib.RecordAuditChange(ctx, "model_router.created", "model_router", saved.ID, saved.Name, nil, saved)
	SendJSON(ctx, saved)
}

// updateModelRouter changes a router's name, description and config. Only
// what the body sends changes, as in a routing update: an omitted name or
// description keeps the stored one, and a body without any classifier
// setting keeps the stored config, so a rename never writes back a config it
// read earlier. A body with classifier settings replaces the whole config,
// whose sections are validated together. Targets and routing are saved on
// their own endpoints and are left as they are, whatever the body holds.
func (h *RoutingHandler) updateModelRouter(ctx *fasthttp.RequestCtx) {
	id := pathID(ctx, "router_id")
	router, ok := decodeModelRouter(ctx)
	if !ok {
		return
	}
	sent, sentConfig := sentModelRouterFields(ctx.PostBody())
	before, err := h.configStore.GetModelRouter(ctx, id)
	// A stored config this version cannot read can still be replaced by one
	// the body sends; anything else needs the stored router.
	if err != nil && !(errors.Is(err, configstore.ErrConfigUnreadable) && sentConfig) {
		sendModelRouterLoadError(ctx, id, err)
		return
	}
	if before != nil {
		if !sent["name"] {
			router.Name = before.Name
		}
		if !sent["description"] {
			router.Description = before.Description
		}
		if !sentConfig {
			router.ComplexityAnalyzerConfig = before.ComplexityAnalyzerConfig
		}
	}
	router.ID = id
	router.Routing = nil
	if sentConfig {
		if !h.prepareModelRouter(ctx, router) {
			return
		}
	} else if err := configstore.ValidateModelRouterIdentity(router); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	if err := h.configStore.UpdateModelRouter(ctx, router); err != nil {
		switch {
		case errors.Is(err, configstore.ErrNotFound):
			SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("model router %q not found", id))
		case errors.Is(err, configstore.ErrAlreadyExists):
			SendError(ctx, fasthttp.StatusConflict, err.Error())
		default:
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to update model router: %v", err))
		}
		return
	}
	if !h.reloadModelRouter(ctx, id) {
		return
	}
	saved, err := h.configStore.GetModelRouter(ctx, id)
	if err != nil {
		sendModelRouterLoadError(ctx, id, err)
		return
	}
	lib.RecordAuditChange(ctx, "model_router.updated", "model_router", saved.ID, saved.Name, before, saved)
	SendJSON(ctx, saved)
}

// sentModelRouterFields reports which top-level fields a router body sends,
// and whether it sends any classifier setting: every field besides the id,
// name, description, targets and routing. The body has already been decoded
// strictly, so every field is a known one.
func sentModelRouterFields(body []byte) (sent map[string]bool, sentConfig bool) {
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(body, &fields)
	sent = make(map[string]bool, len(fields))
	for name := range fields {
		sent[name] = true
		switch name {
		case "id", "name", "description", "targets", "routing":
		default:
			sentConfig = true
		}
	}
	return sent, sentConfig
}

// deleteModelRouter removes a router with its targets and routing. Routing
// rules never name a router, so nothing refuses the delete.
func (h *RoutingHandler) deleteModelRouter(ctx *fasthttp.RequestCtx) {
	id := pathID(ctx, "router_id")
	before, _ := h.configStore.GetModelRouter(ctx, id)
	if err := h.configStore.DeleteModelRouter(ctx, id); err != nil {
		switch {
		case errors.Is(err, configstore.ErrNotFound):
			SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("model router %q not found", id))
		default:
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to delete model router: %v", err))
		}
		return
	}
	if err := h.routingManager.RemoveModelRouter(ctx, id); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("model router deleted but not removed from memory: %v, please restart bifrost to sync with the database", err))
		return
	}
	name := id
	if before != nil {
		name = before.Name
	}
	lib.RecordAuditChange(ctx, "model_router.deleted", "model_router", id, name, before, nil)
	SendJSON(ctx, map[string]any{"message": "Model router deleted successfully"})
}

// resetModelRouter restores a router's phrases to the built-in defaults and
// keeps its other settings.
func (h *RoutingHandler) resetModelRouter(ctx *fasthttp.RequestCtx) {
	id := pathID(ctx, "router_id")
	before, _ := h.configStore.GetModelRouter(ctx, id)
	defaults := complexity.DefaultAnalyzerConfig()
	restored, err := h.configStore.ResetModelRouter(ctx, id, &defaults)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("model router %q not found", id))
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to reset model router: %v", err))
		return
	}
	if !h.reloadModelRouter(ctx, id) {
		return
	}
	// Read back with its targets and routing, which the reset keeps.
	saved, err := h.configStore.GetModelRouter(ctx, id)
	if err != nil {
		sendModelRouterLoadError(ctx, id, err)
		return
	}
	lib.RecordAuditChange(ctx, "model_router.reset", "model_router", restored.ID, restored.Name, before, saved)
	SendJSON(ctx, saved)
}

// getModelRouterStatus reports one router's classifier readiness, in the
// shape of the complexity analyzer status. Only this per-router status serves
// the suggestion tier step's generated prompt and criteria: what the step sends
// when nothing is edited, rendered from the router's config with its own
// phrases as examples. The settings editors show them, and an edit replaces
// them.
func (h *RoutingHandler) getModelRouterStatus(ctx *fasthttp.RequestCtx) {
	id := pathID(ctx, "router_id")
	semantic, llm, err := h.routingManager.GetModelRouterStatus(ctx, id)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			h.sendModelRouterNotServed(ctx, id)
			return
		}
		SendError(ctx, fasthttp.StatusServiceUnavailable, fmt.Sprintf("failed to get model router status: %v", err))
		return
	}
	// The tier step's generated text comes from the saved router, so the
	// editor shows exactly what an unedited run of it sends. A router that
	// cannot be read gets none, rather than text rendered from the built-in
	// defaults as if it were its own.
	var tierPrompt string
	var tierCriteria map[string]configstore.ComplexityDecisionTierDefaults
	if router, loadErr := h.configStore.GetModelRouter(ctx, id); loadErr == nil {
		normalized := router.ComplexityAnalyzerConfig.Normalized()
		tierPrompt, tierCriteria = routing.SuggestionTierDefaults(&normalized)
	} else {
		logger.Warn("model router %s status: could not read the router for its suggestion tier defaults: %v", id, loadErr)
	}
	SendJSON(ctx, complexityStatusResponse{
		SemanticStatusInfo:                semantic,
		LLM:                               &llm,
		DecisionDefaults:                  configstore.DefaultComplexityDecisionGuidance(),
		SuggestionDefaultInstructions:     suggest.DefaultInstructions(),
		SuggestionDefaultTierInstructions: suggest.DefaultTierInstructions(),
		SuggestionDefaultTierPrompt:       tierPrompt,
		SuggestionDefaultTierCriteria:     tierCriteria,
		LLMDefaultPrompt:                  complexity.DefaultLLMClassifierGuidance(),
	})
}

// sendModelRouterNotServed answers for a router the routing plugin does not
// serve. A saved router can be left out at startup, as session routing
// without a KV store is, so the store decides: a saved router is reported as
// not loaded (503), and only one that is not saved as not found (404).
func (h *RoutingHandler) sendModelRouterNotServed(ctx *fasthttp.RequestCtx, id string) {
	if _, err := h.configStore.GetModelRouter(ctx, id); errors.Is(err, configstore.ErrNotFound) {
		SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("model router %q not found", id))
		return
	}
	SendError(ctx, fasthttp.StatusServiceUnavailable, fmt.Sprintf("model router %q is saved but not loaded; the gateway log says why (session routing needs a KV store)", id))
}

// retryModelRouterWarmup restarts one router's failed semantic warmup.
func (h *RoutingHandler) retryModelRouterWarmup(ctx *fasthttp.RequestCtx) {
	id := pathID(ctx, "router_id")
	status, retried, err := h.routingManager.RetryModelRouterWarmup(ctx, id)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			h.sendModelRouterNotServed(ctx, id)
			return
		}
		SendError(ctx, fasthttp.StatusServiceUnavailable, fmt.Sprintf("failed to retry model router warmup: %v", err))
		return
	}
	if !retried {
		SendError(ctx, fasthttp.StatusConflict, "semantic complexity warmup can only be retried after a failure")
		return
	}
	lib.RecordAuditChange(ctx, "model_router.warmup_retried", "model_router", id, id, nil, map[string]any{"retried": true})
	SendJSONWithStatus(ctx, complexityStatusResponse{SemanticStatusInfo: status}, fasthttp.StatusAccepted)
}

// setModelRouterTargets replaces the model each tier of a router gets.
func (h *RoutingHandler) setModelRouterTargets(ctx *fasthttp.RequestCtx) {
	id := pathID(ctx, "router_id")
	// Decoded strictly, as the router's other writes are, and targets must be
	// there: a body that does not say what the targets are would otherwise
	// read as none and clear them. An explicit [] still clears them.
	var req ModelRouterTargetsRequest
	if !decodeStrictJSON(ctx, &req) {
		return
	}
	if req.Targets == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "targets is required; send [] to clear them")
		return
	}
	targets := req.Targets
	for i, t := range targets {
		if err := validateRoutingFallbacks(t.ParsedFallbacks); err != nil {
			SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("targets[%d]: %s", i, err.Error()))
			return
		}
	}
	if err := configstore.ValidateModelRouterTargets(targets); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	// Targets this version cannot read make the router unreadable; replacing
	// them repairs it, as saving or resetting its config does, so that read
	// error does not stop the save.
	before, err := h.configStore.GetModelRouter(ctx, id)
	if err != nil && !errors.Is(err, configstore.ErrConfigUnreadable) {
		sendModelRouterLoadError(ctx, id, err)
		return
	}
	var beforeTargets any
	if before != nil {
		beforeTargets = before.Targets
	}
	if err := h.configStore.SetModelRouterTargets(ctx, id, targets); err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("model router %q not found", id))
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to save model router targets: %v", err))
		return
	}
	if err := h.routingManager.ReloadModelRouterTargets(ctx, id); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to reload model router targets in memory: %v, please restart bifrost to sync with the database", err))
		return
	}
	saved, err := h.configStore.GetModelRouter(ctx, id)
	if err != nil {
		sendModelRouterLoadError(ctx, id, err)
		return
	}
	lib.RecordAuditChange(ctx, "model_router.targets_updated", "model_router", saved.ID, saved.Name, beforeTargets, saved.Targets)
	SendJSON(ctx, saved)
}

// prepareModelRouterRouting validates a router's routing for saving: its
// structure, its condition and, when checkScopeEntity is set, that its scope's
// entity exists. An update that leaves the scope as stored does not look the
// entity up again, as a routing rule update does not: a router scoped to a
// team, customer or virtual key that was deleted since can still be turned
// off, reordered or given another condition.
func (h *RoutingHandler) prepareModelRouterRouting(ctx *fasthttp.RequestCtx, routing *configstore.ModelRouterRouting, checkScopeEntity bool) bool {
	if err := configstore.ValidateModelRouterRouting(routing); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return false
	}
	if err := rules.ValidateModelRouterCEL(routing.CelExpression); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("invalid CEL expression: %s", err.Error()))
		return false
	}
	if checkScopeEntity && routing.ScopeID != nil {
		if err := h.validateRoutingScopeID(ctx, routing.Scope, *routing.ScopeID); err != nil {
			sendRoutingScopeIDValidationError(ctx, err)
			return false
		}
	}
	return true
}

// modelRouterRoutingUpdate is a routing update body. Each field it sets
// replaces the stored value; an omitted field keeps it, as in a routing rule
// update.
type modelRouterRoutingUpdate struct {
	Enabled       *bool          `json:"enabled"`
	Scope         *string        `json:"scope"`
	ScopeID       *string        `json:"scope_id"`
	CelExpression *string        `json:"cel_expression"`
	Query         map[string]any `json:"query"`
	Priority      *int           `json:"priority"`
}

// setModelRouterRouting changes which requests a router takes: whether it is
// on, its scope, its condition and its priority within the scope. Only the
// fields the body sends change, so a switch that sends {"enabled": ...} never
// writes back a scope, condition or priority it read earlier. The builder
// state describes the condition, so a condition sent without one clears the
// stored state.
func (h *RoutingHandler) setModelRouterRouting(ctx *fasthttp.RequestCtx) {
	id := pathID(ctx, "router_id")
	var update modelRouterRoutingUpdate
	if !decodeStrictJSON(ctx, &update) {
		return
	}
	before, err := h.configStore.GetModelRouter(ctx, id)
	if err != nil {
		sendModelRouterLoadError(ctx, id, err)
		return
	}
	var routing configstore.ModelRouterRouting
	if before.Routing != nil {
		routing = *before.Routing
	}
	if update.Enabled != nil {
		routing.Enabled = *update.Enabled
	}
	if update.Scope != nil {
		routing.Scope = *update.Scope
	}
	if update.ScopeID != nil {
		routing.ScopeID = update.ScopeID
	}
	if update.CelExpression != nil {
		routing.CelExpression = *update.CelExpression
		routing.Query = update.Query
	} else if update.Query != nil {
		routing.Query = update.Query
	}
	if update.Priority != nil {
		routing.Priority = *update.Priority
	}
	// The scope's entity is looked up only when this update names the scope.
	if !h.prepareModelRouterRouting(ctx, &routing, update.Scope != nil || update.ScopeID != nil) {
		return
	}
	if err := h.configStore.SetModelRouterRouting(ctx, id, routing); err != nil {
		switch {
		case errors.Is(err, configstore.ErrModelRouterPriorityTaken):
			SendError(ctx, fasthttp.StatusConflict, fmt.Sprintf("%v; use a different priority", err))
		case errors.Is(err, configstore.ErrNotFound):
			SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("model router %q not found", id))
		default:
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to save model router routing: %v", err))
		}
		return
	}
	if err := h.routingManager.ReloadModelRouterRouting(ctx, id); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to reload model router routing in memory: %v, please restart bifrost to sync with the database", err))
		return
	}
	saved, err := h.configStore.GetModelRouter(ctx, id)
	if err != nil {
		sendModelRouterLoadError(ctx, id, err)
		return
	}
	lib.RecordAuditChange(ctx, "model_router.routing_updated", "model_router", saved.ID, saved.Name, before.Routing, saved.Routing)
	SendJSON(ctx, saved)
}
