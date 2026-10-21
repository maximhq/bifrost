package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/framework/openapimcp"
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

// Every deny-list entry names an operation the spec defines, and none is served.
func TestBifrostMCPDenyListMatchesSpec(t *testing.T) {
	_, report, err := buildBifrostMCPServer(readRepoSpec(t), "", bifrostMCPTestOptions(true))
	if err != nil {
		t.Fatalf("building server: %v", err)
	}
	sort.Strings(report.unknown)
	if len(report.unknown) > 0 {
		t.Errorf("denied operationIds missing from %s: %v", repoOpenAPISpec, report.unknown)
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
