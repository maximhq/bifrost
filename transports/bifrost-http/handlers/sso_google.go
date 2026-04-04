package handlers

import (
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
)

// GoogleSSOHandler manages HTTP requests for Google OAuth SSO login.
type GoogleSSOHandler struct {
	configStore configstore.ConfigStore
}

// NewGoogleSSOHandler creates a new Google SSO handler instance.
func NewGoogleSSOHandler(configStore configstore.ConfigStore) *GoogleSSOHandler {
	return &GoogleSSOHandler{configStore: configStore}
}

// RegisterRoutes registers the Google SSO routes.
// Actual route implementations will be provided by the SSO login flow unit.
func (h *GoogleSSOHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	// Routes will be registered here once the login/callback handlers are implemented.
}
