package handlers

import (
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
)

// SAMLHandler manages HTTP requests for SAML-based SSO login.
type SAMLHandler struct {
	configStore configstore.ConfigStore
}

// NewSAMLHandler creates a new SAML handler instance.
func NewSAMLHandler(configStore configstore.ConfigStore) *SAMLHandler {
	return &SAMLHandler{configStore: configStore}
}

// RegisterRoutes registers the SAML SSO routes.
// Actual route implementations will be provided by the SSO login flow unit.
func (h *SAMLHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	// Routes will be registered here once the login/callback handlers are implemented.
}
