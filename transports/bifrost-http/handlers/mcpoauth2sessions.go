package handlers

import (
	"errors"
	"strconv"
	"strings"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// OAuth2SessionsHandler serves the Connected Clients API used by the sessions
// management UI to list active downstream grants and revoke them.
type OAuth2SessionsHandler struct {
	store *lib.Config
}

// NewOAuth2SessionsHandler creates a new sessions handler.
func NewOAuth2SessionsHandler(store *lib.Config) *OAuth2SessionsHandler {
	return &OAuth2SessionsHandler{store: store}
}

// RegisterRoutes wires the Connected Clients endpoints.
func (h *OAuth2SessionsHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET("/api/oauth2/sessions", lib.ChainMiddlewares(h.listSessions, middlewares...))
	r.DELETE("/api/oauth2/sessions/{id}", lib.ChainMiddlewares(h.revokeSession, middlewares...))
}

const (
	oauth2SessionsDefaultLimit = 50
	oauth2SessionsMaxLimit     = 500
)

// oauth2SessionsListResponse is the wire shape for GET /api/oauth2/sessions.
// Mirrors the MCP auth-sessions list contract (sessions + count/total_count/
// limit/offset) so the grants UI paginates the same way.
type oauth2SessionsListResponse struct {
	Sessions   []configstore.OAuth2SessionRow `json:"sessions"`
	Count      int                            `json:"count"`
	TotalCount int                            `json:"total_count"`
	Limit      int                            `json:"limit"`
	Offset     int                            `json:"offset"`
}

// oauth2SessionsListQuery is the parsed query string for GET /api/oauth2/sessions.
// Modes filters on bf_mode (user/vk/session); Search is a case-insensitive
// substring matched against the client name/id and the bound identity.
// Limit/Offset paginate the filtered result.
type oauth2SessionsListQuery struct {
	Search string
	Modes  []string
	Limit  int
	Offset int
}

// parseOAuth2SessionsListQuery extracts pagination + filter params from the
// request query string. On validation failure it writes a 400 response and
// returns ok=false — the caller must early-return without further writes.
func parseOAuth2SessionsListQuery(ctx *fasthttp.RequestCtx) (oauth2SessionsListQuery, bool) {
	q := oauth2SessionsListQuery{Limit: oauth2SessionsDefaultLimit}
	args := ctx.QueryArgs()
	q.Search = strings.TrimSpace(string(args.Peek("q")))
	q.Modes = parseCommaSeparated(string(args.Peek("bf_mode")))
	// bf_mode only ever holds user/vk/session. Reject anything else with a 400
	// rather than letting an unknown mode silently match no rows (the SQL filter
	// would just return an empty page, hiding the typo from the caller).
	for _, mode := range q.Modes {
		switch mode {
		case "user", "vk", "session":
		default:
			SendError(ctx, fasthttp.StatusBadRequest, "Invalid bf_mode parameter: must be one or more of user, vk, session")
			return q, false
		}
	}
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
		if n > oauth2SessionsMaxLimit {
			n = oauth2SessionsMaxLimit
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

// GET /api/oauth2/sessions — list active downstream grants, filtered + paginated.
// Filtering (search + mode) and pagination (limit/offset) are pushed to SQL by
// the store; the single-table source has no cross-table merge, so — unlike the
// MCP auth-sessions handler — there is nothing to slice here. The store also
// returns the total count matching the filters, used for the page indicator.
func (h *OAuth2SessionsHandler) listSessions(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "config store unavailable")
		return
	}
	q, ok := parseOAuth2SessionsListQuery(ctx)
	if !ok {
		return
	}
	sessions, totalCount, err := h.store.ConfigStore.ListOAuth2Sessions(ctx, configstore.OAuth2SessionsQueryParams{
		Search: q.Search,
		Modes:  q.Modes,
		Limit:  q.Limit,
		Offset: q.Offset,
	})
	if err != nil {
		logger.Error("oauth2 sessions: failed to list sessions: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to list sessions")
		return
	}

	SendJSON(ctx, oauth2SessionsListResponse{
		Sessions:   sessions,
		Count:      len(sessions),
		TotalCount: int(totalCount),
		Limit:      q.Limit,
		Offset:     q.Offset,
	})
}

// DELETE /api/oauth2/sessions/{id} — revoke a specific downstream grant.
func (h *OAuth2SessionsHandler) revokeSession(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "config store unavailable")
		return
	}
	id, ok := ctx.UserValue("id").(string)
	if !ok || id == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid session id")
		return
	}

	// Authorization is by visibility: the scoped read returns not-found for rows
	// outside the caller's scope, and RevokeOAuth2Session itself is unscoped, so
	// this load is what stops a caller from revoking a grant they cannot see.
	//
	// Revoke is intentionally not restricted beyond that. It's destructive cleanup
	// — it only stamps revoked_at and never acts under the grant's identity — so
	// any caller who can see a row (its owner, a team lead, or an admin) may revoke
	// it, across all modes. This matches the per-user MCP session revoke.
	// Re-authentication is gated separately to the bound user, because that path
	// mints credentials under the user's identity; revoke does not.
	if _, err := h.store.ConfigStore.GetOAuth2SessionByID(ctx, id); err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, "session not found or already revoked")
			return
		}
		logger.Error("oauth2 sessions: failed to load session: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to load session")
		return
	}

	if err := h.store.ConfigStore.RevokeOAuth2Session(ctx, id); err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, "session not found or already revoked")
			return
		}
		logger.Error("oauth2 sessions: failed to revoke session: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to revoke session")
		return
	}
	ctx.SetStatusCode(fasthttp.StatusNoContent)
}
