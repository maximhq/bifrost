package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fasthttp/router"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
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
	h.mcpServer.SetTools(h.serverTools([]schemas.ChatTool{{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: "alpha-echo"}}})...)

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
	}{
		{name: "nil admitter serves a configured client unrestricted", slug: "context7", wantTools: []string{"context7-*"}},
		{name: "nil admitter refuses an unknown slug", slug: "definitely-does-not-exist", wantStatus: 403},
		{name: "granted virtual mcp", admitter: resolver, slug: "research-tools", wantTools: []string{"alpha-search"}},
		{name: "granted mcp client", admitter: resolver, slug: "context7", wantTools: []string{"context7-*"}},
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
			benchmarkGatewayRequest(b, h, name+"/"+tc.name, tc.headers, tc.body)
		}
	}
}

func benchmarkGatewayRequest(b *testing.B, h *MCPServerHandler, name string, headers map[string]string, body string) {
	b.Run(name, func(b *testing.B) {
		var ctx fasthttp.RequestCtx
		ctx.Request.Header.SetMethod(fasthttp.MethodPost)
		ctx.Request.SetRequestURI("/mcp")
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
