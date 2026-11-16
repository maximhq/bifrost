package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mark3labs/mcp-go/server"
	"github.com/maximhq/bifrost/framework/openapimcp"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
)

// The Bifrost MCP serves Bifrost's own HTTP surface as MCP tools on /v1/mcp.
// Every operation in the spec becomes a tool, except the deprecated ones, the
// routes on bifrostMCPDeniedRoutes, and any this gateway does not route. Which
// spec, the base URL tools address and whether /api writes are served come from
// settings through BifrostMCPOptions.

const (
	bifrostMCPSpecRepoURL = "https://raw.githubusercontent.com/maximhq/bifrost/refs/heads/dev/docs/openapi/"
	// BifrostMCPSpecURLOSS is the default spec of an OSS gateway: dev's
	// openapi-oss.json, which bundle.py writes without the enterprise-only operations.
	BifrostMCPSpecURLOSS = bifrostMCPSpecRepoURL + "openapi-oss.json"
	// BifrostMCPSpecURLEnterprise is the default spec of an enterprise gateway:
	// dev's openapi.json, every operation.
	BifrostMCPSpecURLEnterprise = bifrostMCPSpecRepoURL + "openapi.json"

	bifrostMCPUnavailableMsg = "the Bifrost MCP spec is unavailable"
)

// bifrostMCPDestructiveExt marks a POST or PUT operation in the spec that
// destroys or invalidates state. Every DELETE is destructive without it.
const bifrostMCPDestructiveExt = "x-bifrost-destructive"

// bifrostMCPDeniedRoutes lists the routes never served on /v1/mcp. They are
// matched by route rather than operationId, so a spec that renames an operation
// or a path parameter cannot bring one back; a spec that moves a route is
// covered by Served, which drops any route this gateway does not register.
// Path parameters compare by position, not name (see bifrostMCPRouteKey).
var bifrostMCPDeniedRoutes = bifrostMCPRouteSet(
	// Mint, trade or end dashboard credentials.
	"POST /api/session/login", "POST /api/session/logout", "POST /api/session/setup",
	"POST /api/session/ws-ticket", "GET /api/session/is-auth-enabled",
	// Browser steps of end-user OAuth and per-user-headers flows, authorized by one-time tokens.
	"GET /api/oauth/callback", "GET /api/oauth/per-user/flows/{id}/start", "GET /api/oauth/per-user/flows/{id}",
	"GET /api/mcp/per-user-headers/flows/{id}", "PUT /api/mcp/per-user-headers/flows/{id}",
	// Streams (SSE) and WebSocket upgrades, which a request/response tool cannot carry.
	"POST /api/warp/chat", "GET /ws", "GET /v1/realtime", "GET /openai/v1/realtime",
	"GET /v1/responses", "GET /openai/v1/responses",
	"POST /genai/v1beta/models/{model}:streamGenerateContent",
	"POST /litellm/genai/v1beta/models/{model}:streamGenerateContent",
	"POST /langchain/genai/v1beta/models/{model}:streamGenerateContent",
	"POST /pydanticai/genai/v1beta/models/{model}:streamGenerateContent",
	"POST /agents/a2a/{name}/rest/message:stream", "POST /agents/a2a/{name}/rest/tasks/{taskId}:subscribe",
	// The other MCP server, whose session a stateless tool call cannot hold.
	"POST /mcp", "GET /mcp",
	// Binary downloads and installer manifests: a model only sees "[binary response]".
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
	SpecURL     string                         // URL or file path of the spec; empty reads BifrostMCPSpecURLOSS
	AppDir      string                         // a relative file path resolves against it
	BaseURL     string                         // the gateway's public base URL, which tool requests address
	AllowWrites bool                           // false leaves out non-GET /api operations
	Served      func(method, path string) bool // nil serves every operation; else only those it accepts
	SpecClient  *http.Client                   // fetches a URL spec; carries the proxy policy
	ExecClient  *http.Client                   // serves tool calls through the /api chain
	Headers     openapimcp.HeaderResolver
}

// BifrostMCP holds the Bifrost MCP server. The spec is loaded on first use and
// again when Reload hands it new options.
type BifrostMCP struct {
	mu        sync.Mutex // serializes loads and guards opts
	opts      BifrostMCPOptions
	server    atomic.Pointer[server.MCPServer]
	attempted atomic.Bool           // a load has run; later ones run only in the background
	loading   atomic.Bool           // a load is in flight
	retryAt   atomic.Int64          // unix nanos before which a failed load is not retried
	lastErr   atomic.Pointer[error] // why the last load failed, answered until a load succeeds
}

// bifrostMCPRetryInterval is how long after a failed load the spec source is
// tried again.
const bifrostMCPRetryInterval = 30 * time.Second

// NewBifrostMCP returns an unloaded holder.
func NewBifrostMCP(opts BifrostMCPOptions) *BifrostMCP {
	return &BifrostMCP{opts: opts}
}

// Start loads the spec in the background, so no request waits for it. It does
// nothing once the spec has loaded, while a load is in flight, or within
// bifrostMCPRetryInterval of a failed one, so repeated calls never pile up
// loads. A failure is logged and kept for Server to answer.
func (m *BifrostMCP) Start(ctx context.Context) {
	if m == nil || m.server.Load() != nil || time.Now().UnixNano() < m.retryAt.Load() {
		return
	}
	if !m.loading.CompareAndSwap(false, true) {
		return
	}
	go func() {
		if _, err := m.loadHeld(ctx); err != nil {
			logger.Warn("[Bifrost MCP] %s: %v", bifrostMCPUnavailableMsg, err)
		}
	}()
}

// Server returns the loaded server. Only the first call, before any load has
// run, fetches the spec itself (at boot that is Start's background load). After
// that a caller never waits on the spec source: it gets the server, or at once
// the reason there is none (still loading, or the last failure), while Start
// retries in the background once bifrostMCPRetryInterval has passed, so a
// source that was briefly unreachable needs no restart.
func (m *BifrostMCP) Server(ctx context.Context) (*server.MCPServer, error) {
	if m == nil {
		return nil, errors.New(bifrostMCPUnavailableMsg)
	}
	if srv := m.server.Load(); srv != nil {
		return srv, nil
	}
	if m.attempted.Load() {
		m.Start(context.WithoutCancel(ctx))
		return nil, m.unavailable()
	}
	if !m.loading.CompareAndSwap(false, true) {
		return nil, m.unavailable()
	}
	return m.loadHeld(ctx)
}

// unavailable is why there is no server: the last load's failure, or a load
// still in flight.
func (m *BifrostMCP) unavailable() error {
	if err := m.lastErr.Load(); err != nil {
		return *err
	}
	return fmt.Errorf("%s: the spec is still loading", bifrostMCPUnavailableMsg)
}

// loadHeld loads the spec for a caller that holds the loading flag, and
// releases it. The retry time is checked again under the flag: a load that
// failed between a caller's first check and its taking the flag has set a new
// one, which stands.
func (m *BifrostMCP) loadHeld(ctx context.Context) (*server.MCPServer, error) {
	defer m.loading.Store(false)
	if time.Now().UnixNano() < m.retryAt.Load() {
		return nil, m.unavailable()
	}
	m.attempted.Store(true)
	m.mu.Lock()
	defer m.mu.Unlock()
	if srv := m.server.Load(); srv != nil {
		return srv, nil
	}
	srv, err := loadBifrostMCPServer(ctx, m.opts)
	if err != nil {
		m.lastErr.Store(&err)
		m.retryAt.Store(time.Now().Add(bifrostMCPRetryInterval).UnixNano())
		return nil, err
	}
	m.retryAt.Store(0)
	m.server.Store(srv)
	return srv, nil
}

// Reload rebuilds the server from opts, for a settings change. On failure the
// previous server and options stay in place and the error is returned.
func (m *BifrostMCP) Reload(ctx context.Context, opts BifrostMCPOptions) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	srv, err := loadBifrostMCPServer(ctx, opts)
	if err != nil {
		return err
	}
	m.opts = opts
	m.server.Store(srv)
	return nil
}

// loadBifrostMCPServer fetches, curates and synthesizes the spec once.
func loadBifrostMCPServer(ctx context.Context, opts BifrostMCPOptions) (*server.MCPServer, error) {
	source := strings.TrimSpace(opts.SpecURL)
	if source == "" {
		source = BifrostMCPSpecURLOSS
	}
	// The source is reported with its userinfo and query values masked: a settings
	// URL can carry a token, and both the error and the log line below reach logs.
	shown := shownSpecSource(source)
	data, specURL, err := loadBifrostMCPSpec(ctx, opts, source)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", shown, redactSourceIn(err, source, shown))
	}
	srv, report, err := buildBifrostMCPServer(data, specURL, opts)
	if err != nil {
		return nil, err
	}
	logger.Info("[Bifrost MCP] loaded spec from %s: %d tools exposed, %d operations excluded (%d not routed by this gateway), %d unsupported, writes %s",
		shown, report.exposed, report.excluded, report.unrouted, len(report.unsupported), enabledWord(opts.AllowWrites))
	for _, op := range report.unsupported {
		logger.Debug("[Bifrost MCP] operation %s (%s %s) cannot be served: %s", op.OperationID, op.Method, op.Path, op.Reason)
	}
	return srv, nil
}

// shownSpecSource is source as logs and errors may show it: a URL with its
// userinfo and query values masked, or, for a URL that does not parse (so its
// credentials cannot be told apart), a placeholder. A file path is shown as is.
func shownSpecSource(source string) string {
	lower := strings.ToLower(source)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return source
	}
	if u, err := url.Parse(source); err != nil || u.Host == "" {
		return "[spec URL that does not parse]"
	}
	return lib.RedactURLCredentials(source)
}

// redactedSourceError carries a loader error's message with the source swapped
// for its shown form, keeping the error itself for errors.Is.
type redactedSourceError struct {
	msg string
	err error
}

func (e redactedSourceError) Error() string { return e.msg }
func (e redactedSourceError) Unwrap() error { return e.err }

// redactSourceIn swaps every copy of source in err's message, raw or quoted as
// a URL parse error quotes it, for shown.
func redactSourceIn(err error, source, shown string) error {
	if source == shown {
		return err
	}
	msg := err.Error()
	quoted := strconv.Quote(source)
	msg = strings.ReplaceAll(msg, quoted[1:len(quoted)-1], shown)
	msg = strings.ReplaceAll(msg, source, shown)
	if msg == err.Error() {
		return err
	}
	return redactedSourceError{msg: msg, err: err}
}

// loadBifrostMCPSpec reads source, a URL or a file path, and returns the URL
// relative servers resolve against.
func loadBifrostMCPSpec(ctx context.Context, opts BifrostMCPOptions, source string) ([]byte, string, error) {
	if lower := strings.ToLower(source); strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return openapimcp.LoadURL(ctx, opts.SpecClient, source, 0)
	}
	baseDir := ""
	if !filepath.IsAbs(source) {
		baseDir = opts.AppDir
	}
	data, err := openapimcp.LoadFile(source, baseDir, 0)
	return data, "", err
}

type bifrostMCPReport struct {
	exposed     int
	excluded    int
	unrouted    int // excluded because this gateway does not route them
	unsupported []openapimcp.UnsupportedOperation
}

// buildBifrostMCPServer turns spec bytes into the Bifrost MCP server.
func buildBifrostMCPServer(data []byte, specURL string, opts BifrostMCPOptions) (*server.MCPServer, bifrostMCPReport, error) {
	var report bifrostMCPReport
	doc, err := openapimcp.Parse(data, openapimcp.ParseOptions{SpecURL: specURL})
	if err != nil {
		return nil, report, fmt.Errorf("parsing spec: %w", err)
	}
	var kept []openapimcp.Operation
	kept, report.unrouted = curateBifrostMCPOperations(doc.Operations, opts)
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
		BaseURL:         opts.BaseURL,
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
// deprecated, not on a denied route, routed by this gateway and, when writes are
// off, not a non-GET /api operation. It also returns how many were left out
// because the gateway does not route them; on an OSS gateway that is what drops
// the enterprise-only operations of a full spec.
func curateBifrostMCPOperations(ops []openapimcp.Operation, opts BifrostMCPOptions) ([]openapimcp.Operation, int) {
	var kept []openapimcp.Operation
	unrouted := 0
	for _, op := range ops {
		if bifrostMCPDeniedRoutes[bifrostMCPRouteKey(op.Method, op.Path)] {
			continue
		}
		if op.Deprecated || (!opts.AllowWrites && bifrostMCPWrite(op)) {
			continue
		}
		if opts.Served != nil && !opts.Served(op.Method, op.Path) {
			unrouted++
			continue
		}
		kept = append(kept, op)
	}
	return kept, unrouted
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
