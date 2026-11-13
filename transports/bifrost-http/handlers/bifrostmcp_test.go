package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fasthttp/router"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/openapimcp"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
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

// testBifrostMCPBaseURL stands in for the gateway's bifrost_url setting.
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

// With no spec URL set the gateway reads DefaultOpenAPIURL, dev's OSS spec; a
// setting replaces it.
func TestBifrostMCPDefaultSpecURL(t *testing.T) {
	var fetched []string
	opts := bifrostMCPTestOptions(true)
	opts.SpecClient = specServingClient(t, &fetched)
	if _, err := NewBifrostMCP(opts).Server(context.Background()); err != nil {
		t.Fatal(err)
	}
	const custom = "https://example.com/specs/openapi.json"
	opts.SpecURL = custom
	if _, err := NewBifrostMCP(opts).Server(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{DefaultOpenAPIURL, custom}
	if strings.Join(fetched, " ") != strings.Join(want, " ") {
		t.Errorf("fetched %v, want %v", fetched, want)
	}
	if DefaultOpenAPIURL != "https://raw.githubusercontent.com/maximhq/bifrost/refs/heads/dev/docs/openapi/openapi-oss.json" {
		t.Errorf("default points at %s, want dev's openapi-oss.json", DefaultOpenAPIURL)
	}
}

// Reload applies new settings; a reload that fails keeps the server it had.
// A rebuild that fails cannot leave a server serving what the saved settings no
// longer allow: a save that turns /api writes off, or clears bifrost_url, with
// a spec that does not load unloads the previous server (/v1/mcp answers
// unavailable) instead of keeping its write tools. A failed save that restricts
// nothing still keeps the previous server.
func TestBifrostMCPFailedRestrictiveReloadUnloads(t *testing.T) {
	var fetched []string
	opts := bifrostMCPTestOptions(true)
	opts.SpecClient = specServingClient(t, &fetched)
	missing := filepath.Join(t.TempDir(), "missing.json")

	m := NewBifrostMCP(opts)
	before, err := m.Server(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	broken := opts
	broken.SpecURL = missing
	if err := m.Reload(context.Background(), broken); err == nil {
		t.Fatal("reload with a missing spec succeeded")
	}
	if kept, _ := m.Server(context.Background()); kept != before {
		t.Fatal("a failed save that restricts nothing must keep the previous server")
	}

	writesOff := broken
	writesOff.AllowWrites = false
	if err := m.Reload(context.Background(), writesOff); err == nil {
		t.Fatal("reload with a missing spec succeeded")
	}
	if srv, err := m.Server(context.Background()); srv != nil || err == nil {
		t.Fatalf("writes turned off with a spec that does not load left %v serving (err %v); want none", srv, err)
	}
	if m.Options().AllowWrites {
		t.Error("the saved writes switch was not taken")
	}

	m = NewBifrostMCP(opts)
	if _, err := m.Server(context.Background()); err != nil {
		t.Fatal(err)
	}
	cleared := broken
	cleared.BaseURL = ""
	if err := m.Reload(context.Background(), cleared); err == nil {
		t.Fatal("reload without bifrost_url succeeded")
	}
	if srv, err := m.Server(context.Background()); srv != nil || err == nil {
		t.Fatalf("bifrost_url cleared left %v serving (err %v); want none", srv, err)
	}
}

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
	m := NewBifrostMCP(BifrostMCPOptions{SpecURL: spec, BaseURL: testBifrostMCPBaseURL, AllowWrites: true, ExecClient: exec.Client})
	if _, err := m.Server(context.Background()); err != nil {
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

// /v1/mcp is stateless, so mcp-go keys every in-flight request by its JSON-RPC
// id alone. A notifications/cancelled from one caller would cancel another
// caller's request that happens to use the same id, so the endpoint drops
// cancellations instead of dispatching them.
func TestBifrostMCPEndpoint_CancellationIsNotSharedAcrossCallers(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	h := newBifrostMCPTestEndpoint(t, func(ctx *fasthttp.RequestCtx) {
		entered <- struct{}{}
		<-release
		ctx.SetContentType("application/json")
		ctx.SetBodyString(`"v-test"`)
	})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	asCaller := func(credential string) func(*fasthttp.RequestCtx) {
		return func(ctx *fasthttp.RequestCtx) { ctx.Request.Header.Set("Authorization", "Bearer "+credential) }
	}

	// Caller A's call, id 7, blocks in its route.
	done := make(chan []byte, 1)
	go func() {
		var req fasthttp.Request
		req.Header.SetMethod(fasthttp.MethodPost)
		req.Header.SetContentType("application/json")
		req.SetRequestURI("/v1/mcp")
		req.SetBodyString(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"getVersion","arguments":{}}}`)
		ctx := initCtx(&req)
		asCaller("caller-a")(ctx)
		h.handlePost(ctx)
		done <- append([]byte(nil), ctx.Response.Body()...)
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("caller A's call never reached its route")
	}

	// Caller B cancels "its" request 7.
	status, _ := postBifrostMCP(t, h, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":7,"reason":"stop"}}`, asCaller("caller-b"))
	if status != fasthttp.StatusAccepted {
		t.Errorf("notifications/cancelled answered %d, want 202", status)
	}
	select {
	case body := <-done:
		t.Fatalf("caller B's cancellation ended caller A's call: %s", body)
	case <-time.After(300 * time.Millisecond):
	}

	releaseOnce.Do(func() { close(release) })
	select {
	case body := <-done:
		var resp map[string]any
		if err := json.Unmarshal(body, &resp); err != nil {
			t.Fatalf("decoding %s: %v", body, err)
		}
		if text, isError := toolCallResult(t, resp); isError || !strings.Contains(text, "v-test") {
			t.Errorf("caller A's call: got %q (error %v), want its route's result", text, isError)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("caller A's call did not finish")
	}
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

// A tool call carries every header the caller sent and its peer address into
// /api, as a direct call would, but never its Host, which the caller controls:
// the call carries the configured base URL's host. And never a Cookie: a browser
// session cannot be ridden through /v1/mcp.
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
		ctx.Request.Header.Set("x-bf-eh-x-tenant", "acme")
		ctx.Request.Header.Set("X-Custom-Trace", "trace-1")
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
		"Authorization":    "Bearer bfst-test",
		SetupTokenHeader:   "s3cret",
		"X-Forwarded-For":  "203.0.113.9",
		"x-bf-eh-x-tenant": "acme",
		"X-Custom-Trace":   "trace-1",
	} {
		if got := string(seen.Header.Peek(header)); got != want {
			t.Errorf("/api saw %s %q, want %q", header, got, want)
		}
	}
	if got := string(seen.Header.Host()); got != strings.TrimPrefix(testBifrostMCPBaseURL, "http://") {
		t.Errorf("/api saw Host %q, want the configured base URL's, never the caller's", got)
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

// Without bifrost_url the Bifrost MCP does not load: there is no fallback to
// the caller's Host header. The load is refused before the spec is fetched, and
// /v1/mcp answers 503 naming the setting.
func TestBifrostMCPRequiresBifrostURL(t *testing.T) {
	var fetched []string
	opts := bifrostMCPTestOptions(true)
	opts.BaseURL = ""
	opts.SpecClient = specServingClient(t, &fetched)
	m := NewBifrostMCP(opts)
	if _, err := m.Server(context.Background()); !errors.Is(err, ErrBifrostMCPNoBifrostURL) {
		t.Fatalf("Server with no base URL = %v, want ErrBifrostMCPNoBifrostURL", err)
	}
	if len(fetched) != 0 {
		t.Errorf("the spec was fetched with no base URL to serve it: %v", fetched)
	}
	r := router.New()
	NewBifrostMCPHandler(m).RegisterRoutes(r)
	var req fasthttp.Request
	req.Header.SetMethod(fasthttp.MethodPost)
	req.SetRequestURI("/v1/mcp")
	req.SetBodyString(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	ctx := initCtx(&req)
	r.Handler(ctx)
	if ctx.Response.StatusCode() != fasthttp.StatusServiceUnavailable || !strings.Contains(string(ctx.Response.Body()), "bifrost_url") {
		t.Errorf("/v1/mcp answered %d %s, want 503 naming bifrost_url", ctx.Response.StatusCode(), ctx.Response.Body())
	}
}

// With a Bifrost URL configured, a tool call arrives as if through it: the route
// sees that URL's host and scheme, whatever Host and forwarded proto the caller sent,
// so the self URLs it builds come from configuration.
func TestBifrostMCPEndpoint_BifrostURLSetsHostAndScheme(t *testing.T) {
	SetLogger(&mockLogger{})
	spec, err := filepath.Abs(repoOpenAPISpec)
	if err != nil {
		t.Fatal(err)
	}
	var seenHost, seenBase, seenProto string
	exec := NewBifrostMCPExecutor(func(ctx *fasthttp.RequestCtx) {
		seenHost = string(ctx.Host())
		seenProto = string(ctx.Request.Header.Peek("x-bf-forwarded-proto"))
		seenBase = lib.BuildBaseURL(ctx, "")
		ctx.SetContentType("application/json")
		ctx.SetBodyString(`"v-test"`)
	}, 4<<20, 64<<10)
	t.Cleanup(func() { _ = exec.Close() })
	m := NewBifrostMCP(BifrostMCPOptions{SpecURL: spec, BaseURL: "https://gw.example.com", BaseIsEdge: true, AllowWrites: true, ExecClient: exec.Client})
	if _, err := m.Server(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, resp := postBifrostMCP(t, NewBifrostMCPHandler(m), callBifrostMCPTool("getVersion"), func(ctx *fasthttp.RequestCtx) {
		ctx.Request.Header.Set("Authorization", "Bearer bfst-test")
		ctx.Request.Header.SetHost("spoofed.example")
		ctx.Request.Header.Set("X-Forwarded-Proto", "http")
	})
	if status != fasthttp.StatusOK {
		t.Fatalf("status %d, %v", status, resp)
	}
	if seenHost != "gw.example.com" || seenProto != "https" {
		t.Errorf("/api saw Host %q and x-bf-forwarded-proto %q, want gw.example.com and https", seenHost, seenProto)
	}
	if seenBase != "https://gw.example.com" {
		t.Errorf("a self URL built by the route is %q, want https://gw.example.com", seenBase)
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
	req, err := http.NewRequest(http.MethodGet, testBifrostMCPBaseURL+"/api/version", nil)
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
	resp, err := exec.Client.Get(testBifrostMCPBaseURL + "/api/version")
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
		if resp, err := exec.Client.Get(testBifrostMCPBaseURL + "/api/version"); err == nil {
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
	m := NewBifrostMCP(BifrostMCPOptions{SpecURL: spec, BaseURL: testBifrostMCPBaseURL, ExecClient: exec.Client})
	if _, err := m.Server(context.Background()); err != nil {
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
	return toolsListNamesFor(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, prepare)
}

// toolsListNamesFor is toolsListNames for a tools/list request written as body.
func toolsListNamesFor(t *testing.T, h *BifrostMCPHandler, body string, prepare func(*fasthttp.RequestCtx)) map[string]bool {
	t.Helper()
	status, resp := postBifrostMCP(t, h, body, prepare)
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
	m := NewBifrostMCP(BifrostMCPOptions{SpecURL: spec, BaseURL: testBifrostMCPBaseURL, AllowWrites: true, ExecClient: exec.Client,
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
	if _, err := m.Server(context.Background()); err != nil {
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

	// mcp-go decodes "method" with encoding/json, which matches keys
	// case-insensitively and keeps the last duplicate, so these all dispatch as a
	// tools/list. Each must be filtered like the canonical spelling.
	for _, body := range []string{
		`{"jsonrpc":"2.0","id":1,"Method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":1,"METHOD":"tools/list"}`,
		`{"jsonrpc":"2.0","id":1,"method":"ping","Method":"tools/list"}`,
	} {
		listed := toolsListNamesFor(t, h, body, viewer)
		if !listed["listProviders"] || listed["getVersion"] || listed["createVirtualKey"] {
			t.Errorf("%s: want the filtered list, got listProviders=%v getVersion=%v createVirtualKey=%v", body, listed["listProviders"], listed["getVersion"], listed["createVirtualKey"])
		}
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

// No tool asks a route to stream: a tool result is one message, so a streamed
// answer would reach the model as its raw event stream. Every operation whose body
// takes stream loses stream and stream_options from its arguments, and a call that
// passes stream anyway sends a body without it.
func TestBifrostMCPToolsNeverStream(t *testing.T) {
	var sent []byte
	opts := bifrostMCPTestOptions(true)
	opts.ExecClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Body != nil {
			sent, _ = io.ReadAll(req.Body)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"choices":[]}`)), Header: http.Header{"Content-Type": {"application/json"}}, Request: req}, nil
	})}
	srv, _, err := buildBifrostMCPServer(readRepoSpec(t), "", opts)
	if err != nil {
		t.Fatal(err)
	}
	tools := srv.ListTools()
	streaming := 0
	var wholeBody []string
	for _, op := range repoSpecOperations(t) {
		if op.Body == nil || op.Body.Schema == nil {
			continue
		}
		props, _ := op.Body.Schema["properties"].(map[string]any)
		if _, ok := props["stream"]; !ok {
			continue
		}
		st, served := tools[op.ID]
		if !served {
			continue
		}
		streaming++
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(st.Tool.RawInputSchema, &schema); err != nil {
			t.Fatalf("%s: %v", op.ID, err)
		}
		for _, field := range bifrostMCPStreamingFields {
			if _, ok := schema.Properties[field]; ok {
				t.Errorf("%s still takes %s", op.ID, field)
			}
		}
		if _, ok := schema.Properties["body"]; ok {
			wholeBody = append(wholeBody, op.ID)
		}
	}
	if streaming == 0 {
		t.Fatal("no served operation takes stream; the check proves nothing")
	}
	if len(wholeBody) > 0 {
		t.Logf("operations taking their body whole as one argument, where stream is undeclared: %v", wholeBody)
	}

	call, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "createChatCompletion", "arguments": map[string]any{
		"model": "openai/gpt-4o-mini", "messages": []any{map[string]any{"role": "user", "content": "hi"}}, "stream": true, "stream_options": map[string]any{"include_usage": true},
	}}})
	srv.HandleMessage(context.Background(), call)
	if len(sent) == 0 {
		t.Fatal("createChatCompletion sent no body")
	}
	var body map[string]any
	if err := json.Unmarshal(sent, &body); err != nil {
		t.Fatalf("body %s: %v", sent, err)
	}
	for _, field := range bifrostMCPStreamingFields {
		if _, ok := body[field]; ok {
			t.Errorf("createChatCompletion sent %s: %s", field, sent)
		}
	}
	if body["model"] != "openai/gpt-4o-mini" {
		t.Errorf("the rest of the body was lost: %s", sent)
	}

	// A body taken whole as one argument declares no stream field, so nothing in
	// its schema stops a caller sending one; it is dropped from the body sent.
	if _, ok := tools["cursorCohereChat"]; !ok {
		t.Fatal("cursorCohereChat is not served; the whole-body check proves nothing")
	}
	sent = nil
	call, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "cursorCohereChat", "arguments": map[string]any{
		"body": map[string]any{"model": "command-r", "messages": []any{map[string]any{"role": "user", "content": "hi"}}, "stream": true, "stream_options": map[string]any{"include_usage": true}},
	}}})
	srv.HandleMessage(context.Background(), call)
	body = nil
	if err := json.Unmarshal(sent, &body); err != nil {
		t.Fatalf("cursorCohereChat body %s: %v", sent, err)
	}
	for _, field := range bifrostMCPStreamingFields {
		if _, ok := body[field]; ok {
			t.Errorf("cursorCohereChat sent %s: %s", field, sent)
		}
	}
	if body["model"] != "command-r" {
		t.Errorf("the rest of the whole body was lost: %s", sent)
	}
}
