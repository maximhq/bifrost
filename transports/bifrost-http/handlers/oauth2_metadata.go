// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file implements OAuth 2.0 metadata discovery endpoints per RFC 9728
// (Protected Resource Metadata) and RFC 8414 (Authorization Server Metadata).
// These endpoints enable MCP-spec-compliant clients (like Claude Code) to
// automatically discover Bifrost's OAuth configuration and authenticate.
package handlers

import (
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// OAuthMetadataHandler serves OAuth 2.0 discovery metadata endpoints.
// It provides the Protected Resource Metadata (RFC 9728) and Authorization
// Server Metadata (RFC 8414) that MCP clients use to discover how to
// authenticate with Bifrost's MCP server endpoint.
type OAuthMetadataHandler struct {
	store *lib.Config
}

// NewOAuthMetadataHandler creates a new OAuth metadata handler instance.
func NewOAuthMetadataHandler(store *lib.Config) *OAuthMetadataHandler {
	return &OAuthMetadataHandler{store: store}
}

// RegisterRoutes registers the well-known metadata discovery routes.
// These routes do NOT go through auth middleware since they must be
// accessible to unauthenticated clients during OAuth discovery.
func (h *OAuthMetadataHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	// RFC 9728: Protected Resource Metadata
	r.GET("/.well-known/oauth-protected-resource", lib.ChainMiddlewares(h.handleProtectedResourceMetadata, middlewares...))
	// RFC 8414: Authorization Server Metadata
	r.GET("/.well-known/oauth-authorization-server", lib.ChainMiddlewares(h.handleAuthorizationServerMetadata, middlewares...))
}

// handleProtectedResourceMetadata serves the Protected Resource Metadata
// document per RFC 9728. MCP clients fetch this after receiving a 401 response
// to discover which authorization server(s) protect the MCP resource.
//
// GET /.well-known/oauth-protected-resource
func (h *OAuthMetadataHandler) handleProtectedResourceMetadata(ctx *fasthttp.RequestCtx) {
	if clients := h.store.GetPerUserOAuthMCPClients(); len(clients) == 0 {
		sendStringError(ctx, fasthttp.StatusNotFound, "Not Found")
		return
	}
	baseURL := lib.BuildBaseURL(ctx, h.store.GetMCPExternalBaseURL())

	SendJSON(ctx, map[string]interface{}{
		"resource":                 baseURL + "/mcp",
		"authorization_servers":    []string{baseURL},
		"scopes_supported":         []string{"mcp:read", "mcp:write"},
		"bearer_methods_supported": []string{"header"},
	})
}

// handleAuthorizationServerMetadata serves the Authorization Server Metadata
// document per RFC 8414. MCP clients use this to discover Bifrost's OAuth
// endpoints (authorize, token, register) and supported capabilities.
//
// GET /.well-known/oauth-authorization-server
func (h *OAuthMetadataHandler) handleAuthorizationServerMetadata(ctx *fasthttp.RequestCtx) {
	if clients := h.store.GetPerUserOAuthMCPClients(); len(clients) == 0 {
		sendStringError(ctx, fasthttp.StatusNotFound, "Not Found")
		return
	}
	baseURL := lib.BuildBaseURL(ctx, h.store.GetMCPExternalBaseURL())

	SendJSON(ctx, map[string]interface{}{
		"issuer":                                baseURL,
		"authorization_endpoint":                baseURL + "/api/oauth/per-user/authorize",
		"token_endpoint":                        baseURL + "/api/oauth/per-user/token",
		"registration_endpoint":                 baseURL + "/api/oauth/per-user/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      []string{"mcp:read", "mcp:write"},
	})
}
