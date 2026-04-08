package handlers

import (
	"encoding/json"
	"time"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/license"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// AdaptiveRoutingHandler handles routing policy and provider metrics endpoints.
type AdaptiveRoutingHandler struct {
	store configstore.ConfigStore
	rbac  *RBACMiddleware
}

// NewAdaptiveRoutingHandler creates a new adaptive routing handler.
func NewAdaptiveRoutingHandler(store configstore.ConfigStore, rbac *RBACMiddleware) *AdaptiveRoutingHandler {
	return &AdaptiveRoutingHandler{store: store, rbac: rbac}
}

// RegisterRoutes wires all adaptive routing HTTP endpoints.
func (h *AdaptiveRoutingHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	adminMW := append([]schemas.BifrostHTTPMiddleware{h.rbac.RequireRole("admin")}, middlewares...)

	r.POST("/api/enterprise/routing/policies", lib.ChainMiddlewares(h.createPolicy, adminMW...))
	r.GET("/api/enterprise/routing/policies", lib.ChainMiddlewares(h.listPolicies, adminMW...))
	r.GET("/api/enterprise/routing/policies/{id}", lib.ChainMiddlewares(h.getPolicy, adminMW...))
	r.PUT("/api/enterprise/routing/policies/{id}", lib.ChainMiddlewares(h.updatePolicy, adminMW...))
	r.DELETE("/api/enterprise/routing/policies/{id}", lib.ChainMiddlewares(h.deletePolicy, adminMW...))

	r.GET("/api/enterprise/routing/metrics", lib.ChainMiddlewares(h.listMetrics, adminMW...))
	r.GET("/api/enterprise/routing/quality-scores", lib.ChainMiddlewares(h.listQualityScores, adminMW...))
	r.PUT("/api/enterprise/routing/quality-scores", lib.ChainMiddlewares(h.upsertQualityScore, adminMW...))
}

func (h *AdaptiveRoutingHandler) requireFeature(ctx *fasthttp.RequestCtx) bool {
	if !license.IsFeatureEnabled(license.FeatureAdaptiveRouting) {
		SendError(ctx, fasthttp.StatusPaymentRequired, "adaptive_routing feature not included in current license")
		return false
	}
	return true
}

func (h *AdaptiveRoutingHandler) createPolicy(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	var p tables.TableRoutingPolicy
	if err := json.Unmarshal(ctx.PostBody(), &p); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	if err := h.store.CreateRoutingPolicy(ctx, &p); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSONWithStatus(ctx, p, fasthttp.StatusCreated)
}

func (h *AdaptiveRoutingHandler) listPolicies(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	policies, err := h.store.ListRoutingPolicies(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSON(ctx, policies)
}

func (h *AdaptiveRoutingHandler) getPolicy(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	id := ctx.UserValue("id").(string)
	p, err := h.store.GetRoutingPolicy(ctx, id)
	if err != nil {
		SendError(ctx, fasthttp.StatusNotFound, err.Error())
		return
	}
	SendJSON(ctx, p)
}

func (h *AdaptiveRoutingHandler) updatePolicy(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	id := ctx.UserValue("id").(string)
	var updates map[string]any
	if err := json.Unmarshal(ctx.PostBody(), &updates); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	if err := h.store.UpdateRoutingPolicy(ctx, id, updates); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSON(ctx, map[string]any{"status": "updated"})
}

func (h *AdaptiveRoutingHandler) deletePolicy(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	id := ctx.UserValue("id").(string)
	if err := h.store.DeleteRoutingPolicy(ctx, id); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSON(ctx, map[string]any{"status": "deleted"})
}

func (h *AdaptiveRoutingHandler) listMetrics(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	provider := string(ctx.QueryArgs().Peek("provider"))
	model := string(ctx.QueryArgs().Peek("model"))
	windowMinutes := ctx.QueryArgs().GetUintOrZero("window_minutes")
	if windowMinutes == 0 {
		windowMinutes = 60
	}
	sinceHours := ctx.QueryArgs().GetUintOrZero("since_hours")
	if sinceHours == 0 {
		sinceHours = 24
	}
	since := time.Now().Add(-time.Duration(sinceHours) * time.Hour)
	metrics, err := h.store.GetProviderMetrics(ctx, provider, model, windowMinutes, since)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSON(ctx, metrics)
}

func (h *AdaptiveRoutingHandler) listQualityScores(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	scores, err := h.store.ListModelQualityScores(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSON(ctx, scores)
}

func (h *AdaptiveRoutingHandler) upsertQualityScore(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	var score tables.TableModelQualityScore
	if err := json.Unmarshal(ctx.PostBody(), &score); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	if err := h.store.UpsertModelQualityScore(ctx, &score); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSON(ctx, score)
}
