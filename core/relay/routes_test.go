package relay

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
	cfg := &schemas.RelayConfig{
		SkipRouteCheck: true,
		Usage:          &schemas.RelayUsageConfig{CostPerRequest: schemas.Ptr(0.01)},
		Routes: []schemas.RelayRouteConfig{
			{Method: "post", Path: "/search", Usage: &schemas.RelayUsageConfig{CostPerRequest: schemas.Ptr(0.02)}},
			{Method: "POST", Path: "/**", Billable: schemas.Ptr(false)},
			{Method: "GET", Path: "/jobs/*", Stream: schemas.Ptr(true)},
		},
	}
	provider := &endpoint{cfg: cfg, routes: compileRoutes(cfg), usage: resolveUsage(cfg.Usage, nil)}

	route := provider.matchRoute("POST", "/search", nil)
	require.NotNil(t, route)
	assert.True(t, route.billable)
	assert.InDelta(t, 0.02, route.usage.costPerRequest, 1e-9, "the first matching route decides")

	route = provider.matchRoute("POST", "/anything/else", nil)
	require.NotNil(t, route)
	assert.False(t, route.billable)
	assert.InDelta(t, 0.01, route.usage.costPerRequest, 1e-9, "a route without usage inherits the endpoint mapping")

	route = provider.matchRoute("GET", "/jobs/9", nil)
	require.NotNil(t, route)
	require.NotNil(t, route.stream)
	assert.True(t, *route.stream)

	assert.Nil(t, provider.matchRoute("DELETE", "/search", nil), "method must match")

	usage, billable, matched := resolveFor(provider, "PATCH", "/x", nil)
	assert.Nil(t, matched)
	assert.False(t, billable, "no route means the method default: a PATCH is not billable")
	assert.InDelta(t, 0.01, usage.costPerRequest, 1e-9, "and the endpoint usage mapping")

	_, billable, matched = resolveFor(provider, "GET", "/x", nil)
	assert.Nil(t, matched)
	assert.False(t, billable, "an unrouted GET is a poll or a listing: not counted")
	_, billable, _ = resolveFor(provider, "HEAD", "/x", nil)
	assert.False(t, billable)
	_, billable, _ = resolveFor(provider, "PUT", "/x", nil)
	assert.False(t, billable, "PUT, PATCH and DELETE update or clean up: not counted without a route saying so")
	_, billable, _ = resolveFor(provider, "GET", "/jobs/9", nil)
	assert.False(t, billable, "a route without an explicit billable keeps the method default: a GET stays unbilled")
}

// A route may require request headers, so AWS JSON APIs that send every operation to POST /
// and name it in X-Amz-Target get one route per operation. Names match case-insensitively (the
// transport lower-cases them), values with the single-segment glob, "/" as an ordinary character.
func TestMatchRouteHeaders(t *testing.T) {
	cfg := &schemas.RelayConfig{
		Routes: []schemas.RelayRouteConfig{
			{Method: "POST", Path: "/", Headers: map[string]string{"X-Amz-Target": "Textract.AnalyzeDocument"}, Usage: &schemas.RelayUsageConfig{CostPerRequest: schemas.Ptr(0.05)}},
			{Method: "POST", Path: "/", Headers: map[string]string{"X-Amz-Target": "Textract.Get*"}, Billable: schemas.Ptr(false)},
			{Method: "POST", Path: "/", Headers: map[string]string{"X-Amz-Target": "Textract.*", "Content-Type": "application/x-amz-json-*"}, Usage: &schemas.RelayUsageConfig{CostPerRequest: schemas.Ptr(0.0015)}},
			{Method: "POST", Path: "/plain"},
		},
	}
	provider := &endpoint{cfg: cfg, routes: compileRoutes(cfg), usage: resolveUsage(cfg.Usage, nil)}
	hdr := func(target string) map[string]string {
		return map[string]string{"x-amz-target": target, "content-type": "application/x-amz-json-1.1"}
	}

	route := provider.matchRoute("POST", "/", hdr("Textract.AnalyzeDocument"))
	require.NotNil(t, route)
	assert.InDelta(t, 0.05, route.usage.costPerRequest, 1e-9, "the exact operation wins by order")

	route = provider.matchRoute("POST", "/", hdr("Textract.GetDocumentAnalysis"))
	require.NotNil(t, route)
	assert.False(t, route.billable, "a glob picks the result polls")

	route = provider.matchRoute("POST", "/", hdr("Textract.DetectDocumentText"))
	require.NotNil(t, route)
	assert.InDelta(t, 0.0015, route.usage.costPerRequest, 1e-9, "the catch-all needs both headers, and / in a value is literal")

	assert.Nil(t, provider.matchRoute("POST", "/", map[string]string{"x-amz-target": "Textract.DetectDocumentText"}), "a route needs every listed header")
	assert.Nil(t, provider.matchRoute("POST", "/", hdr("Translate.TranslateText")), "another service matches no route")
	assert.Nil(t, provider.matchRoute("POST", "/", nil), "no headers: only header-free routes can match")
	require.NotNil(t, provider.matchRoute("POST", "/plain", nil), "a route without headers is untouched by the feature")
	assert.Nil(t, provider.matchRoute("POST", "/", hdr("textract.analyzedocument")), "values are case-sensitive, as AWS targets are")

	plan := provider.plan("POST", "/", hdr("Textract.GetDocumentAnalysis"))
	assert.True(t, plan.allowed)
	require.NotNil(t, plan.route)
	assert.False(t, plan.route.billable)
	assert.False(t, provider.plan("POST", "/", hdr("Translate.TranslateText")).allowed, "the route check refuses an unlisted operation")

	_, billable, matched := resolveFor(provider, "POST", "/", hdr("Textract.GetDocumentAnalysis"))
	require.NotNil(t, matched)
	assert.False(t, billable, "the SDK path, with no plan on the context, matches with the request headers too")
}

// A route's billable flag is explicit or inherits the method default, so a routes list imported
// wholesale (every operation of an OpenAPI document) does not turn polls and listings into
// charged calls, while an explicit true on a GET still counts it.
func TestRouteBillableInheritsMethodDefault(t *testing.T) {
	cfg := &schemas.RelayConfig{
		SkipRouteCheck: true,
		Routes: []schemas.RelayRouteConfig{
			{Method: "GET", Path: "/jobs/*"},
			{Method: "HEAD", Path: "/jobs/*"},
			{Method: "POST", Path: "/jobs"},
			{Method: "DELETE", Path: "/jobs/*"},
			{Method: "GET", Path: "/report", Billable: schemas.Ptr(true)},
			{Method: "POST", Path: "/free", Billable: schemas.Ptr(false)},
		},
	}
	provider := &endpoint{cfg: cfg, routes: compileRoutes(cfg), usage: resolveUsage(cfg.Usage, nil)}
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
		_, billable, matched := resolveFor(provider, tc.method, tc.path, nil)
		require.NotNilf(t, matched, "%s %s must match a route", tc.method, tc.path)
		assert.Equalf(t, tc.want, billable, "%s %s", tc.method, tc.path)
	}
	_, billable, matched := resolveFor(provider, "POST", "/unrouted", nil)
	assert.Nil(t, matched)
	assert.True(t, billable, "an unrouted POST is billable: the same method default applies with or without a route")
}

// A route may waive the provider's flat fee by setting cost_per_request to 0, and may name its
// own model path.
func TestRouteOverridesZeroFeeAndModelPath(t *testing.T) {
	cfg := &schemas.RelayConfig{
		SkipRouteCheck: true,
		Usage:          &schemas.RelayUsageConfig{CostPerRequest: schemas.Ptr(0.05), ModelPath: "engine"},
		Routes: []schemas.RelayRouteConfig{
			{Method: "POST", Path: "/free", Usage: &schemas.RelayUsageConfig{CostPerRequest: schemas.Ptr(0.0)}},
			{Method: "POST", Path: "/named", Usage: &schemas.RelayUsageConfig{ModelPath: "name"}},
		},
	}
	provider := &endpoint{cfg: cfg, routes: compileRoutes(cfg), usage: resolveUsage(cfg.Usage, nil)}
	usage, billable, _ := resolveFor(provider, "POST", "/free", nil)
	assert.True(t, billable)
	assert.InDelta(t, 0.0, usage.costPerRequest, 1e-9, "an explicit 0 waives the endpoint fee")
	assert.Zero(t, usage.defaultUsage(usageFields{}).Cost)
	assert.Equal(t, "name", provider.plan("POST", "/named", nil).usage.effectiveModelPath())
	assert.Equal(t, "engine", provider.plan("POST", "/other", nil).usage.effectiveModelPath(), "unrouted operations use the endpoint mapping")
	assert.Equal(t, "", (&endpoint{cfg: &schemas.RelayConfig{SkipRouteCheck: true}, usage: resolveUsage(nil, nil)}).plan("POST", "/x", nil).usage.effectiveModelPath(), "no model is read unless mapped")
}

// The routes list is an allowlist: anything it does not name is refused unless skip_route_check is set.
func TestRouteCheckAllowlist(t *testing.T) {
	cfg := &schemas.RelayConfig{SkipRouteCheck: true, Routes: []schemas.RelayRouteConfig{{Method: "POST", Path: "/search"}, {Method: "GET", Path: "/jobs/*"}}}
	open := &endpoint{cfg: cfg, routes: compileRoutes(cfg), usage: resolveUsage(cfg.Usage, nil)}
	assert.True(t, open.plan("DELETE", "/anything", nil).allowed, "with skip_route_check every operation is relayed")

	strictCfg := &schemas.RelayConfig{Routes: cfg.Routes}
	strict := &endpoint{cfg: strictCfg, routes: compileRoutes(strictCfg), usage: resolveUsage(nil, nil)}
	allowed := func(method, path string) bool { return strict.plan(method, path, nil).allowed }
	assert.True(t, allowed("POST", "/search"))
	assert.True(t, allowed("GET", "/jobs/42"))
	assert.False(t, allowed("DELETE", "/search"), "method must be listed")
	assert.False(t, allowed("POST", "/other"))
	assert.False(t, allowed("GET", "/jobs/42/status"))
}

// A route matches on the header value that is actually sent: the operator's extra header when one
// replaces the caller's, so a caller cannot select a cheaper route by naming another operation;
// and a Go SDK caller's header names match whatever their casing.
func TestMatchRouteSeesSentHeaders(t *testing.T) {
	cfg := &schemas.RelayConfig{SkipRouteCheck: true, Routes: []schemas.RelayRouteConfig{
		{Method: "POST", Path: "/", Headers: map[string]string{"X-Amz-Target": "Textract.Get*"}, Billable: schemas.Ptr(false)},
		{Method: "POST", Path: "/", Headers: map[string]string{"X-Amz-Target": "Textract.AnalyzeDocument"}},
	}}
	operator := &endpoint{cfg: cfg, routes: compileRoutes(cfg), usage: resolveUsage(nil, nil), operatorHeaders: map[string]string{"x-amz-target": "Textract.AnalyzeDocument"}}
	route := operator.matchRoute("POST", "/", map[string]string{"x-amz-target": "Textract.GetDocumentAnalysis"})
	require.NotNil(t, route)
	assert.True(t, route.billable, "the operator's header is what is sent, so its route bills")

	sdk := &endpoint{cfg: cfg, routes: compileRoutes(cfg), usage: resolveUsage(nil, nil)}
	route = sdk.matchRoute("POST", "/", map[string]string{"X-Amz-Target": "Textract.GetDocumentAnalysis"})
	require.NotNil(t, route, "an SDK caller's canonical-case header name matches")
	assert.False(t, route.billable)
}

// resolveFor is the usage mapping, billing flag and route a request resolves to.
func resolveFor(e *endpoint, method, path string, headers map[string]string) (resolvedUsage, bool, *compiledRoute) {
	p := e.plan(method, path, headers)
	return p.usage, p.billing, p.route
}
