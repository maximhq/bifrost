package generic

import (
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRouteGlobMatch(t *testing.T) {
	cases := []struct {
		glob, path string
		want       bool
	}{
		{"/search", "/search", true},
		{"/search", "/search/", false},
		{"/search", "/searches", false},
		{"/jobs/*", "/jobs/123", true},
		{"/jobs/*", "/jobs/123/status", false},
		{"/jobs/*", "/jobs", false},
		{"/v1/**", "/v1", true},
		{"/v1/**", "/v1/a/b/c", true},
		{"/v1/**", "/v2/a", false},
		{"/v1/*/text", "/v1/abc/text", true},
		{"/v1/*/text", "/v1/abc/audio", false},
		{"/files/*.pdf", "/files/report.pdf", true},
		{"/files/*.pdf", "/files/report.txt", false},
	}
	for _, tc := range cases {
		assert.Equalf(t, tc.want, compileGlob(tc.glob).match(tc.path), "glob %q path %q", tc.glob, tc.path)
	}
}

func TestMatchRouteFirstWins(t *testing.T) {
	cfg := &schemas.GenericProviderConfig{
		Usage: &schemas.GenericUsageConfig{CostPerRequest: schemas.Ptr(0.01)},
		Routes: []schemas.GenericRouteConfig{
			{Method: "post", Path: "/search", Usage: &schemas.GenericUsageConfig{CostPerRequest: schemas.Ptr(0.02)}},
			{Method: "POST", Path: "/**", Billable: schemas.Ptr(false)},
			{Method: "GET", Path: "/jobs/*", Stream: schemas.Ptr(true)},
		},
	}
	provider := &GenericProvider{cfg: cfg, routes: compileRoutes(cfg), usage: resolveUsage(cfg.Usage, nil)}

	route := provider.matchRoute("POST", "/search")
	require.NotNil(t, route)
	assert.True(t, route.billable)
	assert.InDelta(t, 0.02, route.usage.costPerRequest, 1e-9, "the first matching route decides")

	route = provider.matchRoute("POST", "/anything/else")
	require.NotNil(t, route)
	assert.False(t, route.billable)
	assert.InDelta(t, 0.01, route.usage.costPerRequest, 1e-9, "a route without usage inherits the provider mapping")

	route = provider.matchRoute("GET", "/jobs/9")
	require.NotNil(t, route)
	require.NotNil(t, route.stream)
	assert.True(t, *route.stream)

	assert.Nil(t, provider.matchRoute("DELETE", "/search"), "method must match")

	ctx := testCtx(t)
	usage, billable, matched := provider.resolve(ctx, "PATCH", "/x")
	assert.Nil(t, matched)
	assert.False(t, billable, "no route means the method default: a PATCH is not billable")
	assert.InDelta(t, 0.01, usage.costPerRequest, 1e-9, "and the provider usage mapping")

	_, billable, matched = provider.resolve(ctx, "GET", "/x")
	assert.Nil(t, matched)
	assert.False(t, billable, "an unrouted GET is a poll or a listing: not counted")
	_, billable, _ = provider.resolve(ctx, "HEAD", "/x")
	assert.False(t, billable)
	_, billable, _ = provider.resolve(ctx, "PUT", "/x")
	assert.False(t, billable, "PUT, PATCH and DELETE update or clean up: not counted without a route saying so")
	_, billable, _ = provider.resolve(ctx, "GET", "/jobs/9")
	assert.False(t, billable, "a route without an explicit billable keeps the method default: a GET stays unbilled")
}

// A plan the transport stored on the context is what resolve uses, when it was made by this
// provider instance; a plan from another instance (the provider was rebuilt in between) or no
// plan at all falls back to matching the routes.
func TestResolveUsesPlanFromContext(t *testing.T) {
	cfg := &schemas.GenericProviderConfig{Routes: []schemas.GenericRouteConfig{{Method: "POST", Path: "/search", Billable: schemas.Ptr(false)}}}
	provider := &GenericProvider{cfg: cfg, routes: compileRoutes(cfg), usage: resolveUsage(cfg.Usage, nil)}
	other := &GenericProvider{cfg: cfg, routes: compileRoutes(cfg), usage: resolveUsage(cfg.Usage, nil)}

	ctx := testCtx(t)
	plan := provider.Plan("POST", "/search", "", false)
	require.NotNil(t, plan.route)
	assert.True(t, plan.Allowed)
	assert.False(t, plan.Streams)
	WithPlan(ctx, plan)
	_, billable, matched := provider.resolve(ctx, "POST", "/search")
	assert.Same(t, plan.route, matched, "the stored plan's route is reused")
	assert.False(t, billable)

	_, _, matched = other.resolve(ctx, "POST", "/search")
	require.NotNil(t, matched)
	assert.NotSame(t, plan.route, matched, "another instance matches its own routes")
	_, _, matched = provider.resolve(testCtx(t), "POST", "/search")
	require.NotNil(t, matched, "no plan on the context: matched here")
}

// A route's billable flag is explicit or inherits the method default, so a routes list imported
// wholesale (every operation of an OpenAPI document) does not turn polls and listings into
// charged calls, while an explicit true on a GET still counts it.
func TestRouteBillableInheritsMethodDefault(t *testing.T) {
	cfg := &schemas.GenericProviderConfig{
		Routes: []schemas.GenericRouteConfig{
			{Method: "GET", Path: "/jobs/*"},
			{Method: "HEAD", Path: "/jobs/*"},
			{Method: "POST", Path: "/jobs"},
			{Method: "DELETE", Path: "/jobs/*"},
			{Method: "GET", Path: "/report", Billable: schemas.Ptr(true)},
			{Method: "POST", Path: "/free", Billable: schemas.Ptr(false)},
		},
	}
	provider := &GenericProvider{cfg: cfg, routes: compileRoutes(cfg), usage: resolveUsage(cfg.Usage, nil)}
	cases := []struct {
		method, path string
		want         bool
	}{
		{"GET", "/jobs/1", false},
		{"HEAD", "/jobs/1", false},
		{"POST", "/jobs", true},
		{"DELETE", "/jobs/1", false},
		{"GET", "/report", true},
		{"POST", "/free", false},
	}
	for _, tc := range cases {
		_, billable, matched := provider.resolve(testCtx(t), tc.method, tc.path)
		require.NotNilf(t, matched, "%s %s must match a route", tc.method, tc.path)
		assert.Equalf(t, tc.want, billable, "%s %s", tc.method, tc.path)
	}
	_, billable, matched := provider.resolve(testCtx(t), "POST", "/unrouted")
	assert.Nil(t, matched)
	assert.True(t, billable, "an unrouted POST is billable: the same method default applies with or without a route")
}

// A route may waive the provider's flat fee by setting cost_per_request to 0, and may name its
// own model path.
func TestRouteOverridesZeroFeeAndModelPath(t *testing.T) {
	cfg := &schemas.GenericProviderConfig{
		Usage: &schemas.GenericUsageConfig{CostPerRequest: schemas.Ptr(0.05), ModelPath: "engine"},
		Routes: []schemas.GenericRouteConfig{
			{Method: "POST", Path: "/free", Usage: &schemas.GenericUsageConfig{CostPerRequest: schemas.Ptr(0.0)}},
			{Method: "POST", Path: "/named", Usage: &schemas.GenericUsageConfig{ModelPath: "name"}},
		},
	}
	provider := &GenericProvider{cfg: cfg, routes: compileRoutes(cfg), usage: resolveUsage(cfg.Usage, nil)}
	usage, billable, _ := provider.resolve(testCtx(t), "POST", "/free")
	assert.True(t, billable)
	assert.InDelta(t, 0.0, usage.costPerRequest, 1e-9, "an explicit 0 waives the provider fee")
	assert.Nil(t, usage.defaultUsage(usageFields{}).Cost)
	assert.Equal(t, "name", provider.ModelPath("POST", "/named"))
	assert.Equal(t, "engine", provider.ModelPath("POST", "/other"), "unrouted operations use the provider mapping")
	assert.Equal(t, "model", (&GenericProvider{cfg: &schemas.GenericProviderConfig{}, usage: resolveUsage(nil, nil)}).ModelPath("POST", "/x"))
}

// strict_routes turns the routes list into an allowlist: anything it does not name is refused.
func TestRouteAllowed(t *testing.T) {
	cfg := &schemas.GenericProviderConfig{Routes: []schemas.GenericRouteConfig{{Method: "POST", Path: "/search"}, {Method: "GET", Path: "/jobs/*"}}}
	open := &GenericProvider{cfg: cfg, routes: compileRoutes(cfg), usage: resolveUsage(cfg.Usage, nil)}
	assert.True(t, open.RouteAllowed("DELETE", "/anything"), "without strict_routes every operation is relayed")

	strictCfg := &schemas.GenericProviderConfig{StrictRoutes: true, Routes: cfg.Routes}
	strict := &GenericProvider{cfg: strictCfg, routes: compileRoutes(strictCfg), usage: resolveUsage(nil, nil)}
	assert.True(t, strict.RouteAllowed("POST", "/search"))
	assert.True(t, strict.RouteAllowed("GET", "/jobs/42"))
	assert.False(t, strict.RouteAllowed("DELETE", "/search"), "method must be listed")
	assert.False(t, strict.RouteAllowed("POST", "/other"))
	assert.False(t, strict.RouteAllowed("GET", "/jobs/42/status"))
}
