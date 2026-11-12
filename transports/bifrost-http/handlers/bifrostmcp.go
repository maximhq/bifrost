package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
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
// Every operation in the spec becomes a tool, except the deprecated ones, the
// routes on bifrostMCPDeniedRoutes, and any this gateway does not route. Which
// spec, the base URL tools address and whether /api writes are served come from
// settings through BifrostMCPOptions.

const (
	// DefaultOpenAPIURL is the spec /v1/mcp reads while the framework config's
	// openapi_url is unset: dev's openapi-oss.json, which bundle.py writes without
	// the enterprise-only operations. A build that serves another spec passes its
	// own default to StartBifrostMCP.
	DefaultOpenAPIURL = "https://raw.githubusercontent.com/maximhq/bifrost/refs/heads/dev/docs/openapi/openapi-oss.json"

	bifrostMCPUnavailableMsg = "the Bifrost MCP spec is unavailable"
)

// ErrBifrostMCPNoBifrostURL is a load refused because bifrost_url is not
// set. Tool calls take their host and scheme from it and never from the caller's
// Host header, which the caller controls, so there is no fallback: like the MCP
// OAuth issuer URL, the setting is required.
var ErrBifrostMCPNoBifrostURL = errors.New("the Bifrost MCP needs bifrost_url set: its tool calls address Bifrost through that URL")

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
	// Browser steps of end-user OAuth, per-user-headers and MCP OAuth consent flows, authorized by one-time tokens.
	"GET /api/oauth/callback", "GET /api/oauth/per-user/flows/{id}/start", "GET /api/oauth/per-user/flows/{id}",
	"GET /api/mcp/per-user-headers/flows/{id}", "PUT /api/mcp/per-user-headers/flows/{id}",
	"GET /api/oauth2/consent/flows/{id}", "PUT /api/oauth2/consent/flows/{id}",
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
	SpecURL     string                         // URL or file path of the spec; empty reads DefaultOpenAPIURL
	AppDir      string                         // a relative file path resolves against it
	BaseURL     string                         // the base tool requests address: the public URL, or a stand-in served in process
	BaseIsEdge  bool                           // BaseURL is the configured public URL; calls carry its host and scheme, not the caller's
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

// BifrostMCP holds the Bifrost MCP server. The spec is loaded on first use and
// again when Reload hands it new options.
type BifrostMCP struct {
	mu        sync.Mutex // serializes loads and guards opts
	opts      BifrostMCPOptions
	loaded    atomic.Pointer[bifrostMCPLoaded]
	attempted atomic.Bool           // a load has run; later ones run only in the background
	loading   atomic.Bool           // a load is in flight
	retryAt   atomic.Int64          // unix nanos before which a failed load is not retried
	lastErr   atomic.Pointer[error] // why the last load failed, answered until a load succeeds
}

// bifrostMCPLoaded is one spec load: the server, the operation behind each of its
// tools by tool name, and what the handler needs from the options it was built
// with, so a call never reads options a concurrent Reload replaces.
type bifrostMCPLoaded struct {
	server     *server.MCPServer
	operations map[string]openapimcp.Operation
	edgeScheme string // the public URL's scheme when calls take its host; empty keeps the caller's
	toolAccess bool   // tools/list is shaped per caller
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
	if m == nil || m.loaded.Load() != nil || time.Now().UnixNano() < m.retryAt.Load() {
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

// Server returns the loaded server (see load).
func (m *BifrostMCP) Server(ctx context.Context) (*server.MCPServer, error) {
	loaded, err := m.load(ctx)
	if err != nil {
		return nil, err
	}
	return loaded.server, nil
}

// load returns the current load. Only the first call, before any load has run,
// fetches the spec itself (at boot that is Start's background load). After that
// a caller, a /v1/mcp request included, never waits on the spec source: it gets
// the load, or at once the reason there is none (still loading, or the last
// failure), while Start retries in the background once bifrostMCPRetryInterval
// has passed, so a source that was briefly unreachable needs no restart.
func (m *BifrostMCP) load(ctx context.Context) (*bifrostMCPLoaded, error) {
	if m == nil {
		return nil, errors.New(bifrostMCPUnavailableMsg)
	}
	if loaded := m.loaded.Load(); loaded != nil {
		return loaded, nil
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
func (m *BifrostMCP) loadHeld(ctx context.Context) (*bifrostMCPLoaded, error) {
	defer m.loading.Store(false)
	if time.Now().UnixNano() < m.retryAt.Load() {
		return nil, m.unavailable()
	}
	m.attempted.Store(true)
	m.mu.Lock()
	defer m.mu.Unlock()
	if loaded := m.loaded.Load(); loaded != nil {
		return loaded, nil
	}
	loaded, err := loadBifrostMCPServer(ctx, m.opts)
	if err != nil {
		m.lastErr.Store(&err)
		m.retryAt.Store(time.Now().Add(bifrostMCPRetryInterval).UnixNano())
		return nil, err
	}
	m.retryAt.Store(0)
	m.loaded.Store(loaded)
	return loaded, nil
}

// Reload rebuilds the server from opts, for a settings change. On failure the
// previous server and options stay in place and the error is returned, unless
// opts restricts what the previous server serves (see bifrostMCPRestricts): then
// keeping it would serve what the saved settings no longer allow, so it is
// unloaded instead, /v1/mcp answers unavailable, and the load is retried in the
// background with the new options.
func (m *BifrostMCP) Reload(ctx context.Context, opts BifrostMCPOptions) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	loaded, err := loadBifrostMCPServer(ctx, opts)
	if err != nil {
		if bifrostMCPRestricts(m.opts, opts) {
			m.opts = opts
			m.loaded.Store(nil)
			m.attempted.Store(true)
			m.lastErr.Store(&err)
			m.retryAt.Store(time.Now().Add(bifrostMCPRetryInterval).UnixNano())
		}
		return err
	}
	m.opts = opts
	m.loaded.Store(loaded)
	return nil
}

// bifrostMCPRestricts reports whether next serves less than prev: /api writes
// turned off, or bifrost_url cleared.
func bifrostMCPRestricts(prev, next BifrostMCPOptions) bool {
	return (prev.AllowWrites && !next.AllowWrites) ||
		(strings.TrimSpace(prev.BaseURL) != "" && strings.TrimSpace(next.BaseURL) == "")
}

// Options returns the options the current server was built from.
func (m *BifrostMCP) Options() BifrostMCPOptions {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.opts
}

// loadBifrostMCPServer fetches, curates and synthesizes the spec once.
func loadBifrostMCPServer(ctx context.Context, opts BifrostMCPOptions) (*bifrostMCPLoaded, error) {
	source := strings.TrimSpace(opts.SpecURL)
	if source == "" {
		source = DefaultOpenAPIURL
	}
	// The source is reported with its userinfo and query values masked: a settings
	// URL can carry a token, and both the error and the log line below reach logs.
	shown := shownSpecSource(source)
	if strings.TrimSpace(opts.BaseURL) == "" {
		return nil, ErrBifrostMCPNoBifrostURL
	}
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
	loaded := &bifrostMCPLoaded{server: srv, operations: report.operations, toolAccess: opts.ToolAccess != nil}
	if opts.BaseIsEdge {
		if base, err := url.Parse(opts.BaseURL); err == nil {
			loaded.edgeScheme = base.Scheme
		}
	}
	return loaded, nil
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
	operations  map[string]openapimcp.Operation // the operation behind each exposed tool
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
	for i := range kept {
		dropStreamingArguments(&kept[i])
	}
	doc.Operations = kept
	// Bifrost's spec writes dynamic query names as <placeholder> (metadata_<key>),
	// a convention openapimcp only expands when asked to.
	syn, err := openapimcp.Synthesize(doc, openapimcp.SynthesizeOptions{TemplateParams: true})
	if err != nil {
		return nil, report, fmt.Errorf("synthesizing tools: %w", err)
	}
	report.unsupported = syn.Unsupported
	report.exposed = len(syn.Tools)
	report.operations = make(map[string]openapimcp.Operation, len(syn.Tools))
	for i := range syn.Tools {
		annotateBifrostMCPTool(&syn.Tools[i])
		report.operations[syn.Tools[i].Name] = syn.Tools[i].Operation
	}
	srv, err := openapimcp.BuildServer(openapimcp.ServerOptions{
		ClientName:      "gateway",
		Synthesis:       syn,
		BaseURL:         opts.BaseURL,
		BaseURLExplicit: true,
		HTTPClient:      opts.ExecClient,
		Headers:         bifrostMCPCallerHeaders,
		Logger:          logger,
		ToolFilter:      bifrostMCPToolListFilter(syn.Tools, opts.ToolAccess),
		DropBodyFields:  bifrostMCPStreamingFields,
	})
	if err != nil {
		return nil, report, fmt.Errorf("building server: %w", err)
	}
	return srv, report, nil
}

// bifrostMCPStreamingFields are the request body fields that ask a route to stream.
var bifrostMCPStreamingFields = []string{"stream", "stream_options"}

// dropStreamingArguments takes the streaming fields out of op's request body, so
// no tool call asks for a stream. A tool result is one message whatever the route
// does, and a streamed answer would reach the model as its raw event stream (every
// chunk, with its metadata) rather than as the completion. A flattened body sends
// only the arguments its schema declares, so without them the route answers once;
// a body taken whole as one argument declares none, so the executor drops the
// fields from it as well (DropBodyFields). The schema is copied, never edited in place: it may be shared with other
// operations of the parsed document.
func dropStreamingArguments(op *openapimcp.Operation) {
	if op.Body == nil || op.Body.Schema == nil {
		return
	}
	props, ok := op.Body.Schema["properties"].(map[string]any)
	if !ok {
		return
	}
	found := false
	for _, field := range bifrostMCPStreamingFields {
		if _, ok := props[field]; ok {
			found = true
		}
	}
	if !found {
		return
	}
	schema := maps.Clone(op.Body.Schema)
	trimmed := maps.Clone(props)
	for _, field := range bifrostMCPStreamingFields {
		delete(trimmed, field)
	}
	schema["properties"] = trimmed
	if required, ok := schema["required"].([]any); ok {
		schema["required"] = slices.DeleteFunc(slices.Clone(required), func(v any) bool {
			name, _ := v.(string)
			return slices.Contains(bifrostMCPStreamingFields, name)
		})
	}
	body := *op.Body
	body.Schema = schema
	op.Body = &body
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

type bifrostMCPCallerKey struct{}

// bifrostMCPCaller is who a tool call acts for: every header the caller sent but
// Host, and the peer address it reached the gateway with, so the route and the
// logs that record the peer see what a direct call would. Host is never taken from
// the caller, who controls it: a call carries the configured base URL's host, so
// URLs a route builds from the request come from configuration. The route decides what the caller's credential may do,
// so forwarding a header grants nothing the caller did not already have. Cookies
// are forwarded here but stripped by bifrostMCPTransport, so a web page cannot ride
// an admin's browser session through /v1/mcp.
type bifrostMCPCaller struct {
	headers http.Header
	proto   string // set: the scheme the request arrived with, as x-bf-forwarded-proto
	remote  net.Addr
	cookie  string // the dashboard's Cookie header; set only for Warp (see warpManagementCaller)
}

// WithBifrostMCPCaller records on ctx who the Bifrost MCP's tool calls act
// for. The /v1/mcp handler sets it from the inbound request; an in-process
// client (Warp) sets it from its own caller's request.
func WithBifrostMCPCaller(ctx context.Context, req *fasthttp.Request, remote net.Addr) context.Context {
	caller := bifrostMCPCaller{headers: http.Header{}, remote: remote}
	for name, value := range req.Header.All() {
		if key := string(name); !strings.EqualFold(key, fasthttp.HeaderHost) {
			caller.headers.Add(key, string(value))
		}
	}
	return context.WithValue(ctx, bifrostMCPCallerKey{}, caller)
}

// withBifrostMCPEdge makes the caller on ctx arrive through the configured public
// URL's scheme, as x-bf-forwarded-proto, which RequestIsHTTPS reads first (the
// request already carries that URL's host). Self URLs a route builds then come
// from configuration, not from what the client sent.
func withBifrostMCPEdge(ctx context.Context, scheme string) context.Context {
	caller, ok := ctx.Value(bifrostMCPCallerKey{}).(bifrostMCPCaller)
	if !ok {
		return ctx
	}
	caller.proto = scheme
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
		Client:   &http.Client{Transport: bifrostMCPTransport{next: transport}},
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

// bifrostMCPTransport gives each call the public URL's scheme (its host is the
// base URL's, from the request URL) and drops any Cookie header, whatever put it
// there, so a /v1/mcp call never carries a browser session. The one exception is
// Warp's caller, whose dashboard cookie replaces it.
type bifrostMCPTransport struct{ next http.RoundTripper }

func (t bifrostMCPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	// The listener is in memory and plaintext; an https public base URL reaches the
	// route as x-bf-forwarded-proto instead (see withBifrostMCPEdge).
	req.URL.Scheme = "http"
	req.Header.Del("Cookie")
	caller, _ := req.Context().Value(bifrostMCPCallerKey{}).(bifrostMCPCaller)
	if caller.proto != "" {
		req.Header.Set("x-bf-forwarded-proto", caller.proto)
	}
	if caller.cookie != "" {
		req.Header.Set("Cookie", caller.cookie)
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
	loaded, err := h.mcp.load(ctx)
	if err != nil {
		logger.Warn("[Bifrost MCP] %s: %v", bifrostMCPUnavailableMsg, err)
		msg := bifrostMCPUnavailableMsg
		if errors.Is(err, ErrBifrostMCPNoBifrostURL) {
			msg = ErrBifrostMCPNoBifrostURL.Error()
		}
		SendError(ctx, fasthttp.StatusServiceUnavailable, msg)
		return
	}
	// Decoded in full rather than pre-filtered on the raw bytes: the dispatcher
	// unescapes the value, so a byte search would miss an escaped spelling.
	method := bifrostMCPMethod(ctx.PostBody())
	// /v1/mcp has no session, so mcp-go keys each in-flight request by its JSON-RPC
	// id alone, and a cancellation would end any caller's request with that id.
	// Cancellations are dropped instead of dispatched; the MCP spec lets a
	// receiver ignore them.
	if method == string(mcp.MethodNotificationCancelled) {
		ctx.SetStatusCode(fasthttp.StatusAccepted)
		return
	}
	srv := loaded.server
	callCtx := WithBifrostMCPCaller(ctx, &ctx.Request, ctx.RemoteAddr())
	if loaded.edgeScheme != "" {
		callCtx = withBifrostMCPEdge(callCtx, loaded.edgeScheme)
	}
	if loaded.toolAccess {
		if method == string(mcp.MethodToolsList) {
			callCtx = context.WithValue(callCtx, bifrostMCPListingKey{}, ctx)
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

// bifrostMCPMethod is the JSON-RPC method mcp-go dispatches body as. It is
// decoded the way HandleMessage decodes it, with encoding/json into a struct, so
// keys match case-insensitively and the last duplicate wins: a "Method" key is a
// tools/list to the dispatcher, so it must be one to the tools/list filter too.
func bifrostMCPMethod(body []byte) string {
	var msg struct {
		Method string `json:"method"`
	}
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&msg); err != nil {
		return ""
	}
	return msg.Method
}

func (h *BifrostMCPHandler) handleGet(ctx *fasthttp.RequestCtx) {
	ctx.Response.Header.Set("Allow", fasthttp.MethodPost)
	SendError(ctx, fasthttp.StatusMethodNotAllowed, "/v1/mcp serves JSON-RPC over POST; it has no SSE stream")
}
