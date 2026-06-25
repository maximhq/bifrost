package mcptests

import (
	"bytes"
	"context"
	"encoding/json"
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
func buildRecordingHTTPServer(t *testing.T) (*httptest.Server, *headerRecorder) {
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

	streamable := server.NewStreamableHTTPServer(s)
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
func buildRecordingSSEServer(t *testing.T) (*httptest.Server, *headerRecorder) {
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

	ts, rec := buildRecordingHTTPServer(t)
	manager, _ := setupBifrostWithPlugins(t, []schemas.MCPPlugin{&extraHeaderInjectPlugin{}})

	cfg := httpExtraHeaderConfig("hc_ping", ts.URL)
	require.NoError(t, manager.AddClient(context.Background(), cfg))

	// Drive fast health-check pings (the AddClient-owned monitor ticks far too
	// slowly for a test).
	monitor := mcp.NewClientHealthMonitor(manager, cfg.ID, 10*time.Millisecond, true, core.NewDefaultLogger(schemas.LogLevelError))
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
	monitor := mcp.NewClientHealthMonitor(manager, cfg.ID, 10*time.Millisecond, false, core.NewDefaultLogger(schemas.LogLevelError))
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

	ts, rec := buildRecordingSSEServer(t)
	manager, _ := setupBifrostWithPlugins(t, []schemas.MCPPlugin{&extraHeaderInjectPlugin{}})

	cfg := sseExtraHeaderConfig("sse_hc_ping", ts.URL)
	require.NoError(t, manager.AddClient(context.Background(), cfg))

	monitor := mcp.NewClientHealthMonitor(manager, cfg.ID, 10*time.Millisecond, true, core.NewDefaultLogger(schemas.LogLevelError))
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

	ts, rec := buildRecordingSSEServer(t)
	manager, _ := setupBifrostWithPlugins(t, []schemas.MCPPlugin{&extraHeaderInjectPlugin{}})

	cfg := sseExtraHeaderConfig("sse_hc_list", ts.URL)
	require.NoError(t, manager.AddClient(context.Background(), cfg))

	monitor := mcp.NewClientHealthMonitor(manager, cfg.ID, 10*time.Millisecond, false, core.NewDefaultLogger(schemas.LogLevelError))
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

	ts, rec := buildRecordingSSEServer(t)
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
