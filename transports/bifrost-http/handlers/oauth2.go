// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains OAuth 2.0 authentication flow handlers.
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/url"
	"strings"

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

	// Try per-user OAuth runtime flow first (state from oauth_user_sessions table).
	// This handles the case where an end-user authenticates during inference.
	sessionToken, perUserErr := h.oauthProvider.CompleteUserOAuthFlow(context.Background(), state, code)
	if perUserErr != nil && !errors.Is(perUserErr, schemas.ErrOAuth2NotPerUserSession) {
		// Real per-user error (not "state not found") — don't fall through to admin flow
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Per-user OAuth flow failed: %v", perUserErr))
		return
	}
	if perUserErr == nil && sessionToken != "" {
		// Consent flow: session token is a flow proxy ("flow:<flowID>:<mcpClientID>").
		// Redirect back to the MCPs consent page so the user can continue.
		if strings.HasPrefix(sessionToken, "flow:") {
			rest := strings.TrimPrefix(sessionToken, "flow:")
			flowID := strings.SplitN(rest, ":", 2)[0]
			mcpsURL := fmt.Sprintf("/oauth/consent/mcps?flow_id=%s", url.QueryEscape(flowID))
			ctx.Redirect(mcpsURL, fasthttp.StatusFound)
			return
		}

		// Per-user runtime OAuth flow completed — show success page.
		ctx.SetStatusCode(fasthttp.StatusOK)
		ctx.SetContentType("text/html")
		ctx.SetBodyString(oauthSuccessPage(`
				if (window.opener) {
					window.opener.postMessage({ type: 'oauth_success' }, window.location.origin);
					window.close();
				}
		`, "Authorization Successful", "You can close this tab."))
		return
	}

	// Fall through to standard OAuth flow (handles both admin test logins for
	// per_user_oauth setup and regular server-level OAuth).
	if err := h.oauthProvider.CompleteOAuthFlow(context.Background(), state, code); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("OAuth flow completion failed: %v", err))
		return
	}

	// Redirect to success page (or close popup)
	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetContentType("text/html")
	ctx.SetBodyString(oauthSuccessPage(`
		if (window.opener) {
			window.opener.postMessage({ type: 'oauth_success' }, window.location.origin);
			window.close();
		}
	`, "Authorization Successful", "OAuth authorization successful! You can close this window."))
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
	// JSON-encode for safe embedding in JavaScript context (prevents JS injection)
	jsEscaped, _ := json.Marshal(errorMsg)
	// HTML-escape for safe embedding in HTML body (prevents HTML injection)
	htmlEscaped := html.EscapeString(errorMsg)
	ctx.SetBodyString(oauthErrorPage(string(jsEscaped), htmlEscaped))
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
			if token.ExpiresAt != nil {
				response["token_expires_at"] = token.ExpiresAt
			}
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
	ClientID        *schemas.EnvVar `json:"client_id"`
	ClientSecret    *schemas.EnvVar `json:"client_secret"`
	AuthorizeURL    string          `json:"authorize_url"`
	TokenURL        string          `json:"token_url"`
	RegistrationURL string          `json:"registration_url"`
	RedirectURI     string          `json:"redirect_uri"`
	Scopes          []string        `json:"scopes"`
	ServerURL       string          `json:"server_url"` // For OAuth discovery
}

// InitiateOAuthFlow initiates an OAuth flow and returns the authorization URL
// This is called internally by the MCP client creation endpoint
func (h *OAuthHandler) InitiateOAuthFlow(ctx context.Context, req OAuthInitiationRequest) (*schemas.OAuth2FlowInitiation, error) {
	var registrationURL *string
	if req.RegistrationURL != "" {
		registrationURL = &req.RegistrationURL
	}

	clientID := ""
	if req.ClientID != nil {
		if v, _ := req.ClientID.Value(); v != nil {
			clientID, _ = v.(string)
		}
	}
	clientSecret := ""
	if req.ClientSecret != nil {
		if v, _ := req.ClientSecret.Value(); v != nil {
			clientSecret, _ = v.(string)
		}
	}

	config := &schemas.OAuth2Config{
		ClientID:        clientID,
		ClientSecret:    clientSecret,
		AuthorizeURL:    req.AuthorizeURL,
		TokenURL:        req.TokenURL,
		RegistrationURL: registrationURL,
		RedirectURI:     req.RedirectURI,
		Scopes:          req.Scopes,
		ServerURL:       req.ServerURL,
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

// RemovePendingMCPClient removes a pending MCP client after OAuth completion.
func (h *OAuthHandler) RemovePendingMCPClient(oauthConfigID string) error {
	return h.oauthProvider.RemovePendingMCPClient(oauthConfigID)
}

// GetAccessToken retrieves the access token for a given oauth_config_id.
// Used during per-user OAuth setup to get the admin's temporary token for verification.
func (h *OAuthHandler) GetAccessToken(ctx context.Context, oauthConfigID string) (string, error) {
	return h.oauthProvider.GetAccessToken(ctx, oauthConfigID)
}

// oauthSuccessPage renders a Bifrost-themed success HTML page.
// extraScript is injected verbatim into a <script> tag (caller is responsible for safety).
func oauthSuccessPage(extraScript, title, message string) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>%s</title>
<style>%s
  .icon{font-size:2.5rem;margin-bottom:16px}
  .msg{font-size:0.9rem;color:oklch(0.552 0.016 285.938);margin-top:8px}
</style>
<script>%s</script>
</head>
<body>
<div class="card" style="text-align:center">
  <div class="icon">&#10003;</div>
  <h1>%s</h1>
  <p class="msg">%s</p>
</div>
</body>
</html>`, html.EscapeString(title), bifrostPageCSS, extraScript, html.EscapeString(title), html.EscapeString(message))
}

// oauthErrorPage renders a Bifrost-themed error HTML page.
// jsEscapedError must already be JSON-encoded (with quotes) for safe JS embedding.
// htmlError must already be HTML-escaped for safe body embedding.
func oauthErrorPage(jsEscapedError, htmlError string) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Authorization Failed</title>
<style>%s
  .icon{font-size:2.5rem;margin-bottom:16px;color:oklch(0.50 0.18 27)}
  .err-msg{font-size:0.9rem;color:oklch(0.552 0.016 285.938);margin-top:8px}
  .hint{font-size:0.8rem;color:oklch(0.65 0.01 286);margin-top:16px}
</style>
<script>
  if (window.opener) {
    window.opener.postMessage({ type: 'oauth_failed', error: %s }, window.location.origin);
    window.close();
  }
</script>
</head>
<body>
<div class="card" style="text-align:center">
  <div class="icon">&#10007;</div>
  <h1>Authorization Failed</h1>
  <p class="err-msg">%s</p>
  <p class="hint">You can close this window.</p>
</div>
</body>
</html>`, bifrostPageCSS, jsEscapedError, htmlError)
}

// jsEscapeString returns a JSON-encoded string (with quotes) safe for embedding in JavaScript.
func jsEscapeString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// RevokeToken revokes the OAuth token for a given oauth_config_id.
// Used during per-user OAuth setup to discard the admin's temporary token after verification.
func (h *OAuthHandler) RevokeToken(ctx context.Context, oauthConfigID string) error {
	return h.oauthProvider.RevokeToken(ctx, oauthConfigID)
}
