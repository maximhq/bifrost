package handlers

import (
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/license"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// LicenseHandler serves license status and feature flag endpoints.
type LicenseHandler struct {
	rbac *RBACMiddleware
}

// NewLicenseHandler creates a new license handler.
func NewLicenseHandler(rbac *RBACMiddleware) *LicenseHandler {
	return &LicenseHandler{rbac: rbac}
}

// RegisterRoutes wires the license HTTP endpoints.
func (h *LicenseHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	// GET /api/license — no auth required (public)
	r.GET("/api/license", lib.ChainMiddlewares(h.getStatus, middlewares...))
	// GET /api/license/features — authenticated
	r.GET("/api/license/features", lib.ChainMiddlewares(h.listFeatures, middlewares...))
}

// GET /api/license
// Returns the current license tier, validity, and expiry. No auth required.
// Raw JWT is never exposed.
func (h *LicenseHandler) getStatus(ctx *fasthttp.RequestCtx) {
	status := license.GetStatus()
	SendJSON(ctx, status)
}

// GET /api/license/features
// Returns all 15 enterprise feature flags as a map[string]bool.
func (h *LicenseHandler) listFeatures(ctx *fasthttp.RequestCtx) {
	features := map[string]bool{
		license.FeatureRBAC:            license.IsFeatureEnabled(license.FeatureRBAC),
		license.FeatureAuditLogs:       license.IsFeatureEnabled(license.FeatureAuditLogs),
		license.FeatureGuardrails:      license.IsFeatureEnabled(license.FeatureGuardrails),
		license.FeaturePIIRedactor:     license.IsFeatureEnabled(license.FeaturePIIRedactor),
		license.FeatureSSOOIDC:         license.IsFeatureEnabled(license.FeatureSSOOIDC),
		license.FeatureSSOSAML:         license.IsFeatureEnabled(license.FeatureSSOSAML),
		license.FeatureSCIM:            license.IsFeatureEnabled(license.FeatureSCIM),
		license.FeatureAdaptiveRouting: license.IsFeatureEnabled(license.FeatureAdaptiveRouting),
		license.FeatureClustering:      license.IsFeatureEnabled(license.FeatureClustering),
		license.FeatureVault:           license.IsFeatureEnabled(license.FeatureVault),
		license.FeatureAlerts:          license.IsFeatureEnabled(license.FeatureAlerts),
		license.FeatureLargePayload:    license.IsFeatureEnabled(license.FeatureLargePayload),
		license.FeatureMCPToolGroups:   license.IsFeatureEnabled(license.FeatureMCPToolGroups),
		license.FeatureUserGroups:      license.IsFeatureEnabled(license.FeatureUserGroups),
		license.FeatureDataConnectors:  license.IsFeatureEnabled(license.FeatureDataConnectors),
	}
	SendJSON(ctx, features)
}
