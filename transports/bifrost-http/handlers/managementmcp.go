package handlers

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/mark3labs/mcp-go/server"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/openapimcp"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttputil"
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
	// managementMCPCallTimeout bounds one tool call's trip through /api.
	managementMCPCallTimeout = 2 * time.Minute
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
	// Browser steps of end-user OAuth, per-user-headers and MCP OAuth consent flows, authorized by one-time tokens.
	"handleOAuthCallback": true, "startPerUserOauthFlow": true, "getPerUserOauthFlow": true,
	"getPerUserHeadersFlow": true, "submitPerUserHeadersFlow": true,
	"getOAuth2ConsentFlow": true, "submitOAuth2ConsentFlow": true,
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
		Headers:         managementMCPCallerHeaders,
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

// managementMCPPathParam matches a path parameter in either a spec path
// ({vk_id}) or a router template ({id}, {filepath:*}, {id?}, {id:[0-9]+}).
var managementMCPPathParam = regexp.MustCompile(`\{[^}]*\}`)

// ManagementMCPServedRoutes returns a Served filter for the routes a router
// registers (fasthttp/router's List: method to path templates). Templates are
// copied, so later registrations do not race with it; parameter names are
// ignored, so /api/x/{id} serves the spec's /api/x/{x_id}.
func ManagementMCPServedRoutes(routes map[string][]string) func(method, path string) bool {
	served := make(map[string]bool)
	for method, paths := range routes {
		for _, path := range paths {
			served[method+" "+managementMCPPathParam.ReplaceAllString(path, "{}")] = true
		}
	}
	return func(method, path string) bool {
		return served[method+" "+managementMCPPathParam.ReplaceAllString(path, "{}")]
	}
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

// managementMCPForwardedHeaders are the caller's headers a tool call carries into
// /api: its credential, and what a direct /api call would also have said about
// where it came from. /api decides what the credential may do. Cookies are never
// forwarded, so a web page cannot ride an admin's browser session through /v1/mcp.
var managementMCPForwardedHeaders = []string{
	"Authorization", SetupTokenHeader,
	"X-Forwarded-For", "X-Forwarded-Proto", "X-Forwarded-Host", "X-Real-IP", "User-Agent",
}

type managementMCPCallerKey struct{}

// managementMCPCaller is who a tool call acts for: the headers forwarded into
// /api, plus the Host and peer address the caller reached the gateway with, so
// handlers that build URLs from the request and logs that record the peer see
// the same values a direct call would.
type managementMCPCaller struct {
	headers http.Header
	host    string
	remote  net.Addr
}

// WithManagementMCPCaller records on ctx who the management MCP's tool calls act
// for. The /v1/mcp handler sets it from the inbound request; an in-process
// client (Warp) sets it from its own caller's request.
func WithManagementMCPCaller(ctx context.Context, req *fasthttp.Request, remote net.Addr) context.Context {
	caller := managementMCPCaller{headers: http.Header{}, host: string(req.Header.Host()), remote: remote}
	for _, name := range managementMCPForwardedHeaders {
		if v := req.Header.Peek(name); len(v) > 0 {
			caller.headers.Set(name, string(v))
		}
	}
	return context.WithValue(ctx, managementMCPCallerKey{}, caller)
}

// managementMCPCallerHeaders is the HeaderResolver of the management MCP: the
// forwarded headers of the caller on ctx, none when there is no caller.
func managementMCPCallerHeaders(ctx context.Context) (http.Header, error) {
	caller, _ := ctx.Value(managementMCPCallerKey{}).(managementMCPCaller)
	return caller.headers.Clone(), nil
}

// ManagementMCPExecutor serves tool calls in process: an in-memory listener in
// front of the server's root handler, so each call runs the same middleware
// chain (auth, RBAC, audit) as a request arriving from the network.
type ManagementMCPExecutor struct {
	Client   *http.Client
	listener *fasthttputil.InmemoryListener
	server   *fasthttp.Server
}

// NewManagementMCPExecutor starts serving handler on an in-memory listener with
// the main server's body and header-buffer limits, so a call /api would accept
// over the network is accepted here too. Close stops it.
func NewManagementMCPExecutor(handler fasthttp.RequestHandler, maxRequestBodySize, readBufferSize int) *ManagementMCPExecutor {
	ln := fasthttputil.NewInmemoryListener()
	srv := &fasthttp.Server{Handler: handler, MaxRequestBodySize: maxRequestBodySize, ReadBufferSize: readBufferSize}
	go func() {
		if err := srv.Serve(ln); err != nil {
			logger.Warn("[Management MCP] in-process listener stopped: %v", err)
		}
	}()
	transport := &http.Transport{
		// One connection per call: its local address is the caller's peer
		// address, which a pooled connection would carry over to the next call.
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			caller, _ := ctx.Value(managementMCPCallerKey{}).(managementMCPCaller)
			if caller.remote != nil {
				return ln.DialWithLocalAddr(caller.remote)
			}
			return ln.Dial()
		},
	}
	return &ManagementMCPExecutor{
		Client:   &http.Client{Transport: managementMCPTransport{next: transport}, Timeout: managementMCPCallTimeout},
		listener: ln,
		server:   srv,
	}
}

// Close stops the in-process listener.
func (e *ManagementMCPExecutor) Close() error {
	if e == nil {
		return nil
	}
	err := e.server.Shutdown()
	if cerr := e.listener.Close(); err == nil {
		err = cerr
	}
	return err
}

// managementMCPTransport gives each call the caller's Host and drops any Cookie
// header, whatever put it there: a call never carries a browser session.
type managementMCPTransport struct{ next http.RoundTripper }

func (t managementMCPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Del("Cookie")
	if caller, _ := req.Context().Value(managementMCPCallerKey{}).(managementMCPCaller); caller.host != "" {
		req.Host = caller.host
	}
	return t.next.RoundTrip(req)
}

// ManagementMCPHandler serves the management MCP on /v1/mcp.
type ManagementMCPHandler struct {
	mcp *ManagementMCP
}

// NewManagementMCPHandler returns the /v1/mcp handler for m.
func NewManagementMCPHandler(m *ManagementMCP) *ManagementMCPHandler {
	return &ManagementMCPHandler{mcp: m}
}

// RegisterRoutes registers /v1/mcp. It takes no auth middleware: initialize and
// tools/list describe the public spec, and every tools/call is authenticated by
// /api with the caller's forwarded credential.
func (h *ManagementMCPHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.POST("/v1/mcp", lib.ChainMiddlewares(h.handlePost, middlewares...))
	// Streamable HTTP lets a server refuse the SSE stream; POST carries every
	// call. Registered explicitly, or the dashboard's catch-all would answer.
	r.GET("/v1/mcp", lib.ChainMiddlewares(h.handleGet, middlewares...))
}

func (h *ManagementMCPHandler) handlePost(ctx *fasthttp.RequestCtx) {
	if managementMCPVirtualKey(&ctx.Request) {
		SendError(ctx, fasthttp.StatusUnauthorized, managementMCPVirtualKeyMsg)
		return
	}
	srv := h.mcp.Server()
	if srv == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, managementMCPUnavailableMsg)
		return
	}
	callCtx := WithManagementMCPCaller(ctx, &ctx.Request, ctx.RemoteAddr())
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

const managementMCPVirtualKeyMsg = "/v1/mcp takes a management credential (the admin login, the setup token, or an API key), not a virtual key"

// managementMCPVirtualKey reports whether the request authenticates with a
// virtual key. /api would refuse one with a bare 401 on the first tool call, so
// /v1/mcp says why before anything runs.
func managementMCPVirtualKey(req *fasthttp.Request) bool {
	if len(req.Header.Peek(string(schemas.BifrostContextKeyVirtualKey))) > 0 {
		return true
	}
	auth := string(req.Header.Peek("Authorization"))
	if len(auth) > 7 && strings.EqualFold(auth[:7], "bearer ") {
		return strings.HasPrefix(strings.ToLower(strings.TrimSpace(auth[7:])), governance.VirtualKeyPrefix)
	}
	return false
}

func (h *ManagementMCPHandler) handleGet(ctx *fasthttp.RequestCtx) {
	ctx.Response.Header.Set("Allow", fasthttp.MethodPost)
	SendError(ctx, fasthttp.StatusMethodNotAllowed, "/v1/mcp serves JSON-RPC over POST; it has no SSE stream")
}
