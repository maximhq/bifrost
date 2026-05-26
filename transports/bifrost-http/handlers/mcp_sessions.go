// Package handlers — MCP Auth Sessions tab API.
//
// Surfaces the per-user OAuth token rows and pending flow rows visible to the
// caller's identity. Mode-strict: the handler derives AuthMode at request time
// via ctx.AuthMode() and only returns rows whose identity column matches.
// Orphaned tokens are included in the listing so the UI can surface "needs
// re-auth"; they never satisfy a runtime token lookup (filtered at the
// resolver layer).
package handlers

import (
	"errors"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// MCPSessionsHandler serves the sessions tab API.
type MCPSessionsHandler struct {
	store *lib.Config
}

// NewMCPSessionsHandler creates the handler.
func NewMCPSessionsHandler(store *lib.Config) *MCPSessionsHandler {
	return &MCPSessionsHandler{store: store}
}

// RegisterRoutes registers the sessions tab routes.
func (h *MCPSessionsHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET("/api/mcp/sessions", lib.ChainMiddlewares(h.list, middlewares...))
	r.POST("/api/mcp/sessions/{id}/reauth", lib.ChainMiddlewares(h.reauth, middlewares...))
	r.DELETE("/api/mcp/sessions/{id}", lib.ChainMiddlewares(h.revoke, middlewares...))
	r.GET("/api/oauth/per-user/flows/{id}", lib.ChainMiddlewares(h.flowDetail, middlewares...))
	r.GET("/api/oauth/per-user/flows/{id}/start", lib.ChainMiddlewares(h.flowStart, middlewares...))
}

// mcpClientSummary is the minimal MCP client view embedded in session rows.
type mcpClientSummary struct {
	ClientID string `json:"client_id"`
	Name     string `json:"name"`
}

// virtualKeySummary is the minimal VK view embedded in session rows.
type virtualKeySummary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// userSummary is the minimal user view embedded in user-keyed session rows.
// Populated server-side from the enterprise SCIM user table; OSS leaves it
// nil so the UI falls back to rendering the raw user_id.
type userSummary struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// mcpSessionRow is the wire shape for both authenticated tokens and pending flows.
type mcpSessionRow struct {
	ID              string             `json:"id"`
	Kind            string             `json:"kind"` // "token" | "flow"
	AuthMode        string             `json:"auth_mode"`
	UserID          *string            `json:"user_id,omitempty"`
	User            *userSummary       `json:"user,omitempty"` // Preloaded by enterprise on user-keyed rows; nil in OSS so UI falls back to user_id
	VirtualKey      *virtualKeySummary `json:"virtual_key,omitempty"`
	MCPClient       *mcpClientSummary  `json:"mcp_client,omitempty"`
	SessionID       *string            `json:"session_id,omitempty"`        // Session-mode identity: caller-issued x-bf-mcp-session-id value
	Status          string             `json:"status"`                      // 'active' | 'orphaned' | 'pending' | 'needs_reauth'
	ExpiresAt       *string            `json:"expires_at,omitempty"`        // RFC3339; nil for non-expiring tokens
	CreatedAt       string             `json:"created_at"`                  // When the session was first authenticated
	LastRefreshedAt *string            `json:"last_refreshed_at,omitempty"` // Token rows only; nil if never refreshed
	OauthConfigID   string             `json:"oauth_config_id,omitempty"`
}

type mcpSessionsListResponse struct {
	Sessions []mcpSessionRow `json:"sessions"`
}

// list returns sessions visible to the caller. Each row's identity column
// matches the caller's derived AuthMode + identity.
func (h *MCPSessionsHandler) list(ctx *fasthttp.RequestCtx) {
	// Always call the unfiltered list methods. Row visibility is handled
	// by DAC scope at the enterprise configstore layer (own-data narrows
	// to the caller's identity + owned VKs; team-data widens to members;
	// all-data / OSS-only sees everything). Matches the canonical pattern
	// used by getVirtualKeys, getPrompts, getTeams, etc.
	tokens, err := h.store.ConfigStore.ListAllOauthUserTokens(ctx)
	var flows []tables.TableOauthUserSession
	if err == nil {
		flows, err = h.store.ConfigStore.ListAllPendingOauthUserSessions(ctx)
	}
	if err != nil {
		logger.Error("[mcp/sessions] list failed: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to list MCP sessions")
		return
	}

	// A reauth in flight produces a token row (the live credential) and a
	// pending flow row (the in-flight OAuth attempt) for the same binding.
	// They're conceptually the same identity — surface the token row only and
	// suppress the flow row to avoid confusing duplicates in the UI. The flow
	// row is still in the DB; once the callback completes it moves to status
	// 'authorized' and stops being returned by the pending-flow query anyway.
	tokenBindings := make(map[sessionBindingKey]struct{}, len(tokens))
	for _, t := range tokens {
		tokenBindings[bindingKeyFromToken(t)] = struct{}{}
	}

	rows := make([]mcpSessionRow, 0, len(tokens)+len(flows))
	for _, t := range tokens {
		rows = append(rows, tokenRow(t))
	}
	for _, f := range flows {
		if _, hasToken := tokenBindings[bindingKeyFromFlow(f)]; hasToken {
			continue
		}
		// Skip deferred-fill user-mode flow rows (user_id not yet stamped).
		// They have no concrete binding to render, and surfacing them in
		// the table forces an ambiguous label.
		if f.FlowMode == string(schemas.MCPAuthModeUser) && (f.UserID == nil || *f.UserID == "") {
			continue
		}
		rows = append(rows, flowRow(f))
	}
	SendJSON(ctx, mcpSessionsListResponse{Sessions: rows})
}

type sessionBindingKey struct {
	Mode        string
	Identity    string
	MCPClientID string
}

func bindingKeyFromToken(t tables.TableOauthUserToken) sessionBindingKey {
	k := sessionBindingKey{Mode: t.AuthMode, MCPClientID: t.MCPClientID}
	switch schemas.MCPAuthMode(t.AuthMode) {
	case schemas.MCPAuthModeUser:
		if t.UserID != nil {
			k.Identity = *t.UserID
		}
	case schemas.MCPAuthModeVK:
		if t.VirtualKeyID != nil {
			k.Identity = *t.VirtualKeyID
		}
	case schemas.MCPAuthModeSession:
		k.Identity = t.SessionID
	}
	return k
}

func bindingKeyFromFlow(f tables.TableOauthUserSession) sessionBindingKey {
	k := sessionBindingKey{Mode: f.FlowMode, MCPClientID: f.MCPClientID}
	switch schemas.MCPAuthMode(f.FlowMode) {
	case schemas.MCPAuthModeUser:
		if f.UserID != nil {
			k.Identity = *f.UserID
		}
	case schemas.MCPAuthModeVK:
		if f.VirtualKeyID != nil {
			k.Identity = *f.VirtualKeyID
		}
	case schemas.MCPAuthModeSession:
		k.Identity = f.SessionID
	}
	return k
}

// reauth starts a fresh OAuth flow for the MCP client backing the given token
// row. Returns the authorize URL the user must visit.
func (h *MCPSessionsHandler) reauth(ctx *fasthttp.RequestCtx) {
	rowID, ok := ctx.UserValue("id").(string)
	if !ok || rowID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid session id")
		return
	}
	bfCtx, cancel := lib.ConvertToBifrostContext(ctx, h.store)
	defer cancel()

	tok, err := h.loadRowAuthorizedForCaller(ctx, rowID)
	if err != nil {
		// loadRowAuthorizedForCaller already wrote the error response.
		_ = err
		return
	}

	// Re-auth on an 'orphaned' row would be a no-op for the user: the
	// credential isn't dead, they've lost grant-level access. The OAuth
	// callback would mint a row keyed to a user who can't use it, and
	// the next AP sync would just orphan it again. Refuse with a
	// distinct status so the UI can show the right copy.
	if tok.Status == "orphaned" {
		SendError(ctx, fasthttp.StatusForbidden, "Access to this MCP has been revoked. Re-authenticating will not restore access - contact your administrator.")
		return
	}

	// The new flow must reuse the existing row's identity so the callback's
	// upsert lands on the same (identity, mcp_client) row. Inject the row's
	// values into context; InitiateUserOAuthFlow reads them per-mode.
	rowMode := schemas.MCPAuthMode(tok.AuthMode)
	switch rowMode {
	case schemas.MCPAuthModeSession:
		if tok.SessionID != "" {
			bfCtx.SetValue(schemas.BifrostContextKeyMCPSessionID, tok.SessionID)
		}
	case schemas.MCPAuthModeVK:
		if tok.VirtualKeyID != nil && *tok.VirtualKeyID != "" {
			bfCtx.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyID, *tok.VirtualKeyID)
		}
	case schemas.MCPAuthModeUser:
		if tok.UserID != nil && *tok.UserID != "" {
			bfCtx.SetValue(schemas.BifrostContextKeyUserID, *tok.UserID)
		}
	}

	provider := h.store.OAuthProvider
	if provider == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "OAuth provider not configured")
		return
	}
	redirectURI := lib.BuildBaseURL(ctx, h.store.GetMCPExternalClientURL()) + "/api/oauth/callback"
	flow, sessionID, err := provider.InitiateUserOAuthFlow(bfCtx, tok.OauthConfigID, tok.MCPClientID, redirectURI, rowMode)
	if err != nil {
		logger.Error("[mcp/sessions] reauth flow init failed: token=%s err=%v", rowID, err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to initiate reauthentication")
		return
	}
	logger.Debug("[mcp/sessions] reauth initiated: token=%s mcp_client=%s mode=%s flow=%s", rowID, tok.MCPClientID, rowMode, sessionID)
	SendJSON(ctx, map[string]any{
		"authorize_url": flow.AuthorizeURL,
		"session_id":    sessionID,
	})
}

// revoke hard-deletes the local token row and any pending flow rows for the
// same identity + MCP client. Upstream revocation against the OAuth provider
// is NOT performed — the per-user OAuth template config doesn't carry a
// revocation endpoint, and discovery for it is not yet wired up. The provider
// token therefore remains live upstream until natural expiry; a follow-up can
// add explicit upstream revocation once the schema captures the endpoint.
//
// Same authorization model as list/reauth: caller with an identity must match
// the row's identity column; caller without an identity is the dashboard admin
// view and can act on any row.
func (h *MCPSessionsHandler) revoke(ctx *fasthttp.RequestCtx) {
	rowID, ok := ctx.UserValue("id").(string)
	if !ok || rowID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid session id")
		return
	}
	tok, err := h.loadRowAuthorizedForCaller(ctx, rowID)
	if err != nil {
		_ = err
		return
	}
	// Delete pending flow rows BEFORE the token row. If a flow finishes
	// upstream and lands on /api/oauth/callback after a successful revoke,
	// CompleteUserOAuthFlow would mint a brand-new token for the same
	// binding — the revoke would be effectively undone. Dropping flows
	// first closes that window; if this step fails we bail without
	// touching the token, so the caller retries cleanly.
	rowMode, rowIdentity := identityFromTokenRow(tok)
	if rowIdentity != "" {
		if delErr := h.store.ConfigStore.DeleteOauthUserSessionsByModeIdentityAndMCPClient(ctx, rowMode, rowIdentity, tok.MCPClientID); delErr != nil {
			logger.Error("[mcp/sessions] clearing flow rows failed: token=%s err=%v", rowID, delErr)
			SendError(ctx, fasthttp.StatusInternalServerError, "Failed to delete MCP session")
			return
		}
	}
	if err := h.store.ConfigStore.DeleteOauthUserToken(ctx, tok.ID); err != nil {
		logger.Error("[mcp/sessions] delete row failed: token=%s err=%v", rowID, err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to delete MCP session")
		return
	}
	logger.Debug("[mcp/sessions] revoked: token=%s mcp_client=%s mode=%s", rowID, tok.MCPClientID, tok.AuthMode)
	ctx.SetStatusCode(fasthttp.StatusNoContent)
}

// mcpFlowDetailResponse is the wire shape for GET /api/oauth/per-user/flows/{id}.
type mcpFlowDetailResponse struct {
	ID            string             `json:"id"`
	FlowMode      string             `json:"flow_mode"`
	Status        string             `json:"status"`
	MCPClient     *mcpClientSummary  `json:"mcp_client,omitempty"`
	OauthConfigID string             `json:"oauth_config_id"`
	UserID        *string            `json:"user_id,omitempty"`
	User          *userSummary       `json:"user,omitempty"`
	VirtualKey    *virtualKeySummary `json:"virtual_key,omitempty"`
	SessionID     *string            `json:"session_id,omitempty"`
	ExpiresAt     string             `json:"expires_at"`
	CreatedAt     string             `json:"created_at"`
	// HasActiveToken is true when an active token already exists for the
	// flow's (mode, identity, mcp_client) binding. A pending flow with this
	// set means the user re-initiated OAuth (or a stale caller did) on a
	// binding that already has a working token — the auth page should treat
	// it as "no auth needed" rather than prompting the user.
	HasActiveToken bool `json:"has_active_token"`
}

// flowDetail returns the pending flow row's metadata so the frontend sessions
// auth page can render a "you're about to authenticate X" view.
//
// Permission model: deferred-fill flows (flow_mode='user' with user_id=nil)
// are visible to any user-mode caller — the first SCIM-authenticated user to
// open the URL will become the row's user_id at completion time. For all
// other modes the caller's identity must match the row's identity column.
func (h *MCPSessionsHandler) flowDetail(ctx *fasthttp.RequestCtx) {
	flowID, ok := ctx.UserValue("id").(string)
	if !ok || flowID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid flow id")
		return
	}

	flow, err := h.loadAuthorizedFlow(ctx, flowID)
	if err != nil {
		_ = err
		return
	}
	resp := mcpFlowDetailResponse{
		ID:            flow.ID,
		FlowMode:      flow.FlowMode,
		Status:        flow.Status,
		OauthConfigID: flow.OauthConfigID,
		UserID:        flow.UserID,
		ExpiresAt:     flow.ExpiresAt.UTC().Format(rfc3339Nano),
		CreatedAt:     flow.CreatedAt.UTC().Format(rfc3339Nano),
	}
	if flow.MCPClient != nil {
		resp.MCPClient = &mcpClientSummary{ClientID: flow.MCPClient.ClientID, Name: flow.MCPClient.Name}
	} else {
		resp.MCPClient = &mcpClientSummary{ClientID: flow.MCPClientID}
	}
	if flow.VirtualKey != nil {
		resp.VirtualKey = &virtualKeySummary{ID: flow.VirtualKey.ID, Name: flow.VirtualKey.Name}
	} else if flow.VirtualKeyID != nil {
		resp.VirtualKey = &virtualKeySummary{ID: *flow.VirtualKeyID}
	}
	if flow.User != nil {
		resp.User = &userSummary{ID: flow.User.ID, Name: flow.User.Name}
	}
	if flow.FlowMode == string(schemas.MCPAuthModeSession) && flow.SessionID != "" {
		sid := flow.SessionID
		resp.SessionID = &sid
	}
	// Check whether an active token already exists for this binding. A pending
	// flow on top of a working token means OAuth was re-initiated for some
	// reason; the auth page should display this as "already authenticated"
	// rather than prompt the user to authenticate again.
	if flowMode := schemas.MCPAuthMode(flow.FlowMode); flowMode != "" {
		identity := ""
		switch flowMode {
		case schemas.MCPAuthModeUser:
			if flow.UserID != nil && *flow.UserID != "" {
				identity = *flow.UserID
			} else if v, _ := ctx.UserValue(schemas.BifrostContextKeyUserID).(string); v != "" {
				// Deferred-fill: flow.UserID is nil until completion. Fall
				// back to the signed-in caller's user_id so HasActiveToken
				// reflects whether THIS user already has a credential for
				// the MCP client and we don't prompt an unnecessary re-auth.
				//
				// Use UserValue (not Value) — auth middleware stores via
				// SetUserValue, and fasthttp's Value() only handles bare
				// string keys, so a typed BifrostContextKey lookup via
				// Value() always returns nil.
				identity = v
			}
		case schemas.MCPAuthModeVK:
			if flow.VirtualKeyID != nil {
				identity = *flow.VirtualKeyID
			}
		case schemas.MCPAuthModeSession:
			identity = flow.SessionID
		}
		if identity != "" {
			if tok, lookupErr := h.store.ConfigStore.GetOauthUserTokenByMode(ctx, flowMode, identity, flow.MCPClientID); lookupErr == nil && tok != nil {
				resp.HasActiveToken = true
			}
		}
	}
	SendJSON(ctx, resp)
}

// flowStart reconstructs the upstream provider authorize URL for a pending
// flow and returns it. The frontend redirects the browser to that URL; the
// user completes upstream auth; upstream redirects back to /api/oauth/callback
// which calls CompleteUserOAuthFlow.
func (h *MCPSessionsHandler) flowStart(ctx *fasthttp.RequestCtx) {
	flowID, ok := ctx.UserValue("id").(string)
	if !ok || flowID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid flow id")
		return
	}
	if _, err := h.loadAuthorizedFlow(ctx, flowID); err != nil {
		_ = err
		return
	}
	if h.store.OAuthProvider == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "OAuth provider not configured")
		return
	}
	upstreamURL, err := h.store.OAuthProvider.BuildUpstreamAuthorizeURL(ctx, flowID)
	if err != nil {
		// Stale flow state is a client-recoverable condition (restart the auth
		// flow), not an outage. Map it to a 4xx so the frontend can show
		// "expired / restart auth" instead of a generic error.
		switch {
		case errors.Is(err, schemas.ErrOAuth2FlowExpired),
			errors.Is(err, schemas.ErrOAuth2FlowNotPending),
			errors.Is(err, schemas.ErrOAuth2NotPerUserSession):
			logger.Debug("[mcp/sessions] flow start refused (stale): flow=%s err=%v", flowID, err)
			SendError(ctx, fasthttp.StatusGone, "OAuth flow is no longer pending; restart authentication")
			return
		}
		logger.Error("[mcp/sessions] flow start failed: flow=%s err=%v", flowID, err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to build upstream authorize URL")
		return
	}
	SendJSON(ctx, map[string]any{"authorize_url": upstreamURL})
}

// loadAuthorizedFlow looks up the pending flow row. Visibility is enforced
// at the enterprise configstore layer via DAC scope on
// GetOauthUserSessionByID: if the caller is not allowed to see this row,
// the store returns (nil, nil) and we surface 404. Deferred-fill user-mode
// flows (user_id IS NULL) are visible to any SCIM-authenticated caller by
// the scope builder so they can claim the auth URL. Writes the appropriate
// HTTP error response and returns a sentinel error on failure.
func (h *MCPSessionsHandler) loadAuthorizedFlow(ctx *fasthttp.RequestCtx, flowID string) (*tables.TableOauthUserSession, error) {
	flow, err := h.store.ConfigStore.GetOauthUserSessionByID(ctx, flowID)
	if err != nil {
		logger.Error("[mcp/sessions] load flow failed: flow=%s err=%v", flowID, err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to load OAuth flow")
		return nil, err
	}
	if flow == nil {
		SendError(ctx, fasthttp.StatusNotFound, "OAuth flow not found")
		return nil, errNotFound
	}
	return flow, nil
}

// identityFromTokenRow returns the (mode, identity) pair recorded on the row.
// Inverse of the mode/identity routing used when creating the row; the row's
// AuthMode column is the source of truth for which identity column is keyed.
func identityFromTokenRow(tok *tables.TableOauthUserToken) (schemas.MCPAuthMode, string) {
	switch schemas.MCPAuthMode(tok.AuthMode) {
	case schemas.MCPAuthModeUser:
		if tok.UserID != nil {
			return schemas.MCPAuthModeUser, *tok.UserID
		}
	case schemas.MCPAuthModeVK:
		if tok.VirtualKeyID != nil {
			return schemas.MCPAuthModeVK, *tok.VirtualKeyID
		}
	case schemas.MCPAuthModeSession:
		return schemas.MCPAuthModeSession, tok.SessionID
	}
	return schemas.MCPAuthMode(tok.AuthMode), ""
}

// loadRowAuthorizedForCaller loads a token row. Visibility is enforced at
// the enterprise configstore layer via DAC scope on GetOauthUserTokenByID:
// if the caller is not allowed to see this row, the store returns
// (nil, nil) and we surface 404 — the same "if you can see it, you can
// act on it" model used by GetVirtualKey / DeleteVirtualKey. Writes the
// HTTP error response on failure.
func (h *MCPSessionsHandler) loadRowAuthorizedForCaller(ctx *fasthttp.RequestCtx, rowID string) (*tables.TableOauthUserToken, error) {
	tok, err := h.store.ConfigStore.GetOauthUserTokenByID(ctx, rowID)
	if err != nil {
		logger.Error("[mcp/sessions] load row failed: token=%s err=%v", rowID, err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to load MCP session")
		return nil, err
	}
	if tok == nil {
		SendError(ctx, fasthttp.StatusNotFound, "MCP session not found")
		return nil, errNotFound
	}
	return tok, nil
}

// errNotFound is a sentinel so handlers can early-return without
// re-writing the HTTP response.
var errNotFound = errSentinel("not found")

type errSentinel string

func (e errSentinel) Error() string { return string(e) }

// tokenRow maps an oauth_user_tokens row to the wire shape.
func tokenRow(t tables.TableOauthUserToken) mcpSessionRow {
	row := mcpSessionRow{
		ID:            t.ID,
		Kind:          "token",
		AuthMode:      t.AuthMode,
		UserID:        t.UserID,
		Status:        t.Status,
		CreatedAt:     t.CreatedAt.UTC().Format(rfc3339Nano),
		OauthConfigID: t.OauthConfigID,
	}
	if t.MCPClient != nil {
		row.MCPClient = &mcpClientSummary{ClientID: t.MCPClient.ClientID, Name: t.MCPClient.Name}
	} else {
		row.MCPClient = &mcpClientSummary{ClientID: t.MCPClientID}
	}
	if t.VirtualKey != nil {
		row.VirtualKey = &virtualKeySummary{ID: t.VirtualKey.ID, Name: t.VirtualKey.Name}
	} else if t.VirtualKeyID != nil {
		row.VirtualKey = &virtualKeySummary{ID: *t.VirtualKeyID}
	}
	if t.User != nil {
		row.User = &userSummary{ID: t.User.ID, Name: t.User.Name}
	}
	if t.AuthMode == string(schemas.MCPAuthModeSession) && t.SessionID != "" {
		s := t.SessionID
		row.SessionID = &s
	}
	if t.ExpiresAt != nil {
		s := t.ExpiresAt.UTC().Format(rfc3339Nano)
		row.ExpiresAt = &s
	}
	if t.LastRefreshedAt != nil {
		s := t.LastRefreshedAt.UTC().Format(rfc3339Nano)
		row.LastRefreshedAt = &s
	}
	return row
}

// flowRow maps an oauth_user_sessions (pending flow) row to the wire shape.
func flowRow(f tables.TableOauthUserSession) mcpSessionRow {
	exp := f.ExpiresAt.UTC().Format(rfc3339Nano)
	row := mcpSessionRow{
		ID:            f.ID,
		Kind:          "flow",
		AuthMode:      f.FlowMode,
		UserID:        f.UserID,
		Status:        f.Status,
		ExpiresAt:     &exp,
		CreatedAt:     f.CreatedAt.UTC().Format(rfc3339Nano),
		OauthConfigID: f.OauthConfigID,
	}
	if f.MCPClient != nil {
		row.MCPClient = &mcpClientSummary{ClientID: f.MCPClient.ClientID, Name: f.MCPClient.Name}
	} else {
		row.MCPClient = &mcpClientSummary{ClientID: f.MCPClientID}
	}
	if f.VirtualKey != nil {
		row.VirtualKey = &virtualKeySummary{ID: f.VirtualKey.ID, Name: f.VirtualKey.Name}
	} else if f.VirtualKeyID != nil {
		row.VirtualKey = &virtualKeySummary{ID: *f.VirtualKeyID}
	}
	if f.User != nil {
		row.User = &userSummary{ID: f.User.ID, Name: f.User.Name}
	}
	if f.FlowMode == string(schemas.MCPAuthModeSession) && f.SessionID != "" {
		s := f.SessionID
		row.SessionID = &s
	}
	return row
}

const rfc3339Nano = "2006-01-02T15:04:05.999999999Z07:00"
