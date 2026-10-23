package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fasthttp/router"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
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

// initializeResult drives a real initialize through the built server and returns the decoded
// result, which is what a connecting MCP client would actually receive.
func initializeResult(t *testing.T, tm MCPToolManager, ctx context.Context) map[string]any {
	t.Helper()
	h := &MCPServerHandler{
		toolManager: tm,
		config:      &lib.Config{ClientConfig: &configstore.ClientConfig{}},
	}
	h.mcpServer.Store(h.buildServer(nil))

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

// chainToolManager only adapts the transport interface to the real MCP manager.
// Discovery, permissions, upstream HTTP calls and result conversion stay on the
// production path at both hops; no tool result is manufactured by this adapter.
type chainToolManager struct{ manager *coremcp.MCPManager }

func (m *chainToolManager) GetAvailableMCPTools(ctx context.Context) []schemas.ChatTool {
	return m.manager.GetAvailableTools(schemas.NewBifrostContext(ctx, schemas.NoDeadline))
}

func (m *chainToolManager) ExecuteChatMCPTool(ctx context.Context, call *schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, *schemas.BifrostError) {
	return m.manager.ExecuteChatTool(schemas.NewBifrostContext(ctx, schemas.NoDeadline), call)
}

func (m *chainToolManager) ExecuteResponsesMCPTool(ctx context.Context, call *schemas.ResponsesToolMessage) (*schemas.ResponsesMessage, *schemas.BifrostError) {
	return m.manager.ExecuteResponsesTool(schemas.NewBifrostContext(ctx, schemas.NoDeadline), call)
}

// startChainGateway serves the actual Bifrost routes on an ephemeral TCP port.
func startChainGateway(t *testing.T, ctx context.Context, upstream *schemas.MCPClientConfig) (*coremcp.MCPManager, string) {
	t.Helper()
	manager := coremcp.NewMCPManager(ctx, schemas.MCPConfig{ToolSyncInterval: time.Hour}, nil, &mockLogger{}, nil)
	t.Cleanup(func() { require.NoError(t, manager.Cleanup()) })
	require.NoError(t, manager.AddClient(ctx, upstream))
	handler, err := NewMCPServerHandler(ctx, &lib.Config{ClientConfig: &configstore.ClientConfig{}}, &chainToolManager{manager}, nil, nil, nil)
	require.NoError(t, err)
	r := router.New()
	handler.RegisterRoutes(r)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	httpServer := &fasthttp.Server{Handler: r.Handler}
	done := make(chan error, 1)
	go func() { done <- httpServer.Serve(listener) }()
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		assert.NoError(t, httpServer.ShutdownWithContext(shutdownCtx))
		select {
		case err := <-done:
			assert.NoError(t, err)
		case <-shutdownCtx.Done():
			t.Error("gateway did not stop")
		}
	})
	return manager, "http://" + listener.Addr().String() + "/mcp"
}

func chainClientConfig(name, url string) *schemas.MCPClientConfig {
	return &schemas.MCPClientConfig{
		ID: name, Name: name,
		ConnectionType:   schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar(url),
		AuthType:         schemas.MCPAuthTypeNone,
		ToolsToExecute:   []string{"*"},
		ToolSyncInterval: time.Hour,
	}
}

// chainRPC checks the HTTP and JSON-RPC envelopes separately from tool IsError.
func chainRPC(t *testing.T, ctx context.Context, client *http.Client, url, method string, params any) json.RawMessage {
	t.Helper()
	id := schemas.Ptr(1)
	if method == "notifications/initialized" {
		id = nil
	}
	body, err := json.Marshal(struct {
		JSONRPC string `json:"jsonrpc"`
		ID      *int   `json:"id,omitempty"`
		Method  string `json:"method"`
		Params  any    `json:"params"`
	}{"2.0", id, method, params})
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	if id == nil {
		require.Equal(t, http.StatusAccepted, resp.StatusCode)
		return nil
	}
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var envelope struct {
		ID     int             `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&envelope))
	require.Equal(t, 1, envelope.ID)
	require.Empty(t, envelope.Error, "JSON-RPC error: %s", envelope.Error)
	require.NotEmpty(t, envelope.Result)
	return envelope.Result
}

func TestMCPGatewayChainPreservesToolErrors(t *testing.T) {
	SetLogger(&mockLogger{})
	for _, tc := range []struct {
		name         string
		text         string
		leafError    bool
		denyAtShared bool
	}{
		{name: "upstream tool error", text: "bad input", leafError: true},
		{name: "success", text: "ok"},
		{name: "success containing error text", text: "error count: 0"},
		{name: "shared execution refusal", denyAtShared: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			t.Cleanup(cancel)
			var calls atomic.Int32
			leaf := server.NewMCPServer("chain-leaf", "1", server.WithToolCapabilities(false))
			leaf.AddTool(mcpgo.NewTool("probe", mcpgo.WithString("message", mcpgo.Required())),
				func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
					calls.Add(1)
					text, _ := req.GetArguments()["message"].(string)
					result := mcpgo.NewToolResultText(text)
					result.IsError = tc.leafError
					return result, nil
				})
			leafHTTP := httptest.NewServer(server.NewStreamableHTTPServer(leaf))
			t.Cleanup(leafHTTP.Close)
			leafConfig := chainClientConfig("leaf", leafHTTP.URL)
			shared, sharedURL := startChainGateway(t, ctx, leafConfig)
			_, localURL := startChainGateway(t, ctx, chainClientConfig("estate", sharedURL))
			client, tool := chainDiscover(t, ctx, localURL)
			require.Zero(t, calls.Load(), "discovery must not execute the tool")
			if tc.denyAtShared {
				// Keep both handler registries and the local discovery cache intact;
				// the real shared execution gate must reject the stale callable name.
				updated := *leafConfig
				updated.ToolsToExecute = []string{}
				require.NoError(t, shared.UpdateClient(leafConfig.ID, &updated))
			}
			resultJSON := chainRPC(t, ctx, client, localURL, "tools/call", mcpgo.CallToolParams{
				Name:      tool.Name,
				Arguments: map[string]any{"message": tc.text},
			})
			var result struct {
				IsError bool                `json:"isError"`
				Content []mcpgo.TextContent `json:"content"`
			}
			require.NoError(t, json.Unmarshal(resultJSON, &result))
			assert.Equal(t, tc.leafError || tc.denyAtShared, result.IsError, "error flag must survive both gateways: %s", resultJSON)
			require.Len(t, result.Content, 1)
			assert.Equal(t, "text", result.Content[0].Type)
			if tc.denyAtShared {
				assert.Contains(t, result.Content[0].Text, "not permitted")
				assert.Zero(t, calls.Load(), "shared refusal must not reach the leaf")
			} else {
				assert.Equal(t, tc.text, result.Content[0].Text)
				assert.EqualValues(t, 1, calls.Load(), "one logical call must invoke the leaf once")
			}
		})
	}
}

func (m *chainToolManager) GetMCPServerInstructions(ctx context.Context) string {
	return m.manager.GetAggregatedServerInstructions(ctx)
}

// Include ordered text/whitespace, binary and text resources,
// links, structured content and result metadata in one wire-level comparison.
const chainRichResult = `{
  "_meta":{"fixture":"two-gateway","nested":{"count":2}},
  "content":[
    {"type":"text","text":" first\n"},
    {"type":"image","data":"aW1hZ2U=","mimeType":"image/png"},
    {"type":"text","text":"\tsecond "},
    {"type":"audio","data":"YXVkaW8=","mimeType":"audio/wav"},
    {"type":"resource","resource":{"uri":"fixture://report","mimeType":"application/pdf","blob":"JVBERi0xLjQK"}},
    {"type":"resource","resource":{"uri":"fixture://note","mimeType":"text/plain","text":" note\n"}},
    {"type":"resource_link","uri":"fixture://report","name":"report.pdf","description":"Open report","mimeType":"application/pdf"}
  ],
  "structuredContent":{"items":[1,"two"],"nested":{"ok":true}}
}`

// BenchmarkMCPGatewayResultDecode measures the SDK decode boundary used once
// per gateway hop; it excludes transport, discovery and gateway processing.
func BenchmarkMCPGatewayResultDecode(b *testing.B) {
	for _, tc := range []struct{ name, result string }{
		{"text", `{"content":[{"type":"text","text":"ok"}]}`},
		{"content_metadata", `{"content":[{"type":"text","text":"ok","_meta":{"part":1}}]}`},
		{"rich", chainRichResult},
	} {
		b.Run(tc.name, func(b *testing.B) {
			raw := json.RawMessage(tc.result)
			b.ReportAllocs()
			for b.Loop() {
				if _, err := mcpgo.ParseCallToolResult(&raw); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func chainLeaf(t *testing.T, annotations mcpgo.ToolAnnotation, execute server.ToolHandlerFunc) string {
	t.Helper()
	leaf := server.NewMCPServer("chain-leaf", "1", server.WithToolCapabilities(false))
	leaf.AddTool(mcpgo.NewTool("probe", mcpgo.WithToolAnnotation(annotations), mcpgo.WithString("operation", mcpgo.Required())), execute)
	leafHTTP := httptest.NewServer(server.NewStreamableHTTPServer(leaf))
	t.Cleanup(leafHTTP.Close)
	return leafHTTP.URL
}

func chainDiscover(t *testing.T, ctx context.Context, endpoint string) (*http.Client, mcpgo.Tool) {
	t.Helper()
	transport := &http.Transport{}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	chainRPC(t, ctx, client, endpoint, "initialize", mcpgo.InitializeParams{
		ProtocolVersion: mcpgo.LATEST_PROTOCOL_VERSION,
		ClientInfo:      mcpgo.Implementation{Name: "chain-test", Version: "1"},
	})
	chainRPC(t, ctx, client, endpoint, "notifications/initialized", struct{}{})
	var listed mcpgo.ListToolsResult
	require.NoError(t, json.Unmarshal(chainRPC(t, ctx, client, endpoint, "tools/list", struct{}{}), &listed))
	require.Len(t, listed.Tools, 1)
	require.Equal(t, "estate-leaf-probe", listed.Tools[0].Name)
	return client, listed.Tools[0]
}

func TestMCPGatewayChainPreservesRichResults(t *testing.T) {
	SetLogger(&mockLogger{})
	for _, tc := range []struct{ name, result string }{
		{"mixed_success", chainRichResult},
		{"mixed_error", `{"isError":true,` + chainRichResult[1:]},
		{"structured_only", `{"content":[],"structuredContent":{"answer":42},"_meta":{"source":"leaf"}}`},
		{"empty_success", `{"content":[]}`},
		{"content_metadata", `{"content":[{"type":"text","text":"with metadata","_meta":{"part":1}}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			t.Cleanup(cancel)
			var calls atomic.Int32
			raw := json.RawMessage(tc.result)
			var want *mcpgo.CallToolResult
			if tc.name == "content_metadata" {
				// Construct this natively: the SDK's ParseContent currently drops
				// block _meta, and using it here would lose data before serving it.
				want = &mcpgo.CallToolResult{Content: []mcpgo.Content{mcpgo.TextContent{
					Type: mcpgo.ContentTypeText, Text: "with metadata",
					Meta: &mcpgo.Meta{AdditionalFields: map[string]any{"part": 1}},
				}}}
			} else {
				var err error
				want, err = mcpgo.ParseCallToolResult(&raw)
				require.NoError(t, err)
			}
			leafJSON, err := json.Marshal(want)
			require.NoError(t, err)
			require.JSONEq(t, tc.result, string(leafJSON), "leaf must emit the exact fixture")
			leafURL := chainLeaf(t, mcpgo.ToolAnnotation{}, func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
				calls.Add(1)
				assert.Equal(t, "one", req.GetArguments()["operation"])
				return want, nil
			})
			_, sharedURL := startChainGateway(t, ctx, chainClientConfig("leaf", leafURL))
			_, localURL := startChainGateway(t, ctx, chainClientConfig("estate", sharedURL))
			client, tool := chainDiscover(t, ctx, localURL)
			require.Zero(t, calls.Load())
			got := chainRPC(t, ctx, client, localURL, "tools/call", mcpgo.CallToolParams{
				Name: tool.Name, Arguments: map[string]any{"operation": "one"},
			})
			assert.JSONEq(t, tc.result, string(got), "native result must survive both gateways")
			assert.EqualValues(t, 1, calls.Load())
		})
	}
}

// chainResponseLink forwards real HTTP requests and counts tools/call dispatches.
// On the first call only, it consumes the COMPLETE upstream response before
// replacing it with 503 or closing the connection. Thus the tool has already run;
// this cannot accidentally test a failure before dispatch. Handshakes and lists
// are never faulted, and later calls receive the real upstream response.
func chainResponseLink(t *testing.T, endpoint, fault string) (string, *atomic.Int32) {
	t.Helper()
	target, err := url.Parse(endpoint)
	require.NoError(t, err)
	path := target.Path
	target.Path = ""
	proxy := httputil.NewSingleHostReverseProxy(target)
	transport := &http.Transport{}
	proxy.Transport = transport
	t.Cleanup(transport.CloseIdleConnections)
	calls := &atomic.Int32{}
	link := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			http.Error(w, "fixture could not read request", http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		var request struct {
			Method string `json:"method"`
		}
		if json.Unmarshal(body, &request) != nil || request.Method != "tools/call" {
			proxy.ServeHTTP(w, r)
			return
		}
		attempt := calls.Add(1)
		recorder := httptest.NewRecorder()
		proxy.ServeHTTP(recorder, r)
		if attempt == 1 && fault != "" {
			assert.Equal(t, http.StatusOK, recorder.Code, "fault must follow a completed MCP response")
			var envelope struct {
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			assert.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
			assert.Empty(t, envelope.Error, "fault must not hide a fixture RPC error")
			assert.NotEmpty(t, envelope.Result, "fault must discard a real tool result")
			t.Logf("lost completed tools/call response: fault=%s response=%s", fault, recorder.Body.String())
			if fault == "503" {
				http.Error(w, "response lost after execution", http.StatusServiceUnavailable)
			} else {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = conn.Close()
			}
			return
		}
		for key, values := range recorder.Header() {
			w.Header()[key] = values
		}
		w.WriteHeader(recorder.Code)
		_, _ = w.Write(recorder.Body.Bytes())
	}))
	t.Cleanup(link.Close)
	return link.URL + path, calls
}

func TestMCPGatewayChainResponseLoss(t *testing.T) {
	SetLogger(&mockLogger{})
	for _, link := range []string{"leaf_to_shared", "shared_to_local"} {
		for _, fault := range []string{"503", "eof"} {
			for _, policy := range []string{"unknown", "additive", "destructive", "readonly", "idempotent"} {
				t.Run(link+"/"+fault+"/"+policy, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
					t.Cleanup(cancel)
					annotations := mcpgo.ToolAnnotation{}
					switch policy {
					case "additive", "destructive":
						annotations.DestructiveHint = schemas.Ptr(policy == "destructive")
						annotations.IdempotentHint = schemas.Ptr(false)
					case "readonly":
						annotations.ReadOnlyHint = schemas.Ptr(true)
					case "idempotent":
						annotations.IdempotentHint = schemas.Ptr(true)
					}
					var invocations, commits atomic.Int32
					leafURL := chainLeaf(t, annotations, func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
						invocations.Add(1)
						assert.Equal(t, "one", req.GetArguments()["operation"])
						if policy == "idempotent" {
							commits.CompareAndSwap(0, 1) // the fixture implements genuine deduplication
						} else if policy != "readonly" {
							commits.Add(1)
						}
						raw := json.RawMessage(chainRichResult)
						return mcpgo.ParseCallToolResult(&raw)
					})
					innerFault, outerFault := "", ""
					if link == "leaf_to_shared" {
						innerFault = fault
					} else {
						outerFault = fault
					}
					leafURL, innerCalls := chainResponseLink(t, leafURL, innerFault)
					_, sharedURL := startChainGateway(t, ctx, chainClientConfig("leaf", leafURL))
					sharedURL, outerCalls := chainResponseLink(t, sharedURL, outerFault)
					_, localURL := startChainGateway(t, ctx, chainClientConfig("estate", sharedURL))
					client, tool := chainDiscover(t, ctx, localURL)
					require.Zero(t, invocations.Load())
					// Discovery must carry the replay contract all the way to L.
					assert.Equal(t, annotations.ReadOnlyHint, tool.Annotations.ReadOnlyHint)
					assert.Equal(t, annotations.IdempotentHint, tool.Annotations.IdempotentHint)
					got := chainRPC(t, ctx, client, localURL, "tools/call", mcpgo.CallToolParams{
						Name: tool.Name, Arguments: map[string]any{"operation": "one"},
					})
					safe := policy == "readonly" || policy == "idempotent"
					wantCalls, wantOuter := int32(1), int32(1)
					var result struct {
						IsError bool `json:"isError"`
					}
					require.NoError(t, json.Unmarshal(got, &result))
					// Only safe 503 calls retry. EOF is not transient or auth-shaped,
					// even when an ephemeral port happens to contain 401 or 403.
					if safe && fault == "503" {
						wantCalls = 2
						if link == "shared_to_local" {
							wantOuter = 2
						}
						assert.JSONEq(t, chainRichResult, string(got), "successful replay must retain rich results")
					} else {
						assert.True(t, result.IsError, "ambiguous failure must survive both gateways: %s", got)
						if !safe {
							assert.Contains(t, string(got), "execution may have succeeded")
							assert.Contains(t, string(got), "automatic replay was suppressed")
						} else {
							assert.Contains(t, string(got), "EOF")
						}
					}
					assert.Equal(t, wantCalls, invocations.Load(), "leaf invocations")
					assert.Equal(t, wantCalls, innerCalls.Load(), "S -> leaf dispatches")
					assert.Equal(t, wantOuter, outerCalls.Load(), "L -> S dispatches")
					wantCommits := int32(1)
					if policy == "readonly" {
						wantCommits = 0
					}
					assert.Equal(t, wantCommits, commits.Load(), "committed effects per logical call")
				})
			}
		}
	}
}
