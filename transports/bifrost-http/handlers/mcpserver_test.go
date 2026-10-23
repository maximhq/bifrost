package handlers

import (
	"context"
	"encoding/json"
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

// BenchmarkMCPGateway drives the /mcp handler in process: admission, transport and the server.
func BenchmarkMCPGateway(b *testing.B) {
	h := newGatewayHandler(b)
	legacy := map[string]string{"Content-Type": "application/json", "Accept": "application/json, text/event-stream", "MCP-Protocol-Version": "2025-11-25"}
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
		{"discover/2026-07-28", modern("server/discover", ""), `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{` + modernMeta + `}}`},
		{"tools-list/2026-07-28", modern("tools/list", ""), `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{` + modernMeta + `}}`},
		{"tools-call/2026-07-28", modern("tools/call", "gw-echo"), `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"gw-echo","arguments":{"text":"hi"},` + modernMeta + `}}`},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			var ctx fasthttp.RequestCtx
			ctx.Request.Header.SetMethod(fasthttp.MethodPost)
			ctx.Request.SetRequestURI("/mcp")
			for k, v := range tc.headers {
				ctx.Request.Header.Set(k, v)
			}
			ctx.Request.SetBodyString(tc.body)
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
}
