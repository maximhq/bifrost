package handlers

import (
	"testing"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// countRegisteredRoute reports how many times a method/path pair appears in the router inventory.
func countRegisteredRoute(r *router.Router, method, path string) int {
	count := 0
	for _, registeredPath := range r.List()[method] {
		if registeredPath == path {
			count++
		}
	}
	return count
}

// TestGovernanceRouteOverridesReplaceTeamFamily verifies downstream Team ownership does not duplicate OSS routes.
func TestGovernanceRouteOverridesReplaceTeamFamily(t *testing.T) {
	r := router.New()
	h := &GovernanceHandler{}
	overrideCalls := 0

	h.RegisterRoutesWithOverrides(r, GovernanceRouteOverrides{
		Teams: &GovernanceTeamRouteOverrides{
			List: func(ctx *fasthttp.RequestCtx) {
				ctx.SetBodyString("enterprise")
			},
			Extensions: func(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
				overrideCalls++
				r.GET("/api/governance/teams/{team_id}/members", func(*fasthttp.RequestCtx) {})
			},
		},
	})

	if overrideCalls != 1 {
		t.Fatalf("override registrar calls = %d, want 1", overrideCalls)
	}
	if got := countRegisteredRoute(r, fasthttp.MethodGet, "/api/governance/teams"); got != 1 {
		t.Fatalf("GET collection registrations = %d, want 1", got)
	}
	if got := countRegisteredRoute(r, fasthttp.MethodPut, "/api/governance/teams/{team_id}"); got != 1 {
		t.Fatalf("PUT item registrations = %d, want 1", got)
	}
	if got := countRegisteredRoute(r, fasthttp.MethodGet, "/api/governance/teams/{team_id}/members"); got != 1 {
		t.Fatalf("GET members registrations = %d, want 1", got)
	}

	ctx := &fasthttp.RequestCtx{}
	handler, _ := r.Lookup(fasthttp.MethodGet, "/api/governance/teams", ctx)
	if handler == nil {
		t.Fatal("canonical Team collection handler was not registered")
	}
	handler(ctx)
	if got := string(ctx.Response.Body()); got != "enterprise" {
		t.Fatalf("canonical Team handler response = %q, want enterprise override", got)
	}
}

// TestGovernanceRouteOverridesDefaultToOSS verifies OSS retains its Team route family without an override.
func TestGovernanceRouteOverridesDefaultToOSS(t *testing.T) {
	r := router.New()
	h := &GovernanceHandler{}
	h.RegisterRoutesWithOverrides(r, GovernanceRouteOverrides{})

	for _, route := range []struct {
		method string
		path   string
	}{
		{method: fasthttp.MethodGet, path: "/api/governance/teams"},
		{method: fasthttp.MethodPost, path: "/api/governance/teams"},
		{method: fasthttp.MethodGet, path: "/api/governance/teams/{team_id}"},
		{method: fasthttp.MethodPut, path: "/api/governance/teams/{team_id}"},
		{method: fasthttp.MethodDelete, path: "/api/governance/teams/{team_id}"},
	} {
		if got := countRegisteredRoute(r, route.method, route.path); got != 1 {
			t.Fatalf("%s %s registrations = %d, want 1", route.method, route.path, got)
		}
	}
}
