// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains OAuth 2.0 authentication flow handlers.
package handlers

import (
	"context"
	"fmt"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/oauth2"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// OAuth2Handler manages HTTP requests for OAuth2 operations
type OAuthHandler struct {
	client        *bifrost.Bifrost
	store         *lib.Config
	oauthProvider *oauth2.OAuth2Provider
}

// NewOAuthHandler creates a new OAuth handler instance
func NewOAuthHandler(oauthProvider *oauth2.OAuth2Provider, client *bifrost.Bifrost, store *lib.Config) *OAuthHandler {
	return &OAuthHandler{
		client:        client,
		store:         store,
		oauthProvider: oauthProvider,
	}
}

// RegisterRoutes registers all OAuth-related routes
func (h *OAuthHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET("/api/oauth/callback", lib.ChainMiddlewares(h.handleOAuthCallback, middlewares...))
	r.GET("/api/oauth/config/{id}/status", lib.ChainMiddlewares(h.getOAuthConfigStatus, middlewares...))
	r.DELETE("/api/oauth/config/{id}", lib.ChainMiddlewares(h.revokeOAuthConfig, middlewares...))
}

// handleOAuthCallback handles the OAuth provider callback
// GET /api/oauth/callback?state=xxx&code=yyy&error=zzz
func (h *OAuthHandler) handleOAuthCallback(ctx *fasthttp.RequestCtx) {
	state := string(ctx.QueryArgs().Peek("state"))
	code := string(ctx.QueryArgs().Peek("code"))
	errorParam := string(ctx.QueryArgs().Peek("error"))
	errorDescription := string(ctx.QueryArgs().Peek("error_description"))

	// Handle authorization denial
	if errorParam != "" {
		h.handleCallbackError(ctx, state, errorParam, errorDescription)
		return
	}

	// Validate required parameters
	if state == "" || code == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "Missing required parameters: state and code")
		return
	}

	// Complete OAuth flow
	if err := h.oauthProvider.CompleteOAuthFlow(context.Background(), state, code); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("OAuth flow completion failed: %v", err))
		return
	}

	// Redirect to success page (or close popup)
	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetContentType("text/html")
	ctx.SetBodyString(`
		<!DOCTYPE html>
		<html>
		<head>
			<title>OAuth Success</title>
			<script>
				// Close the popup window
				if (window.opener) {
					window.opener.postMessage({ type: 'oauth_success' }, '*');
					window.close();
				} else {
					document.getElementById('message').textContent = 'OAuth authorization successful! You can close this window.';
				}
			</script>
		</head>
		<body>
			<div style="display: flex; align-items: center; justify-content: center; height: 100vh; font-family: system-ui;">
				<div style="text-align: center;">
					<h1>✓ Authorization Successful</h1>
					<p id="message">This window will close automatically...</p>
				</div>
			</div>
		</body>
		</html>
	`)
}

// handleCallbackError handles OAuth callback errors
func (h *OAuthHandler) handleCallbackError(ctx *fasthttp.RequestCtx, state, errorParam, errorDescription string) {
	// Update OAuth config status to failed if state is provided
	if state != "" {
		oauthConfig, err := h.store.ConfigStore.GetOauthConfigByState(context.Background(), state)
		if err == nil && oauthConfig != nil {
			oauthConfig.Status = "failed"
			h.store.ConfigStore.UpdateOauthConfig(context.Background(), oauthConfig)
		}
	}

	// Show error page
	ctx.SetStatusCode(fasthttp.StatusBadRequest)
	ctx.SetContentType("text/html")
	errorMsg := errorParam
	if errorDescription != "" {
		errorMsg = fmt.Sprintf("%s: %s", errorParam, errorDescription)
	}
	ctx.SetBodyString(fmt.Sprintf(`
		<!DOCTYPE html>
		<html>
		<head>
			<title>OAuth Failed</title>
			<script>
				// Notify parent window
				if (window.opener) {
					window.opener.postMessage({ type: 'oauth_failed', error: '%s' }, '*');
					window.close();
				}
			</script>
		</head>
		<body>
			<div style="display: flex; align-items: center; justify-content: center; height: 100vh; font-family: system-ui;">
				<div style="text-align: center;">
					<h1>✗ Authorization Failed</h1>
					<p>%s</p>
					<p style="color: #666;">You can close this window.</p>
				</div>
			</div>
		</body>
		</html>
	`, errorMsg, errorMsg))
}

// getOAuthConfigStatus returns the current status of an OAuth config
// GET /api/oauth/config/{id}/status
func (h *OAuthHandler) getOAuthConfigStatus(ctx *fasthttp.RequestCtx) {
	configID := ctx.UserValue("id").(string)

	oauthConfig, err := h.store.ConfigStore.GetOauthConfigByID(context.Background(), configID)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get OAuth config: %v", err))
		return
	}

	if oauthConfig == nil {
		SendError(ctx, fasthttp.StatusNotFound, "OAuth config not found")
		return
	}

	response := map[string]interface{}{
		"id":         oauthConfig.ID,
		"status":     oauthConfig.Status,
		"created_at": oauthConfig.CreatedAt,
		"expires_at": oauthConfig.ExpiresAt,
	}

	if oauthConfig.Status == "authorized" && oauthConfig.TokenID != nil {
		response["token_id"] = *oauthConfig.TokenID

		// Get token metadata
		token, err := h.store.ConfigStore.GetOauthTokenByID(context.Background(), *oauthConfig.TokenID)
		if err == nil && token != nil {
			response["token_expires_at"] = token.ExpiresAt
			response["token_scopes"] = token.Scopes
		}
	}

	SendJSON(ctx, response)
}

// revokeOAuthConfig revokes an OAuth configuration and its associated token
// DELETE /api/oauth/config/{id}
func (h *OAuthHandler) revokeOAuthConfig(ctx *fasthttp.RequestCtx) {
	configID := ctx.UserValue("id").(string)

	if err := h.oauthProvider.RevokeToken(context.Background(), configID); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to revoke OAuth token: %v", err))
		return
	}

	SendJSON(ctx, map[string]interface{}{
		"message": "OAuth token revoked successfully",
	})
}

// OAuthInitiationRequest represents the request to initiate an OAuth flow
type OAuthInitiationRequest struct {
	ClientID        string   `json:"client_id"`
	ClientSecret    string   `json:"client_secret"`
	AuthorizeURL    string   `json:"authorize_url"`
	TokenURL        string   `json:"token_url"`
	RegistrationURL string   `json:"registration_url"`
	RedirectURI     string   `json:"redirect_uri"`
	Scopes          []string `json:"scopes"`
	ServerURL       string   `json:"server_url"` // For OAuth discovery
}

// InitiateOAuthFlow initiates an OAuth flow and returns the authorization URL
// This is called internally by the MCP client creation endpoint
func (h *OAuthHandler) InitiateOAuthFlow(ctx context.Context, req OAuthInitiationRequest) (*schemas.OAuth2FlowInitiation, error) {
	var registrationURL *string
	if req.RegistrationURL != "" {
		registrationURL = &req.RegistrationURL
	}

	config := &schemas.OAuth2Config{
		ClientID:        req.ClientID,
		ClientSecret:    req.ClientSecret,
		AuthorizeURL:    req.AuthorizeURL,
		TokenURL:        req.TokenURL,
		RegistrationURL: registrationURL,
		RedirectURI:     req.RedirectURI,
		Scopes:          req.Scopes,
		ServerURL:       req.ServerURL, // MCP server URL for OAuth discovery
	}

	return h.oauthProvider.InitiateOAuthFlow(ctx, config)
}

// StorePendingMCPClient stores an MCP client config in the database while waiting for OAuth completion
// This supports multi-instance deployments where OAuth callback may hit a different server instance
func (h *OAuthHandler) StorePendingMCPClient(oauthConfigID string, mcpClientConfig schemas.MCPClientConfig) error {
	return h.oauthProvider.StorePendingMCPClient(oauthConfigID, mcpClientConfig)
}

// GetPendingMCPClient retrieves a pending MCP client config by oauth_config_id
func (h *OAuthHandler) GetPendingMCPClient(oauthConfigID string) (*schemas.MCPClientConfig, error) {
	return h.oauthProvider.GetPendingMCPClient(oauthConfigID)
}

// GetPendingMCPClientByState retrieves a pending MCP client config by OAuth state token
func (h *OAuthHandler) GetPendingMCPClientByState(state string) (*schemas.MCPClientConfig, string, error) {
	return h.oauthProvider.GetPendingMCPClientByState(state)
}

// RemovePendingMCPClient removes a pending MCP client after OAuth completion
func (h *OAuthHandler) RemovePendingMCPClient(oauthConfigID string) error {
	return h.oauthProvider.RemovePendingMCPClient(oauthConfigID)
}
