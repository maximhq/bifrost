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
	"sort"
	"strconv"
	"strings"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
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

// mcpSessionRow is the wire shape for OAuth tokens, OAuth pending flows,
// per-user header credentials, and per-user header pending flows. Kind
// discriminates the row TYPE; AuthKind disambiguates flow rows by auth
// surface (OAuth vs Headers) so the UI can route "Complete authentication"
// to the correct landing page. Per-kind-only fields use omitempty.
type mcpSessionRow struct {
	ID              string             `json:"id"`
	Kind            string             `json:"kind"`      // "token" | "flow" | "header"
	AuthKind        string             `json:"auth_kind"` // "oauth" | "headers" — disambiguates flow rows; tokens are always "oauth", header creds are always "headers"
	AuthMode        string             `json:"auth_mode"`
	UserID          *string            `json:"user_id,omitempty"`
	User            *userSummary       `json:"user,omitempty"` // Preloaded by enterprise on user-keyed rows; nil in OSS so UI falls back to user_id
	VirtualKey      *virtualKeySummary `json:"virtual_key,omitempty"`
	MCPClient       *mcpClientSummary  `json:"mcp_client,omitempty"`
	SessionID       *string            `json:"session_id,omitempty"`        // Session-mode identity: caller-issued x-bf-mcp-session-id value
	Status          string             `json:"status"`                      // OAuth: 'active' | 'orphaned' | 'pending' | 'needs_reauth'. Headers: 'active' | 'orphaned' | 'needs_update'.
	ExpiresAt       *string            `json:"expires_at,omitempty"`        // OAuth-only; nil for non-expiring tokens and always nil for headers
	CreatedAt       string             `json:"created_at"`                  // When the session was first authenticated
	LastRefreshedAt *string            `json:"last_refreshed_at,omitempty"` // OAuth token rows only; nil if never refreshed
	UpdatedAt       *string            `json:"updated_at,omitempty"`        // Headers rows: timestamp of last submission/edit
	OauthConfigID   string             `json:"oauth_config_id,omitempty"`   // OAuth rows only
	// CanReauth mirrors the server-side identity gate on POST /reauth: user-mode
	// rows are only reauthable by the bound user (admin DAC scope is enough to
	// see the row, but reauthing mints credentials for whoever clicks the URL).
	// Non-user-mode rows are always reauthable. The UI hides the action when false.
	// Always false for kind == "flow" — flow rows are completed via
	// /api/oauth/per-user/flows/{id}/start, not /reauth.
	CanReauth bool `json:"can_reauth"`
}

type mcpSessionsListResponse struct {
	Sessions   []mcpSessionRow `json:"sessions"`
	Count      int             `json:"count"`
	TotalCount int             `json:"total_count"`
	Limit      int             `json:"limit"`
	Offset     int             `json:"offset"`
}

const (
	mcpSessionsDefaultLimit = 50
	mcpSessionsMaxLimit     = 500
)

// mcpSessionsListQuery is the parsed query string for GET /api/mcp/sessions.
// Filters is forwarded to the four store list methods. Kinds is a wire-row
// taxonomy filter (token/header/flow) applied in the handler — it doesn't
// map to a column, just decides which store calls to skip. Limit/Offset
// apply to the merged result after cross-table de-dup, so they're handled
// here rather than in the store params struct.
type mcpSessionsListQuery struct {
	Filters configstore.MCPSessionsFilterParams
	Kinds   []string
	Limit   int
	Offset  int
}

// parseMCPSessionsListQuery extracts and validates pagination + filter params
// from the request query string. On validation failure it writes a 400
// response and returns ok=false — the caller must early-return without
// further writes.
func parseMCPSessionsListQuery(ctx *fasthttp.RequestCtx) (mcpSessionsListQuery, bool) {
	q := mcpSessionsListQuery{Limit: mcpSessionsDefaultLimit}
	args := ctx.QueryArgs()
	q.Filters.Search = strings.TrimSpace(string(args.Peek("q")))
	q.Filters.Statuses = parseCommaSeparated(string(args.Peek("status")))
	q.Filters.AuthModes = parseCommaSeparated(string(args.Peek("auth_mode")))
	q.Filters.MCPClientIDs = parseCommaSeparated(string(args.Peek("mcp_client_id")))
	q.Kinds = parseCommaSeparated(string(args.Peek("kind")))
	if s := string(args.Peek("limit")); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			SendError(ctx, fasthttp.StatusBadRequest, "Invalid limit parameter: must be a number")
			return q, false
		}
		if n <= 0 {
			SendError(ctx, fasthttp.StatusBadRequest, "Invalid limit parameter: must be greater than zero")
			return q, false
		}
		if n > mcpSessionsMaxLimit {
			n = mcpSessionsMaxLimit
		}
		q.Limit = n
	}
	if s := string(args.Peek("offset")); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			SendError(ctx, fasthttp.StatusBadRequest, "Invalid offset parameter: must be a number")
			return q, false
		}
		if n < 0 {
			SendError(ctx, fasthttp.StatusBadRequest, "Invalid offset parameter: must be non-negative")
			return q, false
		}
		q.Offset = n
	}
	return q, true
}

// kindAllowed reports whether the given wire-row kind passes the optional
// kind filter. Empty filter matches all. The UI's Type column shows the
// auth kind (OAuth / Headers) for all rows — including pending flows —
// so flows follow the same filter as their eventual credential row:
// kindAllowed("token") admits both token credentials and OAuth flows;
// kindAllowed("header") admits both header credentials and header-
// submission flows.
func (q mcpSessionsListQuery) kindAllowed(kind string) bool {
	if len(q.Kinds) == 0 {
		return true
	}
	for _, k := range q.Kinds {
		if k == kind {
			return true
		}
	}
	return false
}

// statusFilterAllowsPending reports whether the status filter would
// admit a row with status='pending'. Empty filter (no filter) admits
// everything, so returns true.
func (q mcpSessionsListQuery) statusFilterAllowsPending() bool {
	if len(q.Filters.Statuses) == 0 {
		return true
	}
	for _, s := range q.Filters.Statuses {
		if s == "pending" {
			return true
		}
	}
	return false
}

// list returns sessions visible to the caller, filtered + paginated. Each
// row's identity column matches the caller's derived AuthMode + identity
// (enforced by DAC scope at the configstore layer). Filtering is pushed
// to SQL via the four List* store methods; pagination + cross-table
// de-dup + sort happens here because per-table LIMIT/OFFSET would not
// compose into a correct global page.
func (h *MCPSessionsHandler) list(ctx *fasthttp.RequestCtx) {
	q, ok := parseMCPSessionsListQuery(ctx)
	if !ok {
		return
	}

	// Skip store calls we know can't contribute rows. Cuts pointless DB
	// work for common filter shapes:
	//   - kind=token only → only the tokens table
	//   - kind=header only → only the headers table
	//   - status excludes pending → skip both flow tables (flow rows are
	//     always status='pending')
	// We still fetch tokens/headers when their corresponding flow table is
	// queried, because cross-table de-dup (suppress a flow row when a
	// matching token/header credential exists) needs that data.
	var (
		tokens      []tables.TableOauthUserToken
		flows       []tables.TableOauthUserSession
		headerCreds []tables.TableMCPPerUserHeaderCredential
		headerFlows []tables.TableMCPPerUserHeaderFlow
		err         error
	)
	queryFlows := q.kindAllowed("token") && q.statusFilterAllowsPending()
	queryHeaderFlows := q.kindAllowed("header") && q.statusFilterAllowsPending()
	queryTokens := q.kindAllowed("token")
	queryHeaders := q.kindAllowed("header")

	if queryTokens {
		tokens, err = h.store.ConfigStore.ListOauthUserTokens(ctx, q.Filters)
	}
	if err == nil && queryFlows {
		flows, err = h.store.ConfigStore.ListPendingOauthUserSessions(ctx, q.Filters)
	}
	if err == nil && queryHeaders {
		headerCreds, err = h.store.ConfigStore.ListMCPPerUserHeaderCredentials(ctx, q.Filters)
	}
	if err == nil && queryHeaderFlows {
		headerFlows, err = h.store.ConfigStore.ListPendingMCPPerUserHeaderFlows(ctx, q.Filters)
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
	// Same de-dup model for headers: a credential row + a pending flow
	// for the same binding means the user re-initiated submission for an
	// already-stored credential. Surface the credential row only.
	headerCredBindings := make(map[sessionBindingKey]struct{}, len(headerCreds))
	for _, c := range headerCreds {
		headerCredBindings[bindingKeyFromHeaderCredential(c)] = struct{}{}
	}

	rows := make([]mcpSessionRow, 0, len(tokens)+len(flows)+len(headerCreds)+len(headerFlows))
	if q.kindAllowed("token") {
		for _, t := range tokens {
			rows = append(rows, tokenRow(t))
		}
	}
	if queryFlows {
		for _, f := range flows {
			if _, hasToken := tokenBindings[bindingKeyFromFlow(f)]; hasToken {
				continue
			}
			rows = append(rows, flowRow(f))
		}
	}
	if q.kindAllowed("header") {
		for _, c := range headerCreds {
			rows = append(rows, headerCredentialRow(c))
		}
	}
	if queryHeaderFlows {
		for _, f := range headerFlows {
			if _, hasCred := headerCredBindings[bindingKeyFromHeaderFlow(f)]; hasCred {
				continue
			}
			rows = append(rows, headerFlowRow(f))
		}
	}

	// Stable sort across the merged set so pagination is deterministic.
	// The per-table queries each ORDER BY created_at DESC, but cross-table
	// merge order is undefined without this step.
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].CreatedAt > rows[j].CreatedAt
	})

	caller := callerUserIDFromCtx(ctx)
	for i := range rows {
		// Flow rows are completed via /api/oauth/per-user/flows/{id}/start, not
		// /reauth — passing a flow ID to /reauth always 404s before the identity
		// gate runs. Leave CanReauth at its zero value so the wire field matches
		// the endpoint's actual behavior.
		if rows[i].Kind == "flow" {
			continue
		}
		rows[i].CanReauth = canAccessUserFlow(rows[i].AuthMode, rows[i].UserID, caller)
	}

	totalCount := len(rows)
	start := min(q.Offset, totalCount)
	end := start + q.Limit
	if end > totalCount {
		end = totalCount
	}
	page := rows[start:end]

	SendJSON(ctx, mcpSessionsListResponse{
		Sessions:   page,
		Count:      len(page),
		TotalCount: totalCount,
		Limit:      q.Limit,
		Offset:     q.Offset,
	})
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

// callerUserIDFromCtx pulls the SCIM-authenticated user_id off the request
// context. Returns "" when unauthenticated or in OSS — callers should treat
// empty as "no user identity" (which fails the user-mode access gate).
func callerUserIDFromCtx(ctx *fasthttp.RequestCtx) string {
	v, _ := ctx.UserValue(schemas.BifrostContextKeyUserID).(string)
	return v
}

// canAccessUserFlow is the single identity gate for user-mode flow/credential
// rows across every surface that lets a caller act on one: POST /reauth,
// the per-user OAuth flow detail / start endpoints, and the per-user-headers
// flow detail / submit endpoints. The CanReauth field on the sessions list
// rows uses it too so the UI hides actions the caller cannot perform.
//
// For user-mode rows the caller's user_id must match the row's user_id —
// admin DAC scope lets them see the row, but the OAuth callback or header
// submission lands under whoever clicks the URL, so an admin acting here
// would either overwrite identity or plant secret material under the bound
// user's name. VK / session rows are intentionally shared and have no
// identity-of-clicker problem; the gate passes through for those modes.
func canAccessUserFlow(authMode string, rowUserID *string, callerUserID string) bool {
	if authMode != string(schemas.MCPAuthModeUser) {
		return true
	}
	if rowUserID == nil || *rowUserID == "" {
		return false
	}
	return callerUserID != "" && callerUserID == *rowUserID
}

// reauth starts a fresh OAuth flow OR a fresh header-submission flow for
// the MCP client backing the given session row. The row's table determines
// the branch — header credential rows mint a header submission flow, OAuth
// token rows mint an OAuth flow. Returns the URL the caller must visit
// (authorize_url for OAuth, submit_url for headers) along with the new
// flow's ID under session_id (kept identical for UI symmetry).
func (h *MCPSessionsHandler) reauth(ctx *fasthttp.RequestCtx) {
	rowID, ok := ctx.UserValue("id").(string)
	if !ok || rowID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid session id")
		return
	}
	bfCtx, cancel := lib.ConvertToBifrostContext(ctx, h.store)
	defer cancel()

	// Header credential rows and OAuth token rows are both UUIDs in
	// separate tables. Try headers first; on miss fall through to OAuth.
	// Don't swallow store errors here — a real DB outage would otherwise
	// surface as a misleading 404 from the OAuth fallback path, which
	// hides the outage from the caller and from any retry logic that
	// switches on status code.
	headerCred, headerCredErr := h.store.ConfigStore.GetMCPPerUserHeaderCredentialByID(ctx, rowID)
	if headerCredErr != nil {
		logger.Error("[mcp/sessions] load header credential failed: id=%s err=%v", rowID, headerCredErr)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to load MCP session")
		return
	}
	if headerCred != nil {
		h.reauthHeaderCredential(ctx, bfCtx, headerCred)
		return
	}

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

	// Identity gate: user-mode rows can only be reauthed by the bound user.
	// Admin DAC scope is enough to see the row (revoke/list still work), but
	// the OAuth callback lands under whoever clicks the authorize URL — an
	// admin clicking would either get a "wrong user" credential or overwrite
	// the row's identity. Mirrored on the wire via mcpSessionRow.CanReauth.
	if !canAccessUserFlow(tok.AuthMode, tok.UserID, callerUserIDFromCtx(ctx)) {
		SendError(ctx, fasthttp.StatusForbidden, "Only the bound user can re-authenticate this session.")
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

// reauthHeaderCredential is the header-credential branch of reauth: creates
// a fresh per-user-headers submission flow row keyed to the credential's
// existing identity, then returns the submission URL. Mirrors the OAuth
// branch's call to InitiateUserOAuthFlow — same shape on the wire so the UI
// can render a single "click → redirect" affordance for both kinds.
func (h *MCPSessionsHandler) reauthHeaderCredential(ctx *fasthttp.RequestCtx, bfCtx *schemas.BifrostContext, cred *tables.TableMCPPerUserHeaderCredential) {
	if cred.Status == "orphaned" {
		SendError(ctx, fasthttp.StatusForbidden, "Access to this MCP has been revoked. Re-submitting headers will not restore access - contact your administrator.")
		return
	}
	// Identity gate: user-mode header credentials can only be resubmitted by the
	// bound user. An admin clicking through would submit secret material under
	// their own identity, attached to the bound user's row. Same rule as the
	// OAuth branch; surfaced to the UI via mcpSessionRow.CanReauth.
	if !canAccessUserFlow(cred.AuthMode, cred.UserID, callerUserIDFromCtx(ctx)) {
		SendError(ctx, fasthttp.StatusForbidden, "Only the bound user can re-submit headers for this session.")
		return
	}
	provider := h.store.MCPHeadersProvider
	if provider == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "Per-user headers provider not configured")
		return
	}

	rowMode := schemas.MCPAuthMode(cred.AuthMode)
	identity := ""
	switch rowMode {
	case schemas.MCPAuthModeUser:
		if cred.UserID != nil {
			identity = *cred.UserID
		}
	case schemas.MCPAuthModeVK:
		if cred.VirtualKeyID != nil {
			identity = *cred.VirtualKeyID
		}
	case schemas.MCPAuthModeSession:
		identity = cred.SessionID
	}
	if identity == "" {
		SendError(ctx, fasthttp.StatusInternalServerError, "Credential row is missing an identity column for its auth mode")
		return
	}

	baseURL := lib.BuildBaseURL(ctx, h.store.GetMCPExternalClientURL())
	if baseURL == "" {
		SendError(ctx, fasthttp.StatusInternalServerError, "Could not derive callback base URL")
		return
	}
	initiation, err := provider.InitiateUserSubmissionFlow(bfCtx, rowMode, identity, cred.MCPClientID, baseURL)
	if err != nil {
		logger.Error("[mcp/sessions] reauth header flow init failed: cred=%s err=%v", cred.ID, err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to initiate header resubmission")
		return
	}
	logger.Debug("[mcp/sessions] reauth header initiated: cred=%s mcp_client=%s mode=%s flow=%s", cred.ID, cred.MCPClientID, rowMode, initiation.FlowID)
	SendJSON(ctx, map[string]any{
		// submit_url is set so a future header-specific client can branch
		// cleanly; authorize_url stays compatible with the existing UI which
		// already does window.location.href = res.authorize_url.
		"authorize_url": initiation.FrontendURL,
		"submit_url":    initiation.FrontendURL,
		"session_id":    initiation.FlowID,
		"kind":          "headers",
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
	// Row IDs from three tables (OAuth tokens, header credentials, header
	// flows) are all UUIDs. Try headers first (credential, then pending
	// flow); on miss fall through to OAuth tokens. Each branch returns;
	// only one delete runs per request. Store errors must surface as 500
	// (not be swallowed into a miss) so a DB outage doesn't manifest as
	// a misleading 404 from the OAuth fallback.
	headerCred, headerCredErr := h.store.ConfigStore.GetMCPPerUserHeaderCredentialByID(ctx, rowID)
	if headerCredErr != nil {
		logger.Error("[mcp/sessions] load header credential failed: id=%s err=%v", rowID, headerCredErr)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to delete MCP session")
		return
	}
	if headerCred != nil {
		// Drop pending submission flow rows for the same binding BEFORE
		// the credential. If a flow finishes (submit lands) after the
		// credential is gone, the upsert would mint a fresh credential and
		// undo the revoke — same race protection as the OAuth revoke path.
		credMode := schemas.MCPAuthMode(headerCred.AuthMode)
		credIdentity := ""
		switch credMode {
		case schemas.MCPAuthModeUser:
			if headerCred.UserID != nil {
				credIdentity = *headerCred.UserID
			}
		case schemas.MCPAuthModeVK:
			if headerCred.VirtualKeyID != nil {
				credIdentity = *headerCred.VirtualKeyID
			}
		case schemas.MCPAuthModeSession:
			credIdentity = headerCred.SessionID
		}
		if credIdentity != "" {
			if delErr := h.store.ConfigStore.DeleteMCPPerUserHeaderFlowsByModeIdentityAndMCPClient(ctx, credMode, credIdentity, headerCred.MCPClientID); delErr != nil {
				logger.Error("[mcp/sessions] clearing header flow rows failed: cred=%s err=%v", rowID, delErr)
				SendError(ctx, fasthttp.StatusInternalServerError, "Failed to delete MCP session")
				return
			}
		}
		if err := h.store.ConfigStore.DeleteMCPPerUserHeaderCredential(ctx, headerCred.ID); err != nil {
			logger.Error("[mcp/sessions] delete header credential failed: id=%s err=%v", rowID, err)
			SendError(ctx, fasthttp.StatusInternalServerError, "Failed to delete MCP session")
			return
		}
		logger.Debug("[mcp/sessions] revoked header credential: id=%s mcp_client=%s mode=%s", rowID, headerCred.MCPClientID, headerCred.AuthMode)
		ctx.SetStatusCode(fasthttp.StatusNoContent)
		return
	}
	headerFlow, headerFlowErr := h.store.ConfigStore.GetMCPPerUserHeaderFlowByID(ctx, rowID)
	if headerFlowErr != nil {
		logger.Error("[mcp/sessions] load header flow failed: id=%s err=%v", rowID, headerFlowErr)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to delete MCP session")
		return
	}
	if headerFlow != nil {
		if err := h.store.ConfigStore.DeleteMCPPerUserHeaderFlow(ctx, headerFlow.ID); err != nil {
			logger.Error("[mcp/sessions] delete header flow failed: id=%s err=%v", rowID, err)
			SendError(ctx, fasthttp.StatusInternalServerError, "Failed to delete MCP session")
			return
		}
		logger.Debug("[mcp/sessions] revoked header flow: id=%s mcp_client=%s mode=%s", rowID, headerFlow.MCPClientID, headerFlow.FlowMode)
		ctx.SetStatusCode(fasthttp.StatusNoContent)
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
// Permission model: the caller's identity must match the row's identity column
// for the row's flow_mode. Enforced at the configstore layer via DAC scope.
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
	if !canAccessUserFlow(flow.FlowMode, flow.UserID, callerUserIDFromCtx(ctx)) {
		SendError(ctx, fasthttp.StatusForbidden, "This authentication link is bound to a different user.")
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
	flow, err := h.loadAuthorizedFlow(ctx, flowID)
	if err != nil {
		_ = err
		return
	}
	if !canAccessUserFlow(flow.FlowMode, flow.UserID, callerUserIDFromCtx(ctx)) {
		SendError(ctx, fasthttp.StatusForbidden, "This authentication link is bound to a different user.")
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
// the store returns (nil, nil) and we surface 404. Writes the appropriate
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
		AuthKind:      "oauth",
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
		AuthKind:      "oauth",
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

// headerCredentialRow maps a per-user MCP header credential row to the wire
// shape. No expires_at / last_refreshed_at / oauth_config_id — those are
// OAuth-specific. updated_at is included so the UI can show "submitted Xm
// ago" / "edited Xm ago".
func headerCredentialRow(c tables.TableMCPPerUserHeaderCredential) mcpSessionRow {
	updated := c.UpdatedAt.UTC().Format(rfc3339Nano)
	row := mcpSessionRow{
		ID:        c.ID,
		Kind:      "header",
		AuthKind:  "headers",
		AuthMode:  c.AuthMode,
		UserID:    c.UserID,
		Status:    c.Status,
		CreatedAt: c.CreatedAt.UTC().Format(rfc3339Nano),
		UpdatedAt: &updated,
	}
	if c.MCPClient != nil {
		row.MCPClient = &mcpClientSummary{ClientID: c.MCPClient.ClientID, Name: c.MCPClient.Name}
	} else {
		row.MCPClient = &mcpClientSummary{ClientID: c.MCPClientID}
	}
	if c.VirtualKey != nil {
		row.VirtualKey = &virtualKeySummary{ID: c.VirtualKey.ID, Name: c.VirtualKey.Name}
	} else if c.VirtualKeyID != nil {
		row.VirtualKey = &virtualKeySummary{ID: *c.VirtualKeyID}
	}
	if c.User != nil {
		row.User = &userSummary{ID: c.User.ID, Name: c.User.Name}
	}
	if c.AuthMode == string(schemas.MCPAuthModeSession) && c.SessionID != "" {
		s := c.SessionID
		row.SessionID = &s
	}
	return row
}

// headerFlowRow maps a pending per-user header submission flow row to the
// wire shape. Mirrors flowRow for OAuth — same "Pending" kind status so the
// UI renders it uniformly with OAuth pending flows.
func headerFlowRow(f tables.TableMCPPerUserHeaderFlow) mcpSessionRow {
	exp := f.ExpiresAt.UTC().Format(rfc3339Nano)
	row := mcpSessionRow{
		ID:        f.ID,
		Kind:      "flow",
		AuthKind:  "headers",
		AuthMode:  f.FlowMode,
		UserID:    f.UserID,
		Status:    f.Status,
		ExpiresAt: &exp,
		CreatedAt: f.CreatedAt.UTC().Format(rfc3339Nano),
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

// bindingKeyFromHeaderCredential / bindingKeyFromHeaderFlow build a comparable
// key for de-dup between credentials and their in-flight resubmission flow.
// Mirrors bindingKeyFromToken / bindingKeyFromFlow on the OAuth side.
func bindingKeyFromHeaderCredential(c tables.TableMCPPerUserHeaderCredential) sessionBindingKey {
	k := sessionBindingKey{Mode: c.AuthMode, MCPClientID: c.MCPClientID}
	switch schemas.MCPAuthMode(c.AuthMode) {
	case schemas.MCPAuthModeUser:
		if c.UserID != nil {
			k.Identity = *c.UserID
		}
	case schemas.MCPAuthModeVK:
		if c.VirtualKeyID != nil {
			k.Identity = *c.VirtualKeyID
		}
	case schemas.MCPAuthModeSession:
		k.Identity = c.SessionID
	}
	return k
}

func bindingKeyFromHeaderFlow(f tables.TableMCPPerUserHeaderFlow) sessionBindingKey {
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

const rfc3339Nano = "2006-01-02T15:04:05.999999999Z07:00"
