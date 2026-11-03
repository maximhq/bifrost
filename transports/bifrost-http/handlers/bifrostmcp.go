package handlers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/openapimcp"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttputil"
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
	// bifrostMCPCallTimeout bounds one tool call's trip through /api.
	bifrostMCPCallTimeout = 2 * time.Minute
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

// bifrostMCPPathParam matches a path parameter in either a spec path
// ({vk_id}) or a router template ({id}, {filepath:*}, {id?}, {id:[0-9]+}).
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
	ToolAccess  BifrostMCPToolAccess           // which /api tools a tools/list caller sees; nil lists them all
}

// BifrostMCPToolAccess resolves the caller of one tools/list and returns whether
// they may call the /api operation at a method and path, or nil to list every
// tool. caller is the /v1/mcp request after the /api auth chain has run on it,
// so it carries the caller's resolved identity; it is called once per list, so
// any lookup it needs happens once, not per tool. It only shapes the list; every
// call is still authorized by its route.
type BifrostMCPToolAccess func(caller *fasthttp.RequestCtx) func(method, path string) bool

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
		Headers:         bifrostMCPCallerHeaders,
		Logger:          logger,
		ToolFilter:      bifrostMCPToolListFilter(syn.Tools, opts.ToolAccess),
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

// bifrostMCPCatchAll matches a router template's trailing catch-all ({path:*}).
var bifrostMCPCatchAll = regexp.MustCompile(`\{[^{}]*:\*\}$`)

// BifrostMCPServedRoutes returns a Served filter for the routes a router
// registers (fasthttp/router's List: method to path templates). Templates are
// copied, so later registrations do not race with it; parameter names are
// ignored, so /api/x/{id} serves the spec's /api/x/{x_id}. A catch-all serves
// what sits under its prefix, which is how the SDK integrations route paths like
// /genai/v1beta/models/{model}:generateContent; the dashboard's root catch-all
// serves nothing, or every path would count as routed.
func BifrostMCPServedRoutes(routes map[string][]string) func(method, path string) bool {
	served := make(map[string]bool)
	prefixes := make(map[string][]string)
	for method, paths := range routes {
		for _, path := range paths {
			shape := bifrostMCPPathParam.ReplaceAllString(path, "{}")
			served[method+" "+shape] = true
			if bifrostMCPCatchAll.MatchString(path) {
				if prefix := strings.TrimSuffix(shape, "{}"); prefix != "/" {
					prefixes[method] = append(prefixes[method], prefix)
				}
			}
		}
	}
	return func(method, path string) bool {
		shape := bifrostMCPPathParam.ReplaceAllString(path, "{}")
		if served[method+" "+shape] {
			return true
		}
		return slices.ContainsFunc(prefixes[method], func(prefix string) bool { return strings.HasPrefix(shape, prefix) })
	}
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

// bifrostMCPForwardedHeaders are the caller's headers a tool call carries to its
// route: its credential, in each place the gateway reads one, and what a direct
// call would also have said about where it came from. The route decides what the
// credential may do. Cookies are never forwarded, so a web page cannot ride an
// admin's browser session through /v1/mcp.
var bifrostMCPForwardedHeaders = []string{
	"Authorization", SetupTokenHeader, string(schemas.BifrostContextKeyVirtualKey), "x-api-key", "x-goog-api-key", "api-key",
	"X-Forwarded-For", "X-Forwarded-Proto", "X-Forwarded-Host", "X-Real-IP", "User-Agent",
}

type bifrostMCPCallerKey struct{}

// bifrostMCPCaller is who a tool call acts for: the headers forwarded into
// /api, plus the Host and peer address the caller reached the gateway with, so
// handlers that build URLs from the request and logs that record the peer see
// the same values a direct call would.
type bifrostMCPCaller struct {
	headers http.Header
	host    string
	remote  net.Addr
}

// WithBifrostMCPCaller records on ctx who the Bifrost MCP's tool calls act
// for. The /v1/mcp handler sets it from the inbound request; an in-process
// client (Warp) sets it from its own caller's request.
func WithBifrostMCPCaller(ctx context.Context, req *fasthttp.Request, remote net.Addr) context.Context {
	caller := bifrostMCPCaller{headers: http.Header{}, host: string(req.Header.Host()), remote: remote}
	for _, name := range bifrostMCPForwardedHeaders {
		if v := req.Header.Peek(name); len(v) > 0 {
			caller.headers.Set(name, string(v))
		}
	}
	return context.WithValue(ctx, bifrostMCPCallerKey{}, caller)
}

// bifrostMCPCallerHeaders is the HeaderResolver of the Bifrost MCP: the
// forwarded headers of the caller on ctx, none when there is no caller.
func bifrostMCPCallerHeaders(ctx context.Context) (http.Header, error) {
	caller, _ := ctx.Value(bifrostMCPCallerKey{}).(bifrostMCPCaller)
	return caller.headers.Clone(), nil
}

// BifrostMCPExecutor serves tool calls in process: an in-memory listener in
// front of the server's root handler, so each call runs the same middleware
// chain (auth, RBAC, audit) as a request arriving from the network.
type BifrostMCPExecutor struct {
	Client   *http.Client
	listener *fasthttputil.InmemoryListener
	server   *fasthttp.Server
}

// NewBifrostMCPExecutor starts serving handler on an in-memory listener with
// the main server's body and header-buffer limits, so a call /api would accept
// over the network is accepted here too. Close stops it.
func NewBifrostMCPExecutor(handler fasthttp.RequestHandler, maxRequestBodySize, readBufferSize int) *BifrostMCPExecutor {
	ln := fasthttputil.NewInmemoryListener()
	srv := &fasthttp.Server{Handler: handler, MaxRequestBodySize: maxRequestBodySize, ReadBufferSize: readBufferSize}
	go func() {
		if err := srv.Serve(ln); err != nil {
			logger.Warn("[Bifrost MCP] in-process listener stopped: %v", err)
		}
	}()
	transport := &http.Transport{
		// One connection per call: its local address is the caller's peer
		// address, which a pooled connection would carry over to the next call.
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			caller, _ := ctx.Value(bifrostMCPCallerKey{}).(bifrostMCPCaller)
			if caller.remote != nil {
				return ln.DialWithLocalAddr(caller.remote)
			}
			return ln.Dial()
		},
	}
	return &BifrostMCPExecutor{
		Client:   &http.Client{Transport: bifrostMCPTransport{next: transport}, Timeout: bifrostMCPCallTimeout},
		listener: ln,
		server:   srv,
	}
}

// Close stops the in-process listener, waiting for in-flight calls however long
// they take. Shutdown bounds the wait.
func (e *BifrostMCPExecutor) Close() error {
	return e.Shutdown(context.Background())
}

// Shutdown stops the in-process listener and waits for in-flight calls until ctx
// is done. Stopping the server cancels each call's RequestCtx.Done(), so a
// handler that watches it returns early; one that does not is abandoned when
// ctx expires. The listener is closed directly too, for a Serve that has not
// started yet; its error (already closed, after a clean shutdown) is not the
// result.
func (e *BifrostMCPExecutor) Shutdown(ctx context.Context) error {
	if e == nil {
		return nil
	}
	err := e.server.ShutdownWithContext(ctx)
	_ = e.listener.Close()
	return err
}

// bifrostMCPTransport gives each call the caller's Host and drops any Cookie
// header, whatever put it there: a call never carries a browser session.
type bifrostMCPTransport struct{ next http.RoundTripper }

func (t bifrostMCPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Del("Cookie")
	if caller, _ := req.Context().Value(bifrostMCPCallerKey{}).(bifrostMCPCaller); caller.host != "" {
		req.Host = caller.host
	}
	return t.next.RoundTrip(req)
}

// BifrostMCPHandler serves the Bifrost MCP on /v1/mcp.
type BifrostMCPHandler struct {
	mcp *BifrostMCP
}

// NewBifrostMCPHandler returns the /v1/mcp handler for m.
func NewBifrostMCPHandler(m *BifrostMCP) *BifrostMCPHandler {
	return &BifrostMCPHandler{mcp: m}
}

// RegisterRoutes registers /v1/mcp behind middlewares, which a gateway passes as
// its /api auth chain: every MCP method, tools/list included, takes a management
// credential, and the virtual-key refusal runs first. Each tools/call is then
// authorized again by the route it reaches, with the caller's forwarded
// credential.
func (h *BifrostMCPHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	chain := append([]schemas.BifrostHTTPMiddleware{bifrostMCPVirtualKeyGuard}, middlewares...)
	r.POST("/v1/mcp", lib.ChainMiddlewares(h.handlePost, chain...))
	// Streamable HTTP lets a server refuse the SSE stream; POST carries every
	// call. Registered explicitly, or the dashboard's catch-all would answer.
	r.GET("/v1/mcp", lib.ChainMiddlewares(h.handleGet, chain...))
}

// bifrostMCPToolListFilter returns the tools/list filter for access, or nil when
// there is none. On a tools/list it keeps every tool outside /api and the /api
// tools access allows for the caller; on anything else (mcp-go also filters at
// call time) it changes nothing, since each call is authorized by its route.
func bifrostMCPToolListFilter(tools []openapimcp.Tool, access BifrostMCPToolAccess) func(context.Context, []mcp.Tool) []mcp.Tool {
	if access == nil {
		return nil
	}
	apiOps := make(map[string]openapimcp.Operation, len(tools))
	for i := range tools {
		if strings.HasPrefix(tools[i].Operation.Path, "/api/") {
			apiOps[tools[i].Name] = tools[i].Operation
		}
	}
	return func(ctx context.Context, listed []mcp.Tool) []mcp.Tool {
		caller, listing := ctx.Value(bifrostMCPListingKey{}).(*fasthttp.RequestCtx)
		if !listing || caller == nil {
			return listed
		}
		allowed := access(caller)
		if allowed == nil {
			return listed
		}
		kept := make([]mcp.Tool, 0, len(listed))
		for _, tool := range listed {
			if op, isAPI := apiOps[tool.Name]; !isAPI || allowed(op.Method, op.Path) {
				kept = append(kept, tool)
			}
		}
		return kept
	}
}

// bifrostMCPListingKey marks a tools/list on the context the MCP server sees,
// carrying the authenticated /v1/mcp request for ToolAccess.
type bifrostMCPListingKey struct{}

const bifrostMCPVirtualKeyMsg = "/v1/mcp takes a management credential (the admin login, the setup token, or an API key), not a virtual key alone; to use a virtual key with inference tools, send it in x-bf-vk alongside one"

// bifrostMCPVirtualKeyGuard runs ahead of the auth chain and refuses a request
// whose only credential is a virtual key: the chain would refuse it too, with a
// bare 401, so this says why. A virtual key sent alongside a management
// credential passes, since inference tools use it.
func bifrostMCPVirtualKeyGuard(next fasthttp.RequestHandler) fasthttp.RequestHandler {
	return func(ctx *fasthttp.RequestCtx) {
		if bifrostMCPOnlyVirtualKey(&ctx.Request) {
			SendError(ctx, fasthttp.StatusUnauthorized, bifrostMCPVirtualKeyMsg)
			return
		}
		next(ctx)
	}
}

// bifrostMCPOnlyVirtualKey reports whether a request presents a virtual key and
// nothing that could be a management credential. Any other Authorization, the
// setup token or a cookie leaves the decision to the auth chain.
func bifrostMCPOnlyVirtualKey(req *fasthttp.Request) bool {
	if len(req.Header.Peek(SetupTokenHeader)) > 0 || len(req.Header.Peek("Cookie")) > 0 {
		return false
	}
	virtualKey := len(bytes.TrimSpace(req.Header.Peek(string(schemas.BifrostContextKeyVirtualKey)))) > 0
	for _, name := range []string{"x-api-key", "x-goog-api-key", "api-key"} {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(string(req.Header.Peek(name)))), governance.VirtualKeyPrefix) {
			virtualKey = true
		}
	}
	if auth := strings.TrimSpace(string(req.Header.Peek("Authorization"))); auth != "" {
		if len(auth) <= 7 || !strings.EqualFold(auth[:7], "bearer ") || !strings.HasPrefix(strings.ToLower(strings.TrimSpace(auth[7:])), governance.VirtualKeyPrefix) {
			return false
		}
		virtualKey = true
	}
	return virtualKey
}

func (h *BifrostMCPHandler) handlePost(ctx *fasthttp.RequestCtx) {
	srv := h.mcp.Server()
	if srv == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, bifrostMCPUnavailableMsg)
		return
	}
	callCtx := WithBifrostMCPCaller(ctx, &ctx.Request, ctx.RemoteAddr())
	if h.mcp.opts.ToolAccess != nil {
		if method, err := sonic.Get(ctx.PostBody(), "method"); err == nil {
			if name, _ := method.String(); name == "tools/list" {
				callCtx = context.WithValue(callCtx, bifrostMCPListingKey{}, ctx)
			}
		}
	}
	response := srv.HandleMessage(callCtx, ctx.PostBody())
	if response == nil {
		ctx.SetStatusCode(fasthttp.StatusAccepted)
		return
	}
	body, err := sonic.Marshal(response)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to encode response: %v", err))
		return
	}
	ctx.SetContentType("application/json")
	ctx.SetBody(body)
}

func (h *BifrostMCPHandler) handleGet(ctx *fasthttp.RequestCtx) {
	ctx.Response.Header.Set("Allow", fasthttp.MethodPost)
	SendError(ctx, fasthttp.StatusMethodNotAllowed, "/v1/mcp serves JSON-RPC over POST; it has no SSE stream")
}
