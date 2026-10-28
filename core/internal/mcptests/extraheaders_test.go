package mcptests

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	core "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/mcp/codemode/starlark"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// EXTRA-HEADER FORWARDING (wire-level) TESTS
//
// These tests assert the end-to-end contract that BifrostContextKeyMCPExtraHeaders
// set during the plugin gate is forwarded to the upstream MCP server — on
// bifrost-generated health-check probes (ping AND list_tools) as well as on a
// normal caller tools/call — scoped by MCPClientConfig.AllowedExtraHeaders.
//
// They deliberately use a real streamable-HTTP transport (not the in-process
// transport) because the per-request headers are injected by the transport
// headerFunc registered in createHTTPConnection; the in-process transport has no
// HTTP layer and would never exercise that path.
// =============================================================================

const (
	allowedExtraHeader = "X-Allowed-Extra"
	allowedExtraValue  = "reached-the-wire"
	deniedExtraHeader  = "X-Denied-Extra"
)

// recordedRequest captures the JSON-RPC method and inbound headers of a single
// HTTP request the upstream MCP server received.
type recordedRequest struct {
	method string // JSON-RPC method ("ping", "tools/list", "tools/call", ...); "" if not parseable
	header http.Header
}

// headerRecorder is a thread-safe sink for inbound request headers, fronting the
// mcp-go streamable HTTP handler.
type headerRecorder struct {
	mu   sync.Mutex
	reqs []recordedRequest
}

func (hr *headerRecorder) add(method string, h http.Header) {
	hr.mu.Lock()
	defer hr.mu.Unlock()
	hr.reqs = append(hr.reqs, recordedRequest{method: method, header: h.Clone()})
}

// valuesForMethod returns every value seen for headerName across recorded
// requests whose JSON-RPC method matches.
func (hr *headerRecorder) valuesForMethod(method, headerName string) []string {
	hr.mu.Lock()
	defer hr.mu.Unlock()
	var out []string
	for _, r := range hr.reqs {
		if r.method == method {
			if v := r.header.Get(headerName); v != "" {
				out = append(out, v)
			}
		}
	}
	return out
}

// anyRequestHasHeader reports whether ANY recorded request (of any method)
// carried headerName — used to prove a non-allowlisted header never escapes.
func (hr *headerRecorder) anyRequestHasHeader(headerName string) bool {
	hr.mu.Lock()
	defer hr.mu.Unlock()
	for _, r := range hr.reqs {
		if r.header.Get(headerName) != "" {
			return true
		}
	}
	return false
}

// buildRecordingHTTPServer starts a streamable-HTTP MCP server (one "echo" tool)
// behind an httptest server whose middleware records the JSON-RPC method and
// inbound headers of every request. StreamableHTTPServer.ServeHTTP dispatches on
// HTTP method only (it ignores the path), so delegating from a root-mounted
// handler is sufficient.
func buildRecordingHTTPServer(t *testing.T, opts ...server.StreamableHTTPOption) (*httptest.Server, *headerRecorder) {
	t.Helper()

	s := server.NewMCPServer("test-http-headers", "1.0.0", server.WithToolCapabilities(true))
	echoTool := mcpgo.NewTool("echo",
		mcpgo.WithDescription("Echo tool"),
		mcpgo.WithString("message", mcpgo.Required(), mcpgo.Description("message")),
	)
	s.AddTool(echoTool, func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		msg, _ := req.GetArguments()["message"].(string)
		return mcpgo.NewToolResultText(msg), nil
	})

	streamable := server.NewStreamableHTTPServer(s, opts...)
	rec := &headerRecorder{}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := ""
		if r.Method == http.MethodPost && r.Body != nil {
			if body, err := io.ReadAll(r.Body); err == nil {
				var probe struct {
					Method string `json:"method"`
				}
				_ = json.Unmarshal(body, &probe)
				method = probe.Method
				// Restore the consumed body for the streamable handler.
				r.Body = io.NopCloser(bytes.NewReader(body))
			}
		}
		rec.add(method, r.Header)
		streamable.ServeHTTP(w, r)
	})

	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts, rec
}

// buildRecordingSSEServer starts an SSE MCP server (one "echo" tool) behind an
// httptest server whose middleware records the JSON-RPC method and inbound
// headers of every request. SSE splits the long-lived GET event stream from the
// POST /message requests, so the JSON-RPC method is parsed from POST bodies only
// (the GET stream records as method ""). The SSEServer must advertise an absolute
// message endpoint back to the client, which is only known once httptest has
// bound a port — hence sseSrv is wired after ts is created.
func buildRecordingSSEServer(t *testing.T, legacy bool) (*httptest.Server, *headerRecorder) {
	t.Helper()

	s := server.NewMCPServer("test-sse-headers", "1.0.0", server.WithToolCapabilities(true))
	echoTool := mcpgo.NewTool("echo",
		mcpgo.WithDescription("Echo tool"),
		mcpgo.WithString("message", mcpgo.Required(), mcpgo.Description("message")),
	)
	s.AddTool(echoTool, func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		msg, _ := req.GetArguments()["message"].(string)
		return mcpgo.NewToolResultText(msg), nil
	})

	rec := &headerRecorder{}
	var sseSrv *server.SSEServer

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := ""
		if r.Method == http.MethodPost && r.Body != nil {
			if body, err := io.ReadAll(r.Body); err == nil {
				var probe struct {
					Method string `json:"method"`
				}
				_ = json.Unmarshal(body, &probe)
				method = probe.Method
				// A pre-2026-07-28 server answers server/discover with method not found.
				if legacy && method == string(mcpgo.MethodServerDiscover) {
					body = bytes.Replace(body, []byte(`"server/discover"`), []byte(`"server/discover-unknown"`), 1)
				}
				// Restore the consumed body for the SSE handler.
				r.Body = io.NopCloser(bytes.NewReader(body))
			}
		}
		rec.add(method, r.Header)
		sseSrv.ServeHTTP(w, r)
	})

	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	sseSrv = server.NewSSEServer(s, server.WithBaseURL(ts.URL))
	return ts, rec
}

// extraHeaderInjectPlugin sets BifrostContextKeyMCPExtraHeaders on every MCP
// request that flows through the generic gate (ping / list_tools / tools/call).
// It injects one allowlisted and one non-allowlisted header so the tests can
// prove both forwarding and AllowedExtraHeaders filtering at the wire.
type extraHeaderInjectPlugin struct {
	schemas.MCPPluginNoOpHooks
}

func (p *extraHeaderInjectPlugin) GetName() string { return "extraHeaderInjectPlugin" }
func (p *extraHeaderInjectPlugin) Cleanup() error  { return nil }

func (p *extraHeaderInjectPlugin) PreMCPHook(ctx *schemas.BifrostContext, req *schemas.BifrostMCPRequest) (*schemas.BifrostMCPRequest, *schemas.MCPPluginShortCircuit, error) {
	ctx.SetValue(schemas.BifrostContextKeyMCPExtraHeaders, map[string][]string{
		allowedExtraHeader: {allowedExtraValue},
		deniedExtraHeader:  {"should-be-filtered"},
	})
	return req, nil, nil
}

// httpExtraHeaderConfig builds an HTTP MCP client config that allowlists only
// allowedExtraHeader.
func httpExtraHeaderConfig(clientName, serverURL string) *schemas.MCPClientConfig {
	return &schemas.MCPClientConfig{
		ID:                  clientName + "-id",
		Name:                clientName,
		ConnectionType:      schemas.MCPConnectionTypeHTTP,
		ConnectionString:    schemas.NewSecretVar(serverURL),
		ToolsToExecute:      []string{"*"},
		AllowedExtraHeaders: schemas.WhiteList{allowedExtraHeader},
	}
}

// sseExtraHeaderConfig builds an SSE MCP client config that allowlists only
// allowedExtraHeader. The connection string targets the SSE event-stream
// endpoint ("/sse"); the client learns the message endpoint from the stream.
func sseExtraHeaderConfig(clientName, serverURL string) *schemas.MCPClientConfig {
	return &schemas.MCPClientConfig{
		ID:                  clientName + "-id",
		Name:                clientName,
		ConnectionType:      schemas.MCPConnectionTypeSSE,
		ConnectionString:    schemas.NewSecretVar(serverURL + "/sse"),
		ToolsToExecute:      []string{"*"},
		AllowedExtraHeaders: schemas.WhiteList{allowedExtraHeader},
	}
}

// TestExtraHeadersHealthCheckPingReachWire is the core regression for the bug:
// a hook-set extra header must ride a bifrost-generated health-check ping onto
// the wire (the library drops request.Header for ping, so this can only work via
// the transport headerFunc).
func TestExtraHeadersHealthCheckPingReachWire(t *testing.T) {
	t.Parallel()

	ts, rec := buildRecordingHTTPServer(t, server.WithStreamableHTTPProtocolVersions(mcpgo.ProtocolVersion20251125))
	manager, _ := setupBifrostWithPlugins(t, []schemas.MCPPlugin{&extraHeaderInjectPlugin{}})

	cfg := httpExtraHeaderConfig("hc_ping", ts.URL)
	require.NoError(t, manager.AddClient(context.Background(), cfg))

	// Drive fast health-check pings (the AddClient-owned monitor ticks far too
	// slowly for a test).
	monitor := mcp.NewClientConnectionChecker(manager, cfg.ID, 10*time.Millisecond, true, core.NewDefaultLogger(schemas.LogLevelError))
	monitor.Start()
	defer monitor.Stop()

	require.Eventually(t, func() bool {
		return len(rec.valuesForMethod("ping", allowedExtraHeader)) > 0
	}, 3*time.Second, 10*time.Millisecond, "allowlisted extra header should reach the upstream on a health-check ping")

	vals := rec.valuesForMethod("ping", allowedExtraHeader)
	require.NotEmpty(t, vals)
	assert.Equal(t, allowedExtraValue, vals[0], "allowlisted header value should be forwarded verbatim")

	assert.False(t, rec.anyRequestHasHeader(deniedExtraHeader), "non-allowlisted extra header must be filtered out everywhere")
}

// TestExtraHeadersHealthCheckListToolsReachWire covers the ping-unavailable
// fallback where list_tools is the liveness probe.
func TestExtraHeadersHealthCheckListToolsReachWire(t *testing.T) {
	t.Parallel()

	ts, rec := buildRecordingHTTPServer(t)
	manager, _ := setupBifrostWithPlugins(t, []schemas.MCPPlugin{&extraHeaderInjectPlugin{}})

	cfg := httpExtraHeaderConfig("hc_list", ts.URL)
	require.NoError(t, manager.AddClient(context.Background(), cfg))

	// isPingAvailable=false → health check uses list_tools as the probe.
	monitor := mcp.NewClientConnectionChecker(manager, cfg.ID, 10*time.Millisecond, false, core.NewDefaultLogger(schemas.LogLevelError))
	monitor.Start()
	defer monitor.Stop()

	require.Eventually(t, func() bool {
		return len(rec.valuesForMethod("tools/list", allowedExtraHeader)) > 0
	}, 3*time.Second, 10*time.Millisecond, "allowlisted extra header should reach the upstream on a health-check list_tools probe")

	assert.False(t, rec.anyRequestHasHeader(deniedExtraHeader), "non-allowlisted extra header must be filtered out everywhere")
}

// TestExtraHeadersToolCallReachWire guards the centralization change: removing
// the per-call CallToolRequest.Header must NOT stop a normal tools/call from
// forwarding allowlisted extras — they now ride the same transport headerFunc.
func TestExtraHeadersToolCallReachWire(t *testing.T) {
	t.Parallel()

	ts, rec := buildRecordingHTTPServer(t)
	manager, bf := setupBifrostWithPlugins(t, []schemas.MCPPlugin{&extraHeaderInjectPlugin{}})

	cfg := httpExtraHeaderConfig("call_extra", ts.URL)
	require.NoError(t, manager.AddClient(context.Background(), cfg))

	toolCall := schemas.ChatAssistantMessageToolCall{
		ID: schemas.Ptr("call-1"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("call_extra-echo"),
			Arguments: `{"message":"hi"}`,
		},
	}
	_, bErr := bf.ExecuteChatMCPTool(createTestContext(), &toolCall)
	require.Nil(t, bErr, "echo tool call should succeed")

	vals := rec.valuesForMethod("tools/call", allowedExtraHeader)
	require.NotEmpty(t, vals, "allowlisted extra header should reach the upstream on a normal tools/call")
	assert.Equal(t, allowedExtraValue, vals[0])

	assert.False(t, rec.anyRequestHasHeader(deniedExtraHeader), "non-allowlisted extra header must be filtered out everywhere")
}

// TestExtraHeadersSSEHealthCheckPingReachWire is the SSE analogue of the ping
// regression: createSSEConnection registers the same headerFunc, so a hook-set
// extra header must ride a bifrost-generated health-check ping onto the wire.
func TestExtraHeadersSSEHealthCheckPingReachWire(t *testing.T) {
	t.Parallel()

	ts, rec := buildRecordingSSEServer(t, true)
	manager, _ := setupBifrostWithPlugins(t, []schemas.MCPPlugin{&extraHeaderInjectPlugin{}})

	cfg := sseExtraHeaderConfig("sse_hc_ping", ts.URL)
	require.NoError(t, manager.AddClient(context.Background(), cfg))

	monitor := mcp.NewClientConnectionChecker(manager, cfg.ID, 10*time.Millisecond, true, core.NewDefaultLogger(schemas.LogLevelError))
	monitor.Start()
	defer monitor.Stop()

	require.Eventually(t, func() bool {
		return len(rec.valuesForMethod("ping", allowedExtraHeader)) > 0
	}, 3*time.Second, 10*time.Millisecond, "allowlisted extra header should reach the SSE upstream on a health-check ping")

	vals := rec.valuesForMethod("ping", allowedExtraHeader)
	require.NotEmpty(t, vals)
	assert.Equal(t, allowedExtraValue, vals[0], "allowlisted header value should be forwarded verbatim")

	assert.False(t, rec.anyRequestHasHeader(deniedExtraHeader), "non-allowlisted extra header must be filtered out everywhere")
}

// TestExtraHeadersSSEHealthCheckListToolsReachWire covers the SSE ping-unavailable
// fallback where list_tools is the liveness probe.
func TestExtraHeadersSSEHealthCheckListToolsReachWire(t *testing.T) {
	t.Parallel()

	ts, rec := buildRecordingSSEServer(t, false)
	manager, _ := setupBifrostWithPlugins(t, []schemas.MCPPlugin{&extraHeaderInjectPlugin{}})

	cfg := sseExtraHeaderConfig("sse_hc_list", ts.URL)
	require.NoError(t, manager.AddClient(context.Background(), cfg))

	monitor := mcp.NewClientConnectionChecker(manager, cfg.ID, 10*time.Millisecond, false, core.NewDefaultLogger(schemas.LogLevelError))
	monitor.Start()
	defer monitor.Stop()

	require.Eventually(t, func() bool {
		return len(rec.valuesForMethod("tools/list", allowedExtraHeader)) > 0
	}, 3*time.Second, 10*time.Millisecond, "allowlisted extra header should reach the SSE upstream on a health-check list_tools probe")

	assert.False(t, rec.anyRequestHasHeader(deniedExtraHeader), "non-allowlisted extra header must be filtered out everywhere")
}

// TestExtraHeadersSSEToolCallReachWire guards the SSE side of the centralization
// change: a normal tools/call must forward allowlisted extras via the transport
// headerFunc.
func TestExtraHeadersSSEToolCallReachWire(t *testing.T) {
	t.Parallel()

	ts, rec := buildRecordingSSEServer(t, false)
	manager, bf := setupBifrostWithPlugins(t, []schemas.MCPPlugin{&extraHeaderInjectPlugin{}})

	cfg := sseExtraHeaderConfig("sse_call_extra", ts.URL)
	require.NoError(t, manager.AddClient(context.Background(), cfg))

	toolCall := schemas.ChatAssistantMessageToolCall{
		ID: schemas.Ptr("sse-call-1"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("sse_call_extra-echo"),
			Arguments: `{"message":"hi"}`,
		},
	}
	_, bErr := bf.ExecuteChatMCPTool(createTestContext(), &toolCall)
	require.Nil(t, bErr, "echo tool call should succeed")

	vals := rec.valuesForMethod("tools/call", allowedExtraHeader)
	require.NotEmpty(t, vals, "allowlisted extra header should reach the SSE upstream on a normal tools/call")
	assert.Equal(t, allowedExtraValue, vals[0])

	assert.False(t, rec.anyRequestHasHeader(deniedExtraHeader), "non-allowlisted extra header must be filtered out everywhere")
}

// =============================================================================
// Outbound token (per-call Authorization) tests
// =============================================================================

const outboundStaticAuth = "Bearer static-credential"

type outboundTenantKey struct{}

// funcTokenProvider adapts a function to schemas.OutboundMCPTokenProvider.
type funcTokenProvider func(ctx context.Context, serverName string) (string, error)

func (f funcTokenProvider) GetTokenForUpstream(ctx context.Context, serverName string) (string, error) {
	return f(ctx, serverName)
}

// buildAuthEchoServer echoes message and received Authorization header.
func buildAuthEchoServer(t *testing.T) (*httptest.Server, *headerRecorder) {
	t.Helper()

	type authKey struct{}
	s := server.NewMCPServer("test-outbound-token", "1.0.0", server.WithToolCapabilities(true))
	s.AddTool(mcpgo.NewTool("echo",
		mcpgo.WithDescription("Echo tool"),
		mcpgo.WithString("message", mcpgo.Required(), mcpgo.Description("message")),
	), func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		msg, _ := req.GetArguments()["message"].(string)
		auth, _ := ctx.Value(authKey{}).(string)
		return mcpgo.NewToolResultText(msg + "|" + auth), nil
	})

	streamable := server.NewStreamableHTTPServer(s, server.WithHTTPContextFunc(func(ctx context.Context, r *http.Request) context.Context {
		return context.WithValue(ctx, authKey{}, r.Header.Get("Authorization"))
	}))
	rec := &headerRecorder{}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := ""
		if r.Method == http.MethodPost && r.Body != nil {
			if body, err := io.ReadAll(r.Body); err == nil {
				var probe struct {
					Method string `json:"method"`
				}
				_ = json.Unmarshal(body, &probe)
				method = probe.Method
				r.Body = io.NopCloser(bytes.NewReader(body))
			}
		}
		rec.add(method, r.Header)
		streamable.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts, rec
}

// removeClientsOnCleanup closes every client before the test servers shut down; an open
// upstream stream would otherwise block httptest.Server.Close.
func removeClientsOnCleanup(t *testing.T, manager *mcp.MCPManager) {
	t.Helper()
	t.Cleanup(func() {
		for _, c := range manager.GetClients() {
			_ = manager.RemoveClient(c.ExecutionConfig.ID)
		}
	})
}

// setupOutboundTokenBifrostWithStickiness wires manager and client with configured stickiness.
func setupOutboundTokenBifrostWithStickiness(t *testing.T, provider schemas.OutboundMCPTokenProvider, clientName, serverURL string, sticky bool) (*mcp.MCPManager, *core.Bifrost) {
	t.Helper()

	logger := newTestLogger(t)
	manager := mcp.NewMCPManager(context.Background(), schemas.MCPConfig{
		OutboundTokenProvider: provider,
		PluginPipelineProvider: func() interface{} {
			return &noopPluginPipeline{}
		},
		ReleasePluginPipeline: func(pipeline interface{}) {},
	}, nil, logger, nil)
	bf, err := core.Init(context.Background(), schemas.BifrostConfig{
		Account: &testAccount{},
		Logger:  core.NewDefaultLogger(schemas.LogLevelError),
	})
	require.NoError(t, err)
	bf.SetMCPManager(manager)
	removeClientsOnCleanup(t, manager)

	var stickiness *bool
	if sticky {
		stickiness = schemas.Ptr(true)
	}
	require.NoError(t, manager.AddClient(context.Background(), &schemas.MCPClientConfig{
		ID:                     clientName + "-id",
		Name:                   clientName,
		ConnectionType:         schemas.MCPConnectionTypeHTTP,
		ConnectionString:       schemas.NewSecretVar(serverURL),
		AuthType:               schemas.MCPAuthTypeHeaders,
		Headers:                map[string]schemas.SecretVar{"Authorization": *schemas.NewSecretVar(outboundStaticAuth)},
		ToolsToExecute:         []string{"*"},
		NeedsSessionStickiness: stickiness,
	}))
	return manager, bf
}

func callOutboundEcho(bf *core.Bifrost, ctx *schemas.BifrostContext, clientName, message string) (string, *schemas.BifrostError) {
	msg, bErr := bf.ExecuteChatMCPTool(ctx, &schemas.ChatAssistantMessageToolCall{
		ID: schemas.Ptr("call-" + message),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr(clientName + "-echo"),
			Arguments: fmt.Sprintf(`{"message":%q}`, message),
		},
	})
	if bErr != nil {
		return "", bErr
	}
	if msg == nil || msg.Content == nil || msg.Content.ContentStr == nil {
		return "", &schemas.BifrostError{Error: &schemas.ErrorField{Message: "tool result carried no text"}}
	}
	return *msg.Content.ContentStr, nil
}

// TestOutboundTokenConcurrentCallsAreIsolated proves concurrent calls isolate tokens across stickiness modes.
func TestOutboundTokenConcurrentCallsAreIsolated(t *testing.T) {
	t.Parallel()

	for _, sticky := range []bool{false, true} {
		name := "non_sticky"
		if sticky {
			name = "sticky"
		}
		t.Run(name, func(t *testing.T) {
			ts, rec := buildAuthEchoServer(t)
			provider := funcTokenProvider(func(ctx context.Context, serverName string) (string, error) {
				tenant, _ := ctx.Value(outboundTenantKey{}).(string)
				return "token-" + tenant, nil
			})
			clientName := "outbound_conc_" + name
			_, bf := setupOutboundTokenBifrostWithStickiness(t, provider, clientName, ts.URL, sticky)

			tenants := []string{"alpha", "beta", "gamma", "delta", "epsilon", "zeta"}
			results := make([]string, len(tenants))
			errs := make([]*schemas.BifrostError, len(tenants))
			var wg sync.WaitGroup
			for i, tenant := range tenants {
				wg.Add(1)
				go func(idx int, tn string) {
					defer wg.Done()
					ctx := createTestContext()
					ctx.SetValue(outboundTenantKey{}, tn)
					results[idx], errs[idx] = callOutboundEcho(bf, ctx, clientName, tn)
				}(i, tenant)
			}
			wg.Wait()

			for i, tenant := range tenants {
				require.Nil(t, errs[i], "call for %s should succeed", tenant)
				assert.Equal(t, tenant+"|Bearer token-"+tenant, results[i], "upstream must see only this call token")
			}

			wire := rec.valuesForMethod("tools/call", "Authorization")
			require.Len(t, wire, len(tenants))
			for _, tenant := range tenants {
				assert.Contains(t, wire, "Bearer token-"+tenant)
			}
			assert.NotContains(t, wire, outboundStaticAuth, "no call should fall back to static credential")
		})
	}
}

// TestOutboundTokenEmptyKeepsConfiguredCredential proves empty token keeps static auth across stickiness modes.
func TestOutboundTokenEmptyKeepsConfiguredCredential(t *testing.T) {
	t.Parallel()

	for _, sticky := range []bool{false, true} {
		name := "non_sticky"
		if sticky {
			name = "sticky"
		}
		t.Run(name, func(t *testing.T) {
			ts, rec := buildAuthEchoServer(t)
			provider := funcTokenProvider(func(context.Context, string) (string, error) { return "", nil })
			clientName := "outbound_empty_" + name
			_, bf := setupOutboundTokenBifrostWithStickiness(t, provider, clientName, ts.URL, sticky)

			out, bErr := callOutboundEcho(bf, createTestContext(), clientName, "hi")
			require.Nil(t, bErr)
			assert.Equal(t, "hi|"+outboundStaticAuth, out)
			assert.Equal(t, []string{outboundStaticAuth}, rec.valuesForMethod("tools/call", "Authorization"))
		})
	}
}

// TestOutboundTokenProviderErrorFailsCall proves provider errors fail before wire transport.
func TestOutboundTokenProviderErrorFailsCall(t *testing.T) {
	t.Parallel()

	for _, sticky := range []bool{false, true} {
		name := "non_sticky"
		if sticky {
			name = "sticky"
		}
		t.Run(name, func(t *testing.T) {
			ts, rec := buildAuthEchoServer(t)
			provider := funcTokenProvider(func(context.Context, string) (string, error) {
				return "", errors.New("secret provider failure")
			})
			clientName := "outbound_err_" + name
			_, bf := setupOutboundTokenBifrostWithStickiness(t, provider, clientName, ts.URL, sticky)

			rec.mu.Lock()
			nBefore := len(rec.reqs)
			rec.mu.Unlock()

			_, bErr := callOutboundEcho(bf, createTestContext(), clientName, "hi")
			require.NotNil(t, bErr, "provider error must fail the tool call")
			require.NotNil(t, bErr.Error)
			assert.Equal(t, fmt.Sprintf("failed to resolve outbound token for MCP server '%s'", clientName), bErr.Error.Message)
			assert.NotContains(t, bErr.Error.Message, "secret provider failure")

			rec.mu.Lock()
			nAfter := len(rec.reqs)
			rec.mu.Unlock()

			if !sticky {
				assert.Equal(t, 0, nAfter-nBefore, "no request of any method may reach upstream on non-sticky client")
			} else {
				assert.Empty(t, rec.valuesForMethod("tools/call", "Authorization"), "no tools/call may reach upstream")
			}
		})
	}
}

// outboundTokenHookPlugin injects token into context via PreMCPHook.
type outboundTokenHookPlugin struct {
	schemas.MCPPluginNoOpHooks
	token string
}

func (p *outboundTokenHookPlugin) GetName() string { return "outboundTokenHookPlugin" }
func (p *outboundTokenHookPlugin) Cleanup() error  { return nil }

func (p *outboundTokenHookPlugin) PreMCPHook(ctx *schemas.BifrostContext, req *schemas.BifrostMCPRequest) (*schemas.BifrostMCPRequest, *schemas.MCPPluginShortCircuit, error) {
	ctx.SetValue(schemas.BifrostContextKeyMCPOutboundToken, p.token)
	return req, nil, nil
}

// TestOutboundTokenContextKeyWithoutProvider proves hook context key alone works across stickiness modes.
func TestOutboundTokenContextKeyWithoutProvider(t *testing.T) {
	t.Parallel()

	for _, sticky := range []bool{false, true} {
		name := "non_sticky"
		if sticky {
			name = "sticky"
		}
		t.Run(name, func(t *testing.T) {
			ts, rec := buildAuthEchoServer(t)
			manager := setupMCPManager(t)
			bf, err := core.Init(context.Background(), schemas.BifrostConfig{
				Account:    &testAccount{},
				MCPPlugins: []schemas.MCPPlugin{&outboundTokenHookPlugin{token: "hook-token"}},
				Logger:     core.NewDefaultLogger(schemas.LogLevelError),
			})
			require.NoError(t, err)
			bf.SetMCPManager(manager)
			removeClientsOnCleanup(t, manager)

			var stickiness *bool
			if sticky {
				stickiness = schemas.Ptr(true)
			}
			clientName := "outbound_ctx_" + name
			require.NoError(t, manager.AddClient(context.Background(), &schemas.MCPClientConfig{
				ID:                     clientName + "-id",
				Name:                   clientName,
				ConnectionType:         schemas.MCPConnectionTypeHTTP,
				ConnectionString:       schemas.NewSecretVar(ts.URL),
				AuthType:               schemas.MCPAuthTypeHeaders,
				Headers:                map[string]schemas.SecretVar{"Authorization": *schemas.NewSecretVar(outboundStaticAuth)},
				ToolsToExecute:         []string{"*"},
				NeedsSessionStickiness: stickiness,
			}))

			out, bErr := callOutboundEcho(bf, createTestContext(), clientName, "hi")
			require.Nil(t, bErr)
			assert.Equal(t, "hi|Bearer hook-token", out)
			assert.Equal(t, []string{"Bearer hook-token"}, rec.valuesForMethod("tools/call", "Authorization"))
		})
	}
}

// TestOutboundTokenProviderTakesPrecedenceOverContextKey proves provider wins over hook key.
func TestOutboundTokenProviderTakesPrecedenceOverContextKey(t *testing.T) {
	t.Parallel()

	for _, sticky := range []bool{false, true} {
		name := "non_sticky"
		if sticky {
			name = "sticky"
		}
		t.Run(name, func(t *testing.T) {
			ts, _ := buildAuthEchoServer(t)
			provider := funcTokenProvider(func(context.Context, string) (string, error) { return "provider-token", nil })
			clientName := "outbound_prec_" + name
			_, bf := setupOutboundTokenBifrostWithStickiness(t, provider, clientName, ts.URL, sticky)

			ctx := createTestContext()
			ctx.SetValue(schemas.BifrostContextKeyMCPOutboundToken, "ctx-token")
			out, bErr := callOutboundEcho(bf, ctx, clientName, "hi")
			require.Nil(t, bErr)
			assert.Equal(t, "hi|Bearer provider-token", out)
		})
	}
}

// TestOutboundTokenNeverOnProbes proves probes carry static auth on sticky client even with hook writing key.
func TestOutboundTokenNeverOnProbes(t *testing.T) {
	t.Parallel()

	ts, rec := buildAuthEchoServer(t)
	logger := newTestLogger(t)
	manager := mcp.NewMCPManager(context.Background(), schemas.MCPConfig{
		PluginPipelineProvider: func() interface{} { return &noopPluginPipeline{} },
		ReleasePluginPipeline:  func(pipeline interface{}) {},
	}, nil, logger, nil)
	bf, err := core.Init(context.Background(), schemas.BifrostConfig{
		Account:    &testAccount{},
		MCPPlugins: []schemas.MCPPlugin{&outboundTokenHookPlugin{token: "hook-probe-leak"}},
		Logger:     core.NewDefaultLogger(schemas.LogLevelError),
	})
	require.NoError(t, err)
	bf.SetMCPManager(manager)
	removeClientsOnCleanup(t, manager)

	clientID := "outbound_probe-id"
	clientName := "outbound_probe"
	require.NoError(t, manager.AddClient(context.Background(), &schemas.MCPClientConfig{
		ID:                     clientID,
		Name:                   clientName,
		ConnectionType:         schemas.MCPConnectionTypeHTTP,
		ConnectionString:       schemas.NewSecretVar(ts.URL),
		AuthType:               schemas.MCPAuthTypeHeaders,
		Headers:                map[string]schemas.SecretVar{"Authorization": *schemas.NewSecretVar(outboundStaticAuth)},
		ToolsToExecute:         []string{"*"},
		NeedsSessionStickiness: schemas.Ptr(true),
	}))
	t.Cleanup(func() { _ = manager.RemoveClient(clientID) })

	monitor := mcp.NewClientConnectionChecker(manager, clientID, 10*time.Millisecond, false, core.NewDefaultLogger(schemas.LogLevelError))
	monitor.Start()
	defer monitor.Stop()

	require.Eventually(t, func() bool {
		return len(rec.valuesForMethod("tools/list", "Authorization")) >= 1
	}, 3*time.Second, 10*time.Millisecond)

	out, bErr := callOutboundEcho(bf, createTestContext(), clientName, "hi")
	require.Nil(t, bErr)
	assert.Equal(t, "hi|Bearer hook-probe-leak", out)

	initialProbeCount := len(rec.valuesForMethod("tools/list", "Authorization"))
	require.Eventually(t, func() bool {
		return len(rec.valuesForMethod("tools/list", "Authorization")) > initialProbeCount
	}, 3*time.Second, 10*time.Millisecond)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, r := range rec.reqs {
		if r.method != "tools/call" {
			assert.NotContains(t, r.header.Get("Authorization"), "hook-probe-leak",
				"%s must carry static credential, not hook token", r.method)
		}
	}
}

type targetedHookPlugin struct {
	schemas.MCPPluginNoOpHooks
	targetTool string
	token      string
}

func (p *targetedHookPlugin) GetName() string { return "targetedHookPlugin" }
func (p *targetedHookPlugin) Cleanup() error  { return nil }

func (p *targetedHookPlugin) PreMCPHook(ctx *schemas.BifrostContext, req *schemas.BifrostMCPRequest) (*schemas.BifrostMCPRequest, *schemas.MCPPluginShortCircuit, error) {
	if req.GetToolName() == p.targetTool {
		ctx.SetValue(schemas.BifrostContextKeyMCPOutboundToken, p.token)
	}
	return req, nil, nil
}

// TestOutboundTokenSequentialTwoClientsScopedOnSameCtx proves token scoping across sequential calls on one ctx.
func TestOutboundTokenSequentialTwoClientsScopedOnSameCtx(t *testing.T) {
	t.Parallel()

	t.Run("hook scoping", func(t *testing.T) {
		ts, _ := buildAuthEchoServer(t)
		manager := setupMCPManager(t)
		hook := &targetedHookPlugin{targetTool: "client_a-echo", token: "tok-for-A"}
		bf, err := core.Init(context.Background(), schemas.BifrostConfig{
			Account:    &testAccount{},
			MCPPlugins: []schemas.MCPPlugin{hook},
			Logger:     core.NewDefaultLogger(schemas.LogLevelError),
		})
		require.NoError(t, err)
		bf.SetMCPManager(manager)
		removeClientsOnCleanup(t, manager)

		for _, name := range []string{"client_a", "client_b"} {
			require.NoError(t, manager.AddClient(context.Background(), &schemas.MCPClientConfig{
				ID:                     name + "-id",
				Name:                   name,
				ConnectionType:         schemas.MCPConnectionTypeHTTP,
				ConnectionString:       schemas.NewSecretVar(ts.URL),
				AuthType:               schemas.MCPAuthTypeHeaders,
				Headers:                map[string]schemas.SecretVar{"Authorization": *schemas.NewSecretVar(outboundStaticAuth)},
				ToolsToExecute:         []string{"*"},
				NeedsSessionStickiness: schemas.Ptr(true),
			}))
		}

		ctx := createTestContext()
		outA, errA := callOutboundEcho(bf, ctx, "client_a", "msgA")
		require.Nil(t, errA)
		assert.Equal(t, "msgA|Bearer tok-for-A", outA)

		outB, errB := callOutboundEcho(bf, ctx, "client_b", "msgB")
		require.Nil(t, errB)
		assert.Equal(t, "msgB|"+outboundStaticAuth, outB, "client_b must see static credential on shared ctx")
	})

	t.Run("provider scoping", func(t *testing.T) {
		ts, _ := buildAuthEchoServer(t)
		provider := funcTokenProvider(func(_ context.Context, serverName string) (string, error) {
			if serverName == "client_p_a" {
				return "prov-tok-A", nil
			}
			return "", nil
		})
		logger := newTestLogger(t)
		manager := mcp.NewMCPManager(context.Background(), schemas.MCPConfig{
			OutboundTokenProvider:  provider,
			PluginPipelineProvider: func() interface{} { return &noopPluginPipeline{} },
			ReleasePluginPipeline:  func(pipeline interface{}) {},
		}, nil, logger, nil)
		bf, err := core.Init(context.Background(), schemas.BifrostConfig{
			Account: &testAccount{},
			Logger:  core.NewDefaultLogger(schemas.LogLevelError),
		})
		require.NoError(t, err)
		bf.SetMCPManager(manager)
		removeClientsOnCleanup(t, manager)

		for _, name := range []string{"client_p_a", "client_p_b"} {
			require.NoError(t, manager.AddClient(context.Background(), &schemas.MCPClientConfig{
				ID:                     name + "-id",
				Name:                   name,
				ConnectionType:         schemas.MCPConnectionTypeHTTP,
				ConnectionString:       schemas.NewSecretVar(ts.URL),
				AuthType:               schemas.MCPAuthTypeHeaders,
				Headers:                map[string]schemas.SecretVar{"Authorization": *schemas.NewSecretVar(outboundStaticAuth)},
				ToolsToExecute:         []string{"*"},
				NeedsSessionStickiness: schemas.Ptr(true),
			}))
		}

		ctx := createTestContext()
		outA, errA := callOutboundEcho(bf, ctx, "client_p_a", "msgA")
		require.Nil(t, errA)
		assert.Equal(t, "msgA|Bearer prov-tok-A", outA)

		outB, errB := callOutboundEcho(bf, ctx, "client_p_b", "msgB")
		require.Nil(t, errB)
		assert.Equal(t, "msgB|"+outboundStaticAuth, outB, "client_p_b must see static credential")
	})
}

// TestOutboundTokenProviderEmptyFallsBackToContextKey proves empty provider falls back to context key.
func TestOutboundTokenProviderEmptyFallsBackToContextKey(t *testing.T) {
	t.Parallel()

	ts, rec := buildAuthEchoServer(t)
	provider := funcTokenProvider(func(context.Context, string) (string, error) { return "", nil })
	logger := newTestLogger(t)
	manager := mcp.NewMCPManager(context.Background(), schemas.MCPConfig{
		OutboundTokenProvider:  provider,
		PluginPipelineProvider: func() interface{} { return &noopPluginPipeline{} },
		ReleasePluginPipeline:  func(pipeline interface{}) {},
	}, nil, logger, nil)
	bf, err := core.Init(context.Background(), schemas.BifrostConfig{
		Account:    &testAccount{},
		MCPPlugins: []schemas.MCPPlugin{&outboundTokenHookPlugin{token: "fallback-hook-token"}},
		Logger:     core.NewDefaultLogger(schemas.LogLevelError),
	})
	require.NoError(t, err)
	bf.SetMCPManager(manager)
	removeClientsOnCleanup(t, manager)

	clientName := "outbound_fallback"
	require.NoError(t, manager.AddClient(context.Background(), &schemas.MCPClientConfig{
		ID:                     clientName + "-id",
		Name:                   clientName,
		ConnectionType:         schemas.MCPConnectionTypeHTTP,
		ConnectionString:       schemas.NewSecretVar(ts.URL),
		AuthType:               schemas.MCPAuthTypeHeaders,
		Headers:                map[string]schemas.SecretVar{"Authorization": *schemas.NewSecretVar(outboundStaticAuth)},
		ToolsToExecute:         []string{"*"},
		NeedsSessionStickiness: schemas.Ptr(true),
	}))

	out, bErr := callOutboundEcho(bf, createTestContext(), clientName, "hi")
	require.Nil(t, bErr)
	assert.Equal(t, "hi|Bearer fallback-hook-token", out)
	assert.Equal(t, []string{"Bearer fallback-hook-token"}, rec.valuesForMethod("tools/call", "Authorization"))
}

// TestOutboundTokenEphemeralDiscoverCarriesCallToken proves ephemeral dials carry call token on discovery.
func TestOutboundTokenEphemeralDiscoverCarriesCallToken(t *testing.T) {
	t.Parallel()

	ts, rec := buildAuthEchoServer(t)
	provider := funcTokenProvider(func(context.Context, string) (string, error) { return "ephemeral-tok", nil })
	clientName := "outbound_ephemeral"
	_, bf := setupOutboundTokenBifrostWithStickiness(t, provider, clientName, ts.URL, false)

	out, bErr := callOutboundEcho(bf, createTestContext(), clientName, "hello")
	require.Nil(t, bErr)
	assert.Equal(t, "hello|Bearer ephemeral-tok", out)

	// AddClient's admin discovery precedes the call and must keep the static credential.
	discoverAuth := rec.valuesForMethod("server/discover", "Authorization")
	require.Len(t, discoverAuth, 2, "admin discovery and the ephemeral dial must each send server/discover")
	assert.Equal(t, outboundStaticAuth, discoverAuth[0])
	assert.Equal(t, "Bearer ephemeral-tok", discoverAuth[1])

	callAuth := rec.valuesForMethod("tools/call", "Authorization")
	require.NotEmpty(t, callAuth)
	assert.Equal(t, "Bearer ephemeral-tok", callAuth[0])
}

// TestOutboundTokenStickyClient403SkipsReconnect proves 403 on per-call token suppresses reconnect.
func TestOutboundTokenStickyClient403SkipsReconnect(t *testing.T) {
	t.Parallel()

	type authKey struct{}
	s := server.NewMCPServer("test-403", "1.0.0", server.WithToolCapabilities(true))
	s.AddTool(mcpgo.NewTool("echo",
		mcpgo.WithDescription("Echo tool"),
		mcpgo.WithString("message", mcpgo.Required(), mcpgo.Description("message")),
	), func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		msg, _ := req.GetArguments()["message"].(string)
		auth, _ := ctx.Value(authKey{}).(string)
		return mcpgo.NewToolResultText(msg + "|" + auth), nil
	})

	streamable := server.NewStreamableHTTPServer(s, server.WithHTTPContextFunc(func(ctx context.Context, r *http.Request) context.Context {
		return context.WithValue(ctx, authKey{}, r.Header.Get("Authorization"))
	}))
	rec := &headerRecorder{}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := ""
		if r.Method == http.MethodPost && r.Body != nil {
			if body, err := io.ReadAll(r.Body); err == nil {
				var probe struct {
					Method string `json:"method"`
				}
				_ = json.Unmarshal(body, &probe)
				method = probe.Method
				r.Body = io.NopCloser(bytes.NewReader(body))
			}
		}
		rec.add(method, r.Header)
		if r.Header.Get("Authorization") == "Bearer reject-403" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		streamable.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)

	provider := funcTokenProvider(func(context.Context, string) (string, error) { return "reject-403", nil })
	clientName := "sticky_403"
	_, bf := setupOutboundTokenBifrostWithStickiness(t, provider, clientName, ts.URL, true)

	rec.mu.Lock()
	nBefore := len(rec.reqs)
	rec.mu.Unlock()

	_, bErr := callOutboundEcho(bf, createTestContext(), clientName, "fail")
	require.NotNil(t, bErr, "403 call must return error")

	// Recovery always redials first; a bare tools/list can come from the listen stream's refresh.
	require.Never(t, func() bool {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		for _, req := range rec.reqs[nBefore:] {
			if req.method == "server/discover" || req.method == "initialize" {
				return true
			}
		}
		return false
	}, time.Second, 50*time.Millisecond, "must not reconnect on per-call token 403")
}

func buildAuthEchoSSEServer(t *testing.T) (*httptest.Server, *headerRecorder) {
	t.Helper()

	s := server.NewMCPServer("test-sse-outbound", "1.0.0", server.WithToolCapabilities(true))
	s.AddTool(mcpgo.NewTool("echo",
		mcpgo.WithDescription("Echo tool"),
		mcpgo.WithString("message", mcpgo.Required(), mcpgo.Description("message")),
	), func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		msg, _ := req.GetArguments()["message"].(string)
		return mcpgo.NewToolResultText(msg), nil
	})

	rec := &headerRecorder{}
	var sseSrv *server.SSEServer

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := ""
		if r.Method == http.MethodPost && r.Body != nil {
			if body, err := io.ReadAll(r.Body); err == nil {
				var probe struct {
					Method string `json:"method"`
				}
				_ = json.Unmarshal(body, &probe)
				method = probe.Method
				r.Body = io.NopCloser(bytes.NewReader(body))
			}
		}
		rec.add(method, r.Header)
		sseSrv.ServeHTTP(w, r)
	})

	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	sseSrv = server.NewSSEServer(s, server.WithBaseURL(ts.URL))
	return ts, rec
}

// TestOutboundTokenSSEConcurrentCallsAreIsolated proves concurrent SSE calls isolate tokens.
func TestOutboundTokenSSEConcurrentCallsAreIsolated(t *testing.T) {
	t.Parallel()

	ts, rec := buildAuthEchoSSEServer(t)
	provider := funcTokenProvider(func(ctx context.Context, serverName string) (string, error) {
		tenant, _ := ctx.Value(outboundTenantKey{}).(string)
		return "sse-token-" + tenant, nil
	})
	logger := newTestLogger(t)
	manager := mcp.NewMCPManager(context.Background(), schemas.MCPConfig{
		OutboundTokenProvider:  provider,
		PluginPipelineProvider: func() interface{} { return &noopPluginPipeline{} },
		ReleasePluginPipeline:  func(pipeline interface{}) {},
	}, nil, logger, nil)
	bf, err := core.Init(context.Background(), schemas.BifrostConfig{
		Account: &testAccount{},
		Logger:  core.NewDefaultLogger(schemas.LogLevelError),
	})
	require.NoError(t, err)
	bf.SetMCPManager(manager)
	removeClientsOnCleanup(t, manager)

	clientName := "outbound_sse_conc"
	require.NoError(t, manager.AddClient(context.Background(), &schemas.MCPClientConfig{
		ID:               clientName + "-id",
		Name:             clientName,
		ConnectionType:   schemas.MCPConnectionTypeSSE,
		ConnectionString: schemas.NewSecretVar(ts.URL + "/sse"),
		AuthType:         schemas.MCPAuthTypeHeaders,
		Headers:          map[string]schemas.SecretVar{"Authorization": *schemas.NewSecretVar(outboundStaticAuth)},
		ToolsToExecute:   []string{"*"},
	}))

	tenants := []string{"alpha", "beta", "gamma", "delta"}
	errs := make([]*schemas.BifrostError, len(tenants))
	var wg sync.WaitGroup
	for i, tenant := range tenants {
		wg.Add(1)
		go func(idx int, tn string) {
			defer wg.Done()
			ctx := createTestContext()
			ctx.SetValue(outboundTenantKey{}, tn)
			_, errs[idx] = callOutboundEcho(bf, ctx, clientName, tn)
		}(i, tenant)
	}
	wg.Wait()

	for i, tenant := range tenants {
		require.Nil(t, errs[i], "SSE call for %s should succeed", tenant)
	}

	wire := rec.valuesForMethod("tools/call", "Authorization")
	require.Len(t, wire, len(tenants))
	for _, tenant := range tenants {
		assert.Contains(t, wire, "Bearer sse-token-"+tenant)
	}
	assert.NotContains(t, wire, outboundStaticAuth)
}

// TestOutboundTokenCodeModeNestedCallCarriesToken proves nested code-mode tool calls carry token.
func TestOutboundTokenCodeModeNestedCallCarriesToken(t *testing.T) {
	t.Parallel()

	ts, _ := buildAuthEchoServer(t)
	provider := funcTokenProvider(func(context.Context, string) (string, error) {
		return "nested-tok", nil
	})
	logger := newTestLogger(t)
	codeMode := starlark.NewStarlarkCodeMode(nil, logger)
	manager := mcp.NewMCPManager(context.Background(), schemas.MCPConfig{
		OutboundTokenProvider:  provider,
		PluginPipelineProvider: func() interface{} { return &noopPluginPipeline{} },
		ReleasePluginPipeline:  func(pipeline interface{}) {},
	}, nil, logger, codeMode)

	bf, err := core.Init(context.Background(), schemas.BifrostConfig{
		Account: &testAccount{},
		Logger:  core.NewDefaultLogger(schemas.LogLevelError),
	})
	require.NoError(t, err)
	bf.SetMCPManager(manager)
	removeClientsOnCleanup(t, manager)

	clientName := "codemode_target"
	require.NoError(t, manager.AddClient(context.Background(), &schemas.MCPClientConfig{
		ID:                     clientName + "-id",
		Name:                   clientName,
		ConnectionType:         schemas.MCPConnectionTypeHTTP,
		ConnectionString:       schemas.NewSecretVar(ts.URL),
		AuthType:               schemas.MCPAuthTypeHeaders,
		Headers:                map[string]schemas.SecretVar{"Authorization": *schemas.NewSecretVar(outboundStaticAuth)},
		IsCodeModeClient:       true,
		ToolsToExecute:         []string{"*"},
		NeedsSessionStickiness: schemas.Ptr(true),
	}))

	code := `result = codemode_target.echo(message="from-code")`
	toolCall := schemas.ChatAssistantMessageToolCall{
		ID: schemas.Ptr("call-code-outbound"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("executeToolCode"),
			Arguments: fmt.Sprintf(`{"code": %s}`, mustJSONString(code)),
		},
	}
	resp, bErr := bf.ExecuteChatMCPTool(createTestContext(), &toolCall)
	require.Nil(t, bErr)
	require.NotNil(t, resp)
	require.NotNil(t, resp.Content)
	require.NotNil(t, resp.Content.ContentStr)

	val, hasErr, errMsg := ParseCodeModeResponse(t, *resp.Content.ContentStr)
	require.False(t, hasErr, errMsg)
	assert.Equal(t, "from-code|Bearer nested-tok", val)
}

// nestedOnlyHookPlugin acts only on nested code-mode calls, never on the outer executeToolCode.
type nestedOnlyHookPlugin struct {
	schemas.MCPPluginNoOpHooks
	token  string
	tenant string
}

func (p *nestedOnlyHookPlugin) GetName() string { return "nestedOnlyHookPlugin" }
func (p *nestedOnlyHookPlugin) Cleanup() error  { return nil }

func (p *nestedOnlyHookPlugin) PreMCPHook(ctx *schemas.BifrostContext, req *schemas.BifrostMCPRequest) (*schemas.BifrostMCPRequest, *schemas.MCPPluginShortCircuit, error) {
	if req.GetToolName() == "executeToolCode" {
		return req, nil, nil
	}
	if p.token != "" {
		ctx.SetValue(schemas.BifrostContextKeyMCPOutboundToken, p.token)
	}
	if p.tenant != "" {
		ctx.SetValue(outboundTenantKey{}, p.tenant)
	}
	return req, nil, nil
}

// TestOutboundTokenCodeModeNestedResolvesAfterHooks proves a nested call resolves its token and
// dials only after its own PreMCPHook ran.
func TestOutboundTokenCodeModeNestedResolvesAfterHooks(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		provider schemas.OutboundMCPTokenProvider
		hook     *nestedOnlyHookPlugin
		want     string
	}{
		{
			name: "hook_token_on_per_call_dial",
			hook: &nestedOnlyHookPlugin{token: "hook-tok"},
			want: "Bearer hook-tok",
		},
		{
			name: "provider_sees_hook_value",
			provider: funcTokenProvider(func(ctx context.Context, _ string) (string, error) {
				tenant, _ := ctx.Value(outboundTenantKey{}).(string)
				if tenant == "" {
					return "", errors.New("tenant not set before token resolution")
				}
				return "prov-" + tenant, nil
			}),
			hook: &nestedOnlyHookPlugin{tenant: "acme"},
			want: "Bearer prov-acme",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts, rec := buildAuthEchoServer(t)
			logger := newTestLogger(t)
			manager := mcp.NewMCPManager(context.Background(), schemas.MCPConfig{
				OutboundTokenProvider:  tc.provider,
				PluginPipelineProvider: func() interface{} { return &noopPluginPipeline{} },
				ReleasePluginPipeline:  func(pipeline interface{}) {},
			}, nil, logger, starlark.NewStarlarkCodeMode(nil, logger))
			bf, err := core.Init(context.Background(), schemas.BifrostConfig{
				Account:    &testAccount{},
				MCPPlugins: []schemas.MCPPlugin{tc.hook},
				Logger:     core.NewDefaultLogger(schemas.LogLevelError),
			})
			require.NoError(t, err)
			bf.SetMCPManager(manager)
			removeClientsOnCleanup(t, manager)

			require.NoError(t, manager.AddClient(context.Background(), &schemas.MCPClientConfig{
				ID:               "codemode_target-id",
				Name:             "codemode_target",
				ConnectionType:   schemas.MCPConnectionTypeHTTP,
				ConnectionString: schemas.NewSecretVar(ts.URL),
				AuthType:         schemas.MCPAuthTypeHeaders,
				Headers:          map[string]schemas.SecretVar{"Authorization": *schemas.NewSecretVar(outboundStaticAuth)},
				IsCodeModeClient: true,
				ToolsToExecute:   []string{"*"},
			}))

			code := `result = codemode_target.echo(message="from-code")`
			resp, bErr := bf.ExecuteChatMCPTool(createTestContext(), &schemas.ChatAssistantMessageToolCall{
				ID: schemas.Ptr("call-code-hook"),
				Function: schemas.ChatAssistantMessageToolCallFunction{
					Name:      schemas.Ptr("executeToolCode"),
					Arguments: fmt.Sprintf(`{"code": %s}`, mustJSONString(code)),
				},
			})
			require.Nil(t, bErr)
			require.NotNil(t, resp.Content.ContentStr)
			val, hasErr, errMsg := ParseCodeModeResponse(t, *resp.Content.ContentStr)
			require.False(t, hasErr, errMsg)
			assert.Equal(t, "from-code|"+tc.want, val)

			discoverAuth := rec.valuesForMethod("server/discover", "Authorization")
			require.NotEmpty(t, discoverAuth)
			assert.Equal(t, tc.want, discoverAuth[len(discoverAuth)-1], "the nested per-call dial must carry the call's token")
		})
	}
}

// TestOutboundTokenCallIsNotRetried proves a token-bearing call is sent once on a transient
// failure, while a call without one keeps the usual retries.
func TestOutboundTokenCallIsNotRetried(t *testing.T) {
	t.Parallel()

	s := server.NewMCPServer("test-noretry", "1.0.0", server.WithToolCapabilities(true))
	s.AddTool(mcpgo.NewTool("echo",
		mcpgo.WithDescription("Echo tool"),
		mcpgo.WithString("message", mcpgo.Required(), mcpgo.Description("message")),
	), func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		return mcpgo.NewToolResultText("unreachable"), nil
	})
	streamable := server.NewStreamableHTTPServer(s)
	rec := &headerRecorder{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var probe struct {
			Method string `json:"method"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &probe)
		rec.add(probe.Method, r.Header)
		if probe.Method == "tools/call" {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		streamable.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)

	provider := funcTokenProvider(func(ctx context.Context, _ string) (string, error) {
		tenant, _ := ctx.Value(outboundTenantKey{}).(string)
		if tenant == "" {
			return "", nil
		}
		return "once-" + tenant, nil
	})
	clientName := "outbound_noretry"
	_, bf := setupOutboundTokenBifrostWithStickiness(t, provider, clientName, ts.URL, true)

	_, bErr := callOutboundEcho(bf, createTestContext(), clientName, "static")
	require.NotNil(t, bErr)
	assert.Len(t, rec.valuesForMethod("tools/call", "Authorization"), 1+mcp.ToolCallRetryConfig.MaxRetries,
		"a call without a token keeps the usual retries")

	ctx := createTestContext()
	ctx.SetValue(outboundTenantKey{}, "acme")
	_, bErr = callOutboundEcho(bf, ctx, clientName, "token")
	require.NotNil(t, bErr)
	calls := rec.valuesForMethod("tools/call", "Authorization")
	tokenCalls := 0
	for _, v := range calls {
		if v == "Bearer once-acme" {
			tokenCalls++
		}
	}
	assert.Equal(t, 1, tokenCalls, "a token-bearing call must be sent once")
}
