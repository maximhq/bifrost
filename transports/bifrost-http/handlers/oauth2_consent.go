// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file implements the per-user OAuth consent flow — the intermediate screens
// shown between the MCP client's authorize request and the final authorization code
// issuance. The flow is:
//
//  1. GET /oauth/consent?flow_id=xxx       → VK input page (HTML)
//  2. POST /api/oauth/per-user/consent/vk  → validate VK, update PendingFlow, redirect
//  3. GET /oauth/consent/mcps?flow_id=xxx  → MCPs page (HTML, server-rendered)
//  4. POST /api/oauth/per-user/consent/submit → create session + code, redirect to client
package handlers

import (
	"errors"
	"fmt"
	"html"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/fasthttp/router"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// ConsentHandler manages the per-user OAuth consent flow screens.
type ConsentHandler struct {
	store *lib.Config
}

// NewConsentHandler creates a new consent handler instance.
func NewConsentHandler(store *lib.Config) *ConsentHandler {
	return &ConsentHandler{store: store}
}

// RegisterRoutes registers the consent flow routes.
// All routes are public — no auth middleware — since they are part of the OAuth
// flow for unauthenticated users acquiring credentials.
func (h *ConsentHandler) RegisterRoutes(r *router.Router) {
	// HTML pages (GET, served by Go)
	r.GET("/oauth/consent", h.handleIdentityPage)
	r.GET("/oauth/consent/mcps", h.handleMCPsPage)

	// API actions (POST)
	// NOTE: All state-mutating endpoints use POST. CSRF protection relies on the
	// SameSite=Lax browser-binding cookie (__bifrost_flow_secret) combined with
	// the flow_id — SameSite=Lax blocks cross-site POST, and the cookie is
	// HttpOnly+Secure. This is sufficient for the threat model here.
	r.POST("/api/oauth/per-user/consent/vk", h.handleSubmitVK)
	r.POST("/api/oauth/per-user/consent/user-id", h.handleSubmitUserID)
	r.POST("/api/oauth/per-user/consent/skip", h.handleSkip)
	r.POST("/api/oauth/per-user/consent/submit", h.handleSubmit)
}

// ---------- HTML pages ----------

// handleIdentityPage renders the identity selection page with three options:
// User ID, Virtual Key, or skip (lazy auth when tools are called).
// GET /oauth/consent?flow_id=xxx[&error=xxx]
func (h *ConsentHandler) handleIdentityPage(ctx *fasthttp.RequestCtx) {
	flowID := string(ctx.QueryArgs().Peek("flow_id"))
	errorMsg := string(ctx.QueryArgs().Peek("error"))

	if flowID == "" {
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
		ctx.SetBodyString("Missing flow_id")
		return
	}

	if h.store.ConfigStore == nil {
		ctx.SetStatusCode(fasthttp.StatusServiceUnavailable)
		ctx.SetBodyString("Config store unavailable")
		return
	}

	flow, err := h.store.ConfigStore.GetPerUserOAuthPendingFlow(ctx, flowID)
	if err != nil {
		ctx.SetStatusCode(fasthttp.StatusInternalServerError)
		ctx.SetBodyString("Failed to load consent flow.")
		return
	}
	if flow == nil || time.Now().After(flow.ExpiresAt) {
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
		ctx.SetBodyString("Invalid or expired consent flow. Please restart the authentication process.")
		return
	}
	if !validateFlowBrowserSecret(ctx, flow) {
		ctx.SetStatusCode(fasthttp.StatusForbidden)
		ctx.SetBodyString("Flow does not belong to this browser session. Please restart the authentication process.")
		return
	}

	h.store.Mu.RLock()
	enforceVK := h.store.ClientConfig.EnforceAuthOnInference
	h.store.Mu.RUnlock()

	safeFlowID := html.EscapeString(flowID)
	safeError := html.EscapeString(errorMsg)

	errorBanner := ""
	if safeError != "" {
		errorBanner = fmt.Sprintf(`<div class="error-banner">%s</div>`, safeError)
	}

	skipOption := ""
	if !enforceVK {
		skipOption = fmt.Sprintf(`
		<div class="option">
			<span class="option-title">Skip for now</span>
			<span class="option-desc">Connect to services when a tool is called</span>
			<form action="/api/oauth/per-user/consent/skip" method="POST" style="margin-top:10px">
				<input type="hidden" name="flow_id" value="%s">
				<button type="submit" class="btn btn-ghost">Skip</button>
			</form>
		</div>`, safeFlowID)
	}

	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetContentType("text/html; charset=utf-8")
	ctx.SetBodyString(fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Connect to Bifrost</title>
<style>
  %s
  .option{border:1px solid oklch(0.92 0.004 286.32);border-radius:0.5rem;padding:16px 18px;margin-bottom:10px}
  .option-title{display:block;font-size:0.9rem;font-weight:600;color:oklch(0.141 0.005 285.823);margin-bottom:2px}
  .option-desc{display:block;font-size:0.8rem;color:oklch(0.552 0.016 285.938);margin-bottom:12px}
</style>
</head>
<body>
<div class="card">
  <h1>Connect to Bifrost</h1>
  <p class="subtitle">Choose how to identify yourself for this session.</p>
  <p style="font-size:0.75rem;color:oklch(0.65 0.01 286);margin-bottom:18px">This setup page expires in 15 minutes.</p>
  %s
  <div class="option">
    <span class="option-title">User ID</span>
    <span class="option-desc">Use a stable identifier — access all available services</span>
    <form action="/api/oauth/per-user/consent/user-id" method="POST">
      <input type="hidden" name="flow_id" value="%s">
      <label for="user_id">User ID</label>
      <input type="text" id="user_id" name="user_id" placeholder="e.g. alice" autocomplete="off" spellcheck="false" autocapitalize="none" autocorrect="off">
      <button type="submit" class="btn btn-primary">Continue with User ID</button>
    </form>
  </div>
  <div class="option">
    <span class="option-title">Virtual Key</span>
    <span class="option-desc">Use a VK — access services within your key's limits</span>
    <form action="/api/oauth/per-user/consent/vk" method="POST">
      <input type="hidden" name="flow_id" value="%s">
      <label for="vk">Virtual Key</label>
      <input type="password" id="vk" name="vk" placeholder="sk-bf-..." autocomplete="off" spellcheck="false" autocapitalize="none">
      <button type="submit" class="btn btn-primary">Continue with Virtual Key</button>
    </form>
  </div>
  %s
</div>
</body>
</html>`, bifrostPageCSS, errorBanner, safeFlowID, safeFlowID, skipOption))
}

// handleMCPsPage renders the MCP authentication list page.
// GET /oauth/consent/mcps?flow_id=xxx
func (h *ConsentHandler) handleMCPsPage(ctx *fasthttp.RequestCtx) {
	flowID := string(ctx.QueryArgs().Peek("flow_id"))

	if flowID == "" {
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
		ctx.SetBodyString("Missing flow_id")
		return
	}

	if h.store.ConfigStore == nil {
		ctx.SetStatusCode(fasthttp.StatusServiceUnavailable)
		ctx.SetBodyString("Config store unavailable")
		return
	}

	flow, err := h.store.ConfigStore.GetPerUserOAuthPendingFlow(ctx, flowID)
	if err != nil {
		ctx.SetStatusCode(fasthttp.StatusInternalServerError)
		ctx.SetBodyString("Failed to load consent flow.")
		return
	}
	if flow == nil || time.Now().After(flow.ExpiresAt) {
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
		ctx.SetBodyString("Invalid or expired consent flow. Please restart the authentication process.")
		return
	}
	if !validateFlowBrowserSecret(ctx, flow) {
		ctx.SetStatusCode(fasthttp.StatusForbidden)
		ctx.SetBodyString("Flow does not belong to this browser session. Please restart the authentication process.")
		return
	}

	// Find which MCP clients the user has already authed.
	// Check both: tokens stored under the flow proxy (connected during this flow)
	// and tokens already stored under the VK/user identity (connected in a prior flow).
	completedTokens, err := h.store.ConfigStore.GetOauthUserTokensByGatewaySessionID(ctx, flowID)
	if err != nil {
		completedTokens = nil // non-fatal; just show no checkmarks
	}
	completedMCPs := make(map[string]bool, len(completedTokens))
	for _, t := range completedTokens {
		completedMCPs[t.MCPClientID] = true
	}

	// Per_user_oauth MCP clients visible to this identity — sorted for deterministic rendering.
	// When a VK is set on the flow, only show clients that VK is allowed to use.
	perUserClients := h.store.GetPerUserOAuthMCPClientsForVirtualKey(ctx, strVal(flow.VirtualKeyID))
	clientIDs := make([]string, 0, len(perUserClients))
	for id := range perUserClients {
		clientIDs = append(clientIDs, id)
	}
	sort.Strings(clientIDs)

	safeFlowID := html.EscapeString(flowID)

	// Determine if user skipped identity selection.
	isSkipped := strVal(flow.VirtualKeyID) == "" && strVal(flow.UserID) == ""

	// Build MCP rows — only show connect buttons if user has an identity.
	var mcpRows strings.Builder
	if isSkipped {
		mcpRows.WriteString(`<p style="color:#6b7280;font-size:14px;">You skipped identity selection. Services will be connected when you first use their tools. Since no identity is attached, your connections will only persist as long as the service keeps the OAuth token active — they will not be remembered across sessions.</p>`)
	} else {
		for _, clientID := range clientIDs {
			clientName := perUserClients[clientID]
			safeName := html.EscapeString(clientName)

			// Also check if a token already exists under the user's identity (e.g. from a prior LLM gateway auth).
			alreadyConnected := completedMCPs[clientID]
			if !alreadyConnected && (strVal(flow.VirtualKeyID) != "" || strVal(flow.UserID) != "") {
				existing, tokenErr := h.store.ConfigStore.GetOauthUserTokenByIdentity(ctx, strVal(flow.VirtualKeyID), strVal(flow.UserID), "", clientID)
				if tokenErr != nil {
					logger.Warn("[consent/mcps] failed to check existing token: mcp_client_id=%s err=%v", clientID, tokenErr)
				}
				alreadyConnected = existing != nil
			}

			if alreadyConnected {
				mcpRows.WriteString(fmt.Sprintf(`
				<div class="mcp-row">
					<div class="mcp-name">%s</div>
					<span class="badge connected">&#10003; Connected</span>
				</div>`, safeName))
			} else {
				connectURL := fmt.Sprintf("/api/oauth/per-user/upstream/authorize?mcp_client_id=%s&flow_id=%s",
					url.QueryEscape(clientID), url.QueryEscape(flowID))
				mcpRows.WriteString(fmt.Sprintf(`
				<div class="mcp-row">
					<div class="mcp-name">%s</div>
					<a class="badge connect" href="%s">Connect</a>
				</div>`, safeName, html.EscapeString(connectURL)))
			}
		}
		if len(perUserClients) == 0 {
			mcpRows.WriteString(`<p style="color:#6b7280;font-size:14px;">No MCP services require authentication.</p>`)
		}
	}

	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetContentType("text/html; charset=utf-8")
	ctx.SetBodyString(fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Connect Your Apps — Bifrost</title>
<style>
  %s
  .mcp-row{display:flex;align-items:center;justify-content:space-between;padding:12px 0;border-bottom:1px solid oklch(0.92 0.004 286.32)}
  .mcp-row:last-of-type{border-bottom:none}
  .mcp-name{font-size:0.9rem;font-weight:500;color:oklch(0.141 0.005 285.823)}
  .badge{font-size:0.8rem;font-weight:500;padding:4px 12px;border-radius:20px;text-decoration:none;display:inline-block}
  .badge.connected{background:oklch(0.95 0.05 160);color:oklch(0.35 0.08 160)}
  .badge.connect{background:oklch(0.5081 0.1049 165.61);color:oklch(0.985 0 0);cursor:pointer;
        padding:8px 18px;border-radius:0.5rem;font-weight:500;
        transition:background .15s}
  .badge.connect:hover{background:oklch(0.43 0.1049 165.61)}
  .mcp-list{margin-bottom:4px}
</style>
</head>
<body>
<div class="card">
  <h1>Connect Your Apps</h1>
  <p class="subtitle">Authenticate with the services below to enable their tools.</p>
  <p style="font-size:0.75rem;color:oklch(0.65 0.01 286);margin-bottom:18px">This setup page expires in 15 minutes.</p>
  <div class="mcp-list">%s</div>
  <form action="/api/oauth/per-user/consent/submit" method="POST" style="margin-top:24px">
    <input type="hidden" name="flow_id" value="%s">
    <button type="submit" class="btn btn-primary">Finish Setup</button>
  </form>
  <div style="text-align:center;margin-top:12px">
    <a href="/oauth/consent?flow_id=%s" style="font-size:0.8rem;color:oklch(0.552 0.016 285.938);text-decoration:none">Change identity</a>
  </div>
</div>
</body>
</html>`, bifrostPageCSS, mcpRows.String(), safeFlowID, safeFlowID))
}

// ---------- API action handlers ----------

// handleSubmitVK validates the submitted Virtual Key, links it to the pending flow,
// and redirects to the MCPs page.
// POST /api/oauth/per-user/consent/vk  (form: flow_id, vk)
func (h *ConsentHandler) handleSubmitVK(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "Config store unavailable")
		return
	}

	flowID := string(ctx.FormValue("flow_id"))
	vkValue := strings.TrimSpace(string(ctx.FormValue("vk")))

	if flowID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "flow_id is required")
		return
	}

	flow, err := h.store.ConfigStore.GetPerUserOAuthPendingFlow(ctx, flowID)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to load consent flow")
		return
	}
	if flow == nil || time.Now().After(flow.ExpiresAt) {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid or expired consent flow")
		return
	}
	if !validateFlowBrowserSecret(ctx, flow) {
		SendError(ctx, fasthttp.StatusForbidden, "Flow does not belong to this browser session")
		return
	}

	if vkValue == "" {
		redirectToIdentityPage(ctx, flowID, "Please enter a Virtual Key.")
		return
	}

	vk, err := h.store.ConfigStore.GetVirtualKeyByValue(ctx, vkValue)
	if err != nil {
		redirectToIdentityPage(ctx, flowID, "Failed to validate Virtual Key. Please try again.")
		return
	}
	if vk == nil || !vk.IsActive {
		redirectToIdentityPage(ctx, flowID, "Virtual Key not found or inactive. Please check and try again.")
		return
	}

	flow.VirtualKeyID = &vk.ID
	flow.UserID = nil // Clear other identity to keep selection exclusive
	if err := h.store.ConfigStore.UpdatePerUserOAuthPendingFlow(ctx, flow); err != nil {
		redirectToIdentityPage(ctx, flowID, "Failed to save Virtual Key. Please try again.")
		return
	}

	ctx.Redirect(fmt.Sprintf("/oauth/consent/mcps?flow_id=%s", url.QueryEscape(flowID)), fasthttp.StatusFound)
}

// handleSubmitUserID links a user-supplied User ID to the pending flow and proceeds to MCPs page.
// SECURITY: The User ID is self-declared (typed in a form) with no server-side verification.
// This matches the trust model of X-Bf-User-Id in the LLM gateway path. Deployments requiring
// verified identity should use Virtual Keys or an auth layer in front of Bifrost.
// POST /api/oauth/per-user/consent/user-id  (form: flow_id, user_id)
func (h *ConsentHandler) handleSubmitUserID(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "Config store unavailable")
		return
	}

	flowID := string(ctx.FormValue("flow_id"))
	userID := strings.TrimSpace(string(ctx.FormValue("user_id")))

	if flowID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "flow_id is required")
		return
	}

	flow, err := h.store.ConfigStore.GetPerUserOAuthPendingFlow(ctx, flowID)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to load consent flow")
		return
	}
	if flow == nil || time.Now().After(flow.ExpiresAt) {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid or expired consent flow")
		return
	}
	if !validateFlowBrowserSecret(ctx, flow) {
		SendError(ctx, fasthttp.StatusForbidden, "Flow does not belong to this browser session")
		return
	}

	if userID == "" {
		redirectToIdentityPage(ctx, flowID, "Please enter a User ID.")
		return
	}
	if len(userID) > 255 {
		redirectToIdentityPage(ctx, flowID, "User ID is too long (max 255 characters).")
		return
	}

	if userID != "" {
		flow.UserID = &userID
	}
	flow.VirtualKeyID = nil // Clear other identity to keep selection exclusive
	if err := h.store.ConfigStore.UpdatePerUserOAuthPendingFlow(ctx, flow); err != nil {
		redirectToIdentityPage(ctx, flowID, "Failed to save User ID. Please try again.")
		return
	}

	ctx.Redirect(fmt.Sprintf("/oauth/consent/mcps?flow_id=%s", url.QueryEscape(flowID)), fasthttp.StatusFound)
}

// handleSkip skips identity selection and proceeds directly to the MCPs page.
// Upstream services will be connected lazily when tools are first called.
// POST /api/oauth/per-user/consent/skip  (form: flow_id)
func (h *ConsentHandler) handleSkip(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "Config store unavailable")
		return
	}

	flowID := string(ctx.FormValue("flow_id"))
	if flowID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "flow_id is required")
		return
	}

	flow, err := h.store.ConfigStore.GetPerUserOAuthPendingFlow(ctx, flowID)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to load consent flow")
		return
	}
	if flow == nil || time.Now().After(flow.ExpiresAt) {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid or expired consent flow")
		return
	}
	if !validateFlowBrowserSecret(ctx, flow) {
		SendError(ctx, fasthttp.StatusForbidden, "Flow does not belong to this browser session")
		return
	}

	h.store.Mu.RLock()
	enforceVK := h.store.ClientConfig.EnforceAuthOnInference
	h.store.Mu.RUnlock()

	if enforceVK {
		redirectToIdentityPage(ctx, flowID, "An identity (Virtual Key or User ID) is required. Please choose one to continue.")
		return
	}

	// Clear any previously selected identity so skip truly resets the flow.
	if strVal(flow.VirtualKeyID) != "" || strVal(flow.UserID) != "" {
		flow.VirtualKeyID = nil
		flow.UserID = nil
		if err := h.store.ConfigStore.UpdatePerUserOAuthPendingFlow(ctx, flow); err != nil {
			redirectToIdentityPage(ctx, flowID, "Failed to clear identity. Please try again.")
			return
		}
	}

	// Skip goes straight to MCPs page; no identity means only lazy auth is available.
	ctx.Redirect(fmt.Sprintf("/oauth/consent/mcps?flow_id=%s", url.QueryEscape(flowID)), fasthttp.StatusFound)
}

// handleSubmit finalises the consent flow:
//  1. Creates a real Bifrost session (TablePerUserOAuthSession)
//  2. Migrates upstream tokens from the flow proxy to the real session
//  3. Issues a TablePerUserOAuthCode
//  4. Deletes the PendingFlow
//  5. Redirects to the original MCP client callback URL with code + state
//
// POST /api/oauth/per-user/consent/submit  (form: flow_id)
func (h *ConsentHandler) handleSubmit(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "Config store unavailable")
		return
	}

	flowID := string(ctx.FormValue("flow_id"))
	if flowID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "flow_id is required")
		return
	}
	flow, err := h.store.ConfigStore.GetPerUserOAuthPendingFlow(ctx, flowID)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to load consent flow")
		return
	}
	if flow == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid consent flow")
		return
	}
	if time.Now().After(flow.ExpiresAt) {
		SendError(ctx, fasthttp.StatusBadRequest, "Consent flow has expired. Please restart the authentication process.")
		return
	}
	if !validateFlowBrowserSecret(ctx, flow) {
		SendError(ctx, fasthttp.StatusForbidden, "Flow does not belong to this browser session")
		return
	}

	// Server-side enforcement: reject if identity is required but not provided.
	h.store.Mu.RLock()
	enforceAuth := h.store.ClientConfig.EnforceAuthOnInference
	h.store.Mu.RUnlock()
	if enforceAuth && strVal(flow.VirtualKeyID) == "" && strVal(flow.UserID) == "" {
		redirectToIdentityPage(ctx, flowID, "An identity (Virtual Key or User ID) is required. Please choose one to continue.")
		return
	}

	// 1. Generate session credentials.
	accessToken, err := generateOpaqueToken(32)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to generate session token")
		return
	}
	refreshToken, err := generateOpaqueToken(32)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to generate refresh token")
		return
	}

	session := &tables.TablePerUserOAuthSession{
		ID:           uuid.New().String(),
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ClientID:     flow.ClientID,
		VirtualKeyID: flow.VirtualKeyID,
		UserID:       flow.UserID,
		ExpiresAt:    time.Now().Add(24 * time.Hour),
	}

	// 2. Generate authorization code.
	code, err := generateOpaqueToken(32)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to generate authorization code")
		return
	}
	codeRecord := &tables.TablePerUserOAuthCode{
		ID:            uuid.New().String(),
		Code:          code,
		ClientID:      flow.ClientID,
		RedirectURI:   flow.RedirectURI,
		CodeChallenge: flow.CodeChallenge,
		SessionID:     session.ID, // Links token endpoint to this session so it can return the same access token
		// Scopes intentionally omitted: the consent flow has no scope selection step.
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}

	// 3. Atomically consume the pending flow, create session, and create auth code.
	// If another concurrent request already consumed the flow, rowsAffected will be 0.
	rowsAffected, err := h.store.ConfigStore.FinalizePerUserOAuthConsent(ctx, flowID, session, codeRecord)
	if err != nil {
		if errors.Is(err, schemas.ErrPerUserOAuthPendingFlowExpired) {
			SendError(ctx, fasthttp.StatusGone, "Consent flow has expired. Please restart the authentication process.")
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to finalize consent flow")
		return
	}
	if rowsAffected == 0 {
		SendError(ctx, fasthttp.StatusConflict, "Consent flow has already been submitted")
		return
	}
	logger.Debug("[consent/submit] session created: session_id=%s flow_id=%s", session.ID, flowID)

	// 4. Migrate upstream tokens from flow proxy sessions to real session (non-fatal).
	if err := h.store.ConfigStore.TransferOauthUserTokensFromGatewaySession(ctx, flowID, accessToken, strVal(flow.VirtualKeyID), strVal(flow.UserID)); err != nil {
		// Non-fatal: tokens can be re-acquired on first tool use.
		logger.Warn("[consent/submit] failed to transfer upstream tokens: flow_id=%s err=%v", flowID, err)
	}

	// 5. Redirect to MCP client callback with code + original state.
	redirectURL, err := url.Parse(flow.RedirectURI)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Invalid redirect URI in pending flow")
		return
	}
	q := redirectURL.Query()
	q.Set("code", code)
	if flow.State != "" {
		q.Set("state", flow.State)
	}
	redirectURL.RawQuery = q.Encode()

	ctx.Redirect(redirectURL.String(), fasthttp.StatusFound)
}

// ---------- helpers ----------

// bifrostPageCSS is the shared inline CSS for all Go-rendered consent/callback pages.
// It mirrors Bifrost's UI design tokens: teal primary, zinc palette, Geist font stack.
const bifrostPageCSS = `
  *,*::before,*::after{box-sizing:border-box;margin:0;padding:0}
  body{font-family:"Geist",system-ui,-apple-system,sans-serif;font-size:0.95rem;
       line-height:1.5;background:#f4f4f5;color:oklch(0.141 0.005 285.823);
       display:flex;align-items:center;justify-content:center;min-height:100vh;
       -webkit-font-smoothing:antialiased}
  .card{background:#fff;border:1px solid oklch(0.92 0.004 286.32);border-radius:12px;
        padding:40px;width:100%;max-width:480px}
  h1{font-size:1.25rem;font-weight:600;color:oklch(0.141 0.005 285.823);margin-bottom:6px}
  .subtitle{font-size:0.825rem;color:oklch(0.552 0.016 285.938);line-height:1.5;margin-bottom:24px}
  label{display:block;font-size:0.825rem;font-weight:500;color:oklch(0.141 0.005 285.823);margin-bottom:5px}
  input[type=text],input[type=password]{width:100%;padding:8px 12px;border:1px solid oklch(0.92 0.004 286.32);
        border-radius:0.5rem;font-size:0.875rem;outline:none;
        transition:border-color .15s,box-shadow .15s;margin-bottom:10px;
        background:#fff;color:oklch(0.141 0.005 285.823)}
  input[type=text]:focus,input[type=password]:focus{border-color:oklch(0.5081 0.1049 165.61);
        box-shadow:0 0 0 3px oklch(0.5081 0.1049 165.61 / 0.15)}
  .btn{display:block;width:100%;padding:9px 16px;border-radius:0.5rem;font-size:0.875rem;
       font-weight:500;cursor:pointer;border:none;text-align:center;text-decoration:none;
       transition:background .15s;font-family:inherit}
  .btn-primary{background:oklch(0.5081 0.1049 165.61);color:oklch(0.985 0 0)}
  .btn-primary:hover{background:oklch(0.43 0.1049 165.61)}
  .btn-ghost{background:transparent;border:1px solid oklch(0.92 0.004 286.32);
             color:oklch(0.552 0.016 285.938);display:inline-block;width:auto;padding:8px 16px}
  .btn-ghost:hover{background:#f4f4f5}
  .error-banner{background:oklch(0.97 0.02 27);border:1px solid oklch(0.88 0.06 27);
        border-radius:0.5rem;padding:12px 14px;margin-bottom:18px;
        color:oklch(0.50 0.18 27);font-size:0.825rem}
`

// redirectToIdentityPage redirects to the identity selection page with an error message.
func redirectToIdentityPage(ctx *fasthttp.RequestCtx, flowID, errorMsg string) {
	u := fmt.Sprintf("/oauth/consent?flow_id=%s&error=%s",
		url.QueryEscape(flowID), url.QueryEscape(errorMsg))
	ctx.Redirect(u, fasthttp.StatusFound)
}

// strVal safely dereferences a *string, returning "" for nil.
func strVal(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
