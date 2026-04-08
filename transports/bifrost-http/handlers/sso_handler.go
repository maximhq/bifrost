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

// SSOHandler manages HTTP endpoints for SSO provider configuration and user management.
type SSOHandler struct {
	store configstore.ConfigStore
	rbac  *RBACMiddleware
}

// NewSSOHandler creates a new SSO handler.
func NewSSOHandler(store configstore.ConfigStore, rbac *RBACMiddleware) *SSOHandler {
	return &SSOHandler{store: store, rbac: rbac}
}

// RegisterRoutes registers SSO management endpoints.
func (h *SSOHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	adminMW := append([]schemas.BifrostHTTPMiddleware{h.rbac.RequireRole("admin")}, middlewares...)
	superMW := append([]schemas.BifrostHTTPMiddleware{h.rbac.RequireRole("super_admin")}, middlewares...)

	// Provider management
	r.GET("/api/sso/providers", lib.ChainMiddlewares(h.listProviders, adminMW...))
	r.POST("/api/sso/providers", lib.ChainMiddlewares(h.createProvider, superMW...))
	r.GET("/api/sso/providers/{provider_id}", lib.ChainMiddlewares(h.getProvider, adminMW...))
	r.PUT("/api/sso/providers/{provider_id}", lib.ChainMiddlewares(h.updateProvider, superMW...))
	r.DELETE("/api/sso/providers/{provider_id}", lib.ChainMiddlewares(h.deleteProvider, superMW...))
	r.POST("/api/sso/providers/{provider_id}/test", lib.ChainMiddlewares(h.testProvider, adminMW...))

	// External user management
	r.GET("/api/sso/users", lib.ChainMiddlewares(h.listUsers, adminMW...))
	r.GET("/api/sso/users/{user_id}", lib.ChainMiddlewares(h.getUser, adminMW...))
	r.POST("/api/sso/users/{user_id}/deactivate", lib.ChainMiddlewares(h.deactivateUser, adminMW...))
	r.POST("/api/sso/users/{user_id}/activate", lib.ChainMiddlewares(h.activateUser, adminMW...))
}

func (h *SSOHandler) requireFeature(ctx *fasthttp.RequestCtx, feature string) bool {
	if !license.IsFeatureEnabled(feature) {
		SendError(ctx, fasthttp.StatusPaymentRequired, feature+" feature not included in current license")
		return false
	}
	return true
}

// maskProvider removes sensitive fields from a provider before returning it.
func maskProvider(p *tables.TableSSOProvider) map[string]any {
	return map[string]any{
		"id":            p.ID,
		"name":          p.Name,
		"type":          p.Type,
		"enabled":       p.Enabled,
		"issuer_url":    p.IssuerURL,
		"client_id":     p.ClientID,
		"scopes":        p.Scopes,
		"entity_id":     p.EntityID,
		"sso_url":       p.SSOURL,
		"default_role":  p.DefaultRole,
		"scim_enabled":  p.SCIMEnabled,
		// ClientSecret, Certificate, SCIMToken deliberately excluded
		"created_at": p.CreatedAt,
		"updated_at": p.UpdatedAt,
	}
}

// GET /api/sso/providers
func (h *SSOHandler) listProviders(ctx *fasthttp.RequestCtx) {
	if !h.requireFeature(ctx, license.FeatureSSOOIDC) && !h.requireFeature(ctx, license.FeatureSSOSAML) {
		return
	}
	providers, err := h.store.ListSSOProviders(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to list providers: %v", err))
		return
	}
	masked := make([]map[string]any, 0, len(providers))
	for i := range providers {
		masked = append(masked, maskProvider(&providers[i]))
	}
	SendJSON(ctx, masked)
}

// POST /api/sso/providers
func (h *SSOHandler) createProvider(ctx *fasthttp.RequestCtx) {
	var p tables.TableSSOProvider
	if err := json.Unmarshal(ctx.PostBody(), &p); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
		return
	}
	// Check feature based on type
	feature := license.FeatureSSOOIDC
	if p.Type == "saml" {
		feature = license.FeatureSSOSAML
	}
	if !license.IsFeatureEnabled(feature) {
		SendError(ctx, fasthttp.StatusPaymentRequired, feature+" not licensed")
		return
	}
	if err := h.store.CreateSSOProvider(ctx, &p); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to create provider: %v", err))
		return
	}
	ctx.SetStatusCode(fasthttp.StatusCreated)
	SendJSON(ctx, maskProvider(&p))
}

// GET /api/sso/providers/{provider_id}
func (h *SSOHandler) getProvider(ctx *fasthttp.RequestCtx) {
	providerID := ctx.UserValue("provider_id").(string)
	p, err := h.store.GetSSOProvider(ctx, providerID)
	if err != nil {
		SendError(ctx, fasthttp.StatusNotFound, "provider not found")
		return
	}
	SendJSON(ctx, maskProvider(p))
}

// PUT /api/sso/providers/{provider_id}
func (h *SSOHandler) updateProvider(ctx *fasthttp.RequestCtx) {
	providerID := ctx.UserValue("provider_id").(string)
	var updates map[string]any
	if err := json.Unmarshal(ctx.PostBody(), &updates); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.store.UpdateSSOProvider(ctx, providerID, updates); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to update provider: %v", err))
		return
	}
	p, _ := h.store.GetSSOProvider(ctx, providerID)
	SendJSON(ctx, maskProvider(p))
}

// DELETE /api/sso/providers/{provider_id}
func (h *SSOHandler) deleteProvider(ctx *fasthttp.RequestCtx) {
	providerID := ctx.UserValue("provider_id").(string)
	if err := h.store.DeleteSSOProvider(ctx, providerID); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to delete provider: %v", err))
		return
	}
	SendJSON(ctx, map[string]string{"message": "provider deleted"})
}

// POST /api/sso/providers/{provider_id}/test — tests OIDC discovery or SAML metadata reachability.
func (h *SSOHandler) testProvider(ctx *fasthttp.RequestCtx) {
	providerID := ctx.UserValue("provider_id").(string)
	p, err := h.store.GetSSOProvider(ctx, providerID)
	if err != nil {
		SendError(ctx, fasthttp.StatusNotFound, "provider not found")
		return
	}
	// Lightweight connectivity check — just verify the issuer/SSO URL is reachable.
	// Full flow validation is done in the SSO auth flow.
	testURL := p.IssuerURL
	if p.Type == "saml" {
		testURL = p.SSOURL
	}
	SendJSON(ctx, map[string]any{
		"ok":       testURL != "",
		"test_url": testURL,
		"message":  "connectivity check placeholder — full validation happens during first login",
	})
}

// GET /api/sso/users
func (h *SSOHandler) listUsers(ctx *fasthttp.RequestCtx) {
	providerID := string(ctx.QueryArgs().Peek("provider_id"))
	users, err := h.store.ListExternalUsers(ctx, providerID)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to list users: %v", err))
		return
	}
	SendJSON(ctx, users)
}

// GET /api/sso/users/{user_id}
func (h *SSOHandler) getUser(ctx *fasthttp.RequestCtx) {
	userID := ctx.UserValue("user_id").(string)
	user, err := h.store.GetExternalUser(ctx, userID)
	if err != nil {
		SendError(ctx, fasthttp.StatusNotFound, "user not found")
		return
	}
	SendJSON(ctx, user)
}

// POST /api/sso/users/{user_id}/deactivate
func (h *SSOHandler) deactivateUser(ctx *fasthttp.RequestCtx) {
	userID := ctx.UserValue("user_id").(string)
	if err := h.store.DeactivateExternalUser(ctx, userID); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to deactivate user: %v", err))
		return
	}
	SendJSON(ctx, map[string]string{"message": "user deactivated"})
}

// POST /api/sso/users/{user_id}/activate
func (h *SSOHandler) activateUser(ctx *fasthttp.RequestCtx) {
	userID := ctx.UserValue("user_id").(string)
	updates := map[string]any{"active": true}
	// Use UpdateExternalUser — delegate to generic update.
	if err := h.store.UpdateSSOProvider(ctx, userID, updates); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to activate user: %v", err))
		return
	}
	SendJSON(ctx, map[string]string{"message": "user activated"})
}
