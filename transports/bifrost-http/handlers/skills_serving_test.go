package handlers

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fasthttp/router"
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
