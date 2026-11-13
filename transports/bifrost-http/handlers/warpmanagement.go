package handlers

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/framework/openapimcp"
	"github.com/maximhq/bifrost/framework/warp"
	"github.com/valyala/fasthttp"
)

// Warp reaches the Bifrost MCP in process, as the dashboard user who asked:
// the same server /v1/mcp serves, through mcp-go's in-process client, so each
// call runs the full /api chain with that user's credential. Warp offers the
// model every read except the log reads its own Q&A tools answer, and the
// creates, which it runs only after the person confirms them.

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

// warpManagementSecretCreates are the creates Warp leaves out because their
// response carries a plaintext secret shown once (createWebhookEndpoint's
// signing secret): the result would go to the model, and the person would have
// no other place to read it.
var warpManagementSecretCreates = bifrostMCPRouteSet("POST /api/webhooks")

// warpManagementInitTimeout bounds the in-process client's initialize handshake.
const warpManagementInitTimeout = 10 * time.Second

// warpManagementTools adapts a BifrostMCP to warp.ManagementTools. The client
// and catalog are built once per spec load and reused for every turn.
type warpManagementTools struct {
	mcp *BifrostMCP

	mu      sync.Mutex
	loaded  *bifrostMCPLoaded
	client  *client.Client
	catalog *warp.ManagementCatalog
}

// NewWarpManagementTools returns m as Warp's management tools.
func NewWarpManagementTools(m *BifrostMCP) warp.ManagementTools {
	return &warpManagementTools{mcp: m}
}

// Catalog returns the tools Warp offers from the current spec load, or nil
// while the spec is unavailable.
func (w *warpManagementTools) Catalog() *warp.ManagementCatalog {
	_, catalog, err := w.current()
	if err != nil {
		logger.Warn("[Warp] management tools unavailable: %v", err)
		return nil
	}
	return catalog
}

// warpManagementBinding is the load a catalog was built from, bound to it (see
// warp.ManagementCatalog.Bind) so a call runs on the client of that load.
type warpManagementBinding struct {
	client *client.Client
	loaded *bifrostMCPLoaded
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
	return text.String(), result.IsError, nil
}

// current returns the client and catalog for the loaded spec, building them
// when the spec has loaded or reloaded since the last call. With no spec loaded
// it starts a load in the background and offers no tools this turn, so a chat
// never waits on the spec source; a later turn picks them up.
func (w *warpManagementTools) current() (*client.Client, *warp.ManagementCatalog, error) {
	if w == nil || w.mcp == nil {
		return nil, nil, nil
	}
	loaded := w.mcp.loaded.Load()
	if loaded == nil {
		w.mcp.Start(context.Background())
		return nil, nil, nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.loaded == loaded {
		return w.client, w.catalog, nil
	}
	c, err := client.NewInProcessClient(loaded.server)
	if err != nil {
		return nil, nil, fmt.Errorf("creating the in-process client: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), warpManagementInitTimeout)
	defer cancel()
	if err := c.Start(ctx); err != nil {
		return nil, nil, fmt.Errorf("starting the in-process client: %w", err)
	}
	if _, err := c.Initialize(ctx, mcp.InitializeRequest{Params: mcp.InitializeParams{
		ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
		ClientInfo:      mcp.Implementation{Name: "bifrost-warp", Version: "1"},
	}}); err != nil {
		return nil, nil, fmt.Errorf("initializing the in-process client: %w", err)
	}
	w.loaded, w.client = loaded, c
	w.catalog = warpManagementCatalog(loaded).Bind(&warpManagementBinding{client: c, loaded: loaded})
	return w.client, w.catalog, nil
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
		write, offered := warpManagementOffered(op)
		if !offered {
			continue
		}
		tool := served[name].Tool
		inputSchema, err := toolInputSchema(tool)
		if err != nil {
			continue
		}
		tools = append(tools, warp.ManagementTool{Name: name, Description: tool.Description, InputSchema: inputSchema, Write: write})
	}
	return warp.NewManagementCatalog(tools)
}

// warpManagementOffered reports whether Warp offers an operation, and whether
// it is a write. Warp manages this deployment, so it is offered /api only, not
// the inference and SDK routes the Bifrost MCP also serves. Reads are every GET
// except the log reads and the reads that return virtual key values; writes are
// the creates (POST operations named create*) not marked destructive, except
// those that answer with a plaintext secret.
func warpManagementOffered(op openapimcp.Operation) (write, offered bool) {
	if !strings.HasPrefix(op.Path, "/api/") {
		return false, false
	}
	switch op.Method {
	case http.MethodGet:
		if warpManagementSecretReads[bifrostMCPRouteKey(op.Method, op.Path)] {
			return false, false
		}
		for _, prefix := range warpManagementLogPaths {
			if op.Path == prefix || strings.HasPrefix(op.Path, prefix+"/") {
				return false, false
			}
		}
		return false, true
	case http.MethodPost:
		if warpManagementSecretCreates[bifrostMCPRouteKey(op.Method, op.Path)] {
			return false, false
		}
		destructive, _ := op.Extensions[bifrostMCPDestructiveExt].(bool)
		return true, strings.HasPrefix(op.ID, "create") && !destructive
	}
	return false, false
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
