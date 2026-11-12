package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/framework/warp"
	"github.com/valyala/fasthttp"
)

// loadedWarpManagement loads the repo spec into a Bifrost MCP whose tool
// calls are served by root, and returns it as Warp's management tools.
func loadedWarpManagement(t *testing.T, root fasthttp.RequestHandler, allowWrites bool) *warpManagementTools {
	t.Helper()
	SetLogger(&mockLogger{})
	spec, err := filepath.Abs(repoOpenAPISpec)
	if err != nil {
		t.Fatal(err)
	}
	exec := NewBifrostMCPExecutor(root, 4<<20, 64<<10)
	t.Cleanup(func() { _ = exec.Close() })
	m := NewBifrostMCP(BifrostMCPOptions{SpecURL: spec, BaseURL: testBifrostMCPBaseURL, AllowWrites: allowWrites, ExecClient: exec.Client})
	if _, err := m.Server(context.Background()); err != nil {
		t.Fatalf("loading spec: %v", err)
	}
	return NewWarpManagementTools(m).(*warpManagementTools)
}

// Warp is offered every /api read except the log reads its own tools answer and
// the reads that return virtual key values, and the /api creates except the one
// that answers with a plaintext secret; never an update, a delete, a destructive
// POST or a route outside /api, however it is named. Creates appear only when
// the Bifrost MCP serves writes at all.
func TestWarpManagementCatalogOffersReadsAndCreates(t *testing.T) {
	catalog := loadedWarpManagement(t, func(*fasthttp.RequestCtx) {}, true).Catalog()
	if catalog.Len() == 0 {
		t.Fatal("empty catalog")
	}
	tools := map[string]bool{}
	for _, name := range []string{"listVirtualKeys", "getVirtualKey", "listUserVirtualKeys", "getUserVirtualKeysByEmail", "listVirtualKeyUsers", "listTeams", "getConfig", "createVirtualKey", "getLogs", "getMCPLogs", "updateVirtualKey", "deleteVirtualKey", "rotateVirtualKey", "createChatCompletion", "listModels", "createWebhookEndpoint"} {
		tools[name] = warpCatalogHas(catalog, name)
	}
	want := map[string]bool{
		"listVirtualKeys": false, "getVirtualKey": false, "listUserVirtualKeys": false, "getUserVirtualKeysByEmail": false,
		"listVirtualKeyUsers": true, "listTeams": true, "getConfig": true, "createVirtualKey": true,
		"getLogs": false, "getMCPLogs": false,
		"updateVirtualKey": false, "deleteVirtualKey": false, "rotateVirtualKey": false,
		"createChatCompletion": false, "listModels": false,
		"createWebhookEndpoint": false,
	}
	for name, offered := range want {
		if tools[name] != offered {
			t.Errorf("%s offered=%v, want %v", name, tools[name], offered)
		}
		if tool, ok := catalog.Tool(name); ok && tool.Write != strings.HasPrefix(name, "create") {
			t.Errorf("%s write=%v, want %v", name, tool.Write, strings.HasPrefix(name, "create"))
		}
	}

	readOnly := loadedWarpManagement(t, func(*fasthttp.RequestCtx) {}, false).Catalog()
	if warpCatalogHas(readOnly, "createVirtualKey") || !warpCatalogHas(readOnly, "listTeams") {
		t.Error("with writes off Warp must get reads only")
	}
}

// warpCatalogHas reports whether a catalog offers a tool.
func warpCatalogHas(catalog *warp.ManagementCatalog, name string) bool {
	_, ok := catalog.Tool(name)
	return ok
}

// A management call from Warp reaches /api as the dashboard user: it carries
// the dashboard's session cookie, which /api resolves the same way it did for
// the chat request (Bifrost's own session on OSS, the SSO session on
// Enterprise). /v1/mcp's own calls never carry a cookie.
func TestWarpManagementCallRunsAsTheDashboardUser(t *testing.T) {
	var seenAuth, seenCookie string
	r := router.New()
	r.GET("/api/providers", func(ctx *fasthttp.RequestCtx) {
		seenAuth = string(ctx.Request.Header.Peek("Authorization"))
		seenCookie = string(ctx.Request.Header.Peek("Cookie"))
		ctx.SetContentType("application/json")
		ctx.SetBodyString(`{"providers":[],"total":0}`)
	})
	tools := loadedWarpManagement(t, r.Handler, false)

	var req fasthttp.Request
	req.Header.Set("Cookie", "token=session-abc; bf_oidc_session=sso-xyz")
	dashboard := initCtx(&req)

	// Through the snapshot the chat route takes, which is what outlives the request.
	snapshot, cancel, err := snapshotWarpContext(dashboard, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	result, failed, err := tools.Call(snapshot, tools.Catalog(), "listProviders", map[string]any{})
	if err != nil || failed {
		t.Fatalf("call failed: %v %q", err, result)
	}
	if seenCookie != "token=session-abc; bf_oidc_session=sso-xyz" {
		t.Errorf("Cookie %q, want the dashboard's session cookies", seenCookie)
	}
	if seenAuth != "" {
		t.Errorf("Authorization %q, want none: the cookie is the credential", seenAuth)
	}

	// A credential header the request carries is forwarded as it is.
	var withHeader fasthttp.Request
	withHeader.Header.Set("Authorization", "Basic YWRtaW46cHc=")
	dashboard = initCtx(&withHeader)
	if _, _, err := tools.Call(warpManagementCaller(context.Background(), dashboard), tools.Catalog(), "listProviders", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if seenAuth != "Basic YWRtaW46cHc=" || seenCookie != "" {
		t.Errorf("Authorization %q Cookie %q, want the request's own header and no cookie", seenAuth, seenCookie)
	}

	// The /v1/mcp caller drops the cookie, even through the same client.
	if _, _, err := tools.Call(WithBifrostMCPCaller(context.Background(), &req, nil), tools.Catalog(), "listProviders", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if seenCookie != "" {
		t.Errorf("Cookie %q forwarded for a /v1/mcp caller", seenCookie)
	}

	// No caller on ctx means no credential, which /api would refuse.
	if _, _, err := tools.Call(context.Background(), tools.Catalog(), "listProviders", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if seenAuth != "" || seenCookie != "" {
		t.Errorf("Authorization %q Cookie %q sent without a caller", seenAuth, seenCookie)
	}
}

// BenchmarkWarpManagementCaller is what each Warp chat request now spends
// recording its caller for management calls.
func BenchmarkWarpManagementCaller(b *testing.B) {
	var req fasthttp.Request
	req.Header.Set("Cookie", "token=session-abc")
	req.Header.Set("User-Agent", "Mozilla/5.0")
	dashboard := initCtx(&req)
	b.ReportAllocs()
	for b.Loop() {
		_ = warpManagementCaller(context.Background(), dashboard)
	}
}

// With no spec loaded yet (the boot load failed or has not finished), a Warp turn
// is not held up: it gets no management tools and starts a load, and a later turn
// is offered them once it lands.
func TestWarpManagementLoadsSpecInBackground(t *testing.T) {
	SetLogger(&mockLogger{})
	spec, err := filepath.Abs(repoOpenAPISpec)
	if err != nil {
		t.Fatal(err)
	}
	exec := NewBifrostMCPExecutor(func(*fasthttp.RequestCtx) {}, 4<<20, 64<<10)
	t.Cleanup(func() { _ = exec.Close() })
	m := NewBifrostMCP(BifrostMCPOptions{SpecURL: spec, BaseURL: testBifrostMCPBaseURL, ExecClient: exec.Client})
	tools := NewWarpManagementTools(m).(*warpManagementTools)

	if catalog := tools.Catalog(); catalog != nil && catalog.Len() > 0 {
		t.Fatal("tools offered before the spec loaded")
	}
	deadline := time.Now().Add(10 * time.Second)
	for m.loaded.Load() == nil {
		if time.Now().After(deadline) {
			t.Fatal("the turn did not start a spec load")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !warpCatalogHas(tools.Catalog(), "listTeams") {
		t.Error("listTeams not offered once the spec loaded")
	}
}

// With a public base URL configured, Warp's tool calls arrive through it as
// /v1/mcp's do: the route sees the configured host and scheme rather than the
// dashboard request's, and the dashboard cookie still rides along.
func TestWarpManagementCallUsesPublicBaseURL(t *testing.T) {
	SetLogger(&mockLogger{})
	var seenHost, seenProto, seenCookie string
	r := router.New()
	r.GET("/api/providers", func(ctx *fasthttp.RequestCtx) {
		seenHost = string(ctx.Host())
		seenProto = string(ctx.Request.Header.Peek("x-bf-forwarded-proto"))
		seenCookie = string(ctx.Request.Header.Peek("Cookie"))
		ctx.SetContentType("application/json")
		ctx.SetBodyString(`{"providers":[],"total":0}`)
	})
	spec, err := filepath.Abs(repoOpenAPISpec)
	if err != nil {
		t.Fatal(err)
	}
	exec := NewBifrostMCPExecutor(r.Handler, 4<<20, 64<<10)
	t.Cleanup(func() { _ = exec.Close() })
	m := NewBifrostMCP(BifrostMCPOptions{SpecURL: spec, BaseURL: "https://gw.example.com", BaseIsEdge: true, ExecClient: exec.Client})
	if _, err := m.Server(context.Background()); err != nil {
		t.Fatal(err)
	}
	tools := NewWarpManagementTools(m).(*warpManagementTools)

	var req fasthttp.Request
	req.Header.SetHost("dashboard.internal:8080")
	req.Header.Set("Cookie", "token=session-abc")
	if _, failed, err := tools.Call(warpManagementCaller(context.Background(), initCtx(&req)), tools.Catalog(), "listProviders", map[string]any{}); err != nil || failed {
		t.Fatalf("call failed: %v", err)
	}
	if seenHost != "gw.example.com" || seenProto != "https" {
		t.Errorf("/api saw Host %q and x-bf-forwarded-proto %q, want gw.example.com and https", seenHost, seenProto)
	}
	if seenCookie != "token=session-abc" {
		t.Errorf("Cookie %q, want the dashboard's session cookie", seenCookie)
	}
}

// Repeated turns while the spec is missing share one background load, and after
// that load fails no turn starts another until the retry interval has passed.
func TestWarpManagementTurnsShareOneSpecLoad(t *testing.T) {
	SetLogger(&mockLogger{})
	var hits atomic.Int32
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		<-release
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer upstream.Close()
	// Deferred after Close so it runs first: Close waits for the handler held on
	// release, which would hang a test that fails before the body releases it.
	var releaseOnce sync.Once
	releaseFetch := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseFetch()
	m := NewBifrostMCP(BifrostMCPOptions{SpecURL: upstream.URL + "/openapi.json", BaseURL: testBifrostMCPBaseURL, SpecClient: upstream.Client(), ExecClient: http.DefaultClient})
	tools := NewWarpManagementTools(m).(*warpManagementTools)

	for range 50 {
		tools.Catalog()
	}
	waitFor := func(cond func() bool, what string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatal(what)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitFor(func() bool { return hits.Load() == 1 }, "no load started")
	releaseFetch()
	waitFor(func() bool { return !m.loading.Load() && m.retryAt.Load() != 0 }, "the failed load was not recorded")
	for range 50 {
		tools.Catalog()
	}
	time.Sleep(100 * time.Millisecond)
	if got := hits.Load(); got != 1 {
		t.Fatalf("spec fetched %d times, want 1: turns started loads while one ran or right after it failed", got)
	}

	// Once the retry interval has passed, the next turn tries again.
	m.retryAt.Store(time.Now().Add(-time.Second).UnixNano())
	tools.Catalog()
	waitFor(func() bool { return hits.Load() == 2 }, "no retry after the interval")
}

// A call runs against the load its catalog came from: after a reload that adds
// writes, a turn holding the earlier read-only catalog still cannot reach the
// create, while the new catalog can.
func TestWarpManagementCallIsBoundToItsCatalog(t *testing.T) {
	SetLogger(&mockLogger{})
	spec, err := filepath.Abs(repoOpenAPISpec)
	if err != nil {
		t.Fatal(err)
	}
	var created atomic.Int32
	r := router.New()
	r.POST("/api/governance/virtual-keys", func(ctx *fasthttp.RequestCtx) {
		created.Add(1)
		ctx.SetContentType("application/json")
		ctx.SetBodyString(`{"virtual_key":{"id":"vk-1"}}`)
	})
	exec := NewBifrostMCPExecutor(r.Handler, 4<<20, 64<<10)
	t.Cleanup(func() { _ = exec.Close() })
	opts := BifrostMCPOptions{SpecURL: spec, BaseURL: testBifrostMCPBaseURL, ExecClient: exec.Client}
	m := NewBifrostMCP(opts)
	if _, err := m.Server(context.Background()); err != nil {
		t.Fatal(err)
	}
	tools := NewWarpManagementTools(m).(*warpManagementTools)
	before := tools.Catalog()

	opts.AllowWrites = true
	if err := m.Reload(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	after := tools.Catalog()
	if after.Binding() == before.Binding() {
		t.Fatal("a reload kept the earlier binding")
	}
	args := map[string]any{"name": "team-a"}
	if _, _, err := tools.Call(context.Background(), before, "createVirtualKey", args); err == nil {
		t.Error("a write ran through a catalog that never offered it")
	}
	if created.Load() != 0 {
		t.Fatal("the write reached /api through the earlier catalog")
	}
	if _, failed, err := tools.Call(context.Background(), after, "createVirtualKey", args); err != nil || failed {
		t.Fatalf("the reloaded catalog's write failed: %v", err)
	}
	if created.Load() != 1 {
		t.Errorf("create reached /api %d times, want 1", created.Load())
	}
}

// A management result that embeds virtual keys reaches the model without their
// values: a customer listed with its keys, a single key, and a refusal whose body
// follows a status line. Everything else in the result is kept, and a body that
// names no virtual key is returned as it is.
func TestWarpManagementRedactsVirtualKeyValues(t *testing.T) {
	customers := `{"customers":[{"id":"c1","name":"acme","virtual_keys":[{"id":"vk1","name":"acme-key","value":"sk-bf-secret-1"}]}],"count":1}`
	got := redactVirtualKeyValues(customers)
	if strings.Contains(got, "sk-bf-secret-1") {
		t.Fatalf("a customer's key value reached the result: %s", got)
	}
	for _, kept := range []string{`"acme"`, `"acme-key"`, `"vk1"`} {
		if !strings.Contains(got, kept) {
			t.Errorf("%s was dropped: %s", kept, got)
		}
	}
	single := `{"virtual_key":{"id":"vk2","value":{"value":"sk-bf-secret-2","type":"plain_text"},"team":{"id":"t1","value":"kept"}}}`
	got = redactVirtualKeyValues(single)
	if strings.Contains(got, "sk-bf-secret-2") {
		t.Fatalf("a key's value reached the result: %s", got)
	}
	if !strings.Contains(got, `"kept"`) {
		t.Errorf("a value field outside the key itself was dropped: %s", got)
	}
	refusal := "HTTP 409 Conflict\n" + customers
	got = redactVirtualKeyValues(refusal)
	if strings.Contains(got, "sk-bf-secret-1") || !strings.HasPrefix(got, "HTTP 409 Conflict\n") {
		t.Fatalf("a refusal's body was not redacted, or lost its status line: %s", got)
	}
	// Numbers are carried as written: an id past 2^53 would lose digits as a float.
	big := `{"virtual_key":{"id":"vk3","value":"sk-bf-secret-3","budget_id":9007199254740993}}`
	if got := redactVirtualKeyValues(big); !strings.Contains(got, "9007199254740993") || strings.Contains(got, "sk-bf-secret-3") {
		t.Errorf("a large integer changed, or the value stayed: %s", got)
	}
	// A body cut at the response limit no longer parses; it is withheld, not passed on.
	truncated := customers[:len(customers)-12] + "\n…[response truncated at 65536 bytes]"
	if got := redactVirtualKeyValues(truncated); strings.Contains(got, "sk-bf-secret-1") || strings.Contains(got, `"value"`) {
		t.Errorf("a truncated body kept its key value: %s", got)
	}
	if got := redactVirtualKeyValues("HTTP 500 Internal Server Error\n" + truncated); strings.Contains(got, "sk-bf-secret-1") || !strings.HasPrefix(got, "HTTP 500 Internal Server Error\n") {
		t.Errorf("a truncated refusal kept its key value, or lost its status line: %s", got)
	}
	plain := `{"providers":[{"name":"openai","value":"x"}]}`
	if got := redactVirtualKeyValues(plain); got != plain {
		t.Errorf("a body naming no virtual key changed: %s", got)
	}
}
