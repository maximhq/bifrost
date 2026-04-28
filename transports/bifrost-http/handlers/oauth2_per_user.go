// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file implements Bifrost's OAuth 2.1 Authorization Server for per-user MCP
// authentication. It provides Dynamic Client Registration (RFC 7591), Authorization
// Code flow with PKCE, and token issuance. MCP clients (Claude Code, IDEs) use
// these endpoints to authenticate users before accessing Bifrost's /mcp endpoint.
package handlers

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"strings"
	"time"

	"github.com/fasthttp/router"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// PerUserOAuthHandler implements Bifrost's OAuth 2.1 Authorization Server.
// It handles dynamic client registration, authorization code issuance with PKCE,
// and token exchange for MCP per-user authentication.
type PerUserOAuthHandler struct {
	store *lib.Config
}

// NewPerUserOAuthHandler creates a new per-user OAuth handler instance.
func NewPerUserOAuthHandler(store *lib.Config) *PerUserOAuthHandler {
	return &PerUserOAuthHandler{store: store}
}

// RegisterRoutes registers the per-user OAuth authorization server routes.
// These routes do NOT go through auth middleware since they are part of the
// OAuth flow that unauthenticated clients use to obtain tokens.
func (h *PerUserOAuthHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.POST("/api/oauth/per-user/register", lib.ChainMiddlewares(h.handleDynamicClientRegistration, middlewares...))
	r.GET("/api/oauth/per-user/authorize", lib.ChainMiddlewares(h.handleAuthorize, middlewares...))
	r.POST("/api/oauth/per-user/token", lib.ChainMiddlewares(h.handleToken, middlewares...))
	r.GET("/api/oauth/per-user/upstream/authorize", lib.ChainMiddlewares(h.handleUpstreamAuthorize, middlewares...))
}

// handleDynamicClientRegistration handles OAuth 2.0 Dynamic Client Registration
// per RFC 7591. MCP clients register themselves to obtain a client_id.
//
// POST /api/oauth/per-user/register
func (h *PerUserOAuthHandler) handleDynamicClientRegistration(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "OAuth registration unavailable: config store is disabled")
		return
	}

	if len(h.store.GetPerUserOAuthMCPClients()) == 0 {
		sendStringError(ctx, fasthttp.StatusNotFound, "Not found")
		return
	}

	var req struct {
		ClientName              string   `json:"client_name"`
		RedirectURIs            []string `json:"redirect_uris"`
		GrantTypes              []string `json:"grant_types"`
		ResponseTypes           []string `json:"response_types"`
		TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
		Scope                   string   `json:"scope"`
	}

	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid registration request: %v", err))
		return
	}

	if len(req.RedirectURIs) == 0 {
		SendError(ctx, fasthttp.StatusBadRequest, "redirect_uris is required")
		return
	}

	// Generate client_id
	clientID := uuid.New().String()

	// Serialize arrays
	redirectURIsJSON, _ := json.Marshal(req.RedirectURIs)
	grantTypes := req.GrantTypes
	if len(grantTypes) == 0 {
		grantTypes = []string{"authorization_code"}
	}
	grantTypesJSON, _ := json.Marshal(grantTypes)

	client := &tables.TablePerUserOAuthClient{
		ID:           uuid.New().String(),
		ClientID:     clientID,
		ClientName:   req.ClientName,
		RedirectURIs: string(redirectURIsJSON),
		GrantTypes:   string(grantTypesJSON),
	}

	if err := h.store.ConfigStore.CreatePerUserOAuthClient(ctx, client); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to register client: %v", err))
		return
	}

	// Return RFC 7591 response
	ctx.SetStatusCode(fasthttp.StatusCreated)
	SendJSON(ctx, map[string]interface{}{
		"client_id":                  clientID,
		"client_name":                req.ClientName,
		"redirect_uris":              req.RedirectURIs,
		"grant_types":                grantTypes,
		"response_types":             req.ResponseTypes,
		"token_endpoint_auth_method": "none",
	})
}

// handleAuthorize handles the OAuth 2.1 authorization endpoint.
// Instead of issuing a code immediately, it validates the request parameters,
// creates a PendingFlow record, and redirects the user to the consent screen.
// The code is only issued after the user completes the consent flow (VK + MCP auths).
//
// GET /api/oauth/per-user/authorize?response_type=code&client_id=xxx&redirect_uri=xxx&code_challenge=xxx&code_challenge_method=S256[&state=xxx]
func (h *PerUserOAuthHandler) handleAuthorize(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "OAuth authorization unavailable: config store is disabled")
		return
	}

	if len(h.store.GetPerUserOAuthMCPClients()) == 0 {
		sendStringError(ctx, fasthttp.StatusNotFound, "Not found")
		return
	}

	// Extract parameters
	responseType := string(ctx.QueryArgs().Peek("response_type"))
	clientID := string(ctx.QueryArgs().Peek("client_id"))
	redirectURI := string(ctx.QueryArgs().Peek("redirect_uri"))
	state := string(ctx.QueryArgs().Peek("state"))
	codeChallenge := string(ctx.QueryArgs().Peek("code_challenge"))
	codeChallengeMethod := string(ctx.QueryArgs().Peek("code_challenge_method"))

	// Validate required parameters
	if responseType != "code" {
		SendError(ctx, fasthttp.StatusBadRequest, "response_type must be 'code'")
		return
	}
	if clientID == "" || redirectURI == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "client_id and redirect_uri are required")
		return
	}
	if codeChallenge == "" || codeChallengeMethod != "S256" {
		SendError(ctx, fasthttp.StatusBadRequest, "PKCE is required: code_challenge and code_challenge_method=S256")
		return
	}

	// Validate client exists and redirect_uri is registered
	client, err := h.store.ConfigStore.GetPerUserOAuthClientByClientID(ctx, clientID)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to validate client: %v", err))
		return
	}
	if client == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Unknown client_id")
		return
	}
	var allowedURIs []string
	json.Unmarshal([]byte(client.RedirectURIs), &allowedURIs)
	uriAllowed := false
	for _, allowed := range allowedURIs {
		if allowed == redirectURI {
			uriAllowed = true
			break
		}
	}
	if !uriAllowed {
		SendError(ctx, fasthttp.StatusBadRequest, "redirect_uri not registered for this client")
		return
	}

	// Generate a browser-binding secret so only the initiating browser can resume this flow.
	browserSecret, err := generateOpaqueToken(32)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to generate browser secret")
		return
	}
	browserSecretHash := fmt.Sprintf("%x", sha256.Sum256([]byte(browserSecret)))

	// Create a PendingFlow to carry OAuth params through the consent screen.
	flow := &tables.TablePerUserOAuthPendingFlow{
		ID:                uuid.New().String(),
		ClientID:          clientID,
		RedirectURI:       redirectURI,
		CodeChallenge:     codeChallenge,
		State:             state,
		BrowserSecretHash: browserSecretHash,
		ExpiresAt:         time.Now().Add(15 * time.Minute),
	}
	if err := h.store.ConfigStore.CreatePerUserOAuthPendingFlow(ctx, flow); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to create pending flow: %v", err))
		return
	}
	logger.Debug("[oauth/authorize] PendingFlow created: flow_id=%s client_id=%s", flow.ID, clientID)

	// Set HttpOnly cookie binding this flow to the current browser.
	var cookie fasthttp.Cookie
	cookie.SetKey("__bifrost_flow_secret")
	cookie.SetValue(browserSecret)
	cookie.SetPath("/")
	cookie.SetHTTPOnly(true)
	cookie.SetSameSite(fasthttp.CookieSameSiteLaxMode)
	isSecure := ctx.IsTLS() || string(ctx.Request.Header.Peek("X-Forwarded-Proto")) == "https"
	cookie.SetSecure(isSecure)
	cookie.SetMaxAge(15 * 60) // 15 minutes, matching flow TTL
	ctx.Response.Header.SetCookie(&cookie)

	// Redirect to consent screen with flow_id (relative path — stays on current origin).
	consentURL := fmt.Sprintf("/oauth/consent?flow_id=%s", url.QueryEscape(flow.ID))
	ctx.Redirect(consentURL, fasthttp.StatusFound)
}

// handleToken handles the OAuth 2.1 token endpoint.
// It validates the authorization code + PKCE verifier and issues access/refresh tokens.
//
// POST /api/oauth/per-user/token
func (h *PerUserOAuthHandler) handleToken(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "OAuth token endpoint unavailable: config store is disabled")
		return
	}

	if len(h.store.GetPerUserOAuthMCPClients()) == 0 {
		sendStringError(ctx, fasthttp.StatusNotFound, "Not found")
		return
	}

	// Parse form-encoded body
	grantType := string(ctx.FormValue("grant_type"))
	code := string(ctx.FormValue("code"))
	redirectURI := string(ctx.FormValue("redirect_uri"))
	clientID := string(ctx.FormValue("client_id"))
	codeVerifier := string(ctx.FormValue("code_verifier"))

	if grantType != "authorization_code" {
		sendOAuthError(ctx, fasthttp.StatusBadRequest, "unsupported_grant_type", "Only authorization_code grant is supported")
		return
	}

	if code == "" || codeVerifier == "" {
		sendOAuthError(ctx, fasthttp.StatusBadRequest, "invalid_request", "code and code_verifier are required")
		return
	}

	// Atomically claim authorization code (prevents concurrent redemption)
	codeRecord, err := h.store.ConfigStore.ClaimPerUserOAuthCode(ctx, code)
	if err != nil {
		sendOAuthError(ctx, fasthttp.StatusInternalServerError, "server_error", "Failed to validate code")
		return
	}
	if codeRecord == nil {
		sendOAuthError(ctx, fasthttp.StatusBadRequest, "invalid_grant", "Invalid or already used authorization code")
		return
	}

	// Validate code is not expired
	if time.Now().After(codeRecord.ExpiresAt) {
		sendOAuthError(ctx, fasthttp.StatusBadRequest, "invalid_grant", "Authorization code expired")
		return
	}

	// Validate client_id if provided — some public clients omit it (RFC 6749 §4.1.3 allows
	// omitting client_id when the client is not authenticating with the server).
	// The code record already binds the code to the correct client, so this is safe.
	if clientID != "" && codeRecord.ClientID != clientID {
		logger.Debug("[oauth/token] client_id mismatch: code_client=%s request_client=%s", codeRecord.ClientID, clientID)
		sendOAuthError(ctx, fasthttp.StatusBadRequest, "invalid_grant", "client_id mismatch")
		return
	}
	// Use the client_id from the code record as the authoritative value.
	clientID = codeRecord.ClientID

	// Validate redirect_uri matches
	if redirectURI != "" && codeRecord.RedirectURI != redirectURI {
		logger.Debug("[oauth/token] redirect_uri mismatch: code=%s request=%s", codeRecord.RedirectURI, redirectURI)
		sendOAuthError(ctx, fasthttp.StatusBadRequest, "invalid_grant", "redirect_uri mismatch")
		return
	}

	// Validate PKCE: SHA256(code_verifier) must match code_challenge
	verifierHash := sha256.Sum256([]byte(codeVerifier))
	computedChallenge := base64.RawURLEncoding.EncodeToString(verifierHash[:])
	if computedChallenge != codeRecord.CodeChallenge {
		logger.Debug("[oauth/token] PKCE verification failed")
		sendOAuthError(ctx, fasthttp.StatusBadRequest, "invalid_grant", "PKCE verification failed")
		return
	}

	// If the code was issued by the consent flow (handleSubmit), the session already exists
	// with the upstream tokens transferred to it. Reuse that session's access token so the
	// client receives the token that the upstream (Notion, GitHub, etc.) tokens are linked to.
	var accessToken string
	var expiresAt time.Time

	if codeRecord.SessionID != "" {
		existingSession, err := h.store.ConfigStore.GetPerUserOAuthSessionByID(ctx, codeRecord.SessionID)
		if err != nil {
			logger.Info("[oauth/token] Failed to load existing session: session_id=%s err=%v", codeRecord.SessionID, err)
			sendOAuthError(ctx, fasthttp.StatusInternalServerError, "server_error", "Failed to load session")
			return
		}
		if existingSession == nil {
			logger.Info("[oauth/token] Existing session not found: session_id=%s", codeRecord.SessionID)
			sendOAuthError(ctx, fasthttp.StatusInternalServerError, "server_error", "Session not found")
			return
		}
		if !existingSession.ExpiresAt.After(time.Now()) {
			sendOAuthError(ctx, fasthttp.StatusBadRequest, "invalid_grant", "Session expired")
			return
		}
		accessToken = existingSession.AccessToken
		expiresAt = existingSession.ExpiresAt
		logger.Debug("[oauth/token] reusing consent session: session_id=%s", existingSession.ID)
	} else {
		// Fallback: no linked session (legacy path) — create a new one.
		var newAccessToken, newRefreshToken string
		newAccessToken, err = generateOpaqueToken(32)
		if err != nil {
			sendOAuthError(ctx, fasthttp.StatusInternalServerError, "server_error", "Failed to generate access token")
			return
		}
		newRefreshToken, err = generateOpaqueToken(32)
		if err != nil {
			sendOAuthError(ctx, fasthttp.StatusInternalServerError, "server_error", "Failed to generate refresh token")
			return
		}
		expiresAt = time.Now().Add(24 * time.Hour)
		newSession := &tables.TablePerUserOAuthSession{
			ID:           uuid.New().String(),
			AccessToken:  newAccessToken,
			RefreshToken: newRefreshToken,
			ClientID:     clientID,
			ExpiresAt:    expiresAt,
		}
		if err := h.store.ConfigStore.CreatePerUserOAuthSession(ctx, newSession); err != nil {
			sendOAuthError(ctx, fasthttp.StatusInternalServerError, "server_error", "Failed to create session")
			return
		}
		accessToken = newAccessToken
		logger.Debug("[oauth/token] created new session (legacy path): session_id=%s", newSession.ID)
	}
	// Return OAuth token response
	ctx.SetContentType("application/json")
	ctx.SetStatusCode(fasthttp.StatusOK)
	SendJSON(ctx, map[string]interface{}{
		"access_token": accessToken,
		"token_type":   "Bearer",
		"expires_in":   int(time.Until(expiresAt).Seconds()),
		"scope":        codeRecord.Scopes,
	})
}

// sendOAuthError sends an OAuth 2.0 error response per RFC 6749 Section 5.2.
func sendOAuthError(ctx *fasthttp.RequestCtx, statusCode int, errorCode, description string) {
	ctx.SetContentType("application/json")
	ctx.SetStatusCode(statusCode)
	resp, _ := json.Marshal(map[string]string{
		"error":             errorCode,
		"error_description": description,
	})
	ctx.SetBody(resp)
}

func sendStringError(ctx *fasthttp.RequestCtx, statusCode int, message string) {
	ctx.SetContentType("text/plain")
	ctx.SetStatusCode(statusCode)
	ctx.SetBodyString(message)
}

// generateOpaqueToken generates a cryptographically secure random token.
// validateFlowBrowserSecret checks that the request carries the __bifrost_flow_secret
// cookie matching the hash stored on the pending flow. Returns true if valid.
func validateFlowBrowserSecret(ctx *fasthttp.RequestCtx, flow *tables.TablePerUserOAuthPendingFlow) bool {
	if flow.BrowserSecretHash == "" {
		// Legacy flow without browser binding — allow for backwards compatibility.
		return true
	}
	secret := ctx.Request.Header.Cookie("__bifrost_flow_secret")
	if len(secret) == 0 {
		return false
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(secret))
	return hash == flow.BrowserSecretHash
}

func generateOpaqueToken(length int) (string, error) {
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

// handleUpstreamAuthorize handles the upstream OAuth proxy for per-user OAuth.
// When a user needs to authenticate with an upstream MCP server (e.g., Notion),
// this endpoint redirects them to the upstream provider's OAuth authorize URL.
// After the user authenticates, the callback stores their upstream token linked
// to either their Bifrost session (runtime flow) or a PendingFlow (consent flow).
//
// Runtime flow:  GET /api/oauth/per-user/upstream/authorize?mcp_client_id=xxx&session=xxx
// Consent flow:  GET /api/oauth/per-user/upstream/authorize?mcp_client_id=xxx&flow_id=xxx
func (h *PerUserOAuthHandler) handleUpstreamAuthorize(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "OAuth upstream authorization unavailable: config store is disabled")
		return
	}

	mcpClientID := string(ctx.QueryArgs().Peek("mcp_client_id"))
	sessionID := string(ctx.QueryArgs().Peek("session"))
	flowID := string(ctx.QueryArgs().Peek("flow_id"))

	if mcpClientID == "" || (sessionID == "" && flowID == "") {
		SendError(ctx, fasthttp.StatusBadRequest, "mcp_client_id and either session or flow_id are required")
		return
	}

	// Resolve identity depending on whether this is a runtime session or a consent flow.
	var virtualKeyID, userID, proxySessionToken, gatewaySessionID string
	if flowID != "" {
		// Consent flow: use the pending flow for identity and proxy token.
		flow, err := h.store.ConfigStore.GetPerUserOAuthPendingFlow(ctx, flowID)
		if err != nil || flow == nil || time.Now().After(flow.ExpiresAt) {
			SendError(ctx, fasthttp.StatusUnauthorized, "Invalid or expired consent flow")
			return
		}
		if !validateFlowBrowserSecret(ctx, flow) {
			SendError(ctx, fasthttp.StatusForbidden, "Flow does not belong to this browser session")
			return
		}
		if strVal(flow.VirtualKeyID) != "" {
			virtualKeyID = *flow.VirtualKeyID
		}
		if strVal(flow.UserID) != "" {
			userID = *flow.UserID
		}
		// Use a prefixed flow token so the callback can detect the consent path.
		// Include mcpClientID to avoid unique constraint violations when multiple
		// MCP services are connected in the same consent flow.
		proxySessionToken = "flow:" + flowID + ":" + mcpClientID
		gatewaySessionID = flowID
	} else {
		// Runtime flow: validate the existing Bifrost session.
		bifrostSession, err := h.store.ConfigStore.GetPerUserOAuthSessionByID(ctx, sessionID)
		if err != nil || bifrostSession == nil {
			SendError(ctx, fasthttp.StatusUnauthorized, "Invalid or expired session")
			return
		}
		if !bifrostSession.ExpiresAt.After(time.Now()) {
			SendError(ctx, fasthttp.StatusUnauthorized, "Invalid or expired session")
			return
		}
		virtualKeyID = strVal(bifrostSession.VirtualKeyID)
		userID = strVal(bifrostSession.UserID)
		proxySessionToken = "runtime:" + sessionID + ":" + mcpClientID
		gatewaySessionID = sessionID
	}

	// Look up the MCP client config to get the template OAuth config.
	mcpClient, err := h.store.ConfigStore.GetMCPClientByID(ctx, mcpClientID)
	if err != nil || mcpClient == nil {
		SendError(ctx, fasthttp.StatusNotFound, "MCP client not found")
		return
	}
	if mcpClient.AuthType != string(schemas.MCPAuthTypePerUserOauth) {
		SendError(ctx, fasthttp.StatusBadRequest, "MCP client does not use per-user OAuth")
		return
	}
	if mcpClient.OauthConfigID == nil || *mcpClient.OauthConfigID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "MCP client has no OAuth configuration")
		return
	}

	// Load template OAuth config (has upstream authorize_url, client_id, etc.)
	templateConfig, err := h.store.ConfigStore.GetOauthConfigByID(ctx, *mcpClient.OauthConfigID)
	if err != nil || templateConfig == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to load OAuth template config")
		return
	}

	// Generate PKCE challenge for upstream.
	codeVerifier, err := generateOpaqueToken(32)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to generate PKCE verifier")
		return
	}
	verifierHash := sha256.Sum256([]byte(codeVerifier))
	codeChallenge := base64.RawURLEncoding.EncodeToString(verifierHash[:])

	// Generate state for upstream.
	state, err := generateOpaqueToken(32)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to generate state token")
		return
	}

	// Build redirect URI (Bifrost's callback endpoint).
	redirectURI := lib.BuildBaseURL(ctx, h.store.GetMCPExternalBaseURL()) + "/api/oauth/callback"
	var vkId *string
	if virtualKeyID != "" {
		vkId = &virtualKeyID
	}
	var uid *string
	if userID != "" {
		uid = &userID
	}
	// Store upstream OAuth session linking state → MCP client + identity.
	upstreamSession := &tables.TableOauthUserSession{
		ID:               uuid.New().String(),
		MCPClientID:      mcpClientID,
		OauthConfigID:    *mcpClient.OauthConfigID,
		State:            state,
		CodeVerifier:     codeVerifier,
		SessionToken:     proxySessionToken, // "runtime:xxx" for runtime flow; "flow:xxx" for consent flow
		GatewaySessionID: gatewaySessionID,
		VirtualKeyID:     vkId,
		UserID:           uid,
		Status:           "pending",
		ExpiresAt:        time.Now().Add(15 * time.Minute),
	}
	logger.Debug("[oauth/upstream-authorize] creating upstream session: mcp_client=%s flow=%s", mcpClientID, proxySessionToken)
	if err := h.store.ConfigStore.CreateOauthUserSession(ctx, upstreamSession); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to create upstream OAuth session: %v", err))
		return
	}

	// Parse scopes from template config.
	var scopes []string
	if templateConfig.Scopes != "" {
		json.Unmarshal([]byte(templateConfig.Scopes), &scopes)
	}

	// Build upstream authorize URL with PKCE.
	params := url.Values{}
	params.Set("response_type", "code")
	params.Set("client_id", templateConfig.ClientID)
	params.Set("redirect_uri", redirectURI)
	params.Set("state", state)
	params.Set("code_challenge", codeChallenge)
	params.Set("code_challenge_method", "S256")
	if len(scopes) > 0 {
		params.Set("scope", strings.Join(scopes, " "))
	}

	baseURL, err := url.Parse(templateConfig.AuthorizeURL)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Invalid upstream authorize URL")
		return
	}
	existing := baseURL.Query()
	for k, vals := range params {
		for _, v := range vals {
			existing.Set(k, v)
		}
	}
	baseURL.RawQuery = existing.Encode()
	ctx.Redirect(baseURL.String(), fasthttp.StatusFound)
}

// Ensure unused imports are referenced.
var _ = html.EscapeString
var _ configstore.ConfigStore
