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
		Usage: &schemas.GenericUsageConfig{CostPerRequest: 0.01},
		Routes: []schemas.GenericRouteConfig{
			{Method: "post", Path: "/search", Usage: &schemas.GenericUsageConfig{CostPerRequest: 0.02}},
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
}
