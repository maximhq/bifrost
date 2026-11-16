package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/framework/openapimcp"
)

// repoOpenAPISpec is the spec this source tree publishes. A gateway fetches it
// from the dev branch unless its settings name another spec URL.
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

// testBifrostMCPBaseURL stands in for the gateway's public base URL setting.
const testBifrostMCPBaseURL = "http://gateway.test:8080"

func bifrostMCPTestOptions(allowWrites bool) BifrostMCPOptions {
	return BifrostMCPOptions{AllowWrites: allowWrites, BaseURL: testBifrostMCPBaseURL, ExecClient: http.DefaultClient}
}

// specServingClient answers every spec fetch with the repo spec and records the
// URLs it was asked for.
func specServingClient(t *testing.T, fetched *[]string) *http.Client {
	t.Helper()
	spec := readRepoSpec(t)
	return &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		*fetched = append(*fetched, req.URL.String())
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(spec))), Header: http.Header{"Content-Type": {"application/json"}}, Request: req}, nil
	})}
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
		if op.Deprecated || bifrostMCPDeniedRoutes[bifrostMCPRouteKey(op.Method, op.Path)] || knownUnsupported[op.ID] {
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

// Every denied route names an operation the spec defines, so a stale entry
// fails here, and none of those operations is served.
func TestBifrostMCPDeniedRoutesMatchSpec(t *testing.T) {
	tools := listBifrostMCPTools(t, bifrostMCPTestOptions(true))
	matched := map[string]bool{}
	for _, op := range repoSpecOperations(t) {
		key := bifrostMCPRouteKey(op.Method, op.Path)
		if !bifrostMCPDeniedRoutes[key] {
			continue
		}
		matched[key] = true
		if _, ok := tools[op.ID]; ok {
			t.Errorf("denied route %s is served as %s", key, op.ID)
		}
	}
	for key := range bifrostMCPDeniedRoutes {
		if !matched[key] {
			t.Errorf("denied route %s matches no operation in %s", key, repoOpenAPISpec)
		}
	}
}

func TestBifrostMCPCuration(t *testing.T) {
	ops := []openapimcp.Operation{
		{ID: "listProviders", Method: http.MethodGet, Path: "/api/providers"},
		{ID: "addProvider", Method: http.MethodPost, Path: "/api/providers"},
		{ID: "chatCompletion", Method: http.MethodPost, Path: "/v1/chat/completions"},
		{ID: "login", Method: http.MethodPost, Path: "/api/session/login"},
		{ID: "listTeamsLegacy", Method: http.MethodGet, Path: "/api/teams", Deprecated: true},
		// A spec renaming a denied operation, or its path parameter, keeps it out:
		// the route is what is denied.
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
	kept, _ := curateBifrostMCPOperations(ops, BifrostMCPOptions{AllowWrites: true})
	if got := ids(kept); strings.Join(got, ",") != "listProviders,addProvider,chatCompletion" {
		t.Errorf("writes on kept %v, want listProviders, addProvider and chatCompletion", got)
	}
	// Writes gate /api only: with them off the inference POST stays, the /api POST goes.
	kept, _ = curateBifrostMCPOperations(ops, BifrostMCPOptions{})
	if got := ids(kept); strings.Join(got, ",") != "listProviders,chatCompletion" {
		t.Errorf("writes off kept %v, want listProviders and chatCompletion", got)
	}
	// An operation the gateway does not route is left out and counted.
	served := func(method, path string) bool { return method == http.MethodGet }
	kept, unrouted := curateBifrostMCPOperations(ops, BifrostMCPOptions{AllowWrites: true, Served: served})
	if got := ids(kept); strings.Join(got, ",") != "listProviders" || unrouted != 2 {
		t.Errorf("served filter kept %v (unrouted %d), want listProviders with addProvider and chatCompletion unrouted", got, unrouted)
	}
}

// A gateway that does not register the enterprise routes serves none of the
// full spec's enterprise-only operations: Served drops them, with no edition or
// tag check of its own.
func TestBifrostMCPUnroutedEnterpriseOperations(t *testing.T) {
	opts := bifrostMCPTestOptions(true)
	opts.Served = func(_, path string) bool { return !strings.HasPrefix(path, "/api/governance/users") }
	tools := listBifrostMCPTools(t, opts)
	if _, ok := tools["listUsers"]; ok {
		t.Error("listUsers is served by a gateway that does not route it")
	}
	if _, ok := tools["listProviders"]; !ok {
		t.Error("listProviders missing")
	}
	if _, ok := listBifrostMCPTools(t, bifrostMCPTestOptions(true))["listUsers"]; !ok {
		t.Error("listUsers missing on a gateway that routes it")
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

// Every x-bifrost-destructive marker in the YAML sources reaches the tools:
// the spec is bundled from the sources with docs/openapi/bundle.py, as CI
// bundles it, and each marked operation must come out with DestructiveHint set.
// TestBifrostMCPDestructiveMarker covers the annotation alone; this covers a
// marker the bundler or the parser drops. Skipped where python3 with PyYAML is
// not installed.
func TestBifrostMCPDestructiveMarkersSurviveBundling(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	// Checked up front: bundle.py reports a missing PyYAML in its own words, and
	// any other bundling failure must fail the test, not skip it.
	if err := exec.Command(python, "-c", "import yaml").Run(); err != nil {
		t.Skip("PyYAML is not installed")
	}
	sources := filepath.Dir(repoOpenAPISpec)
	bundled := filepath.Join(t.TempDir(), "openapi.json")
	cmd := exec.Command(python, "bundle.py", "--output", bundled)
	cmd.Dir = sources
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bundling the spec: %v\n%s", err, out)
	}

	marked := destructiveMarkedOperations(t, filepath.Join(sources, "paths"))
	if len(marked) == 0 {
		t.Fatal("no operation in the YAML sources carries x-bifrost-destructive")
	}
	data, err := os.ReadFile(bundled)
	if err != nil {
		t.Fatal(err)
	}
	srv, _, err := buildBifrostMCPServer(data, "", bifrostMCPTestOptions(true))
	if err != nil {
		t.Fatalf("building server from the bundled spec: %v", err)
	}
	tools := srv.ListTools()
	for _, id := range marked {
		tool, ok := tools[id]
		if !ok {
			t.Errorf("marked operation %s is not served", id)
			continue
		}
		if hint := tool.Tool.Annotations.DestructiveHint; hint == nil || !*hint {
			t.Errorf("marked operation %s is not annotated destructive", id)
		}
	}
}

// destructiveMarkedOperations returns the operationIds the YAML path files mark
// x-bifrost-destructive: true, each the operationId declared above the marker.
func destructiveMarkedOperations(t *testing.T, dir string) []string {
	t.Helper()
	var marked []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		operationID := ""
		for _, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			if id, ok := strings.CutPrefix(trimmed, "operationId:"); ok {
				operationID = strings.TrimSpace(id)
			}
			if trimmed == "x-bifrost-destructive: true" {
				if operationID == "" {
					t.Errorf("%s: x-bifrost-destructive with no operationId above it", path)
					continue
				}
				marked = append(marked, operationID)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return marked
}

// A failed load leaves the server unset (the endpoint answers 503). Until the
// retry interval passes, calls answer that failure without fetching again; after
// it, the next call starts a load in the background and still answers at once,
// so a request never waits on the spec source, even when it stalls. Once the
// background load lands the server is there, with no restart.
func TestBifrostMCPLoadFailureThenRecovery(t *testing.T) {
	spec := readRepoSpec(t)
	var healthy atomic.Bool
	var fetches atomic.Int32
	stall := make(chan struct{})
	var stalling atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetches.Add(1)
		if stalling.Load() {
			<-stall
		}
		if !healthy.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(spec)
	}))
	defer upstream.Close()
	defer func() {
		select {
		case <-stall:
		default:
			close(stall)
		}
	}()

	m := NewBifrostMCP(BifrostMCPOptions{
		SpecURL:    upstream.URL + "/openapi.json",
		BaseURL:    testBifrostMCPBaseURL,
		SpecClient: upstream.Client(),
		ExecClient: http.DefaultClient,
	})
	if srv, err := m.Server(context.Background()); err == nil || srv != nil {
		t.Fatalf("Server against a failing upstream = %v, %v; want an error and no server", srv, err)
	}
	healthy.Store(true)
	for range 5 {
		if srv, err := m.Server(context.Background()); err == nil || srv != nil {
			t.Fatalf("Server within the retry interval = %v, %v; want the recorded failure", srv, err)
		}
	}
	if got := fetches.Load(); got != 1 {
		t.Fatalf("spec fetched %d times within the retry interval, want 1", got)
	}

	// After the interval the source stalls: the call that starts the retry, and
	// every call while it hangs, still answers at once.
	stalling.Store(true)
	m.retryAt.Store(time.Now().Add(-time.Second).UnixNano())
	for range 5 {
		answered := make(chan error, 1)
		go func() {
			_, err := m.Server(context.Background())
			answered <- err
		}()
		select {
		case err := <-answered:
			if err == nil {
				t.Fatal("Server returned a server while the retry was stalled")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Server waited on the stalled retry")
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for fetches.Load() != 2 {
		if time.Now().After(deadline) {
			t.Fatalf("spec fetched %d times, want one background retry", fetches.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}

	stalling.Store(false)
	close(stall)
	deadline = time.Now().Add(10 * time.Second)
	for {
		if srv, err := m.Server(context.Background()); err == nil && srv != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the server never loaded after the source recovered")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := fetches.Load(); got != 2 {
		t.Errorf("spec fetched %d times, want 2: one failure, one background retry", got)
	}
}

// Start loads in the background without holding up Server: while its fetch is
// stalled a Server call answers unavailable at once, and once the fetch
// completes the server is there.
func TestBifrostMCPStartDoesNotBlockServer(t *testing.T) {
	spec := readRepoSpec(t)
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(spec)
	}))
	defer upstream.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	m := NewBifrostMCP(BifrostMCPOptions{SpecURL: upstream.URL + "/openapi.json", BaseURL: testBifrostMCPBaseURL, SpecClient: upstream.Client(), ExecClient: http.DefaultClient})
	m.Start(context.Background())
	deadline := time.Now().Add(5 * time.Second)
	for !m.loading.Load() {
		if time.Now().After(deadline) {
			t.Fatal("Start did not begin a load")
		}
		time.Sleep(5 * time.Millisecond)
	}

	answered := make(chan error, 1)
	go func() {
		_, err := m.Server(context.Background())
		answered <- err
	}()
	select {
	case err := <-answered:
		if err == nil {
			t.Fatal("Server returned a server while the fetch was still stalled")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Server waited on Start's stalled fetch")
	}

	close(release)
	deadline = time.Now().Add(10 * time.Second)
	for {
		if srv, err := m.Server(context.Background()); err == nil && srv != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the server never loaded after the fetch was released")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A spec URL is recognised whatever the case of its scheme.
func TestBifrostMCPLoadsUppercaseSchemeURL(t *testing.T) {
	spec := readRepoSpec(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(spec)
	}))
	defer upstream.Close()
	source := "HTTP://" + strings.TrimPrefix(upstream.URL, "http://") + "/openapi.json"
	if _, err := NewBifrostMCP(BifrostMCPOptions{SpecURL: source, BaseURL: testBifrostMCPBaseURL, SpecClient: upstream.Client(), ExecClient: http.DefaultClient}).Server(context.Background()); err != nil {
		t.Fatalf("loading %s: %v", source, err)
	}
}

// A spec URL from settings can carry a credential in its userinfo or query; the
// load error names the source with both masked, keeping the rest of the address.
func TestBifrostMCPLoadErrorRedactsSource(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer upstream.Close()
	source := strings.Replace(upstream.URL, "http://", "http://spec-user:spec-pass@", 1) + "/openapi.json?token=spec-secret"
	_, err := NewBifrostMCP(BifrostMCPOptions{SpecURL: source, BaseURL: testBifrostMCPBaseURL, SpecClient: upstream.Client(), ExecClient: http.DefaultClient}).Server(context.Background())
	if err == nil {
		t.Fatal("load against a failing upstream succeeded")
	}
	for _, secret := range []string{"spec-pass", "spec-user", "spec-secret"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("load error leaks %q: %v", secret, err)
		}
	}
	if !strings.Contains(err.Error(), "/openapi.json?token=redacted") {
		t.Errorf("load error lost the non-secret source details: %v", err)
	}
}

// A spec URL that does not parse cannot have its credentials told apart from
// the rest, so neither the load error nor anything it wraps shows it.
func TestBifrostMCPLoadErrorRedactsUnparseableSource(t *testing.T) {
	source := "https://spec-user:spec-pass@gateway.example.com:bad-port/openapi.json?token=spec-secret"
	_, err := NewBifrostMCP(BifrostMCPOptions{SpecURL: source, BaseURL: testBifrostMCPBaseURL, SpecClient: http.DefaultClient, ExecClient: http.DefaultClient}).Server(context.Background())
	if err == nil {
		t.Fatal("loading a URL that does not parse succeeded")
	}
	for _, secret := range []string{"spec-pass", "spec-user", "spec-secret"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("load error leaks %q: %v", secret, err)
		}
	}
	if !errors.Is(err, openapimcp.ErrSpecURLInvalid) {
		t.Errorf("the redacted error lost its cause: %v", err)
	}
}

func TestBifrostMCPLoadFromFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "openapi.json"), readRepoSpec(t), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, opts := range map[string]BifrostMCPOptions{
		"relative to app dir": {SpecURL: "openapi.json", AppDir: dir, BaseURL: testBifrostMCPBaseURL, ExecClient: http.DefaultClient},
		"absolute":            {SpecURL: filepath.Join(dir, "openapi.json"), BaseURL: testBifrostMCPBaseURL, ExecClient: http.DefaultClient},
	} {
		if _, err := NewBifrostMCP(opts).Server(context.Background()); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// With no spec URL set the gateway reads the OSS default; a setting replaces it.
func TestBifrostMCPDefaultSpecURL(t *testing.T) {
	var fetched []string
	opts := bifrostMCPTestOptions(true)
	opts.SpecClient = specServingClient(t, &fetched)
	if _, err := NewBifrostMCP(opts).Server(context.Background()); err != nil {
		t.Fatal(err)
	}
	opts.SpecURL = BifrostMCPSpecURLEnterprise
	if _, err := NewBifrostMCP(opts).Server(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{BifrostMCPSpecURLOSS, BifrostMCPSpecURLEnterprise}
	if strings.Join(fetched, " ") != strings.Join(want, " ") {
		t.Errorf("fetched %v, want %v", fetched, want)
	}
	if !strings.HasSuffix(BifrostMCPSpecURLOSS, "/docs/openapi/openapi-oss.json") || !strings.HasSuffix(BifrostMCPSpecURLEnterprise, "/docs/openapi/openapi.json") {
		t.Errorf("defaults point at %s and %s", BifrostMCPSpecURLOSS, BifrostMCPSpecURLEnterprise)
	}
}

// Reload applies new settings; a reload that fails keeps the server it had.
func TestBifrostMCPReload(t *testing.T) {
	var fetched []string
	opts := bifrostMCPTestOptions(false)
	opts.SpecClient = specServingClient(t, &fetched)
	m := NewBifrostMCP(opts)
	before, err := m.Server(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := before.ListTools()["createVirtualKey"]; ok {
		t.Fatal("writes off serves createVirtualKey")
	}
	opts.AllowWrites = true
	if err := m.Reload(context.Background(), opts); err != nil {
		t.Fatalf("reload: %v", err)
	}
	after, err := m.Server(context.Background())
	if err != nil || after == before {
		t.Fatalf("Server after reload = %p, %v; want a new server", after, err)
	}
	if _, ok := after.ListTools()["createVirtualKey"]; !ok {
		t.Error("reload with writes on does not serve createVirtualKey")
	}
	broken := opts
	broken.SpecURL = filepath.Join(t.TempDir(), "missing.json")
	if err := m.Reload(context.Background(), broken); err == nil {
		t.Fatal("reload with a missing spec succeeded")
	}
	if kept, _ := m.Server(context.Background()); kept != after {
		t.Error("a failed reload replaced the server")
	}
}

// Tool calls address the configured base URL; with none the load fails rather
// than serving tools that point nowhere.
func TestBifrostMCPBaseURL(t *testing.T) {
	var called []string
	opts := bifrostMCPTestOptions(true)
	opts.ExecClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		called = append(called, req.URL.String())
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"providers":[]}`)), Header: http.Header{"Content-Type": {"application/json"}}, Request: req}, nil
	})}
	srv, _, err := buildBifrostMCPServer(readRepoSpec(t), "", opts)
	if err != nil {
		t.Fatal(err)
	}
	call, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "listProviders", "arguments": map[string]any{}}})
	srv.HandleMessage(context.Background(), call)
	if len(called) != 1 || !strings.HasPrefix(called[0], testBifrostMCPBaseURL+"/api/providers") {
		t.Errorf("tool call went to %v, want %s/api/providers", called, testBifrostMCPBaseURL)
	}

	opts.BaseURL = ""
	if _, _, err := buildBifrostMCPServer(readRepoSpec(t), "", opts); err == nil {
		t.Error("built a server with no base URL")
	}
}
