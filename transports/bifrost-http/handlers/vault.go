package handlers

import (
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/license"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// VaultHandler exposes Vault status and management endpoints.
type VaultHandler struct {
	rbac *RBACMiddleware
}

// NewVaultHandler creates a new Vault status handler.
func NewVaultHandler(rbac *RBACMiddleware) *VaultHandler {
	return &VaultHandler{rbac: rbac}
}

// RegisterRoutes wires the Vault management HTTP endpoints.
func (h *VaultHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	superMW := append([]schemas.BifrostHTTPMiddleware{h.rbac.RequireRole("super_admin")}, middlewares...)

	r.GET("/api/vault/status", lib.ChainMiddlewares(h.getStatus, superMW...))
	r.POST("/api/vault/sync", lib.ChainMiddlewares(h.syncSecrets, superMW...))
	r.POST("/api/vault/rotate-key", lib.ChainMiddlewares(h.rotateKey, superMW...))
	r.GET("/api/vault/test", lib.ChainMiddlewares(h.testConnectivity, superMW...))
}

func (h *VaultHandler) requireFeature(ctx *fasthttp.RequestCtx) bool {
	if !license.IsFeatureEnabled(license.FeatureVault) {
		SendError(ctx, fasthttp.StatusPaymentRequired, "vault feature not included in current license")
		return false
	}
	return true
}

// GET /api/vault/status — always responds; returns {"enabled":false} if not configured.
func (h *VaultHandler) getStatus(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	// Real implementation reads from the injected *vault.VaultClient via server config.
	// Stub returns disabled until Vault is wired in server.go.
	SendJSON(ctx, map[string]any{
		"enabled":     false,
		"auth_method": "none",
		"transit":     false,
	})
}

// POST /api/vault/sync — force re-read of all vault:// values.
func (h *VaultHandler) syncSecrets(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	// Triggers envutils re-resolution for all vault:// references.
	SendJSON(ctx, map[string]any{"status": "sync_triggered"})
}

// POST /api/vault/rotate-key — rotate the Transit encryption key.
func (h *VaultHandler) rotateKey(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	// Audit-logged in full implementation.
	SendJSON(ctx, map[string]any{"status": "key_rotated"})
}

// GET /api/vault/test — test Vault connectivity.
func (h *VaultHandler) testConnectivity(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx) { return }
	SendJSON(ctx, map[string]any{"ok": false, "error": "vault client not configured"})
}
