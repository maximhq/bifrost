package handlers

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fasthttp/router"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/openapimcp"
	"github.com/valyala/fasthttp"
)

// repoOpenAPISpec is the spec this source tree publishes. A gateway fetches it
// from the dev branch unless BIFROST_MCP_SPEC names another source.
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

func bifrostMCPTestOptions(allowWrites bool) BifrostMCPOptions {
	return BifrostMCPOptions{AllowWrites: allowWrites, ExecClient: http.DefaultClient}
}

// listBifrostMCPTools returns the tools a built server answers tools/list with.
func listBifrostMCPTools(t *testing.T, opts BifrostMCPOptions) map[string]mcp.Tool {
	t.Helper()
	srv, _, err := buildBifrostMCPServer(readRepoSpec(t), "", opts)
	if err != nil {
		t.Fatalf("building server: %v", err)
	}
	tools := map[string]mcp.Tool{}
	for name, st := range srv.ListTools() {
		tools[name] = st.Tool
	}
	return tools
}

// Every current operation in the spec becomes a tool unless it is denied. An
// operation the synthesizer cannot serve must be a file upload; anything else
// fails here by name, so a new endpoint that silently drops out of the MCP is
// caught in CI.
func TestBifrostMCPExposesEveryOperation(t *testing.T) {
	_, report, err := buildBifrostMCPServer(readRepoSpec(t), "", bifrostMCPTestOptions(true))
	if err != nil {
		t.Fatalf("building server: %v", err)
	}
	knownUnsupported := map[string]bool{}
	for _, op := range report.unsupported {
		knownUnsupported[op.OperationID] = true
		if !strings.Contains(op.Reason, "multipart/binary") {
			t.Errorf("operation %s (%s %s) cannot become a tool: %s", op.OperationID, op.Method, op.Path, op.Reason)
		}
	}
	tools := listBifrostMCPTools(t, bifrostMCPTestOptions(true))
	for _, op := range repoSpecOperations(t) {
		if op.Deprecated || bifrostMCPDenied[op.ID] || knownUnsupported[op.ID] || bifrostMCPEnterpriseOnly(op) {
			continue
		}
		if _, ok := tools[op.ID]; !ok {
			t.Errorf("operation %s (%s %s) is not served", op.ID, op.Method, op.Path)
		}
	}
	if _, ok := tools["createChatCompletion"]; !ok {
		t.Error("the inference route is not a tool")
	}
}

// repoSpecSources are the YAML files openapi.json is bundled from. CI rebundles
// after merge, so an operation added in a PR is in these before it is in the bundle.
const repoSpecSources = "../../../docs/openapi/paths"

// Every deny-list entry names an operation the spec sources define, and none is
// served.
func TestBifrostMCPDenyListMatchesSpec(t *testing.T) {
	defined := map[string]bool{}
	err := filepath.WalkDir(repoSpecSources, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".yaml" {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range regexp.MustCompile(`(?m)^\s*operationId:\s*["']?([A-Za-z0-9_]+)`).FindAllSubmatch(data, -1) {
			defined[string(m[1])] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("reading %s: %v", repoSpecSources, err)
	}
	var unknown []string
	for id := range bifrostMCPDenied {
		if !defined[id] {
			unknown = append(unknown, id)
		}
	}
	sort.Strings(unknown)
	if len(unknown) > 0 {
		t.Errorf("denied operationIds missing from %s: %v", repoSpecSources, unknown)
	}
	tools := listBifrostMCPTools(t, bifrostMCPTestOptions(true))
	for id := range bifrostMCPDenied {
		if _, ok := tools[id]; ok {
			t.Errorf("denied operation %s is served", id)
		}
	}
}

// Every route-deny entry names an operation the spec defines, and the two deny
// lists name the same operations: each denied route's operation is a denied
// operationId, and each denied operationId's route is denied.
func TestBifrostMCPDeniedRoutesMatchSpec(t *testing.T) {
	doc, err := openapimcp.Parse(readRepoSpec(t), openapimcp.ParseOptions{})
	if err != nil {
		t.Fatalf("parsing %s: %v", repoOpenAPISpec, err)
	}
	matched := map[string]bool{}
	for _, op := range doc.Operations {
		key := bifrostMCPRouteKey(op.Method, op.Path)
		if bifrostMCPDeniedRoutes[key] {
			matched[key] = true
			if !bifrostMCPDenied[op.ID] {
				t.Errorf("denied route %s is operation %s, which bifrostMCPDenied does not list", key, op.ID)
			}
		} else if bifrostMCPDenied[op.ID] {
			t.Errorf("denied operation %s (%s %s) has no entry in bifrostMCPDeniedRoutes", op.ID, op.Method, op.Path)
		}
	}
	for key := range bifrostMCPDeniedRoutes {
		if !matched[key] {
			t.Errorf("denied route %s matches no operation in %s", key, repoOpenAPISpec)
		}
	}
}

func TestBifrostMCPCuration(t *testing.T) {
	enterprise := map[string]any{"x-mint": map[string]any{"metadata": map[string]any{"tag": "Enterprise"}}}
	ops := []openapimcp.Operation{
		{ID: "listProviders", Method: http.MethodGet, Path: "/api/providers"},
		{ID: "addProvider", Method: http.MethodPost, Path: "/api/providers"},
		{ID: "chatCompletion", Method: http.MethodPost, Path: "/v1/chat/completions"},
		{ID: "login", Method: http.MethodPost, Path: "/api/session/login"},
		{ID: "listTeamsLegacy", Method: http.MethodGet, Path: "/api/teams", Deprecated: true},
		{ID: "listUsers", Method: http.MethodGet, Path: "/api/governance/users", Extensions: enterprise},
		// A newer spec renaming a denied operation, or its path parameter, keeps
		// it out: the route is denied as well as the operationId.
		{ID: "signIn", Method: http.MethodPost, Path: "/api/session/login"},
		{ID: "readOauthFlow", Method: http.MethodGet, Path: "/api/oauth/per-user/flows/{flow_id}"},
	}
	ids := func(kept []openapimcp.Operation) []string {
		var out []string
		for _, op := range kept {
			out = append(out, op.ID)
		}
		return out
	}
	kept, _, unknown := curateBifrostMCPOperations(ops, BifrostMCPOptions{AllowWrites: true})
	if got := ids(kept); strings.Join(got, ",") != "listProviders,addProvider,chatCompletion" {
		t.Errorf("OSS, writes on kept %v, want listProviders, addProvider and chatCompletion", got)
	}
	if len(unknown) != len(bifrostMCPDenied)-1 {
		t.Errorf("unknown = %d entries, want every denied id but login", len(unknown))
	}
	// Writes gate /api only: with them off the inference POST stays, the /api POST goes.
	kept, _, _ = curateBifrostMCPOperations(ops, BifrostMCPOptions{})
	if got := ids(kept); strings.Join(got, ",") != "listProviders,chatCompletion" {
		t.Errorf("OSS, writes off kept %v, want listProviders and chatCompletion", got)
	}
	kept, _, _ = curateBifrostMCPOperations(ops, BifrostMCPOptions{AllowWrites: true, Edition: BifrostMCPEditionEnterprise})
	if got := ids(kept); strings.Join(got, ",") != "listProviders,addProvider,chatCompletion,listUsers" {
		t.Errorf("enterprise kept %v, want the enterprise-only listUsers too", got)
	}
	// An operation the gateway does not route is left out and counted.
	served := func(method, path string) bool { return method == http.MethodGet }
	kept, unrouted, _ := curateBifrostMCPOperations(ops, BifrostMCPOptions{AllowWrites: true, Served: served})
	if got := ids(kept); strings.Join(got, ",") != "listProviders" || unrouted != 2 {
		t.Errorf("served filter kept %v (unrouted %d), want listProviders with addProvider and chatCompletion unrouted", got, unrouted)
	}
}

// Bifrost's own spec writes its metadata filters as metadata_<key>, so the
// Bifrost MCP expands them: getLogs takes metadata as an object of key to value.
func TestBifrostMCPExpandsMetadataTemplates(t *testing.T) {
	getLogs, ok := listBifrostMCPTools(t, bifrostMCPTestOptions(true))["getLogs"]
	if !ok {
		t.Fatal("getLogs is not a tool")
	}
	schema := string(getLogs.RawInputSchema)
	if strings.Contains(schema, `"metadata__key_"`) || !strings.Contains(schema, `"metadata":{`) {
		t.Fatalf("metadata_<key> was not expanded into a metadata object argument: %s", schema)
	}
	if !strings.Contains(schema, `"type":"object"`) {
		t.Errorf("metadata is not an object argument: %s", schema)
	}
}

// An OSS gateway serves none of the spec's enterprise-only operations, whichever
// spec it read; an enterprise gateway serves them.
func TestBifrostMCPEditions(t *testing.T) {
	oss := listBifrostMCPTools(t, bifrostMCPTestOptions(true))
	if _, ok := oss["listUsers"]; ok {
		t.Error("OSS serves the enterprise-only listUsers")
	}
	if _, ok := oss["listProviders"]; !ok {
		t.Error("OSS lacks listProviders")
	}
	opts := bifrostMCPTestOptions(true)
	opts.Edition = BifrostMCPEditionEnterprise
	if _, ok := listBifrostMCPTools(t, opts)["listUsers"]; !ok {
		t.Error("enterprise lacks listUsers")
	}
}

// The writes switch gates the gateway's own /api mutations. Inference and the
// other routes outside /api are served either way: the caller's credential
// authorizes them, as it would a direct call.
func TestBifrostMCPWritesSwitch(t *testing.T) {
	readOnly := listBifrostMCPTools(t, bifrostMCPTestOptions(false))
	ops := map[string]openapimcp.Operation{}
	for _, op := range repoSpecOperations(t) {
		ops[op.ID] = op
	}
	for name, tool := range readOnly {
		if !strings.HasPrefix(ops[name].Path, "/api/") {
			continue
		}
		if tool.Annotations.ReadOnlyHint == nil || !*tool.Annotations.ReadOnlyHint {
			t.Errorf("writes off: /api tool %s is served but not read-only", name)
		}
	}
	if _, ok := readOnly["createChatCompletion"]; !ok {
		t.Error("writes off: createChatCompletion missing; inference is not a write")
	}
	if _, ok := readOnly["listProviders"]; !ok {
		t.Error("writes off: listProviders missing")
	}
	if _, ok := readOnly["createVirtualKey"]; ok {
		t.Error("writes off: createVirtualKey is served")
	}

	withWrites := listBifrostMCPTools(t, bifrostMCPTestOptions(true))
	if _, ok := withWrites["createVirtualKey"]; !ok {
		t.Error("writes on: createVirtualKey missing")
	}
	if len(withWrites) <= len(readOnly) {
		t.Errorf("writes on serves %d tools, writes off %d", len(withWrites), len(readOnly))
	}
}

func TestBifrostMCPAnnotations(t *testing.T) {
	tools := listBifrostMCPTools(t, bifrostMCPTestOptions(true))
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
func TestBifrostMCPDestructiveMarker(t *testing.T) {
	cases := []struct {
		method string
		ext    map[string]any
		want   bool
	}{
		{http.MethodPost, nil, false},
		{http.MethodPost, map[string]any{bifrostMCPDestructiveExt: true}, true},
		{http.MethodPut, map[string]any{bifrostMCPDestructiveExt: true}, true},
		{http.MethodPut, map[string]any{bifrostMCPDestructiveExt: false}, false},
		{http.MethodPost, map[string]any{bifrostMCPDestructiveExt: "yes"}, false},
		{http.MethodDelete, nil, true},
	}
	for _, tc := range cases {
		tool := openapimcp.Tool{Operation: openapimcp.Operation{ID: "op", Method: tc.method, Extensions: tc.ext}}
		annotateBifrostMCPTool(&tool)
		if got := tool.Annotations.DestructiveHint; got == nil || *got != tc.want {
			t.Errorf("%s %v: DestructiveHint = %v, want %v", tc.method, tc.ext, got, tc.want)
		}
	}
}

// A failed load leaves the server unset (the endpoint answers 503); a later
// successful load publishes it.
func TestBifrostMCPLoadFailureThenRecovery(t *testing.T) {
	spec := readRepoSpec(t)
	var healthy atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !healthy.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(spec)
	}))
	defer upstream.Close()

	m := NewBifrostMCP(BifrostMCPOptions{
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
	healthy.Store(true)
	if err := m.Load(context.Background()); err != nil {
		t.Fatalf("load after recovery: %v", err)
	}
	if m.Server() == nil {
		t.Fatal("server not published after a successful load")
	}
}

func TestBifrostMCPLoadFromFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "openapi.json"), readRepoSpec(t), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, opts := range map[string]BifrostMCPOptions{
		"relative to app dir": {SpecSources: []string{"openapi.json"}, AppDir: dir, ExecClient: http.DefaultClient},
		"absolute":            {SpecSources: []string{filepath.Join(dir, "openapi.json")}, ExecClient: http.DefaultClient},
	} {
		m := NewBifrostMCP(opts)
		if err := m.Load(context.Background()); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestBifrostMCPSpecSources(t *testing.T) {
	t.Setenv(BifrostMCPSpecEnv, "")
	suffixes := func(sources []string) []string {
		var out []string
		for _, s := range sources {
			out = append(out, s[strings.Index(s, "/refs/"):])
		}
		return out
	}
	// Every build reads dev's spec, whatever its version: a version can look like
	// a release with no spec behind it (enterprise's dev build stamps v1.2.3), and
	// only operations the gateway routes become tools, so dev's newer spec cannot
	// expose an operation this build lacks.
	cases := []struct {
		edition BifrostMCPEdition
		want    []string
	}{
		{BifrostMCPEditionOSS, []string{"/refs/heads/dev/docs/openapi/openapi-oss.json", "/refs/heads/dev/docs/openapi/openapi.json"}},
		{BifrostMCPEditionEnterprise, []string{"/refs/heads/dev/docs/openapi/openapi.json"}},
	}
	for _, tc := range cases {
		if got := suffixes(BifrostMCPSpecSources(tc.edition)); strings.Join(got, " ") != strings.Join(tc.want, " ") {
			t.Errorf("%s reads %v, want %v", tc.edition, got, tc.want)
		}
	}
	t.Setenv(BifrostMCPSpecEnv, "/etc/bifrost/openapi.json")
	if got := BifrostMCPSpecSources(BifrostMCPEditionOSS); len(got) != 1 || got[0] != "/etc/bifrost/openapi.json" {
		t.Errorf("override ignored: got %v", got)
	}
}

// A missing OSS spec (a release cut before CI published it) falls back to the
// full one; its enterprise-only operations are still left out.
func TestBifrostMCPSpecFallback(t *testing.T) {
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

	m := NewBifrostMCP(BifrostMCPOptions{
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

	none := NewBifrostMCP(BifrostMCPOptions{SpecSources: []string{upstream.URL + "/a.json", upstream.URL + "/b.json"}, SpecClient: upstream.Client(), ExecClient: http.DefaultClient})
	err := none.Load(context.Background())
	if err == nil || !strings.Contains(err.Error(), "/a.json") || !strings.Contains(err.Error(), "/b.json") {
		t.Errorf("all sources failing: %v, want both named", err)
	}
}

func TestBifrostMCPAllowWrites(t *testing.T) {
	// Writes are on unless turned off: /api still authorizes every call with the
	// caller's own credential, and clients confirm what destructiveHint marks.
	t.Setenv(BifrostMCPWritesEnv, "")
	if !BifrostMCPAllowWrites() {
		t.Error("unset: writes are off, want on by default")
	}
	t.Setenv(BifrostMCPWritesEnv, "true")
	if !BifrostMCPAllowWrites() {
		t.Error("true not honoured")
	}
	t.Setenv(BifrostMCPWritesEnv, "false")
	if BifrostMCPAllowWrites() {
		t.Error("false not honoured")
	}
}

// newBifrostMCPTestEndpoint loads the repo spec into a Bifrost MCP whose tool
// calls are served in process by root, and returns its /v1/mcp handler.
func newBifrostMCPTestEndpoint(t *testing.T, root fasthttp.RequestHandler) *BifrostMCPHandler {
	t.Helper()
	SetLogger(&mockLogger{})
	spec, err := filepath.Abs(repoOpenAPISpec)
	if err != nil {
		t.Fatal(err)
	}
	exec := NewBifrostMCPExecutor(root, 4<<20, 64<<10)
	t.Cleanup(func() { _ = exec.Close() })
	m := NewBifrostMCP(BifrostMCPOptions{SpecSources: []string{spec}, AllowWrites: true, ExecClient: exec.Client})
	if err := m.Load(context.Background()); err != nil {
		t.Fatalf("loading spec: %v", err)
	}
	return NewBifrostMCPHandler(m)
}

// postBifrostMCP sends one JSON-RPC message to /v1/mcp and returns the
// status and the decoded response (nil for an empty body).
func postBifrostMCP(t *testing.T, h *BifrostMCPHandler, body string, prepare func(*fasthttp.RequestCtx)) (int, map[string]any) {
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

func callBifrostMCPTool(name string) string {
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

func TestBifrostMCPEndpoint_Protocol(t *testing.T) {
	h := newBifrostMCPTestEndpoint(t, func(ctx *fasthttp.RequestCtx) {})

	status, resp := postBifrostMCP(t, h, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`, nil)
	if status != fasthttp.StatusOK || resp["result"] == nil {
		t.Fatalf("initialize: status %d, %v", status, resp)
	}

	status, resp = postBifrostMCP(t, h, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, nil)
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

	status, resp = postBifrostMCP(t, h, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, nil)
	if status != fasthttp.StatusAccepted || resp != nil {
		t.Errorf("notification: status %d, %v; want 202 with no body", status, resp)
	}
}

// /v1/mcp answers POST only, and GET is answered by /v1/mcp itself rather than
// the dashboard's catch-all. /v1/mcp/tool/execute keeps its own route.
func TestBifrostMCPEndpoint_Routes(t *testing.T) {
	h := newBifrostMCPTestEndpoint(t, func(ctx *fasthttp.RequestCtx) {})
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

func TestBifrostMCPEndpoint_UnavailableUntilLoaded(t *testing.T) {
	SetLogger(&mockLogger{})
	h := NewBifrostMCPHandler(NewBifrostMCP(BifrostMCPOptions{}))
	status, resp := postBifrostMCP(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, nil)
	if status != fasthttp.StatusServiceUnavailable {
		t.Errorf("status %d, %v; want 503", status, resp)
	}
}

// A tool call carries the caller's credential, Host, forwarding headers and peer
// address into /api, and never a Cookie: a browser session cannot be ridden
// through /v1/mcp.
func TestBifrostMCPEndpoint_ForwardsCallerButNotCookies(t *testing.T) {
	var seen fasthttp.Request
	var seenRemote string
	h := newBifrostMCPTestEndpoint(t, func(ctx *fasthttp.RequestCtx) {
		ctx.Request.CopyTo(&seen)
		seenRemote = ctx.RemoteIP().String()
		ctx.SetContentType("application/json")
		ctx.SetBodyString(`"v-test"`)
	})
	status, resp := postBifrostMCP(t, h, callBifrostMCPTool("getVersion"), func(ctx *fasthttp.RequestCtx) {
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
func TestBifrostMCPExecutor_UsesConfiguredReadBufferSize(t *testing.T) {
	SetLogger(&mockLogger{})
	var seenAuth string
	exec := NewBifrostMCPExecutor(func(ctx *fasthttp.RequestCtx) {
		seenAuth = string(ctx.Request.Header.Peek("Authorization"))
		ctx.SetBodyString("ok")
	}, 4<<20, 64<<10)
	t.Cleanup(func() { _ = exec.Close() })
	auth := "Bearer " + strings.Repeat("a", 8<<10)
	req, err := http.NewRequest(http.MethodGet, bifrostMCPBaseURL+"/api/version", nil)
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

// Close reports the server's shutdown, not the listener close that follows it:
// a clean shutdown has already closed the listener, which is not an error.
func TestBifrostMCPExecutor_CloseAfterServing(t *testing.T) {
	SetLogger(&mockLogger{})
	exec := NewBifrostMCPExecutor(func(ctx *fasthttp.RequestCtx) { ctx.SetBodyString("ok") }, 4<<20, 64<<10)
	resp, err := exec.Client.Get(bifrostMCPBaseURL + "/api/version")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if err := exec.Close(); err != nil {
		t.Errorf("Close after a clean shutdown = %v, want nil", err)
	}
}

// Shutdown is bounded by its context: a handler that never returns cannot hold
// the gateway's shutdown open.
func TestBifrostMCPExecutor_ShutdownBoundedByContext(t *testing.T) {
	SetLogger(&mockLogger{})
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	exec := NewBifrostMCPExecutor(func(ctx *fasthttp.RequestCtx) {
		close(entered)
		<-release
	}, 4<<20, 64<<10)
	go func() {
		if resp, err := exec.Client.Get(bifrostMCPBaseURL + "/api/version"); err == nil {
			resp.Body.Close()
		}
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- exec.Shutdown(ctx) }()
	select {
	case err := <-done:
		if err != context.DeadlineExceeded {
			t.Errorf("Shutdown with a blocked handler = %v, want %v", err, context.DeadlineExceeded)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not return after its context expired")
	}
}

// Tool calls are authenticated by the real OSS /api chain (setup lock, then
// APIMiddleware), with the caller's credential and nothing else.
func TestBifrostMCPEndpoint_RealAPIChain(t *testing.T) {
	SetLogger(&mockLogger{})
	am := newSetupLockedAuthMiddleware("s3cret", nil)
	r := router.New()
	r.GET("/api/providers", am.SetupLockMiddleware()(am.APIMiddleware()(func(ctx *fasthttp.RequestCtx) {
		ctx.SetContentType("application/json")
		ctx.SetBodyString(`{"providers":[],"total":0}`)
	})))
	h := newBifrostMCPTestEndpoint(t, r.Handler)
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
			status, resp := postBifrostMCP(t, h, callBifrostMCPTool("listProviders"), tc.prepare)
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

// A virtual key reaches the route a tool calls, in each place a client can send
// one, and that route decides: inference takes it, /api refuses it.
func TestBifrostMCPEndpoint_ForwardsVirtualKeys(t *testing.T) {
	for _, header := range []string{"Authorization", string(schemas.BifrostContextKeyVirtualKey), "x-api-key", "x-goog-api-key", "api-key"} {
		t.Run(header, func(t *testing.T) {
			var seen, seenPath, seenBody string
			h := newBifrostMCPTestEndpoint(t, func(ctx *fasthttp.RequestCtx) {
				seen, seenPath, seenBody = string(ctx.Request.Header.Peek(header)), string(ctx.Path()), string(ctx.PostBody())
				ctx.SetContentType("application/json")
				ctx.SetBodyString(`{"id":"chatcmpl-1"}`)
			})
			call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"createChatCompletion","arguments":{"model":"openai/gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}}}`
			status, resp := postBifrostMCP(t, h, call, func(ctx *fasthttp.RequestCtx) { ctx.Request.Header.Set(header, "sk-bf-abc") })
			if status != fasthttp.StatusOK {
				t.Fatalf("status %d, %v", status, resp)
			}
			if text, isError := toolCallResult(t, resp); isError || !strings.Contains(text, "chatcmpl-1") {
				t.Fatalf("tool result %q (error %v)", text, isError)
			}
			if !strings.Contains(seen, "sk-bf-abc") {
				t.Errorf("the route saw %s %q, want the virtual key", header, seen)
			}
			if seenPath != "/v1/chat/completions" || !strings.Contains(seenBody, "gpt-4o-mini") {
				t.Errorf("the route saw %s with body %q", seenPath, seenBody)
			}
		})
	}
}

// Routes beyond /api are open-world: they reach providers and agents, not only
// this gateway. WebSocket, SSE-only and MCP server routes are never tools.
func TestBifrostMCPInferenceTools(t *testing.T) {
	tools := listBifrostMCPTools(t, bifrostMCPTestOptions(true))
	for name, want := range map[string]bool{"createChatCompletion": true, "listModels": true, "listProviders": false} {
		tool, ok := tools[name]
		if !ok {
			t.Errorf("%s is not a tool", name)
			continue
		}
		if hint := tool.Annotations.OpenWorldHint; hint == nil || *hint != want {
			t.Errorf("%s openWorldHint = %v, want %v", name, hint, want)
		}
	}
	for _, name := range []string{"websocketConnect", "connectRealtime", "mcpServerSSE", "mcpServerMessage", "geminiStreamGenerateContent"} {
		if _, ok := tools[name]; ok {
			t.Errorf("%s is a tool", name)
		}
	}
	// Writes off gates /api mutations only: inference is served, createVirtualKey is not.
	readOnly := listBifrostMCPTools(t, bifrostMCPTestOptions(false))
	if _, ok := readOnly["createChatCompletion"]; !ok {
		t.Error("writes off: createChatCompletion is missing")
	}
	if _, ok := readOnly["createVirtualKey"]; ok {
		t.Error("writes off: createVirtualKey is served")
	}
	if _, ok := readOnly["listModels"]; !ok {
		t.Error("writes off: listModels is missing")
	}
}

// BenchmarkBifrostMCPToolCall measures one tools/call through /v1/mcp: the
// JSON-RPC handling, request building and the in-process hop into /api, against
// a root handler that answers at once.
func BenchmarkBifrostMCPToolCall(b *testing.B) {
	SetLogger(&mockLogger{})
	spec, err := filepath.Abs(repoOpenAPISpec)
	if err != nil {
		b.Fatal(err)
	}
	exec := NewBifrostMCPExecutor(func(ctx *fasthttp.RequestCtx) {
		ctx.SetContentType("application/json")
		ctx.SetBodyString(`"v-bench"`)
	}, 4<<20, 64<<10)
	defer exec.Close()
	m := NewBifrostMCP(BifrostMCPOptions{SpecSources: []string{spec}, ExecClient: exec.Client})
	if err := m.Load(context.Background()); err != nil {
		b.Fatal(err)
	}
	h := NewBifrostMCPHandler(m)
	body := []byte(callBifrostMCPTool("getVersion"))
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

// BifrostMCPServedRoutes matches spec paths to a router's route templates by
// shape, not by parameter name, per method, and a catch-all route does not make
// every path count as served.
func TestBifrostMCPServedRoutes(t *testing.T) {
	r := router.New()
	noop := func(*fasthttp.RequestCtx) {}
	r.GET("/api/providers", noop)
	r.GET("/api/providers/{provider}", noop)
	r.DELETE("/api/governance/virtual-keys/{id}", noop)
	r.GET("/api/skills/serve/{skill-name}/files/{filepath:*}", noop)
	r.POST("/genai/v1beta/models/{model:*}", noop)
	r.GET("/{filepath:*}", noop) // the dashboard
	served := BifrostMCPServedRoutes(r.List())
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
		{http.MethodPost, "/genai/v1beta/models/{model}:generateContent", true}, // under a catch-all
		{http.MethodGet, "/genai/v1beta/models/{model}:generateContent", false}, // per method
		{http.MethodPost, "/genai/v1beta/files", false},
	}
	for _, tc := range cases {
		if got := served(tc.method, tc.path); got != tc.want {
			t.Errorf("served(%s %s) = %v, want %v", tc.method, tc.path, got, tc.want)
		}
	}
}

// With a Served filter from the gateway's router, only operations the gateway
// routes become tools.
func TestBifrostMCPServesOnlyRoutedOperations(t *testing.T) {
	r := router.New()
	r.GET("/api/providers", func(*fasthttp.RequestCtx) {})
	opts := bifrostMCPTestOptions(true)
	opts.Served = BifrostMCPServedRoutes(r.List())
	srv, report, err := buildBifrostMCPServer(readRepoSpec(t), "", opts)
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

// toolsListNames runs tools/list through h and returns the tool names it lists.
func toolsListNames(t *testing.T, h *BifrostMCPHandler, prepare func(*fasthttp.RequestCtx)) map[string]bool {
	t.Helper()
	status, resp := postBifrostMCP(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, prepare)
	if status != fasthttp.StatusOK {
		t.Fatalf("tools/list status %d: %v", status, resp)
	}
	result, _ := resp["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	names := map[string]bool{}
	for _, tool := range tools {
		if name, ok := tool.(map[string]any)["name"].(string); ok {
			names[name] = true
		}
	}
	return names
}

// /v1/mcp takes a management credential, like /api. A request whose only
// credential is a virtual key is refused before the auth chain, with a message
// saying why; a virtual key sent alongside a management credential (so inference
// tools can use it) passes, and a request with no credential is left to the
// chain to refuse.
func TestBifrostMCPVirtualKeyGuard(t *testing.T) {
	reached := false
	guard := bifrostMCPVirtualKeyGuard(func(*fasthttp.RequestCtx) { reached = true })
	run := func(set func(*fasthttp.Request)) (*fasthttp.RequestCtx, bool) {
		reached = false
		ctx := &fasthttp.RequestCtx{}
		if set != nil {
			set(&ctx.Request)
		}
		guard(ctx)
		return ctx, reached
	}
	for name, set := range map[string]func(*fasthttp.Request){
		"x-bf-vk":        func(r *fasthttp.Request) { r.Header.Set("x-bf-vk", "sk-bf-abc") },
		"bearer":         func(r *fasthttp.Request) { r.Header.Set("Authorization", "Bearer sk-bf-abc") },
		"x-api-key":      func(r *fasthttp.Request) { r.Header.Set("x-api-key", "sk-bf-abc") },
		"x-goog-api-key": func(r *fasthttp.Request) { r.Header.Set("x-goog-api-key", "sk-bf-abc") },
	} {
		ctx, passed := run(set)
		if passed || ctx.Response.StatusCode() != fasthttp.StatusUnauthorized || !strings.Contains(string(ctx.Response.Body()), "virtual key") {
			t.Errorf("%s alone: passed=%v status %d body %s; want a 401 naming the virtual key", name, passed, ctx.Response.StatusCode(), ctx.Response.Body())
		}
	}
	for name, set := range map[string]func(*fasthttp.Request){
		"no credential": nil,
		"vk with setup token": func(r *fasthttp.Request) {
			r.Header.Set("x-bf-vk", "sk-bf-abc")
			r.Header.Set(SetupTokenHeader, "s3cret")
		},
		"vk with bearer": func(r *fasthttp.Request) {
			r.Header.Set("x-bf-vk", "sk-bf-abc")
			r.Header.Set("Authorization", "Bearer bfst-abc")
		},
		"vk with basic": func(r *fasthttp.Request) {
			r.Header.Set("x-bf-vk", "sk-bf-abc")
			r.Header.Set("Authorization", "Basic YWRtaW46eA==")
		},
		"vk with a cookie": func(r *fasthttp.Request) {
			r.Header.Set("x-bf-vk", "sk-bf-abc")
			r.Header.SetCookie("token", "session")
		},
	} {
		if _, passed := run(set); !passed {
			t.Errorf("%s: refused, want it left to the auth chain", name)
		}
	}
}

// Both /v1/mcp routes run the chain they are registered with (the /api auth
// chain in a gateway), after the virtual-key refusal.
func TestBifrostMCPEndpoint_RoutesTakeTheAPIChain(t *testing.T) {
	h := newBifrostMCPTestEndpoint(t, func(*fasthttp.RequestCtx) {})
	authChain := func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			if len(ctx.Request.Header.Peek(SetupTokenHeader)) == 0 {
				SendError(ctx, fasthttp.StatusUnauthorized, "auth chain refused")
				return
			}
			next(ctx)
		}
	}
	r := router.New()
	h.RegisterRoutes(r, authChain)
	serve := func(method string, set func(*fasthttp.Request)) *fasthttp.RequestCtx {
		var req fasthttp.Request
		req.Header.SetMethod(method)
		req.SetRequestURI("/v1/mcp")
		req.SetBodyString(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
		if set != nil {
			set(&req)
		}
		ctx := initCtx(&req)
		r.Handler(ctx)
		return ctx
	}
	for _, method := range []string{fasthttp.MethodPost, fasthttp.MethodGet} {
		if ctx := serve(method, nil); ctx.Response.StatusCode() != fasthttp.StatusUnauthorized || !strings.Contains(string(ctx.Response.Body()), "auth chain refused") {
			t.Errorf("%s without a credential: status %d %s, want the chain's 401", method, ctx.Response.StatusCode(), ctx.Response.Body())
		}
	}
	if ctx := serve(fasthttp.MethodPost, func(r *fasthttp.Request) { r.Header.Set("x-bf-vk", "sk-bf-abc") }); !strings.Contains(string(ctx.Response.Body()), "virtual key") {
		t.Errorf("a virtual key alone got %s, want the refusal that names it", ctx.Response.Body())
	}
	if ctx := serve(fasthttp.MethodPost, func(r *fasthttp.Request) { r.Header.Set(SetupTokenHeader, "s3cret") }); ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Errorf("with a credential: status %d %s, want 200", ctx.Response.StatusCode(), ctx.Response.Body())
	}
}

// ToolAccess decides which /api tools a tools/list caller sees, from the
// authenticated request; tools outside /api are always listed, and calls are
// never filtered (each one is authorized by the route it reaches).
func TestBifrostMCPToolsListAsksToolAccess(t *testing.T) {
	SetLogger(&mockLogger{})
	exec := NewBifrostMCPExecutor(func(ctx *fasthttp.RequestCtx) {
		ctx.SetContentType("application/json")
		ctx.SetBodyString(`"v-test"`)
	}, 4<<20, 64<<10)
	t.Cleanup(func() { _ = exec.Close() })
	spec, err := filepath.Abs(repoOpenAPISpec)
	if err != nil {
		t.Fatal(err)
	}
	asked, resolved := 0, 0
	var outside []string
	m := NewBifrostMCP(BifrostMCPOptions{SpecSources: []string{spec}, AllowWrites: true, ExecClient: exec.Client,
		ToolAccess: func(caller *fasthttp.RequestCtx) func(method, path string) bool {
			resolved++
			role, _ := caller.UserValue("test-role").(string)
			if role == "" {
				return nil // undecided: list everything
			}
			return func(method, path string) bool {
				asked++
				if !strings.HasPrefix(path, "/api/") {
					outside = append(outside, method+" "+path)
				}
				return role == "viewer" && method == http.MethodGet && path == "/api/providers"
			}
		}})
	if err := m.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	h := NewBifrostMCPHandler(m)
	viewer := func(ctx *fasthttp.RequestCtx) { ctx.SetUserValue("test-role", "viewer") }

	names := toolsListNames(t, h, viewer)
	if !names["listProviders"] || names["getVersion"] || names["createVirtualKey"] {
		t.Errorf("want only the /api tools ToolAccess allows, got listProviders=%v getVersion=%v createVirtualKey=%v", names["listProviders"], names["getVersion"], names["createVirtualKey"])
	}
	if !names["createChatCompletion"] || !names["listModels"] {
		t.Error("tools outside /api are listed whatever ToolAccess says")
	}
	if len(outside) > 0 {
		t.Errorf("ToolAccess was asked about routes outside /api: %v", outside[:min(3, len(outside))])
	}
	if resolved != 1 {
		t.Errorf("the caller was resolved %d times for one tools/list, want once", resolved)
	}

	before, resolvedBefore := asked, resolved
	status, resp := postBifrostMCP(t, h, callBifrostMCPTool("getVersion"), viewer)
	if status != fasthttp.StatusOK || asked != before || resolved != resolvedBefore {
		t.Errorf("a tools/call consulted ToolAccess (asked %d, resolved %d) or failed with %d", asked-before, resolved-resolvedBefore, status)
	}
	if text, isError := toolCallResult(t, resp); isError || !strings.Contains(text, "v-test") {
		t.Errorf("a call to a tool the list hides still reaches its route: got %q (error %v)", text, isError)
	}

	// An undecided caller (nil from ToolAccess) is shown every tool.
	if all := toolsListNames(t, h, nil); !all["getVersion"] || !all["createVirtualKey"] {
		t.Error("an undecided ToolAccess should list every tool")
	}

	// Without ToolAccess every tool is listed.
	if all := toolsListNames(t, newBifrostMCPTestEndpoint(t, func(*fasthttp.RequestCtx) {}), nil); !all["getVersion"] || !all["createVirtualKey"] {
		t.Error("without ToolAccess, /api tools are missing from the list")
	}
}
