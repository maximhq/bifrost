package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/warp"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// userLimitsReader answers for the users it holds and records who was asked for.
type userLimitsReader struct {
	users map[string]*warp.UserGovernance
	asked []string
}

func (r *userLimitsReader) DescribeUserGovernance(_ context.Context, userID string) (*warp.UserGovernance, error) {
	r.asked = append(r.asked, userID)
	if gov, ok := r.users[userID]; ok {
		return gov, nil
	}
	return nil, warp.ErrUserNotFound
}

// The user limits routes return the same projection Warp's describe_user_limits
// reads, without the tool's guidance; /me resolves the caller; a user the
// reader cannot see is 404.
func TestUserLimitsRoutes(t *testing.T) {
	SetLogger(&mockLogger{})
	reader := &userLimitsReader{users: map[string]*warp.UserGovernance{
		"u-1": {UserID: "u-1", Name: "Ada", Profiles: []warp.UserGovernanceProfile{{
			Name: "engineering", Active: true,
			Budgets: []tables.TableBudget{{ID: "b-1", MaxLimit: 450, CurrentUsage: 50, ResetDuration: "1M"}},
		}}},
	}}
	r := router.New()
	NewUserLimitsHandler(reader).RegisterRoutes(r)
	get := func(path, user string) *fasthttp.RequestCtx {
		var req fasthttp.Request
		req.SetRequestURI(path)
		ctx := initCtx(&req)
		if user != "" {
			ctx.SetUserValue(schemas.BifrostContextKeyUserID, user)
		}
		r.Handler(ctx)
		return ctx
	}

	ctx := get("/api/governance/users/u-1/limits", "")
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	var limits map[string]any
	require.NoError(t, json.Unmarshal(ctx.Response.Body(), &limits))
	require.Equal(t, "u-1", limits["user_id"])
	require.Equal(t, "Ada", limits["name"])
	require.NotContains(t, limits, "guidance", "reading guidance is Warp's, not the API's")
	profile := limits["profiles"].([]any)[0].(map[string]any)
	budget := profile["budgets"].([]any)[0].(map[string]any)
	require.InDelta(t, 400, budget["remaining_usd"], 1e-9)

	ctx = get("/api/governance/users/me/limits", "u-1")
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	require.Equal(t, "u-1", reader.asked[len(reader.asked)-1], "/me asks for the caller")

	ctx = get("/api/governance/users/me/limits", "")
	require.Equal(t, fasthttp.StatusNotFound, ctx.Response.StatusCode(), "no user on the request")

	ctx = get("/api/governance/users/someone-else/limits", "u-1")
	require.Equal(t, fasthttp.StatusNotFound, ctx.Response.StatusCode(), "a user the reader cannot see")
}

// Enterprise registers its own /api/governance/users/me and
// /api/governance/users/{user_id}/... routes on the same router. A parameter
// named differently at the same position panics at startup, so the limits
// routes must register beside them.
func TestUserLimitsRoutesCoexistWithEnterpriseUserRoutes(t *testing.T) {
	r := router.New()
	noop := func(*fasthttp.RequestCtx) {}
	r.GET("/api/governance/users", noop)
	r.GET("/api/governance/users/me", noop)
	r.GET("/api/governance/users/me/permissions", noop)
	r.DELETE("/api/governance/users/{user_id}", noop)
	r.GET("/api/governance/users/{user_id}/teams", noop)
	require.NotPanics(t, func() { NewUserLimitsHandler(&userLimitsReader{}).RegisterRoutes(r) })
}
