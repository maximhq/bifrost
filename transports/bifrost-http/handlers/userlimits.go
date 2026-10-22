package handlers

import (
	"errors"
	"fmt"
	"strings"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/warp"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// UserLimitsHandler serves what caps one person's spend: the budgets and rate
// limits their access profiles put on them, with live usage. It answers from
// the same reader and projection as Warp's describe_user_limits. Only a
// deployment with per-user governance (Enterprise access profiles) has a
// reader, so only there are the routes registered.
type UserLimitsHandler struct {
	reader warp.UserGovernanceReader
}

// NewUserLimitsHandler returns the handler for reader.
func NewUserLimitsHandler(reader warp.UserGovernanceReader) *UserLimitsHandler {
	return &UserLimitsHandler{reader: reader}
}

// RegisterRoutes registers the user limits routes. The path parameter is named
// user_id to match the other /api/governance/users/{user_id} routes, which the
// router requires.
func (h *UserLimitsHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET("/api/governance/users/me/limits", lib.ChainMiddlewares(h.getMyLimits, middlewares...))
	r.GET("/api/governance/users/{user_id}/limits", lib.ChainMiddlewares(h.getUserLimits, middlewares...))
}

// getUserLimits returns one user's limits. A user the caller may not see is 404.
func (h *UserLimitsHandler) getUserLimits(ctx *fasthttp.RequestCtx) {
	id, _ := ctx.UserValue("user_id").(string)
	h.sendLimits(ctx, strings.TrimSpace(id))
}

// getMyLimits returns the limits of the user the request acts for: the
// signed-in user, or for an API key the user who created it.
func (h *UserLimitsHandler) getMyLimits(ctx *fasthttp.RequestCtx) {
	id, _ := ctx.UserValue(schemas.BifrostContextKeyUserID).(string)
	if strings.TrimSpace(id) == "" {
		SendError(ctx, fasthttp.StatusNotFound, "this request does not act for a user, so it has no user limits")
		return
	}
	h.sendLimits(ctx, strings.TrimSpace(id))
}

func (h *UserLimitsHandler) sendLimits(ctx *fasthttp.RequestCtx, userID string) {
	if userID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "user id is required")
		return
	}
	limits, err := warp.DescribeUserLimits(ctx, h.reader, userID)
	if errors.Is(err, warp.ErrUserNotFound) {
		SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("no user %q visible to this caller", userID))
		return
	}
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to read user limits: %v", err))
		return
	}
	SendJSON(ctx, limits)
}
