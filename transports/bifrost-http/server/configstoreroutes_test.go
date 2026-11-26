package server

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/handlers"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// configStoreRefusal is the body RequireConfigStoreMiddleware answers with.
const configStoreRefusal = "config store not available"

// routeParam matches a path parameter such as {id} or {filepath:*}.
var routeParam = regexp.MustCompile(`\{[^}]+\}`)

// namedPlugin stands in for a loaded plugin: route registration only looks plugins up by name.
type namedPlugin struct {
	schemas.LLMPlugin
	name string
}

// GetName returns the name the plugin was registered under.
func (p namedPlugin) GetName() string { return p.name }

// configStoreOpenRoute reports whether an /api route keeps working with the config store
// disabled: the dashboard shell and version, the vector stores list (read-only then, its
// settings coming from config.json alone), sign-in and session, the log stores, cache clearing
// and dev profiling. Every other /api route is configuration.
func configStoreOpenRoute(method, path string) bool {
	switch {
	case method == fasthttp.MethodGet && (path == "/api/config" || path == "/api/version" || path == "/api/config/vector-stores"):
		return true
	case strings.HasPrefix(path, "/api/logs/recalculate-cost"):
		return false // a background job kept in the config store
	}
	for _, prefix := range []string{"/api/session/", "/api/logs", "/api/mcp-logs", "/api/relay-logs", "/api/agents/history", "/api/cache/", "/api/dev/"} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// serveRoute sends one request through the router and reports whether the config store guard
// refused it. A handler that panics on the test's empty dependencies was reached, so it was not
// refused.
func serveRoute(r *router.Router, method, path string) (refused bool, status int) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod(method)
	ctx.Request.SetRequestURI(routeParam.ReplaceAllString(path, "x"))
	defer func() {
		if recover() != nil {
			refused, status = false, 0
		}
	}()
	r.Handler(ctx)
	status = ctx.Response.StatusCode()
	return status == fasthttp.StatusServiceUnavailable && strings.Contains(string(ctx.Response.Body()), configStoreRefusal), status
}

// TestFileOnlyModeRefusesConfigurationRoutes registers the API routes with the config store
// disabled and sends a request to every /api route. Each configuration route, reads included,
// answers 503 from RequireConfigStoreMiddleware; the open routes are not refused. Handlers that
// used to be skipped without a store must now be registered and refuse too.
func TestFileOnlyModeRefusesConfigurationRoutes(t *testing.T) {
	SetLogger(noopTestLogger{})
	handlers.SetLogger(noopTestLogger{})
	lib.SetLogger(noopTestLogger{})
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	t.Cleanup(cancel)

	config := &lib.Config{ClientConfig: &configstore.ClientConfig{}}
	if err := config.AttachLiveStore(liveStoreForTest(t)); err != nil {
		t.Fatal(err)
	}
	plugins := []schemas.BasePlugin{namedPlugin{name: governance.PluginName}}
	config.BasePlugins.Store(&plugins)
	s := &BifrostHTTPServer{Ctx: ctx, Config: config, Router: router.New()}
	if err := s.RegisterAPIRoutes(ctx, s); err != nil {
		t.Fatalf("RegisterAPIRoutes with the config store disabled: %v", err)
	}
	t.Cleanup(func() {
		if s.WarpHandler != nil {
			s.WarpHandler.Shutdown()
		}
		s.skillsServingHandler.Close()
	})

	refusedPaths := map[string]bool{}
	for method, paths := range s.Router.List() {
		for _, path := range paths {
			if !strings.HasPrefix(path, "/api/") {
				continue
			}
			refused, status := serveRoute(s.Router, method, path)
			if configStoreOpenRoute(method, path) {
				if refused {
					t.Errorf("%s %s is open but was refused for the config store", method, path)
				}
				continue
			}
			if !refused {
				t.Errorf("%s %s answered %d, want 503 %q: register it behind RequireConfigStoreMiddleware or add it to the open routes", method, path, status, configStoreRefusal)
			}
			refusedPaths[method+" "+path] = true
		}
	}

	// One route from each handler that used to be skipped without a store, and from each
	// handler that guards its own routes.
	for _, route := range []string{
		"GET /api/governance/virtual-keys",
		"GET /api/routing/rules",
		"GET /api/routing/model-routers",
		"GET /api/mcp/virtual-mcps",
		"GET /api/prompt-repo/prompts",
		"GET /api/skills",
		"GET /api/skills/serve/all/download.zip",
		"GET /api/sidekiq/jobs",
		"PUT /api/config",
		"GET /api/providers",
	} {
		if !refusedPaths[route] {
			t.Errorf("%s is not registered behind the config store guard", route)
		}
	}
}

// TestRequireConfigStoreMiddlewarePassesWithAStore checks that the guard adds nothing when the
// config store is enabled: the route's own handler is returned unchanged.
func TestRequireConfigStoreMiddlewarePassesWithAStore(t *testing.T) {
	called := false
	next := func(*fasthttp.RequestCtx) { called = true }
	guarded := handlers.RequireConfigStoreMiddleware(&updateStatusOnlyConfigStore{})(next)
	guarded(&fasthttp.RequestCtx{})
	if !called {
		t.Fatal("with a config store the guard must call the route's handler")
	}
}
