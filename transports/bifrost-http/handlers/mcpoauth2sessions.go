package handlers

import (
	"errors"
	"fmt"

	"github.com/bytedance/sonic"
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

// GET /api/oauth2/sessions — list active downstream grants.
func (h *OAuth2SessionsHandler) listSessions(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "config store unavailable")
		return
	}
	sessions, err := h.store.ConfigStore.ListOAuth2Sessions(ctx)
	if err != nil {
		logger.Error("oauth2 sessions: failed to list sessions: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to list sessions")
		return
	}
	data, err := sonic.Marshal(map[string]any{"sessions": sessions})
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to encode sessions response: %v", err))
		return
	}
	ctx.SetContentType("application/json")
	ctx.SetBody(data)
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
