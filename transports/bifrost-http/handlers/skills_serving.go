package handlers

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/fasthttp/router"
	billy "github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-billy/v5/osfs"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/objectstore"
	"github.com/valyala/fasthttp"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// ============================================================================
// Git binary availability check
// ============================================================================

// gitBinaryPath holds the resolved path to the git binary, or empty if unavailable.
var gitBinaryPath string

func init() {
	gitBinaryPath, _ = exec.LookPath("git")
}

// CheckGitAvailability returns true if the git binary is available on PATH.
func CheckGitAvailability() bool {
	return gitBinaryPath != ""
}

// ============================================================================
// SkillsServingHandler — public (no auth) marketplace and download endpoints
// ============================================================================

// SkillsServingHandler serves skill marketplace catalogs, plugin manifests,
// composed SKILL.md files, individual file content, tarballs, and zip archives.
// All routes are public — intentionally NOT wrapped with auth middlewares.
type SkillsServingHandler struct {
	store        configstore.ConfigStore
	objectStore  objectstore.ObjectStore // nullable — may not be configured
	gitAvailable bool
}

// NewSkillsServingHandler creates a new SkillsServingHandler.
// Returns nil if store is nil.
func NewSkillsServingHandler(store configstore.ConfigStore, objectStore objectstore.ObjectStore) *SkillsServingHandler {
	if store == nil {
		return nil
	}
	return &SkillsServingHandler{store: store, objectStore: objectStore, gitAvailable: CheckGitAvailability()}
}

// RegisterRoutes registers public serving endpoints.
// These routes are intentionally NOT wrapped with auth middlewares — marketplace
// URLs cannot carry credentials securely.
func (h *SkillsServingHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	// Git-based marketplace routes only registered when git binary is available,
	// since Claude Code and Codex require git clone support.
	if h.gitAvailable {
		// Claude Code marketplace
		r.GET("/api/skills/serve/claude-code/.claude-plugin/marketplace.json", h.claudeCodeMarketplace)
		r.GET("/api/skills/serve/claude-code/plugins/{skill-name}/download.tar.gz", h.claudeCodeTarball)

		// Codex marketplace — Codex expects .agents/plugins/marketplace.json
		r.GET("/api/skills/serve/codex/.agents/plugins/marketplace.json", h.codexMarketplace)
		r.GET("/api/skills/serve/codex/plugins/{skill-name}/download.tar.gz", h.codexTarball)

		// Codex marketplace git routes (Codex clones the marketplace URL itself)
		codexMktBase := "/api/skills/serve/codex"
		r.GET(codexMktBase+"/info/refs", h.codexMarketplaceGit())
		r.POST(codexMktBase+"/git-upload-pack", h.codexMarketplaceGit())

		// Git smart HTTP serving (shared by both Claude Code and Codex)
		for _, harness := range []string{"claude-code", "codex"} {
			h.registerGitRoutes(r, harness)
		}
	} else {
		logger.Warn("git binary not found — Claude Code / Codex marketplace routes disabled")
	}

	// Generic download (always available, no git required)
	r.GET("/api/skills/serve/all/download.zip", h.allSkillsZipDownload)
	r.GET("/api/skills/serve/{skill-name}/download.zip", h.genericZipDownload)
	r.GET("/api/skills/serve/{skill-name}/files/{filepath:*}", h.genericFileDownload)
}

// pluginNamePrefix is prepended to all skill names when exposed as marketplace plugins.
const pluginNamePrefix = "bifrost-"

// allSkillsPluginName is the name of the bundled "all skills" plugin.
const allSkillsPluginName = pluginNamePrefix + "all-skills"

// ============================================================================
// Marketplace JSON generation
// ============================================================================

// claudeCodeMarketplace generates GET /api/skills/serve/claude-code/.claude-plugin/marketplace.json
func (h *SkillsServingHandler) claudeCodeMarketplace(ctx *fasthttp.RequestCtx) {
	skills, err := h.listAllSkills(ctx)
	if err != nil {
		return
	}

	allSkillsVersion := "0.0.0"
	if len(skills) > 0 {
		allSkillsVersion, err = h.store.GetAllSkillsVersion(ctx)
		if err != nil {
			logger.Error("all-skills: failed to get version: %v", err)
			SendError(ctx, fasthttp.StatusInternalServerError, "failed to get all-skills version")
			return
		}
	}

	baseURL := h.resolveBaseURL(ctx)
	plugins := make([]map[string]any, 0, len(skills)+1)
	for _, skillSummary := range skills {
		plugins = append(plugins, map[string]any{
			"name":        pluginNamePrefix + skillSummary.Name,
			"description": skillSummary.Description,
			"version":     skillSummary.LatestVersion,
			"source": map[string]any{
				"source": "url",
				"url":    baseURL + "/api/skills/serve/claude-code/plugins/" + pluginNamePrefix + skillSummary.Name,
			},
		})
	}
	// Bundled "all skills" plugin
	if len(skills) > 0 {
		plugins = append(plugins, map[string]any{
			"name":        allSkillsPluginName,
			"description": "All Bifrost skills bundled in a single plugin.",
			"version":     allSkillsVersion,
			"source": map[string]any{
				"source": "url",
				"url":    baseURL + "/api/skills/serve/claude-code/plugins/" + allSkillsPluginName,
			},
		})
	}

	result := map[string]any{
		"name": "bifrost-skills",
		"owner": map[string]any{
			"name": "Bifrost Gateway",
		},
		"plugins": plugins,
	}

	SendJSON(ctx, result)
}

// codexMarketplace generates GET /api/skills/serve/codex/.codex-plugin/marketplace.json
func (h *SkillsServingHandler) codexMarketplace(ctx *fasthttp.RequestCtx) {
	marketplaceJSON, err := h.buildCodexMarketplaceJSON(ctx)
	if err != nil {
		return // error already sent
	}
	ctx.SetContentType("application/json")
	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetBody(marketplaceJSON)
}

// buildCodexMarketplaceJSON builds the Codex marketplace JSON bytes.
// Shared by both the plain HTTP endpoint and the git-clone endpoint.
func (h *SkillsServingHandler) buildCodexMarketplaceJSON(ctx *fasthttp.RequestCtx) ([]byte, error) {
	skills, err := h.listAllSkills(ctx)
	if err != nil {
		return nil, err
	}

	allSkillsVersion := "0.0.0"
	if len(skills) > 0 {
		allSkillsVersion, err = h.store.GetAllSkillsVersion(ctx)
		if err != nil {
			logger.Error("all-skills: failed to get version: %v", err)
			SendError(ctx, fasthttp.StatusInternalServerError, "failed to get all-skills version")
			return nil, err
		}
	}

	baseURL := h.resolveBaseURL(ctx)
	plugins := make([]map[string]any, 0, len(skills)+1)
	for _, skillSummary := range skills {
		plugins = append(plugins, map[string]any{
			"name":        pluginNamePrefix + skillSummary.Name,
			"description": skillSummary.Description,
			"version":     skillSummary.LatestVersion,
			"source": map[string]any{
				"source": "url",
				"url":    baseURL + "/api/skills/serve/codex/plugins/" + pluginNamePrefix + skillSummary.Name,
			},
			"policy": map[string]any{
				"installation":   "AVAILABLE",
				"authentication": "ON_INSTALL",
			},
			"category": "Productivity",
		})
	}
	// Bundled "all skills" plugin
	if len(skills) > 0 {
		plugins = append(plugins, map[string]any{
			"name":        allSkillsPluginName,
			"description": "All Bifrost skills bundled in a single plugin.",
			"version":     allSkillsVersion,
			"source": map[string]any{
				"source": "url",
				"url":    baseURL + "/api/skills/serve/codex/plugins/" + allSkillsPluginName,
			},
			"policy": map[string]any{
				"installation":   "AVAILABLE",
				"authentication": "ON_INSTALL",
			},
			"category": "Productivity",
		})
	}

	result := map[string]any{
		"name": "bifrost-skills",
		"interface": map[string]any{
			"displayName": "Bifrost Skills",
		},
		"plugins": plugins,
	}

	return json.MarshalIndent(result, "", "  ")
}

// ============================================================================
// Git repository abstraction
// ============================================================================

// GitRepoFile represents a single file to include in a generated git repository.
type GitRepoFile struct {
	Path    string // relative path within the repo (e.g. ".claude-plugin/plugin.json")
	Content []byte
}

// GitRepoSpec describes the full contents of a git repository to be built.
// All git serving paths (plugin install, marketplace clone) produce a
// GitRepoSpec and then feed it through the same buildGitRepo → serveGitRepo
// pipeline.
type GitRepoSpec struct {
	Files []GitRepoFile
	Label string // human-readable label for error logs (e.g. skill name)
}

// buildGitRepo creates an in-memory bare git repository from a GitRepoSpec.
func buildGitRepo(spec *GitRepoSpec) (*memory.Storage, error) {
	storage := memory.NewStorage()
	wt := memfs.New()
	repo, err := git.Init(storage, wt)
	if err != nil {
		return nil, fmt.Errorf("git init: %w", err)
	}

	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName("main"))
	err = storage.SetReference(headRef)
	if err != nil {
		return nil, fmt.Errorf("set HEAD: %w", err)
	}

	for _, f := range spec.Files {
		if err := writeBillyFile(wt, f.Path, f.Content); err != nil {
			return nil, fmt.Errorf("write %s: %w", f.Path, err)
		}
	}

	w, err := repo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("get worktree: %w", err)
	}
	w.Add(".")
	_, err = w.Commit("serve "+spec.Label, &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Bifrost Gateway",
			Email: "bifrost@getbifrost.ai",
			When:  time.Now(),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return storage, nil
}

// ============================================================================
// Git repo spec assemblers
// ============================================================================

// assemblePluginRepoSpec builds a GitRepoSpec for a single skill plugin.
// Layout:
//
//	.<harness>-plugin/plugin.json
//	skills/<skill-name>/SKILL.md
//	skills/<skill-name>/<file-path>...
func (h *SkillsServingHandler) assemblePluginRepoSpec(ctx context.Context, skill *tables.TableSkill, harness string) (*GitRepoSpec, error) {
	var files []GitRepoFile

	// Plugin manifest
	manifestDir := manifestDirName(harness)
	manifestJSON, err := json.MarshalIndent(buildPluginManifest(skill, harness), "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal plugin.json: %w", err)
	}
	files = append(files, GitRepoFile{Path: path.Join(manifestDir, "plugin.json"), Content: manifestJSON})

	// SKILL.md
	files = append(files, GitRepoFile{
		Path:    path.Join("skills", skill.Name, "SKILL.md"),
		Content: []byte(composeSkillMD(skill)),
	})

	// Attached files
	for i := range skill.Files {
		file := &skill.Files[i]
		data, err := fetchFileContentForArchive(ctx, file, h.objectStore)
		if err != nil {
			logger.Error("skill %s: repo spec: failed to fetch file %s: %v", skill.Name, file.Path, err)
			continue
		}
		files = append(files, GitRepoFile{
			Path:    path.Join("skills", buildSkillFilePath(skill.Name, file)),
			Content: data,
		})
	}

	return &GitRepoSpec{Files: files, Label: skill.Name + " " + skill.LatestVersion}, nil
}

// assembleAllSkillsRepoSpec builds a GitRepoSpec bundling every skill into one plugin.
// Layout:
//
//	.<harness>-plugin/plugin.json
//	skills/<skill-name>/SKILL.md   (for each skill)
//	skills/<skill-name>/<files>... (for each skill)
func (h *SkillsServingHandler) assembleAllSkillsRepoSpec(ctx context.Context, harness string) (*GitRepoSpec, error) {
	skills, _, err := h.store.ListSkills(ctx, configstore.SkillListQueryParams{Limit: 10000})
	if err != nil {
		return nil, fmt.Errorf("list skills: %w", err)
	}
	version, err := h.store.GetAllSkillsVersion(ctx)
	if err != nil {
		return nil, fmt.Errorf("get all-skills version: %w", err)
	}

	var repoFiles []GitRepoFile

	// Plugin manifest — reuse buildPluginManifest with a synthetic skill record
	allSkillsSkill := &tables.TableSkill{
		Name:          strings.TrimPrefix(allSkillsPluginName, pluginNamePrefix),
		Description:   "All Bifrost skills bundled in a single plugin.",
		LatestVersion: version,
	}
	manifestDir := manifestDirName(harness)
	manifestJSON, _ := json.MarshalIndent(buildPluginManifest(allSkillsSkill, harness), "", "  ")
	repoFiles = append(repoFiles, GitRepoFile{Path: path.Join(manifestDir, "plugin.json"), Content: manifestJSON})

	// Each skill's SKILL.md + files
	for i := range skills {
		// Fetch full skill with blob data
		full, err := h.store.GetSkillByName(ctx, skills[i].Name)
		if err != nil {
			logger.Error("all-skills: failed to get skill %s: %v", skills[i].Name, err)
			continue
		}
		repoFiles = append(repoFiles, GitRepoFile{
			Path:    path.Join("skills", full.Name, "SKILL.md"),
			Content: []byte(composeSkillMD(full)),
		})
		for j := range full.Files {
			file := &full.Files[j]
			data, err := fetchFileContentForArchive(ctx, file, h.objectStore)
			if err != nil {
				logger.Error("all-skills: failed to fetch file %s/%s: %v", full.Name, file.Path, err)
				continue
			}
			repoFiles = append(repoFiles, GitRepoFile{
				Path:    path.Join("skills", buildSkillFilePath(full.Name, file)),
				Content: data,
			})
		}
	}

	return &GitRepoSpec{Files: repoFiles, Label: "all-skills"}, nil
}

// assembleMarketplaceRepoSpec builds a GitRepoSpec for a marketplace git repo.
// Claude Code: .claude-plugin/marketplace.json
// Codex:       .agents/plugins/marketplace.json
func assembleMarketplaceRepoSpec(marketplaceJSON []byte, harness string) *GitRepoSpec {
	var manifestPath string
	switch harness {
	case "codex":
		manifestPath = ".agents/plugins/marketplace.json"
	default:
		manifestPath = path.Join(manifestDirName(harness), "marketplace.json")
	}
	return &GitRepoSpec{
		Files: []GitRepoFile{
			{Path: manifestPath, Content: marketplaceJSON},
		},
		Label: harness + "-marketplace",
	}
}

// ============================================================================
// Git smart HTTP serving via direct git upload-pack
// ============================================================================

// registerGitRoutes registers Git smart HTTP routes for a given harness.
func (h *SkillsServingHandler) registerGitRoutes(r *router.Router, harness string) {
	base := "/api/skills/serve/" + harness + "/plugins/{skill-name}"
	r.GET(base+"/info/refs", h.servePluginGit(harness))
	r.POST(base+"/git-upload-pack", h.servePluginGit(harness))
}

// servePluginGit handles git smart HTTP for per-plugin repos.
// Plugin names in the URL are prefixed with "bifrost-". The special name
// "bifrost-all-skills" serves a bundled repo containing every skill.
func (h *SkillsServingHandler) servePluginGit(harness string) fasthttp.RequestHandler {
	return func(ctx *fasthttp.RequestCtx) {
		rawName := ""
		if val := ctx.UserValue("skill-name"); val != nil {
			rawName, _ = val.(string)
		}
		if rawName == "" {
			SendError(ctx, fasthttp.StatusBadRequest, "skill name is required")
			return
		}

		repoBase := "/api/skills/serve/" + harness + "/plugins/" + rawName

		// Handle the bundled "all skills" plugin.
		if rawName == allSkillsPluginName {
			spec, err := h.assembleAllSkillsRepoSpec(ctx, harness)
			if err != nil {
				logger.Error("all-skills: failed to assemble repo spec: %v", err)
				SendError(ctx, fasthttp.StatusInternalServerError, "failed to prepare all-skills plugin")
				return
			}
			serveGitRepo(ctx, spec, repoBase)
			return
		}

		// Strip the "bifrost-" prefix to look up the actual skill name.
		skillName := strings.TrimPrefix(rawName, pluginNamePrefix)
		skill, err := h.store.GetSkillByName(ctx, skillName)
		if err != nil {
			if errors.Is(err, configstore.ErrNotFound) {
				SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("skill %q not found", skillName))
				return
			}
			logger.Error("failed to get skill %s: %v", skillName, err)
			SendError(ctx, fasthttp.StatusInternalServerError, "failed to retrieve skill")
			return
		}

		spec, err := h.assemblePluginRepoSpec(ctx, skill, harness)
		if err != nil {
			logger.Error("skill %s: failed to assemble repo spec: %v", skill.Name, err)
			SendError(ctx, fasthttp.StatusInternalServerError, "failed to prepare plugin git repository")
			return
		}

		serveGitRepo(ctx, spec, repoBase)
	}
}

// codexMarketplaceGit returns a handler that serves the Codex marketplace as a
// git repo. Codex clones the marketplace URL itself (unlike Claude Code which
// fetches marketplace.json as plain HTTP).
func (h *SkillsServingHandler) codexMarketplaceGit() fasthttp.RequestHandler {
	repoBase := "/api/skills/serve/codex"
	return func(ctx *fasthttp.RequestCtx) {
		marketplaceJSON, err := h.buildCodexMarketplaceJSON(ctx)
		if err != nil {
			return // error already sent
		}

		spec := assembleMarketplaceRepoSpec(marketplaceJSON, "codex")
		serveGitRepo(ctx, spec, repoBase)
	}
}

// serveGitRepo builds a git repo from a spec and serves it via direct git
// upload-pack calls (inspired by go-git-http pattern). No CGI layer needed.
func serveGitRepo(ctx *fasthttp.RequestCtx, spec *GitRepoSpec, repoBase string) {
	storage, err := buildGitRepo(spec)
	if err != nil {
		logger.Error("%s: failed to build git repo: %v", spec.Label, err)
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to build git repository")
		return
	}

	tempDir, err := exportToTempBareRepo(storage)
	if err != nil {
		logger.Error("%s: failed to export bare repo: %v", spec.Label, err)
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to prepare git repository")
		return
	}
	defer os.RemoveAll(tempDir)

	pathInfo := strings.TrimPrefix(string(ctx.Path()), repoBase)
	if strings.HasSuffix(pathInfo, "/info/refs") {
		serveInfoRefs(ctx, tempDir, spec.Label)
	} else if strings.HasSuffix(pathInfo, "/git-upload-pack") {
		serveUploadPack(ctx, tempDir, spec.Label)
	} else {
		SendError(ctx, fasthttp.StatusNotFound, "unknown git endpoint")
	}
}

// serveInfoRefs handles GET /info/refs?service=git-upload-pack by running
// `git upload-pack --stateless-rpc --advertise-refs` and wrapping the output
// in the git smart HTTP advertisement format.
func serveInfoRefs(ctx *fasthttp.RequestCtx, repoDir, label string) {
	serviceName := string(ctx.QueryArgs().Peek("service"))
	if serviceName == "" {
		serviceName = "git-upload-pack"
	}

	// Only upload-pack is supported (read-only serving).
	if serviceName != "git-upload-pack" {
		SendError(ctx, fasthttp.StatusForbidden, "service not available")
		return
	}

	cmd := exec.Command(gitBinaryPath, "upload-pack", "--stateless-rpc", "--advertise-refs", ".") //nolint:gosec // gitBinaryPath is from exec.LookPath
	cmd.Dir = repoDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		logger.Error("%s: git upload-pack --advertise-refs failed: %v, stderr: %s", label, err, stderr.String())
		SendError(ctx, fasthttp.StatusInternalServerError, "git info/refs failed")
		return
	}

	ctx.Response.Header.Set("Cache-Control", "no-cache")
	ctx.SetContentType(fmt.Sprintf("application/x-%s-advertisement", serviceName))
	ctx.SetStatusCode(fasthttp.StatusOK)

	// Write pkt-line service announcement header, then the advertised refs.
	ctx.Write(pktLine("# service=" + serviceName + "\n")) //nolint:errcheck
	ctx.Write(pktFlush())                                 //nolint:errcheck
	ctx.Write(stdout.Bytes())                             //nolint:errcheck
}

// serveUploadPack handles POST /git-upload-pack by piping the request body
// into `git upload-pack --stateless-rpc` and streaming the output back.
func serveUploadPack(ctx *fasthttp.RequestCtx, repoDir, label string) {
	cmd := exec.Command(gitBinaryPath, "upload-pack", "--stateless-rpc", ".") //nolint:gosec // gitBinaryPath is from exec.LookPath
	cmd.Dir = repoDir
	cmd.Stdin = bytes.NewReader(ctx.PostBody())

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		logger.Error("%s: git upload-pack failed: %v, stderr: %s", label, err, stderr.String())
		SendError(ctx, fasthttp.StatusInternalServerError, "git upload-pack failed")
		return
	}

	ctx.Response.Header.Set("Cache-Control", "no-cache")
	ctx.SetContentType("application/x-git-upload-pack-result")
	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetBody(stdout.Bytes())
}

// pktLine encodes a string as a git pkt-line (4-hex-digit length prefix + data).
func pktLine(s string) []byte {
	return []byte(fmt.Sprintf("%04x%s", len(s)+4, s))
}

// pktFlush returns the git pkt-line flush packet.
func pktFlush() []byte {
	return []byte("0000")
}

// exportToTempBareRepo exports an in-memory go-git storage to a temporary bare
// git repository on disk, suitable for use with `git http-backend`.
func exportToTempBareRepo(memStorage *memory.Storage) (string, error) {
	tempDir, err := os.MkdirTemp("", "bifrost-skill-*")
	if err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}

	// Create a bare repo on disk.
	fs := osfs.New(tempDir)
	dot, err := fs.Chroot(".")
	if err != nil {
		os.RemoveAll(tempDir)
		return "", fmt.Errorf("chroot: %w", err)
	}
	diskStorage := filesystem.NewStorage(dot, cache.NewObjectLRUDefault())

	if err := diskStorage.Init(); err != nil {
		os.RemoveAll(tempDir)
		return "", fmt.Errorf("init bare repo: %w", err)
	}

	// Copy all objects from memory to disk storage.
	for _, objType := range []plumbing.ObjectType{plumbing.CommitObject, plumbing.TreeObject, plumbing.BlobObject, plumbing.TagObject} {
		iter, err := memStorage.IterEncodedObjects(objType)
		if err != nil {
			continue
		}
		_ = iter.ForEach(func(obj plumbing.EncodedObject) error {
			_, err := diskStorage.SetEncodedObject(obj)
			return err
		})
	}

	// Copy all references from memory to disk storage.
	refIter, err := memStorage.IterReferences()
	if err == nil {
		_ = refIter.ForEach(func(ref *plumbing.Reference) error {
			return diskStorage.SetReference(ref)
		})
	}

	// Also copy HEAD.
	head, err := memStorage.Reference(plumbing.HEAD)
	if err == nil {
		_ = diskStorage.SetReference(head)
	}

	return tempDir, nil
}

// writeBillyFile writes data to a file in a billy in-memory filesystem,
// creating parent directories as needed.
func writeBillyFile(fs billy.Filesystem, filePath string, data []byte) error {
	dir := path.Dir(filePath)
	if dir != "." && dir != "/" {
		if err := fs.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	f, err := fs.Create(filePath)
	if err != nil {
		return fmt.Errorf("create %s: %w", filePath, err)
	}
	defer f.Close()
	_, err = f.Write(data)
	if err != nil {
		return fmt.Errorf("write %s: %w", filePath, err)
	}
	return nil
}

// ============================================================================
// Plugin manifest generation
// ============================================================================

// manifestDirName returns the plugin manifest directory name for a harness.
// Claude Code uses ".claude-plugin", Codex uses ".codex-plugin".
func manifestDirName(harness string) string {
	switch harness {
	case "claude-code":
		return ".claude-plugin"
	case "codex":
		return ".codex-plugin"
	default:
		return "." + harness + "-plugin"
	}
}

// buildPluginManifest creates the plugin.json for a given harness.
func buildPluginManifest(skill *tables.TableSkill, harness string) map[string]any {
	manifest := map[string]any{
		"name":        pluginNamePrefix + skill.Name,
		"description": skill.Description,
		"version":     skill.LatestVersion,
	}
	if harness == "codex" {
		// Format name for display: replace hyphens with spaces, title case
		displayName := strings.ReplaceAll(skill.Name, "-", " ")
		displayName = cases.Title(language.English).String(displayName) // simple title case is fine here
		manifest["interface"] = map[string]any{
			"displayName":      displayName,
			"shortDescription": skill.Description,
			"category":         "Productivity",
		}
	}
	return manifest
}

// ============================================================================
// SKILL.md composition
// ============================================================================

// composeSkillMD builds the full SKILL.md from DB fields:
// YAML frontmatter (standard fields → extra_frontmatter → metadata) + body.
func composeSkillMD(skill *tables.TableSkill) string {
	var b strings.Builder
	b.WriteString("---\n")

	// Standard fields first
	b.WriteString("name: ")
	b.WriteString(yamlScalar(skill.Name))
	b.WriteString("\n")

	b.WriteString("description: ")
	b.WriteString(yamlScalar(skill.Description))
	b.WriteString("\n")

	if skill.License != nil && *skill.License != "" {
		b.WriteString("license: ")
		b.WriteString(yamlScalar(*skill.License))
		b.WriteString("\n")
	}
	if skill.Compatibility != nil && *skill.Compatibility != "" {
		b.WriteString("compatibility: ")
		b.WriteString(yamlScalar(*skill.Compatibility))
		b.WriteString("\n")
	}
	if skill.AllowedTools != nil && *skill.AllowedTools != "" {
		b.WriteString("allowed-tools: ")
		b.WriteString(yamlScalar(*skill.AllowedTools))
		b.WriteString("\n")
	}

	// Extra frontmatter fields spread as top-level YAML keys
	// Sort keys for deterministic output
	if len(skill.ExtraFrontmatter) > 0 {
		extraKeys := make([]string, 0, len(skill.ExtraFrontmatter))
		for k := range skill.ExtraFrontmatter {
			if !configstore.IsSkillReservedFrontmatterField(k) {
				extraKeys = append(extraKeys, k)
			}
		}
		sort.Strings(extraKeys)
		for _, k := range extraKeys {
			writeYAMLKeyValue(&b, k, skill.ExtraFrontmatter[k], 0)
		}
	}

	// Metadata as nested block
	if len(skill.Metadata) > 0 {
		b.WriteString("metadata:\n")
		metaKeys := make([]string, 0, len(skill.Metadata))
		for k := range skill.Metadata {
			metaKeys = append(metaKeys, k)
		}
		sort.Strings(metaKeys)
		for _, k := range metaKeys {
			b.WriteString("  ")
			b.WriteString(k)
			b.WriteString(": ")
			b.WriteString(yamlMetadataScalar(skill.Metadata[k]))
			b.WriteString("\n")
		}
	}

	b.WriteString("---\n")
	b.WriteString(skill.SkillMDBody)

	return b.String()
}

// yamlScalar returns a YAML-safe representation of a string value.
// Quotes the value if it contains special characters.
func yamlScalar(s string) string {
	if s == "" {
		return `""`
	}
	// Quote if the value contains YAML-special characters or starts/ends with whitespace
	needsQuote := false
	for _, ch := range s {
		if ch == ':' || ch == '#' || ch == '\n' || ch == '\r' || ch == '"' || ch == '\'' || ch == '{' || ch == '}' || ch == '[' || ch == ']' || ch == ',' || ch == '&' || ch == '*' || ch == '!' || ch == '|' || ch == '>' || ch == '%' || ch == '@' || ch == '`' {
			needsQuote = true
			break
		}
	}
	if strings.HasPrefix(s, " ") || strings.HasSuffix(s, " ") {
		needsQuote = true
	}
	// Also quote strings that look like booleans or numbers
	lower := strings.ToLower(s)
	if lower == "true" || lower == "false" || lower == "null" || lower == "yes" || lower == "no" || lower == "on" || lower == "off" {
		needsQuote = true
	}
	if !needsQuote {
		return s
	}
	// Use double-quoted with escaping
	escaped := strings.ReplaceAll(s, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	escaped = strings.ReplaceAll(escaped, "\n", `\n`)
	escaped = strings.ReplaceAll(escaped, "\r", `\r`)
	return `"` + escaped + `"`
}

func yamlMetadataScalar(s string) string {
	return s
}

// writeYAMLKeyValue writes a key-value pair to a YAML builder.
// Handles nested maps and slices with indentation.
func writeYAMLKeyValue(b *strings.Builder, key string, value any, indent int) {
	prefix := strings.Repeat("  ", indent)
	switch v := value.(type) {
	case map[string]any:
		b.WriteString(prefix)
		b.WriteString(key)
		b.WriteString(":\n")
		subKeys := make([]string, 0, len(v))
		for k := range v {
			subKeys = append(subKeys, k)
		}
		sort.Strings(subKeys)
		for _, sk := range subKeys {
			writeYAMLKeyValue(b, sk, v[sk], indent+1)
		}
	case []any:
		b.WriteString(prefix)
		b.WriteString(key)
		b.WriteString(":\n")
		for _, item := range v {
			b.WriteString(prefix)
			b.WriteString("  - ")
			b.WriteString(yamlScalar(fmt.Sprintf("%v", item)))
			b.WriteString("\n")
		}
	default:
		b.WriteString(prefix)
		b.WriteString(key)
		b.WriteString(": ")
		switch val := value.(type) {
		case bool:
			if val {
				b.WriteString("true")
			} else {
				b.WriteString("false")
			}
		case float64:
			// JSON numbers unmarshal as float64
			if val == float64(int64(val)) {
				fmt.Fprintf(b, "%d", int64(val))
			} else {
				fmt.Fprintf(b, "%g", val)
			}
		case string:
			b.WriteString(yamlScalar(val))
		default:
			b.WriteString(yamlScalar(fmt.Sprintf("%v", value)))
		}
		b.WriteString("\n")
	}
}

// ============================================================================
// Individual file serving
// ============================================================================

// genericFileDownload serves GET /api/skills/serve/{skill-name}/files/{filepath:*}
func (h *SkillsServingHandler) genericFileDownload(ctx *fasthttp.RequestCtx) {
	h.doServeFileContent(ctx)
}

func (h *SkillsServingHandler) doServeFileContent(ctx *fasthttp.RequestCtx) {
	skill, ok := h.lookupSkillByPathParam(ctx)
	if !ok {
		return
	}

	filePath := ""
	if val := ctx.UserValue("filepath"); val != nil {
		filePath, _ = val.(string)
	}
	if filePath == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "file path is required")
		return
	}

	// Find matching file
	var matchedFile *tables.TableSkillFile
	for i := range skill.Files {
		f := &skill.Files[i]
		if f.NormalizedPath() == filePath {
			matchedFile = f
			break
		}
	}

	if matchedFile == nil {
		SendError(ctx, fasthttp.StatusNotFound, "file not found")
		return
	}

	serveSkillFile(ctx, matchedFile, h.objectStore, skill.Name)
}

// serveSkillFile streams file content based on source type.
func serveSkillFile(ctx *fasthttp.RequestCtx, file *tables.TableSkillFile, objStore objectstore.ObjectStore, skillName string) {
	if file.MimeType != "" {
		ctx.SetContentType(file.MimeType)
	} else {
		ctx.SetContentType("application/octet-stream")
	}
	ctx.Response.Header.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, path.Base(file.Path)))

	switch file.SourceType {
	case tables.SkillSourceTypeURL:
		if file.SourceURL == nil || *file.SourceURL == "" {
			logger.Error("skill %s file %s: url source has no source_url", skillName, file.Path)
			SendError(ctx, fasthttp.StatusInternalServerError, "file source URL not configured")
			return
		}
		resp, err := http.Get(*file.SourceURL) //nolint:gosec // URL is user-configured
		if err != nil {
			logger.Error("skill %s file %s: failed to fetch from URL %s: %v", skillName, file.Path, *file.SourceURL, err)
			SendError(ctx, fasthttp.StatusBadGateway, "failed to fetch file from source URL")
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			logger.Error("skill %s file %s: URL %s returned status %d", skillName, file.Path, *file.SourceURL, resp.StatusCode)
			SendError(ctx, fasthttp.StatusBadGateway, fmt.Sprintf("source URL returned status %d", resp.StatusCode))
			return
		}
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			logger.Error("skill %s file %s: failed to read URL response body: %v", skillName, file.Path, err)
			SendError(ctx, fasthttp.StatusBadGateway, "failed to read source URL response")
			return
		}
		ctx.SetStatusCode(fasthttp.StatusOK)
		ctx.SetBody(data)

	case tables.SkillSourceTypeFilepath:
		if file.SourceFilepath == nil || *file.SourceFilepath == "" {
			logger.Error("skill %s file %s: filepath source has no source_filepath", skillName, file.Path)
			SendError(ctx, fasthttp.StatusInternalServerError, "file source filepath not configured")
			return
		}
		data, err := os.ReadFile(*file.SourceFilepath)
		if err != nil {
			logger.Error("skill %s file %s: failed to read filepath %s: %v", skillName, file.Path, *file.SourceFilepath, err)
			SendError(ctx, fasthttp.StatusInternalServerError, "failed to read file from source path")
			return
		}
		ctx.SetStatusCode(fasthttp.StatusOK)
		ctx.SetBody(data)

	case tables.SkillSourceTypeText, tables.SkillSourceTypeDataURL, tables.SkillSourceTypeUpload:
		data, err := fetchStoredFileContent(ctx, file, objStore)
		if err != nil {
			logger.Error("skill %s file %s: failed to retrieve stored content: %v", skillName, file.Path, err)
			SendError(ctx, fasthttp.StatusInternalServerError, "failed to retrieve file content")
			return
		}
		ctx.SetStatusCode(fasthttp.StatusOK)
		ctx.SetBody(data)

	default:
		SendError(ctx, fasthttp.StatusInternalServerError, "unsupported file source type")
	}
}

// fetchStoredFileContent retrieves file bytes from object storage or DB blob.
func fetchStoredFileContent(ctx context.Context, file *tables.TableSkillFile, objStore objectstore.ObjectStore) ([]byte, error) {
	// Try object storage first
	if objStore != nil && file.StorageKey != nil && *file.StorageKey != "" {
		data, err := objStore.Get(ctx, *file.StorageKey)
		if err != nil {
			return nil, fmt.Errorf("object store get: %w", err)
		}
		return data, nil
	}
	// Fall back to DB blob
	if file.Blob != nil && len(file.Blob.Data) > 0 {
		return file.Blob.Data, nil
	}
	if file.BlobID != nil && *file.BlobID != "" {
		return nil, fmt.Errorf("blob_id set but blob data not preloaded (blob_id=%s)", *file.BlobID)
	}
	return nil, fmt.Errorf("no storage_key or blob_id available for file %s", file.Path)
}

// fetchFileContentForArchive retrieves file bytes for archive generation.
// Same logic as serveSkillFile but returns bytes instead of writing to response.
func fetchFileContentForArchive(ctx context.Context, file *tables.TableSkillFile, objStore objectstore.ObjectStore) ([]byte, error) {
	switch file.SourceType {
	case tables.SkillSourceTypeURL:
		if file.SourceURL == nil || *file.SourceURL == "" {
			return nil, fmt.Errorf("url source has no source_url")
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, *file.SourceURL, nil) // URL is user-configured
		if err != nil {
			return nil, fmt.Errorf("new request for URL %s: %w", *file.SourceURL, err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetch URL %s: %w", *file.SourceURL, err)
		}
		defer resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("URL %s returned status %d", *file.SourceURL, resp.StatusCode)
		}
		return io.ReadAll(resp.Body)

	case tables.SkillSourceTypeFilepath:
		if file.SourceFilepath == nil || *file.SourceFilepath == "" {
			return nil, fmt.Errorf("filepath source has no source_filepath")
		}
		return os.ReadFile(*file.SourceFilepath)

	case tables.SkillSourceTypeText, tables.SkillSourceTypeDataURL, tables.SkillSourceTypeUpload:
		// Handle transient pre-storage content (available at create/update time
		// before files are persisted to blob/object storage).
		if file.InlineContent != nil && *file.InlineContent != "" {
			return []byte(*file.InlineContent), nil
		}
		if file.DataURL != nil && *file.DataURL != "" {
			parts := strings.SplitN(*file.DataURL, ",", 2)
			if len(parts) == 2 {
				if strings.Contains(parts[0], ";base64") {
					data, err := base64.StdEncoding.DecodeString(parts[1])
					if err == nil {
						return data, nil
					}
				} else {
					// Plain data URL (e.g. data:text/plain,hello)
					return []byte(parts[1]), nil
				}
			}
		}
		return fetchStoredFileContent(ctx, file, objStore)

	default:
		return nil, fmt.Errorf("unsupported source type %s", file.SourceType)
	}
}

// ============================================================================
// Tarball generation
// ============================================================================

// claudeCodeTarball serves GET /api/skills/serve/claude-code/plugins/{skill-name}/download.tar.gz
func (h *SkillsServingHandler) claudeCodeTarball(ctx *fasthttp.RequestCtx) {
	h.doTarball(ctx, "claude-code")
}

// codexTarball serves GET /api/skills/serve/codex/plugins/{skill-name}/download.tar.gz
func (h *SkillsServingHandler) codexTarball(ctx *fasthttp.RequestCtx) {
	h.doTarball(ctx, "codex")
}

func (h *SkillsServingHandler) doTarball(ctx *fasthttp.RequestCtx, harness string) {
	skill, ok := h.lookupSkillByPathParam(ctx)
	if !ok {
		return
	}

	data, err := buildPluginTarball(ctx, skill, harness, h.objectStore)
	if err != nil {
		logger.Error("skill %s: failed to build tarball: %v", skill.Name, err)
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to build plugin tarball")
		return
	}

	ctx.SetContentType("application/gzip")
	ctx.Response.Header.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-plugin.tar.gz"`, skill.Name))
	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetBody(data)
}

// buildPluginTarball creates a tar.gz archive with the plugin directory structure:
//
//	<skill-name>-plugin/
//	├── .<harness>-plugin/
//	│   └── plugin.json
//	└── skills/
//	    └── <skill-name>/
//	        ├── SKILL.md
//	        ├── scripts/...
//	        ├── references/...
//	        └── assets/...
func buildPluginTarball(ctx context.Context, skill *tables.TableSkill, harness string, objStore objectstore.ObjectStore) ([]byte, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	pluginDir := skill.Name + "-plugin"
	manifestDir := manifestDirName(harness)

	// Write plugin.json
	manifest := buildPluginManifest(skill, harness)
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal plugin.json: %w", err)
	}
	if err := addTarFile(tw, path.Join(pluginDir, manifestDir, "plugin.json"), manifestJSON); err != nil {
		return nil, err
	}

	// Write SKILL.md
	composed := composeSkillMD(skill)
	skillPath := path.Join(pluginDir, "skills", skill.Name, "SKILL.md")
	if err := addTarFile(tw, skillPath, []byte(composed)); err != nil {
		return nil, err
	}

	// Write files
	for i := range skill.Files {
		file := &skill.Files[i]
		data, err := fetchFileContentForArchive(ctx, file, objStore)
		if err != nil {
			logger.Error("skill %s: tarball: failed to fetch file %s: %v", skill.Name, file.Path, err)
			continue // skip files we can't fetch rather than failing the whole tarball
		}

		filePath := buildSkillFilePath(skill.Name, file)
		tarPath := path.Join(pluginDir, "skills", filePath)
		if err := addTarFile(tw, tarPath, data); err != nil {
			return nil, err
		}
	}

	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("close tar: %w", err)
	}
	if err := gw.Close(); err != nil {
		return nil, fmt.Errorf("close gzip: %w", err)
	}

	return buf.Bytes(), nil
}

func addTarFile(tw *tar.Writer, name string, data []byte) error {
	header := &tar.Header{
		Name: name,
		Mode: 0644,
		Size: int64(len(data)),
	}
	if err := tw.WriteHeader(header); err != nil {
		return fmt.Errorf("tar write header %s: %w", name, err)
	}
	if _, err := tw.Write(data); err != nil {
		return fmt.Errorf("tar write data %s: %w", name, err)
	}
	return nil
}

// ============================================================================
// Generic zip download
// ============================================================================

// allSkillsZipDownload serves GET /api/skills/serve/all/download.zip
// Returns a zip archive containing all skills.
func (h *SkillsServingHandler) allSkillsZipDownload(ctx *fasthttp.RequestCtx) {
	skills, err := h.listAllSkills(ctx)
	if err != nil {
		logger.Error("all-skills zip: failed to list skills: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to list skills")
		return
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	for _, s := range skills {
		full, err := h.store.GetSkillByName(ctx, s.Name)
		if err != nil {
			logger.Error("all-skills zip: failed to get skill %s: %v", s.Name, err)
			continue
		}
		composed := composeSkillMD(full)
		w, err := zw.Create(path.Join(full.Name, "SKILL.md"))
		if err != nil {
			continue
		}
		w.Write([]byte(composed))

		for i := range full.Files {
			file := &full.Files[i]
			data, err := fetchFileContentForArchive(ctx, file, h.objectStore)
			if err != nil {
				continue
			}
			filePath := buildSkillFilePath(full.Name, file)
			fw, err := zw.Create(filePath)
			if err != nil {
				continue
			}
			fw.Write(data)
		}
	}

	if err := zw.Close(); err != nil {
		logger.Error("all-skills zip: failed to close zip: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to build zip archive")
		return
	}

	ctx.SetContentType("application/zip")
	ctx.Response.Header.Set("Content-Disposition", `attachment; filename="all-skills.zip"`)
	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetBody(buf.Bytes())
}

// genericZipDownload serves GET /api/skills/serve/{skill-name}/download.zip
// Returns a raw Agent Skills directory zip without the plugin wrapper.
func (h *SkillsServingHandler) genericZipDownload(ctx *fasthttp.RequestCtx) {
	skill, ok := h.lookupSkillByPathParam(ctx)
	if !ok {
		return
	}

	data, err := buildSkillZip(ctx, skill, h.objectStore)
	if err != nil {
		logger.Error("skill %s: failed to build zip: %v", skill.Name, err)
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to build skill zip archive")
		return
	}

	ctx.SetContentType("application/zip")
	ctx.Response.Header.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.zip"`, skill.Name))
	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetBody(data)
}

// buildSkillZip creates a zip archive of the raw Agent Skills directory structure:
//
//	<skill-name>/
//	├── SKILL.md
//	├── scripts/...
//	├── references/...
//	└── assets/...
func buildSkillZip(ctx context.Context, skill *tables.TableSkill, objStore objectstore.ObjectStore) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	// Write SKILL.md
	composed := composeSkillMD(skill)
	w, err := zw.Create(path.Join(skill.Name, "SKILL.md"))
	if err != nil {
		return nil, fmt.Errorf("zip create SKILL.md: %w", err)
	}
	if _, err := w.Write([]byte(composed)); err != nil {
		return nil, fmt.Errorf("zip write SKILL.md: %w", err)
	}

	// Write files
	for i := range skill.Files {
		file := &skill.Files[i]
		data, err := fetchFileContentForArchive(ctx, file, objStore)
		if err != nil {
			logger.Error("skill %s: zip: failed to fetch file %s: %v", skill.Name, file.Path, err)
			continue // skip files we can't fetch
		}

		filePath := buildSkillFilePath(skill.Name, file)
		w, err := zw.Create(filePath)
		if err != nil {
			return nil, fmt.Errorf("zip create %s: %w", filePath, err)
		}
		if _, err := w.Write(data); err != nil {
			return nil, fmt.Errorf("zip write %s: %w", filePath, err)
		}
	}

	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("close zip: %w", err)
	}
	return buf.Bytes(), nil
}

// ============================================================================
// Helpers
// ============================================================================

// buildSkillFilePath computes the relative file path within the skill directory.
// e.g., <skill-name>/nested/path/file.py
func buildSkillFilePath(skillName string, file *tables.TableSkillFile) string {
	return path.Join(skillName, file.Path)
}

// lookupSkillByPathParam extracts the skill-name path parameter and fetches the skill.
func (h *SkillsServingHandler) lookupSkillByPathParam(ctx *fasthttp.RequestCtx) (*tables.TableSkill, bool) {
	val := ctx.UserValue("skill-name")
	if val == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "skill name is required")
		return nil, false
	}
	name, ok := val.(string)
	if !ok || name == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid skill name")
		return nil, false
	}

	skill, err := h.store.GetSkillByName(ctx, name)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("skill %q not found", name))
			return nil, false
		}
		logger.Error("failed to get skill %s: %v", name, err)
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to retrieve skill")
		return nil, false
	}
	return skill, true
}

// listAllSkills fetches all skills for marketplace generation.
func (h *SkillsServingHandler) listAllSkills(ctx *fasthttp.RequestCtx) ([]tables.TableSkill, error) {
	// Use a large limit to get all skills for the marketplace catalog
	skills, _, err := h.store.ListSkills(ctx, configstore.SkillListQueryParams{Limit: 10000})
	if err != nil {
		logger.Error("failed to list skills for marketplace: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to list skills")
		return nil, err
	}
	return skills, nil
}

// resolveBaseURL derives the base URL from the request's Host header and scheme.
func (h *SkillsServingHandler) resolveBaseURL(ctx *fasthttp.RequestCtx) string {
	scheme := string(ctx.Request.Header.Peek("X-Forwarded-Proto"))
	if scheme == "" {
		if ctx.IsTLS() {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}

	host := string(ctx.Request.Header.Peek("X-Forwarded-Host"))
	if host == "" {
		host = string(ctx.Host())
	}

	return scheme + "://" + host
}
