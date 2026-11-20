package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/warp"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// GET /api/session/me reports the caller the way Warp's tools are scoped: an
// OSS admin is unidentified and unrestricted; a user the deployment restricts
// is identified, restricted and told what they may see; the local admin is
// never restricted.
func TestCallerRoute(t *testing.T) {
	restrict := func(context.Context) warp.CallerRestriction {
		return warp.CallerRestriction{Restricted: true, Visibility: "their teams' traffic"}
	}
	call := func(resolve warp.CallerRestrictionResolver, values map[any]any) CallerResponse {
		r := router.New()
		NewCallerHandler(resolve).RegisterRoutes(r)
		var req fasthttp.Request
		req.SetRequestURI("/api/session/me")
		ctx := initCtx(&req)
		for key, value := range values {
			ctx.SetUserValue(key, value)
		}
		r.Handler(ctx)
		require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
		var out CallerResponse
		require.NoError(t, json.Unmarshal(ctx.Response.Body(), &out))
		return out
	}

	require.Equal(t, CallerResponse{Unrestricted: true}, call(nil, nil))
	require.Equal(t,
		CallerResponse{Identified: true, UserID: "u-1", Visibility: "their teams' traffic"},
		call(restrict, map[any]any{schemas.BifrostContextKeyUserID: "u-1"}))
	require.Equal(t,
		CallerResponse{Unrestricted: true},
		call(restrict, map[any]any{schemas.IsLocalAdminContextKey: true}))
}

// BenchmarkCallerRoute measures GET /api/session/me: routing, resolving the
// caller and writing the answer. "oss" has no resolver (an OSS admin);
// "restricted" runs a resolver that restricts an identified user, the most
// work the handler does. The resolver is a fake, so whatever a real
// deployment's resolver reads is left out.
func BenchmarkCallerRoute(b *testing.B) {
	restrict := func(context.Context) warp.CallerRestriction {
		return warp.CallerRestriction{Restricted: true, Visibility: "their teams' traffic"}
	}
	for _, tc := range []struct {
		name    string
		resolve warp.CallerRestrictionResolver
		userID  string
	}{{"oss", nil, ""}, {"restricted", restrict, "u-1"}} {
		b.Run(tc.name, func(b *testing.B) {
			r := router.New()
			NewCallerHandler(tc.resolve).RegisterRoutes(r)
			var req fasthttp.Request
			req.SetRequestURI("/api/session/me")
			b.ReportAllocs()
			for b.Loop() {
				ctx := initCtx(&req)
				if tc.userID != "" {
					ctx.SetUserValue(schemas.BifrostContextKeyUserID, tc.userID)
				}
				r.Handler(ctx)
			}
		})
	}
}
