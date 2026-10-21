package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/framework/openapimcp"
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
