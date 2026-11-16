package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/openapimcp"
	"github.com/maximhq/bifrost/framework/warp"
	"github.com/valyala/fasthttp"
)

// Warp reaches the Bifrost MCP in process, as the dashboard user who asked:
// the same server /v1/mcp serves, through mcp-go's in-process client, so each
// call runs the full /api chain with that user's credential. Warp can be offered
// what the Bifrost MCP serves under /api (see warpManagementOffered): reads
// always, and the kinds of write its settings allow, each run only after the
// person confirms it.

// warpManagementLogPaths are the /api reads Warp leaves out: its own Q&A tools
// answer log questions, scoped to what the caller may see.
var warpManagementLogPaths = []string{"/api/logs", "/api/mcp-logs"}

// warpManagementSecretReads are the /api reads Warp leaves out because they
// return virtual keys with their values in plaintext (listVirtualKeys,
// getVirtualKey, listUserVirtualKeys, getUserVirtualKeysByEmail): a result goes
// to the model, and so to its provider. Matched by route, as
// bifrostMCPDeniedRoutes is, so a renamed operation stays out.
var warpManagementSecretReads = bifrostMCPRouteSet(
	"GET /api/governance/virtual-keys", "GET /api/governance/virtual-keys/{vk_id}",
	"GET /api/governance/users/{user_id}/virtual-keys", "GET /api/governance/users/email/{email}/virtual-keys",
)

// warpManagementSecretWrites are the writes Warp leaves out because their
// response carries a plaintext secret shown once (a webhook's signing secret, a
// rotated virtual key's new value): the result would go to the model, and the
// person would have no other place to read it.
var warpManagementSecretWrites = bifrostMCPRouteSet(
	"POST /api/webhooks", "POST /api/webhooks/{id}/rotate-secret",
	"POST /api/governance/virtual-keys/{vk_id}/rotate", "POST /api/governance/virtual-keys/rotate",
)

// warpManagementInitTimeout bounds the in-process client's initialize handshake.
const warpManagementInitTimeout = 10 * time.Second

// warpManagementTools adapts a BifrostMCP to warp.ManagementTools. The client
// and catalog are built once per spec load and reused for every turn.
//
// Each client holds a session on its load's server and a goroutine that only
// Close stops, so a replaced client has to be closed - but not while a turn that
// snapshotted its catalog can still call through it. Turns lease the catalog
// (LeaseCatalog); a reload retires the old binding, which closes once its last
// lease is released, and Close retires the current one at shutdown.
type warpManagementTools struct {
	mcp *BifrostMCP

	mu      sync.Mutex
	loaded  *bifrostMCPLoaded
	binding *warpManagementBinding
	catalog *warp.ManagementCatalog
	closed  bool // Close ran: no new client is built
}

// NewWarpManagementTools returns m as Warp's management tools.
func NewWarpManagementTools(m *BifrostMCP) warp.ManagementTools {
	return &warpManagementTools{mcp: m}
}

// Catalog returns the tools Warp offers from the current spec load, or nil
// while the spec is unavailable.
func (w *warpManagementTools) Catalog() *warp.ManagementCatalog {
	catalog, _ := w.lease(false)
	return catalog
}

// LeaseCatalog is Catalog for a turn: the catalog, and a release the turn calls
// when it ends. Until then the client the catalog is bound to stays open, even
// if the spec reloads in between.
func (w *warpManagementTools) LeaseCatalog() (*warp.ManagementCatalog, func()) {
	return w.lease(true)
}

func (w *warpManagementTools) lease(lease bool) (*warp.ManagementCatalog, func()) {
	if w == nil || w.mcp == nil {
		return nil, func() {}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	// Read under the lock: read before it, a load another turn installed while
	// this one waited would be newer than this one, and retired below.
	loaded := w.mcp.loaded.Load()
	if loaded == nil {
		// Unloaded (a reload restricted what it serves): retire the client of the
		// earlier load once its turns end, so nothing keeps that server alive.
		if w.binding != nil {
			w.binding.retired = true
			w.closeIfDoneLocked(w.binding)
			w.loaded, w.binding, w.catalog = nil, nil, nil
		}
		w.mcp.Start(context.Background())
		return nil, func() {}
	}
	if err := w.refreshLocked(loaded); err != nil {
		logger.Warn("[Warp] management tools unavailable: %v", err)
		return nil, func() {}
	}
	if w.binding == nil {
		return nil, func() {}
	}
	if !lease {
		return w.catalog, func() {}
	}
	binding := w.binding
	binding.leases++
	var once sync.Once
	return w.catalog, func() {
		once.Do(func() {
			w.mu.Lock()
			defer w.mu.Unlock()
			binding.leases--
			w.closeIfDoneLocked(binding)
		})
	}
}

// Close retires the current client, closing it now or when the last turn using
// it ends, and stops new ones being built. Called at shutdown, or when Warp is
// bound to a new instance.
func (w *warpManagementTools) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	if w.binding != nil {
		w.binding.retired = true
		w.closeIfDoneLocked(w.binding)
	}
	w.loaded, w.binding, w.catalog = nil, nil, nil
	return nil
}

// warpManagementBinding is the load a catalog was built from, bound to it (see
// warp.ManagementCatalog.Bind) so a call runs on the client of that load. Its
// lease count and state are guarded by warpManagementTools.mu.
type warpManagementBinding struct {
	client  *client.Client
	loaded  *bifrostMCPLoaded
	leases  int  // turns holding this binding's catalog
	retired bool // replaced by a reload, or shut down: close once no turn holds it
	closed  bool
}

// closeIfDoneLocked closes a retired binding's client once no turn holds it.
// Close is mcp-go's own: it unregisters the session and stops the transport's
// goroutine. Callers hold w.mu.
func (w *warpManagementTools) closeIfDoneLocked(binding *warpManagementBinding) {
	if binding == nil || binding.closed || !binding.retired || binding.leases > 0 {
		return
	}
	binding.closed = true
	if err := binding.client.Close(); err != nil {
		logger.Warn("[Warp] closing a retired management client: %v", err)
	}
}

// Call runs one tool of catalog through the in-process client of the load that
// catalog was built from, so a spec reloaded between the turn's snapshot (or a
// card's confirmation) and the call cannot change what the tool does. A name
// the catalog does not offer is refused. ctx must carry the caller (see
// WithBifrostMCPCaller); without one the call reaches /api with no credential
// and /api refuses it.
func (w *warpManagementTools) Call(ctx context.Context, catalog *warp.ManagementCatalog, name string, arguments map[string]any) (string, bool, error) {
	binding, _ := catalog.Binding().(*warpManagementBinding)
	if binding == nil || binding.client == nil {
		return "", false, fmt.Errorf("the management API is not available yet")
	}
	w.mu.Lock()
	closed := binding.closed
	w.mu.Unlock()
	if closed {
		// Only a turn holding a lease calls, so this means the catalog was used past its turn.
		return "", false, fmt.Errorf("the management API was reloaded; ask again")
	}
	if _, offered := catalog.Tool(name); !offered {
		return "", false, fmt.Errorf("%s is not a management tool this conversation offers", name)
	}
	c := binding.client
	// With a public base URL configured, Warp's calls arrive through it like
	// /v1/mcp's do, keeping the dashboard cookie but not the dashboard's Host.
	if binding.loaded.edgeScheme != "" {
		ctx = withBifrostMCPEdge(ctx, binding.loaded.edgeScheme)
	}
	result, err := c.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: name, Arguments: arguments}})
	if err != nil {
		return "", false, err
	}
	var text strings.Builder
	for _, content := range result.Content {
		if part, ok := content.(mcp.TextContent); ok {
			if text.Len() > 0 {
				text.WriteByte('\n')
			}
			text.WriteString(part.Text)
		}
	}
	return redactVirtualKeyValues(text.String()), result.IsError, nil
}

// redactVirtualKeyValues removes the value of every virtual key a management
// result embeds, so it never reaches the model and, through it, its provider.
// The reads that return keys are not offered at all; this covers the ones that
// embed them, such as a customer listed with its keys. A result is the /api body,
// or for a refusal a status line followed by it, and only a body that names a
// virtual key is parsed.
func redactVirtualKeyValues(result string) string {
	if !strings.Contains(result, "virtual_key") {
		return result
	}
	head, body := "", result
	if !json.Valid([]byte(body)) {
		i := strings.IndexByte(result, '\n')
		if i >= 0 && strings.HasPrefix(result, "HTTP ") {
			head, body = result[:i+1], result[i+1:]
		}
		// A body that names a virtual key but does not parse, such as one cut at
		// the response limit, cannot be redacted, so it is withheld.
		if !json.Valid([]byte(body)) {
			return head + warpManagementWithheldResult
		}
	}
	// UseNumber keeps every number as written: decoded as a float64, an id or
	// counter past 2^53 would come back with different digits.
	var decoded any
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return result
	}
	if !redactVirtualKeys(decoded, false) {
		return result
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return result
	}
	return head + string(encoded)
}

// warpManagementWithheldResult replaces a result that names virtual keys but
// could not be parsed to remove their values.
const warpManagementWithheldResult = "[result withheld: it includes virtual keys and could not be parsed to remove their values; narrow the request, for example with a smaller page]"

// redactVirtualKeys walks v and drops the value field of each virtual key: an
// object held by a virtual_key or virtual_keys field (inKey). It reports whether
// it removed anything.
func redactVirtualKeys(v any, inKey bool) bool {
	removed := false
	switch node := v.(type) {
	case map[string]any:
		if inKey {
			if _, ok := node["value"]; ok {
				delete(node, "value")
				removed = true
			}
		}
		for field, child := range node {
			if redactVirtualKeys(child, field == "virtual_key" || field == "virtual_keys") {
				removed = true
			}
		}
	case []any:
		for _, child := range node {
			if redactVirtualKeys(child, inKey) {
				removed = true
			}
		}
	}
	return removed
}

// refreshLocked builds the client and catalog for loaded when the spec has
// loaded or reloaded since the last call, and retires the binding it replaces.
// Callers hold w.mu.
func (w *warpManagementTools) refreshLocked(loaded *bifrostMCPLoaded) error {
	if w.closed || w.loaded == loaded {
		return nil
	}
	c, err := client.NewInProcessClient(loaded.server)
	if err != nil {
		return fmt.Errorf("creating the in-process client: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), warpManagementInitTimeout)
	defer cancel()
	if err := c.Start(ctx); err != nil {
		_ = c.Close()
		return fmt.Errorf("starting the in-process client: %w", err)
	}
	if _, err := c.Initialize(ctx, mcp.InitializeRequest{Params: mcp.InitializeParams{
		ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
		ClientInfo:      mcp.Implementation{Name: "bifrost-warp", Version: "1"},
	}}); err != nil {
		// Started: a session is registered and its goroutine runs until Close.
		_ = c.Close()
		return fmt.Errorf("initializing the in-process client: %w", err)
	}
	if previous := w.binding; previous != nil {
		previous.retired = true
		w.closeIfDoneLocked(previous)
	}
	w.loaded = loaded
	w.binding = &warpManagementBinding{client: c, loaded: loaded}
	w.catalog = warpManagementCatalog(loaded).Bind(w.binding)
	return nil
}

// warpManagementCatalog picks the tools Warp offers from one spec load, in name
// order so the declarations the model sees are stable across loads.
func warpManagementCatalog(loaded *bifrostMCPLoaded) *warp.ManagementCatalog {
	served := loaded.server.ListTools()
	names := make([]string, 0, len(served))
	for name := range served {
		names = append(names, name)
	}
	slices.Sort(names)
	tools := make([]warp.ManagementTool, 0, len(names))
	for _, name := range names {
		op, ok := loaded.operations[name]
		if !ok {
			continue
		}
		kind, destructive, offered := warpManagementOffered(op)
		if !offered {
			continue
		}
		tool := served[name].Tool
		inputSchema, err := toolInputSchema(tool)
		if err != nil {
			continue
		}
		tools = append(tools, warp.ManagementTool{Name: name, Description: tool.Description, InputSchema: inputSchema, Kind: kind, Destructive: destructive})
	}
	return warp.NewManagementCatalog(tools)
}

// warpManagementOffered reports whether Warp may be offered an operation, and
// for a write its kind (a schemas.WarpManagementWrites value) and whether it is
// destructive. Warp manages this deployment, so only /api, not the inference and
// SDK routes the Bifrost MCP also serves. Within /api it can be offered what the
// Bifrost MCP serves - which leaves writes out when bifrost_mcp_allow_writes is
// off - except what it would hand the model: the log reads its own tools
// answer, the reads that return virtual key values, and the writes that answer
// with a secret shown once. Which kinds of write a turn sees is Warp's own
// setting (WarpConfig.ManagementWrites); reads are always offered.
//
// Writes and destructiveness follow the Bifrost MCP's rules (bifrostMCPWrite,
// annotateBifrostMCPTool): every non-GET is a write, and every DELETE, or a
// write the spec marks, is destructive.
func warpManagementOffered(op openapimcp.Operation) (kind string, destructive, offered bool) {
	if !strings.HasPrefix(op.Path, "/api/") {
		return "", false, false
	}
	route := bifrostMCPRouteKey(op.Method, op.Path)
	if op.Method == http.MethodGet {
		if warpManagementSecretReads[route] {
			return "", false, false
		}
		for _, prefix := range warpManagementLogPaths {
			if op.Path == prefix || strings.HasPrefix(op.Path, prefix+"/") {
				return "", false, false
			}
		}
		return "", false, true
	}
	if warpManagementSecretWrites[route] {
		return "", false, false
	}
	marked, _ := op.Extensions[bifrostMCPDestructiveExt].(bool)
	destructive = op.Method == http.MethodDelete || marked
	switch {
	case op.Method == http.MethodPut || op.Method == http.MethodPatch:
		kind = schemas.WarpManagementUpdate
	case op.Method == http.MethodDelete:
		kind = schemas.WarpManagementDelete
	case op.Method == http.MethodPost && strings.HasPrefix(op.ID, "create") && !destructive:
		kind = schemas.WarpManagementCreate
	default:
		kind = schemas.WarpManagementAction
	}
	return kind, destructive, true
}

// toolInputSchema returns a tool's argument schema as JSON. The management
// server builds its tools from raw schemas, which are used as they are.
func toolInputSchema(tool mcp.Tool) (string, error) {
	if len(tool.RawInputSchema) > 0 {
		return string(tool.RawInputSchema), nil
	}
	encoded, err := sonic.Marshal(tool.InputSchema)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// warpManagementCaller records on ctx the dashboard user Warp's management
// calls act for. The dashboard authenticates with a session cookie (Bifrost's
// own, or an SSO session on Enterprise), which the auth middleware has already
// verified for this request; the call carries that cookie with the request's
// other credentials, so /api resolves the same user it did. Only Warp's caller
// carries a cookie: /v1/mcp never forwards one, and the call stays in process.
func warpManagementCaller(ctx context.Context, request *fasthttp.RequestCtx) context.Context {
	ctx = WithBifrostMCPCaller(ctx, &request.Request, request.RemoteAddr())
	if cookie := request.Request.Header.Peek("Cookie"); len(cookie) > 0 {
		caller, _ := ctx.Value(bifrostMCPCallerKey{}).(bifrostMCPCaller)
		caller.cookie = string(cookie)
		ctx = context.WithValue(ctx, bifrostMCPCallerKey{}, caller)
	}
	return ctx
}
