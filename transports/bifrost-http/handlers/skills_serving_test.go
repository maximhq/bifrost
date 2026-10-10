package handlers

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fasthttp/router"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/maximhq/bifrost/core/network"
	"github.com/maximhq/bifrost/core/network/proxytest"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttputil"
)

func TestSkillsServingGenericFileDownloadDecodesEncodedPathParams(t *testing.T) {
	ctx := context.Background()
	store := newTestConfigStore(t)
	blobID := "encoded-file-blob"
	content := []byte("encoded file content")

	if err := store.CreateSkillFileBlob(ctx, &tables.TableSkillFileBlob{ID: blobID, Data: content}); err != nil {
		t.Fatalf("create blob: %v", err)
	}
	if err := store.CreateSkill(ctx, &tables.TableSkill{
		Name:        "encoded-file-skill",
		Description: "skill with encoded file paths",
		SkillMDBody: "body",
		Files: []tables.TableSkillFile{{
			Path:          "nested dir/file with spaces.txt",
			SourceType:    tables.SkillSourceTypeText,
			BlobID:        &blobID,
			MimeType:      "text/plain",
			FileSizeBytes: int64(len(content)),
		}},
	}, "1.0.0", nil); err != nil {
		t.Fatalf("create skill: %v", err)
	}

	handler := NewSkillsServingHandler(store, nil)
	r := router.New()
	handler.RegisterRoutes(r)

	server := &fasthttp.Server{Handler: r.Handler}
	ln := fasthttputil.NewInmemoryListener()
	go server.Serve(ln) //nolint:errcheck
	defer ln.Close()
	defer server.Shutdown()

	client := &fasthttp.Client{
		Dial: func(addr string) (net.Conn, error) {
			return ln.Dial()
		},
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.Header.SetMethod(fasthttp.MethodGet)
	req.SetRequestURI("http://test.local/api/skills/serve/encoded-file-skill/files/nested%20dir/file%20with%20spaces.txt")

	if err := client.Do(req, resp); err != nil {
		t.Fatalf("request failed: %v", err)
	}

	if resp.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("status got %d, want %d; body=%s", resp.StatusCode(), fasthttp.StatusOK, string(resp.Body()))
	}
	if got := string(resp.Body()); got != string(content) {
		t.Fatalf("body got %q, want %q", got, string(content))
	}
}

func TestClaudeMarketplaceGitRepoContainsMarketplaceAndCloneablePlugin(t *testing.T) {
	if !CheckGitAvailability() {
		t.Skip("git binary is unavailable")
	}

	ctx := context.Background()
	store := newTestConfigStore(t)
	if err := store.CreateSkill(ctx, &tables.TableSkill{
		Name:        "desktop-skill",
		Description: "skill for testing Claude Desktop marketplace installation",
		SkillMDBody: "Use this skill from Claude Desktop.",
	}, "1.0.0", nil); err != nil {
		t.Fatalf("create skill: %v", err)
	}

	handler := NewSkillsServingHandler(store, nil)
	r := router.New()
	handler.RegisterRoutes(r)

	server := &fasthttp.Server{Handler: r.Handler}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go server.Serve(ln) //nolint:errcheck
	defer server.Shutdown()
	defer ln.Close()

	baseURL := "http://" + ln.Addr().String()
	marketplaceURL := baseURL + "/api/skills/serve/claude-code.git"
	cloneDir := filepath.Join(t.TempDir(), "marketplace")
	cloneGitRepo(t, marketplaceURL, cloneDir)

	marketplaceBytes, err := os.ReadFile(filepath.Join(cloneDir, ".claude-plugin", "marketplace.json"))
	if err != nil {
		t.Fatalf("read marketplace manifest: %v", err)
	}
	var marketplace struct {
		Plugins []struct {
			Name   string `json:"name"`
			Source struct {
				Source string `json:"source"`
				URL    string `json:"url"`
			} `json:"source"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(marketplaceBytes, &marketplace); err != nil {
		t.Fatalf("decode marketplace manifest: %v", err)
	}

	var pluginURL string
	for _, plugin := range marketplace.Plugins {
		if plugin.Name == "bifrost-desktop-skill" {
			if plugin.Source.Source != "url" {
				t.Fatalf("plugin source type got %q, want url", plugin.Source.Source)
			}
			pluginURL = plugin.Source.URL
			break
		}
	}
	if pluginURL != baseURL+"/api/skills/serve/claude-code/plugins/bifrost-desktop-skill" {
		t.Fatalf("plugin URL got %q; marketplace=%s", pluginURL, marketplaceBytes)
	}

	pluginCloneDir := filepath.Join(t.TempDir(), "plugin")
	cloneGitRepo(t, pluginURL, pluginCloneDir)
	for _, relativePath := range []string{
		filepath.Join(".claude-plugin", "plugin.json"),
		filepath.Join("skills", "desktop-skill", "SKILL.md"),
	} {
		if _, err := os.Stat(filepath.Join(pluginCloneDir, relativePath)); err != nil {
			t.Errorf("expected plugin file %s: %v", relativePath, err)
		}
	}

	statusCode, _, err := fasthttp.Get(nil, baseURL+"/api/skills/serve/claude-code/.claude-plugin/marketplace.json")
	if err != nil {
		t.Fatalf("get raw marketplace: %v", err)
	}
	if statusCode != fasthttp.StatusOK {
		t.Fatalf("raw marketplace status got %d, want %d", statusCode, fasthttp.StatusOK)
	}
}

func cloneGitRepo(t *testing.T, repoURL, destination string) {
	t.Helper()
	cmd := exec.Command(gitBinaryPath, "clone", "--quiet", repoURL, destination) //nolint:gosec // gitBinaryPath is resolved from PATH
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git clone %s: %v: %s", repoURL, err, strings.TrimSpace(string(output)))
	}
}

// countingBareRepoExport swaps exportBareRepo for the test's lifetime and
// records every directory it produced.
func countingBareRepoExport(t *testing.T) *[]string {
	t.Helper()
	var dirs []string
	original := exportBareRepo
	exportBareRepo = func(storage *memory.Storage) (string, error) {
		dir, err := original(storage)
		if err == nil {
			dirs = append(dirs, dir)
		}
		return dir, err
	}
	t.Cleanup(func() { exportBareRepo = original })
	return &dirs
}

// skillsServingTestServer starts the handler's routes on an in-memory listener.
// The git-serving path derives a context from the *fasthttp.RequestCtx, which a
// bare RequestCtx cannot satisfy, so these tests go over a real served request.
func skillsServingTestServer(t *testing.T, r *router.Router) *fasthttp.Client {
	t.Helper()
	server := &fasthttp.Server{Handler: r.Handler}
	ln := fasthttputil.NewInmemoryListener()
	go server.Serve(ln) //nolint:errcheck
	t.Cleanup(func() { _ = ln.Close() })
	t.Cleanup(func() { _ = server.Shutdown() })
	return &fasthttp.Client{
		Dial:         func(string) (net.Conn, error) { return ln.Dial() },
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
}

func getSkillsServing(t *testing.T, client *fasthttp.Client, uri string) (int, []byte, *fasthttp.ResponseHeader) {
	t.Helper()
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	t.Cleanup(func() { fasthttp.ReleaseRequest(req); fasthttp.ReleaseResponse(resp) })
	req.Header.SetMethod(fasthttp.MethodGet)
	req.SetRequestURI("http://bifrost" + uri)
	if err := client.Do(req, resp); err != nil {
		t.Fatalf("request %s: %v", uri, err)
	}
	return resp.StatusCode(), append([]byte(nil), resp.Body()...), &resp.Header
}

func TestSkillsServingAllSkillsGitRepoReusedUntilCorpusChanges(t *testing.T) {
	SetLogger(&mockLogger{})
	if !CheckGitAvailability() {
		t.Skip("git binary is unavailable")
	}

	ctx := context.Background()
	store := newTestConfigStore(t)
	if err := store.CreateSkill(ctx, &tables.TableSkill{
		Name:        "cached-skill",
		Description: "skill served from the cached all-skills repo",
		SkillMDBody: "body",
	}, "1.0.0", nil); err != nil {
		t.Fatalf("create skill: %v", err)
	}

	exports := countingBareRepoExport(t)
	handler := NewSkillsServingHandler(store, nil)
	t.Cleanup(handler.Close)
	r := router.New()
	handler.RegisterRoutes(r)

	client := skillsServingTestServer(t, r)
	const infoRefs = "/api/skills/serve/claude-code/plugins/bifrost-all-skills/info/refs?service=git-upload-pack"
	for i := 0; i < 3; i++ {
		code, body, _ := getSkillsServing(t, client, infoRefs)
		if code != fasthttp.StatusOK {
			t.Fatalf("request %d: status %d: %s", i, code, body)
		}
		if !strings.Contains(string(body), "refs/heads/main") {
			t.Fatalf("request %d: advertisement missing refs: %s", i, body)
		}
	}
	if len(*exports) != 1 {
		t.Fatalf("expected one bare repo export across three identical requests, got %d", len(*exports))
	}
	firstDir := (*exports)[0]
	if _, err := os.Stat(firstDir); err != nil {
		t.Fatalf("cached export should still exist between requests: %v", err)
	}

	// Any corpus change bumps the all-skills version and must invalidate the cache.
	if err := store.CreateSkill(ctx, &tables.TableSkill{
		Name:        "second-skill",
		Description: "a change to the corpus",
		SkillMDBody: "body",
	}, "1.0.0", nil); err != nil {
		t.Fatalf("create second skill: %v", err)
	}
	code, body, _ := getSkillsServing(t, client, infoRefs)
	if code != fasthttp.StatusOK {
		t.Fatalf("post-change request: status %d: %s", code, body)
	}
	if len(*exports) != 2 {
		t.Fatalf("expected a fresh export after the corpus changed, got %d exports", len(*exports))
	}
	if _, err := os.Stat(firstDir); !os.IsNotExist(err) {
		t.Fatalf("stale export %s should have been removed once evicted, stat err=%v", firstDir, err)
	}

	handler.Close()
	if _, err := os.Stat((*exports)[1]); !os.IsNotExist(err) {
		t.Fatalf("Close should remove the cached export, stat err=%v", err)
	}
}

func TestSkillsServingPluginGitRepoUnknownSkillIsNotFound(t *testing.T) {
	SetLogger(&mockLogger{})
	if !CheckGitAvailability() {
		t.Skip("git binary is unavailable")
	}
	store := newTestConfigStore(t)
	exports := countingBareRepoExport(t)
	handler := NewSkillsServingHandler(store, nil)
	t.Cleanup(handler.Close)
	r := router.New()
	handler.RegisterRoutes(r)
	client := skillsServingTestServer(t, r)

	code, body, _ := getSkillsServing(t, client, "/api/skills/serve/claude-code/plugins/bifrost-missing/info/refs?service=git-upload-pack")
	if code != fasthttp.StatusNotFound {
		t.Fatalf("status %d, want 404: %s", code, body)
	}
	if len(*exports) != 0 {
		t.Fatalf("a missing skill must not export a repo, got %d exports", len(*exports))
	}
}

func TestSkillsServingCorpusGateRejectsWhenSaturated(t *testing.T) {
	SetLogger(&mockLogger{})
	store := newTestConfigStore(t)
	handler := NewSkillsServingHandler(store, nil)
	t.Cleanup(handler.Close)
	r := router.New()
	handler.RegisterRoutes(r)

	// Fill every slot so the next corpus-serving request finds the gate closed.
	for i := 0; i < skillsCorpusServeConcurrency; i++ {
		skillsCorpusServeGate <- struct{}{}
	}
	t.Cleanup(func() {
		for i := 0; i < skillsCorpusServeConcurrency; i++ {
			<-skillsCorpusServeGate
		}
	})

	client := skillsServingTestServer(t, r)

	uris := []string{"/api/skills/serve/all/download.zip"}
	if CheckGitAvailability() {
		uris = append(uris, "/api/skills/serve/claude-code/plugins/bifrost-all-skills/info/refs?service=git-upload-pack")
	}
	for _, uri := range uris {
		code, body, header := getSkillsServing(t, client, uri)
		if code != fasthttp.StatusServiceUnavailable {
			t.Fatalf("%s: status %d, want 503: %s", uri, code, body)
		}
		if got := string(header.Peek("Retry-After")); got == "" {
			t.Fatalf("%s: expected a Retry-After header on the 503", uri)
		}
	}

	// Per-skill file downloads do not walk the corpus and stay outside the gate.
	code, _, _ := getSkillsServing(t, client, "/api/skills/serve/nope/files/SKILL.md")
	if code == fasthttp.StatusServiceUnavailable {
		t.Fatal("single-file downloads must not be gated")
	}

	// Once a slot frees, the same request is served.
	<-skillsCorpusServeGate
	defer func() { skillsCorpusServeGate <- struct{}{} }()
	code, body, _ := getSkillsServing(t, client, "/api/skills/serve/all/download.zip")
	if code != fasthttp.StatusOK {
		t.Fatalf("zip after a slot freed: status %d: %s", code, body)
	}
}

func TestSkillsGitRepoCacheDropsDirectoryAfterLastReader(t *testing.T) {
	cache := newSkillsGitRepoCache()
	builds := 0
	build := func() (string, error) {
		builds++
		return t.TempDir(), nil
	}

	dir1, release1, err := cache.acquire("repo", "v1", build)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	dir1Again, release1Again, err := cache.acquire("repo", "v1", build)
	if err != nil {
		t.Fatalf("acquire again: %v", err)
	}
	if dir1 != dir1Again || builds != 1 {
		t.Fatalf("same version must share one build: dirs %q/%q, builds %d", dir1, dir1Again, builds)
	}

	// A new version while dir1 is still being read: dir1 is dropped but kept on disk
	// until both readers release.
	dir2, release2, err := cache.acquire("repo", "v2", build)
	if err != nil {
		t.Fatalf("acquire v2: %v", err)
	}
	if dir2 == dir1 || builds != 2 {
		t.Fatalf("new version must rebuild: dir2 %q, builds %d", dir2, builds)
	}
	if _, err := os.Stat(dir1); err != nil {
		t.Fatalf("dir1 must survive while readers hold it: %v", err)
	}
	release1()
	release1() // idempotent
	if _, err := os.Stat(dir1); err != nil {
		t.Fatalf("dir1 must survive while one reader still holds it: %v", err)
	}
	release1Again()
	if _, err := os.Stat(dir1); !os.IsNotExist(err) {
		t.Fatalf("dir1 must be removed after the last reader releases, stat err=%v", err)
	}
	release2()
	if _, err := os.Stat(dir2); err != nil {
		t.Fatalf("live entry must stay on disk after release: %v", err)
	}

	cache.purge()
	if _, err := os.Stat(dir2); !os.IsNotExist(err) {
		t.Fatalf("purge must remove live entries, stat err=%v", err)
	}
}

// TestSkillsGitRepoCacheBuildDoesNotBlockOtherKeys: a slow build for one repository must not
// hold up a cached repository or the release of a finished request. Both would otherwise wait
// on the cache lock while still holding corpus-gate slots, turning one slow miss into 503s for
// unrelated, already-cached repositories.
func TestSkillsGitRepoCacheBuildDoesNotBlockOtherKeys(t *testing.T) {
	cache := newSkillsGitRepoCache()
	fast := func() (string, error) { return t.TempDir(), nil }
	if _, releaseB, err := cache.acquire("b", "v1", fast); err != nil {
		t.Fatalf("acquire b: %v", err)
	} else {
		releaseB()
	}

	unblock := make(chan struct{})
	started := make(chan struct{})
	slowDone := make(chan error, 1)
	go func() {
		_, release, err := cache.acquire("a", "v1", func() (string, error) {
			close(started)
			<-unblock
			return t.TempDir(), nil
		})
		if err == nil {
			release()
		}
		slowDone <- err
	}()
	<-started

	got := make(chan error, 1)
	go func() {
		_, releaseB, err := cache.acquire("b", "v1", fast)
		if err == nil {
			releaseB()
		}
		got <- err
	}()
	select {
	case err := <-got:
		if err != nil {
			t.Fatalf("acquire b during a's build: %v", err)
		}
	case <-time.After(2 * time.Second):
		close(unblock)
		t.Fatal("a cached repository must be served while another repository is still building")
	}
	close(unblock)
	if err := <-slowDone; err != nil {
		t.Fatalf("slow build: %v", err)
	}
}

// TestSkillsGitRepoCacheConcurrentMissesBuildOnce: two requests missing on the same repository
// at the same time share one build; the second waits for the first instead of exporting again.
func TestSkillsGitRepoCacheConcurrentMissesBuildOnce(t *testing.T) {
	cache := newSkillsGitRepoCache()
	var builds int32
	gate := make(chan struct{})
	build := func() (string, error) {
		atomic.AddInt32(&builds, 1)
		<-gate
		return t.TempDir(), nil
	}
	type result struct {
		dir string
		err error
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() {
			dir, release, err := cache.acquire("a", "v1", build)
			if err == nil {
				release()
			}
			results <- result{dir, err}
		}()
	}
	// Let both goroutines reach acquire before the build is allowed to finish.
	time.Sleep(100 * time.Millisecond)
	close(gate)
	first := <-results
	second := <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("acquire errors: %v / %v", first.err, second.err)
	}
	if first.dir != second.dir {
		t.Fatalf("concurrent misses must share one export: %q vs %q", first.dir, second.dir)
	}
	if n := atomic.LoadInt32(&builds); n != 1 {
		t.Fatalf("expected one build, got %d", n)
	}
}

// TestSkillsGitRepoCacheBuildPanicDoesNotStrandWaiters: a build that panics must not leave its
// placeholder in the cache with ready never closed, or every later request for that key would
// wait forever while holding a corpus-gate slot. The panic still propagates to the caller (the
// recovery middleware turns it into a 500); the next request for the key builds afresh.
func TestSkillsGitRepoCacheBuildPanicDoesNotStrandWaiters(t *testing.T) {
	cache := newSkillsGitRepoCache()
	panicked := make(chan interface{}, 1)
	go func() {
		defer func() { panicked <- recover() }()
		_, _, _ = cache.acquire("a", "v1", func() (string, error) { panic("export exploded") })
	}()
	select {
	case p := <-panicked:
		if p == nil {
			t.Fatal("the build panic must propagate out of acquire")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("acquire never returned from the panicking build")
	}

	done := make(chan error, 1)
	go func() {
		_, release, err := cache.acquire("a", "v1", func() (string, error) { return t.TempDir(), nil })
		if err == nil {
			release()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the key must be buildable again after a panicking build: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a request after a panicking build must not wait on the stranded placeholder")
	}
}

// TestFetchURLSafeGoesThroughGlobalProxy pins that URL-sourced skill fetches honour the
// global proxy for API traffic once the server installs its factory, and keep refusing
// non-public targets before anything reaches the proxy. The skill client used to build
// its own transport with no proxy support.
func TestFetchURLSafeGoesThroughGlobalProxy(t *testing.T) {
	set := proxytest.NewSet(t)
	SetSkillFetchHTTPClientFactory(network.NewHTTPClientFactory(&network.GlobalProxyConfig{
		Enabled:      true,
		Type:         network.GlobalProxyTypeHTTP,
		URL:          "http://127.0.0.1:" + set.Config.Port(),
		EnableForAPI: true,
	}, nil))
	t.Cleanup(func() { SetSkillFetchHTTPClientFactory(nil) })

	if _, err := fetchURLSafe(t.Context(), "https://203.0.113.10/skill.md"); err == nil {
		t.Fatal("expected the recorder's closed tunnel to fail the fetch")
	}
	proxytest.AssertRoute(t, set, proxytest.Route{Proxy: "config"}, "203.0.113.10:443", nil)

	set.Reset()
	_, err := fetchURLSafe(t.Context(), "https://10.0.0.5/skill.md")
	if err == nil || !strings.Contains(err.Error(), "non-public address") {
		t.Fatalf("expected a private target to be refused, got %v", err)
	}
	proxytest.AssertRoute(t, set, proxytest.Direct, "", nil)
}
