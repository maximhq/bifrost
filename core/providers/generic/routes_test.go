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

	usage, billable, matched := provider.resolve("PUT", "/x")
	assert.Nil(t, matched)
	assert.True(t, billable, "no route means the provider defaults, billable")
	assert.InDelta(t, 0.01, usage.costPerRequest, 1e-9)

	_, billable, matched = provider.resolve("GET", "/x")
	assert.Nil(t, matched)
	assert.False(t, billable, "an unrouted GET is a poll or a listing: not counted")
	_, billable, _ = provider.resolve("HEAD", "/x")
	assert.False(t, billable)
	_, billable, _ = provider.resolve("GET", "/jobs/9")
	assert.True(t, billable, "a route makes any method billable")
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
	usage, billable, _ := provider.resolve("POST", "/free")
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
