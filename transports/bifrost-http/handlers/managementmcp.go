package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mark3labs/mcp-go/server"
	"github.com/maximhq/bifrost/framework/openapimcp"
)

// The management MCP serves Bifrost's own /api surface as MCP tools on /v1/mcp.
// Every /api operation in the published OpenAPI spec becomes a tool, except the
// deprecated ones, the few on managementMCPDenied, the enterprise-only ones on an
// OSS gateway, and any this gateway does not route. The spec is read at the
// release's own tag, so a release exposes exactly the API it ships with.

const (
	// ManagementMCPSpecEnv overrides the spec source with a URL or a file path
	// (absolute, or relative to the app directory), for air-gapped installs.
	ManagementMCPSpecEnv = "BIFROST_MCP_SPEC"
	// ManagementMCPWritesEnv turns non-GET management tools on or off.
	ManagementMCPWritesEnv = "BIFROST_MCP_ALLOW_WRITES"
	// managementMCPWritesDefault applies when ManagementMCPWritesEnv is unset.
	managementMCPWritesDefault = false

	// managementMCPBaseURL is the base the synthesized tools address. The
	// executor serves every request in process, so the host is never dialed.
	managementMCPBaseURL = "http://bifrost.internal"

	managementMCPSpecRepoURL = "https://raw.githubusercontent.com/maximhq/bifrost"
	managementMCPSpecDir     = "docs/openapi/"
	managementMCPSpecFull    = "openapi.json"     // every operation
	managementMCPSpecOSS     = "openapi-oss.json" // enterprise-only operations removed
	// unsetBuildVersion is what main.Version holds when no -X ldflag set it.
	unsetBuildVersion = "v1.0.0"

	managementMCPLoadTimeout    = 30 * time.Second
	managementMCPRetryInitial   = 5 * time.Second
	managementMCPRetryMax       = 5 * time.Minute
	managementMCPUnavailableMsg = "management MCP spec unavailable"
)

// ManagementMCPEdition is the build the management MCP serves: it decides which
// spec is read and whether enterprise-only operations become tools.
type ManagementMCPEdition string

const (
	ManagementMCPEditionOSS        ManagementMCPEdition = "oss"
	ManagementMCPEditionEnterprise ManagementMCPEdition = "enterprise"
)

// managementMCPEnterpriseTag is the spec's marker for an enterprise-only
// operation (x-mint.metadata.tag), the one bundle.py --edition oss filters on.
const managementMCPEnterpriseTag = "Enterprise"

// managementMCPDestructiveExt marks a POST or PUT operation in the spec that
// destroys or invalidates state. Every DELETE is destructive without it.
const managementMCPDestructiveExt = "x-bifrost-destructive"

// managementMCPDenied lists the /api operations never served on /v1/mcp, by
// operationId. operationIds are a public contract, so a rename in the spec
// surfaces as an unknown entry here (logged at load, failed in tests).
var managementMCPDenied = map[string]bool{
	// Mint, trade or end dashboard credentials.
	"login": true, "logout": true, "startSetupSession": true, "issueWSTicket": true, "isAuthEnabled": true,
	// Browser steps of end-user OAuth and per-user-headers flows, authorized by one-time tokens.
	"handleOAuthCallback": true, "startPerUserOauthFlow": true, "getPerUserOauthFlow": true,
	"getPerUserHeadersFlow": true, "submitPerUserHeadersFlow": true,
	// Streams (SSE), which a request/response tool cannot carry.
	"warpSession": true,
	// Binary downloads and installer manifests: a model only sees "[binary response]".
	"downloadAllSkills": true, "downloadSkill": true, "downloadSkillFile": true,
	"getClaudeCodeSkillsMarketplace": true, "getCodexSkillsMarketplace": true,
}

// ManagementMCPOptions configures the management MCP.
type ManagementMCPOptions struct {
	SpecSources []string // URLs or file paths, tried in order; see ManagementMCPSpecSources
	AppDir      string   // relative file paths resolve against it
	Edition     ManagementMCPEdition
	AllowWrites bool                           // false registers GET operations only
	Served      func(method, path string) bool // nil serves every operation; else only those it accepts
	SpecClient  *http.Client                   // fetches a URL spec; carries the proxy policy
	ExecClient  *http.Client                   // serves tool calls through the /api chain
	Headers     openapimcp.HeaderResolver
}

// ManagementMCP holds the management MCP server, which becomes available once
// the spec has loaded. Until then Server returns nil.
type ManagementMCP struct {
	opts   ManagementMCPOptions
	server atomic.Pointer[server.MCPServer]
}

// NewManagementMCP returns an unloaded holder; call Start to load it.
func NewManagementMCP(opts ManagementMCPOptions) *ManagementMCP {
	return &ManagementMCP{opts: opts}
}

// Server returns the loaded server, or nil while the spec is unavailable.
func (m *ManagementMCP) Server() *server.MCPServer {
	if m == nil {
		return nil
	}
	return m.server.Load()
}

// Start loads the spec in the background, retrying with backoff until it
// succeeds or ctx ends. It never blocks startup.
func (m *ManagementMCP) Start(ctx context.Context) {
	go func() {
		delay := managementMCPRetryInitial
		for {
			err := m.Load(ctx)
			if err == nil {
				return
			}
			logger.Warn("[Management MCP] %s, retrying in %s: %v", managementMCPUnavailableMsg, delay, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			delay = min(delay*2, managementMCPRetryMax)
		}
	}()
}

// Load fetches, curates and synthesizes the spec once, and publishes the
// resulting server.
func (m *ManagementMCP) Load(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, managementMCPLoadTimeout)
	defer cancel()
	data, source, specURL, err := loadManagementSpec(ctx, m.opts)
	if err != nil {
		return err
	}
	srv, report, err := buildManagementMCPServer(data, specURL, m.opts)
	if err != nil {
		return err
	}
	logger.Info("[Management MCP] loaded %s spec from %s: %d tools exposed, %d operations excluded (%d not routed by this gateway), %d unsupported, writes %s",
		m.opts.edition(), source, report.exposed, report.excluded, report.unrouted, len(report.unsupported), enabledWord(m.opts.AllowWrites))
	for _, op := range report.unsupported {
		logger.Debug("[Management MCP] operation %s (%s %s) cannot be served: %s", op.OperationID, op.Method, op.Path, op.Reason)
	}
	for _, id := range report.unknown {
		logger.Warn("[Management MCP] denied operation %s is not in the spec", id)
	}
	m.server.Store(srv)
	return nil
}

// edition defaults an unset edition to OSS.
func (o ManagementMCPOptions) edition() ManagementMCPEdition {
	if o.Edition == "" {
		return ManagementMCPEditionOSS
	}
	return o.Edition
}

// managementMCPReleaseVersion matches a version stamped by a release build and
// captures its transports tag. Enterprise builds append "-enterprise" to the OSS
// release they are built on; local builds stamp values like "vdev-build".
var managementMCPReleaseVersion = regexp.MustCompile(`^(v\d+\.\d+\.\d+(?:-prerelease\d+)?)(?:-enterprise)?$`)

// ManagementMCPSpecSources lists the specs the gateway reads, in order: the env
// override alone when set; else this release's tag, or dev for a build without a
// release version. An OSS gateway reads the OSS spec first and falls back to the
// full one (filtered by the enterprise tag), which also covers a release cut
// before CI published the OSS spec.
func ManagementMCPSpecSources(version string, edition ManagementMCPEdition) []string {
	if override := strings.TrimSpace(os.Getenv(ManagementMCPSpecEnv)); override != "" {
		return []string{override}
	}
	ref := "refs/heads/dev"
	if m := managementMCPReleaseVersion.FindStringSubmatch(strings.TrimSpace(version)); m != nil && m[1] != unsetBuildVersion {
		ref = "refs/tags/transports/" + m[1]
	}
	base := managementMCPSpecRepoURL + "/" + ref + "/" + managementMCPSpecDir
	if edition == ManagementMCPEditionEnterprise {
		return []string{base + managementMCPSpecFull}
	}
	return []string{base + managementMCPSpecOSS, base + managementMCPSpecFull}
}

// ManagementMCPAllowWrites reads the writes switch.
func ManagementMCPAllowWrites() bool {
	if v, err := strconv.ParseBool(strings.TrimSpace(os.Getenv(ManagementMCPWritesEnv))); err == nil {
		return v
	}
	return managementMCPWritesDefault
}

// loadManagementSpec returns the first spec source that loads, which one it was,
// and the URL relative servers resolve against.
func loadManagementSpec(ctx context.Context, opts ManagementMCPOptions) ([]byte, string, string, error) {
	var errs []error
	for _, source := range opts.SpecSources {
		if source = strings.TrimSpace(source); source == "" {
			continue
		}
		data, specURL, err := loadManagementSpecSource(ctx, opts, source)
		if err == nil {
			return data, source, specURL, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", source, err))
	}
	if len(errs) == 0 {
		return nil, "", "", errors.New("no spec source configured")
	}
	return nil, "", "", errors.Join(errs...)
}

func loadManagementSpecSource(ctx context.Context, opts ManagementMCPOptions, source string) ([]byte, string, error) {
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		return openapimcp.LoadURL(ctx, opts.SpecClient, source)
	}
	baseDir := ""
	if !filepath.IsAbs(source) {
		baseDir = opts.AppDir
	}
	data, err := openapimcp.LoadFile(source, baseDir)
	return data, "", err
}

type managementMCPReport struct {
	exposed     int
	excluded    int
	unrouted    int // excluded because this gateway does not route them
	unsupported []openapimcp.UnsupportedOperation
	unknown     []string // managementMCPDenied entries the spec does not define
}

// buildManagementMCPServer turns spec bytes into the management MCP server.
func buildManagementMCPServer(data []byte, specURL string, opts ManagementMCPOptions) (*server.MCPServer, managementMCPReport, error) {
	var report managementMCPReport
	doc, err := openapimcp.Parse(data, openapimcp.ParseOptions{SpecURL: specURL})
	if err != nil {
		return nil, report, fmt.Errorf("parsing spec: %w", err)
	}
	var kept []openapimcp.Operation
	kept, report.unrouted, report.unknown = curateManagementOperations(doc.Operations, opts)
	report.excluded = len(doc.Operations) - len(kept)
	doc.Operations = kept
	syn, err := openapimcp.Synthesize(doc, openapimcp.SynthesizeOptions{})
	if err != nil {
		return nil, report, fmt.Errorf("synthesizing tools: %w", err)
	}
	report.unsupported = syn.Unsupported
	report.exposed = len(syn.Tools)
	for i := range syn.Tools {
		annotateManagementTool(&syn.Tools[i])
	}
	srv, err := openapimcp.BuildServer(openapimcp.ServerOptions{
		ClientName:      "management",
		Synthesis:       syn,
		BaseURL:         managementMCPBaseURL,
		BaseURLExplicit: true,
		HTTPClient:      opts.ExecClient,
		Headers:         opts.Headers,
		Logger:          logger,
	})
	if err != nil {
		return nil, report, fmt.Errorf("building server: %w", err)
	}
	return srv, report, nil
}

// curateManagementOperations keeps the /api operations that become tools: not
// deprecated, not denied, not enterprise-only on an OSS gateway, routed by this
// gateway and, when writes are off, GET only. It also returns how many were
// left out because the gateway does not route them, and the denied operationIds
// the spec does not define.
func curateManagementOperations(ops []openapimcp.Operation, opts ManagementMCPOptions) ([]openapimcp.Operation, int, []string) {
	seen := make(map[string]bool, len(managementMCPDenied))
	oss := opts.edition() == ManagementMCPEditionOSS
	var kept []openapimcp.Operation
	unrouted := 0
	for _, op := range ops {
		if managementMCPDenied[op.ID] {
			seen[op.ID] = true
			continue
		}
		if !strings.HasPrefix(op.Path, "/api/") || op.Deprecated || (!opts.AllowWrites && op.Method != http.MethodGet) {
			continue
		}
		if oss && managementMCPEnterpriseOnly(op) {
			continue
		}
		if opts.Served != nil && !opts.Served(op.Method, op.Path) {
			unrouted++
			continue
		}
		kept = append(kept, op)
	}
	var unknown []string
	for id := range managementMCPDenied {
		if !seen[id] {
			unknown = append(unknown, id)
		}
	}
	return kept, unrouted, unknown
}

// managementMCPEnterpriseOnly reports whether the spec marks op enterprise-only.
func managementMCPEnterpriseOnly(op openapimcp.Operation) bool {
	mint, _ := op.Extensions["x-mint"].(map[string]any)
	metadata, _ := mint["metadata"].(map[string]any)
	tag, _ := metadata["tag"].(string)
	return tag == managementMCPEnterpriseTag
}

// annotateManagementTool replaces the method-derived hints openapimcp sets for
// an arbitrary upstream: these tools act on this gateway only (closed world),
// and a POST or PUT is destructive only when the spec marks it so.
func annotateManagementTool(tool *openapimcp.Tool) {
	b := func(v bool) *bool { return &v }
	ann := &tool.Annotations
	ann.OpenWorldHint = b(false)
	switch tool.Operation.Method {
	case http.MethodGet:
		return
	case http.MethodDelete:
		ann.DestructiveHint = b(true)
	default:
		marked, _ := tool.Operation.Extensions[managementMCPDestructiveExt].(bool)
		ann.DestructiveHint = b(marked)
	}
}

func enabledWord(on bool) string {
	if on {
		return "enabled"
	}
	return "disabled"
}
