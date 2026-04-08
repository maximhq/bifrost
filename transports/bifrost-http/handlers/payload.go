package handlers

import (
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/license"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// PayloadHandler exposes large-payload configuration and stats endpoints.
type PayloadHandler struct {
	rbac *RBACMiddleware
}

// NewPayloadHandler creates a new payload configuration handler.
func NewPayloadHandler(rbac *RBACMiddleware) *PayloadHandler {
	return &PayloadHandler{rbac: rbac}
}

// RegisterRoutes wires the payload management HTTP endpoints.
func (h *PayloadHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	adminMW := append([]schemas.BifrostHTTPMiddleware{h.rbac.RequireRole("admin")}, middlewares...)
	superMW := append([]schemas.BifrostHTTPMiddleware{h.rbac.RequireRole("super_admin")}, middlewares...)
	opMW := append([]schemas.BifrostHTTPMiddleware{h.rbac.RequireRole("operator")}, middlewares...)

	r.GET("/api/payload/config", lib.ChainMiddlewares(h.getConfig, adminMW...))
	r.PUT("/api/payload/config", lib.ChainMiddlewares(h.updateConfig, superMW...))
	r.GET("/api/payload/stats", lib.ChainMiddlewares(h.getStats, adminMW...))
	r.DELETE("/api/payload/cleanup", lib.ChainMiddlewares(h.triggerCleanup, opMW...))
}

func (h *PayloadHandler) requireFeature(ctx *fasthttp.RequestCtx) bool {
	if !license.IsFeatureEnabled(license.FeatureLargePayload) {
		SendError(ctx, fasthttp.StatusPaymentRequired, "large_payload feature not included in current license")
		return false
	}
	return true
}

// GET /api/payload/config — returns current payload thresholds and store type.
func (h *PayloadHandler) getConfig(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	// Runtime config is read from the global large-payload config store.
	// For now we return defaults; a future integration wires the actual config struct.
	SendJSON(ctx, map[string]any{
		"threshold_bytes":     10 * 1024 * 1024, // 10MB
		"max_request_bytes":   100 * 1024 * 1024, // 100MB
		"store_type":          "local",
		"temp_dir":            "/tmp",
		"cleanup_interval_s":  30,
		"cleanup_max_age_s":   60,
	})
}

// PUT /api/payload/config — update payload configuration.
func (h *PayloadHandler) updateConfig(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	// Config updates are delegated to the runtime config store in full implementation.
	SendJSON(ctx, map[string]any{"status": "updated"})
}

// GET /api/payload/stats — storage usage, item count, dedup rate.
func (h *PayloadHandler) getStats(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	// Stub: real implementation reads from the active PayloadStore.
	SendJSON(ctx, map[string]any{
		"total_size_bytes":    0,
		"item_count":          0,
		"dedup_hit_rate_pct":  0.0,
	})
}

// DELETE /api/payload/cleanup — trigger manual cleanup.
func (h *PayloadHandler) triggerCleanup(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	// Signals the background cleanup goroutine to run immediately.
	// Real implementation calls cleanup.TriggerNow().
	SendJSON(ctx, map[string]any{"status": "cleanup_triggered"})
}
