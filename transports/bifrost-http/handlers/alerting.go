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

// AlertingHandler handles alert rule, channel, and history endpoints.
type AlertingHandler struct {
	store configstore.ConfigStore
	rbac  *RBACMiddleware
}

// NewAlertingHandler creates a new alerting handler.
func NewAlertingHandler(store configstore.ConfigStore, rbac *RBACMiddleware) *AlertingHandler {
	return &AlertingHandler{store: store, rbac: rbac}
}

// RegisterRoutes wires all alerting HTTP endpoints.
func (h *AlertingHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	adminMW := append([]schemas.BifrostHTTPMiddleware{h.rbac.RequireRole("admin")}, middlewares...)

	// Rules
	r.POST("/api/enterprise/alerts/rules", lib.ChainMiddlewares(h.createRule, adminMW...))
	r.GET("/api/enterprise/alerts/rules", lib.ChainMiddlewares(h.listRules, adminMW...))
	r.GET("/api/enterprise/alerts/rules/{id}", lib.ChainMiddlewares(h.getRule, adminMW...))
	r.PUT("/api/enterprise/alerts/rules/{id}", lib.ChainMiddlewares(h.updateRule, adminMW...))
	r.DELETE("/api/enterprise/alerts/rules/{id}", lib.ChainMiddlewares(h.deleteRule, adminMW...))
	// Channels
	r.POST("/api/enterprise/alerts/channels", lib.ChainMiddlewares(h.createChannel, adminMW...))
	r.GET("/api/enterprise/alerts/channels", lib.ChainMiddlewares(h.listChannels, adminMW...))
	r.GET("/api/enterprise/alerts/channels/{id}", lib.ChainMiddlewares(h.getChannel, adminMW...))
	r.PUT("/api/enterprise/alerts/channels/{id}", lib.ChainMiddlewares(h.updateChannel, adminMW...))
	r.DELETE("/api/enterprise/alerts/channels/{id}", lib.ChainMiddlewares(h.deleteChannel, adminMW...))
	// States & History
	r.GET("/api/enterprise/alerts/states", lib.ChainMiddlewares(h.listStates, adminMW...))
	r.GET("/api/enterprise/alerts/history", lib.ChainMiddlewares(h.queryHistory, adminMW...))
}

func (h *AlertingHandler) requireFeature(ctx *fasthttp.RequestCtx) bool {
	if !license.IsFeatureEnabled(license.FeatureAlerts) {
		SendError(ctx, fasthttp.StatusPaymentRequired, "alerting feature not included in current license")
		return false
	}
	return true
}

// ─── Alert Rules ─────────────────────────────────────────────────────────────

func (h *AlertingHandler) createRule(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	var r tables.TableAlertRule
	if err := json.Unmarshal(ctx.PostBody(), &r); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	if err := h.store.CreateAlertRule(ctx, &r); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSONWithStatus(ctx, r, fasthttp.StatusCreated)
}

func (h *AlertingHandler) listRules(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	rules, err := h.store.ListAlertRules(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSON(ctx, rules)
}

func (h *AlertingHandler) getRule(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	id := ctx.UserValue("id").(string)
	r, err := h.store.GetAlertRule(ctx, id)
	if err != nil {
		SendError(ctx, fasthttp.StatusNotFound, err.Error())
		return
	}
	state, _ := h.store.GetAlertState(ctx, id)
	SendJSON(ctx, map[string]any{"rule": r, "state": state})
}

func (h *AlertingHandler) updateRule(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	id := ctx.UserValue("id").(string)
	var updates map[string]any
	if err := json.Unmarshal(ctx.PostBody(), &updates); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	if err := h.store.UpdateAlertRule(ctx, id, updates); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSON(ctx, map[string]any{"status": "updated"})
}

func (h *AlertingHandler) deleteRule(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	id := ctx.UserValue("id").(string)
	if err := h.store.DeleteAlertRule(ctx, id); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSON(ctx, map[string]any{"status": "deleted"})
}

// ─── Alert Channels ───────────────────────────────────────────────────────────

func (h *AlertingHandler) createChannel(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	var c tables.TableAlertChannel
	if err := json.Unmarshal(ctx.PostBody(), &c); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	if err := h.store.CreateAlertChannel(ctx, &c); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSONWithStatus(ctx, c, fasthttp.StatusCreated)
}

func (h *AlertingHandler) listChannels(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	channels, err := h.store.ListAlertChannels(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSON(ctx, channels)
}

func (h *AlertingHandler) getChannel(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	id := ctx.UserValue("id").(string)
	c, err := h.store.GetAlertChannel(ctx, id)
	if err != nil {
		SendError(ctx, fasthttp.StatusNotFound, err.Error())
		return
	}
	SendJSON(ctx, c)
}

func (h *AlertingHandler) updateChannel(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	id := ctx.UserValue("id").(string)
	var updates map[string]any
	if err := json.Unmarshal(ctx.PostBody(), &updates); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	if err := h.store.UpdateAlertChannel(ctx, id, updates); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSON(ctx, map[string]any{"status": "updated"})
}

func (h *AlertingHandler) deleteChannel(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	id := ctx.UserValue("id").(string)
	if err := h.store.DeleteAlertChannel(ctx, id); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSON(ctx, map[string]any{"status": "deleted"})
}

// ─── States & History ─────────────────────────────────────────────────────────

func (h *AlertingHandler) listStates(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	states, err := h.store.ListAlertStates(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSON(ctx, states)
}

func (h *AlertingHandler) queryHistory(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	opts := configstore.AlertHistoryQueryOpts{
		RuleID:   string(ctx.QueryArgs().Peek("rule_id")),
		Severity: string(ctx.QueryArgs().Peek("severity")),
		Page:     ctx.QueryArgs().GetUintOrZero("page"),
		PageSize: ctx.QueryArgs().GetUintOrZero("page_size"),
	}
	if s := string(ctx.QueryArgs().Peek("start")); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil { opts.Start = t }
	}
	if e := string(ctx.QueryArgs().Peek("end")); e != "" {
		if t, err := time.Parse(time.RFC3339, e); err == nil { opts.End = t }
	}
	history, total, err := h.store.QueryAlertHistory(ctx, opts)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSON(ctx, map[string]any{"data": history, "total": total})
}
