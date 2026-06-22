package mcptests

import (
	"context"
	"fmt"
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
// SHARED HELPERS
// =============================================================================

// buildInProcessServer creates a fresh mcp-go server with a deterministic set of
// tools and returns it. The server is independent of the bifrost-internal server
// so AddClient flows go through the full connect/list_tools gate.
func buildInProcessServer(t *testing.T) *server.MCPServer {
	t.Helper()
	s := server.NewMCPServer("test-inproc", "1.2.3", server.WithToolCapabilities(true))

	// Tool A — echo
	echoTool := mcpgo.NewTool("echo",
		mcpgo.WithDescription("Echo tool"),
		mcpgo.WithString("message", mcpgo.Required(), mcpgo.Description("message")),
	)
	s.AddTool(echoTool, func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		msg, _ := req.GetArguments()["message"].(string)
		return mcpgo.NewToolResultText(msg), nil
	})

	// Tool B — adder (separate tool so PostHook filtering tests can drop one and keep the other)
	addTool := mcpgo.NewTool("add",
		mcpgo.WithDescription("Adds two numbers"),
		mcpgo.WithNumber("x", mcpgo.Required(), mcpgo.Description("x")),
		mcpgo.WithNumber("y", mcpgo.Required(), mcpgo.Description("y")),
	)
	s.AddTool(addTool, func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		args := req.GetArguments()
		x, _ := args["x"].(float64)
		y, _ := args["y"].(float64)
		return mcpgo.NewToolResultText(fmt.Sprintf("%v", x+y)), nil
	})

	return s
}

// inProcessClientConfig builds a Client config wrapping the given server, ready
// for AddClient. ID embeds clientName for easy identification in test assertions.
func inProcessClientConfig(clientName string, s *server.MCPServer) *schemas.MCPClientConfig {
	return &schemas.MCPClientConfig{
		ID:              clientName + "-id",
		Name:            clientName,
		ConnectionType:  schemas.MCPConnectionTypeInProcess,
		InProcessServer: s,
		ToolsToExecute:  []string{"*"},
	}
}

// setupBifrostWithPlugins returns a manager + bifrost where the manager's plugin
// pipeline is wired to a Bifrost instance carrying the given MCP plugins. Plugins
// fire for any AddClient performed after this returns.
func setupBifrostWithPlugins(t *testing.T, plugins []schemas.MCPPlugin) (*mcp.MCPManager, *core.Bifrost) {
	t.Helper()
	manager := setupMCPManager(t)
	bf, err := core.Init(context.Background(), schemas.BifrostConfig{
		Account:    &testAccount{},
		MCPPlugins: plugins,
		Logger:     core.NewDefaultLogger(schemas.LogLevelError),
	})
	require.NoError(t, err)
	bf.SetMCPManager(manager)
	return manager, bf
}

// =============================================================================
// CONNECT HOOK TESTS
// =============================================================================

func TestConnectHook_FiresOnAddClient(t *testing.T) {
	t.Parallel()

	plugin := NewTestConnectPlugin()
	manager, _ := setupBifrostWithPlugins(t, []schemas.MCPPlugin{plugin})

	cfg := inProcessClientConfig("connect_fires", buildInProcessServer(t))
	require.NoError(t, manager.AddClient(context.Background(), cfg))

	pre := plugin.GetPreHookCalls()
	post := plugin.GetPostHookCalls()
	require.Len(t, pre, 1, "Connect PreHook should fire exactly once for AddClient")
	require.Len(t, post, 1, "Connect PostHook should fire exactly once for AddClient")

	// Verify PreHook saw the right typed sub-request.
	req := pre[0].ConnectRequest
	require.NotNil(t, req, "typed Connect captures land in ConnectRequest, not the envelope Request field")
	assert.Equal(t, "connect_fires", req.ClientName)
	assert.Equal(t, schemas.MCPConnectionTypeInProcess, req.ConnectionType)
}

func TestConnectHook_PostHookPopulatesServerInfo(t *testing.T) {
	t.Parallel()

	plugin := NewTestConnectPlugin()
	manager, _ := setupBifrostWithPlugins(t, []schemas.MCPPlugin{plugin})

	require.NoError(t, manager.AddClient(context.Background(), inProcessClientConfig("server_info", buildInProcessServer(t))))

	post := plugin.GetPostHookCalls()
	require.Len(t, post, 1)
	resp := post[0].ConnectResponse
	require.NotNil(t, resp, "typed Connect captures land in ConnectResponse")

	si := resp.ServerInfo
	require.NotNil(t, si, "ServerInfo must be populated from initialize handshake")
	assert.Equal(t, "test-inproc", si.Name)
	assert.Equal(t, "1.2.3", si.Version)
	assert.NotEmpty(t, resp.ProtocolVersion)
	require.NotNil(t, resp.ServerCapabilities)
	assert.True(t, resp.ServerCapabilities.Tools, "server advertises tool capability")

	// ExtraFields on the typed sub-response carries Latency + ClientName backfill.
	assert.Greater(t, resp.ExtraFields.Latency, int64(-1), "Latency should be non-negative")
	assert.Equal(t, "server_info", resp.ExtraFields.ClientName, "ClientName backfilled via PopulateExtraFields")

	// Typed plugins also see ClientName on the captured sub-request.
	pre := plugin.GetPreHookCalls()
	require.NotEmpty(t, pre)
	require.NotNil(t, pre[0].ConnectRequest)
	assert.Equal(t, "server_info", pre[0].ConnectRequest.ClientName)
}

func TestConnectHook_PreHookShortCircuitError_FailsAddClient(t *testing.T) {
	t.Parallel()

	plugin := NewTestConnectPlugin()
	plugin.SetShortCircuitError(&schemas.BifrostError{
		IsBifrostError: false,
		Error:          &schemas.ErrorField{Message: "blocked by governance"},
	})
	manager, _ := setupBifrostWithPlugins(t, []schemas.MCPPlugin{plugin})

	err := manager.AddClient(context.Background(), inProcessClientConfig("blocked_client", buildInProcessServer(t)))
	require.Error(t, err, "Plugin error short-circuit should fail AddClient")
	assert.Contains(t, err.Error(), "blocked by governance")

	// PostHook should still have fired for the executed PreHook plugins.
	assert.Len(t, plugin.GetPostHookCalls(), 0, "PostHook only fires on success or recovery; raw error short-circuit yields no response")
	assert.Len(t, plugin.GetPreHookCalls(), 1, "PreHook ran once before short-circuit")

	// Verify no client was registered.
	clients := manager.GetClients()
	for _, c := range clients {
		assert.NotEqual(t, "blocked_client", c.Name, "no client should be registered after error short-circuit")
	}
}

func TestConnectHook_PreHookShortCircuitResponse_RegistersWithEmptyTools(t *testing.T) {
	t.Parallel()

	plugin := NewTestConnectPlugin()
	plugin.SetShortCircuitResponse(&schemas.BifrostMCPConnectResponse{
		ServerInfo: &schemas.MCPServerInfo{Name: "synthetic_client", Version: "0.0.0"},
		ConnectionInfo: &schemas.MCPClientConnectionInfo{
			Type: schemas.MCPConnectionTypeInProcess,
		},
	})
	manager, _ := setupBifrostWithPlugins(t, []schemas.MCPPlugin{plugin})

	// AddClient should succeed (no wire dial happens) — documented Connect-success
	// short-circuit gotcha: client registered as connected with no live transport.
	require.NoError(t, manager.AddClient(context.Background(), inProcessClientConfig("synthetic_client", buildInProcessServer(t))))

	clients := manager.GetClients()
	var found *schemas.MCPClientState
	for i := range clients {
		if clients[i].Name == "synthetic_client" {
			found = &clients[i]
			break
		}
	}
	require.NotNil(t, found, "client should be registered even with synthetic connect")
	assert.Empty(t, found.ToolMap, "synthetic-connect client has no tools (list_tools is never called)")
}

func TestConnectHook_AuthorizationHidden_HeadersAuth(t *testing.T) {
	t.Parallel()

	// SECURITY: PreHook plugins must NOT see the Authorization header. The connect
	// gate strips Authorization from headers exposed to plugins and re-injects it
	// only after all PreHooks have run. Test via MCPAuthTypeHeaders so we don't need
	// a live OAuth provider.
	//
	// We short-circuit after capture so the test doesn't wait on the unreachable
	// transport retry loop. The strip happens before any PreHook runs, so capturing
	// the request once is sufficient to prove it.
	plugin := NewTestConnectPlugin()
	plugin.SetShortCircuitError(&schemas.BifrostError{
		IsBifrostError: false,
		Error:          &schemas.ErrorField{Message: "captured, aborting"},
	})

	manager, _ := setupBifrostWithPlugins(t, []schemas.MCPPlugin{plugin})

	url := *schemas.NewSecretVar("http://example.invalid")
	cfg := &schemas.MCPClientConfig{
		ID:               "auth_strip-id",
		Name:             "auth_strip",
		ConnectionType:   schemas.MCPConnectionTypeHTTP,
		ConnectionString: &url,
		AuthType:         schemas.MCPAuthTypeHeaders,
		Headers: map[string]schemas.SecretVar{
			"Authorization": *schemas.NewSecretVar("Bearer super-secret-token"),
			"X-Custom":      *schemas.NewSecretVar("plugin-visible"),
		},
		ToolsToExecute: []string{"*"},
	}
	// Short-circuit returns an error → AddClient fails. That's expected.
	_ = manager.AddClient(context.Background(), cfg)

	calls := plugin.GetPreHookCalls()
	require.NotEmpty(t, calls, "PreHook should have fired before short-circuit")

	req := calls[0].ConnectRequest
	require.NotNil(t, req)
	require.NotNil(t, req.Headers, "Headers should be populated (we set X-Custom)")

	// Authorization must NOT be present in the headers plugins see.
	_, hasAuth := req.Headers["Authorization"]
	assert.False(t, hasAuth, "Authorization header must be stripped before PreHooks run")
	// Verify the secret never appeared anywhere in the visible header values.
	for k, v := range req.Headers {
		assert.NotContains(t, v, "super-secret-token",
			"bearer token should not leak via any header (%q)", k)
	}
	// Other user-configured headers should still be visible.
	assert.Equal(t, "plugin-visible", req.Headers["X-Custom"],
		"Non-Authorization headers should remain visible to plugins")
}

func TestConnectHook_OnlyFiresForConnectRequestType(t *testing.T) {
	t.Parallel()

	// TestConnectPlugin filters by RequestType. Verify it ignores execute-tool flows.
	connectPlugin := NewTestConnectPlugin()
	logPlugin := NewTestLoggingPlugin() // captures everything for cross-reference
	manager, bf := setupBifrostWithPlugins(t, []schemas.MCPPlugin{connectPlugin, logPlugin})

	// Trigger execute-tool path (no connect involved on the bifrost-internal client).
	require.NoError(t, RegisterEchoTool(manager))
	echoCall := GetSampleEchoToolCall("filter_test", "hello")
	_, bifrostErr := bf.ExecuteChatMCPTool(createTestContext(), &echoCall)
	require.Nil(t, bifrostErr)

	// Connect plugin must not have fired.
	assert.Empty(t, connectPlugin.GetPreHookCalls(), "Connect plugin should ignore execute-tool requests")
	assert.Empty(t, connectPlugin.GetPostHookCalls(), "Connect plugin should ignore execute-tool responses")
	// Logging plugin captures the execute-tool flow (as a cross-check that the test ran).
	assert.GreaterOrEqual(t, logPlugin.GetPreHookCallCount(), 1)
}

// =============================================================================
// LISTTOOLS HOOK TESTS
// =============================================================================

func TestListToolsHook_FiresOnAddClient(t *testing.T) {
	t.Parallel()

	plugin := NewTestListToolsPlugin()
	manager, _ := setupBifrostWithPlugins(t, []schemas.MCPPlugin{plugin})

	require.NoError(t, manager.AddClient(context.Background(), inProcessClientConfig("list_fires", buildInProcessServer(t))))

	pre := plugin.GetPreHookCalls()
	post := plugin.GetPostHookCalls()
	require.Len(t, pre, 1, "ListTools PreHook should fire once during AddClient (post-init tool retrieval)")
	require.Len(t, post, 1, "ListTools PostHook should fire once")

	// Verify response shape.
	resp := post[0].Response
	require.NotNil(t, resp)
	require.NotNil(t, resp.BifrostMCPListToolsResponse)
	assert.Equal(t, 2, resp.RawToolCount, "raw count should reflect both tools (echo + add)")
	assert.Len(t, resp.Tools, 2, "filtered tools should match (no name violations in this set)")
	// Tools are prefixed with client name.
	_, hasEcho := resp.Tools["list_fires-echo"]
	_, hasAdd := resp.Tools["list_fires-add"]
	assert.True(t, hasEcho, "echo tool should be present with client prefix")
	assert.True(t, hasAdd, "add tool should be present with client prefix")
}

func TestListToolsHook_PostHookFilterAppliedToClientState(t *testing.T) {
	t.Parallel()

	plugin := NewTestListToolsPlugin()
	// Drop the "add" tool; keep "echo".
	plugin.SetPostHookFilter(func(prefixedName string) bool {
		return prefixedName == "list_filter-echo"
	})

	manager, _ := setupBifrostWithPlugins(t, []schemas.MCPPlugin{plugin})
	require.NoError(t, manager.AddClient(context.Background(), inProcessClientConfig("list_filter", buildInProcessServer(t))))

	// Verify the filtered set landed in the manager's stored ToolMap (not just the
	// gate response). The connect path applies the gate result to clientState.ToolMap.
	clients := manager.GetClients()
	var target *schemas.MCPClientState
	for i := range clients {
		if clients[i].Name == "list_filter" {
			target = &clients[i]
			break
		}
	}
	require.NotNil(t, target)
	assert.Len(t, target.ToolMap, 1, "PostHook filter should have removed 'add' tool")
	_, hasEcho := target.ToolMap["list_filter-echo"]
	_, hasAdd := target.ToolMap["list_filter-add"]
	assert.True(t, hasEcho, "filtered ToolMap should keep echo")
	assert.False(t, hasAdd, "filtered ToolMap should drop add")
}

func TestListToolsHook_PreHookShortCircuitWithSyntheticTools(t *testing.T) {
	t.Parallel()

	plugin := NewTestListToolsPlugin()
	synthetic := &schemas.BifrostMCPListToolsResponse{
		Tools: map[string]schemas.ChatTool{
			"synthetic-tool": {
				Type: schemas.ChatToolTypeFunction,
				Function: &schemas.ChatToolFunction{
					Name:        "synthetic-tool",
					Description: schemas.Ptr("Plugin-injected tool"),
				},
			},
		},
		ToolNameMapping: map[string]string{"synthetic_tool": "synthetic-tool"},
	}
	plugin.SetShortCircuitResponse(synthetic)

	manager, _ := setupBifrostWithPlugins(t, []schemas.MCPPlugin{plugin})
	require.NoError(t, manager.AddClient(context.Background(), inProcessClientConfig("list_synth", buildInProcessServer(t))))

	clients := manager.GetClients()
	var target *schemas.MCPClientState
	for i := range clients {
		if clients[i].Name == "list_synth" {
			target = &clients[i]
			break
		}
	}
	require.NotNil(t, target)
	// The synthetic tool list should have replaced the real server's tools.
	_, hasSynthetic := target.ToolMap["synthetic-tool"]
	_, hasEcho := target.ToolMap["list_synth-echo"]
	assert.True(t, hasSynthetic, "synthetic tool should be in ToolMap from short-circuit")
	assert.False(t, hasEcho, "real server tools should not appear when PreHook short-circuited")
}

func TestListToolsHook_PreHookShortCircuitError_LeavesEmptyToolMap(t *testing.T) {
	t.Parallel()

	plugin := NewTestListToolsPlugin()
	plugin.SetShortCircuitError(&schemas.BifrostError{
		IsBifrostError: false,
		Error:          &schemas.ErrorField{Message: "list_tools blocked"},
	})

	manager, _ := setupBifrostWithPlugins(t, []schemas.MCPPlugin{plugin})
	// AddClient should still succeed — the connect path tolerates list_tools failure
	// and falls back to empty tools (matching pre-plugin behavior).
	require.NoError(t, manager.AddClient(context.Background(), inProcessClientConfig("list_err", buildInProcessServer(t))))

	clients := manager.GetClients()
	var target *schemas.MCPClientState
	for i := range clients {
		if clients[i].Name == "list_err" {
			target = &clients[i]
			break
		}
	}
	require.NotNil(t, target)
	assert.Empty(t, target.ToolMap, "list_tools error short-circuit should result in empty ToolMap")
}

func TestListToolsHook_FiresOnConnectAndAgain(t *testing.T) {
	t.Parallel()

	// Verifies that re-establishing a connection re-fires the list_tools gate.
	plugin := NewTestListToolsPlugin()
	manager, _ := setupBifrostWithPlugins(t, []schemas.MCPPlugin{plugin})

	cfg := inProcessClientConfig("list_reconnect", buildInProcessServer(t))
	require.NoError(t, manager.AddClient(context.Background(), cfg))
	require.Len(t, plugin.GetPreHookCalls(), 1, "first AddClient should fire list_tools once")

	// Reconnect: this tears down and re-establishes the client, firing list_tools again.
	require.NoError(t, manager.ReconnectClient(cfg.ID))
	require.GreaterOrEqual(t, len(plugin.GetPreHookCalls()), 2, "ReconnectClient should re-fire list_tools")
}

// =============================================================================
// PING HOOK TESTS
// =============================================================================

func TestPingHook_FiresViaHealthMonitor(t *testing.T) {
	t.Parallel()

	plugin := NewTestPingPlugin()
	manager, _ := setupBifrostWithPlugins(t, []schemas.MCPPlugin{plugin})

	cfg := inProcessClientConfig("ping_fires", buildInProcessServer(t))
	require.NoError(t, manager.AddClient(context.Background(), cfg))

	// AddClient starts its own health monitor at 10s interval — far too slow for
	// tests. Spin up a dedicated monitor at 10ms instead.
	monitor := mcp.NewClientHealthMonitor(manager, cfg.ID, 10*time.Millisecond, true, core.NewDefaultLogger(schemas.LogLevelError))
	monitor.Start()
	defer monitor.Stop()

	// Wait for at least a couple of complete tick cycles (both PreHook and PostHook).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if plugin.GetPreHookCallCount() >= 2 && plugin.GetPostHookCallCount() >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	preCount := plugin.GetPreHookCallCount()
	postCount := plugin.GetPostHookCallCount()
	require.GreaterOrEqual(t, preCount, 2, "Ping PreHook should fire on each health-check tick")
	require.GreaterOrEqual(t, postCount, 2, "Ping PostHook should fire on each successful ping")

	// Verify request shape.
	pre := plugin.GetPreHookCalls()
	for _, e := range pre {
		assert.Equal(t, schemas.MCPRequestTypePing, e.Request.RequestType)
		assert.Equal(t, "ping_fires", e.Request.ClientName)
	}
}

func TestPingHook_PreHookShortCircuitHealthy(t *testing.T) {
	t.Parallel()

	plugin := NewTestPingPlugin()
	plugin.SetShortCircuitHealthy(true)
	manager, _ := setupBifrostWithPlugins(t, []schemas.MCPPlugin{plugin})

	cfg := inProcessClientConfig("ping_healthy", buildInProcessServer(t))
	require.NoError(t, manager.AddClient(context.Background(), cfg))

	monitor := mcp.NewClientHealthMonitor(manager, cfg.ID, 10*time.Millisecond, true, core.NewDefaultLogger(schemas.LogLevelError))
	monitor.Start()
	defer monitor.Stop()

	// Wait for at least two complete cycles (both pre and post).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if plugin.GetPreHookCallCount() >= 2 && plugin.GetPostHookCallCount() >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	require.GreaterOrEqual(t, plugin.GetPreHookCallCount(), 2, "PreHook fires even when short-circuiting")
	// Short-circuited healthy → PostHook still runs with the synthetic response.
	require.GreaterOrEqual(t, plugin.GetPostHookCallCount(), 2, "PostHook fires for short-circuit success path")
}

func TestPingHook_PreHookShortCircuitError_DoesNotPanic(t *testing.T) {
	t.Parallel()

	// Short-circuiting with error is treated by the health monitor as a normal
	// ping failure. We verify the gate plumbing doesn't blow up and the plugin
	// got invoked.
	plugin := NewTestPingPlugin()
	plugin.SetShortCircuitError(&schemas.BifrostError{
		IsBifrostError: false,
		Error:          &schemas.ErrorField{Message: "synthetic ping failure"},
	})

	manager, _ := setupBifrostWithPlugins(t, []schemas.MCPPlugin{plugin})
	cfg := inProcessClientConfig("ping_err", buildInProcessServer(t))
	require.NoError(t, manager.AddClient(context.Background(), cfg))

	monitor := mcp.NewClientHealthMonitor(manager, cfg.ID, 10*time.Millisecond, true, core.NewDefaultLogger(schemas.LogLevelError))
	monitor.Start()
	defer monitor.Stop()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if plugin.GetPreHookCallCount() >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.GreaterOrEqual(t, plugin.GetPreHookCallCount(), 2, "ping plugin fires regardless of short-circuit outcome")
	// PostHook should NOT have captured anything because our plugin filters out
	// non-healthy responses, and short-circuit-error skips the success path.
	assert.Equal(t, 0, plugin.GetPostHookCallCount(), "no healthy ping response captured under error short-circuit")
}

func TestPingHook_DoesNotFireWhenPingUnavailable(t *testing.T) {
	t.Parallel()

	// When isPingAvailable=false, health monitor falls back to list_tools as the
	// liveness probe. Ping hook should NEVER fire; list_tools hook fires instead.
	pingPlugin := NewTestPingPlugin()
	listPlugin := NewTestListToolsPlugin()
	manager, _ := setupBifrostWithPlugins(t, []schemas.MCPPlugin{pingPlugin, listPlugin})

	cfg := inProcessClientConfig("ping_unavailable", buildInProcessServer(t))
	require.NoError(t, manager.AddClient(context.Background(), cfg))

	// Reset the list-tools plugin so we ignore the AddClient-time invocation.
	listPlugin.Reset()

	monitor := mcp.NewClientHealthMonitor(manager, cfg.ID, 10*time.Millisecond, false, core.NewDefaultLogger(schemas.LogLevelError))
	monitor.Start()
	defer monitor.Stop()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if listPlugin.GetPreHookCallCount() >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	assert.Equal(t, 0, pingPlugin.GetPreHookCallCount(), "Ping hook MUST NOT fire when isPingAvailable=false")
	assert.GreaterOrEqual(t, listPlugin.GetPreHookCallCount(), 2, "list_tools hook fires as liveness fallback")
}

// =============================================================================
// CROSS-CUTTING TESTS
// =============================================================================

func TestMCPGate_AllRequestTypesCarryClientName(t *testing.T) {
	t.Parallel()

	// Verify ClientName is populated on requests handed to PreMCPHook for all
	// three op types. Use the generic logging plugin so we see them all.
	logPlugin := NewTestLoggingPlugin()
	manager, _ := setupBifrostWithPlugins(t, []schemas.MCPPlugin{logPlugin})

	require.NoError(t, manager.AddClient(context.Background(), inProcessClientConfig("client_name_test", buildInProcessServer(t))))

	// Force a list_tools via reconnect to make sure we see at least one of each kind.
	require.NoError(t, manager.ReconnectClient("client_name_test-id"))

	calls := logPlugin.GetPreHookCalls()
	sawConnect := false
	sawListTools := false
	for _, c := range calls {
		if c.ConnectRequest != nil {
			// Typed Connect capture
			assert.Equal(t, "client_name_test", c.ConnectRequest.ClientName,
				"ClientName must be set on Connect sub-request")
			sawConnect = true
			continue
		}
		// Envelope capture (Ping / ListTools / ExecuteTool)
		require.NotNil(t, c.Request)
		assert.Equal(t, "client_name_test", c.Request.ClientName,
			"ClientName must be set for %s requests", c.Request.RequestType)
		if c.Request.RequestType == schemas.MCPRequestTypeListTools {
			sawListTools = true
		}
	}
	assert.True(t, sawConnect, "should have seen Connect requests via typed pipeline")
	assert.True(t, sawListTools, "should have seen ListTools requests via envelope pipeline")
}

func TestMCPGate_NoPluginsConfigured_OpStillRuns(t *testing.T) {
	t.Parallel()

	// Even with no MCP plugins, the gate must transparently pass through.
	manager, _ := setupBifrostWithPlugins(t, []schemas.MCPPlugin{})

	require.NoError(t, manager.AddClient(context.Background(), inProcessClientConfig("no_plugins", buildInProcessServer(t))))

	clients := manager.GetClients()
	var found bool
	for _, c := range clients {
		if c.Name == "no_plugins" {
			found = true
			assert.NotEmpty(t, c.ToolMap, "tools should have been discovered through the gate even without plugins")
			break
		}
	}
	assert.True(t, found, "client should be registered")
}
