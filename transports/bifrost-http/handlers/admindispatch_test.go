package handlers

import (
	"context"
	"strings"
	"testing"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// The allow-list is the whole reach of the MCP write tools, so what it refuses
// matters as much as what it admits: reads, deletes, routes outside the admin
// API, and any path that would match one route and resolve to another.
func TestAdminDispatchRouteAllowed(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		want   bool
	}{
		{"create virtual key", fasthttp.MethodPost, "/api/governance/virtual-keys", true},
		{"update virtual key", fasthttp.MethodPut, "/api/governance/virtual-keys/vk-1", true},
		{"rotate virtual key", fasthttp.MethodPost, "/api/governance/virtual-keys/vk-1/rotate", true},
		{"update provider key", fasthttp.MethodPut, "/api/providers/openai/keys/k-1", true},
		{"detach virtual mcp", fasthttp.MethodDelete, "/api/mcp/virtual-mcps/3/virtual-keys/vk-1", true},
		{"update warp config", fasthttp.MethodPut, "/api/warp/config", true},

		{"read is not a write tool's business", fasthttp.MethodGet, "/api/governance/virtual-keys", false},
		{"delete virtual key", fasthttp.MethodDelete, "/api/governance/virtual-keys/vk-1", false},
		{"delete provider", fasthttp.MethodDelete, "/api/providers/openai", false},
		{"whole-config write", fasthttp.MethodPut, "/api/config", false},
		{"session login", fasthttp.MethodPost, "/api/session/login", false},
		{"inference", fasthttp.MethodPost, "/v1/chat/completions", false},
		{"mcp endpoint", fasthttp.MethodPost, "/mcp/bifrost", false},
		{"missing id", fasthttp.MethodPut, "/api/governance/virtual-keys/", false},
		{"extra segment", fasthttp.MethodPut, "/api/governance/virtual-keys/vk-1/extra", false},
		{"traversal as id", fasthttp.MethodPut, "/api/governance/virtual-keys/..", false},
		{"traversal out of the route", fasthttp.MethodPut, "/api/providers/../config", false},
		{"doubled separator", fasthttp.MethodPost, "/api//providers", false},
		{"no leading slash", fasthttp.MethodPost, "api/providers", false},
		{"lowercase method", "post", "/api/providers", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := adminDispatchRouteAllowed(tc.method, tc.path); got != tc.want {
				t.Fatalf("adminDispatchRouteAllowed(%q, %q) = %v, want %v", tc.method, tc.path, got, tc.want)
			}
		})
	}
}

// Every allow-listed route must be one the router would register as written:
// a typo here is a tool that can never run, found at the first live call.
func TestAdminDispatchRoutesAreWellFormed(t *testing.T) {
	seen := map[adminRoute]bool{}
	for _, route := range adminDispatchRoutes {
		if seen[route] {
			t.Errorf("%s %s is listed twice", route.method, route.path)
		}
		seen[route] = true
		if !strings.HasPrefix(route.path, "/api/") {
			t.Errorf("%s %s is outside the admin API", route.method, route.path)
		}
		if route.method == fasthttp.MethodGet {
			t.Errorf("%s %s is a read; write tools read through the scoped readers", route.method, route.path)
		}
		r := router.New()
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Errorf("%s %s cannot be registered: %v", route.method, route.path, recovered)
				}
			}()
			r.Handle(route.method, route.path, func(*fasthttp.RequestCtx) {})
		}()
	}
}

func TestAdminDispatcherDispatchesToTheRoute(t *testing.T) {
	r := router.New()
	var gotID, gotBody, gotContentType string
	var gotLocalAdmin, gotBypassed bool
	r.PUT("/api/governance/virtual-keys/{vk_id}", func(ctx *fasthttp.RequestCtx) {
		gotID, _ = ctx.UserValue("vk_id").(string)
		gotBody = string(ctx.PostBody())
		gotContentType = string(ctx.Request.Header.ContentType())
		gotLocalAdmin, _ = ctx.UserValue(schemas.IsLocalAdminContextKey).(bool)
		gotBypassed, _ = ctx.UserValue(schemas.BifrostContextKeyAuthBypassed).(bool)
		SendJSON(ctx, map[string]any{"ok": true})
	})
	api := NewAdminDispatcher(func() fasthttp.RequestHandler { return r.Handler })

	status, response, err := api.Do(context.Background(), fasthttp.MethodPut, "/api/governance/virtual-keys/vk-1", []byte(`{"name":"renamed"}`))
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if status != fasthttp.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", status, response)
	}
	if !strings.Contains(string(response), `"ok":true`) {
		t.Fatalf("response = %s, want the route's body", response)
	}
	if gotID != "vk-1" || gotBody != `{"name":"renamed"}` || gotContentType != "application/json" {
		t.Fatalf("route saw id %q body %q content type %q", gotID, gotBody, gotContentType)
	}
	if !gotLocalAdmin {
		t.Error("a dispatched request must be a local admin, or admin-gated routes refuse it")
	}
	if !gotBypassed {
		t.Error("a dispatched request must carry BifrostContextKeyAuthBypassed: nobody authenticated as an admin for it")
	}
}

// A route's refusal is a status for the tool to report, not a dispatch error.
func TestAdminDispatcherReturnsTheRoutesRefusal(t *testing.T) {
	r := router.New()
	r.POST("/api/providers", func(ctx *fasthttp.RequestCtx) {
		SendError(ctx, fasthttp.StatusBadRequest, "concurrency must not exceed buffer size")
	})
	api := NewAdminDispatcher(func() fasthttp.RequestHandler { return r.Handler })

	status, response, err := api.Do(context.Background(), fasthttp.MethodPost, "/api/providers", []byte(`{}`))
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if status != fasthttp.StatusBadRequest || !strings.Contains(string(response), "concurrency must not exceed buffer size") {
		t.Fatalf("status %d body %s, want the route's 400 and message", status, response)
	}
}

func TestAdminDispatcherRefusesRoutesOffTheList(t *testing.T) {
	r := router.New()
	called := false
	reached := func(*fasthttp.RequestCtx) { called = true }
	r.DELETE("/api/governance/virtual-keys/{vk_id}", reached)
	r.GET("/api/governance/virtual-keys", reached)
	r.PUT("/api/config", reached)
	api := NewAdminDispatcher(func() fasthttp.RequestHandler { return r.Handler })

	for _, call := range []adminRoute{
		{fasthttp.MethodDelete, "/api/governance/virtual-keys/vk-1"},
		{fasthttp.MethodGet, "/api/governance/virtual-keys"},
		{fasthttp.MethodPut, "/api/config"},
		{fasthttp.MethodPut, "/api/providers/../config"},
	} {
		if _, _, err := api.Do(context.Background(), call.method, call.path, nil); err == nil {
			t.Errorf("%s %s was dispatched, want a refusal", call.method, call.path)
		}
	}
	if called {
		t.Fatal("a refused route must never reach a handler")
	}
}

// The allow-list and the router must agree on which route a path names. The
// router matches the raw path, so an encoded separator or traversal is one
// opaque id segment to both: it reaches the route it was checked against, never
// the one it would decode to.
func TestAdminDispatcherEncodedPathStaysOnTheMatchedRoute(t *testing.T) {
	r := router.New()
	var reached, provider string
	r.PUT("/api/providers/{provider}", func(ctx *fasthttp.RequestCtx) {
		reached = "provider"
		provider, _ = ctx.UserValue("provider").(string)
	})
	r.PUT("/api/config", func(*fasthttp.RequestCtx) { reached = "config" })
	r.PUT("/api/providers/{provider}/keys/{key_id}", func(*fasthttp.RequestCtx) { reached = "key" })
	api := NewAdminDispatcher(func() fasthttp.RequestHandler { return r.Handler })

	for _, path := range []string{
		"/api/providers/%2e%2e%2fconfig",
		"/api/providers/..%2fconfig",
		"/api/providers/a%2fkeys%2fb",
		"/api/providers/openai?next=/../../config",
	} {
		reached, provider = "", ""
		if _, _, err := api.Do(context.Background(), fasthttp.MethodPut, path, nil); err != nil {
			t.Fatalf("Do(%q) error = %v", path, err)
		}
		if reached != "provider" {
			t.Errorf("%q reached %q, want the provider route it was allow-listed against", path, reached)
		}
		if strings.Contains(provider, "/") {
			t.Errorf("%q gave the route a decoded id %q", path, provider)
		}
	}
}

// The handlers withhold three capabilities from a caller nobody authenticated:
// a native plugin path, a stdio MCP client, a private-network MCP target. A
// virtual key calling through the write tools is such a caller, so the gates
// must stay closed to a dispatched request.
func TestAdminDispatcherKeepsUnauthenticatedGatesClosed(t *testing.T) {
	r := router.New()
	var stdioRejected, privateRejected bool
	r.POST("/api/mcp/client", func(ctx *fasthttp.RequestCtx) {
		stdioRejected = rejectStdioMCPClientIfAuthBypassed(ctx, string(schemas.MCPConnectionTypeSTDIO))
	})
	r.PUT("/api/mcp/client/{id}", func(ctx *fasthttp.RequestCtx) {
		privateRejected = rejectPrivateMCPTargetIfAuthBypassed(ctx, string(schemas.MCPConnectionTypeHTTP), schemas.NewSecretVar("http://127.0.0.1:3001/mcp"))
	})
	api := NewAdminDispatcher(func() fasthttp.RequestHandler { return r.Handler })

	status, response, err := api.Do(context.Background(), fasthttp.MethodPost, "/api/mcp/client", []byte(`{}`))
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if !stdioRejected || status != fasthttp.StatusForbidden {
		t.Fatalf("a stdio MCP client must be refused for a dispatched request, got status %d body %s", status, response)
	}
	status, response, err = api.Do(context.Background(), fasthttp.MethodPut, "/api/mcp/client/c-1", []byte(`{}`))
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if !privateRejected || status != fasthttp.StatusForbidden {
		t.Fatalf("a loopback MCP target must be refused for a dispatched request, got status %d body %s", status, response)
	}
}

func TestAdminDispatcherUnavailable(t *testing.T) {
	var missing *AdminDispatcher
	if _, _, err := missing.Do(context.Background(), fasthttp.MethodPost, "/api/providers", nil); err == nil {
		t.Error("a nil dispatcher must report itself unavailable")
	}
	noRouter := NewAdminDispatcher(func() fasthttp.RequestHandler { return nil })
	if _, _, err := noRouter.Do(context.Background(), fasthttp.MethodPost, "/api/providers", nil); err == nil {
		t.Error("a dispatcher with no router yet must report itself unavailable")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	r := router.New()
	called := false
	r.POST("/api/providers", func(*fasthttp.RequestCtx) { called = true })
	api := NewAdminDispatcher(func() fasthttp.RequestHandler { return r.Handler })
	if _, _, err := api.Do(cancelled, fasthttp.MethodPost, "/api/providers", nil); err == nil || called {
		t.Errorf("a cancelled call must not dispatch, err %v called %v", err, called)
	}
}
