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

// The Bifrost MCP serves Bifrost's own HTTP surface as MCP tools on /v1/mcp.
// Every operation in the published OpenAPI spec becomes a tool, except the
// deprecated ones, the few on bifrostMCPDenied, the enterprise-only ones on an
// OSS gateway, and any this gateway does not route. With BIFROST_MCP_SPEC
// unset the spec is read from the dev branch; a non-empty value replaces that
// source.

const (
	// BifrostMCPSpecEnv overrides the spec source with a URL or a file path
	// (absolute, or relative to the app directory), for air-gapped installs.
	BifrostMCPSpecEnv = "BIFROST_MCP_SPEC"
	// BifrostMCPWritesEnv turns the non-GET /api tools (the gateway's own
	// mutations) on or off. Routes outside /api are served either way.
	BifrostMCPWritesEnv = "BIFROST_MCP_ALLOW_WRITES"
	// bifrostMCPWritesDefault applies when BifrostMCPWritesEnv is unset. Writes
	// are on: /api authorizes each call with the caller's own credential, exactly
	// as a direct call, and destructive operations carry destructiveHint so
	// clients confirm them.
	bifrostMCPWritesDefault = true

	// bifrostMCPBaseURL is the base the synthesized tools address. The
	// executor serves every request in process, so the host is never dialed.
	bifrostMCPBaseURL = "http://bifrost.internal"

	bifrostMCPSpecRepoURL = "https://raw.githubusercontent.com/maximhq/bifrost"
	bifrostMCPSpecDir     = "docs/openapi/"
	bifrostMCPSpecFull    = "openapi.json"     // every operation
	bifrostMCPSpecOSS     = "openapi-oss.json" // enterprise-only operations removed

	bifrostMCPLoadTimeout    = 30 * time.Second
	bifrostMCPRetryInitial   = 5 * time.Second
	bifrostMCPRetryMax       = 5 * time.Minute
	bifrostMCPUnavailableMsg = "Bifrost MCP spec unavailable"
)

// BifrostMCPEdition is the build the Bifrost MCP serves: it decides which
// spec is read and whether enterprise-only operations become tools.
type BifrostMCPEdition string

const (
	BifrostMCPEditionOSS        BifrostMCPEdition = "oss"
	BifrostMCPEditionEnterprise BifrostMCPEdition = "enterprise"
)

// bifrostMCPEnterpriseTag is the spec's marker for an enterprise-only
// operation (x-mint.metadata.tag), the one bundle.py --edition oss filters on.
const bifrostMCPEnterpriseTag = "Enterprise"

// bifrostMCPDestructiveExt marks a POST or PUT operation in the spec that
// destroys or invalidates state. Every DELETE is destructive without it.
const bifrostMCPDestructiveExt = "x-bifrost-destructive"

// bifrostMCPDenied lists the /api operations never served on /v1/mcp, by
// operationId. operationIds are a public contract, so a rename in the spec
// surfaces as an unknown entry here (logged at load, failed in tests).
var bifrostMCPDenied = map[string]bool{
	// Mint, trade or end dashboard credentials.
	"login": true, "logout": true, "startSetupSession": true, "issueWSTicket": true, "isAuthEnabled": true,
	// Browser steps of end-user OAuth and per-user-headers flows, authorized by one-time tokens.
	"handleOAuthCallback": true, "startPerUserOauthFlow": true, "getPerUserOauthFlow": true,
	"getPerUserHeadersFlow": true, "submitPerUserHeadersFlow": true,
	// Streams (SSE) and WebSocket upgrades, which a request/response tool cannot carry.
	"warpSession": true, "websocketConnect": true, "connectRealtime": true, "openaiRealtimeWebSocket": true,
	"createResponseWebSocket": true, "openaiResponsesWebSocket": true,
	"geminiStreamGenerateContent": true, "litellmGeminiStreamGenerateContent": true,
	"langchainGeminiStreamGenerateContent": true, "pydanticaiGeminiStreamGenerateContent": true,
	"agent_rest_message_stream_post": true, "agent_rest_tasks_taskId_subscribe_post": true,
	// The other MCP server, whose session a stateless tool call cannot hold.
	"mcpServerMessage": true, "mcpServerSSE": true,
	// Binary downloads and installer manifests: a model only sees "[binary response]".
	"downloadAllSkills": true, "downloadSkill": true, "downloadSkillFile": true,
	"getClaudeCodeSkillsMarketplace": true, "getCodexSkillsMarketplace": true,
}

// bifrostMCPDeniedRoutes denies the operations of bifrostMCPDenied by route as
// well. The gateway reads dev's spec, so a released build can meet an
// operationId renamed after it shipped, which the ID list no longer matches.
// Path parameters compare by position, not name (see bifrostMCPRouteKey).
var bifrostMCPDeniedRoutes = bifrostMCPRouteSet(
	"POST /api/session/login", "POST /api/session/logout", "POST /api/session/setup",
	"POST /api/session/ws-ticket", "GET /api/session/is-auth-enabled",
	"GET /api/oauth/callback", "GET /api/oauth/per-user/flows/{id}/start", "GET /api/oauth/per-user/flows/{id}",
	"GET /api/mcp/per-user-headers/flows/{id}", "PUT /api/mcp/per-user-headers/flows/{id}",
	"POST /api/warp/chat", "GET /ws", "GET /v1/realtime", "GET /openai/v1/realtime",
	"GET /v1/responses", "GET /openai/v1/responses",
	"POST /genai/v1beta/models/{model}:streamGenerateContent",
	"POST /litellm/genai/v1beta/models/{model}:streamGenerateContent",
	"POST /langchain/genai/v1beta/models/{model}:streamGenerateContent",
	"POST /pydanticai/genai/v1beta/models/{model}:streamGenerateContent",
	"POST /agents/a2a/{name}/rest/message:stream", "POST /agents/a2a/{name}/rest/tasks/{taskId}:subscribe",
	"POST /mcp", "GET /mcp",
	"GET /api/skills/serve/all/download.zip", "GET /api/skills/serve/{skill-name}/download.zip",
	"GET /api/skills/serve/{skill-name}/files/{filepath}",
	"GET /api/skills/serve/claude-code/.claude-plugin/marketplace.json",
	"GET /api/skills/serve/codex/.agents/plugins/marketplace.json",
)

// bifrostMCPPathParam matches one path parameter, {name}.
var bifrostMCPPathParam = regexp.MustCompile(`\{[^}]*\}`)

// bifrostMCPRouteKey is "METHOD path" with every path parameter written {}, so
// renaming a parameter does not change the key.
func bifrostMCPRouteKey(method, path string) string {
	return strings.ToUpper(method) + " " + bifrostMCPPathParam.ReplaceAllString(path, "{}")
}

// bifrostMCPRouteSet keys "METHOD path" routes by bifrostMCPRouteKey.
func bifrostMCPRouteSet(routes ...string) map[string]bool {
	set := make(map[string]bool, len(routes))
	for _, route := range routes {
		method, path, _ := strings.Cut(route, " ")
		set[bifrostMCPRouteKey(method, path)] = true
	}
	return set
}

// BifrostMCPOptions configures the Bifrost MCP.
type BifrostMCPOptions struct {
	SpecSources []string // URLs or file paths, tried in order; see BifrostMCPSpecSources
	AppDir      string   // relative file paths resolve against it
	Edition     BifrostMCPEdition
	AllowWrites bool                           // false leaves out non-GET /api operations
	Served      func(method, path string) bool // nil serves every operation; else only those it accepts
	SpecClient  *http.Client                   // fetches a URL spec; carries the proxy policy
	ExecClient  *http.Client                   // serves tool calls through the /api chain
	Headers     openapimcp.HeaderResolver
}

// BifrostMCP holds the Bifrost MCP server, which becomes available once
// the spec has loaded. Until then Server returns nil.
type BifrostMCP struct {
	opts   BifrostMCPOptions
	server atomic.Pointer[server.MCPServer]
}

// NewBifrostMCP returns an unloaded holder; call Start to load it.
func NewBifrostMCP(opts BifrostMCPOptions) *BifrostMCP {
	return &BifrostMCP{opts: opts}
}

// Server returns the loaded server, or nil while the spec is unavailable.
func (m *BifrostMCP) Server() *server.MCPServer {
	if m == nil {
		return nil
	}
	return m.server.Load()
}

// Start loads the spec in the background, retrying with backoff until it
// succeeds or ctx ends. It never blocks startup.
func (m *BifrostMCP) Start(ctx context.Context) {
	go func() {
		delay := bifrostMCPRetryInitial
		for {
			err := m.Load(ctx)
			if err == nil {
				return
			}
			logger.Warn("[Bifrost MCP] %s, retrying in %s: %v", bifrostMCPUnavailableMsg, delay, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			delay = min(delay*2, bifrostMCPRetryMax)
		}
	}()
}

// Load fetches, curates and synthesizes the spec once, and publishes the
// resulting server.
func (m *BifrostMCP) Load(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, bifrostMCPLoadTimeout)
	defer cancel()
	data, source, specURL, err := loadBifrostMCPSpec(ctx, m.opts)
	if err != nil {
		return err
	}
	srv, report, err := buildBifrostMCPServer(data, specURL, m.opts)
	if err != nil {
		return err
	}
	logger.Info("[Bifrost MCP] loaded %s spec from %s: %d tools exposed, %d operations excluded (%d not routed by this gateway), %d unsupported, writes %s",
		m.opts.edition(), source, report.exposed, report.excluded, report.unrouted, len(report.unsupported), enabledWord(m.opts.AllowWrites))
	for _, op := range report.unsupported {
		logger.Debug("[Bifrost MCP] operation %s (%s %s) cannot be served: %s", op.OperationID, op.Method, op.Path, op.Reason)
	}
	for _, id := range report.unknown {
		logger.Warn("[Bifrost MCP] denied operation %s is not in the spec", id)
	}
	m.server.Store(srv)
	return nil
}

// edition defaults an unset edition to OSS.
func (o BifrostMCPOptions) edition() BifrostMCPEdition {
	if o.Edition == "" {
		return BifrostMCPEditionOSS
	}
	return o.Edition
}

// BifrostMCPSpecSources lists the specs the gateway reads, in order: a
// non-empty BIFROST_MCP_SPEC alone, replacing the default; else dev's published
// spec. Every build reads dev, whatever its version: a version can look like a
// release with no spec behind it (enterprise's dev build stamps v1.2.3).
// BifrostMCPOptions.Served drops the operations this gateway does not route, so
// a newer spec cannot add a tool the build lacks; it does not pin the schemas or
// annotations of the operations it serves, which follow dev's spec. An OSS
// gateway reads openapi-oss.json, which bundle.py writes beside openapi.json, and
// falls back to the full spec filtered by the enterprise tag.
func BifrostMCPSpecSources(edition BifrostMCPEdition) []string {
	if override := strings.TrimSpace(os.Getenv(BifrostMCPSpecEnv)); override != "" {
		return []string{override}
	}
	base := bifrostMCPSpecRepoURL + "/refs/heads/dev/" + bifrostMCPSpecDir
	if edition == BifrostMCPEditionEnterprise {
		return []string{base + bifrostMCPSpecFull}
	}
	return []string{base + bifrostMCPSpecOSS, base + bifrostMCPSpecFull}
}

// BifrostMCPAllowWrites reads the writes switch.
func BifrostMCPAllowWrites() bool {
	if v, err := strconv.ParseBool(strings.TrimSpace(os.Getenv(BifrostMCPWritesEnv))); err == nil {
		return v
	}
	return bifrostMCPWritesDefault
}

// loadBifrostMCPSpec returns the first spec source that loads, which one it was,
// and the URL relative servers resolve against.
func loadBifrostMCPSpec(ctx context.Context, opts BifrostMCPOptions) ([]byte, string, string, error) {
	var errs []error
	for _, source := range opts.SpecSources {
		if source = strings.TrimSpace(source); source == "" {
			continue
		}
		data, specURL, err := loadBifrostMCPSpecSource(ctx, opts, source)
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

func loadBifrostMCPSpecSource(ctx context.Context, opts BifrostMCPOptions, source string) ([]byte, string, error) {
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

type bifrostMCPReport struct {
	exposed     int
	excluded    int
	unrouted    int // excluded because this gateway does not route them
	unsupported []openapimcp.UnsupportedOperation
	unknown     []string // bifrostMCPDenied entries the spec does not define
}

// buildBifrostMCPServer turns spec bytes into the Bifrost MCP server.
func buildBifrostMCPServer(data []byte, specURL string, opts BifrostMCPOptions) (*server.MCPServer, bifrostMCPReport, error) {
	var report bifrostMCPReport
	doc, err := openapimcp.Parse(data, openapimcp.ParseOptions{SpecURL: specURL})
	if err != nil {
		return nil, report, fmt.Errorf("parsing spec: %w", err)
	}
	var kept []openapimcp.Operation
	kept, report.unrouted, report.unknown = curateBifrostMCPOperations(doc.Operations, opts)
	report.excluded = len(doc.Operations) - len(kept)
	doc.Operations = kept
	// Bifrost's spec writes dynamic query names as <placeholder> (metadata_<key>),
	// a convention openapimcp only expands when asked to.
	syn, err := openapimcp.Synthesize(doc, openapimcp.SynthesizeOptions{TemplateParams: true})
	if err != nil {
		return nil, report, fmt.Errorf("synthesizing tools: %w", err)
	}
	report.unsupported = syn.Unsupported
	report.exposed = len(syn.Tools)
	for i := range syn.Tools {
		annotateBifrostMCPTool(&syn.Tools[i])
	}
	srv, err := openapimcp.BuildServer(openapimcp.ServerOptions{
		ClientName:      "gateway",
		Synthesis:       syn,
		BaseURL:         bifrostMCPBaseURL,
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

// bifrostMCPWrite reports whether op is one the writes switch gates: a non-GET
// operation on the gateway's own /api, which changes its configuration. A
// non-GET outside /api (an inference call, a batch, a file upload to a
// provider) is the caller's own request, authorized by its credential the same
// way a direct call is, so the switch leaves it alone.
func bifrostMCPWrite(op openapimcp.Operation) bool {
	return op.Method != http.MethodGet && strings.HasPrefix(op.Path, "/api/")
}

// curateBifrostMCPOperations keeps the operations that become tools: not
// deprecated, not denied by operationId or route, not enterprise-only on an OSS gateway, routed by this
// gateway and, when writes are off, not a non-GET /api operation. It also returns how many were
// left out because the gateway does not route them, and the denied operationIds
// the spec does not define.
func curateBifrostMCPOperations(ops []openapimcp.Operation, opts BifrostMCPOptions) ([]openapimcp.Operation, int, []string) {
	seen := make(map[string]bool, len(bifrostMCPDenied))
	oss := opts.edition() == BifrostMCPEditionOSS
	var kept []openapimcp.Operation
	unrouted := 0
	for _, op := range ops {
		if bifrostMCPDenied[op.ID] {
			seen[op.ID] = true
			continue
		}
		if bifrostMCPDeniedRoutes[bifrostMCPRouteKey(op.Method, op.Path)] {
			continue
		}
		if op.Deprecated || (!opts.AllowWrites && bifrostMCPWrite(op)) {
			continue
		}
		if oss && bifrostMCPEnterpriseOnly(op) {
			continue
		}
		if opts.Served != nil && !opts.Served(op.Method, op.Path) {
			unrouted++
			continue
		}
		kept = append(kept, op)
	}
	var unknown []string
	for id := range bifrostMCPDenied {
		if !seen[id] {
			unknown = append(unknown, id)
		}
	}
	return kept, unrouted, unknown
}

// bifrostMCPEnterpriseOnly reports whether the spec marks op enterprise-only.
func bifrostMCPEnterpriseOnly(op openapimcp.Operation) bool {
	mint, _ := op.Extensions["x-mint"].(map[string]any)
	metadata, _ := mint["metadata"].(map[string]any)
	tag, _ := metadata["tag"].(string)
	return tag == bifrostMCPEnterpriseTag
}

// annotateBifrostMCPTool replaces the method-derived hints openapimcp sets for
// an arbitrary upstream: /api tools act on this gateway only (closed world),
// the rest reach providers and agents beyond it, and a POST or PUT is
// destructive only when the spec marks it so.
func annotateBifrostMCPTool(tool *openapimcp.Tool) {
	b := func(v bool) *bool { return &v }
	ann := &tool.Annotations
	ann.OpenWorldHint = b(!strings.HasPrefix(tool.Operation.Path, "/api/"))
	switch tool.Operation.Method {
	case http.MethodGet:
		return
	case http.MethodDelete:
		ann.DestructiveHint = b(true)
	default:
		marked, _ := tool.Operation.Extensions[bifrostMCPDestructiveExt].(bool)
		ann.DestructiveHint = b(marked)
	}
}

func enabledWord(on bool) string {
	if on {
		return "enabled"
	}
	return "disabled"
}
