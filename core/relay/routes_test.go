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

// A route matches on the header value that is actually sent: the operator's extra header, which a
// caller's copy (plain or x-bf-eh-*, any spelling) never replaces, so a caller cannot select a
// cheaper route by naming another operation; and a Go SDK caller's header names match whatever
// their casing.
func TestMatchRouteSeesSentHeaders(t *testing.T) {
	cfg := &schemas.RelayConfig{SkipRouteCheck: true, Routes: []schemas.RelayRouteConfig{
		{Method: "POST", Path: "/", Headers: map[string]string{"X-Amz-Target": "Textract.Get*"}, Billable: schemas.Ptr(false)},
		{Method: "POST", Path: "/", Headers: map[string]string{"X-Amz-Target": "Textract.AnalyzeDocument"}},
	}}
	operator := &endpoint{cfg: cfg, routes: compileRoutes(cfg), usage: resolveUsage(nil, nil), operatorHeaders: map[string]string{"x-amz-target": "Textract.AnalyzeDocument"}}
	for _, caller := range []map[string]string{
		{"x-amz-target": "Textract.GetDocumentAnalysis"},
		{"X-Amz-Target": "Textract.GetDocumentAnalysis"},
		operator.routeHeaders(nil, map[string][]string{"x-amz-target": {"Textract.GetDocumentAnalysis"}}),
	} {
		route := operator.matchRoute("POST", "/", caller)
		require.NotNil(t, route)
		assert.True(t, route.billable, "the operator's header is what is sent, so a caller's copy %v cannot select the free route", caller)
	}
	route := operator.matchRoute("POST", "/", nil)
	require.NotNil(t, route)
	assert.True(t, route.billable, "with no caller copy the operator's header is what is sent")

	sdk := &endpoint{cfg: cfg, routes: compileRoutes(cfg), usage: resolveUsage(nil, nil)}
	route = sdk.matchRoute("POST", "/", map[string]string{"X-Amz-Target": "Textract.GetDocumentAnalysis"})
	require.NotNil(t, route, "an SDK caller's canonical-case header name matches")
	assert.False(t, route.billable)
}

// A route matches only on headers the upstream request carries: one buildRequest drops (a
// caller's x-amz-* on a SigV4 relay, plain or x-bf-eh-*, or its own Authorization) cannot select
// a route, so a caller cannot reach a cheaper route with a header the upstream never sees. The
// headers that are forwarded still match.
func TestMatchRouteIgnoresDroppedHeaders(t *testing.T) {
	cfg := &schemas.RelayConfig{
		Auth: schemas.RelayAuthConfig{Type: schemas.RelayAuthAWSSigV4, AWS: &schemas.RelayAWSAuthConfig{Service: "s3"}},
		Routes: []schemas.RelayRouteConfig{
			{Method: "PUT", Path: "/**", Headers: map[string]string{"X-Amz-Acl": "public-read"}, Billable: schemas.Ptr(false)},
			{Method: "PUT", Path: "/**", Headers: map[string]string{"Authorization": "Bearer free"}, Billable: schemas.Ptr(false)},
			{Method: "POST", Path: "/", Headers: map[string]string{"X-Amz-Target": "Textract.Get*"}, Billable: schemas.Ptr(false)},
			{Method: "PUT", Path: "/**", Billable: schemas.Ptr(true)},
			{Method: "POST", Path: "/"},
		},
	}
	sigv4 := &endpoint{cfg: cfg, routes: compileRoutes(cfg), usage: resolveUsage(nil, nil)}
	billable := func(e *endpoint, method string, headers map[string]string, extra map[string][]string) bool {
		_, b, route := resolveFor(e, method, "/bucket/key", e.routeHeaders(headers, extra))
		if method == "POST" {
			_, b, route = resolveFor(e, method, "/", e.routeHeaders(headers, extra))
		}
		require.NotNil(t, route)
		return b
	}
	assert.True(t, billable(sigv4, "PUT", map[string]string{"x-amz-acl": "public-read"}, nil), "a plain x-amz-* header is dropped under SigV4, so it selects nothing")
	assert.True(t, billable(sigv4, "PUT", nil, map[string][]string{"x-amz-acl": {"public-read"}}), "so is an x-bf-eh-x-amz-* header")
	assert.True(t, billable(sigv4, "PUT", map[string]string{"authorization": "Bearer free"}, nil), "the caller's Authorization is replaced by the credential")
	assert.False(t, billable(sigv4, "POST", map[string]string{"x-amz-target": "Textract.GetDocumentAnalysis"}, nil), "x-amz-target is forwarded, so it still selects its route")
	assert.False(t, billable(sigv4, "POST", nil, map[string][]string{"x-amz-target": {"Textract.GetDocumentAnalysis"}}))

	bearerCfg := &schemas.RelayConfig{Routes: []schemas.RelayRouteConfig{
		{Method: "PUT", Path: "/**", Headers: map[string]string{"X-Amz-Acl": "public-read"}, Billable: schemas.Ptr(false)},
		{Method: "PUT", Path: "/**", Billable: schemas.Ptr(true)},
	}}
	bearer := &endpoint{cfg: bearerCfg, routes: compileRoutes(bearerCfg), usage: resolveUsage(nil, nil)}
	assert.False(t, billable(bearer, "PUT", nil, map[string][]string{"x-amz-acl": {"public-read"}}), "off SigV4 the header is forwarded, so it matches")
}

// resolveFor is the usage mapping, billing flag and route a request resolves to.
func resolveFor(e *endpoint, method, path string, headers map[string]string) (resolvedUsage, bool, *compiledRoute) {
	p := e.plan(method, path, headers)
	return p.usage, p.billing, p.route
}

// A caller header and its x-bf-eh-* copy under another spelling are one header: the caller's
// own value is applied last and is what the upstream reads, so it is the one a route matches on.
// The HTTP gateway lowercases names; a Go SDK caller may send X-Amz-Target.
func TestRouteHeadersCallerValueWinsAcrossSpellings(t *testing.T) {
	cfg := &schemas.RelayConfig{Routes: []schemas.RelayRouteConfig{
		{Method: "POST", Path: "/", Headers: map[string]string{"X-Amz-Target": "Textract.GetDocumentAnalysis"}, Billable: schemas.Ptr(false)},
		{Method: "POST", Path: "/"},
	}}
	e := &endpoint{cfg: cfg, routes: compileRoutes(cfg), usage: resolveUsage(nil, nil)}
	headers := e.routeHeaders(map[string]string{"X-Amz-Target": "Textract.AnalyzeDocument"}, map[string][]string{"x-amz-target": {"Textract.GetDocumentAnalysis"}})
	_, billable, route := resolveFor(e, "POST", "/", headers)
	require.NotNil(t, route)
	assert.True(t, billable, "the caller's AnalyzeDocument is sent, so its route is the one matched")
	assert.Len(t, headers, 1, "the two spellings are one header")
}

// BenchmarkRouteHeaders measures building the route-matching headers for a typical call: a few
// forwarded headers and no x-bf-eh-* ones, the case that must not allocate.
func BenchmarkRouteHeaders(b *testing.B) {
	cfg := &schemas.RelayConfig{Auth: schemas.RelayAuthConfig{Type: schemas.RelayAuthBearer}}
	e := &endpoint{cfg: cfg, routes: compileRoutes(cfg), usage: resolveUsage(nil, nil)}
	headers := map[string]string{"content-type": "application/json", "accept": "application/json", "user-agent": "sdk/1.0", "x-request-id": "r-1", "x-tenant": "acme"}
	b.ReportAllocs()
	for b.Loop() {
		_ = e.routeHeaders(headers, nil)
	}
}
