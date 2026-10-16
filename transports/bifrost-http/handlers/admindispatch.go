package handlers

import (
	"context"
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// adminRoute is one admin API route a write tool may call.
type adminRoute struct {
	method string
	path   string
}

// adminDispatchRoutes is every route AdminDispatcher will dispatch to, and
// so the whole reach of Bifrost's MCP write tools. Nothing else on the internal
// router is callable, whatever a handler's RegisterRoutes happens to bring with
// it: reads go through the scoped readers in framework/mcptools, and nothing
// here deletes.
//
// Adding a route here lets a virtual key granted the matching tool make that
// change, so it is a review of the handler behind it, not of the tool: the
// handlers do not filter writes by tenant.
var adminDispatchRoutes = []adminRoute{
	{fasthttp.MethodPost, "/api/governance/virtual-keys"},
	{fasthttp.MethodPut, "/api/governance/virtual-keys/{vk_id}"},
	{fasthttp.MethodPost, "/api/governance/virtual-keys/{vk_id}/rotate"},
	{fasthttp.MethodPost, "/api/governance/teams"},
	{fasthttp.MethodPut, "/api/governance/teams/{team_id}"},
	{fasthttp.MethodPost, "/api/governance/customers"},
	{fasthttp.MethodPut, "/api/governance/customers/{customer_id}"},
	{fasthttp.MethodPost, "/api/governance/model-configs"},
	{fasthttp.MethodPut, "/api/governance/model-configs/{mc_id}"},
	{fasthttp.MethodPost, "/api/governance/pricing-overrides"},
	{fasthttp.MethodPut, "/api/governance/pricing-overrides/{id}"},

	{fasthttp.MethodPost, "/api/routing/rules"},
	{fasthttp.MethodPut, "/api/routing/rules/{rule_id}"},

	{fasthttp.MethodPost, "/api/providers"},
	{fasthttp.MethodPut, "/api/providers/{provider}"},
	{fasthttp.MethodPost, "/api/providers/{provider}/keys"},
	{fasthttp.MethodPut, "/api/providers/{provider}/keys/{key_id}"},
	{fasthttp.MethodPost, "/api/providers/{provider}/refresh-models"},
	{fasthttp.MethodPost, "/api/providers/{provider}/keys/{key_id}/refresh-models"},

	{fasthttp.MethodPost, "/api/mcp/client"},
	{fasthttp.MethodPut, "/api/mcp/client/{id}"},
	{fasthttp.MethodPost, "/api/mcp/client/{id}/reconnect"},
	{fasthttp.MethodPost, "/api/mcp/virtual-mcps"},
	{fasthttp.MethodPut, "/api/mcp/virtual-mcps/{id}"},
	{fasthttp.MethodPost, "/api/mcp/virtual-mcps/{id}/virtual-keys/{vkId}"},
	{fasthttp.MethodDelete, "/api/mcp/virtual-mcps/{id}/virtual-keys/{vkId}"},

	{fasthttp.MethodPost, "/api/plugins"},
	{fasthttp.MethodPut, "/api/plugins/{name}"},
	{fasthttp.MethodPost, "/api/webhooks"},
	{fasthttp.MethodPut, "/api/webhooks/{id}"},
	{fasthttp.MethodPut, "/api/feature-flags/{id}"},
	{fasthttp.MethodPut, "/api/warp/config"},
}

// adminDispatchRouteAllowed reports whether method and rawPath name a route in
// adminDispatchRoutes. It matches the way the router does - segment by segment
// on the raw path, a {param} taking exactly one non-empty segment - so a path
// that passes here reaches the route it was matched against and no other.
func adminDispatchRouteAllowed(method, rawPath string) bool {
	segments := strings.Split(rawPath, "/")
	for _, route := range adminDispatchRoutes {
		if route.method != method {
			continue
		}
		pattern := strings.Split(route.path, "/")
		if len(pattern) != len(segments) {
			continue
		}
		matched := true
		for i, want := range pattern {
			got := segments[i]
			if strings.HasPrefix(want, "{") && strings.HasSuffix(want, "}") {
				// A dot segment is a traversal, not an id.
				if got == "" || got == "." || got == ".." {
					matched = false
					break
				}
				continue
			}
			if got != want {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

// AdminDispatcher runs an admin API request against the admin handlers in
// this process, with no network hop. It satisfies framework/mcptools.AdminDispatcher,
// which is how Bifrost's MCP write tools reuse the handlers' validation,
// transactions and reloads instead of carrying a second copy of them.
//
// It dispatches to a router of its own, not the one the listener serves: the
// same handlers registered a second time, behind no auth middleware, reachable
// only from here. The public router and its authentication are untouched, and
// no request off the wire can arrive at the internal one.
//
// Do checks no credential - its caller has none to present. Whether this caller
// may make the change is decided by the tool before it calls Do, which makes
// every caller responsible for its own authorization. What Do does enforce is
// how far any caller can reach: adminDispatchRoutes, and the posture below.
//
// A dispatched request carries the posture of a deployment with dashboard auth
// off: local admin, with BifrostContextKeyAuthBypassed set. That is the honest
// description - an admin-level change nobody authenticated as an admin for -
// and it is what keeps the handlers' own genuine-auth gates closed to every
// dispatched request, an admin's included: native plugin paths, stdio MCP
// clients, private-network MCP targets, plugin endpoints and env./vault.
// references, private-network webhook delivery, provider base URLs and
// allow_private_network, and provider-key dial targets. Each is refused with a
// 403 and the route's reason, which callAdmin hands back to the tool; those
// changes are made in the dashboard with auth enabled, or in config.json.
type AdminDispatcher struct {
	handler func() fasthttp.RequestHandler
}

// NewAdminDispatcher builds the dispatcher over the internal router's
// handler. handler is resolved per call rather than captured, so the dispatcher
// can be built before the router is.
func NewAdminDispatcher(handler func() fasthttp.RequestHandler) *AdminDispatcher {
	return &AdminDispatcher{handler: handler}
}

// Do dispatches one request and returns the route's status and body. A refusal
// from the route is a status, not an error. The handler runs to completion on
// the calling goroutine: ctx is checked before dispatch, not during, because
// the handlers take the fasthttp request as their context.
func (a *AdminDispatcher) Do(ctx context.Context, method, path string, body []byte) (int, []byte, error) {
	if a == nil || a.handler == nil {
		return 0, nil, fmt.Errorf("admin dispatcher is not available")
	}
	if err := ctx.Err(); err != nil {
		return 0, nil, err
	}
	rawPath, _, _ := strings.Cut(path, "?")
	if !adminDispatchRouteAllowed(method, rawPath) {
		return 0, nil, fmt.Errorf("%s %s is not an admin route write tools may call", method, rawPath)
	}
	handler := a.handler()
	if handler == nil {
		return 0, nil, fmt.Errorf("admin dispatcher is not available")
	}

	var req fasthttp.Request
	req.Header.SetMethod(method)
	req.SetRequestURI(path)
	if len(body) > 0 {
		req.Header.SetContentType("application/json")
		req.SetBody(body)
	}
	var reqCtx fasthttp.RequestCtx
	reqCtx.Init(&req, nil, nil)
	reqCtx.SetUserValue(schemas.IsLocalAdminContextKey, true)
	reqCtx.SetUserValue(schemas.BifrostContextKeyAuthBypassed, true)

	handler(&reqCtx)

	// Copied out: the response buffer belongs to reqCtx.
	return reqCtx.Response.StatusCode(), append([]byte(nil), reqCtx.Response.Body()...), nil
}
