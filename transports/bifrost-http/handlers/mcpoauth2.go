// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains OAuth 2.0 authentication flow handlers.
package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
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

// handleOAuthCallback handles the upstream OAuth provider callback. Performs
// the token exchange server-side (needs client_secret) and then redirects the
// browser into the dashboard, which shows the user-facing success/error UX.
//
// Two flow types share this redirect_uri, distinguished by which state table
// the state token belongs to:
//   - Per-user runtime flow (Bifrost-as-client to upstream for an MCP server's
//     per-user OAuth). On success → /workspace/mcp-sessions.
//   - Server-level admin-test flow (mcpClientSheet OAuth2Authorizer popup
//     validating an OAuth config template). On success → /workspace/mcp-registry/oauth-callback
//     which posts a message to the opener window and closes itself.
//
// Error details from internal completion failures are logged server-side and
// never piped into the redirect URL — the URL ends up in browser history,
// access logs, and the Referer header, so anything that ships there is
// effectively public.
//
// GET /api/oauth/callback?state=xxx&code=yyy&error=zzz
func (h *OAuthHandler) handleOAuthCallback(ctx *fasthttp.RequestCtx) {
	state := string(ctx.QueryArgs().Peek("state"))
	code := string(ctx.QueryArgs().Peek("code"))
	errorParam := string(ctx.QueryArgs().Peek("error"))
	errorDescription := string(ctx.QueryArgs().Peek("error_description"))

	if errorParam != "" {
		h.handleCallbackError(ctx, state, errorParam, errorDescription)
		return
	}

	if state == "" || code == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "Missing required parameters: state and code")
		return
	}

	// Per-user runtime flow (state lives in oauth_user_sessions).
	_, perUserErr := h.oauthProvider.CompleteUserOAuthFlow(ctx, state, code)
	if perUserErr != nil && !errors.Is(perUserErr, schemas.ErrOAuth2NotPerUserSession) {
		// The OAuth state is the CSRF token — never log it raw; it could
		// be replayed by anyone with log access while the flow is alive.
		logger.Error("[oauth] per-user callback completion failed: err=%v", perUserErr)
		const userMsg = "OAuth authentication failed. Please try again."
		ctx.Redirect(perUserCallbackRedirect(ctx, h.store.ConfigStore, userMsg, false), fasthttp.StatusFound)
		return
	}
	if perUserErr == nil {
		ctx.Redirect(perUserCallbackRedirect(ctx, h.store.ConfigStore, "", true), fasthttp.StatusFound)
		return
	}

	// Fall through: server-level admin-test flow.
	if err := h.oauthProvider.CompleteOAuthFlow(ctx, state, code); err != nil {
		logger.Error("[oauth] admin-test callback completion failed: err=%v", err)
		ctx.Redirect("/workspace/mcp-registry/oauth-callback?status=failed&error="+url.QueryEscape("OAuth authentication failed."), fasthttp.StatusFound)
		return
	}
	ctx.Redirect("/workspace/mcp-registry/oauth-callback?status=success", fasthttp.StatusFound)
}

// handleCallbackError handles ?error= responses from upstream providers.
// Marks the admin-mode flow row as failed (for admin UI's pending-setup
// display), then redirects to the route that matches the originating flow:
//   - admin-test popup flow → /workspace/mcp-registry/oauth-callback (posts a
//     message to window.opener and closes itself)
//   - per-user runtime flow → /workspace/mcp-sessions (full-page, no opener)
//
// Every flow — admin and per-user alike — shares one state-token namespace on
// mcp_oauth_flows, so classifying by FlowMode on a single lookup replaces the
// old two-step "does a row exist in oauth_configs, else assume per-user"
// inference (oauth_configs no longer carries a state column at all). Only
// the admin-mode row's status is written here: a per-user flow row is
// deliberately left 'pending' on an upstream error/deny so the same link can
// still be retried within its TTL, matching today's behavior for that path
// (CompleteUserOAuthFlow's own expiry/failure handling covers the cases that
// do need a per-user row mutated or removed). The upstream-supplied error
// code/description is logged server-side; only a generic message reaches
// the URL so provider error text doesn't leak into history / Referer.
func (h *OAuthHandler) handleCallbackError(ctx *fasthttp.RequestCtx, state, errorParam, errorDescription string) {
	isAdminTestFlow := false
	if state != "" {
		flow, err := h.store.ConfigStore.GetOauthUserSessionByState(ctx, state)
		switch {
		case err != nil:
			// Lookup failed — we can't reliably classify the flow. Default
			// to the per-user (full-page) route since that's the common
			// case; the popup-callback page expects window.opener and would
			// strand a per-user caller on a "you can close this tab" view.
			// Log the underlying cause for diagnostics.
			logger.Error("[oauth] failed to look up oauth flow by state for callback error: err=%v", err)
		case flow != nil:
			isAdminTestFlow = flow.FlowMode == "admin"
			if isAdminTestFlow {
				flow.Status = "failed"
				if updateErr := h.store.ConfigStore.UpdateOauthUserSession(ctx, flow); updateErr != nil {
					logger.Warn("[oauth] failed to mark oauth flow as failed: id=%s err=%v", flow.ID, updateErr)
				}
			}
		}
	}
	// Don't log state (CSRF token, replayable while flow is alive).
	logger.Warn("[oauth] upstream callback error: error=%s description=%s", errorParam, errorDescription)
	const userMsg = "Authentication was denied or failed. Please try again."
	if isAdminTestFlow {
		ctx.Redirect("/workspace/mcp-registry/oauth-callback?status=failed&error="+url.QueryEscape(userMsg), fasthttp.StatusFound)
		return
	}
	ctx.Redirect(perUserCallbackRedirect(ctx, h.store.ConfigStore, userMsg, false), fasthttp.StatusFound)
}

// perUserCallbackRedirect picks the post-callback destination for a per-user
// flow based on whether the visitor has a valid dashboard session. Admins land
// back on the sessions list (full chrome) with either ?completed=1 (success)
// or ?error=... (failure). Anonymous temp-token visitors land on the public
// MinimalShell pages (/auth-success or /auth-failed) which don't require a
// cookie. This mirrors the model used by the temp-token-aware UI: keep admins
// in the dashboard, route end users to chrome-less landings.
func perUserCallbackRedirect(ctx *fasthttp.RequestCtx, store configstore.ConfigStore, userMsg string, success bool) string {
	cookieToken := string(ctx.Request.Header.Cookie("token"))
	authenticated := cookieToken != "" && validateSession(ctx, store, cookieToken)
	if success {
		if authenticated {
			return "/workspace/mcp-sessions?completed=1"
		}
		return "/workspace/mcp-sessions/auth-success"
	}
	if authenticated {
		return "/workspace/mcp-sessions?error=" + url.QueryEscape(userMsg)
	}
	return "/workspace/mcp-sessions/auth-failed?error=" + url.QueryEscape(userMsg)
}

// resolveOAuthConfigStatus returns the status the /status endpoint reports.
//
// TableOauthConfig.Status is the one-time bootstrap lifecycle only: it reads
// "authorized" forever after a first successful auth, and InitiateUserOAuthFlow
// never rewrites it when a reauth starts — it rotates the flow row to "pending"
// instead. Echoing that stale value lets the authorizer UI's first poll tick
// close the consent popup and fire complete-oauth while the flow is still
// pending, which completeMCPClientOAuth rejects with 409. The flow row is
// therefore the only honest source mid-reauth, exactly as
// isPrematureOAuthCompletion reasons.
func resolveOAuthConfigStatus(configStatus string, flow *configstoreTables.TableMCPOauthFlow, now time.Time) string {
	if configStatus == "authorized" && isPrematureOAuthCompletion(flow, now) {
		return flow.Status
	}
	return configStatus
}

// currentAdminOauthFlow returns the admin-mode flow row for the MCP client
// owning this OAuth config, or nil when no client owns it. Whether that row
// says a reauth is still in flight is resolveOAuthConfigStatus's call — this
// only fetches.
//
// A lookup failure is returned rather than folded into nil. nil means "no
// reauth in flight", so swallowing one would report the config's own — stale —
// "authorized" and let the authorizer UI complete a reauth whose flow row
// could not actually be read, which is the failure this endpoint exists to
// prevent. The caller turns it into a 500; the UI's poll treats that as
// transient and keeps polling, which is the safe direction.
//
// A config with no owning MCP client is not a failure: the admin-test popup
// validates an OAuth config template that may have no client yet, and there
// is then no flow row to find.
func (h *OAuthHandler) currentAdminOauthFlow(ctx context.Context, oauthConfigID string) (*configstoreTables.TableMCPOauthFlow, error) {
	client, err := h.store.ConfigStore.GetMCPClientByOauthConfigID(ctx, oauthConfigID)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if client == nil {
		return nil, nil
	}
	flow, err := h.store.ConfigStore.GetOauthUserSessionByModeIdentityAndMCPClient(ctx, schemas.MCPAuthModeAdmin, "", client.ClientID)
	if err != nil {
		return nil, err
	}
	return flow, nil
}

// getOAuthConfigStatus returns the current status of an OAuth config
// GET /api/oauth/config/{id}/status
func (h *OAuthHandler) getOAuthConfigStatus(ctx *fasthttp.RequestCtx) {
	configID := ctx.UserValue("id").(string)

	oauthConfig, err := h.store.ConfigStore.GetOauthConfigByID(ctx, configID)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get OAuth config: %v", err))
		return
	}

	if oauthConfig == nil {
		SendError(ctx, fasthttp.StatusNotFound, "OAuth config not found")
		return
	}

	// expires_at (the flow's own PKCE-attempt deadline) lived on this row
	// before the flow table split it out onto TableMCPOauthFlow — dropped
	// from this response rather than joined in: the popup UI that polls this
	// endpoint (oauth2Authorizer.tsx) only ever branches on status, never
	// reads a deadline for a live countdown.
	//
	// That split is also why the row's own status can't be echoed verbatim
	// during a reauth — see resolveOAuthConfigStatus. Only a config already
	// reading "authorized" can be stale that way, so the flow lookup is
	// skipped for every other status.
	var inFlightFlow *configstoreTables.TableMCPOauthFlow
	if oauthConfig.Status == "authorized" {
		flow, flowErr := h.currentAdminOauthFlow(ctx, configID)
		if flowErr != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to read the reauthorization state for this OAuth config: %v", flowErr))
			return
		}
		inFlightFlow = flow
	}
	response := map[string]interface{}{
		"id":         oauthConfig.ID,
		"status":     resolveOAuthConfigStatus(oauthConfig.Status, inFlightFlow, time.Now()),
		"created_at": oauthConfig.CreatedAt,
	}

	if oauthConfig.Status == "authorized" {
		// Resolve the shared token row via (oauth_config_id, auth_mode='shared')
		// — the replacement for the retired TableOauthConfig.TokenID FK shortcut.
		token, err := h.store.ConfigStore.GetSharedOauthTokenByConfigID(ctx, configID)
		if err == nil && token != nil {
			response["token_id"] = token.ID
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

	if err := h.oauthProvider.RevokeToken(ctx, configID); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to revoke OAuth token: %v", err))
		return
	}

	SendJSON(ctx, map[string]interface{}{
		"message": "OAuth token revoked successfully",
	})
}

// OAuthInitiationRequest represents the request to initiate an OAuth flow
type OAuthInitiationRequest struct {
	ClientID        *schemas.SecretVar `json:"client_id"`
	ClientSecret    *schemas.SecretVar `json:"client_secret"`
	AuthorizeURL    string             `json:"authorize_url"`
	TokenURL        string             `json:"token_url"`
	RegistrationURL string             `json:"registration_url"`
	RedirectURI     string             `json:"redirect_uri"`
	Scopes          []string           `json:"scopes"`
	ServerURL       string             `json:"server_url"` // For OAuth discovery
	Resource        string             `json:"resource"`   // OAuth resource indicator (RFC 8707)
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
		ServerURL:       req.ServerURL,
		Resource:        req.Resource,
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

// RemovePendingMCPClient removes a pending MCP client after OAuth completion.
func (h *OAuthHandler) RemovePendingMCPClient(oauthConfigID string) error {
	return h.oauthProvider.RemovePendingMCPClient(oauthConfigID)
}

// GetAccessToken retrieves the access token for a given oauth_config_id.
// Used during per-user OAuth setup to get the admin's temporary token for verification.
func (h *OAuthHandler) GetAccessToken(ctx context.Context, oauthConfigID string) (string, error) {
	return h.oauthProvider.GetAccessToken(ctx, oauthConfigID)
}

// RevokeToken revokes the OAuth token for a given oauth_config_id.
// Used during per-user OAuth setup to discard the admin's temporary token after verification.
func (h *OAuthHandler) RevokeToken(ctx context.Context, oauthConfigID string) error {
	return h.oauthProvider.RevokeToken(ctx, oauthConfigID)
}
