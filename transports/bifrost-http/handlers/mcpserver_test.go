package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fasthttp/router"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	bifrost "github.com/maximhq/bifrost/core"
	coremcp "github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestConvertToolFunctionParametersToMCPInputSchemaPreservesDefs(t *testing.T) {
	params := &schemas.ToolFunctionParameters{
		Type: "object",
		Properties: schemas.NewOrderedMapFromPairs(
			schemas.KV("preferences", map[string]any{"$ref": "#/$defs/Preferences"}),
		),
		Required: []string{"preferences"},
		Defs: schemas.NewOrderedMapFromPairs(
			schemas.KV("Preferences", map[string]any{
				"type": "object",
				"properties": map[string]any{
					"startHour": map[string]any{"type": "string"},
				},
			}),
		),
	}

	inputSchema := convertToolFunctionParametersToMCPInputSchema(params)

	require.Contains(t, inputSchema.Defs, "Preferences")
	data, err := json.Marshal(inputSchema)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"$defs"`)
	assert.Contains(t, string(data), `"$ref":"#/$defs/Preferences"`)
}

func TestConvertToolFunctionParametersToMCPInputSchemaPreservesLegacyDefinitionsAsDefs(t *testing.T) {
	params := &schemas.ToolFunctionParameters{
		Type: "object",
		Properties: schemas.NewOrderedMapFromPairs(
			schemas.KV("preferences", map[string]any{"$ref": "#/$defs/Preferences"}),
		),
		Definitions: schemas.NewOrderedMapFromPairs(
			schemas.KV("Preferences", map[string]any{"type": "object"}),
		),
	}

	inputSchema := convertToolFunctionParametersToMCPInputSchema(params)

	require.Contains(t, inputSchema.Defs, "Preferences")
	data, err := json.Marshal(inputSchema)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"$defs"`)
}

// Forwarding upstream MCP `instructions` on the gateway's own initialize response.

// fakeInstructionsToolManager reports a fixed aggregate and records the context it was asked
// with, so a test can assert the hook resolves the text per request rather than once at build.
type fakeInstructionsToolManager struct {
	instructions string
	seenCtx      context.Context
}

func (f *fakeInstructionsToolManager) GetAvailableMCPTools(context.Context) []schemas.ChatTool {
	return nil
}

func (f *fakeInstructionsToolManager) GetMCPServerInstructions(ctx context.Context) string {
	f.seenCtx = ctx
	return f.instructions
}

func (f *fakeInstructionsToolManager) ExecuteChatMCPTool(context.Context, *schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, *schemas.BifrostError) {
	return nil, nil
}

func (f *fakeInstructionsToolManager) ExecuteResponsesMCPTool(context.Context, *schemas.ResponsesToolMessage) (*schemas.ResponsesMessage, *schemas.BifrostError) {
	return nil, nil
}

// fakeResultToolManager serves one tool and answers every call with the configured message.
type fakeResultToolManager struct {
	fakeInstructionsToolManager
	result *schemas.ChatMessage
}

// ExecuteChatMCPTool returns the configured tool result.
func (f *fakeResultToolManager) ExecuteChatMCPTool(context.Context, *schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, *schemas.BifrostError) {
	return f.result, nil
}

// callToolResult drives a real tools/call for alpha-echo through a server built with that one
// tool and returns the decoded result.
func callToolResult(t *testing.T, tm MCPToolManager) map[string]any {
	t.Helper()
	h := &MCPServerHandler{
		toolManager: tm,
		config:      &lib.Config{ClientConfig: &configstore.ClientConfig{}},
	}
	h.mcpServer = h.buildServer()
	tools, _ := h.serverTools([]schemas.ChatTool{{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: "alpha-echo"}}})
	h.mcpServer.SetTools(tools...)

	raw := h.server().HandleMessage(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"alpha-echo","arguments":{}}}`))
	require.NotNil(t, raw)
	encoded, err := json.Marshal(raw)
	require.NoError(t, err)
	var envelope struct {
		Result map[string]any `json:"result"`
	}
	require.NoError(t, json.Unmarshal(encoded, &envelope))
	require.NotNil(t, envelope.Result, "tools/call answered with a result: %s", encoded)
	return envelope.Result
}

// TestToolsCallCarriesTheToolErrorFlag pins that a tool result marked failed reaches the /mcp
// caller as isError true, with its text. The handler returned every result as a plain text
// result, so an upstream's isError was dropped and the call looked successful.
func TestToolsCallCarriesTheToolErrorFlag(t *testing.T) {
	text := "upstream refused"
	failed := callToolResult(t, &fakeResultToolManager{result: &schemas.ChatMessage{
		Role:            schemas.ChatMessageRoleTool,
		Content:         &schemas.ChatMessageContent{ContentStr: &text},
		ChatToolMessage: &schemas.ChatToolMessage{IsError: schemas.Ptr(true)},
	}})
	assert.Equal(t, true, failed["isError"])
	content, _ := failed["content"].([]any)
	require.Len(t, content, 1)
	assert.Equal(t, text, content[0].(map[string]any)["text"])

	ok := "18C"
	succeeded := callToolResult(t, &fakeResultToolManager{result: &schemas.ChatMessage{
		Role:            schemas.ChatMessageRoleTool,
		Content:         &schemas.ChatMessageContent{ContentStr: &ok},
		ChatToolMessage: &schemas.ChatToolMessage{},
	}})
	assert.NotEqual(t, true, succeeded["isError"], "a successful result is not marked failed")
}

// syncCtxToolManager records the context SyncMCPServer reads the tool list with.
type syncCtxToolManager struct {
	fakeInstructionsToolManager
	toolsCtx context.Context
}

func (f *syncCtxToolManager) GetAvailableMCPTools(ctx context.Context) []schemas.ChatTool {
	f.toolsCtx = ctx
	return nil
}

// syncCtxServed records the ctx each resources and prompts list is read with.
type syncCtxServed struct {
	MCPResourcesAndPromptsServer
	ctxs []context.Context
}

func (f *syncCtxServed) GetAvailableMCPPrompts(ctx context.Context) []mcp.Prompt {
	f.ctxs = append(f.ctxs, ctx)
	return nil
}

func (f *syncCtxServed) GetAvailableMCPResources(ctx context.Context) []mcp.Resource {
	f.ctxs = append(f.ctxs, ctx)
	return nil
}

func (f *syncCtxServed) GetAvailableMCPResourceTemplates(ctx context.Context) []mcp.ResourceTemplate {
	f.ctxs = append(f.ctxs, ctx)
	return nil
}

// Some syncs run on an admin's request. The shared server is built from no caller's identity,
// or a client with per_user_discovery on would publish that admin's own discovery (its tools,
// resources and prompts) to everyone.
func TestSyncMCPServer_ReadsToolsWithoutTheTriggersIdentity(t *testing.T) {
	tm := &syncCtxToolManager{}
	served := &syncCtxServed{}
	h := &MCPServerHandler{
		toolManager: tm,
		served:      served,
		config:      &lib.Config{ClientConfig: &configstore.ClientConfig{}},
	}
	h.mcpServer = h.buildServer()
	admin := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	admin.SetValue(schemas.BifrostContextKeyUserID, "admin-user")

	require.NoError(t, h.SyncMCPServer(admin))
	require.NotNil(t, tm.toolsCtx)
	assert.Nil(t, tm.toolsCtx.Value(schemas.BifrostContextKeyUserID), "the shared tool list must not be read as the admin who triggered the sync")
	// Prompts, resources and templates, plus resources and templates again in their aggregate /mcp form (MCP Apps).
	require.Len(t, served.ctxs, 5)
	for _, ctx := range served.ctxs {
		assert.Nil(t, ctx.Value(schemas.BifrostContextKeyUserID), "the shared resources and prompts must not be read as the admin who triggered the sync")
	}
}

// initializeResult drives a real initialize through the built server and returns the decoded
// result, which is what a connecting MCP client would actually receive.
func initializeResult(t *testing.T, tm MCPToolManager, ctx context.Context) map[string]any {
	t.Helper()
	h := &MCPServerHandler{
		toolManager: tm,
		config:      &lib.Config{ClientConfig: &configstore.ClientConfig{}},
	}
	h.mcpServer = h.buildServer()

	raw := h.server().HandleMessage(ctx, []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"probe","version":"0.0.1"}}}`))
	require.NotNil(t, raw)

	encoded, err := json.Marshal(raw)
	require.NoError(t, err)
	var envelope struct {
		Result map[string]any `json:"result"`
	}
	require.NoError(t, json.Unmarshal(encoded, &envelope))
	return envelope.Result
}

func TestInitializeForwardsInstructionsOnTheHandshake(t *testing.T) {
	tm := &fakeInstructionsToolManager{instructions: "<mcp_server name=\"github\">\nAlways call get_me first.\n</mcp_server>"}

	result := initializeResult(t, tm, context.Background())

	assert.Equal(t, "<mcp_server name=\"github\">\nAlways call get_me first.\n</mcp_server>", result["instructions"])
}

// Nothing to say means no field, rather than an empty one a client would have to special-case.
func TestInitializeOmitsInstructionsWhenNoUpstreamAdvertisesAny(t *testing.T) {
	tm := &fakeInstructionsToolManager{instructions: ""}

	result := initializeResult(t, tm, context.Background())

	_, present := result["instructions"]
	assert.False(t, present)
}

// The hook must resolve against the per-request context — that is the whole reason it is a
// hook rather than server.WithInstructions, which is fixed at construction. If the request's
// narrowing did not reach the resolver, a caller scoped to one upstream would be handed every
// upstream's text.
func TestInitializeResolvesInstructionsFromTheRequestContext(t *testing.T) {
	tm := &fakeInstructionsToolManager{instructions: "<mcp_server name=\"alpha\">\nscoped\n</mcp_server>"}
	ctx := context.WithValue(context.Background(), schemas.MCPContextKeyIncludeClients, []string{"alpha"})

	initializeResult(t, tm, ctx)

	require.NotNil(t, tm.seenCtx)
	assert.Equal(t, []string{"alpha"}, tm.seenCtx.Value(schemas.MCPContextKeyIncludeClients))
}

// A /mcp/<slug> request is narrowed by admitBySlug, which stamps the served tool list on
// MCPContextKeyIncludeTools rather than include-clients. The resolver has to see that narrowing
// too, or a Virtual MCP endpoint would answer initialize with every upstream's instructions.
func TestInitializeResolvesInstructionsForSlugNarrowedRequests(t *testing.T) {
	tm := &fakeInstructionsToolManager{instructions: "<mcp_server name=\"alpha\">\nscoped\n</mcp_server>"}
	ctx := context.WithValue(context.Background(), schemas.MCPContextKeyIncludeTools, []string{"alpha-echo"})

	result := initializeResult(t, tm, ctx)

	assert.Equal(t, "<mcp_server name=\"alpha\">\nscoped\n</mcp_server>", result["instructions"])
	require.NotNil(t, tm.seenCtx)
	assert.Equal(t, []string{"alpha-echo"}, tm.seenCtx.Value(schemas.MCPContextKeyIncludeTools),
		"the slug's served tool list must reach the instructions resolver")
}

// slugAdmitter admits everything and resolves slugs from fixed maps.
type slugAdmitter struct {
	vmcps   map[string][]string
	clients map[string][]string
}

func (slugAdmitter) AdmitMCPGatewayRequest(*schemas.BifrostContext) (schemas.Access, *schemas.BifrostError) {
	return nil, nil
}

func (a slugAdmitter) VirtualMCPToolAccess(_ *schemas.BifrostContext, slug string, _ schemas.Access) ([]string, bool) {
	served, ok := a.vmcps[slug]
	return served, ok
}

func (a slugAdmitter) MCPClientToolAccess(_ *schemas.BifrostContext, slug string, _ schemas.Access) ([]string, bool) {
	served, ok := a.clients[slug]
	return served, ok
}

// admitBySlug answers 403 for a real refusal, stamps the served tools when the slug names a granted
// Virtual MCP or client, and without an admitter serves a configured client whole, as /mcp does.
func TestAdmitBySlug(t *testing.T) {
	resolver := slugAdmitter{
		vmcps:   map[string][]string{"research-tools": {"alpha-search"}},
		clients: map[string][]string{"context7": {"context7-*"}},
	}
	cases := []struct {
		name       string
		admitter   MCPGatewayAdmitter
		slug       string
		wantStatus int
		wantTools  []string
		wantClient any // the single client the request serves; nil for a Virtual MCP
	}{
		{name: "nil admitter serves a configured client unrestricted", slug: "context7", wantTools: []string{"context7-*"}, wantClient: "context7"},
		{name: "nil admitter refuses an unknown slug", slug: "definitely-does-not-exist", wantStatus: 403},
		{name: "granted virtual mcp", admitter: resolver, slug: "research-tools", wantTools: []string{"alpha-search"}},
		{name: "granted mcp client", admitter: resolver, slug: "context7", wantTools: []string{"context7-*"}, wantClient: "context7"},
		{name: "unknown slug", admitter: resolver, slug: "definitely-does-not-exist", wantStatus: 403},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &lib.Config{MCPConfig: &schemas.MCPConfig{ClientConfigs: []*schemas.MCPClientConfig{
				{ID: "c7", Name: "context7", EndpointSlug: "context7"},
			}}}
			h := &MCPServerHandler{admitter: tc.admitter, config: cfg}
			bctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			refusal := h.admitBySlug(bctx, tc.slug, nil)
			if tc.wantStatus != 0 {
				require.NotNil(t, refusal)
				assert.Equal(t, tc.wantStatus, refusal.status)
				if tc.wantStatus == 403 {
					assert.Equal(t, "access_denied", refusal.message)
				}
				return
			}
			require.Nil(t, refusal)
			assert.Equal(t, tc.wantTools, bctx.Value(schemas.MCPContextKeyIncludeTools))
			assert.Equal(t, tc.wantClient, bctx.Value(schemas.BifrostContextKeyMCPGatewayClient))
		})
	}
}

// Serving every MCP protocol revision through the gateway's own routes.

// echoToolManager serves one tool, "gw-echo", that answers with its "text" argument.
type echoToolManager struct {
	fakeInstructionsToolManager
}

func (e *echoToolManager) GetAvailableMCPTools(ctx context.Context) []schemas.ChatTool {
	if include, ok := ctx.Value(schemas.MCPContextKeyIncludeTools).([]string); ok && !slices.Contains(include, "gw-echo") {
		return nil
	}
	return []schemas.ChatTool{{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: "gw-echo"}}}
}

func (e *echoToolManager) ExecuteChatMCPTool(_ context.Context, call *schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, *schemas.BifrostError) {
	var args struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
	return &schemas.ChatMessage{Content: &schemas.ChatMessageContent{ContentStr: &args.Text}}, nil
}

// startGateway serves h's routes on a loopback listener and returns its base URL.
func startGateway(t testing.TB, h *MCPServerHandler) string {
	t.Helper()
	// A short heartbeat lets an abandoned stream end quickly, so Shutdown never waits out 15s.
	previous := sseHeartbeatInterval
	sseHeartbeatInterval = 20 * time.Millisecond
	t.Cleanup(func() { sseHeartbeatInterval = previous })
	r := router.New()
	h.RegisterRoutes(r)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := &fasthttp.Server{Handler: r.Handler}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Shutdown() })
	return "http://" + ln.Addr().String()
}

func newGatewayHandler(t testing.TB) *MCPServerHandler {
	t.Helper()
	tm := &echoToolManager{fakeInstructionsToolManager{instructions: "gateway instructions"}}
	h, err := NewMCPServerHandler(context.Background(), &lib.Config{ClientConfig: &configstore.ClientConfig{}}, tm, nil, nil, nil)
	require.NoError(t, err)
	return h
}

func TestMCPGateway_ProtocolVersionMatrix(t *testing.T) {
	baseURL := startGateway(t, newGatewayHandler(t))
	for _, version := range []string{mcp.ProtocolVersion20250618, mcp.ProtocolVersion20251125, mcp.ProtocolVersion20260728} {
		t.Run(version, func(t *testing.T) {
			ctx := context.Background()
			tr, err := transport.NewStreamableHTTP(baseURL + "/mcp")
			require.NoError(t, err)
			c := client.NewClient(tr, client.WithProtocolVersion(version))
			require.NoError(t, c.Start(ctx))
			t.Cleanup(func() { _ = c.Close() })

			init := mcp.InitializeRequest{}
			init.Params.ProtocolVersion = version
			init.Params.ClientInfo = mcp.Implementation{Name: "matrix", Version: "0.0.1"}
			result, err := c.Initialize(ctx, init)
			require.NoError(t, err)
			assert.Equal(t, version, c.ProtocolVersion())
			assert.Equal(t, "gateway instructions", result.Instructions)

			tools, err := c.ListTools(ctx, mcp.ListToolsRequest{})
			require.NoError(t, err)
			require.Len(t, tools.Tools, 1)
			assert.Equal(t, "gw-echo", tools.Tools[0].Name)

			call := mcp.CallToolRequest{}
			call.Params.Name = "gw-echo"
			call.Params.Arguments = map[string]any{"text": "hi"}
			res, err := c.CallTool(ctx, call)
			require.NoError(t, err)
			require.Len(t, res.Content, 1)
			assert.Equal(t, "hi", res.Content[0].(mcp.TextContent).Text)
		})
	}
}

// Transport rules a client sees on the wire, checked with raw HTTP.
func TestMCPGateway_TransportRules(t *testing.T) {
	baseURL := startGateway(t, newGatewayHandler(t))
	modernList := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{},"io.modelcontextprotocol/clientInfo":{"name":"raw","version":"0"}}}}`
	cases := []struct {
		name       string
		method     string
		headers    map[string]string
		body       string
		wantStatus int
		wantInBody string
	}{
		{
			name:       "legacy tools/call without a session id is still served",
			method:     http.MethodPost,
			headers:    map[string]string{"Content-Type": "application/json", "MCP-Protocol-Version": "2025-06-18"},
			body:       `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"gw-echo","arguments":{"text":"hi"}}}`,
			wantStatus: http.StatusOK,
			wantInBody: `"hi"`,
		},
		{
			name:       "modern request whose Mcp-Method header disagrees with the body",
			method:     http.MethodPost,
			headers:    map[string]string{"Content-Type": "application/json", "MCP-Protocol-Version": "2026-07-28", "Mcp-Method": "tools/call"},
			body:       modernList,
			wantStatus: http.StatusBadRequest,
			wantInBody: "-32020",
		},
		{
			name:       "unsupported protocol version",
			method:     http.MethodPost,
			headers:    map[string]string{"Content-Type": "application/json", "MCP-Protocol-Version": "2099-01-01", "Mcp-Method": "tools/list"},
			body:       strings.Replace(modernList, "2026-07-28", "2099-01-01", 1),
			wantStatus: http.StatusBadRequest,
			wantInBody: "-32022",
		},
		{
			name:       "GET stream is legacy only",
			method:     http.MethodGet,
			headers:    map[string]string{"MCP-Protocol-Version": "2026-07-28", "Accept": "text/event-stream"},
			wantStatus: http.StatusMethodNotAllowed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, baseURL+"/mcp", strings.NewReader(tc.body))
			require.NoError(t, err)
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			var body strings.Builder
			_, _ = io.Copy(&body, resp.Body) // a stream that never ends is cut by the timeout
			assert.Equal(t, tc.wantStatus, resp.StatusCode, body.String())
			assert.Contains(t, body.String(), tc.wantInBody)
		})
	}
}

// Browser requests to /mcp are refused with 403 when their Origin may not reach it (Streamable
// HTTP "Security Warning"). While /mcp accepts unauthenticated calls the bare "*" does not count and
// same-origin is not exempt; with auth enforced allowed_origins applies as configured. Requests
// without an Origin are never affected.
func TestMCPGateway_OriginValidation(t *testing.T) {
	const list = `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	cases := []struct {
		name       string
		enforce    bool
		allowed    []string
		origin     string
		method     string
		wantStatus int
	}{
		{"no origin, open", false, []string{"*"}, "", http.MethodPost, http.StatusOK},
		{"localhost origin, open", false, []string{"*"}, "http://localhost:5173", http.MethodPost, http.StatusOK},
		{"unlisted origin, open, wildcard does not count", false, []string{"*"}, "https://evil.example", http.MethodPost, http.StatusForbidden},
		{"unlisted origin, open, GET", false, []string{"*"}, "https://evil.example", http.MethodGet, http.StatusForbidden},
		{"unlisted origin, open, DELETE", false, []string{"*"}, "https://evil.example", http.MethodDelete, http.StatusForbidden},
		{"rebound page is same-origin, open", false, []string{"*"}, "http://rebound.example", http.MethodPost, http.StatusForbidden},
		{"listed origin, open", false, []string{"*", "https://app.example.com"}, "https://app.example.com", http.MethodPost, http.StatusOK},
		{"pattern-listed origin, open", false, []string{"https://*.example.com"}, "https://tools.example.com", http.MethodPost, http.StatusOK},
		{"unlisted origin, auth enforced, wildcard allows", true, []string{"*"}, "https://app.example.com", http.MethodPost, http.StatusUnauthorized},
		{"unlisted origin, auth enforced, not in list", true, []string{"https://app.example.com"}, "https://evil.example", http.MethodPost, http.StatusForbidden},
		{"no origin, auth enforced", true, []string{"https://app.example.com"}, "", http.MethodPost, http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tm := &echoToolManager{fakeInstructionsToolManager{instructions: "gateway instructions"}}
			cfg := &lib.Config{ClientConfig: &configstore.ClientConfig{EnforceAuthOnInference: tc.enforce, AllowedOrigins: tc.allowed}}
			h, err := NewMCPServerHandler(context.Background(), cfg, tm, nil, nil, nil)
			require.NoError(t, err)
			baseURL := startGateway(t, h)

			var body io.Reader
			if tc.method == http.MethodPost {
				body = strings.NewReader(list)
			}
			req, err := http.NewRequest(tc.method, baseURL+"/mcp", body)
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("MCP-Protocol-Version", "2025-06-18")
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
				// A DNS-rebound page reaches the gateway under its own name, so Origin and Host agree.
				if tc.origin == "http://rebound.example" {
					req.Host = "rebound.example"
				}
			}
			resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			respBody, _ := io.ReadAll(resp.Body)
			if tc.wantStatus == http.StatusForbidden {
				require.Equal(t, tc.wantStatus, resp.StatusCode, string(respBody))
				assert.Contains(t, string(respBody), "allowed_origins")
				return
			}
			assert.NotEqual(t, http.StatusForbidden, resp.StatusCode, string(respBody))
			if tc.method == http.MethodPost {
				assert.Equal(t, tc.wantStatus, resp.StatusCode, string(respBody))
			}
		})
	}
}

// BenchmarkMCPGateway_RefuseOrigin measures the Origin check every /mcp request pays.
func BenchmarkMCPGateway_RefuseOrigin(b *testing.B) {
	for _, tc := range []struct{ name, origin string }{
		{"no origin", ""},
		{"localhost origin", "http://localhost:5173"},
		{"listed origin", "https://app.example.com"},
	} {
		b.Run(tc.name, func(b *testing.B) {
			h := &MCPServerHandler{config: &lib.Config{ClientConfig: &configstore.ClientConfig{AllowedOrigins: []string{"*", "https://app.example.com"}}}}
			ctx := &fasthttp.RequestCtx{}
			if tc.origin != "" {
				ctx.Request.Header.Set("Origin", tc.origin)
			}
			b.ReportAllocs()
			for b.Loop() {
				if h.refuseOrigin(ctx) {
					b.Fatal("refused")
				}
			}
		})
	}
}

// A registry change reaches clients holding a notification stream, on every protocol revision.
func TestMCPGateway_ToolsListChangedReachesListeners(t *testing.T) {
	h := newGatewayHandler(t)
	baseURL := startGateway(t, h)
	for _, version := range []string{mcp.ProtocolVersion20250618, mcp.ProtocolVersion20251125, mcp.ProtocolVersion20260728} {
		t.Run(version, func(t *testing.T) {
			ctx := context.Background()
			// Legacy clients listen on the GET stream; 2026-07-28 clients open subscriptions/listen.
			var opts []transport.StreamableHTTPCOption
			if !mcp.IsModernProtocol(version) {
				opts = append(opts, transport.WithContinuousListening())
			}
			tr, err := transport.NewStreamableHTTP(baseURL+"/mcp", opts...)
			require.NoError(t, err)
			c := client.NewClient(tr, client.WithProtocolVersion(version))
			var changed atomic.Bool
			c.OnNotification(func(n mcp.JSONRPCNotification) {
				if n.Method == string(mcp.MethodNotificationToolsListChanged) {
					changed.Store(true)
				}
			})
			require.NoError(t, c.Start(ctx))
			t.Cleanup(func() { _ = c.Close() })
			init := mcp.InitializeRequest{}
			init.Params.ProtocolVersion = version
			_, err = c.Initialize(ctx, init)
			require.NoError(t, err)
			if mcp.IsModernProtocol(version) {
				stop, err := c.ListenAsync(ctx, mcp.SubscriptionFilter{ToolsListChanged: true}, nil)
				require.NoError(t, err)
				t.Cleanup(stop)
			}

			// The stream opens asynchronously, so keep changing the registry until one lands.
			require.Eventually(t, func() bool {
				require.NoError(t, h.SyncMCPServer(ctx))
				return changed.Load()
			}, 5*time.Second, 50*time.Millisecond)
		})
	}
}

// A legacy GET stream answers with its headers straight away rather than at the first heartbeat.
func TestMCPGateway_GetStreamCommitsHeadersPromptly(t *testing.T) {
	baseURL := startGateway(t, newGatewayHandler(t))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/mcp", nil)
	require.NoError(t, err)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("MCP-Protocol-Version", mcp.ProtocolVersion20251125)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
}

// A client that hangs up ends its stream on the server, legacy GET and subscriptions/listen alike.
func TestMCPGateway_StreamEndsWhenClientLeaves(t *testing.T) {
	baseURL := startGateway(t, newGatewayHandler(t))
	listen := `{"jsonrpc":"2.0","id":1,"method":"subscriptions/listen","params":{"notifications":{"toolsListChanged":true},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{},"io.modelcontextprotocol/clientInfo":{"name":"raw","version":"0"}}}}`
	open := func(method, body string, headers map[string]string) {
		ctx, cancel := context.WithCancel(context.Background())
		req, err := http.NewRequestWithContext(ctx, method, baseURL+"/mcp", strings.NewReader(body))
		require.NoError(t, err)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
		cancel()
		resp.Body.Close()
	}

	for range 5 {
		open(http.MethodGet, "", map[string]string{"Accept": "text/event-stream", "MCP-Protocol-Version": mcp.ProtocolVersion20251125})
		open(http.MethodPost, listen, map[string]string{
			"Content-Type": "application/json", "Accept": "application/json, text/event-stream",
			"MCP-Protocol-Version": mcp.ProtocolVersion20260728, "Mcp-Method": "subscriptions/listen",
		})
	}
	require.Eventually(t, func() bool {
		buf := make([]byte, 1<<20)
		stacks := string(buf[:runtime.Stack(buf, true)])
		return !strings.Contains(stacks, "handlers.streamMCP") && !strings.Contains(stacks, "handlers.(*MCPServerHandler).serveMCPStream.func")
	}, 5*time.Second, 50*time.Millisecond, "stream goroutines still running after their clients left")
}

// /mcp/{slug} is served by the same transport, narrowed to the slug's tools.
func TestMCPGateway_SlugEndpoint(t *testing.T) {
	h := newGatewayHandler(t)
	h.admitter = slugAdmitter{vmcps: map[string][]string{"echo-only": {"gw-echo"}, "nothing": {"other-tool"}}}
	baseURL := startGateway(t, h)
	for slug, want := range map[string]int{"echo-only": 1, "nothing": 0} {
		t.Run(slug, func(t *testing.T) {
			ctx := context.Background()
			tr, err := transport.NewStreamableHTTP(baseURL + "/mcp/" + slug)
			require.NoError(t, err)
			c := client.NewClient(tr)
			require.NoError(t, c.Start(ctx))
			t.Cleanup(func() { _ = c.Close() })
			_, err = c.Initialize(ctx, mcp.InitializeRequest{})
			require.NoError(t, err)
			tools, err := c.ListTools(ctx, mcp.ListToolsRequest{})
			require.NoError(t, err)
			assert.Len(t, tools.Tools, want)
		})
	}
}

// refusingToolManager refuses a call the request may not make, as core does after the MCP pre-hooks,
// and counts the calls that reached it.
type refusingToolManager struct {
	echoToolManager
	calls atomic.Int32
}

func (r *refusingToolManager) ExecuteChatMCPTool(ctx context.Context, call *schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, *schemas.BifrostError) {
	r.calls.Add(1)
	if len(r.GetAvailableMCPTools(ctx)) == 0 {
		return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "tool 'gw-echo' is not permitted (filtered by request context)"}}
	}
	return r.echoToolManager.ExecuteChatMCPTool(ctx, call)
}

// A narrowed caller's tools/call for a tool it cannot see still reaches Bifrost, so the pre-hooks
// (governance, logging) run and the refusal says why, instead of mcp-go answering "not found".
func TestMCPGateway_NarrowedToolCallReachesBifrost(t *testing.T) {
	tm := &refusingToolManager{echoToolManager: echoToolManager{fakeInstructionsToolManager{}}}
	h, err := NewMCPServerHandler(context.Background(), &lib.Config{ClientConfig: &configstore.ClientConfig{}}, tm, nil, nil, nil)
	require.NoError(t, err)
	h.admitter = slugAdmitter{vmcps: map[string][]string{"nothing": {"other-tool"}}}
	baseURL := startGateway(t, h)

	ctx := context.Background()
	tr, err := transport.NewStreamableHTTP(baseURL + "/mcp/nothing")
	require.NoError(t, err)
	c := client.NewClient(tr)
	require.NoError(t, c.Start(ctx))
	t.Cleanup(func() { _ = c.Close() })
	_, err = c.Initialize(ctx, mcp.InitializeRequest{})
	require.NoError(t, err)

	tools, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	require.NoError(t, err)
	assert.Empty(t, tools.Tools, "tools/list stays narrowed")
	assert.Empty(t, tools.NextCursor, "tools/list is not paginated: narrowListedTools narrows the one page")

	call := mcp.CallToolRequest{}
	call.Params.Name = "gw-echo"
	res, err := c.CallTool(ctx, call)
	require.NoError(t, err, "the refusal is a tool result, not a JSON-RPC error")
	assert.True(t, res.IsError)
	require.Len(t, res.Content, 1)
	assert.Contains(t, res.Content[0].(mcp.TextContent).Text, "filtered by request context")
	assert.Equal(t, int32(1), tm.calls.Load(), "the call reached Bifrost")
}

// servingToolManager adds one prompt, resource and resource template from the "gw" client, visible
// exactly when gw-echo is.
type servingToolManager struct {
	echoToolManager
}

func (s *servingToolManager) visible(ctx context.Context) bool {
	return len(s.GetAvailableMCPTools(ctx)) > 0
}

func (s *servingToolManager) GetAvailableMCPPrompts(ctx context.Context) []mcp.Prompt {
	if !s.visible(ctx) {
		return nil
	}
	return []mcp.Prompt{mcp.NewPrompt("gw-review", mcp.WithArgument("diff"))}
}

func (s *servingToolManager) GetAvailableMCPResources(ctx context.Context) []mcp.Resource {
	if !s.visible(ctx) {
		return nil
	}
	return []mcp.Resource{mcp.NewResource("file:///gw.md", "gw")}
}

func (s *servingToolManager) GetAvailableMCPResourceTemplates(ctx context.Context) []mcp.ResourceTemplate {
	if !s.visible(ctx) {
		return nil
	}
	return []mcp.ResourceTemplate{mcp.NewResourceTemplate("gw://notes/{id}", "notes")}
}

func (s *servingToolManager) GetMCPPrompt(ctx context.Context, name string, arguments map[string]string) (*mcp.GetPromptResult, *schemas.BifrostError) {
	if !s.visible(ctx) || name != "gw-review" {
		return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "prompt not found", Error: coremcp.ErrNotFound}}
	}
	return mcp.NewGetPromptResult("review", []mcp.PromptMessage{mcp.NewPromptMessage(mcp.RoleUser, mcp.NewTextContent("Review: "+arguments["diff"]))}), nil
}

func (s *servingToolManager) ReadMCPResource(ctx context.Context, uri string) (*mcp.ReadResourceResult, *schemas.BifrostError) {
	if !s.visible(ctx) {
		return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "resource not found", Error: coremcp.ErrNotFound}}
	}
	return &mcp.ReadResourceResult{Contents: []mcp.ResourceContents{mcp.TextResourceContents{URI: uri, Text: "read " + uri}}}, nil
}

func (s *servingToolManager) CompleteMCPArgument(ctx context.Context, params mcp.CompleteParams) (*mcp.CompleteResult, *schemas.BifrostError) {
	ref, _ := params.Ref.(mcp.PromptReference)
	return &mcp.CompleteResult{Completion: mcp.Completion{Values: []string{ref.Name + ":" + params.Argument.Value}}}, nil
}

func newServingGatewayHandler(t testing.TB) *MCPServerHandler {
	t.Helper()
	tm := &servingToolManager{echoToolManager{fakeInstructionsToolManager{}}}
	h, err := NewMCPServerHandler(context.Background(), &lib.Config{ClientConfig: &configstore.ClientConfig{}}, tm, nil, nil, nil)
	require.NoError(t, err)
	return h
}

// Upstream prompts, resources and completions are served on /mcp in every protocol revision.
func TestMCPGateway_ServesPromptsAndResources(t *testing.T) {
	baseURL := startGateway(t, newServingGatewayHandler(t))
	for _, version := range []string{mcp.ProtocolVersion20250618, mcp.ProtocolVersion20251125, mcp.ProtocolVersion20260728} {
		t.Run(version, func(t *testing.T) {
			ctx := context.Background()
			tr, err := transport.NewStreamableHTTP(baseURL + "/mcp")
			require.NoError(t, err)
			c := client.NewClient(tr, client.WithProtocolVersion(version))
			require.NoError(t, c.Start(ctx))
			t.Cleanup(func() { _ = c.Close() })
			init := mcp.InitializeRequest{}
			init.Params.ProtocolVersion = version
			result, err := c.Initialize(ctx, init)
			require.NoError(t, err)
			require.NotNil(t, result.Capabilities.Prompts)
			require.NotNil(t, result.Capabilities.Resources)
			require.NotNil(t, result.Capabilities.Completions)

			prompts, err := c.ListPrompts(ctx, mcp.ListPromptsRequest{})
			require.NoError(t, err)
			require.Len(t, prompts.Prompts, 1)
			get := mcp.GetPromptRequest{}
			get.Params.Name = "gw-review"
			get.Params.Arguments = map[string]string{"diff": "+1"}
			prompt, err := c.GetPrompt(ctx, get)
			require.NoError(t, err)
			assert.Equal(t, "Review: +1", prompt.Messages[0].Content.(mcp.TextContent).Text)

			resources, err := c.ListResources(ctx, mcp.ListResourcesRequest{})
			require.NoError(t, err)
			require.Len(t, resources.Resources, 1)
			templates, err := c.ListResourceTemplates(ctx, mcp.ListResourceTemplatesRequest{})
			require.NoError(t, err)
			require.Len(t, templates.ResourceTemplates, 1)
			for _, uri := range []string{"file:///gw.md", "gw://notes/7"} {
				read := mcp.ReadResourceRequest{}
				read.Params.URI = uri
				contents, err := c.ReadResource(ctx, read)
				require.NoError(t, err, uri)
				assert.Equal(t, "read "+uri, contents.Contents[0].(mcp.TextResourceContents).Text)
			}

			complete := mcp.CompleteRequest{}
			complete.Params.Ref = mcp.PromptReference{Type: "ref/prompt", Name: "gw-review"}
			complete.Params.Argument = mcp.CompleteArgument{Name: "diff", Value: "+"}
			completion, err := c.Complete(ctx, complete)
			require.NoError(t, err)
			assert.Equal(t, []string{"gw-review:+"}, completion.Completion.Values)
		})
	}
}

// capabilityRecordingToolManager records the caller capabilities each upstream-bound request carried.
type capabilityRecordingToolManager struct {
	servingToolManager
	mu   sync.Mutex
	seen map[string]*mcp.ClientCapabilities
}

func (r *capabilityRecordingToolManager) record(ctx context.Context, method string) {
	caps, _ := ctx.Value(schemas.BifrostContextKeyMCPCallerCapabilities).(*mcp.ClientCapabilities)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen[method] = caps
}

func (r *capabilityRecordingToolManager) ExecuteChatMCPTool(ctx context.Context, call *schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, *schemas.BifrostError) {
	r.record(ctx, string(mcp.MethodToolsCall))
	return r.servingToolManager.ExecuteChatMCPTool(ctx, call)
}

func (r *capabilityRecordingToolManager) GetMCPPrompt(ctx context.Context, name string, arguments map[string]string) (*mcp.GetPromptResult, *schemas.BifrostError) {
	r.record(ctx, string(mcp.MethodPromptsGet))
	return r.servingToolManager.GetMCPPrompt(ctx, name, arguments)
}

func (r *capabilityRecordingToolManager) ReadMCPResource(ctx context.Context, uri string) (*mcp.ReadResourceResult, *schemas.BifrostError) {
	r.record(ctx, string(mcp.MethodResourcesRead))
	return r.servingToolManager.ReadMCPResource(ctx, uri)
}

func (r *capabilityRecordingToolManager) CompleteMCPArgument(ctx context.Context, params mcp.CompleteParams) (*mcp.CompleteResult, *schemas.BifrostError) {
	r.record(ctx, "completion/complete")
	return r.servingToolManager.CompleteMCPArgument(ctx, params)
}

// Every gateway request that reaches an upstream carries the capabilities its caller declared, so core
// can declare them upstream. Only 2026-07-28 requests declare capabilities per request.
func TestMCPGateway_RecordsCallerCapabilities(t *testing.T) {
	declared := mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapability{}}
	for _, version := range []string{mcp.ProtocolVersion20250618, mcp.ProtocolVersion20251125, mcp.ProtocolVersion20260728} {
		t.Run(version, func(t *testing.T) {
			tm := &capabilityRecordingToolManager{
				servingToolManager: servingToolManager{echoToolManager{fakeInstructionsToolManager{}}},
				seen:               map[string]*mcp.ClientCapabilities{},
			}
			h, err := NewMCPServerHandler(context.Background(), &lib.Config{ClientConfig: &configstore.ClientConfig{}}, tm, nil, nil, nil)
			require.NoError(t, err)
			baseURL := startGateway(t, h)

			ctx := context.Background()
			tr, err := transport.NewStreamableHTTP(baseURL + "/mcp")
			require.NoError(t, err)
			c := client.NewClient(tr, client.WithProtocolVersion(version), client.WithClientCapabilities(declared))
			require.NoError(t, c.Start(ctx))
			t.Cleanup(func() { _ = c.Close() })
			init := mcp.InitializeRequest{}
			init.Params.ProtocolVersion = version
			_, err = c.Initialize(ctx, init)
			require.NoError(t, err)

			call := mcp.CallToolRequest{}
			call.Params.Name = "gw-echo"
			_, err = c.CallTool(ctx, call)
			require.NoError(t, err)
			get := mcp.GetPromptRequest{}
			get.Params.Name = "gw-review"
			_, err = c.GetPrompt(ctx, get)
			require.NoError(t, err)
			read := mcp.ReadResourceRequest{}
			read.Params.URI = "file:///gw.md"
			_, err = c.ReadResource(ctx, read)
			require.NoError(t, err)
			complete := mcp.CompleteRequest{}
			complete.Params.Ref = mcp.PromptReference{Type: "ref/prompt", Name: "gw-review"}
			complete.Params.Argument = mcp.CompleteArgument{Name: "diff", Value: "+"}
			_, err = c.Complete(ctx, complete)
			require.NoError(t, err)

			tm.mu.Lock()
			defer tm.mu.Unlock()
			for _, method := range []string{"tools/call", "prompts/get", "resources/read", "completion/complete"} {
				caps, reached := tm.seen[method]
				require.True(t, reached, "%s did not reach Bifrost", method)
				if version != mcp.ProtocolVersion20260728 {
					assert.Nil(t, caps, "%s: a legacy request declares capabilities only at initialize", method)
					continue
				}
				require.NotNil(t, caps, "%s: the caller's capabilities must reach Bifrost", method)
				assert.Equal(t, declared, *caps, method)
			}
		})
	}
}

// inputRequiringToolManager answers gw-echo with an upstream's input_required, as core passes it to a
// caller that can answer, until the caller retries with its answers.
type inputRequiringToolManager struct {
	echoToolManager
	resumed atomic.Pointer[mcp.MultiRoundTripParams]
	carried atomic.Pointer[string]
}

func (m *inputRequiringToolManager) ExecuteChatMCPTool(ctx context.Context, _ *schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, *schemas.BifrostError) {
	carried, _ := ctx.Value(schemas.BifrostContextKeyMCPRoundTripID).(string)
	m.carried.Store(&carried)
	if resume, _ := ctx.Value(schemas.BifrostContextKeyMCPInputResumption).(*mcp.MultiRoundTripParams); resume != nil {
		m.resumed.Store(resume)
		if len(resume.InputResponses) > 0 {
			done := "deployed to staging"
			return &schemas.ChatMessage{Content: &schemas.ChatMessageContent{ContentStr: &done}}, nil
		}
	}
	result := &mcp.CallToolResult{
		Result:               mcp.Result{ResultType: mcp.ResultTypeInputRequired},
		MultiRoundTripResult: mcp.MultiRoundTripResult{RequestState: "s1", InputRequests: mcp.InputRequests{"env": {Method: mcp.MethodElicitationCreate, Elicitation: &mcp.ElicitationParams{Message: "Which environment?"}}}},
	}
	empty := ""
	content := &schemas.ChatMessageContent{ContentStr: &empty}
	message := &schemas.ChatMessage{Content: content, ChatToolMessage: &schemas.ChatToolMessage{}}
	message.ChatToolMessage.SetMCPResult(result, content)
	return message, nil
}

// A 2026-07-28 caller gets an upstream's input_required with the upstream's requestState wrapped in the
// call's round-trip id; its retry's answers reach core with the upstream's own state, and the id stays
// with the call across rounds while a separate call gets its own.
func TestMCPGateway_PassesInputRequiredThrough(t *testing.T) {
	tm := &inputRequiringToolManager{echoToolManager: echoToolManager{fakeInstructionsToolManager{}}}
	h, err := NewMCPServerHandler(context.Background(), &lib.Config{ClientConfig: &configstore.ClientConfig{}}, tm, nil, nil, nil)
	require.NoError(t, err)
	baseURL := startGateway(t, h)

	ctx := context.Background()
	tr, err := transport.NewStreamableHTTP(baseURL + "/mcp")
	require.NoError(t, err)
	c := client.NewClient(tr, client.WithProtocolVersion(mcp.ProtocolVersion20260728), client.WithManualInputRoundTrips(),
		client.WithClientCapabilities(mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapability{}}))
	require.NoError(t, c.Start(ctx))
	t.Cleanup(func() { _ = c.Close() })
	init := mcp.InitializeRequest{}
	init.Params.ProtocolVersion = mcp.ProtocolVersion20260728
	_, err = c.Initialize(ctx, init)
	require.NoError(t, err)

	call := mcp.CallToolRequest{}
	call.Params.Name = "gw-echo"
	result, err := c.CallTool(ctx, call)
	require.NoError(t, err)
	require.True(t, result.NeedsInput(), "the caller gets the upstream's input_required")
	require.Contains(t, result.InputRequests, "env")
	assert.Equal(t, "Which environment?", result.InputRequests["env"].Elicitation.Message)
	id, upstream := coremcp.UnwrapRoundTripState(result.RequestState)
	require.NotEmpty(t, id, "the requestState names the call: %q", result.RequestState)
	assert.Equal(t, "s1", upstream)
	assert.Nil(t, tm.resumed.Load(), "a first call carries nothing to resume")
	assert.Empty(t, *tm.carried.Load())

	// A retry without answers is asked again and keeps the call's id.
	retry := call
	retry.Params.RequestState = result.RequestState
	again, err := c.CallTool(ctx, retry)
	require.NoError(t, err)
	require.True(t, again.NeedsInput())
	assert.Equal(t, result.RequestState, again.RequestState, "a round that asks again keeps the call's id")
	assert.Equal(t, id, *tm.carried.Load(), "core sees the id the retry carried")
	assert.Equal(t, "s1", tm.resumed.Load().RequestState, "the upstream gets its own state back")

	// A separate call to the same server, which issues the same state, is its own round trip.
	other, err := c.CallTool(ctx, call)
	require.NoError(t, err)
	otherID, otherUpstream := coremcp.UnwrapRoundTripState(other.RequestState)
	assert.Equal(t, "s1", otherUpstream)
	assert.NotEqual(t, id, otherID, "separate calls get separate ids")

	answer := mcp.ElicitationResult{ElicitationResponse: mcp.ElicitationResponse{Action: mcp.ElicitationResponseActionAccept, Content: map[string]any{"env": "staging"}}}
	call.Params.InputResponses = mcp.InputResponses{"env": mcp.NewElicitationInputResponse(answer)}
	call.Params.RequestState = again.RequestState
	result, err = c.CallTool(ctx, call)
	require.NoError(t, err)
	require.False(t, result.NeedsInput())
	assert.Equal(t, "deployed to staging", result.Content[0].(mcp.TextContent).Text)
	resumed := tm.resumed.Load()
	require.NotNil(t, resumed, "the retry's answers must reach core")
	assert.Equal(t, "s1", resumed.RequestState)
	require.Contains(t, resumed.InputResponses, "env")
	assert.Equal(t, id, *tm.carried.Load())

	// A state Bifrost did not wrap reaches the upstream unchanged, with no id.
	call.Params.RequestState = "s1"
	_, err = c.CallTool(ctx, call)
	require.NoError(t, err)
	assert.Equal(t, "s1", tm.resumed.Load().RequestState)
	assert.Empty(t, *tm.carried.Load())
}

// A caller narrowed away from the owning client lists none of its prompts or resources and gets
// not-found for them.
func TestMCPGateway_NarrowedCallerCannotReachPromptsOrResources(t *testing.T) {
	h := newServingGatewayHandler(t)
	h.admitter = slugAdmitter{vmcps: map[string][]string{"nothing": {"other-tool"}}}
	baseURL := startGateway(t, h)
	// The resource not-found code changed in 2026-07-28.
	for version, notFound := range map[string]error{mcp.ProtocolVersion20251125: mcp.ErrResourceNotFound, mcp.ProtocolVersion20260728: mcp.ErrInvalidParams} {
		t.Run(version, func(t *testing.T) {
			ctx := context.Background()
			tr, err := transport.NewStreamableHTTP(baseURL + "/mcp/nothing")
			require.NoError(t, err)
			c := client.NewClient(tr, client.WithProtocolVersion(version))
			require.NoError(t, c.Start(ctx))
			t.Cleanup(func() { _ = c.Close() })
			init := mcp.InitializeRequest{}
			init.Params.ProtocolVersion = version
			_, err = c.Initialize(ctx, init)
			require.NoError(t, err)

			prompts, err := c.ListPrompts(ctx, mcp.ListPromptsRequest{})
			require.NoError(t, err)
			assert.Empty(t, prompts.Prompts)
			resources, err := c.ListResources(ctx, mcp.ListResourcesRequest{})
			require.NoError(t, err)
			assert.Empty(t, resources.Resources)
			templates, err := c.ListResourceTemplates(ctx, mcp.ListResourceTemplatesRequest{})
			require.NoError(t, err)
			assert.Empty(t, templates.ResourceTemplates)

			get := mcp.GetPromptRequest{}
			get.Params.Name = "gw-review"
			_, err = c.GetPrompt(ctx, get)
			require.ErrorIs(t, err, mcp.ErrInvalidParams)
			read := mcp.ReadResourceRequest{}
			read.Params.URI = "gw://notes/7"
			_, err = c.ReadResource(ctx, read)
			require.ErrorIs(t, err, notFound)
		})
	}
}

// chartToolManager serves one tool, "gw-chart", that declares a title, an icon and an output schema,
// and answers with text, an image and structured content. With {"rewrite": true} a plugin is taken to
// have rewritten the answer and marked it failed.
type chartToolManager struct {
	fakeInstructionsToolManager
}

func (c *chartToolManager) GetAvailableMCPTools(context.Context) []schemas.ChatTool {
	return []schemas.ChatTool{{
		Type:     schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{Name: "gw-chart"},
		MCP: &schemas.MCPToolMetadata{
			Title:        "Chart",
			Icons:        []mcp.Icon{{Src: "https://example.com/chart.png", MIMEType: "image/png"}},
			OutputSchema: json.RawMessage(`{"type":"object","properties":{"points":{"type":"number"}}}`),
		},
	}}
}

func (c *chartToolManager) ExecuteChatMCPTool(_ context.Context, call *schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, *schemas.BifrostError) {
	upstream := &mcp.CallToolResult{
		Content:           []mcp.Content{mcp.NewTextContent("3 points"), mcp.NewImageContent("iVBORw0KGgo=", "image/png")},
		StructuredContent: map[string]any{"points": 3},
	}
	text := "3 points"
	content := &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
		{Type: schemas.ChatContentBlockTypeText, Text: &text},
		{Type: schemas.ChatContentBlockTypeImage, ImageURLStruct: &schemas.ChatInputImage{URL: "data:image/png;base64,iVBORw0KGgo="}},
	}}
	toolMessage := &schemas.ChatToolMessage{ToolCallID: call.ID}
	toolMessage.SetMCPResult(upstream, content)
	if strings.Contains(call.Function.Arguments, "rewrite") {
		redacted := "[redacted]"
		content.ContentBlocks[0].Text = &redacted
		toolMessage.IsError = schemas.Ptr(true)
	}
	return &schemas.ChatMessage{Role: schemas.ChatMessageRoleTool, Content: content, ChatToolMessage: toolMessage}, nil
}

// The gateway lists a tool's title, icons and output schema, and answers with the upstream's own result
// (images and structured content included); a result a plugin rewrote is rebuilt from the message
// instead, and isError follows the message either way.
func TestMCPGateway_ToolMetadataAndResultFidelity(t *testing.T) {
	h, err := NewMCPServerHandler(context.Background(), &lib.Config{ClientConfig: &configstore.ClientConfig{}}, &chartToolManager{}, nil, nil, nil)
	require.NoError(t, err)
	baseURL := startGateway(t, h)
	for _, version := range []string{mcp.ProtocolVersion20250618, mcp.ProtocolVersion20251125, mcp.ProtocolVersion20260728} {
		t.Run(version, func(t *testing.T) {
			ctx := context.Background()
			tr, err := transport.NewStreamableHTTP(baseURL + "/mcp")
			require.NoError(t, err)
			c := client.NewClient(tr, client.WithProtocolVersion(version))
			require.NoError(t, c.Start(ctx))
			t.Cleanup(func() { _ = c.Close() })
			init := mcp.InitializeRequest{}
			init.Params.ProtocolVersion = version
			_, err = c.Initialize(ctx, init)
			require.NoError(t, err)

			tools, err := c.ListTools(ctx, mcp.ListToolsRequest{})
			require.NoError(t, err)
			require.Len(t, tools.Tools, 1)
			listed := tools.Tools[0]
			assert.Equal(t, "Chart", listed.Title)
			require.Len(t, listed.Icons, 1)
			assert.Equal(t, "https://example.com/chart.png", listed.Icons[0].Src)
			assert.Equal(t, "object", listed.OutputSchema.Type)

			call := mcp.CallToolRequest{}
			call.Params.Name = "gw-chart"
			result, err := c.CallTool(ctx, call)
			require.NoError(t, err)
			assert.False(t, result.IsError)
			require.Len(t, result.Content, 2)
			assert.Equal(t, "image/png", result.Content[1].(mcp.ImageContent).MIMEType)
			assert.Equal(t, map[string]any{"points": float64(3)}, result.StructuredContent)

			call.Params.Arguments = map[string]any{"rewrite": true}
			rewritten, err := c.CallTool(ctx, call)
			require.NoError(t, err)
			assert.True(t, rewritten.IsError, "a failed result is not reported as a success")
			assert.Equal(t, "[redacted]", rewritten.Content[0].(mcp.TextContent).Text)
			assert.Nil(t, rewritten.StructuredContent, "the raw result never reaches the caller once rewritten")
		})
	}
}

// BenchmarkMCPGateway drives the /mcp handler in process: admission, transport and the server,
// tools only and with upstream prompts and resources served.
func BenchmarkMCPGateway(b *testing.B) {
	handlers := map[string]*MCPServerHandler{"tools-only": newGatewayHandler(b), "serving": newServingGatewayHandler(b)}
	legacy := map[string]string{"Content-Type": "application/json", "Accept": "application/json, text/event-stream", "MCP-Protocol-Version": "2025-11-25"}
	narrowed := func(tools string) map[string]string {
		return map[string]string{"Content-Type": "application/json", "Accept": "application/json, text/event-stream", "MCP-Protocol-Version": "2025-11-25", "x-bf-mcp-include-tools": tools}
	}
	modernMeta := `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{},"io.modelcontextprotocol/clientInfo":{"name":"bench","version":"0"}}`
	modern := func(method, name string) map[string]string {
		headers := map[string]string{"Content-Type": "application/json", "Accept": "application/json, text/event-stream", "MCP-Protocol-Version": "2026-07-28", "Mcp-Method": method}
		if name != "" {
			headers["Mcp-Name"] = name
		}
		return headers
	}
	cases := []struct {
		name    string
		headers map[string]string
		body    string
	}{
		{"initialize/2025-11-25", map[string]string{"Content-Type": "application/json", "Accept": "application/json, text/event-stream"}, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"bench","version":"0"}}}`},
		{"tools-list/2025-11-25", legacy, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`},
		{"tools-call/2025-11-25", legacy, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"gw-echo","arguments":{"text":"hi"}}}`},
		{"tools-list-narrowed/2025-11-25", narrowed("gw-echo"), `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`},
		{"tools-call-hidden/2025-11-25", narrowed("other-tool"), `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"gw-echo","arguments":{"text":"hi"}}}`},
		{"discover/2026-07-28", modern("server/discover", ""), `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{` + modernMeta + `}}`},
		{"tools-list/2026-07-28", modern("tools/list", ""), `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{` + modernMeta + `}}`},
		{"tools-call/2026-07-28", modern("tools/call", "gw-echo"), `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"gw-echo","arguments":{"text":"hi"},` + modernMeta + `}}`},
		{"prompts-get/2025-11-25", legacy, `{"jsonrpc":"2.0","id":1,"method":"prompts/get","params":{"name":"gw-review","arguments":{"diff":"+1"}}}`},
		{"resources-read/2025-11-25", legacy, `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"gw://notes/7"}}`},
	}
	for _, name := range []string{"tools-only", "serving"} {
		h := handlers[name]
		for _, tc := range cases {
			if name == "tools-only" && (strings.HasPrefix(tc.name, "prompts-") || strings.HasPrefix(tc.name, "resources-")) {
				continue
			}
			benchmarkGatewayRequest(b, h, name+"/"+tc.name, "", tc.headers, tc.body)
		}
	}

	// MCP Apps: the per-request work once app tools and ui:// resources exist.
	apps := newAppsGateway(b)
	appsMeta := `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{"extensions":{"io.modelcontextprotocol/ui":{}}},"io.modelcontextprotocol/clientInfo":{"name":"bench","version":"0"}}`
	toolsList := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{` + appsMeta + `}}`
	resourcesList := `{"jsonrpc":"2.0","id":1,"method":"resources/list","params":{` + appsMeta + `}}`
	benchmarkGatewayRequest(b, apps, "apps/tools-list/aggregate/2026-07-28", "", modern("tools/list", ""), toolsList)
	benchmarkGatewayRequest(b, apps, "apps/tools-list/slug/2026-07-28", "apps", modern("tools/list", ""), toolsList)
	benchmarkGatewayRequest(b, apps, "apps/resources-list/aggregate/2026-07-28", "", modern("resources/list", ""), resourcesList)
	benchmarkGatewayRequest(b, apps, "apps/resources-list/slug/2026-07-28", "apps", modern("resources/list", ""), resourcesList)

	// Server-given names: the exact name skips alias resolution; the bare one resolves first.
	callView := func(name string) string {
		return `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + name + `","arguments":{},` + appsMeta + `}}`
	}
	benchmarkGatewayRequest(b, apps, "apps/tools-call-exact/aggregate/2026-07-28", "", modern("tools/call", "apps-view"), callView("apps-view"))
	benchmarkGatewayRequest(b, apps, "apps/tools-call-alias/aggregate/2026-07-28", "", modern("tools/call", "view"), callView("view"))
	benchmarkGatewayRequest(b, apps, "apps/tools-call-alias/slug/2026-07-28", "apps", modern("tools/call", "view"), callView("view"))
}

func benchmarkGatewayRequest(b *testing.B, h *MCPServerHandler, name, slug string, headers map[string]string, body string) {
	b.Run(name, func(b *testing.B) {
		var ctx fasthttp.RequestCtx
		ctx.Request.Header.SetMethod(fasthttp.MethodPost)
		ctx.Request.SetRequestURI("/mcp")
		if slug != "" {
			ctx.Request.SetRequestURI("/mcp/" + slug)
			ctx.SetUserValue("slug", slug)
		}
		for k, v := range headers {
			ctx.Request.Header.Set(k, v)
		}
		ctx.Request.SetBodyString(body)
		b.ReportAllocs()
		for b.Loop() {
			ctx.Response.Reset()
			h.handleMCPServer(&ctx)
			if ctx.Response.StatusCode() != fasthttp.StatusOK {
				b.Fatalf("status %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
			}
		}
	})
}

// Two upstreams can list the same URI; a narrowed caller is shown its own server's entry, whose read
// it gets, not whichever entry the gateway registered for that URI.
func TestKeepCallersEntries_SharedURIShowsCallersEntry(t *testing.T) {
	registered := []mcp.Resource{mcp.NewResource("file:///readme.md", "a-readme", mcp.WithResourceDescription("server a")), mcp.NewResource("file:///other.md", "other")}
	callers := []mcp.Resource{mcp.NewResource("file:///readme.md", "b-readme", mcp.WithResourceDescription("server b"))}
	kept := keepCallersEntries(registered, callers, resourceURI)
	require.Len(t, kept, 1)
	assert.Equal(t, "b-readme", kept[0].Name)
	assert.Equal(t, "server b", kept[0].Description)

	registeredTemplates := []mcp.ResourceTemplate{mcp.NewResourceTemplate("repo://{name}", "a-repo")}
	callerTemplates := []mcp.ResourceTemplate{mcp.NewResourceTemplate("repo://{name}", "b-repo")}
	keptTemplates := keepCallersEntries(registeredTemplates, callerTemplates, coremcp.ResourceTemplateURI)
	require.Len(t, keptTemplates, 1)
	assert.Equal(t, "b-repo", keptTemplates[0].Name)
}

// An upstream's invalid-params or resource-not-found answer reaches the caller with its own code, not as an
// internal error.
func TestUpstreamError_KeepsUpstreamCodes(t *testing.T) {
	code := func(err error) int {
		var provider server.JSONRPCErrorProvider
		if !errors.As(err, &provider) {
			return mcp.INTERNAL_ERROR
		}
		return provider.JSONRPCError().Error.Code
	}
	upstream := func(err error) *schemas.BifrostError {
		return &schemas.BifrostError{Error: &schemas.ErrorField{Message: err.Error(), Error: err}}
	}
	assert.Equal(t, mcp.INVALID_PARAMS, code(upstreamError(upstream(fmt.Errorf("%w: missing diff", mcp.ErrInvalidParams)), mcp.RESOURCE_NOT_FOUND)))
	assert.Equal(t, mcp.RESOURCE_NOT_FOUND, code(upstreamError(upstream(fmt.Errorf("%w: fixture://notes/999", mcp.ErrResourceNotFound)), mcp.RESOURCE_NOT_FOUND)))
	assert.Equal(t, mcp.INTERNAL_ERROR, code(upstreamError(upstream(errors.New("backend down")), mcp.RESOURCE_NOT_FOUND)))
}

// A result the gateway rebuilds after a plugin edit sends a file as MCP asks: bare base64 in blob, with
// its media type, even though the tool message carries it as a data URL.
func TestGatewayToolResult_RebuiltFileIsBareBase64(t *testing.T) {
	message := &schemas.ChatMessage{Role: schemas.ChatMessageRoleTool, Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
		{Type: schemas.ChatContentBlockTypeText, Text: schemas.Ptr("file below (edited)")},
		{Type: schemas.ChatContentBlockTypeFile, File: &schemas.ChatInputFile{FileData: schemas.Ptr("data:text/plain;base64,SGVsbG8="), Filename: schemas.Ptr("hello.txt"), FileType: schemas.Ptr("text/plain")}},
		{Type: schemas.ChatContentBlockTypeFile, File: &schemas.ChatInputFile{FileData: schemas.Ptr("data:application/pdf;base64,JVBERi0xLjQK"), Filename: schemas.Ptr("report.pdf")}},
	}}}
	result := gatewayToolResult(message, false)
	require.Len(t, result.Content, 3)
	for i, want := range []mcp.BlobResourceContents{
		{URI: "file:///hello.txt", MIMEType: "text/plain", Blob: "SGVsbG8="},
		{URI: "file:///report.pdf", MIMEType: "application/pdf", Blob: "JVBERi0xLjQK"},
	} {
		resource, ok := result.Content[i+1].(mcp.EmbeddedResource)
		require.True(t, ok, "content %d: %T", i+1, result.Content[i+1])
		assert.Equal(t, want, resource.Resource)
	}
}

// A result the gateway rebuilds for a tool that declares an output schema carries structured content
// parsed from its JSON text, as MCP requires; other results carry none.
func TestGatewayToolResult_StructuredContentForDeclaredSchema(t *testing.T) {
	jsonText := &schemas.ChatMessage{Role: schemas.ChatMessageRoleTool, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr(`{"total":3}`)}}
	assert.Equal(t, map[string]any{"total": float64(3)}, gatewayToolResult(jsonText, true).StructuredContent)
	assert.Nil(t, gatewayToolResult(jsonText, false).StructuredContent, "no schema, no structured content")
	plain := &schemas.ChatMessage{Role: schemas.ChatMessageRoleTool, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("[redacted]")}}
	rewritten := gatewayToolResult(plain, true)
	assert.Nil(t, rewritten.StructuredContent, "text that is not a JSON object is not invented into one")
	// A success without the structured content its schema promises is not valid MCP; an error needs none.
	assert.True(t, rewritten.IsError, "a rewritten result that no longer fits the output schema is an error")
	require.NotEmpty(t, rewritten.Content)
	assert.Equal(t, "[redacted]", rewritten.Content[0].(mcp.TextContent).Text, "the rewritten text is kept")
	assert.False(t, gatewayToolResult(plain, false).IsError, "without a schema, plain text is a normal success")
}

// A rebuilt result sends audio as MCP asks, bare base64, even when the tool message carries a data URL.
func TestGatewayToolResult_RebuiltAudioIsBareBase64(t *testing.T) {
	message := &schemas.ChatMessage{Role: schemas.ChatMessageRoleTool, Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
		{Type: schemas.ChatContentBlockTypeInputAudio, InputAudio: &schemas.ChatInputAudio{Data: "data:audio/mpeg;base64,SUQz"}},
		{Type: schemas.ChatContentBlockTypeInputAudio, InputAudio: &schemas.ChatInputAudio{Data: "UklGRg==", Format: schemas.Ptr("wav")}},
	}}}
	result := gatewayToolResult(message, false)
	require.Len(t, result.Content, 2)
	assert.Equal(t, mcp.NewAudioContent("SUQz", "audio/mpeg"), result.Content[0])
	assert.Equal(t, mcp.NewAudioContent("UklGRg==", "audio/wav"), result.Content[1])
}

// perCallerToolManager serves the shared gw-echo and a client "notes" with per_user_discovery on,
// whose tools, prompts and resources are each caller's own: alice and bob hold notes-search with
// their own descriptions, bob also notes-export, and carol, who has not connected, is offered
// notes-connect. alice holds the notes-review prompt and file:///alice.md, bob file:///bob.md.
type perCallerToolManager struct {
	echoToolManager
	mu    sync.Mutex
	calls []string // "<tool> as <caller>", for every call that reached Bifrost
	// bobArchives says bob has since discovered notes-archive.
	bobArchives atomic.Bool
}

func callerOf(ctx context.Context) string {
	caller, _ := ctx.Value(schemas.BifrostContextKeyUserID).(string)
	return caller
}

func (p *perCallerToolManager) GetAvailableMCPTools(ctx context.Context) []schemas.ChatTool {
	tools := p.echoToolManager.GetAvailableMCPTools(ctx)
	include, narrowed := ctx.Value(schemas.MCPContextKeyIncludeTools).([]string)
	add := func(name, description string, outputSchema bool) {
		if narrowed && !slices.Contains(include, name) && !slices.Contains(include, "notes-*") {
			return
		}
		tool := schemas.ChatTool{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{
			Name:        name,
			Description: schemas.Ptr(description),
			Parameters: &schemas.ToolFunctionParameters{
				Type:       "object",
				Properties: schemas.NewOrderedMapFromPairs(schemas.KV(callerOf(ctx)+"_query", map[string]any{"type": "string"})),
			},
		}}
		if outputSchema {
			tool.MCP = &schemas.MCPToolMetadata{OutputSchema: json.RawMessage(`{"type":"object"}`)}
		}
		tools = append(tools, tool)
	}
	switch callerOf(ctx) {
	case "alice":
		add("notes-search", "Search alice's notes", true)
	case "bob":
		add("notes-search", "Search bob's notes", false)
		add("notes-export", "Export bob's notes", false)
		if p.bobArchives.Load() {
			add("notes-archive", "Archive bob's notes", false)
		}
	case "carol":
		add("notes-connect", "Connect your notes account", false)
	case "dave":
		add("listToolFiles", "List dave's tool files", false)
		add("executeToolCode", "Run code against dave's tools", false)
	case "gina":
		// MCP Apps, as core serves them: board's UI, and refresh only for callers AppToolsAllowed admits.
		add("notes-board", "Show gina's board", false)
		tools[len(tools)-1].MCP = &schemas.MCPToolMetadata{Meta: json.RawMessage(`{"ui":{"resourceUri":"ui://notes/board"}}`), UIResourceURI: "ui://notes/board"}
		// Core lists an app-only tool to a caller AppToolsAllowed admits, and to a WithAllTools listing.
		if coremcp.AppToolsAllowed(ctx) || ctx.Value(schemas.BifrostContextKeyMCPToolListing) == "all" {
			add("notes-refresh", "Refresh gina's board", false)
			tools[len(tools)-1].MCP = &schemas.MCPToolMetadata{Visibility: []string{schemas.MCPToolVisibilityApp}}
		}
	}
	return tools
}

func (p *perCallerToolManager) ExecuteChatMCPTool(ctx context.Context, call *schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, *schemas.BifrostError) {
	p.mu.Lock()
	p.calls = append(p.calls, *call.Function.Name+" as "+callerOf(ctx))
	p.mu.Unlock()
	text := fmt.Sprintf(`{"ran":%q,"as":%q}`, *call.Function.Name, callerOf(ctx))
	return &schemas.ChatMessage{Content: &schemas.ChatMessageContent{ContentStr: &text}}, nil
}

func (p *perCallerToolManager) reachedBifrost() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.calls)
}

func (p *perCallerToolManager) GetAvailableMCPPrompts(ctx context.Context) []mcp.Prompt {
	if callerOf(ctx) == "alice" {
		return []mcp.Prompt{mcp.NewPrompt("notes-review", mcp.WithPromptDescription("Review alice's notes"))}
	}
	return nil
}

func (p *perCallerToolManager) GetAvailableMCPResources(ctx context.Context) []mcp.Resource {
	switch callerOf(ctx) {
	case "alice":
		return []mcp.Resource{mcp.NewResource("file:///alice.md", "alice's handbook")}
	case "bob":
		return []mcp.Resource{mcp.NewResource("file:///bob.md", "bob's handbook")}
	case "gina":
		uri := "ui://notes/board"
		if coremcp.GatewayAggregate(ctx) {
			uri = coremcp.GatewayUIResourceURI("notes", uri)
		}
		return []mcp.Resource{mcp.NewResource(uri, "gina's board", mcp.WithMIMEType(schemas.MCPAppsMimeType))}
	}
	return nil
}

func (p *perCallerToolManager) GetAvailableMCPResourceTemplates(ctx context.Context) []mcp.ResourceTemplate {
	switch callerOf(ctx) {
	case "erin":
		return []mcp.ResourceTemplate{mcp.NewResourceTemplate("file:///{name}.md", "erin's notes")}
	case "frank":
		return []mcp.ResourceTemplate{mcp.NewResourceTemplate("file:///{+path}", "frank's files")}
	}
	return nil
}

func (p *perCallerToolManager) GetMCPPrompt(ctx context.Context, name string, _ map[string]string) (*mcp.GetPromptResult, *schemas.BifrostError) {
	if callerOf(ctx) != "alice" || name != "notes-review" {
		return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "prompt not found", Error: coremcp.ErrNotFound}}
	}
	return mcp.NewGetPromptResult("review", []mcp.PromptMessage{mcp.NewPromptMessage(mcp.RoleUser, mcp.NewTextContent("review as alice"))}), nil
}

func (p *perCallerToolManager) ReadMCPResource(ctx context.Context, uri string) (*mcp.ReadResourceResult, *schemas.BifrostError) {
	read := &mcp.ReadResourceResult{Contents: []mcp.ResourceContents{mcp.TextResourceContents{URI: uri, Text: "read as " + callerOf(ctx)}}}
	for _, resource := range p.GetAvailableMCPResources(ctx) {
		if resource.URI == uri {
			return read, nil
		}
	}
	for _, template := range p.GetAvailableMCPResourceTemplates(ctx) {
		if template.URITemplate.Regexp().MatchString(uri) {
			return read, nil
		}
	}
	return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "resource not found", Error: coremcp.ErrNotFound}}
}

func (p *perCallerToolManager) CompleteMCPArgument(context.Context, mcp.CompleteParams) (*mcp.CompleteResult, *schemas.BifrostError) {
	return &mcp.CompleteResult{}, nil
}

// perCallerNamesStore answers the names in the callers' discoveries of each client.
type perCallerNamesStore struct {
	configstore.ConfigStore
	mu    sync.Mutex
	names map[string]configstore.MCPPerUserDiscoveryNames
	err   error // answers every read while set
}

func (s *perCallerNamesStore) GetMCPPerUserDiscoveryNames(_ context.Context, mcpClientIDs []string) (map[string]configstore.MCPPerUserDiscoveryNames, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	out := make(map[string]configstore.MCPPerUserDiscoveryNames)
	for _, id := range mcpClientIDs {
		if names, ok := s.names[id]; ok {
			out[id] = names
		}
	}
	return out, nil
}

func (s *perCallerNamesStore) setTools(clientID string, tools ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := s.names[clientID]
	names.Tools = tools
	s.names[clientID] = names
}

func (s *perCallerNamesStore) setTemplates(clientID string, templates ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := s.names[clientID]
	names.ResourceTemplates = templates
	s.names[clientID] = names
}

func (s *perCallerNamesStore) setErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func newPerCallerGatewayHandler(t testing.TB) (*MCPServerHandler, *perCallerToolManager, *perCallerNamesStore) {
	t.Helper()
	store := &perCallerNamesStore{names: map[string]configstore.MCPPerUserDiscoveryNames{
		"notes-id": {Tools: []string{"export", "search"}, Prompts: []string{"review"}, Resources: []string{"file:///alice.md", "file:///bob.md"}},
	}}
	config := &lib.Config{
		ClientConfig: &configstore.ClientConfig{},
		ConfigStore:  store,
		MCPConfig: &schemas.MCPConfig{ClientConfigs: []*schemas.MCPClientConfig{
			{ID: "gw-id", Name: "gw"},
			{ID: "notes-id", Name: "notes", PerUserDiscovery: schemas.Ptr(true)},
		}},
	}
	tm := &perCallerToolManager{}
	h, err := NewMCPServerHandler(context.Background(), config, tm, nil, nil, nil)
	require.NoError(t, err)
	return h, tm, store
}

func callerContext(caller string) *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	if caller != "" {
		ctx.SetValue(schemas.BifrostContextKeyUserID, caller)
	}
	return ctx
}

// gatewayRequest sends one JSON-RPC request through h's server as the caller on ctx and returns its
// result, or its JSON-RPC error.
func gatewayRequest(t testing.TB, h *MCPServerHandler, ctx context.Context, method string, params any) (json.RawMessage, *mcp.JSONRPCErrorDetails) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	require.NoError(t, err)
	raw := h.server().HandleMessage(ctx, body)
	require.NotNil(t, raw)
	encoded, err := json.Marshal(raw)
	require.NoError(t, err)
	var envelope struct {
		Result json.RawMessage          `json:"result"`
		Error  *mcp.JSONRPCErrorDetails `json:"error"`
	}
	require.NoError(t, json.Unmarshal(encoded, &envelope))
	return envelope.Result, envelope.Error
}

func listedTools(t testing.TB, h *MCPServerHandler, ctx context.Context) map[string]mcp.Tool {
	t.Helper()
	raw, rpcErr := gatewayRequest(t, h, ctx, "tools/list", map[string]any{})
	require.Nil(t, rpcErr)
	var result mcp.ListToolsResult
	require.NoError(t, json.Unmarshal(raw, &result))
	tools := make(map[string]mcp.Tool, len(result.Tools))
	for _, tool := range result.Tools {
		tools[tool.Name] = tool
	}
	return tools
}

func toolNamesOf(tools map[string]mcp.Tool) []string {
	names := make([]string, 0, len(tools))
	for name := range tools {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// A client with per_user_discovery on has no tool list of its own, only each caller's. /mcp lists
// every caller exactly their own: their tools with their own descriptions and schemas, or the
// connect tool until they have connected, and nothing of another caller's.
func TestMCPGateway_ListsEachCallerTheirOwnPerCallerTools(t *testing.T) {
	h, _, _ := newPerCallerGatewayHandler(t)

	alice := listedTools(t, h, callerContext("alice"))
	assert.Equal(t, []string{"gw-echo", "notes-search"}, toolNamesOf(alice))
	assert.Equal(t, "Search alice's notes", alice["notes-search"].Description)
	assert.Contains(t, alice["notes-search"].InputSchema.Properties, "alice_query", "alice's own schema")
	assert.Equal(t, "object", alice["notes-search"].OutputSchema.Type, "and her own output schema")

	bob := listedTools(t, h, callerContext("bob"))
	assert.Equal(t, []string{"gw-echo", "notes-export", "notes-search"}, toolNamesOf(bob))
	assert.Equal(t, "Search bob's notes", bob["notes-search"].Description)
	assert.Contains(t, bob["notes-search"].InputSchema.Properties, "bob_query")

	assert.Equal(t, []string{"gw-echo", "notes-connect"}, toolNamesOf(listedTools(t, h, callerContext("carol"))), "a caller with no discovery is offered connect")
	assert.Equal(t, []string{"gw-echo"}, toolNamesOf(listedTools(t, h, callerContext(""))), "a request naming no caller sees no per-caller tool")

	narrowed := callerContext("alice")
	narrowed.SetValue(schemas.MCPContextKeyIncludeTools, []string{"gw-echo"})
	assert.Equal(t, []string{"gw-echo"}, toolNamesOf(listedTools(t, h, narrowed)), "a narrowed request stays narrowed")
}

// A per-caller tool runs as the caller who calls it, its result shaped by that caller's own tool.
func TestMCPGateway_RunsPerCallerToolsAsTheirCaller(t *testing.T) {
	h, tm, _ := newPerCallerGatewayHandler(t)

	raw, rpcErr := gatewayRequest(t, h, callerContext("alice"), "tools/call", map[string]any{"name": "notes-search", "arguments": map[string]any{"alice_query": "x"}})
	require.Nil(t, rpcErr)
	var result mcp.CallToolResult
	require.NoError(t, json.Unmarshal(raw, &result))
	assert.False(t, result.IsError)
	assert.Equal(t, map[string]any{"ran": "notes-search", "as": "alice"}, result.StructuredContent, "alice's tool declares an output schema")

	_, rpcErr = gatewayRequest(t, h, callerContext("carol"), "tools/call", map[string]any{"name": "notes-connect", "arguments": map[string]any{}})
	require.Nil(t, rpcErr)
	assert.Equal(t, []string{"notes-search as alice", "notes-connect as carol"}, tm.reachedBifrost())
}

// A per-caller tool name another caller holds answers exactly like a name nobody holds, without
// reaching Bifrost, so calling names cannot tell anyone what another caller's account has.
func TestMCPGateway_APerCallerToolTheCallerLacksReadsAsUnknown(t *testing.T) {
	h, tm, _ := newPerCallerGatewayHandler(t)

	_, held := gatewayRequest(t, h, callerContext("carol"), "tools/call", map[string]any{"name": "notes-export", "arguments": map[string]any{}})
	_, unknown := gatewayRequest(t, h, callerContext("carol"), "tools/call", map[string]any{"name": "notes-nothing", "arguments": map[string]any{}})
	require.NotNil(t, held)
	require.NotNil(t, unknown)
	assert.Equal(t, unknown.Code, held.Code)
	assert.Equal(t, strings.ReplaceAll(unknown.Message, "notes-nothing", "notes-export"), held.Message)
	assert.Empty(t, tm.reachedBifrost())
}

// A caller who has connected no longer lists the connect tool, but a client that kept its list from
// before signing in may still call it. That call reaches Bifrost, which answers it as it does on
// every other path, rather than reading as an unknown name. Only the client's own connect name is
// answered this way: an upstream tool another caller holds whose name merely ends in -connect still
// reads as unknown.
func TestMCPGateway_ACallerWhoHasConnectedCanStillCallConnect(t *testing.T) {
	h, tm, store := newPerCallerGatewayHandler(t)
	assert.NotContains(t, toolNamesOf(listedTools(t, h, callerContext("alice"))), "notes-connect", "alice has connected")

	_, rpcErr := gatewayRequest(t, h, callerContext("alice"), "tools/call", map[string]any{"name": "notes-connect", "arguments": map[string]any{}})
	require.Nil(t, rpcErr)
	assert.Equal(t, []string{"notes-connect as alice"}, tm.reachedBifrost())

	store.setTools("notes-id", "export", "search", "sync-connect")
	require.NoError(t, h.ResyncPerCallerEntries(context.Background()))
	_, held := gatewayRequest(t, h, callerContext("alice"), "tools/call", map[string]any{"name": "notes-sync-connect", "arguments": map[string]any{}})
	_, unknown := gatewayRequest(t, h, callerContext("alice"), "tools/call", map[string]any{"name": "notes-nothing", "arguments": map[string]any{}})
	require.NotNil(t, held)
	require.NotNil(t, unknown)
	assert.Equal(t, unknown.Code, held.Code)
	assert.Equal(t, strings.ReplaceAll(unknown.Message, "notes-nothing", "notes-sync-connect"), held.Message)
	assert.Equal(t, []string{"notes-connect as alice"}, tm.reachedBifrost(), "the -connect upstream name never reached Bifrost")
}

// Prompts and resources follow the same rules: each caller lists and reads their own, and one
// another caller holds answers like one nobody holds.
func TestMCPGateway_ServesPerCallerPromptsAndResourcesToTheirOwnCallers(t *testing.T) {
	h, _, _ := newPerCallerGatewayHandler(t)

	raw, rpcErr := gatewayRequest(t, h, callerContext("alice"), "prompts/list", map[string]any{})
	require.Nil(t, rpcErr)
	var prompts mcp.ListPromptsResult
	require.NoError(t, json.Unmarshal(raw, &prompts))
	require.Len(t, prompts.Prompts, 1)
	assert.Equal(t, "Review alice's notes", prompts.Prompts[0].Description)

	raw, rpcErr = gatewayRequest(t, h, callerContext("alice"), "resources/list", map[string]any{})
	require.Nil(t, rpcErr)
	var resources mcp.ListResourcesResult
	require.NoError(t, json.Unmarshal(raw, &resources))
	require.Len(t, resources.Resources, 1)
	assert.Equal(t, "file:///alice.md", resources.Resources[0].URI)
	assert.Equal(t, "alice's handbook", resources.Resources[0].Name)

	raw, rpcErr = gatewayRequest(t, h, callerContext("alice"), "resources/read", map[string]any{"uri": "file:///alice.md"})
	require.Nil(t, rpcErr)
	assert.Contains(t, string(raw), "read as alice")
	_, rpcErr = gatewayRequest(t, h, callerContext("alice"), "prompts/get", map[string]any{"name": "notes-review"})
	require.Nil(t, rpcErr)

	_, held := gatewayRequest(t, h, callerContext("carol"), "resources/read", map[string]any{"uri": "file:///bob.md"})
	_, unknown := gatewayRequest(t, h, callerContext("carol"), "resources/read", map[string]any{"uri": "file:///nothing.md"})
	require.NotNil(t, held)
	require.NotNil(t, unknown)
	assert.Equal(t, unknown.Code, held.Code)
	assert.Equal(t, strings.ReplaceAll(unknown.Message, "file:///nothing.md", "file:///bob.md"), held.Message)

	_, held = gatewayRequest(t, h, callerContext("carol"), "prompts/get", map[string]any{"name": "notes-review"})
	_, unknown = gatewayRequest(t, h, callerContext("carol"), "prompts/get", map[string]any{"name": "notes-nothing"})
	require.NotNil(t, held)
	require.NotNil(t, unknown)
	assert.Equal(t, unknown.Code, held.Code)
	assert.Equal(t, strings.ReplaceAll(unknown.Message, "notes-nothing", "notes-review"), held.Message)
}

// A caller reads a URI through any of their own resources and templates, whichever placeholder the
// read lands on: mcp-go hands a read to an exact placeholder before any template, and to one of
// several templates that match, so that placeholder can be another caller's entry. A URI none of
// the caller's own entries covers still reads exactly like one nobody holds.
func TestMCPGateway_ACallerReadsThroughTheirOwnTemplateWhicheverPlaceholderTheReadLandsOn(t *testing.T) {
	h, _, store := newPerCallerGatewayHandler(t)
	store.setTemplates("notes-id", "file:///{name}.md", "file:///{+path}")
	require.NoError(t, h.ResyncPerCallerEntries(context.Background()))

	raw, rpcErr := gatewayRequest(t, h, callerContext("erin"), "resources/read", map[string]any{"uri": "file:///bob.md"})
	assert.Nil(t, rpcErr, "bob holds file:///bob.md exactly; erin reads it through her own template")
	assert.Contains(t, string(raw), "read as erin")
	for _, caller := range []string{"erin", "frank"} {
		raw, rpcErr = gatewayRequest(t, h, callerContext(caller), "resources/read", map[string]any{"uri": "file:///x.md"})
		assert.Nil(t, rpcErr, "%s reads through their own template, whichever of the two templates the read lands on", caller)
		assert.Contains(t, string(raw), "read as "+caller)
	}

	_, held := gatewayRequest(t, h, callerContext("carol"), "resources/read", map[string]any{"uri": "file:///x.md"})
	_, unknown := gatewayRequest(t, h, callerContext("carol"), "resources/read", map[string]any{"uri": "other:///x"})
	require.NotNil(t, held)
	require.NotNil(t, unknown)
	assert.Equal(t, unknown.Code, held.Code)
	assert.Equal(t, strings.ReplaceAll(unknown.Message, "other:///x", "file:///x.md"), held.Message, "carol holds neither template")
}

// A name a caller's discovery brings after the last sync is registered by a resync, so the caller
// can list and call it. A resync that finds no name added or dropped leaves the server as it is.
func TestMCPGateway_ResyncRegistersPerCallerNamesWrittenSinceTheLastSync(t *testing.T) {
	h, tm, store := newPerCallerGatewayHandler(t)
	installed := h.perCaller.Load()
	require.NoError(t, h.ResyncPerCallerEntries(context.Background()))
	assert.Same(t, installed, h.perCaller.Load(), "nothing changed, so nothing is replaced")

	tm.bobArchives.Store(true)
	store.setTools("notes-id", "archive", "export", "search")
	assert.NotContains(t, toolNamesOf(listedTools(t, h, callerContext("bob"))), "notes-archive", "not registered before the resync")

	require.NoError(t, h.ResyncPerCallerEntries(context.Background()))
	assert.Contains(t, toolNamesOf(listedTools(t, h, callerContext("bob"))), "notes-archive")
	_, rpcErr := gatewayRequest(t, h, callerContext("bob"), "tools/call", map[string]any{"name": "notes-archive", "arguments": map[string]any{}})
	require.Nil(t, rpcErr)
	assert.Contains(t, tm.reachedBifrost(), "notes-archive as bob")
}

// A sign-in or revoke whose names other callers already hold adds and drops no placeholder, yet it
// changes what that caller lists. So every resync tells listening clients that tools, prompts and
// resources changed, on every protocol revision, even when the placeholders stay as they are.
func TestMCPGateway_AResyncTellsListenersEvenWhenNoPlaceholderChanged(t *testing.T) {
	h, _, _ := newPerCallerGatewayHandler(t)
	baseURL := startGateway(t, h)
	installed := h.perCaller.Load()
	for _, version := range []string{mcp.ProtocolVersion20251125, mcp.ProtocolVersion20260728} {
		t.Run(version, func(t *testing.T) {
			ctx := context.Background()
			var opts []transport.StreamableHTTPCOption
			if !mcp.IsModernProtocol(version) {
				opts = append(opts, transport.WithContinuousListening())
			}
			tr, err := transport.NewStreamableHTTP(baseURL+"/mcp", opts...)
			require.NoError(t, err)
			c := client.NewClient(tr, client.WithProtocolVersion(version))
			var tools, prompts, resources atomic.Bool
			c.OnNotification(func(n mcp.JSONRPCNotification) {
				switch n.Method {
				case string(mcp.MethodNotificationToolsListChanged):
					tools.Store(true)
				case string(mcp.MethodNotificationPromptsListChanged):
					prompts.Store(true)
				case string(mcp.MethodNotificationResourcesListChanged):
					resources.Store(true)
				}
			})
			require.NoError(t, c.Start(ctx))
			t.Cleanup(func() { _ = c.Close() })
			init := mcp.InitializeRequest{}
			init.Params.ProtocolVersion = version
			_, err = c.Initialize(ctx, init)
			require.NoError(t, err)
			if mcp.IsModernProtocol(version) {
				stop, err := c.ListenAsync(ctx, mcp.SubscriptionFilter{ToolsListChanged: true, PromptsListChanged: true, ResourcesListChanged: true}, nil)
				require.NoError(t, err)
				t.Cleanup(stop)
			}

			// The stream opens asynchronously, so keep resyncing until the notifications land.
			assert.Eventually(t, func() bool {
				return assert.NoError(t, h.ResyncPerCallerEntries(ctx)) &&
					tools.Load() && prompts.Load() && resources.Load()
			}, 5*time.Second, 50*time.Millisecond, "tools, prompts and resources list_changed")
			assert.Same(t, installed, h.perCaller.Load(), "no placeholder changed, so none is replaced")
		})
	}
}

// A failed read of the discovery names keeps the placeholders already installed: a resync reports
// the error and replaces nothing, and a full sync keeps the names it knew, so connected callers keep
// their tools until a read succeeds.
func TestMCPGateway_AFailedNamesReadKeepsTheInstalledPlaceholders(t *testing.T) {
	h, tm, store := newPerCallerGatewayHandler(t)
	installed := h.perCaller.Load()
	store.setErr(errors.New("database is locked"))

	require.Error(t, h.ResyncPerCallerEntries(context.Background()))
	assert.Same(t, installed, h.perCaller.Load(), "a failed read replaces nothing")
	assert.Contains(t, toolNamesOf(listedTools(t, h, callerContext("bob"))), "notes-export")

	require.NoError(t, h.SyncMCPServer(context.Background()))
	assert.Contains(t, toolNamesOf(listedTools(t, h, callerContext("bob"))), "notes-export", "a sync during the failure keeps the names it knew")
	_, rpcErr := gatewayRequest(t, h, callerContext("bob"), "tools/call", map[string]any{"name": "notes-export", "arguments": map[string]any{}})
	require.Nil(t, rpcErr)
	assert.Contains(t, tm.reachedBifrost(), "notes-export as bob")
}

// A code-mode client with per_user_discovery on is reached through the code-mode tools, which the
// shared list never holds while no caller is known. They are registered as placeholders too, so a
// connected caller lists and calls them, and a caller who has not connected is offered connect only.
func TestMCPGateway_ServesTheCodeModeToolsOfAPerCallerClient(t *testing.T) {
	h, tm, _ := newPerCallerGatewayHandler(t)
	h.config.MCPConfig.ClientConfigs[1].IsCodeModeClient = true
	require.NoError(t, h.SyncMCPServer(context.Background()))

	assert.Contains(t, toolNamesOf(listedTools(t, h, callerContext("dave"))), "executeToolCode")
	_, rpcErr := gatewayRequest(t, h, callerContext("dave"), "tools/call", map[string]any{"name": "executeToolCode", "arguments": map[string]any{}})
	require.Nil(t, rpcErr)
	assert.Contains(t, tm.reachedBifrost(), "executeToolCode as dave")
	assert.NotContains(t, toolNamesOf(listedTools(t, h, callerContext("carol"))), "executeToolCode")
}

// BenchmarkMCPGatewayToolsList measures tools/list through the shared server: the common case (no
// include list, no client with per_user_discovery on), a narrowed request, and a caller of a client
// with per_user_discovery on.
func BenchmarkMCPGatewayToolsList(b *testing.B) {
	list := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	run := func(b *testing.B, h *MCPServerHandler, ctx context.Context) {
		b.ReportAllocs()
		for b.Loop() {
			_ = h.server().HandleMessage(ctx, list)
		}
	}
	b.Run("shared/unrestricted", func(b *testing.B) {
		run(b, newGatewayHandler(b), callerContext("alice"))
	})
	b.Run("shared/narrowed", func(b *testing.B) {
		ctx := callerContext("alice")
		ctx.SetValue(schemas.MCPContextKeyIncludeTools, []string{"gw-echo"})
		run(b, newGatewayHandler(b), ctx)
	})
	b.Run("per-caller", func(b *testing.B) {
		h, _, _ := newPerCallerGatewayHandler(b)
		run(b, h, callerContext("alice"))
	})
}

// coreGatewayManager serves the gateway from a real core MCP manager, so the gateway runs core's rules.
type coreGatewayManager struct{ m *coremcp.MCPManager }

func bctx(ctx context.Context) *schemas.BifrostContext {
	return schemas.NewBifrostContext(ctx, schemas.NoDeadline)
}

func (c coreGatewayManager) GetAvailableMCPTools(ctx context.Context) []schemas.ChatTool {
	return c.m.GetAvailableTools(bctx(ctx))
}
func (c coreGatewayManager) GetMCPServerInstructions(ctx context.Context) string {
	return c.m.GetAggregatedServerInstructions(ctx)
}
func (c coreGatewayManager) ExecuteChatMCPTool(ctx context.Context, call *schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, *schemas.BifrostError) {
	return c.m.ExecuteChatTool(bctx(ctx), call)
}
func (c coreGatewayManager) ExecuteResponsesMCPTool(context.Context, *schemas.ResponsesToolMessage) (*schemas.ResponsesMessage, *schemas.BifrostError) {
	return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "not used"}}
}
func (c coreGatewayManager) GetAvailableMCPPrompts(ctx context.Context) []mcp.Prompt {
	return c.m.GetAvailablePrompts(ctx)
}
func (c coreGatewayManager) GetAvailableMCPResources(ctx context.Context) []mcp.Resource {
	return c.m.GetAvailableResources(ctx)
}
func (c coreGatewayManager) GetAvailableMCPResourceTemplates(ctx context.Context) []mcp.ResourceTemplate {
	return c.m.GetAvailableResourceTemplates(ctx)
}
func (c coreGatewayManager) GetMCPPrompt(ctx context.Context, name string, arguments map[string]string) (*mcp.GetPromptResult, *schemas.BifrostError) {
	return c.m.GetPrompt(bctx(ctx), name, arguments)
}
func (c coreGatewayManager) ReadMCPResource(ctx context.Context, uri string) (*mcp.ReadResourceResult, *schemas.BifrostError) {
	return c.m.ReadResource(bctx(ctx), uri)
}
func (c coreGatewayManager) CompleteMCPArgument(ctx context.Context, params mcp.CompleteParams) (*mcp.CompleteResult, *schemas.BifrostError) {
	return c.m.Complete(bctx(ctx), params)
}

// newAppsGateway serves an upstream with MCP Apps tools (view, app-only refresh, item via a ui:// template)
// through a real core MCP manager, on /mcp, the client slug "apps" and the Virtual MCP slug "bundle".
func newAppsGateway(tb testing.TB) *MCPServerHandler {
	tb.Helper()
	upstream := server.NewMCPServer("apps-upstream", "1.0.0", server.WithToolCapabilities(true), server.WithResourceCapabilities(false, false))
	view := mcp.NewTool("view")
	view.Meta = &mcp.Meta{AdditionalFields: map[string]any{"ui": map[string]any{"resourceUri": "ui://dash/view"}}}
	refresh := mcp.NewTool("refresh")
	refresh.Meta = &mcp.Meta{AdditionalFields: map[string]any{"ui": map[string]any{"resourceUri": "ui://dash/view", "visibility": []any{"app"}}}}
	ok := func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("ok"), nil
	}
	item := mcp.NewTool("item")
	item.Meta = &mcp.Meta{AdditionalFields: map[string]any{"ui": map[string]any{"resourceUri": "ui://dash/items/7"}, "openai/outputTemplate": "ui://dash/items/7"}}
	upstream.AddTool(view, ok)
	upstream.AddTool(refresh, ok)
	upstream.AddTool(item, ok)
	upstream.AddResourceTemplate(mcp.NewResourceTemplate("ui://dash/items/{id}", "items", mcp.WithTemplateMIMEType(schemas.MCPAppsMimeType)),
		func(_ context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
			return []mcp.ResourceContents{mcp.TextResourceContents{URI: req.Params.URI, MIMEType: schemas.MCPAppsMimeType, Text: "<html>item</html>"}}, nil
		})
	upstream.AddResource(mcp.NewResource("ui://dash/view", "view", mcp.WithMIMEType(schemas.MCPAppsMimeType)),
		func(_ context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
			return []mcp.ResourceContents{mcp.TextResourceContents{URI: req.Params.URI, MIMEType: schemas.MCPAppsMimeType, Text: "<html></html>"}}, nil
		})
	ts := httptest.NewServer(server.NewStreamableHTTPServer(upstream))
	tb.Cleanup(ts.Close)

	ctx := context.Background()
	m := coremcp.NewMCPManager(ctx, schemas.MCPConfig{}, nil, bifrost.NewDefaultLogger(schemas.LogLevelError), nil)
	tb.Cleanup(func() { _ = m.Cleanup() })
	require.NoError(tb, m.AddClient(ctx, &schemas.MCPClientConfig{
		ID: "apps", Name: "apps", EndpointSlug: "apps", ConnectionType: schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar(ts.URL), ToolsToExecute: []string{"*"},
	}))
	cfg := &lib.Config{ClientConfig: &configstore.ClientConfig{}, MCPConfig: &schemas.MCPConfig{ClientConfigs: []*schemas.MCPClientConfig{
		{ID: "apps", Name: "apps", EndpointSlug: "apps"},
	}}}
	admitter := slugAdmitter{vmcps: map[string][]string{"bundle": {"apps-view", "apps-refresh", "apps-item"}}, clients: map[string][]string{"apps": {"apps-*"}}}
	h, err := NewMCPServerHandler(ctx, cfg, coreGatewayManager{m}, admitter, nil, nil)
	require.NoError(tb, err)
	return h
}

// MCP Apps on the gateway: /mcp/<slug> for one server serves its tools' _meta and ui:// URIs as the
// server sent them, and app-only tools to callers that declared MCP Apps; aggregate /mcp and Virtual
// MCP slugs never serve app-only tools and namespace ui:// URIs (resources and templates) by client.
func TestMCPGateway_MCPApps(t *testing.T) {
	ctx := context.Background()
	h := newAppsGateway(t)
	baseURL := startGateway(t, h)

	hostsApps := mcp.ClientCapabilities{Extensions: map[string]any{schemas.MCPAppsExtension: map[string]any{"mimeTypes": []any{schemas.MCPAppsMimeType}}}}
	cases := []struct {
		name, path, version string
		caps                mcp.ClientCapabilities
		appOnly             bool
		prefix              string // what ui:// URIs start with on this endpoint
	}{
		{"slug, apps caller", "/mcp/apps", mcp.ProtocolVersion20260728, hostsApps, true, "ui://"},
		{"slug, caller without apps", "/mcp/apps", mcp.ProtocolVersion20260728, mcp.ClientCapabilities{}, false, "ui://"},
		{"slug, legacy apps caller", "/mcp/apps", mcp.ProtocolVersion20251125, hostsApps, false, "ui://"},
		{"aggregate, apps caller", "/mcp", mcp.ProtocolVersion20260728, hostsApps, false, "ui://apps/"},
		{"aggregate, legacy caller", "/mcp", mcp.ProtocolVersion20251125, mcp.ClientCapabilities{}, false, "ui://apps/"},
		{"virtual mcp, apps caller", "/mcp/bundle", mcp.ProtocolVersion20260728, hostsApps, false, "ui://apps/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr, err := transport.NewStreamableHTTP(baseURL + tc.path)
			require.NoError(t, err)
			c := client.NewClient(tr, client.WithProtocolVersion(tc.version), client.WithClientCapabilities(tc.caps))
			require.NoError(t, c.Start(ctx))
			t.Cleanup(func() { _ = c.Close() })
			init := mcp.InitializeRequest{}
			init.Params.ProtocolVersion = tc.version
			initResult, err := c.Initialize(ctx, init)
			require.NoError(t, err)
			assert.Contains(t, initResult.Capabilities.Extensions, schemas.MCPAppsExtension, "the gateway declares MCP Apps")

			tools, err := c.ListTools(ctx, mcp.ListToolsRequest{})
			require.NoError(t, err)
			byName := map[string]mcp.Tool{}
			for _, tool := range tools.Tools {
				byName[tool.Name] = tool
			}
			_, listed := byName["apps-refresh"]
			assert.Equal(t, tc.appOnly, listed, "app-only tool listing")
			viewURI, itemURI := tc.prefix+"dash/view", tc.prefix+"dash/items/7"
			viewTool, found := byName["apps-view"]
			require.True(t, found)
			require.NotNil(t, viewTool.Meta)
			ui, _ := viewTool.Meta.AdditionalFields["ui"].(map[string]any)
			assert.Equal(t, viewURI, ui["resourceUri"], "the tool's ui:// resource")
			itemTool, found := byName["apps-item"]
			require.True(t, found)
			require.NotNil(t, itemTool.Meta)
			itemUI, _ := itemTool.Meta.AdditionalFields["ui"].(map[string]any)
			assert.Equal(t, itemURI, itemUI["resourceUri"])
			assert.Equal(t, itemURI, itemTool.Meta.AdditionalFields["openai/outputTemplate"], "the OpenAI Apps SDK template")

			call := mcp.CallToolRequest{}
			call.Params.Name = "apps-refresh"
			result, err := c.CallTool(ctx, call)
			require.NoError(t, err)
			assert.Equal(t, !tc.appOnly, result.IsError, "app-only tool call")

			resources, err := c.ListResources(ctx, mcp.ListResourcesRequest{})
			require.NoError(t, err)
			var uris []string
			for _, r := range resources.Resources {
				uris = append(uris, r.URI)
			}
			assert.Equal(t, []string{viewURI}, uris, "the ui:// resource is listed under the URI the tool names")
			templates, err := c.ListResourceTemplates(ctx, mcp.ListResourceTemplatesRequest{})
			require.NoError(t, err)
			var templateURIs []string
			for _, template := range templates.ResourceTemplates {
				templateURIs = append(templateURIs, coremcp.ResourceTemplateURI(template))
			}
			assert.Equal(t, []string{tc.prefix + "dash/items/{id}"}, templateURIs)

			for _, uri := range []string{viewURI, itemURI} {
				read := mcp.ReadResourceRequest{}
				read.Params.URI = uri
				contents, err := c.ReadResource(ctx, read)
				require.NoError(t, err, uri)
				require.Len(t, contents.Contents, 1)
				assert.Equal(t, uri, contents.Contents[0].(mcp.TextResourceContents).URI)
			}
		})
	}

	t.Run("tools-only gateway does not declare MCP Apps", func(t *testing.T) {
		toolsOnly := newGatewayHandler(t)
		tr, err := transport.NewStreamableHTTP(startGateway(t, toolsOnly) + "/mcp")
		require.NoError(t, err)
		c := client.NewClient(tr)
		require.NoError(t, c.Start(ctx))
		t.Cleanup(func() { _ = c.Close() })
		result, err := c.Initialize(ctx, mcp.InitializeRequest{})
		require.NoError(t, err)
		assert.NotContains(t, result.Capabilities.Extensions, schemas.MCPAppsExtension, "it cannot serve ui:// resources")
	})
}

// MCP Apps for a client with per_user_discovery on: a caller's own tools carry their _meta, namespaced
// on /mcp; an app-only tool is listed only on the client's slug to a caller that declared MCP Apps; and
// the caller's ui:// resource is listed and read under the URI its endpoint uses.
func TestMCPGateway_MCPAppsForPerCallerClients(t *testing.T) {
	h, _, store := newPerCallerGatewayHandler(t)
	store.mu.Lock()
	names := store.names["notes-id"]
	names.Tools = append(names.Tools, "board", "refresh")
	names.Resources = append(names.Resources, "ui://notes/board")
	store.names["notes-id"] = names
	store.mu.Unlock()
	require.NoError(t, h.ResyncPerCallerEntries(context.Background()))

	hostsApps := &mcp.ClientCapabilities{Extensions: map[string]any{schemas.MCPAppsExtension: map[string]any{}}}
	// gateway is gina's request on /mcp/<slug>, or on /mcp when slug is "".
	gateway := func(slug string, caps *mcp.ClientCapabilities) *schemas.BifrostContext {
		ctx := callerContext("gina")
		ctx.SetValue(schemas.BifrostContextKeyIsMCPGateway, true)
		if slug != "" {
			ctx.SetValue(schemas.BifrostContextKeyMCPGatewayClient, slug)
			ctx.SetValue(schemas.MCPContextKeyIncludeTools, []string{slug + "-*"})
		}
		if caps != nil {
			ctx.SetValue(schemas.BifrostContextKeyMCPCallerCapabilities, caps)
		}
		return ctx
	}
	resourceURIOf := func(tool mcp.Tool) any {
		require.NotNil(t, tool.Meta, tool.Name)
		ui, _ := tool.Meta.AdditionalFields["ui"].(map[string]any)
		return ui["resourceUri"]
	}
	listedResources := func(ctx context.Context) []string {
		raw, rpcErr := gatewayRequest(t, h, ctx, "resources/list", map[string]any{})
		require.Nil(t, rpcErr)
		var result mcp.ListResourcesResult
		require.NoError(t, json.Unmarshal(raw, &result))
		var uris []string
		for _, r := range result.Resources {
			uris = append(uris, r.URI)
		}
		return uris
	}
	read := func(ctx context.Context, uri string) {
		raw, rpcErr := gatewayRequest(t, h, ctx, "resources/read", map[string]any{"uri": uri})
		require.Nil(t, rpcErr, uri)
		assert.Contains(t, string(raw), uri)
	}

	slug := listedTools(t, h, gateway("notes", hostsApps))
	assert.Equal(t, "ui://notes/board", resourceURIOf(slug["notes-board"]), "a slug lists the server's own URI")
	assert.Contains(t, slug, "notes-refresh", "a slug lists the app-only tool to a caller that declared MCP Apps")
	assert.NotContains(t, listedTools(t, h, gateway("notes", nil)), "notes-refresh", "not to one that did not")
	assert.Equal(t, []string{"ui://notes/board"}, listedResources(gateway("notes", hostsApps)))
	read(gateway("notes", hostsApps), "ui://notes/board")

	aggregate := listedTools(t, h, gateway("", hostsApps))
	assert.Equal(t, "ui://notes/notes/board", resourceURIOf(aggregate["notes-board"]), "/mcp lists the namespaced URI")
	assert.NotContains(t, aggregate, "notes-refresh", "/mcp never lists an app-only tool")
	assert.Equal(t, []string{"ui://notes/notes/board"}, listedResources(gateway("", hostsApps)))
	read(gateway("", hostsApps), "ui://notes/notes/board")
}

// recordingGatewayManager records the tool names the gateway executes.
type recordingGatewayManager struct {
	coreGatewayManager
	mu    sync.Mutex
	names []string
}

func (r *recordingGatewayManager) ExecuteChatMCPTool(ctx context.Context, call *schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, *schemas.BifrostError) {
	r.mu.Lock()
	r.names = append(r.names, *call.Function.Name)
	r.mu.Unlock()
	return r.coreGatewayManager.ExecuteChatMCPTool(ctx, call)
}

// startNamedUpstream serves tools that answer "<server>:<tool>", so a test can tell which server ran.
func startNamedUpstream(tb testing.TB, serverName string, tools ...mcp.Tool) string {
	tb.Helper()
	upstream := server.NewMCPServer(serverName, "1.0.0", server.WithToolCapabilities(true))
	for _, tool := range tools {
		answer := serverName + ":" + tool.Name
		upstream.AddTool(tool, func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText(answer), nil
		})
	}
	ts := httptest.NewServer(server.NewStreamableHTTPServer(upstream))
	tb.Cleanup(ts.Close)
	return ts.URL
}

// MCP Apps call tools by the names their own server gave them. /mcp/<slug> resolves such a name to
// that server's tool; /mcp and Virtual MCPs resolve it only when exactly one reachable tool that
// models and apps may both call has it. Exact (prefixed) names always win, aliases are never listed,
// and Bifrost runs the call under the prefixed name.
func TestMCPGateway_ServerGivenToolNames(t *testing.T) {
	ctx := context.Background()
	refresh := mcp.NewTool("refresh")
	refresh.Meta = &mcp.Meta{AdditionalFields: map[string]any{"ui": map[string]any{"visibility": []any{"app"}}}}
	appsURL := startNamedUpstream(t, "apps", mcp.NewTool("view"), refresh, mcp.NewTool("status"))
	// other's "apps-view" is installed as other-apps-view; the name apps-view stays apps's view.
	otherURL := startNamedUpstream(t, "other", mcp.NewTool("status"), mcp.NewTool("apps-view"))

	m := coremcp.NewMCPManager(ctx, schemas.MCPConfig{}, nil, bifrost.NewDefaultLogger(schemas.LogLevelError), nil)
	t.Cleanup(func() { _ = m.Cleanup() })
	for name, url := range map[string]string{"apps": appsURL, "other": otherURL} {
		require.NoError(t, m.AddClient(ctx, &schemas.MCPClientConfig{
			ID: name, Name: name, EndpointSlug: name, ConnectionType: schemas.MCPConnectionTypeHTTP,
			ConnectionString: schemas.NewSecretVar(url), ToolsToExecute: []string{"*"},
		}))
	}
	cfg := &lib.Config{ClientConfig: &configstore.ClientConfig{}, MCPConfig: &schemas.MCPConfig{ClientConfigs: []*schemas.MCPClientConfig{
		{ID: "apps", Name: "apps", EndpointSlug: "apps"}, {ID: "other", Name: "other", EndpointSlug: "other"},
	}}}
	admitter := slugAdmitter{
		vmcps:   map[string][]string{"bundle": {"apps-status", "apps-view"}, "withrefresh": {"apps-view", "apps-refresh"}},
		clients: map[string][]string{"apps": {"apps-*"}, "other": {"other-*"}},
	}
	tm := &recordingGatewayManager{coreGatewayManager: coreGatewayManager{m}}
	h, err := NewMCPServerHandler(ctx, cfg, tm, admitter, nil, nil)
	require.NoError(t, err)
	baseURL := startGateway(t, h)

	hostsApps := mcp.ClientCapabilities{Extensions: map[string]any{schemas.MCPAppsExtension: map[string]any{}}}
	connect := func(t *testing.T, path string) *client.Client {
		tr, err := transport.NewStreamableHTTP(baseURL + path)
		require.NoError(t, err)
		c := client.NewClient(tr, client.WithProtocolVersion(mcp.ProtocolVersion20260728), client.WithClientCapabilities(hostsApps))
		require.NoError(t, c.Start(ctx))
		t.Cleanup(func() { _ = c.Close() })
		init := mcp.InitializeRequest{}
		init.Params.ProtocolVersion = mcp.ProtocolVersion20260728
		_, err = c.Initialize(ctx, init)
		require.NoError(t, err)
		return c
	}
	call := func(t *testing.T, c *client.Client, name string) *mcp.CallToolResult {
		req := mcp.CallToolRequest{}
		req.Params.Name = name
		result, err := c.CallTool(ctx, req)
		require.NoError(t, err, name)
		return result
	}

	// A name the caller cannot reach fails exactly like a name no server has, so it reveals nothing.
	const notFound = "not found"
	cases := []struct {
		name, path, tool string
		want             string // the answering server and tool, or the refusal text
		refused          bool
		runs             string // the prefixed name Bifrost executes
	}{
		{"slug resolves its server's tool", "/mcp/apps", "status", "apps:status", false, "apps-status"},
		{"slug resolves an app-only tool for an apps caller", "/mcp/apps", "refresh", "apps:refresh", false, "apps-refresh"},
		{"other slug resolves its own tool", "/mcp/other", "status", "other:status", false, "other-status"},
		{"aggregate resolves a unique name", "/mcp", "view", "apps:view", false, "apps-view"},
		{"aggregate refuses an ambiguous name", "/mcp", "status", "call one of apps-status, other-status", true, ""},
		{"aggregate refuses an app-only tool", "/mcp", "refresh", "only callable from an MCP App", true, ""},
		{"aggregate refuses an unknown name", "/mcp", "nothing", notFound, true, ""},
		{"a slug cannot see another server's tool", "/mcp/other", "refresh", notFound, true, ""},
		{"a virtual mcp cannot see a tool outside it", "/mcp/bundle", "refresh", notFound, true, ""},
		{"a virtual mcp refuses its app-only tool as app-only, as /mcp does", "/mcp/withrefresh", "refresh", "only callable from an MCP App", true, ""},
		{"virtual mcp resolves among its own tools", "/mcp/bundle", "status", "apps:status", false, "apps-status"},
		{"exact name wins over another server's same-named tool", "/mcp", "apps-view", "apps:view", false, "apps-view"},
		{"another server's tool named like a prefix keeps its full name", "/mcp", "other-apps-view", "other:apps-view", false, "other-apps-view"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := connect(t, tc.path)
			tm.mu.Lock()
			tm.names = nil
			tm.mu.Unlock()
			if tc.want == notFound {
				req := mcp.CallToolRequest{}
				req.Params.Name = tc.tool
				_, err := c.CallTool(ctx, req)
				require.Error(t, err)
				req.Params.Name = "nope"
				_, unknown := c.CallTool(ctx, req)
				require.Error(t, unknown)
				assert.Equal(t, strings.ReplaceAll(unknown.Error(), "nope", tc.tool), err.Error(), "same answer as a name no server has")
				tm.mu.Lock()
				defer tm.mu.Unlock()
				assert.Empty(t, tm.names)
				return
			}
			result := call(t, c, tc.tool)
			text := resultText(result)
			assert.Equal(t, tc.refused, result.IsError, text)
			assert.Contains(t, text, tc.want)
			tm.mu.Lock()
			defer tm.mu.Unlock()
			if tc.runs == "" {
				assert.Empty(t, tm.names, "a refused alias never reaches Bifrost's executor")
			} else {
				assert.Equal(t, []string{tc.runs}, tm.names, "plugins and logs see the prefixed name")
			}
		})
	}

	t.Run("aliases are never listed", func(t *testing.T) {
		for _, path := range []string{"/mcp", "/mcp/apps", "/mcp/bundle"} {
			tools, err := connect(t, path).ListTools(ctx, mcp.ListToolsRequest{})
			require.NoError(t, err)
			for _, tool := range tools.Tools {
				assert.NotEqual(t, "", coremcp.ToolClientName(tool.Name), "%s lists %q", path, tool.Name)
			}
		}
	})
}

func resultText(result *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, content := range result.Content {
		if text, ok := content.(mcp.TextContent); ok {
			sb.WriteString(text.Text)
		}
	}
	return sb.String()
}

// A refusal tool whose name became resolvable after the call-time hook ran (a sync in between) answers
// not found, never an empty refusal.
func TestRefusalTool_AnswersNotFoundOnceTheNameResolves(t *testing.T) {
	h := &MCPServerHandler{}
	shared := &gatewayToolIndex{aliases: map[string][]gatewayToolAlias{
		"status": {{name: "apps-status", client: "apps"}, {name: "other-status", client: "other"}},
	}}
	tools := h.refusalTools(shared, nil)
	require.Len(t, tools, 1)
	h.toolIndex.Store(shared)
	result, err := tools[0].Handler(context.Background(), mcp.CallToolRequest{})
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(result), "call one of apps-status, other-status")

	h.toolIndex.Store(&gatewayToolIndex{aliases: map[string][]gatewayToolAlias{"status": {{name: "apps-status", client: "apps"}}}})
	result, err = tools[0].Handler(context.Background(), mcp.CallToolRequest{})
	assert.Nil(t, result)
	require.Error(t, err)
	assert.Equal(t, "tool 'status' not found: tool not found", err.Error())
}

// Server-given names for a client with per_user_discovery on resolve against the caller's own tools:
// a name the caller holds runs as that caller, one it does not hold looks unknown, and an app-only
// tool of the caller's is refused on /mcp and runs on the client's slug for a caller hosting apps.
func TestMCPGateway_ServerGivenNamesForPerCallerClients(t *testing.T) {
	h, tm, store := newPerCallerGatewayHandler(t)
	store.mu.Lock()
	names := store.names["notes-id"]
	names.Tools = append(names.Tools, "board", "refresh")
	store.names["notes-id"] = names
	store.mu.Unlock()
	require.NoError(t, h.ResyncPerCallerEntries(context.Background()))

	hostsApps := &mcp.ClientCapabilities{Extensions: map[string]any{schemas.MCPAppsExtension: map[string]any{}}}
	as := func(caller, slug string) *schemas.BifrostContext {
		ctx := callerContext(caller)
		ctx.SetValue(schemas.BifrostContextKeyIsMCPGateway, true)
		ctx.SetValue(schemas.BifrostContextKeyMCPCallerCapabilities, hostsApps)
		if slug != "" {
			ctx.SetValue(schemas.BifrostContextKeyMCPGatewayClient, slug)
			ctx.SetValue(schemas.MCPContextKeyIncludeTools, []string{slug + "-*"})
		}
		return ctx
	}
	call := func(ctx context.Context, name string) (string, *mcp.JSONRPCErrorDetails) {
		raw, rpcErr := gatewayRequest(t, h, ctx, "tools/call", map[string]any{"name": name, "arguments": map[string]any{}})
		return string(raw), rpcErr
	}
	notFound := func(name string) string { return fmt.Sprintf("tool '%s' not found: tool not found", name) }

	out, rpcErr := call(as("alice", ""), "search")
	require.Nil(t, rpcErr)
	assert.Contains(t, out, `\"ran\":\"notes-search\",\"as\":\"alice\"`)
	out, rpcErr = call(as("bob", ""), "export")
	require.Nil(t, rpcErr)
	assert.Contains(t, out, `\"ran\":\"notes-export\",\"as\":\"bob\"`)

	_, rpcErr = call(as("alice", ""), "export")
	require.NotNil(t, rpcErr, "alice holds no notes-export")
	assert.Equal(t, notFound("export"), rpcErr.Message, "a per-caller tool the caller lacks reads as unknown")

	out, rpcErr = call(as("gina", ""), "refresh")
	require.Nil(t, rpcErr)
	assert.Contains(t, out, "only callable from an MCP App", "/mcp refuses the caller's app-only tool")
	out, rpcErr = call(as("gina", "notes"), "refresh")
	require.Nil(t, rpcErr)
	assert.Contains(t, out, `\"ran\":\"notes-refresh\",\"as\":\"gina\"`, "the client's slug runs it")

	assert.Equal(t, []string{"notes-search as alice", "notes-export as bob", "notes-refresh as gina"}, tm.reachedBifrost())
	for name := range listedTools(t, h, as("alice", "")) {
		assert.NotEqual(t, "", coremcp.ToolClientName(name), "tools/list carries no server-given name: %q", name)
	}
}
