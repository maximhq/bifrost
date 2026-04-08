package handlers

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/license"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// GuardrailsHandler handles guardrail policy and violation endpoints.
type GuardrailsHandler struct {
	store configstore.ConfigStore
	rbac  *RBACMiddleware
}

// NewGuardrailsHandler creates a new handler backed by the given config store.
func NewGuardrailsHandler(store configstore.ConfigStore, rbac *RBACMiddleware) *GuardrailsHandler {
	return &GuardrailsHandler{store: store, rbac: rbac}
}

// RegisterRoutes wires all guardrail HTTP endpoints.
func (h *GuardrailsHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	adminMW := append([]schemas.BifrostHTTPMiddleware{h.rbac.RequireRole("admin")}, middlewares...)

	r.POST("/api/enterprise/guardrails/policies", lib.ChainMiddlewares(h.createPolicy, adminMW...))
	r.GET("/api/enterprise/guardrails/policies", lib.ChainMiddlewares(h.listPolicies, adminMW...))
	r.GET("/api/enterprise/guardrails/policies/{id}", lib.ChainMiddlewares(h.getPolicy, adminMW...))
	r.PUT("/api/enterprise/guardrails/policies/{id}", lib.ChainMiddlewares(h.updatePolicy, adminMW...))
	r.DELETE("/api/enterprise/guardrails/policies/{id}", lib.ChainMiddlewares(h.deletePolicy, adminMW...))

	r.POST("/api/enterprise/guardrails/policies/{id}/rules", lib.ChainMiddlewares(h.createRule, adminMW...))
	r.GET("/api/enterprise/guardrails/policies/{id}/rules", lib.ChainMiddlewares(h.listRules, adminMW...))
	r.DELETE("/api/enterprise/guardrails/rules/{ruleId}", lib.ChainMiddlewares(h.deleteRule, adminMW...))

	r.GET("/api/enterprise/guardrails/violations", lib.ChainMiddlewares(h.queryViolations, adminMW...))
}

func (h *GuardrailsHandler) requireFeature(ctx *fasthttp.RequestCtx) bool {
	if !license.IsFeatureEnabled(license.FeatureGuardrails) {
		SendError(ctx, fasthttp.StatusPaymentRequired, "guardrails feature not included in current license")
		return false
	}
	return true
}

func (h *GuardrailsHandler) createPolicy(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	var p tables.TableGuardrailPolicy
	if err := json.Unmarshal(ctx.PostBody(), &p); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("invalid body: %v", err))
		return
	}
	if err := h.store.CreateGuardrailPolicy(ctx, &p); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSONWithStatus(ctx, p, fasthttp.StatusCreated)
}

func (h *GuardrailsHandler) listPolicies(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	policies, err := h.store.ListGuardrailPolicies(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSON(ctx, policies)
}

func (h *GuardrailsHandler) getPolicy(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	id := ctx.UserValue("id").(string)
	p, err := h.store.GetGuardrailPolicy(ctx, id)
	if err != nil {
		SendError(ctx, fasthttp.StatusNotFound, err.Error())
		return
	}
	rules, _ := h.store.ListGuardrailRules(ctx, id)
	SendJSON(ctx, map[string]any{"policy": p, "rules": rules})
}

func (h *GuardrailsHandler) updatePolicy(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	id := ctx.UserValue("id").(string)
	var updates map[string]any
	if err := json.Unmarshal(ctx.PostBody(), &updates); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	if err := h.store.UpdateGuardrailPolicy(ctx, id, updates); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSON(ctx, map[string]any{"status": "updated"})
}

func (h *GuardrailsHandler) deletePolicy(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	id := ctx.UserValue("id").(string)
	if err := h.store.DeleteGuardrailPolicy(ctx, id); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSON(ctx, map[string]any{"status": "deleted"})
}

func (h *GuardrailsHandler) createRule(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	policyID := ctx.UserValue("id").(string)
	var r tables.TableGuardrailRule
	if err := json.Unmarshal(ctx.PostBody(), &r); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	r.PolicyID = policyID
	if err := h.store.CreateGuardrailRule(ctx, &r); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSONWithStatus(ctx, r, fasthttp.StatusCreated)
}

func (h *GuardrailsHandler) listRules(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	policyID := ctx.UserValue("id").(string)
	rules, err := h.store.ListGuardrailRules(ctx, policyID)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSON(ctx, rules)
}

func (h *GuardrailsHandler) deleteRule(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	ruleID := ctx.UserValue("ruleId").(string)
	if err := h.store.DeleteGuardrailRule(ctx, ruleID); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSON(ctx, map[string]any{"status": "deleted"})
}

func (h *GuardrailsHandler) queryViolations(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	opts := configstore.GuardrailViolationQueryOpts{
		PolicyID: string(ctx.QueryArgs().Peek("policy_id")),
		Layer:    string(ctx.QueryArgs().Peek("layer")),
		Action:   string(ctx.QueryArgs().Peek("action")),
		Page:     ctx.QueryArgs().GetUintOrZero("page"),
		PageSize: ctx.QueryArgs().GetUintOrZero("page_size"),
	}
	if s := string(ctx.QueryArgs().Peek("start")); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil { opts.Start = t }
	}
	if e := string(ctx.QueryArgs().Peek("end")); e != "" {
		if t, err := time.Parse(time.RFC3339, e); err == nil { opts.End = t }
	}
	violations, total, err := h.store.QueryGuardrailViolations(ctx, opts)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSON(ctx, map[string]any{"data": violations, "total": total})
}


