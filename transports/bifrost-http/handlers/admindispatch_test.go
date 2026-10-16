package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/fasthttp/router"
	"github.com/mark3labs/mcp-go/mcp"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/featureflags"
	"github.com/maximhq/bifrost/framework/mcptools"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
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

// Every dispatched request carries AuthBypassed, so the real handlers' genuine-auth
// gates refuse the changes that let an unauthenticated caller choose where the
// gateway sends data or credentials: a plugin endpoint or env./vault. reference,
// a webhook allowed to deliver to private networks, a provider base URL or
// allow_private_network, a provider key's dial target. Pinned at the dispatcher,
// below any tool's own pre-check, because the route is the authority: each is a
// 403 with the route's reason, and nothing is stored.
func TestAdminDispatcherRefusesUnauthenticatedDialTargetChanges(t *testing.T) {
	SetLogger(&mockLogger{})
	lib.SetLogger(&mockLogger{})
	ctx := context.Background()
	do := func(t *testing.T, r *router.Router, method, path, body string) (int, string) {
		t.Helper()
		status, response, err := NewAdminDispatcher(func() fasthttp.RequestHandler { return r.Handler }).Do(ctx, method, path, []byte(body))
		require.NoError(t, err)
		return status, gjson.GetBytes(response, "error.message").String()
	}

	t.Run("plugins", func(t *testing.T) {
		store := setupPricingOverrideHandlerStore(t)
		loader := &writeToolPluginsLoader{}
		r := router.New()
		NewPluginsHandler(loader, store).RegisterRoutes(r)

		status, message := do(t, r, fasthttp.MethodPost, "/api/plugins", `{"name":"otel","enabled":true,"config":{"url":"https://collector.example"}}`)
		require.Equal(t, fasthttp.StatusForbidden, status, message)
		require.Contains(t, message, "Setting a plugin's endpoints or env./vault. references (url) requires an authenticated admin session")
		_, err := store.GetPlugin(ctx, "otel")
		require.ErrorIs(t, err, configstore.ErrNotFound)

		status, message = do(t, r, fasthttp.MethodPost, "/api/plugins", `{"name":"otel","enabled":true,"config":{"plugin_span_filter":"llm"}}`)
		require.Equal(t, fasthttp.StatusCreated, status, message)
		// The plugin's typed check runs first: an invalid config is a 400, not the auth refusal.
		status, message = do(t, r, fasthttp.MethodPut, "/api/plugins/otel", `{"enabled":true,"config":{"url":"::"}}`)
		require.Equal(t, fasthttp.StatusBadRequest, status, message)
		require.Contains(t, message, "Invalid plugin configuration")
		status, message = do(t, r, fasthttp.MethodPut, "/api/plugins/otel", `{"enabled":true,"config":{"headers":{"Authorization":"env.GATEWAY_SECRET"}}}`)
		require.Equal(t, fasthttp.StatusForbidden, status, message)
		require.Contains(t, message, "(headers.Authorization) requires an authenticated admin session")
		stored, err := store.GetPlugin(ctx, "otel")
		require.NoError(t, err)
		require.NotContains(t, stored.Config.(map[string]any), "headers", "a refused update stores nothing")
		require.Equal(t, []string{"reload:otel"}, loader.calls, "a refused write never reaches the live plugin")
	})

	t.Run("webhooks", func(t *testing.T) {
		handler, config := newWebhookTestHandler(t)
		r := router.New()
		handler.RegisterRoutes(r)

		status, message := do(t, r, fasthttp.MethodPost, "/api/webhooks", `{"name":"internal","url":"https://example.com/hook","events":["async_job.completed"],"allow_private_network":true}`)
		require.Equal(t, fasthttp.StatusForbidden, status, message)
		require.Contains(t, message, "allows private-network delivery requires an authenticated admin session")
		endpoints, err := config.ConfigStore.GetWebhookEndpoints(ctx)
		require.NoError(t, err)
		require.Empty(t, endpoints, "a refused create stores nothing")

		status, message = do(t, r, fasthttp.MethodPost, "/api/webhooks", `{"name":"alerts","url":"https://example.com/hook","events":["async_job.completed"]}`)
		require.Equal(t, fasthttp.StatusCreated, status, message)
		endpoints, err = config.ConfigStore.GetWebhookEndpoints(ctx)
		require.NoError(t, err)
		require.Len(t, endpoints, 1)
		id := endpoints[0].ID
		status, message = do(t, r, fasthttp.MethodPut, "/api/webhooks/"+id, `{"name":"alerts","url":"https://example.com/hook","events":["async_job.completed"],"allow_private_network":true}`)
		require.Equal(t, fasthttp.StatusForbidden, status, message)
		require.Contains(t, message, "allows private-network delivery requires an authenticated admin session")
		after, err := config.ConfigStore.GetWebhookEndpointByID(ctx, id)
		require.NoError(t, err)
		require.False(t, after.AllowPrivateNetwork, "a refused update stores nothing")
	})

	t.Run("providers and provider keys", func(t *testing.T) {
		store := setupPricingOverrideHandlerStore(t)
		config := &lib.Config{ConfigStore: store, ClientConfig: &configstore.ClientConfig{}, Providers: map[schemas.ModelProvider]configstore.ProviderConfig{}}
		client, err := bifrost.Init(ctx, schemas.BifrostConfig{
			Account: lib.NewBaseAccount(config),
			Logger:  bifrost.NewDefaultLogger(schemas.LogLevelError),
		})
		require.NoError(t, err)
		t.Cleanup(client.Shutdown)
		config.SetBifrostClient(client)
		r := router.New()
		NewProviderHandler(&mockModelsManager{}, config, client).RegisterRoutes(r)

		for _, body := range []string{
			`{"provider":"openai","concurrency_and_buffer_size":{"concurrency":1,"buffer_size":10},"network_config":{"base_url":"https://93.184.216.34/v1"}}`,
			`{"provider":"openai","concurrency_and_buffer_size":{"concurrency":1,"buffer_size":10},"network_config":{"allow_private_network":true}}`,
		} {
			status, message := do(t, r, fasthttp.MethodPost, "/api/providers", body)
			require.Equal(t, fasthttp.StatusForbidden, status, message)
			require.Equal(t, providerDialTargetForbiddenMsg, message)
			_, err := config.GetProviderConfigRaw("openai")
			require.Error(t, err, "a refused create stores nothing")
		}

		status, message := do(t, r, fasthttp.MethodPost, "/api/providers", `{"provider":"openai","concurrency_and_buffer_size":{"concurrency":1,"buffer_size":10}}`)
		require.Equal(t, fasthttp.StatusOK, status, message)
		status, message = do(t, r, fasthttp.MethodPut, "/api/providers/openai", `{"concurrency_and_buffer_size":{"concurrency":1,"buffer_size":10},"network_config":{"base_url":"https://93.184.216.34/v1"}}`)
		require.Equal(t, fasthttp.StatusForbidden, status, message)
		require.Equal(t, providerDialTargetForbiddenMsg, message)
		stored, err := config.GetProviderConfigRaw("openai")
		require.NoError(t, err)
		require.True(t, stored.NetworkConfig == nil || stored.NetworkConfig.BaseURL == "", "a refused update stores nothing")

		status, message = do(t, r, fasthttp.MethodPost, "/api/providers", `{"provider":"ollama","concurrency_and_buffer_size":{"concurrency":1,"buffer_size":10}}`)
		require.Equal(t, fasthttp.StatusOK, status, message)
		status, message = do(t, r, fasthttp.MethodPost, "/api/providers/ollama/keys", `{"name":"local","value":"","weight":1,"ollama_key_config":{"url":"http://127.0.0.1:11434"}}`)
		require.Equal(t, fasthttp.StatusForbidden, status, message)
		require.Contains(t, message, "Changing where a provider key dials (ollama_key_config.url) requires an authenticated admin session")
		keys, err := store.GetProviderKeys(ctx, schemas.Ollama)
		require.NoError(t, err)
		require.Empty(t, keys, "a refused create stores nothing")
	})
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

// runWriteTool calls one of Bifrost's MCP write tools the way the MCP server
// does - tools/call over JSON-RPC - with its writes dispatched to the admin
// handlers registered on r. It is the whole path a write takes: tool, dispatcher,
// allow-list, router, handler. It returns the tool's text and whether the tool
// reported an error.
func runWriteTool(t *testing.T, ctx context.Context, r *router.Router, deps mcptools.Deps, tool string, args map[string]any) (string, bool) {
	t.Helper()
	deps.Admin = NewAdminDispatcher(func() fasthttp.RequestHandler { return r.Handler })
	server := mcptools.NewServer(mcptools.StaticDeps(&deps))
	raw, err := json.Marshal(map[string]any{
		"jsonrpc": mcp.JSONRPC_VERSION,
		"id":      1,
		"method":  "tools/call",
		"params":  map[string]any{"name": tool, "arguments": args},
	})
	require.NoError(t, err)
	encoded, err := json.Marshal(server.HandleMessage(ctx, raw))
	require.NoError(t, err)
	var envelope struct {
		Result mcp.CallToolResult `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(encoded, &envelope))
	require.Nil(t, envelope.Error, "tools/call %s failed at the JSON-RPC layer", tool)
	require.Len(t, envelope.Result.Content, 1)
	text, ok := envelope.Result.Content[0].(mcp.TextContent)
	require.True(t, ok)
	return text.Text, envelope.Result.IsError
}

// adminCaller is a request the transport let run write tools: the dashboard
// admin, or anyone while dashboard auth is off.
func adminCaller() context.Context {
	return mcptools.WithWriteAccess(context.Background())
}

func TestWriteToolUpdateFeatureFlag(t *testing.T) {
	SetLogger(&mockLogger{})
	const flagID = "mcp-write-tool-flag"
	if err := featureflags.Register(featureflags.FlagDef{ID: flagID, DisplayName: "Write tool flag"}); err != nil {
		require.True(t, errors.Is(err, featureflags.ErrFlagAlreadyRegistered), "register: %v", err)
	}
	store, err := featureflags.New(featureflags.Config{})
	require.NoError(t, err)
	r := router.New()
	NewFeatureFlagsHandler(store, nil).RegisterRoutes(r)

	text, isError := runWriteTool(t, adminCaller(), r, mcptools.Deps{}, "update_feature_flag", map[string]any{"id": flagID, "enabled": true})
	require.False(t, isError, text)
	require.True(t, store.IsEnabled(flagID), "the flag must be on in the store the gateway reads")
	var out map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &out))
	require.Equal(t, flagID, out["id"])
	require.Equal(t, true, out["enabled"])

	// The handler's rules reach the caller in the handler's words.
	text, isError = runWriteTool(t, adminCaller(), r, mcptools.Deps{}, "update_feature_flag", map[string]any{"id": "no-such-flag", "enabled": true})
	require.True(t, isError)
	require.Contains(t, text, "Feature flag is not registered")

	store.ApplyFile(flagID, true)
	text, isError = runWriteTool(t, adminCaller(), r, mcptools.Deps{}, "update_feature_flag", map[string]any{"id": flagID, "enabled": false})
	require.True(t, isError)
	require.Contains(t, text, "locked by config.json")
	require.True(t, store.IsEnabled(flagID), "a refused change must leave the flag as it was")

	// Without write access the tool never reaches the dispatcher.
	text, isError = runWriteTool(t, context.Background(), r, mcptools.Deps{}, "update_feature_flag", map[string]any{"id": flagID, "enabled": false})
	require.True(t, isError)
	require.Contains(t, text, "has no permission to")
}

// The routing tools hold no routing rules of their own: weights, duplicate
// targets, fallbacks, CEL and the scope's owner are the routing handler's
// checks, reached through the dispatcher, and a rule the handler refuses is
// never stored.
func TestWriteToolRoutingRules(t *testing.T) {
	SetLogger(&mockLogger{})
	store := setupPricingOverrideHandlerStore(t)
	manager := &reloadRecordingRoutingManager{}
	r := router.New()
	(&RoutingHandler{configStore: store, routingManager: manager}).RegisterRoutes(r)
	call := func(tool string, args map[string]any) (string, bool) {
		return runWriteTool(t, adminCaller(), r, mcptools.Deps{}, tool, args)
	}
	target := []any{map[string]any{"provider": "openai", "model": "gpt-4o", "weight": 1.0}}

	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"weights", map[string]any{"name": "r", "targets": []any{
			map[string]any{"provider": "openai", "weight": 0.5},
			map[string]any{"provider": "anthropic", "weight": 0.3},
		}}, "sum to 1"},
		{"duplicate target", map[string]any{"name": "r", "targets": []any{
			map[string]any{"provider": "OpenAI", "model": "gpt-4o", "weight": 0.5},
			map[string]any{"provider": "openai", "model": "GPT-4o", "weight": 0.5},
		}}, "duplicate"},
		{"key without provider", map[string]any{"name": "r", "targets": []any{map[string]any{"key_id": "k-1", "weight": 1.0}}}, "provider"},
		{"fallback", map[string]any{"name": "r", "targets": target, "fallbacks": []any{"not-a-provider"}}, "fallback"},
		{"cel", map[string]any{"name": "r", "targets": target, "cel_expression": "model !! x"}, "invalid CEL expression"},
		{"scope owner", map[string]any{"name": "r", "targets": target, "scope": "team", "scope_id": "team-typo"}, "team-typo"},
		{"scope id on global", map[string]any{"name": "r", "targets": target, "scope_id": "team-1"}, "scope_id"},
		{"wrong type", map[string]any{"name": "r", "targets": target, "cel_expression": 42.0}, "wrong type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text, isError := call("create_routing_rule", tc.args)
			require.True(t, isError, text)
			require.Contains(t, text, tc.want)
		})
	}
	rules, err := store.GetRoutingRules(context.Background())
	require.NoError(t, err)
	require.Empty(t, rules, "nothing is stored when the handler refuses")
	require.Empty(t, manager.reloaded)

	text, isError := call("create_routing_rule", map[string]any{
		"name": "prod-split", "targets": target, "cel_expression": `model == "gpt-4o"`, "fallbacks": []any{"anthropic/claude-sonnet-4", "azure/"},
	})
	require.False(t, isError, text)
	var created map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &created))
	id, _ := created["id"].(string)
	require.NotEmpty(t, id)
	require.Equal(t, "global", created["scope"])
	require.Equal(t, []string{id}, manager.reloaded, "a created rule is live, not only stored")

	// An absent cel_expression keeps the stored one; an empty one clears it.
	text, isError = call("update_routing_rule", map[string]any{"rule_id": id, "name": "renamed"})
	require.False(t, isError, text)
	stored, err := store.GetRoutingRule(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, "renamed", stored.Name)
	require.Equal(t, `model == "gpt-4o"`, stored.CelExpression)

	text, isError = call("update_routing_rule", map[string]any{"rule_id": id, "cel_expression": ""})
	require.False(t, isError, text)
	stored, err = store.GetRoutingRule(context.Background(), id)
	require.NoError(t, err)
	require.Empty(t, stored.CelExpression)
	require.Equal(t, []string{id, id, id}, manager.reloaded)

	text, isError = call("update_routing_rule", map[string]any{"rule_id": "no-such-rule", "name": "x"})
	require.True(t, isError)
	require.Contains(t, strings.ToLower(text), "not found")
}

// The webhook tools write through the webhook handler: its validation, its
// one-time secret, and its full-replace update, under which an edit made here
// must not clear the headers and delivery tuning this tool never names.
func TestWriteToolWebhooks(t *testing.T) {
	SetLogger(&mockLogger{})
	handler, config := newWebhookTestHandler(t)
	r := router.New()
	handler.RegisterRoutes(r)
	deps := mcptools.Deps{Governance: config.ConfigStore}
	call := func(tool string, args map[string]any) (string, bool) {
		return runWriteTool(t, adminCaller(), r, deps, tool, args)
	}

	text, isError := call("create_webhook", map[string]any{"name": "alerts", "url": "https://example.com/hook", "events": []any{"not.an.event"}})
	require.True(t, isError, text)
	text, isError = call("create_webhook", map[string]any{"name": "alerts", "url": "ftp://example.com/hook", "events": []any{"async_job.completed"}})
	require.True(t, isError, text)

	// Private-network delivery needs a genuine admin session, which a tool call
	// never is: refused whether the tool turns the argument away or the route
	// does (its gate is pinned in TestAdminDispatcherRefusesUnauthenticatedDialTargetChanges).
	text, isError = call("create_webhook", map[string]any{"name": "internal", "url": "https://example.com/hook", "events": []any{"async_job.completed"}, "allow_private_network": true})
	require.True(t, isError, text)
	endpoints, err := config.ConfigStore.GetWebhookEndpoints(context.Background())
	require.NoError(t, err)
	require.Empty(t, endpoints, "a refused create stores nothing")

	text, isError = call("create_webhook", map[string]any{"name": "alerts", "url": "https://example.com/hook", "events": []any{"async_job.completed"}})
	require.False(t, isError, text)
	var created map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &created))
	id, _ := created["id"].(string)
	require.NotEmpty(t, id)
	secret, _ := created["secret"].(string)
	require.NotEmpty(t, secret, "create is the one response that carries the signing secret")
	_, live := config.WebhookEndpointByID(id)
	require.True(t, live, "a created endpoint is live, not only stored")

	text, isError = call("create_webhook", map[string]any{"name": "alerts", "url": "https://example.com/other", "events": []any{"async_job.completed"}})
	require.True(t, isError)
	require.Contains(t, text, "already exists")

	// Headers and tuning set elsewhere, as the dashboard would.
	stored, err := config.ConfigStore.GetWebhookEndpointByID(context.Background(), id)
	require.NoError(t, err)
	stored.Headers = map[string]schemas.SecretVar{"X-Token": *schemas.NewSecretVar("header-secret-value")}
	stored.MaxRetries = 7
	require.NoError(t, config.ConfigStore.UpdateWebhookEndpoint(context.Background(), stored))
	require.NoError(t, configWebhookManager{config}.ReloadWebhookEndpoint(context.Background(), id))

	text, isError = call("update_webhook", map[string]any{"webhook_id": id, "disabled": true})
	require.False(t, isError, text)
	require.NotContains(t, text, secret)
	require.NotContains(t, text, "header-secret-value")
	after, err := config.ConfigStore.GetWebhookEndpointByID(context.Background(), id)
	require.NoError(t, err)
	require.True(t, after.Disabled)
	require.Equal(t, "alerts", after.Name)
	require.Equal(t, 7, after.MaxRetries, "delivery tuning survives an edit to something else")
	header := after.Headers["X-Token"]
	require.Equal(t, "header-secret-value", header.GetValue(), "the header keeps its stored value, not the placeholder")

	text, isError = call("update_webhook", map[string]any{"webhook_id": id, "url": "not a url"})
	require.True(t, isError, text)
	text, isError = call("update_webhook", map[string]any{"webhook_id": id, "allow_private_network": true})
	require.True(t, isError, text)
	after, err = config.ConfigStore.GetWebhookEndpointByID(context.Background(), id)
	require.NoError(t, err)
	require.False(t, after.AllowPrivateNetwork, "a refused update stores nothing")
}

// The pricing tools write through the governance handler, so an override the
// handler would refuse - an unknown scope, a scope missing its id, a wildcard
// that is not one - is refused here too, and never stored.
func TestWriteToolPricingOverrides(t *testing.T) {
	SetLogger(&mockLogger{})
	store := setupPricingOverrideHandlerStore(t)
	r := router.New()
	(&GovernanceHandler{configStore: store, governanceManager: pricingOverrideTestGovernanceManager{}}).RegisterRoutes(r)
	call := func(tool string, args map[string]any) (string, bool) {
		return runWriteTool(t, adminCaller(), r, mcptools.Deps{Governance: store}, tool, args)
	}
	patch := map[string]any{"input_cost_per_token": 0.000001}

	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"unknown scope", map[string]any{"name": "o", "scope_kind": "galaxy", "match_type": "exact", "pattern": "gpt-4o", "patch": patch, "request_types": []any{"chat_completion"}}},
		{"scope without its id", map[string]any{"name": "o", "scope_kind": "virtual_key", "match_type": "exact", "pattern": "gpt-4o", "patch": patch, "request_types": []any{"chat_completion"}}},
		{"unknown match type", map[string]any{"name": "o", "scope_kind": "global", "match_type": "regex", "pattern": "gpt-4o", "patch": patch, "request_types": []any{"chat_completion"}}},
		{"id the scope does not take", map[string]any{"name": "o", "scope_kind": "global", "virtual_key_id": "vk-1", "match_type": "exact", "pattern": "gpt-4o", "patch": patch, "request_types": []any{"chat_completion"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text, isError := call("create_pricing_override", tc.args)
			require.True(t, isError, text)
		})
	}
	stored, _, err := store.GetPricingOverridesPaginated(context.Background(), configstore.PricingOverridesQueryParams{Limit: 10})
	require.NoError(t, err)
	require.Empty(t, stored, "nothing is stored when the handler refuses")

	text, isError := call("create_pricing_override", map[string]any{"name": "cheap", "scope_kind": "global", "match_type": "exact", "pattern": "gpt-4o", "patch": patch, "request_types": []any{"chat_completion"}})
	require.False(t, isError, text)
	var created map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &created))
	id, _ := created["id"].(string)
	require.NotEmpty(t, id)
	require.Equal(t, map[string]any{"input_cost_per_token": 0.000001}, created["patch"])

	text, isError = call("update_pricing_override", map[string]any{"override_id": id, "name": "cheaper"})
	require.False(t, isError, text)
	row, err := store.GetPricingOverrideByID(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, "cheaper", row.Name)
	require.Equal(t, "gpt-4o", row.Pattern, "what the update did not name is kept")
	require.Contains(t, row.PricingPatchJSON, "input_cost_per_token")

	text, isError = call("update_pricing_override", map[string]any{"override_id": id, "match_type": "regex"})
	require.True(t, isError, text)
	text, isError = call("update_pricing_override", map[string]any{"override_id": "no-such-override", "name": "x"})
	require.True(t, isError)
	require.Contains(t, strings.ToLower(text), "not found")
}

// writeToolPluginsLoader records what the plugin handler loads and stops, and
// refuses a config whose url is "::" the way a plugin's typed config would.
type writeToolPluginsLoader struct {
	noopPluginsLoader
	calls []string
}

func (l *writeToolPluginsLoader) ReloadPlugin(_ context.Context, name string, path *string, _ any, _ *schemas.PluginPlacement, _ *int) error {
	if path != nil {
		l.calls = append(l.calls, "load-from-path:"+name)
		return nil
	}
	l.calls = append(l.calls, "reload:"+name)
	return nil
}

func (l *writeToolPluginsLoader) RemovePlugin(_ context.Context, name string) error {
	l.calls = append(l.calls, "remove:"+name)
	return nil
}

func (l *writeToolPluginsLoader) NormalizePluginConfig(_ string, config map[string]any) (map[string]any, error) {
	if config["url"] == "::" {
		return nil, errors.New("url: invalid")
	}
	return config, nil
}

// The plugin tools write through the plugin handler: its typed-config check,
// its merge of the stored config, and its load or stop of the live plugin. No
// plugin path is ever sent, so nothing is loaded from one.
//
// A dispatched request carries AuthBypassed (see AdminDispatcher), so the
// handler's genuine-auth gate refuses any create or update that sets or moves a
// plugin endpoint (url, endpoint, collector_url, ...) or names an env./vault.
// reference: such config is provisioned through config.json or the dashboard,
// never through these tools. Everything else about a plugin stays writable.
func TestWriteToolPlugins(t *testing.T) {
	SetLogger(&mockLogger{})
	store := setupPricingOverrideHandlerStore(t)
	loader := &writeToolPluginsLoader{}
	r := router.New()
	NewPluginsHandler(loader, store).RegisterRoutes(r)
	call := func(tool string, args map[string]any) (string, bool) {
		return runWriteTool(t, adminCaller(), r, mcptools.Deps{Governance: store}, tool, args)
	}
	// The tool may refuse first, in its own words, or relay the route's 403; both name the rule.
	const genuineAuthRefusal = "authenticated admin session"

	for _, tc := range []struct {
		name    string
		config  map[string]any
		changed string
	}{
		{"an endpoint", map[string]any{"url": "https://collector.example", "api_key": "sk-x"}, "(url)"},
		{"a nested endpoint", map[string]any{"profiles": []any{map[string]any{"collector_url": "https://collector.example"}}}, "(profiles[0].collector_url)"},
		{"an env. reference", map[string]any{"headers": map[string]any{"Authorization": "env.GATEWAY_SECRET"}}, "(headers.Authorization)"},
	} {
		t.Run("create refuses "+tc.name, func(t *testing.T) {
			text, isError := call("create_plugin", map[string]any{"name": "otel-refused", "config": tc.config})
			require.True(t, isError, text)
			require.Contains(t, text, genuineAuthRefusal)
			require.Contains(t, text, tc.changed, "the refusal names the field that needs genuine auth")
			_, err := store.GetPlugin(context.Background(), "otel-refused")
			require.ErrorIs(t, err, configstore.ErrNotFound, "a refused create stores nothing")
		})
	}

	text, isError := call("create_plugin", map[string]any{"name": "otel", "config": map[string]any{"plugin_span_filter": "llm", "api_key": "sk-stored"}})
	require.False(t, isError, text)
	require.NotContains(t, text, "sk-stored", "a result never carries a credential")
	text, isError = call("create_plugin", map[string]any{"name": "otel"})
	require.True(t, isError)
	require.Contains(t, text, "already exists")

	// An endpoint set elsewhere (config.json or the dashboard), which the tools
	// may keep but not move.
	stored, err := store.GetPlugin(context.Background(), "otel")
	require.NoError(t, err)
	seeded := stored.Config.(map[string]any)
	seeded["url"] = "https://old"
	stored.Config = seeded
	require.NoError(t, store.UpdatePlugin(context.Background(), stored))

	text, isError = call("update_plugin", map[string]any{"name": "otel", "config": map[string]any{"plugin_span_filter": "all", "api_key": schemas.RedactedAttrValue}})
	require.False(t, isError, text)
	stored, err = store.GetPlugin(context.Background(), "otel")
	require.NoError(t, err)
	config := stored.Config.(map[string]any)
	require.Equal(t, "https://old", config["url"], "an endpoint the caller did not send is kept")
	require.Equal(t, "all", config["plugin_span_filter"])
	require.Equal(t, "sk-stored", config["api_key"], "the placeholder is swapped back for the stored credential")
	require.True(t, stored.Enabled, "enabled is kept when the caller did not name it")

	text, isError = call("update_plugin", map[string]any{"name": "otel", "config": map[string]any{"url": "https://old"}})
	require.False(t, isError, "resending the stored endpoint unchanged is not a change: %s", text)
	text, isError = call("update_plugin", map[string]any{"name": "otel", "config": map[string]any{"url": "https://attacker.example"}})
	require.True(t, isError, text)
	require.Contains(t, text, genuineAuthRefusal)
	require.Contains(t, text, "(url)")
	stored, err = store.GetPlugin(context.Background(), "otel")
	require.NoError(t, err)
	require.Equal(t, "https://old", stored.Config.(map[string]any)["url"], "a refused endpoint move is not stored")

	text, isError = call("update_plugin", map[string]any{"name": "otel", "enabled": false})
	require.False(t, isError, text)
	text, isError = call("update_plugin", map[string]any{"name": "otel", "placement": "sideways"})
	require.True(t, isError, text)

	text, isError = call("create_plugin", map[string]any{"name": "evil", "path": "/tmp/evil.so"})
	require.True(t, isError)
	require.Contains(t, text, "does not take path")
	_, err = store.GetPlugin(context.Background(), "evil")
	require.ErrorIs(t, err, configstore.ErrNotFound)

	require.Equal(t, []string{"reload:otel", "reload:otel", "reload:otel", "remove:otel"}, loader.calls, "a refused write never reaches the live plugin")
}

// The provider tools write through the provider handler. Its update routes
// replace what they are sent, so an edit made here must leave the rest of the
// provider and of the key as they were - the base URL, the key's secret - and
// its checks on concurrency and on a key's content are the ones that apply.
func TestWriteToolProviders(t *testing.T) {
	SetLogger(&mockLogger{})
	lib.SetLogger(&mockLogger{})
	store := setupPricingOverrideHandlerStore(t)
	config := &lib.Config{ConfigStore: store, ClientConfig: &configstore.ClientConfig{}, Providers: map[schemas.ModelProvider]configstore.ProviderConfig{}}
	// A live gateway over the same config: a provider write reloads it there.
	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account: lib.NewBaseAccount(config),
		Logger:  bifrost.NewDefaultLogger(schemas.LogLevelError),
	})
	require.NoError(t, err)
	t.Cleanup(client.Shutdown)
	config.SetBifrostClient(client)
	manager := &mockModelsManager{}
	r := router.New()
	NewProviderHandler(manager, config, client).RegisterRoutes(r)
	call := func(tool string, args map[string]any) (string, bool) {
		return runWriteTool(t, adminCaller(), r, mcptools.Deps{Governance: store}, tool, args)
	}
	ctx := context.Background()

	text, isError := call("add_provider", map[string]any{"provider": "openai"})
	require.False(t, isError, text)
	text, isError = call("add_provider", map[string]any{"provider": "openai"})
	require.True(t, isError)
	require.Contains(t, text, "already exists")

	// A base URL set elsewhere, as the dashboard would.
	stored, err := config.GetProviderConfigRaw("openai")
	require.NoError(t, err)
	stored.NetworkConfig = &schemas.NetworkConfig{BaseURL: "https://93.184.216.34/v1", DefaultRequestTimeoutInSeconds: 45}
	require.NoError(t, config.UpdateProviderConfig(ctx, "openai", *stored))

	text, isError = call("update_provider", map[string]any{"provider": "openai", "concurrency": 8, "buffer_size": 16})
	require.False(t, isError, text)
	after, err := config.GetProviderConfigRaw("openai")
	require.NoError(t, err)
	require.Equal(t, schemas.ConcurrencyAndBufferSize{Concurrency: 8, BufferSize: 16}, *after.ConcurrencyAndBufferSize)
	require.Equal(t, "https://93.184.216.34/v1", after.NetworkConfig.BaseURL, "the network config survives an edit to something else")

	text, isError = call("update_provider", map[string]any{"provider": "openai", "concurrency": 32})
	require.True(t, isError)
	require.Contains(t, text, "less than or equal to buffer size")
	text, isError = call("update_provider", map[string]any{"provider": "openai", "buffer_size": 4})
	require.True(t, isError, "the pair is checked with the stored half: %s", text)
	after, err = config.GetProviderConfigRaw("openai")
	require.NoError(t, err)
	require.Equal(t, 8, after.ConcurrencyAndBufferSize.Concurrency, "a refused update writes nothing")

	text, isError = call("update_provider", map[string]any{"provider": "opnai", "concurrency": 2})
	require.True(t, isError)
	require.Contains(t, text, `no provider named "opnai"`)
	_, err = config.GetProviderConfigRaw("opnai")
	require.Error(t, err, "a mistyped name must not create a provider")

	text, isError = call("create_provider_key", map[string]any{"provider": "openai", "name": "prod", "value": "sk-stored-secret"})
	require.False(t, isError, text)
	require.NotContains(t, text, "sk-stored-secret")
	var created struct {
		Key struct {
			ID string `json:"id"`
		} `json:"key"`
	}
	require.NoError(t, json.Unmarshal([]byte(text), &created))
	require.NotEmpty(t, created.Key.ID)

	text, isError = call("create_provider_key", map[string]any{"provider": "openai", "name": "prod", "value": "sk-other"})
	require.True(t, isError)
	require.Contains(t, text, "unique")
	text, isError = call("create_provider_key", map[string]any{"provider": "anthropic", "name": "k", "value": "sk-x"})
	require.True(t, isError, "a key needs its provider to exist: %s", text)

	text, isError = call("update_provider_key", map[string]any{"provider": "openai", "key_id": created.Key.ID, "enabled": false, "weight": 0.25})
	require.False(t, isError, text)
	require.NotContains(t, text, "sk-stored-secret")
	key, err := config.GetProviderKeyRaw("openai", created.Key.ID)
	require.NoError(t, err)
	require.Equal(t, "sk-stored-secret", key.Value.GetValue(), "the secret the caller did not change is kept, not replaced by its placeholder")
	require.Equal(t, "prod", key.Name)
	require.Equal(t, 0.25, key.Weight)
	require.False(t, *key.Enabled)

	text, isError = call("update_provider_key", map[string]any{"provider": "openai", "key_id": created.Key.ID, "value": "sk-rotated"})
	require.False(t, isError, text)
	key, err = config.GetProviderKeyRaw("openai", created.Key.ID)
	require.NoError(t, err)
	require.Equal(t, "sk-rotated", key.Value.GetValue())

	text, isError = call("refresh_provider_models", map[string]any{"provider": "openai"})
	require.False(t, isError, text)
	text, isError = call("refresh_provider_models", map[string]any{"provider": "openai", "key_id": created.Key.ID})
	require.False(t, isError, text)
	text, isError = call("refresh_provider_models", map[string]any{"provider": "openai", "key_id": "no-such-key"})
	require.True(t, isError, text)
	require.Equal(t, []schemas.ModelProvider{"openai"}, manager.refreshProviderCalls)
	require.Len(t, manager.refreshKeyCalls, 1)
}

// writeToolMCPManager is the live MCP runtime as the handler sees it: it keeps
// the in-memory client list in step, which is what the update route reads.
type writeToolMCPManager struct {
	fakeMCPManagerVerifyOnly
	config *lib.Config
	calls  []string
}

func (m *writeToolMCPManager) AddMCPClient(_ context.Context, client *schemas.MCPClientConfig) error {
	m.calls = append(m.calls, "add:"+client.Name)
	m.config.MCPConfig.ClientConfigs = append(m.config.MCPConfig.ClientConfigs, client)
	return nil
}

func (m *writeToolMCPManager) UpdateMCPClient(_ context.Context, id string, updated *schemas.MCPClientConfig) error {
	m.calls = append(m.calls, fmt.Sprintf("update:%s disabled=%v tools=%v", updated.Name, updated.Disabled, []string(updated.ToolsToExecute)))
	for i, client := range m.config.MCPConfig.ClientConfigs {
		if client.ID == id {
			m.config.MCPConfig.ClientConfigs[i] = updated
		}
	}
	return nil
}

func (m *writeToolMCPManager) ReconnectMCPClient(_ context.Context, id string) error {
	m.calls = append(m.calls, "reconnect:"+id)
	return nil
}

func (m *writeToolMCPManager) DisableMCPClient(_ context.Context, id string) error {
	m.calls = append(m.calls, "disable:"+id)
	return nil
}

// The MCP client tools write through the MCP handler, which is where a client
// is dialed and where the gates on what may be dialed live. A caller of these
// tools authenticated as no admin, so the gates are closed to it: no stdio
// client, and no client pointed at a loopback or private address.
func TestWriteToolMCPClients(t *testing.T) {
	SetLogger(&mockLogger{})
	lib.SetLogger(&mockLogger{})
	store := setupPricingOverrideHandlerStore(t)
	config := &lib.Config{ConfigStore: store, ClientConfig: &configstore.ClientConfig{}, MCPConfig: &schemas.MCPConfig{}}
	manager := &writeToolMCPManager{config: config}
	r := router.New()
	(&MCPHandler{store: config, mcpManager: manager}).RegisterRoutes(r)
	call := func(tool string, args map[string]any) (string, bool) {
		return runWriteTool(t, adminCaller(), r, mcptools.Deps{Governance: store}, tool, args)
	}

	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"stdio", map[string]any{"name": "shell", "connection_type": "stdio", "connection_string": "npx foo"}, "connection_type"},
		{"loopback target", map[string]any{"name": "local", "connection_type": "http", "connection_string": "http://127.0.0.1:3001/mcp"}, ""},
		{"private target", map[string]any{"name": "internal", "connection_type": "sse", "connection_string": "http://10.0.0.5/mcp"}, ""},
		{"reserved name", map[string]any{"name": "bifrostmcp", "connection_type": "http", "connection_string": "https://93.184.216.34/mcp"}, "reserved"},
		{"name with a hyphen", map[string]any{"name": "git-hub", "connection_type": "http", "connection_string": "https://93.184.216.34/mcp"}, "name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text, isError := call("add_mcp_client", tc.args)
			require.True(t, isError, text)
			require.Contains(t, text, tc.want)
		})
	}
	require.Empty(t, manager.calls, "a refused client is never dialed")
	stored, _, err := store.GetMCPClientsPaginated(context.Background(), configstore.MCPClientsQueryParams{Limit: 10})
	require.NoError(t, err)
	require.Empty(t, stored, "a refused client is never stored")

	text, isError := call("add_mcp_client", map[string]any{"name": "github", "connection_type": "http", "connection_string": "https://93.184.216.34/mcp"})
	require.False(t, isError, text)
	require.NotContains(t, text, "93.184.216.34", "the connection string is stored and never returned")
	var created map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &created))
	id, _ := created["client_id"].(string)
	require.NotEmpty(t, id)
	row, err := store.GetMCPClientByID(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, schemas.WhiteList{"*"}, row.ToolsToExecute)

	text, isError = call("update_mcp_client", map[string]any{"client_id": id, "tools_to_execute": []any{"search"}})
	require.False(t, isError, text)
	row, err = store.GetMCPClientByID(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, schemas.WhiteList{"search"}, row.ToolsToExecute)
	require.Equal(t, "github", row.Name, "what the update did not name is kept")

	text, isError = call("update_mcp_client", map[string]any{"client_id": id, "tools_to_execute": []any{}})
	require.False(t, isError, text)
	row, err = store.GetMCPClientByID(context.Background(), id)
	require.NoError(t, err)
	require.Empty(t, row.ToolsToExecute, "[] means no tools, not every tool")

	text, isError = call("reconnect_mcp_client", map[string]any{"client_id": id})
	require.False(t, isError, text)
	text, isError = call("update_mcp_client", map[string]any{"client_id": "no-such-client", "disabled": true})
	require.True(t, isError)
	require.Contains(t, text, "not found")

	require.Equal(t, "add:github", manager.calls[0])
	require.Contains(t, manager.calls, "reconnect:"+id)
}

// writeToolVMCPGovernance records the live-state calls the virtual MCP handler
// makes after a write.
type writeToolVMCPGovernance struct {
	GovernanceManager
	calls []string
}

func (g *writeToolVMCPGovernance) ReloadVirtualMCP(_ context.Context, id uint) (*configstoreTables.TableVirtualMCP, error) {
	g.calls = append(g.calls, fmt.Sprintf("reload:%d", id))
	return nil, nil
}

func (g *writeToolVMCPGovernance) AttachVirtualMCPToVirtualKeyInMemory(_ context.Context, vkID string, id uint) error {
	g.calls = append(g.calls, fmt.Sprintf("attach:%s:%d", vkID, id))
	return nil
}

func (g *writeToolVMCPGovernance) DetachVirtualMCPFromVirtualKeyInMemory(_ context.Context, vkID string, id uint) error {
	g.calls = append(g.calls, fmt.Sprintf("detach:%s:%d", vkID, id))
	return nil
}

// The virtual MCP tools write through the virtual MCP handler: a tool spec
// naming a client that does not exist is refused there, an attach to a key
// that does not exist is refused there, and the live assignment the gateway
// enforces is updated there.
func TestWriteToolVirtualMCPs(t *testing.T) {
	SetLogger(&mockLogger{})
	store := setupPricingOverrideHandlerStore(t)
	ctx := context.Background()
	config := &lib.Config{ConfigStore: store}
	governance := &writeToolVMCPGovernance{}
	r := router.New()
	NewVirtualMCPHandler(config, nil, governance, &mockLogger{}).RegisterRoutes(r)
	call := func(tool string, args map[string]any) (string, bool) {
		return runWriteTool(t, adminCaller(), r, mcptools.Deps{Governance: store}, tool, args)
	}
	require.NoError(t, store.CreateVirtualKey(ctx, &configstoreTables.TableVirtualKey{ID: "vk-1", Name: "key", Value: *schemas.NewSecretVar("sk-bf-write-tool-vmcp")}))

	text, isError := call("create_virtual_mcp", map[string]any{"name": "ops", "tools": []any{map[string]any{"mcp_client_id": "no-such-client", "tool_names": []any{"*"}}}})
	require.True(t, isError, text)
	require.Contains(t, text, "invalid MCP client IDs")

	text, isError = call("create_virtual_mcp", map[string]any{"name": "ops", "description": "on-call tools"})
	require.False(t, isError, text)
	var created map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &created))
	id := created["id"].(float64)
	require.NotZero(t, id)
	require.NotEmpty(t, created["endpoint_slug"])

	text, isError = call("create_virtual_mcp", map[string]any{"name": "ops"})
	require.True(t, isError, "a second definition of the same name is refused: %s", text)

	text, isError = call("update_virtual_mcp", map[string]any{"virtual_mcp_id": id, "enabled": false})
	require.False(t, isError, text)
	stored, err := store.GetVirtualMCPByID(ctx, uint(id))
	require.NoError(t, err)
	require.False(t, stored.Enabled)
	require.Equal(t, "ops", stored.Name, "what the update did not name is kept")
	require.Equal(t, "on-call tools", *stored.Description)

	text, isError = call("attach_virtual_mcp", map[string]any{"virtual_mcp_id": id, "virtual_key_id": "vk-typo"})
	require.True(t, isError)
	require.Contains(t, text, "virtual key not found")
	text, isError = call("attach_virtual_mcp", map[string]any{"virtual_mcp_id": 9999, "virtual_key_id": "vk-1"})
	require.True(t, isError, text)

	text, isError = call("attach_virtual_mcp", map[string]any{"virtual_mcp_id": id, "virtual_key_id": "vk-1"})
	require.False(t, isError, text)
	require.Contains(t, text, "vk-1")
	text, isError = call("detach_virtual_mcp", map[string]any{"virtual_mcp_id": id, "virtual_key_id": "vk-1"})
	require.False(t, isError, text)
	attached, err := store.GetVirtualKeyIDsForVirtualMCP(ctx, uint(id))
	require.NoError(t, err)
	require.Empty(t, attached)

	require.Equal(t, []string{
		fmt.Sprintf("reload:%d", uint(id)), fmt.Sprintf("reload:%d", uint(id)),
		fmt.Sprintf("attach:vk-1:%d", uint(id)), fmt.Sprintf("detach:vk-1:%d", uint(id)),
	}, governance.calls)
}

// writeToolGovernanceManager is the live governance state as the handler sees
// it: a reload reads the row back from the store, as the real one does.
type writeToolGovernanceManager struct {
	pricingOverrideTestGovernanceManager
	store configstore.ConfigStore
	calls []string
}

func (m *writeToolGovernanceManager) ReloadVirtualKey(ctx context.Context, id string) (*configstoreTables.TableVirtualKey, error) {
	m.calls = append(m.calls, "vk:"+id)
	return m.store.GetVirtualKey(ctx, id)
}

func (m *writeToolGovernanceManager) ReloadTeam(ctx context.Context, id string) (*configstoreTables.TableTeam, error) {
	m.calls = append(m.calls, "team:"+id)
	return m.store.GetTeam(ctx, id)
}

func (m *writeToolGovernanceManager) ReloadCustomer(ctx context.Context, id string) (*configstoreTables.TableCustomer, error) {
	m.calls = append(m.calls, "customer:"+id)
	return m.store.GetCustomer(ctx, id)
}

func (m *writeToolGovernanceManager) ReloadModelConfig(ctx context.Context, id string) (*configstoreTables.TableModelConfig, error) {
	m.calls = append(m.calls, "model-config:"+id)
	return m.store.GetModelConfigByID(ctx, id)
}

// governanceWriteTools is a governance handler over a real store, and a way to
// call the write tools against it as any caller.
func governanceWriteTools(t *testing.T) (configstore.ConfigStore, *writeToolGovernanceManager, func(ctx context.Context, tool string, args map[string]any) (map[string]any, string)) {
	t.Helper()
	SetLogger(&mockLogger{})
	store := setupPricingOverrideHandlerStore(t)
	manager := &writeToolGovernanceManager{store: store}
	r := router.New()
	(&GovernanceHandler{configStore: store, governanceManager: manager}).RegisterRoutes(r)
	call := func(ctx context.Context, tool string, args map[string]any) (map[string]any, string) {
		t.Helper()
		text, isError := runWriteTool(t, ctx, r, mcptools.Deps{Governance: store}, tool, args)
		if isError {
			return nil, text
		}
		var out map[string]any
		require.NoError(t, json.Unmarshal([]byte(text), &out), text)
		return out, ""
	}
	return store, manager, call
}

// The governance tools write through the governance handler. Budgets and rate
// limits are set on their owner, under the handler's rules: a window that is
// not a positive duration is refused, the budget list replaces the one there,
// and a rate limit edit keeps the limits the caller did not name.
func TestWriteToolGovernance(t *testing.T) {
	store, manager, call := governanceWriteTools(t)
	ctx := context.Background()
	admin := adminCaller()

	customer, refusal := call(admin, "create_customer", map[string]any{"name": "acme"})
	require.Empty(t, refusal)
	customerID := customer["id"].(string)
	require.NotEmpty(t, customerID)

	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"budget window that is not a duration", map[string]any{"name": "ops", "budgets": []any{map[string]any{"max_limit": 10.0, "reset_duration": "soon"}}}},
		{"budget window of zero", map[string]any{"name": "ops", "budgets": []any{map[string]any{"max_limit": 10.0, "reset_duration": "0s"}}}},
		{"two budgets on one window", map[string]any{"name": "ops", "budgets": []any{
			map[string]any{"max_limit": 10.0, "reset_duration": "1d"}, map[string]any{"max_limit": 20.0, "reset_duration": "1d"},
		}}},
		{"rate limit window that is not a duration", map[string]any{"name": "ops", "request_max_limit": 5, "request_reset_duration": "soon"}},
		{"rate limit with no window", map[string]any{"name": "ops", "request_max_limit": 5}},
		{"customer that does not exist", map[string]any{"name": "ops", "customer_id": "no-such-customer"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, refusal := call(admin, "create_team", tc.args)
			require.NotEmpty(t, refusal, "the handler must refuse this")
		})
	}
	teams, _, err := store.GetTeamsPaginated(ctx, configstore.TeamsQueryParams{Limit: 10})
	require.NoError(t, err)
	require.Empty(t, teams, "nothing is stored when the handler refuses")

	team, refusal := call(admin, "create_team", map[string]any{
		"name": "ops", "customer_id": customerID,
		"budgets":         []any{map[string]any{"max_limit": 10.0, "reset_duration": "1d"}},
		"token_max_limit": 1000, "token_reset_duration": "1h",
	})
	require.Empty(t, refusal)
	teamID := team["id"].(string)
	require.Equal(t, customerID, team["customer_id"])
	require.Len(t, team["budgets"], 1)

	// A rate limit edit names one limit; the other is kept.
	_, refusal = call(admin, "update_team", map[string]any{"team_id": teamID, "request_max_limit": 50, "request_reset_duration": "1m"})
	require.Empty(t, refusal)
	stored, err := store.GetTeam(ctx, teamID)
	require.NoError(t, err)
	require.NotNil(t, stored.RateLimit)
	require.EqualValues(t, 1000, *stored.RateLimit.TokenMaxLimit, "the limit the caller did not name is kept")
	require.EqualValues(t, 50, *stored.RateLimit.RequestMaxLimit)
	require.Len(t, stored.Budgets, 1, "budgets the caller did not name are kept")

	// The budget list is the full set.
	_, refusal = call(admin, "update_team", map[string]any{"team_id": teamID, "budgets": []any{
		map[string]any{"max_limit": 25.0, "reset_duration": "1d"}, map[string]any{"max_limit": 500.0, "reset_duration": "1M"},
	}})
	require.Empty(t, refusal)
	stored, err = store.GetTeam(ctx, teamID)
	require.NoError(t, err)
	require.Len(t, stored.Budgets, 2)
	_, refusal = call(admin, "update_team", map[string]any{"team_id": teamID, "budgets": []any{}})
	require.Empty(t, refusal)
	stored, err = store.GetTeam(ctx, teamID)
	require.NoError(t, err)
	require.Empty(t, stored.Budgets, "[] removes every budget")
	require.NotNil(t, stored.RateLimit, "and leaves the rate limit alone")

	_, refusal = call(admin, "update_team", map[string]any{"team_id": teamID, "token_reset_duration": "0s"})
	require.NotEmpty(t, refusal, "a window of zero is refused on update too")

	_, refusal = call(admin, "update_customer", map[string]any{"customer_id": customerID, "name": "acme-corp", "budgets": []any{map[string]any{"max_limit": 1000.0, "reset_duration": "1M"}}})
	require.Empty(t, refusal)
	storedCustomer, err := store.GetCustomer(ctx, customerID)
	require.NoError(t, err)
	require.Equal(t, "acme-corp", storedCustomer.Name)
	require.Len(t, storedCustomer.Budgets, 1)

	key, refusal := call(admin, "create_virtual_key", map[string]any{"name": "ops-key", "team_id": teamID})
	require.Empty(t, refusal)
	keyID := key["id"].(string)
	secret := key["value"].(string)
	require.True(t, strings.HasPrefix(secret, "sk-bf-"), secret)
	require.Equal(t, teamID, key["team_id"])

	_, refusal = call(admin, "create_virtual_key", map[string]any{"name": "ops-key"})
	require.NotEmpty(t, refusal, "a second key of the same name is refused")
	_, refusal = call(admin, "create_virtual_key", map[string]any{"name": "lost", "team_id": "no-such-team"})
	require.NotEmpty(t, refusal)

	updated, refusal := call(admin, "update_virtual_key", map[string]any{"virtual_key_id": keyID, "name": "renamed", "request_max_limit": 5, "request_reset_duration": "1m"})
	require.Empty(t, refusal)
	require.NotContains(t, updated, "value")
	storedKey, err := store.GetVirtualKey(ctx, keyID)
	require.NoError(t, err)
	require.Equal(t, "renamed", storedKey.Name)
	require.Equal(t, secret, storedKey.Value.GetValue(), "an update does not touch the secret")
	require.Equal(t, teamID, *storedKey.TeamID)

	// A key has one owner: moving it to the customer takes it off the team.
	_, refusal = call(admin, "update_virtual_key", map[string]any{"virtual_key_id": keyID, "customer_id": customerID})
	require.Empty(t, refusal)
	storedKey, err = store.GetVirtualKey(ctx, keyID)
	require.NoError(t, err)
	require.Nil(t, storedKey.TeamID)
	require.Equal(t, customerID, *storedKey.CustomerID)

	_, refusal = call(admin, "deactivate_virtual_key", map[string]any{"virtual_key_id": keyID})
	require.Empty(t, refusal)
	storedKey, err = store.GetVirtualKey(ctx, keyID)
	require.NoError(t, err)
	require.False(t, *storedKey.IsActive)

	rotated, refusal := call(admin, "rotate_virtual_key", map[string]any{"virtual_key_id": keyID})
	require.Empty(t, refusal)
	require.NotEqual(t, secret, rotated["value"])
	storedKey, err = store.GetVirtualKey(ctx, keyID)
	require.NoError(t, err)
	require.Equal(t, rotated["value"], storedKey.Value.GetValue())

	config, refusal := call(admin, "create_model_config", map[string]any{
		"model_name": "gpt-4o", "scope": "virtual_key", "scope_id": keyID,
		"max_limit": 5.0, "reset_duration": "1d", "token_max_limit": 100, "token_reset_duration": "1h",
	})
	require.Empty(t, refusal)
	configID := config["id"].(string)
	_, refusal = call(admin, "create_model_config", map[string]any{"model_name": "gpt-4o", "scope": "virtual_key", "scope_id": keyID})
	require.NotEmpty(t, refusal, "a second config for the same model and scope is refused")
	_, refusal = call(admin, "update_model_config", map[string]any{"model_config_id": configID, "request_max_limit": 9, "request_reset_duration": "1m"})
	require.Empty(t, refusal)
	storedConfig, err := store.GetModelConfigByID(ctx, configID)
	require.NoError(t, err)
	require.EqualValues(t, 100, *storedConfig.RateLimit.TokenMaxLimit, "the limit the caller did not name is kept")
	require.EqualValues(t, 9, *storedConfig.RateLimit.RequestMaxLimit)

	require.Contains(t, manager.calls, "team:"+teamID)
	require.Contains(t, manager.calls, "customer:"+customerID)
	require.Contains(t, manager.calls, "vk:"+keyID)
	require.Contains(t, manager.calls, "model-config:"+configID)
}

// The handler takes any write it is sent; which rows a caller may change is
// the tools' check, made before the handler is reached. A key in one team
// changes that team's rows and nothing else in the store.
func TestWriteToolGovernanceStaysInsideTheCallersTenant(t *testing.T) {
	store, _, call := governanceWriteTools(t)
	ctx := context.Background()
	admin := adminCaller()

	mine, refusal := call(admin, "create_team", map[string]any{"name": "mine"})
	require.Empty(t, refusal)
	theirs, refusal := call(admin, "create_team", map[string]any{"name": "theirs", "budgets": []any{map[string]any{"max_limit": 10.0, "reset_duration": "1d"}}})
	require.Empty(t, refusal)
	mineID, theirsID := mine["id"].(string), theirs["id"].(string)
	theirKey, refusal := call(admin, "create_virtual_key", map[string]any{"name": "their-key", "team_id": theirsID})
	require.Empty(t, refusal)
	theirKeyID := theirKey["id"].(string)

	scoped := context.WithValue(adminCaller(), schemas.BifrostContextKeyGovernanceTeamID, mineID)
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"update_team", map[string]any{"team_id": theirsID, "budgets": []any{}}},
		{"update_team", map[string]any{"team_id": theirsID, "name": "taken"}},
		{"update_virtual_key", map[string]any{"virtual_key_id": theirKeyID, "team_id": mineID}},
		{"deactivate_virtual_key", map[string]any{"virtual_key_id": theirKeyID}},
		{"rotate_virtual_key", map[string]any{"virtual_key_id": theirKeyID}},
		{"create_virtual_key", map[string]any{"name": "planted", "team_id": theirsID}},
		{"create_model_config", map[string]any{"model_name": "gpt-4o", "scope": "virtual_key", "scope_id": theirKeyID}},
		{"create_model_config", map[string]any{"model_name": "gpt-4o"}},
		{"create_team", map[string]any{"name": "sibling"}},
		{"create_customer", map[string]any{"name": "globex"}},
		{"update_feature_flag", map[string]any{"id": "warp", "enabled": true}},
	} {
		_, refusal := call(scoped, tc.tool, tc.args)
		require.NotEmpty(t, refusal, "%s %v", tc.tool, tc.args)
	}
	team, err := store.GetTeam(ctx, theirsID)
	require.NoError(t, err)
	require.Equal(t, "theirs", team.Name)
	require.Len(t, team.Budgets, 1)
	key, err := store.GetVirtualKey(ctx, theirKeyID)
	require.NoError(t, err)
	require.Equal(t, theirsID, *key.TeamID)
	require.Equal(t, theirKey["value"], key.Value.GetValue())
	require.True(t, *key.IsActive)
	keys, _, err := store.GetVirtualKeysPaginated(ctx, configstore.VirtualKeyQueryParams{Limit: 10})
	require.NoError(t, err)
	require.Len(t, keys, 1)

	// Its own team is its to change, and a key it creates lands there.
	_, refusal = call(scoped, "update_team", map[string]any{"team_id": mineID, "budgets": []any{map[string]any{"max_limit": 5.0, "reset_duration": "1d"}}})
	require.Empty(t, refusal)
	created, refusal := call(scoped, "create_virtual_key", map[string]any{"name": "my-key"})
	require.Empty(t, refusal)
	require.Equal(t, mineID, created["team_id"])
}
