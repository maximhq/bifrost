package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/fasthttp/router"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/openapimcp"
	"github.com/valyala/fasthttp"
)

// repoOpenAPISpec is the spec this source tree publishes, which a release
// gateway fetches from its tag.
const repoOpenAPISpec = "../../../docs/openapi/openapi.json"

func readRepoSpec(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(repoOpenAPISpec)
	if err != nil {
		t.Fatalf("reading %s: %v", repoOpenAPISpec, err)
	}
	return data
}

func repoSpecOperations(t *testing.T) []openapimcp.Operation {
	t.Helper()
	doc, err := openapimcp.Parse(readRepoSpec(t), openapimcp.ParseOptions{})
	if err != nil {
		t.Fatalf("parsing repo spec: %v", err)
	}
	return doc.Operations
}

func managementTestOptions(allowWrites bool) ManagementMCPOptions {
	return ManagementMCPOptions{AllowWrites: allowWrites, ExecClient: http.DefaultClient}
}

// listManagementTools returns the tools a built server answers tools/list with.
func listManagementTools(t *testing.T, opts ManagementMCPOptions) map[string]mcp.Tool {
	t.Helper()
	srv, _, err := buildManagementMCPServer(readRepoSpec(t), "", opts)
	if err != nil {
		t.Fatalf("building server: %v", err)
	}
	tools := map[string]mcp.Tool{}
	for name, st := range srv.ListTools() {
		tools[name] = st.Tool
	}
	return tools
}

// Every current /api operation in the spec becomes a tool unless it is denied.
// An operation the synthesizer cannot serve fails here by name, so a new
// endpoint that silently drops out of the MCP is caught in CI.
func TestManagementMCPExposesEveryAPIOperation(t *testing.T) {
	_, report, err := buildManagementMCPServer(readRepoSpec(t), "", managementTestOptions(true))
	if err != nil {
		t.Fatalf("building server: %v", err)
	}
	knownUnsupported := map[string]bool{"uploadSkillFile": true} // multipart upload
	for _, op := range report.unsupported {
		if !knownUnsupported[op.OperationID] {
			t.Errorf("/api operation %s (%s %s) cannot become a tool: %s", op.OperationID, op.Method, op.Path, op.Reason)
		}
	}
	tools := listManagementTools(t, managementTestOptions(true))
	for _, op := range repoSpecOperations(t) {
		if !strings.HasPrefix(op.Path, "/api/") || op.Deprecated || managementMCPDenied[op.ID] || knownUnsupported[op.ID] || managementMCPEnterpriseOnly(op) {
			continue
		}
		if _, ok := tools[op.ID]; !ok {
			t.Errorf("/api operation %s (%s %s) is not served", op.ID, op.Method, op.Path)
		}
	}
}

// Every deny-list entry names an operation the spec defines, and none is served.
func TestManagementMCPDenyListMatchesSpec(t *testing.T) {
	_, report, err := buildManagementMCPServer(readRepoSpec(t), "", managementTestOptions(true))
	if err != nil {
		t.Fatalf("building server: %v", err)
	}
	sort.Strings(report.unknown)
	if len(report.unknown) > 0 {
		t.Errorf("denied operationIds missing from %s: %v", repoOpenAPISpec, report.unknown)
	}
	tools := listManagementTools(t, managementTestOptions(true))
	for id := range managementMCPDenied {
		if _, ok := tools[id]; ok {
			t.Errorf("denied operation %s is served", id)
		}
	}
}

func TestManagementMCPCuration(t *testing.T) {
	enterprise := map[string]any{"x-mint": map[string]any{"metadata": map[string]any{"tag": "Enterprise"}}}
	ops := []openapimcp.Operation{
		{ID: "listProviders", Method: http.MethodGet, Path: "/api/providers"},
		{ID: "addProvider", Method: http.MethodPost, Path: "/api/providers"},
		{ID: "chatCompletion", Method: http.MethodPost, Path: "/v1/chat/completions"},
		{ID: "login", Method: http.MethodPost, Path: "/api/session/login"},
		{ID: "listTeamsLegacy", Method: http.MethodGet, Path: "/api/teams", Deprecated: true},
		{ID: "listUsers", Method: http.MethodGet, Path: "/api/governance/users", Extensions: enterprise},
	}
	ids := func(kept []openapimcp.Operation) []string {
		var out []string
		for _, op := range kept {
			out = append(out, op.ID)
		}
		return out
	}
	kept, _, unknown := curateManagementOperations(ops, ManagementMCPOptions{AllowWrites: true})
	if got := ids(kept); strings.Join(got, ",") != "listProviders,addProvider" {
		t.Errorf("OSS, writes on kept %v, want listProviders and addProvider", got)
	}
	if len(unknown) != len(managementMCPDenied)-1 {
		t.Errorf("unknown = %d entries, want every denied id but login", len(unknown))
	}
	kept, _, _ = curateManagementOperations(ops, ManagementMCPOptions{})
	if got := ids(kept); strings.Join(got, ",") != "listProviders" {
		t.Errorf("OSS, writes off kept %v, want only listProviders", got)
	}
	kept, _, _ = curateManagementOperations(ops, ManagementMCPOptions{AllowWrites: true, Edition: ManagementMCPEditionEnterprise})
	if got := ids(kept); strings.Join(got, ",") != "listProviders,addProvider,listUsers" {
		t.Errorf("enterprise kept %v, want the enterprise-only listUsers too", got)
	}
	// An operation the gateway does not route is left out and counted.
	served := func(method, path string) bool { return method == http.MethodGet }
	kept, unrouted, _ := curateManagementOperations(ops, ManagementMCPOptions{AllowWrites: true, Served: served})
	if got := ids(kept); strings.Join(got, ",") != "listProviders" || unrouted != 1 {
		t.Errorf("served filter kept %v (unrouted %d), want listProviders with addProvider unrouted", got, unrouted)
	}
}

// An OSS gateway serves none of the spec's enterprise-only operations, whichever
// spec it read; an enterprise gateway serves them.
func TestManagementMCPEditions(t *testing.T) {
	oss := listManagementTools(t, managementTestOptions(true))
	if _, ok := oss["listUsers"]; ok {
		t.Error("OSS serves the enterprise-only listUsers")
	}
	if _, ok := oss["listProviders"]; !ok {
		t.Error("OSS lacks listProviders")
	}
	opts := managementTestOptions(true)
	opts.Edition = ManagementMCPEditionEnterprise
	if _, ok := listManagementTools(t, opts)["listUsers"]; !ok {
		t.Error("enterprise lacks listUsers")
	}
}

func TestManagementMCPWritesSwitch(t *testing.T) {
	readOnly := listManagementTools(t, managementTestOptions(false))
	for name, tool := range readOnly {
		if tool.Annotations.ReadOnlyHint == nil || !*tool.Annotations.ReadOnlyHint {
			t.Errorf("writes off: %s is served but not read-only", name)
		}
	}
	if _, ok := readOnly["listProviders"]; !ok {
		t.Error("writes off: listProviders missing")
	}
	if _, ok := readOnly["createVirtualKey"]; ok {
		t.Error("writes off: createVirtualKey is served")
	}

	withWrites := listManagementTools(t, managementTestOptions(true))
	if _, ok := withWrites["createVirtualKey"]; !ok {
		t.Error("writes on: createVirtualKey missing")
	}
	if len(withWrites) <= len(readOnly) {
		t.Errorf("writes on serves %d tools, writes off %d", len(withWrites), len(readOnly))
	}
}

func TestManagementMCPAnnotations(t *testing.T) {
	tools := listManagementTools(t, managementTestOptions(true))
	cases := []struct {
		name                  string
		readOnly, destructive bool
	}{
		{"listVirtualKeys", true, false},
		{"createVirtualKey", false, false},
		{"updateVirtualKey", false, false},
		{"deleteVirtualKey", false, true},
		{"createWebhookEndpoint", false, false},
	}
	for _, tc := range cases {
		tool, ok := tools[tc.name]
		if !ok {
			t.Errorf("%s not served", tc.name)
			continue
		}
		ann := tool.Annotations
		if ann.ReadOnlyHint == nil || *ann.ReadOnlyHint != tc.readOnly {
			t.Errorf("%s ReadOnlyHint = %v, want %v", tc.name, ann.ReadOnlyHint, tc.readOnly)
		}
		if ann.DestructiveHint == nil || *ann.DestructiveHint != tc.destructive {
			t.Errorf("%s DestructiveHint = %v, want %v", tc.name, ann.DestructiveHint, tc.destructive)
		}
		if ann.OpenWorldHint == nil || *ann.OpenWorldHint {
			t.Errorf("%s OpenWorldHint = %v, want false", tc.name, ann.OpenWorldHint)
		}
	}
}

// x-bifrost-destructive marks a POST or PUT destructive; without it only DELETE
// is. Driven by synthetic operations: openapi.json is regenerated by CI after
// merge, so a marker added in the YAML is not in the committed bundle yet.
func TestManagementMCPDestructiveMarker(t *testing.T) {
	cases := []struct {
		method string
		ext    map[string]any
		want   bool
	}{
		{http.MethodPost, nil, false},
		{http.MethodPost, map[string]any{managementMCPDestructiveExt: true}, true},
		{http.MethodPut, map[string]any{managementMCPDestructiveExt: true}, true},
		{http.MethodPut, map[string]any{managementMCPDestructiveExt: false}, false},
		{http.MethodPost, map[string]any{managementMCPDestructiveExt: "yes"}, false},
		{http.MethodDelete, nil, true},
	}
	for _, tc := range cases {
		tool := openapimcp.Tool{Operation: openapimcp.Operation{ID: "op", Method: tc.method, Extensions: tc.ext}}
		annotateManagementTool(&tool)
		if got := tool.Annotations.DestructiveHint; got == nil || *got != tc.want {
			t.Errorf("%s %v: DestructiveHint = %v, want %v", tc.method, tc.ext, got, tc.want)
		}
	}
}

// A failed load leaves the server unset (the endpoint answers 503); a later
// successful load publishes it.
func TestManagementMCPLoadFailureThenRecovery(t *testing.T) {
	spec := readRepoSpec(t)
	healthy := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !healthy {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(spec)
	}))
	defer upstream.Close()

	m := NewManagementMCP(ManagementMCPOptions{
		SpecSources: []string{upstream.URL + "/openapi.json"},
		SpecClient:  upstream.Client(),
		ExecClient:  http.DefaultClient,
	})
	if err := m.Load(context.Background()); err == nil {
		t.Fatal("load succeeded against a failing upstream")
	}
	if m.Server() != nil {
		t.Fatal("server published after a failed load")
	}
	healthy = true
	if err := m.Load(context.Background()); err != nil {
		t.Fatalf("load after recovery: %v", err)
	}
	if m.Server() == nil {
		t.Fatal("server not published after a successful load")
	}
}

func TestManagementMCPLoadFromFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "openapi.json"), readRepoSpec(t), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, opts := range map[string]ManagementMCPOptions{
		"relative to app dir": {SpecSources: []string{"openapi.json"}, AppDir: dir, ExecClient: http.DefaultClient},
		"absolute":            {SpecSources: []string{filepath.Join(dir, "openapi.json")}, ExecClient: http.DefaultClient},
	} {
		m := NewManagementMCP(opts)
		if err := m.Load(context.Background()); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestManagementMCPSpecSources(t *testing.T) {
	t.Setenv(ManagementMCPSpecEnv, "")
	suffixes := func(sources []string) []string {
		var out []string
		for _, s := range sources {
			out = append(out, s[strings.Index(s, "/refs/"):])
		}
		return out
	}
	cases := []struct {
		version string
		edition ManagementMCPEdition
		want    []string
	}{
		{"v2.3.1", ManagementMCPEditionOSS, []string{"/refs/tags/transports/v2.3.1/docs/openapi/openapi-oss.json", "/refs/tags/transports/v2.3.1/docs/openapi/openapi.json"}},
		{"v2.3.1", ManagementMCPEditionEnterprise, []string{"/refs/tags/transports/v2.3.1/docs/openapi/openapi.json"}},
		{"", ManagementMCPEditionOSS, []string{"/refs/heads/dev/docs/openapi/openapi-oss.json", "/refs/heads/dev/docs/openapi/openapi.json"}},
		{unsetBuildVersion, ManagementMCPEditionEnterprise, []string{"/refs/heads/dev/docs/openapi/openapi.json"}},
		{"v2.3.2-enterprise", ManagementMCPEditionEnterprise, []string{"/refs/tags/transports/v2.3.2/docs/openapi/openapi.json"}},
		{"v2.0.0-prerelease3", ManagementMCPEditionEnterprise, []string{"/refs/tags/transports/v2.0.0-prerelease3/docs/openapi/openapi.json"}},
		{"v1.0.0-enterprise", ManagementMCPEditionEnterprise, []string{"/refs/heads/dev/docs/openapi/openapi.json"}},
		{"vdev-build", ManagementMCPEditionOSS, []string{"/refs/heads/dev/docs/openapi/openapi-oss.json", "/refs/heads/dev/docs/openapi/openapi.json"}},
		{"vdev-enterprise", ManagementMCPEditionEnterprise, []string{"/refs/heads/dev/docs/openapi/openapi.json"}},
	}
	for _, tc := range cases {
		if got := suffixes(ManagementMCPSpecSources(tc.version, tc.edition)); strings.Join(got, " ") != strings.Join(tc.want, " ") {
			t.Errorf("%q %s reads %v, want %v", tc.version, tc.edition, got, tc.want)
		}
	}
	t.Setenv(ManagementMCPSpecEnv, "/etc/bifrost/openapi.json")
	if got := ManagementMCPSpecSources("v2.3.1", ManagementMCPEditionOSS); len(got) != 1 || got[0] != "/etc/bifrost/openapi.json" {
		t.Errorf("override ignored: got %v", got)
	}
}

// A missing OSS spec (a release cut before CI published it) falls back to the
// full one; its enterprise-only operations are still left out.
func TestManagementMCPSpecFallback(t *testing.T) {
	spec := readRepoSpec(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openapi.json" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(spec)
	}))
	defer upstream.Close()

	m := NewManagementMCP(ManagementMCPOptions{
		SpecSources: []string{upstream.URL + "/openapi-oss.json", upstream.URL + "/openapi.json"},
		SpecClient:  upstream.Client(),
		ExecClient:  http.DefaultClient,
	})
	if err := m.Load(context.Background()); err != nil {
		t.Fatalf("load with fallback: %v", err)
	}
	tools := m.Server().ListTools()
	if _, ok := tools["listProviders"]; !ok {
		t.Error("listProviders missing after fallback")
	}
	if _, ok := tools["listUsers"]; ok {
		t.Error("enterprise-only listUsers served after falling back to the full spec")
	}

	none := NewManagementMCP(ManagementMCPOptions{SpecSources: []string{upstream.URL + "/a.json", upstream.URL + "/b.json"}, SpecClient: upstream.Client(), ExecClient: http.DefaultClient})
	err := none.Load(context.Background())
	if err == nil || !strings.Contains(err.Error(), "/a.json") || !strings.Contains(err.Error(), "/b.json") {
		t.Errorf("all sources failing: %v, want both named", err)
	}
}

func TestManagementMCPAllowWrites(t *testing.T) {
	t.Setenv(ManagementMCPWritesEnv, "")
	if got := ManagementMCPAllowWrites(); got != managementMCPWritesDefault {
		t.Errorf("unset = %v, want default %v", got, managementMCPWritesDefault)
	}
	t.Setenv(ManagementMCPWritesEnv, "true")
	if !ManagementMCPAllowWrites() {
		t.Error("true not honoured")
	}
	t.Setenv(ManagementMCPWritesEnv, "false")
	if ManagementMCPAllowWrites() {
		t.Error("false not honoured")
	}
}

// newManagementTestEndpoint loads the repo spec into a management MCP whose tool
// calls are served in process by root, and returns its /v1/mcp handler.
func newManagementTestEndpoint(t *testing.T, root fasthttp.RequestHandler) *ManagementMCPHandler {
	t.Helper()
	SetLogger(&mockLogger{})
	spec, err := filepath.Abs(repoOpenAPISpec)
	if err != nil {
		t.Fatal(err)
	}
	exec := NewManagementMCPExecutor(root, 4<<20, 64<<10)
	t.Cleanup(func() { _ = exec.Close() })
	m := NewManagementMCP(ManagementMCPOptions{SpecSources: []string{spec}, AllowWrites: true, ExecClient: exec.Client})
	if err := m.Load(context.Background()); err != nil {
		t.Fatalf("loading spec: %v", err)
	}
	return NewManagementMCPHandler(m)
}

// postManagementMCP sends one JSON-RPC message to /v1/mcp and returns the
// status and the decoded response (nil for an empty body).
func postManagementMCP(t *testing.T, h *ManagementMCPHandler, body string, prepare func(*fasthttp.RequestCtx)) (int, map[string]any) {
	t.Helper()
	var req fasthttp.Request
	req.Header.SetMethod(fasthttp.MethodPost)
	req.Header.SetContentType("application/json")
	req.SetRequestURI("/v1/mcp")
	req.SetBodyString(body)
	ctx := initCtx(&req)
	if prepare != nil {
		prepare(ctx)
	}
	h.handlePost(ctx)
	var out map[string]any
	if len(ctx.Response.Body()) > 0 {
		if err := json.Unmarshal(ctx.Response.Body(), &out); err != nil {
			t.Fatalf("decoding %s: %v", ctx.Response.Body(), err)
		}
	}
	return ctx.Response.StatusCode(), out
}

func callManagementTool(name string) string {
	return `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"` + name + `","arguments":{}}}`
}

// toolCallResult returns a tools/call result's text and whether it is an error.
func toolCallResult(t *testing.T, resp map[string]any) (string, bool) {
	t.Helper()
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result in %v", resp)
	}
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("empty content in %v", resp)
	}
	text, _ := content[0].(map[string]any)["text"].(string)
	isError, _ := result["isError"].(bool)
	return text, isError
}

func TestManagementMCPEndpoint_Protocol(t *testing.T) {
	h := newManagementTestEndpoint(t, func(ctx *fasthttp.RequestCtx) {})

	status, resp := postManagementMCP(t, h, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`, nil)
	if status != fasthttp.StatusOK || resp["result"] == nil {
		t.Fatalf("initialize: status %d, %v", status, resp)
	}

	status, resp = postManagementMCP(t, h, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, nil)
	if status != fasthttp.StatusOK {
		t.Fatalf("tools/list: status %d, %v", status, resp)
	}
	names := map[string]bool{}
	for _, tool := range resp["result"].(map[string]any)["tools"].([]any) {
		names[tool.(map[string]any)["name"].(string)] = true
	}
	if !names["listProviders"] || names["login"] {
		t.Errorf("tools/list: listProviders=%v login=%v, want true/false", names["listProviders"], names["login"])
	}

	status, resp = postManagementMCP(t, h, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, nil)
	if status != fasthttp.StatusAccepted || resp != nil {
		t.Errorf("notification: status %d, %v; want 202 with no body", status, resp)
	}
}

// /v1/mcp answers POST only, and GET is answered by /v1/mcp itself rather than
// the dashboard's catch-all. /v1/mcp/tool/execute keeps its own route.
func TestManagementMCPEndpoint_Routes(t *testing.T) {
	h := newManagementTestEndpoint(t, func(ctx *fasthttp.RequestCtx) {})
	r := router.New()
	h.RegisterRoutes(r)
	r.POST("/v1/mcp/tool/execute", func(ctx *fasthttp.RequestCtx) { ctx.SetStatusCode(fasthttp.StatusTeapot) })
	r.GET("/{filepath:*}", func(ctx *fasthttp.RequestCtx) { ctx.SetBodyString("dashboard") })
	serve := func(method, path string) *fasthttp.RequestCtx {
		var req fasthttp.Request
		req.Header.SetMethod(method)
		req.SetRequestURI(path)
		req.SetBodyString(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
		ctx := initCtx(&req)
		r.Handler(ctx)
		return ctx
	}

	get := serve(fasthttp.MethodGet, "/v1/mcp")
	if get.Response.StatusCode() != fasthttp.StatusMethodNotAllowed || string(get.Response.Header.Peek("Allow")) != fasthttp.MethodPost {
		t.Errorf("GET /v1/mcp: status %d, Allow %q; want 405, POST", get.Response.StatusCode(), get.Response.Header.Peek("Allow"))
	}
	if post := serve(fasthttp.MethodPost, "/v1/mcp"); post.Response.StatusCode() != fasthttp.StatusOK {
		t.Errorf("POST /v1/mcp: status %d", post.Response.StatusCode())
	}
	if exec := serve(fasthttp.MethodPost, "/v1/mcp/tool/execute"); exec.Response.StatusCode() != fasthttp.StatusTeapot {
		t.Errorf("POST /v1/mcp/tool/execute reached another handler: status %d", exec.Response.StatusCode())
	}
}

func TestManagementMCPEndpoint_UnavailableUntilLoaded(t *testing.T) {
	SetLogger(&mockLogger{})
	h := NewManagementMCPHandler(NewManagementMCP(ManagementMCPOptions{}))
	status, resp := postManagementMCP(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, nil)
	if status != fasthttp.StatusServiceUnavailable {
		t.Errorf("status %d, %v; want 503", status, resp)
	}
}

// A tool call carries the caller's credential, Host, forwarding headers and peer
// address into /api, and never a Cookie: a browser session cannot be ridden
// through /v1/mcp.
func TestManagementMCPEndpoint_ForwardsCallerButNotCookies(t *testing.T) {
	var seen fasthttp.Request
	var seenRemote string
	h := newManagementTestEndpoint(t, func(ctx *fasthttp.RequestCtx) {
		ctx.Request.CopyTo(&seen)
		seenRemote = ctx.RemoteIP().String()
		ctx.SetContentType("application/json")
		ctx.SetBodyString(`"v-test"`)
	})
	status, resp := postManagementMCP(t, h, callManagementTool("getVersion"), func(ctx *fasthttp.RequestCtx) {
		ctx.Request.Header.Set("Authorization", "Bearer bfst-test")
		ctx.Request.Header.Set(SetupTokenHeader, "s3cret")
		ctx.Request.Header.Set("X-Forwarded-For", "203.0.113.9")
		ctx.Request.Header.SetHost("gw.example.com")
		ctx.Request.Header.SetCookie("token", "dashboard-session")
		ctx.Request.Header.SetCookie(SetupSessionCookie, "setup-session")
		ctx.SetRemoteAddr(&net.TCPAddr{IP: net.ParseIP("198.51.100.7"), Port: 5555})
	})
	if status != fasthttp.StatusOK {
		t.Fatalf("status %d, %v", status, resp)
	}
	if text, isError := toolCallResult(t, resp); isError || !strings.Contains(text, "v-test") {
		t.Fatalf("tool result %q (error %v)", text, isError)
	}
	for header, want := range map[string]string{
		"Authorization":   "Bearer bfst-test",
		SetupTokenHeader:  "s3cret",
		"X-Forwarded-For": "203.0.113.9",
	} {
		if got := string(seen.Header.Peek(header)); got != want {
			t.Errorf("/api saw %s %q, want %q", header, got, want)
		}
	}
	if got := string(seen.Header.Host()); got != "gw.example.com" {
		t.Errorf("/api saw Host %q, want the caller's", got)
	}
	if got := string(seen.URI().Path()); got != "/api/version" {
		t.Errorf("/api saw path %q", got)
	}
	if cookie := seen.Header.Peek("Cookie"); len(cookie) > 0 {
		t.Errorf("/api saw Cookie %q, want none", cookie)
	}
	if seenRemote != "198.51.100.7" {
		t.Errorf("/api saw peer %s, want the caller's", seenRemote)
	}
}

// The in-process server reads headers with the main server's buffer, so a call
// whose headers fit the configured read_buffer_size but not fasthttp's 4 KiB
// default reaches /api instead of failing with 431.
func TestManagementMCPExecutor_UsesConfiguredReadBufferSize(t *testing.T) {
	SetLogger(&mockLogger{})
	var seenAuth string
	exec := NewManagementMCPExecutor(func(ctx *fasthttp.RequestCtx) {
		seenAuth = string(ctx.Request.Header.Peek("Authorization"))
		ctx.SetBodyString("ok")
	}, 4<<20, 64<<10)
	t.Cleanup(func() { _ = exec.Close() })
	auth := "Bearer " + strings.Repeat("a", 8<<10)
	req, err := http.NewRequest(http.MethodGet, managementMCPBaseURL+"/api/version", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", auth)
	resp, err := exec.Client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200 for headers within the configured read buffer", resp.StatusCode)
	}
	if seenAuth != auth {
		t.Errorf("/api saw an Authorization of %d bytes, want %d", len(seenAuth), len(auth))
	}
}

// Tool calls are authenticated by the real OSS /api chain (setup lock, then
// APIMiddleware), with the caller's credential and nothing else.
func TestManagementMCPEndpoint_RealAPIChain(t *testing.T) {
	SetLogger(&mockLogger{})
	am := newSetupLockedAuthMiddleware("s3cret", nil)
	r := router.New()
	r.GET("/api/providers", am.SetupLockMiddleware()(am.APIMiddleware()(func(ctx *fasthttp.RequestCtx) {
		ctx.SetContentType("application/json")
		ctx.SetBodyString(`{"providers":[],"total":0}`)
	})))
	h := newManagementTestEndpoint(t, r.Handler)
	session, _, err := am.IssueSetupSession(time.Now())
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name      string
		prepare   func(*fasthttp.RequestCtx)
		wantError string // "" = the call succeeds
	}{
		{name: "no credential", wantError: "HTTP 401"},
		{name: "wrong setup token", prepare: func(ctx *fasthttp.RequestCtx) { ctx.Request.Header.Set(SetupTokenHeader, "nope") }, wantError: "HTTP 403"},
		{name: "setup token", prepare: func(ctx *fasthttp.RequestCtx) { ctx.Request.Header.Set(SetupTokenHeader, "s3cret") }},
		// The same cookie authenticates a direct /api call; through /v1/mcp it is dropped.
		{name: "setup session cookie", prepare: func(ctx *fasthttp.RequestCtx) { ctx.Request.Header.SetCookie(SetupSessionCookie, session) }, wantError: "HTTP 401"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, resp := postManagementMCP(t, h, callManagementTool("listProviders"), tc.prepare)
			if status != fasthttp.StatusOK {
				t.Fatalf("status %d, %v", status, resp)
			}
			text, isError := toolCallResult(t, resp)
			if tc.wantError == "" && (isError || !strings.Contains(text, `"providers"`)) {
				t.Errorf("want success, got %q (error %v)", text, isError)
			}
			if tc.wantError != "" && (!isError || !strings.HasPrefix(text, tc.wantError)) {
				t.Errorf("want %s, got %q (error %v)", tc.wantError, text, isError)
			}
		})
	}

	direct := &fasthttp.RequestCtx{}
	direct.Request.SetRequestURI("/api/providers")
	direct.Request.Header.SetCookie(SetupSessionCookie, session)
	if !runOSSAPIChain(am, direct) {
		t.Fatalf("the setup session cookie should authenticate a direct /api call (status %d)", direct.Response.StatusCode())
	}
}

// A virtual key is refused by /v1/mcp itself with a message saying why, in each
// place a client can send one.
func TestManagementMCPEndpoint_RefusesVirtualKeys(t *testing.T) {
	reached := false
	h := newManagementTestEndpoint(t, func(ctx *fasthttp.RequestCtx) { reached = true })
	for name, set := range map[string]func(*fasthttp.Request){
		"bearer":  func(req *fasthttp.Request) { req.Header.Set("Authorization", "Bearer sk-bf-abc") },
		"x-bf-vk": func(req *fasthttp.Request) { req.Header.Set(string(schemas.BifrostContextKeyVirtualKey), "sk-bf-abc") },
	} {
		status, resp := postManagementMCP(t, h, callManagementTool("listProviders"), func(ctx *fasthttp.RequestCtx) { set(&ctx.Request) })
		if status != fasthttp.StatusUnauthorized || !strings.Contains(fmt.Sprint(resp), "not a virtual key") {
			t.Errorf("%s: status %d, %v; want 401 naming the virtual key", name, status, resp)
		}
	}
	if reached {
		t.Error("a virtual-key call reached /api")
	}
	status, _ := postManagementMCP(t, h, callManagementTool("getVersion"), func(ctx *fasthttp.RequestCtx) {
		ctx.Request.Header.Set("Authorization", "Bearer bfst-not-a-vk")
	})
	if status != fasthttp.StatusOK {
		t.Errorf("a non-virtual-key bearer was refused: status %d", status)
	}
}

// BenchmarkManagementMCPToolCall measures one tools/call through /v1/mcp: the
// JSON-RPC handling, request building and the in-process hop into /api, against
// a root handler that answers at once.
func BenchmarkManagementMCPToolCall(b *testing.B) {
	SetLogger(&mockLogger{})
	spec, err := filepath.Abs(repoOpenAPISpec)
	if err != nil {
		b.Fatal(err)
	}
	exec := NewManagementMCPExecutor(func(ctx *fasthttp.RequestCtx) {
		ctx.SetContentType("application/json")
		ctx.SetBodyString(`"v-bench"`)
	}, 4<<20, 64<<10)
	defer exec.Close()
	m := NewManagementMCP(ManagementMCPOptions{SpecSources: []string{spec}, ExecClient: exec.Client})
	if err := m.Load(context.Background()); err != nil {
		b.Fatal(err)
	}
	h := NewManagementMCPHandler(m)
	body := []byte(callManagementTool("getVersion"))
	b.ReportAllocs()
	for b.Loop() {
		var req fasthttp.Request
		req.Header.SetMethod(fasthttp.MethodPost)
		req.Header.Set(SetupTokenHeader, "s3cret")
		req.SetBody(body)
		ctx := initCtx(&req)
		h.handlePost(ctx)
		if ctx.Response.StatusCode() != fasthttp.StatusOK {
			b.Fatalf("status %d", ctx.Response.StatusCode())
		}
	}
}

// ManagementMCPServedRoutes matches spec paths to a router's route templates by
// shape, not by parameter name, per method, and a catch-all route does not make
// every path count as served.
func TestManagementMCPServedRoutes(t *testing.T) {
	r := router.New()
	noop := func(*fasthttp.RequestCtx) {}
	r.GET("/api/providers", noop)
	r.GET("/api/providers/{provider}", noop)
	r.DELETE("/api/governance/virtual-keys/{id}", noop)
	r.GET("/api/skills/serve/{skill-name}/files/{filepath:*}", noop)
	r.GET("/{filepath:*}", noop) // the dashboard
	served := ManagementMCPServedRoutes(r.List())
	r.GET("/api/registered-later", noop) // the snapshot does not change

	cases := []struct {
		method, path string
		want         bool
	}{
		{http.MethodGet, "/api/providers", true},
		{http.MethodGet, "/api/providers/{provider_name}", true},
		{http.MethodDelete, "/api/governance/virtual-keys/{vk_id}", true},
		{http.MethodGet, "/api/governance/virtual-keys/{vk_id}", false},
		{http.MethodGet, "/api/skills/serve/{skill-name}/files/{filepath}", true},
		{http.MethodGet, "/api/governance/users", false},
		{http.MethodGet, "/api/registered-later", false},
	}
	for _, tc := range cases {
		if got := served(tc.method, tc.path); got != tc.want {
			t.Errorf("served(%s %s) = %v, want %v", tc.method, tc.path, got, tc.want)
		}
	}
}

// With a Served filter from the gateway's router, only operations the gateway
// routes become tools.
func TestManagementMCPServesOnlyRoutedOperations(t *testing.T) {
	r := router.New()
	r.GET("/api/providers", func(*fasthttp.RequestCtx) {})
	opts := managementTestOptions(true)
	opts.Served = ManagementMCPServedRoutes(r.List())
	srv, report, err := buildManagementMCPServer(readRepoSpec(t), "", opts)
	if err != nil {
		t.Fatalf("building server: %v", err)
	}
	tools := srv.ListTools()
	if len(tools) != 1 || tools["listProviders"] == nil {
		names := make([]string, 0, len(tools))
		for name := range tools {
			names = append(names, name)
		}
		t.Fatalf("served %v, want only listProviders", names)
	}
	if report.unrouted == 0 {
		t.Error("unrouted operations were not counted")
	}
}
