package handlers

import (
	"encoding/json"
	"fmt"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/license"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// AuditHandler manages HTTP endpoints for audit log queries.
type AuditHandler struct {
	store configstore.ConfigStore
	rbac  *RBACMiddleware
}

// NewAuditHandler creates a new audit handler.
func NewAuditHandler(store configstore.ConfigStore, rbac *RBACMiddleware) *AuditHandler {
	return &AuditHandler{store: store, rbac: rbac}
}

// RegisterRoutes registers audit log endpoints.
func (h *AuditHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	adminMW := append([]schemas.BifrostHTTPMiddleware{h.rbac.RequireRole("admin")}, middlewares...)

	r.GET("/api/audit/logs", lib.ChainMiddlewares(h.queryLogs, adminMW...))
	r.GET("/api/audit/logs/{id}", lib.ChainMiddlewares(h.getLog, adminMW...))
	r.POST("/api/audit/verify", lib.ChainMiddlewares(h.verifyChain, adminMW...))
}

func (h *AuditHandler) requireFeature(ctx *fasthttp.RequestCtx) bool {
	if !license.IsFeatureEnabled(license.FeatureAuditLogs) {
		SendError(ctx, fasthttp.StatusPaymentRequired, "audit_logs feature not included in current license")
		return false
	}
	return true
}

// GET /api/audit/logs
func (h *AuditHandler) queryLogs(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) {
		return
	}

	opts := configstore.AuditLogQueryOpts{
		ActorID:    string(ctx.QueryArgs().Peek("actor_id")),
		Resource:   string(ctx.QueryArgs().Peek("resource")),
		ResourceID: string(ctx.QueryArgs().Peek("resource_id")),
		Action:     string(ctx.QueryArgs().Peek("action")),
	}
	if ps := ctx.QueryArgs().Peek("page_size"); len(ps) > 0 {
		opts.PageSize = ctx.QueryArgs().GetUintOrZero("page_size")
	}
	if p := ctx.QueryArgs().Peek("page"); len(p) > 0 {
		opts.Page = ctx.QueryArgs().GetUintOrZero("page")
	}

	logs, total, err := h.store.QueryAuditLogs(ctx, opts)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to query audit logs: %v", err))
		return
	}
	SendJSON(ctx, map[string]any{
		"total": total,
		"data":  logs,
	})
}

// GET /api/audit/logs/{id}
func (h *AuditHandler) getLog(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) {
		return
	}
	id := ctx.UserValue("id").(string)
	logs, _, err := h.store.QueryAuditLogs(ctx, configstore.AuditLogQueryOpts{
		ResourceID: id,
		PageSize:   1,
		Page:       1,
	})
	if err != nil || len(logs) == 0 {
		SendError(ctx, fasthttp.StatusNotFound, "audit log entry not found")
		return
	}
	SendJSON(ctx, logs[0])
}

// POST /api/audit/verify — verify the hash chain integrity
func (h *AuditHandler) verifyChain(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) {
		return
	}
	var req struct {
		FromSeq int64 `json:"from_seq"`
	}
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		req.FromSeq = 0
	}
	brokenAt, err := h.store.VerifyAuditChain(ctx, req.FromSeq)
	if err != nil {
		SendJSON(ctx, map[string]any{
			"valid":     false,
			"broken_at": brokenAt,
			"error":     err.Error(),
		})
		return
	}
	SendJSON(ctx, map[string]any{
		"valid":     true,
		"broken_at": -1,
	})
}
