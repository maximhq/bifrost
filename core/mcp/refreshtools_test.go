package mcp

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// refreshToolMapKeys snapshots the prefixed tool names currently cached on a
// client, so a test can watch the manager's view of an upstream tool set
// without reaching into the connection.
func refreshToolMapKeys(m *MCPManager, id string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cs, ok := m.clientMap[id]
	if !ok || cs.ToolMap == nil {
		return nil
	}
	keys := make([]string, 0, len(cs.ToolMap))
	for k := range cs.ToolMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// buildRefreshMCPServer starts a streamable-HTTP MCP server serving a single
// "echo" tool. The returned addTool adds another one after the fact, standing
// in for an upstream server whose tool set changed while Bifrost was connected.
func buildRefreshMCPServer(t *testing.T) (ts *httptest.Server, addTool func(name, description string)) {
	t.Helper()

	s := server.NewMCPServer("refresh-tools", "1.0.0", server.WithToolCapabilities(true))
	echoTool := mcpgo.NewTool("echo",
		mcpgo.WithDescription("Echo tool"),
		mcpgo.WithString("message", mcpgo.Required(), mcpgo.Description("message")),
	)
	s.AddTool(echoTool, func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		msg, _ := req.GetArguments()["message"].(string)
		return mcpgo.NewToolResultText(msg), nil
	})

	streamable := server.NewStreamableHTTPServer(s)
	ts = httptest.NewServer(http.HandlerFunc(streamable.ServeHTTP))
	t.Cleanup(func() {
		// A listening streamable-HTTP transport holds a GET open, and
		// httptest.Server.Close waits for outstanding requests.
		ts.CloseClientConnections()
		ts.Close()
	})

	return ts, func(name, description string) {
		s.AddTool(
			mcpgo.NewTool(name, mcpgo.WithDescription(description)),
			func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
				return mcpgo.NewToolResultText("ok"), nil
			},
		)
	}
}

// newRefreshConfig builds an http client config. Leaving stickiness unset makes
// it per-call (the default for newly created clients); setting it true gives it
// a persistent connection.
func newRefreshConfig(id, name, url string, sticky bool) *schemas.MCPClientConfig {
	config := &schemas.MCPClientConfig{
		ID:               id,
		Name:             name,
		ConnectionType:   schemas.MCPConnectionTypeHTTP,
		AuthType:         schemas.MCPAuthTypeNone,
		ConnectionString: schemas.NewSecretVar(url),
		ToolsToExecute:   []string{"*"},
	}
	if sticky {
		config.NeedsSessionStickiness = schemas.Ptr(true)
	}
	return config
}

// TestRefreshClientTools_PerCallClient_RediscoversOnDemand is the regression
// test for #6885's third gap: a per-call client — which covers every per-user
// auth type as well as any shared http client running with
// needs_session_stickiness nil/false — had no way at all to pick up an upstream
// tool-set change short of the periodic checker's tick (up to 10 minutes) or a
// gateway restart. ReconnectClient does not apply to these clients by design:
// there is no persistent connection to re-establish.
func TestRefreshClientTools_PerCallClient_RediscoversOnDemand(t *testing.T) {
	ts, addTool := buildRefreshMCPServer(t)

	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	t.Cleanup(func() { _ = m.Cleanup() })

	config := newRefreshConfig("refresh-percall", "percall", ts.URL, false)
	require.NoError(t, m.AddClient(context.Background(), config))
	require.True(t, m.RequiresPerCallConnection(config),
		"precondition: this client must be per-call for the test to exercise the gap")
	require.Equal(t, []string{"percall-echo"}, refreshToolMapKeys(m, config.ID),
		"precondition: the initial tool must be cached after AddClient")

	addTool("ping", "Ping tool added after the client was registered")

	count, err := m.RefreshClientTools(context.Background(), config.ID)
	require.NoError(t, err)
	require.Equal(t, 2, count, "refresh should report the freshly discovered tool count")
	require.Equal(t, []string{"percall-echo", "percall-ping"}, refreshToolMapKeys(m, config.ID),
		"refresh should have picked up the tool added upstream")
}

// TestRefreshClientTools_StickyClient_RelistsOverLiveConnection covers the
// sticky branch: the client already holds a live connection, so the refresh is
// a plain tools/list over it rather than an ephemeral connect-discover-close.
func TestRefreshClientTools_StickyClient_RelistsOverLiveConnection(t *testing.T) {
	ts, addTool := buildRefreshMCPServer(t)

	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	t.Cleanup(func() { _ = m.Cleanup() })

	config := newRefreshConfig("refresh-sticky", "sticky", ts.URL, true)
	require.NoError(t, m.AddClient(context.Background(), config))
	require.False(t, m.RequiresPerCallConnection(config),
		"precondition: this client must be sticky for the test to exercise the live-connection branch")
	require.Equal(t, []string{"sticky-echo"}, refreshToolMapKeys(m, config.ID),
		"precondition: the initial tool must be cached after AddClient")

	addTool("ping", "Ping tool added after the client already connected")

	count, err := m.RefreshClientTools(context.Background(), config.ID)
	require.NoError(t, err)
	require.Equal(t, 2, count, "refresh should report the freshly discovered tool count")
	require.Equal(t, []string{"sticky-echo", "sticky-ping"}, refreshToolMapKeys(m, config.ID),
		"refresh should have picked up the tool added upstream")
}

// TestRefreshClientTools_FiresToolsChangeCallback pins the seam the transport
// layer persists through: a refresh that genuinely changes the tool set must
// reach the tools-change callback, which is what writes the new set to the DB
// and re-syncs the hosted /mcp surface. Gated on real change, exactly like
// every other discovery path.
func TestRefreshClientTools_FiresToolsChangeCallback(t *testing.T) {
	// Both discovery branches, because they reach the callback by different
	// routes: the per-call one through writeBackDiscoveredTools after an
	// ephemeral discovery, the sticky one through the same write-back after a
	// tools/list over the live connection.
	for _, sticky := range []bool{false, true} {
		name := "per_call"
		if sticky {
			name = "sticky"
		}
		t.Run(name, func(t *testing.T) {
			ts, addTool := buildRefreshMCPServer(t)

			m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
			t.Cleanup(func() { _ = m.Cleanup() })

			fired := make(chan map[string]schemas.ChatTool, 4)
			m.SetToolsChangeCallback(func(_, _ string, tools map[string]schemas.ChatTool, _ map[string]string) {
				fired <- tools
			})

			config := newRefreshConfig("refresh-callback-"+name, "cb", ts.URL, sticky)
			require.NoError(t, m.AddClient(context.Background(), config))
			drainToolsChange(fired)

			// A refresh that rediscovers the identical set is not a change,
			// so it must not churn the DB or the hosted /mcp surface.
			_, err := m.RefreshClientTools(context.Background(), config.ID)
			require.NoError(t, err)
			require.Empty(t, fired, "an unchanged rediscovery must not fire the tools-change callback")

			addTool("ping", "Ping tool added after the client was registered")
			_, err = m.RefreshClientTools(context.Background(), config.ID)
			require.NoError(t, err)

			require.Len(t, fired, 1, "a genuine tool-set change must fire the tools-change callback exactly once")
			require.Contains(t, <-fired, "cb-ping")
		})
	}
}

// TestRefreshClientTools_UnknownClient_Errors keeps the handler's 404 mapping honest.
func TestRefreshClientTools_UnknownClient_Errors(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	t.Cleanup(func() { _ = m.Cleanup() })

	_, err := m.RefreshClientTools(context.Background(), "no-such-client")
	require.ErrorIs(t, err, schemas.ErrMCPClientNotFound)
}

// drainToolsChange empties any callback firings from client setup so a test
// only observes what its own refresh produced.
func drainToolsChange(ch chan map[string]schemas.ChatTool) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

// TestRefreshClientTools_AwaitingAdminVerification_Refuses covers the case an
// on-demand refresh must not quietly resolve. A client parked in
// pending_verification is waiting on a human, and for token_exchange the
// refresh would not merely fail — it would succeed, because that auth type
// resolves its own client-credentials token with no admin involved. Since
// awaitsAdminVerification reads DiscoveredTools == nil as the pending signal
// there, persisting a discovered set would promote the client out of
// pending_verification permanently and drop the Verify CTA with it.
func TestRefreshClientTools_AwaitingAdminVerification_Refuses(t *testing.T) {
	cases := []struct {
		name     string
		authType schemas.MCPAuthType
		pending  func(*schemas.MCPClientConfig)
	}{
		{
			name:     "oauth_with_unauthorized_inline_config",
			authType: schemas.MCPAuthTypeOauth,
			pending:  func(c *schemas.MCPClientConfig) { c.PendingOAuthConfig = &schemas.OAuth2Config{} },
		},
		{
			name:     "per_user_oauth_with_unauthorized_inline_config",
			authType: schemas.MCPAuthTypePerUserOauth,
			pending:  func(c *schemas.MCPClientConfig) { c.PendingOAuthConfig = &schemas.OAuth2Config{} },
		},
		{
			name:     "token_exchange_never_verified",
			authType: schemas.MCPAuthTypeTokenExchange,
			pending:  func(c *schemas.MCPClientConfig) { c.DiscoveredTools = nil },
		},
		{
			name:     "per_user_headers_never_verified",
			authType: schemas.MCPAuthTypePerUserHeaders,
			pending:  func(c *schemas.MCPClientConfig) { c.DiscoveredTools = nil },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
			t.Cleanup(func() { m.checkerManager.StopAll() })

			config := &schemas.MCPClientConfig{
				ID:               "refresh-pending",
				Name:             "pending",
				AuthType:         tc.authType,
				ConnectionType:   schemas.MCPConnectionTypeHTTP,
				ConnectionString: schemas.NewSecretVar("http://127.0.0.1:0/mcp"),
			}
			tc.pending(config)
			require.True(t, awaitsAdminVerification(config),
				"precondition: this config must be one AddClient parks in pending_verification")

			m.mu.Lock()
			m.clientMap[config.ID] = &schemas.MCPClientState{
				Name:            config.Name,
				ExecutionConfig: config,
				State:           schemas.MCPConnectionStatePendingVerification,
				ToolMap:         map[string]schemas.ChatTool{},
				ToolNameMapping: map[string]string{},
				ConnectionInfo:  &schemas.MCPClientConnectionInfo{Type: config.ConnectionType},
			}
			m.mu.Unlock()

			_, err := m.RefreshClientTools(context.Background(), config.ID)
			require.ErrorIs(t, err, schemas.ErrMCPRefreshNotApplicable)

			m.mu.RLock()
			state := m.clientMap[config.ID].State
			tools := len(m.clientMap[config.ID].ToolMap)
			m.mu.RUnlock()
			assert.Equal(t, schemas.MCPConnectionStatePendingVerification, state,
				"a refused refresh must leave the client awaiting its admin")
			assert.Zero(t, tools, "nothing may be discovered onto a client still awaiting verification")
		})
	}
}

// TestRefreshClientTools_DisabledAndNeedsReauth_Refuse pins the other two
// states where discovery is meaningless, and that both are reported as the
// same not-applicable class so the handler can map them to 400 rather than
// surfacing them as discovery failures.
func TestRefreshClientTools_DisabledAndNeedsReauth_Refuse(t *testing.T) {
	for _, state := range []schemas.MCPConnectionState{
		schemas.MCPConnectionStateDisabled,
		schemas.MCPConnectionStateNeedsReauth,
	} {
		t.Run(string(state), func(t *testing.T) {
			m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
			t.Cleanup(func() { m.checkerManager.StopAll() })

			config := newRefreshConfig("refresh-"+string(state), "svc", "http://127.0.0.1:0/mcp", false)
			m.mu.Lock()
			m.clientMap[config.ID] = &schemas.MCPClientState{
				Name:            config.Name,
				ExecutionConfig: config,
				State:           state,
				ToolMap:         map[string]schemas.ChatTool{},
				ToolNameMapping: map[string]string{},
				ConnectionInfo:  &schemas.MCPClientConnectionInfo{Type: config.ConnectionType},
			}
			m.mu.Unlock()

			_, err := m.RefreshClientTools(context.Background(), config.ID)
			require.ErrorIs(t, err, schemas.ErrMCPRefreshNotApplicable)
		})
	}
}

// buildRefreshMCPServerWithHook is buildRefreshMCPServer plus a hook fired on
// every inbound tools/list, which lets a test mutate manager state at the exact
// moment a discovery is in flight.
func buildRefreshMCPServerWithHook(t *testing.T, onToolsList func()) (ts *httptest.Server, addTool func(name, description string)) {
	t.Helper()

	s := server.NewMCPServer("refresh-tools", "1.0.0", server.WithToolCapabilities(true))
	echoTool := mcpgo.NewTool("echo",
		mcpgo.WithDescription("Echo tool"),
		mcpgo.WithString("message", mcpgo.Required(), mcpgo.Description("message")),
	)
	s.AddTool(echoTool, func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		msg, _ := req.GetArguments()["message"].(string)
		return mcpgo.NewToolResultText(msg), nil
	})

	streamable := server.NewStreamableHTTPServer(s)
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && onToolsList != nil {
			body, err := io.ReadAll(r.Body)
			if err == nil {
				r.Body = io.NopCloser(bytes.NewReader(body))
				if bytes.Contains(body, []byte(`"tools/list"`)) {
					onToolsList()
				}
			}
		}
		streamable.ServeHTTP(w, r)
	}))
	t.Cleanup(func() {
		ts.CloseClientConnections()
		ts.Close()
	})

	return ts, func(name, description string) {
		s.AddTool(
			mcpgo.NewTool(name, mcpgo.WithDescription(description)),
			func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
				return mcpgo.NewToolResultText("ok"), nil
			},
		)
	}
}

// TestRefreshClientTools_StaleWriteBack_ReportsInstalledCount pins tool_count to
// what the client actually serves. writeBackDiscoveredTools drops a discovery
// whose ConnGeneration no longer matches — a reconnect swapped the connection
// while the tools/list was in flight — and leaves the existing ToolMap in place.
// Reporting the discarded discovery's length would then describe a tool set the
// client is not serving, contradicting the endpoint's documented contract.
func TestRefreshClientTools_StaleWriteBack_ReportsInstalledCount(t *testing.T) {
	var m *MCPManager
	const clientID = "refresh-stale-writeback"

	// Bump the generation from inside the in-flight tools/list, which is
	// exactly the window writeBackDiscoveredTools guards against.
	bumped := false
	ts, addTool := buildRefreshMCPServerWithHook(t, func() {
		if m == nil || bumped {
			return
		}
		m.mu.Lock()
		if cs, ok := m.clientMap[clientID]; ok {
			cs.ConnGeneration++
			bumped = true
		}
		m.mu.Unlock()
	})

	m = NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	t.Cleanup(func() { _ = m.Cleanup() })

	config := newRefreshConfig(clientID, "stale", ts.URL, false)
	require.NoError(t, m.AddClient(context.Background(), config))
	require.Equal(t, []string{"stale-echo"}, refreshToolMapKeys(m, clientID),
		"precondition: the initial tool must be cached after AddClient")

	// The upstream now serves two tools, so a write-back that landed would
	// install 2 and a discarded one leaves the original 1 in place.
	addTool("ping", "Ping tool added after the client was registered")
	bumped = false

	count, err := m.RefreshClientTools(context.Background(), clientID)
	require.NoError(t, err)
	require.True(t, bumped, "precondition: the generation must have been bumped mid-discovery")

	installed := refreshToolMapKeys(m, clientID)
	require.Equal(t, []string{"stale-echo"}, installed,
		"precondition: a stale write-back must leave the existing tool map untouched")
	assert.Equal(t, len(installed), count,
		"tool_count must describe the tools the client actually serves, not a discarded discovery")
}
