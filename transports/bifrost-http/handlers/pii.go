package handlers

import (
	"encoding/json"
	"fmt"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/license"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// PIIHandler handles PII redaction policy endpoints.
type PIIHandler struct {
	store configstore.ConfigStore
	rbac  *RBACMiddleware
}

// NewPIIHandler creates a new PII redaction handler.
func NewPIIHandler(store configstore.ConfigStore, rbac *RBACMiddleware) *PIIHandler {
	return &PIIHandler{store: store, rbac: rbac}
}

// RegisterRoutes wires all PII redaction HTTP endpoints.
func (h *PIIHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	adminMW := append([]schemas.BifrostHTTPMiddleware{h.rbac.RequireRole("admin")}, middlewares...)

	r.POST("/api/enterprise/pii/policies", lib.ChainMiddlewares(h.createPolicy, adminMW...))
	r.GET("/api/enterprise/pii/policies", lib.ChainMiddlewares(h.listPolicies, adminMW...))
	r.GET("/api/enterprise/pii/policies/{id}", lib.ChainMiddlewares(h.getPolicy, adminMW...))
	r.PUT("/api/enterprise/pii/policies/{id}", lib.ChainMiddlewares(h.updatePolicy, adminMW...))
	r.DELETE("/api/enterprise/pii/policies/{id}", lib.ChainMiddlewares(h.deletePolicy, adminMW...))

	r.POST("/api/enterprise/pii/policies/{id}/rules", lib.ChainMiddlewares(h.createRule, adminMW...))
	r.GET("/api/enterprise/pii/policies/{id}/rules", lib.ChainMiddlewares(h.listRules, adminMW...))
	r.DELETE("/api/enterprise/pii/rules/{ruleId}", lib.ChainMiddlewares(h.deleteRule, adminMW...))
}

func (h *PIIHandler) requireFeature(ctx *fasthttp.RequestCtx) bool {
	if !license.IsFeatureEnabled(license.FeaturePIIRedactor) {
		SendError(ctx, fasthttp.StatusPaymentRequired, "pii_redactor feature not included in current license")
		return false
	}
	return true
}

func (h *PIIHandler) createPolicy(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	var p tables.TablePIIPolicy
	if err := json.Unmarshal(ctx.PostBody(), &p); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("invalid body: %v", err))
		return
	}
	if err := h.store.CreatePIIPolicy(ctx, &p); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSONWithStatus(ctx, p, fasthttp.StatusCreated)
}

func (h *PIIHandler) listPolicies(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	policies, err := h.store.ListPIIPolicies(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSON(ctx, policies)
}

func (h *PIIHandler) getPolicy(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	id := ctx.UserValue("id").(string)
	p, err := h.store.GetPIIPolicy(ctx, id)
	if err != nil {
		SendError(ctx, fasthttp.StatusNotFound, err.Error())
		return
	}
	rules, _ := h.store.ListPIIDetectorRules(ctx, id)
	SendJSON(ctx, map[string]any{"policy": p, "rules": rules})
}

func (h *PIIHandler) updatePolicy(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	id := ctx.UserValue("id").(string)
	var updates map[string]any
	if err := json.Unmarshal(ctx.PostBody(), &updates); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	if err := h.store.UpdatePIIPolicy(ctx, id, updates); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSON(ctx, map[string]any{"status": "updated"})
}

func (h *PIIHandler) deletePolicy(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	id := ctx.UserValue("id").(string)
	if err := h.store.DeletePIIPolicy(ctx, id); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSON(ctx, map[string]any{"status": "deleted"})
}

func (h *PIIHandler) createRule(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	policyID := ctx.UserValue("id").(string)
	var r tables.TablePIIDetectorRule
	if err := json.Unmarshal(ctx.PostBody(), &r); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	r.PolicyID = policyID
	if err := h.store.CreatePIIDetectorRule(ctx, &r); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSONWithStatus(ctx, r, fasthttp.StatusCreated)
}

func (h *PIIHandler) listRules(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	policyID := ctx.UserValue("id").(string)
	rules, err := h.store.ListPIIDetectorRules(ctx, policyID)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSON(ctx, rules)
}

func (h *PIIHandler) deleteRule(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	ruleID := ctx.UserValue("ruleId").(string)
	if err := h.store.DeletePIIDetectorRule(ctx, ruleID); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
		return
	}
	SendJSON(ctx, map[string]any{"status": "deleted"})
}
