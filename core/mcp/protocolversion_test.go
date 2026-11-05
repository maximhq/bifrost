package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveMaxProtocolVersion(t *testing.T) {
	for _, tc := range []struct {
		name, client, global, want string
	}{
		{"nothing set", "", "", ""},
		{"global auto", "", "auto", ""},
		{"global cap", "", "2025-06-18", "2025-06-18"},
		{"client cap wins", "2025-11-25", "2025-06-18", "2025-11-25"},
		{"client auto lifts the global cap", "auto", "2025-06-18", ""},
		{"unknown value is uncapped", "2024-01-01", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ResolveMaxProtocolVersion(&schemas.MCPClientConfig{MaxProtocolVersion: tc.client}, tc.global))
		})
	}
}

func TestNeedsRenegotiation(t *testing.T) {
	for _, tc := range []struct {
		name, negotiated, oldMax, newMax string
		want                             bool
	}{
		{"unchanged cap", "2025-06-18", "2025-06-18", "2025-06-18", false},
		{"now above the cap", "2026-07-28", "", "2025-11-25", true},
		{"still within the new cap", "2025-06-18", "", "2025-11-25", false},
		{"cap lifted, was held back", "2025-06-18", "2025-06-18", "", true},
		{"cap raised, was held back", "2025-06-18", "2025-06-18", "2025-11-25", true},
		{"cap raised, upstream was the limit", "2025-06-18", "2025-11-25", "", false},
		{"not connected", "", "", "2025-06-18", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, needsRenegotiation(tc.negotiated, tc.oldMax, tc.newMax))
		})
	}
}

// newAllVersionsUpstream serves the matrix upstream at every revision mcp-go supports.
func newAllVersionsUpstream(t *testing.T) string {
	t.Helper()
	ts := httptest.NewServer(server.NewStreamableHTTPServer(newMatrixUpstream()))
	t.Cleanup(ts.Close)
	return ts.URL
}

// A max protocol version caps what Bifrost negotiates with an upstream that supports every
// revision, from the global setting or a per-client override, on shared and per-call connections.
func TestMaxProtocolVersion_CapsNegotiation(t *testing.T) {
	for _, tc := range []struct {
		name, global, client, want string
	}{
		{"uncapped", "", "", mcpgo.ProtocolVersion20260728},
		{"global auto", "auto", "", mcpgo.ProtocolVersion20260728},
		{"global 2026-07-28", "2026-07-28", "", mcpgo.ProtocolVersion20260728},
		{"global 2025-11-25", "2025-11-25", "", mcpgo.ProtocolVersion20251125},
		{"global 2025-06-18", "2025-06-18", "", mcpgo.ProtocolVersion20250618},
		{"client cap over global auto", "auto", "2025-06-18", mcpgo.ProtocolVersion20250618},
		{"client auto over global cap", "2025-06-18", "auto", mcpgo.ProtocolVersion20260728},
	} {
		t.Run(tc.name, func(t *testing.T) {
			url := newAllVersionsUpstream(t)

			t.Run("shared", func(t *testing.T) {
				m := NewMCPManager(context.Background(), schemas.MCPConfig{MaxProtocolVersion: tc.global}, nil, &MockLogger{}, nil)
				config := &schemas.MCPClientConfig{
					ID: "cap-shared", Name: "matrix", ConnectionType: schemas.MCPConnectionTypeHTTP,
					ConnectionString: schemas.NewSecretVar(url), ToolsToExecute: []string{"*"}, MaxProtocolVersion: tc.client,
				}
				require.NoError(t, m.connectToMCPClient(context.Background(), config))
				t.Cleanup(func() { _ = m.RemoveClient(config.ID) })
				state, ok := snapshotClientState(m, config.ID)
				require.True(t, ok)
				require.NotNil(t, state.Conn)
				assert.Equal(t, tc.want, state.Conn.ProtocolVersion())
			})

			t.Run("per-call", func(t *testing.T) {
				m := NewMCPManager(context.Background(), schemas.MCPConfig{MaxProtocolVersion: tc.global}, nil, &MockLogger{}, nil)
				config := &schemas.MCPClientConfig{
					ID: "cap-percall", Name: "matrix", AuthType: schemas.MCPAuthTypeHeaders, ConnectionType: schemas.MCPConnectionTypeHTTP,
					ConnectionString: schemas.NewSecretVar(url), ToolsToExecute: []string{"*"}, MaxProtocolVersion: tc.client,
				}
				require.NoError(t, m.AddClient(context.Background(), config))
				t.Cleanup(func() { _ = m.RemoveClient(config.ID) })
				callMatrixEcho(t, m)
				shared, ok := m.perCallConns.Load(config.ID)
				require.True(t, ok)
				assert.Equal(t, tc.want, shared.(*perCallConn).knownVersion(url))
			})
		})
	}
}

// An upstream that answers initialize with a version above the cap is refused, not accepted.
func TestMaxProtocolVersion_UpstreamAnswersAboveCap(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method != "initialize" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":{"protocolVersion":"2025-11-25","capabilities":{"tools":{}},"serverInfo":{"name":"pushy","version":"1"}}}`))
	}))
	t.Cleanup(ts.Close)
	m := NewMCPManager(context.Background(), schemas.MCPConfig{MaxProtocolVersion: mcpgo.ProtocolVersion20250618}, nil, &MockLogger{}, nil)
	config := &schemas.MCPClientConfig{
		ID: "cap-refused", Name: "pushy", ConnectionType: schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar(ts.URL), ToolsToExecute: []string{"*"},
	}
	err := m.connectToMCPClient(context.Background(), config)
	t.Cleanup(func() { _ = m.RemoveClient(config.ID) })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "upstream negotiated 2025-11-25, newer than this client's max protocol version 2025-06-18")
}

// Changing the cap renegotiates the connections it affects: a per-client edit reconnects the shared
// connection, and a global change reconnects the clients that follow it.
func TestMaxProtocolVersion_ChangeRenegotiates(t *testing.T) {
	url := newAllVersionsUpstream(t)
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	newConfig := func(id, max string) *schemas.MCPClientConfig {
		return &schemas.MCPClientConfig{
			ID: id, Name: id, ConnectionType: schemas.MCPConnectionTypeHTTP, ConnectionString: schemas.NewSecretVar(url),
			ToolsToExecute: []string{"*"}, NeedsSessionStickiness: schemas.Ptr(true), MaxProtocolVersion: max,
		}
	}
	negotiated := func(id string) string {
		state, ok := snapshotClientState(m, id)
		if !ok || state.Conn == nil {
			return ""
		}
		return state.Conn.ProtocolVersion()
	}
	following, pinned := newConfig("follows_global", ""), newConfig("own_cap", mcpgo.ProtocolVersion20250618)
	for _, config := range []*schemas.MCPClientConfig{following, pinned} {
		require.NoError(t, m.AddClient(context.Background(), config))
		t.Cleanup(func() { _ = m.RemoveClient(config.ID) })
	}
	require.Equal(t, mcpgo.ProtocolVersion20260728, negotiated(following.ID))
	require.Equal(t, mcpgo.ProtocolVersion20250618, negotiated(pinned.ID))

	t.Run("per-client edit", func(t *testing.T) {
		edited := newConfig(pinned.ID, mcpgo.ProtocolVersion20251125)
		require.NoError(t, m.UpdateClient(pinned.ID, edited))
		assert.Equal(t, mcpgo.ProtocolVersion20251125, negotiated(pinned.ID))
	})

	t.Run("global change", func(t *testing.T) {
		m.UpdateMaxProtocolVersion(mcpgo.ProtocolVersion20250618)
		assert.Eventually(t, func() bool { return negotiated(following.ID) == mcpgo.ProtocolVersion20250618 }, 5*time.Second, 10*time.Millisecond)
		assert.Equal(t, mcpgo.ProtocolVersion20251125, negotiated(pinned.ID), "its own cap is unaffected")

		m.UpdateMaxProtocolVersion(schemas.MCPMaxProtocolVersionAuto)
		assert.Eventually(t, func() bool { return negotiated(following.ID) == mcpgo.ProtocolVersion20260728 }, 5*time.Second, 10*time.Millisecond)
	})
}

// A cap changed while a connect is past initialize (waiting on tools/list) still applies, in both
// directions: the connection installed above a lowered cap, or held back by a raised one, reconnects,
// including when a reconnect was already running.
func TestMaxProtocolVersion_ChangedDuringConnect(t *testing.T) {
	for _, tc := range []struct {
		name               string
		reconnect          bool
		initial, changedTo string
		want               string
	}{
		{"lowered on first connect", false, "", mcpgo.ProtocolVersion20250618, mcpgo.ProtocolVersion20250618},
		{"lowered during a reconnect", true, "", mcpgo.ProtocolVersion20250618, mcpgo.ProtocolVersion20250618},
		{"raised on first connect", false, mcpgo.ProtocolVersion20250618, schemas.MCPMaxProtocolVersionAuto, mcpgo.ProtocolVersion20260728},
		{"raised during a reconnect", true, mcpgo.ProtocolVersion20250618, schemas.MCPMaxProtocolVersionAuto, mcpgo.ProtocolVersion20260728},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var holdNext atomic.Bool
			listing, release := make(chan struct{}, 1), make(chan struct{})
			var releaseOnce sync.Once
			upstream := server.NewStreamableHTTPServer(newMatrixUpstream())
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				r.Body = io.NopCloser(bytes.NewReader(body))
				if bytes.Contains(body, []byte(`"tools/list"`)) && holdNext.CompareAndSwap(true, false) {
					listing <- struct{}{}
					<-release
				}
				upstream.ServeHTTP(w, r)
			}))
			t.Cleanup(ts.Close)
			// After ts.Close, so it runs first: a blocked handler is released before the server waits on it.
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

			m := NewMCPManager(context.Background(), schemas.MCPConfig{MaxProtocolVersion: tc.initial}, nil, &MockLogger{}, nil)
			config := &schemas.MCPClientConfig{
				ID: "cap-changed", Name: "matrix", ConnectionType: schemas.MCPConnectionTypeHTTP, ConnectionString: schemas.NewSecretVar(ts.URL),
				ToolsToExecute: []string{"*"}, NeedsSessionStickiness: schemas.Ptr(true),
			}
			t.Cleanup(func() { _ = m.RemoveClient(config.ID) })
			holdNext.Store(!tc.reconnect)
			connected := make(chan error, 1)
			go func() { connected <- m.AddClient(context.Background(), config) }()
			if tc.reconnect {
				require.NoError(t, <-connected)
				holdNext.Store(true)
				go func() { connected <- m.ReconnectClient(config.ID) }()
			}
			select {
			case <-listing:
			case <-time.After(5 * time.Second):
				t.Fatal("the connect never reached tools/list")
			}

			m.UpdateMaxProtocolVersion(tc.changedTo)
			releaseOnce.Do(func() { close(release) })
			require.NoError(t, <-connected)

			assert.Eventually(t, func() bool {
				state, ok := snapshotClientState(m, config.ID)
				return ok && state.Conn != nil && state.Conn.ProtocolVersion() == tc.want
			}, 5*time.Second, 10*time.Millisecond, "the connection must follow the cap set while it connected")
		})
	}
}

// A connection left above a lowered cap because the renegotiating handshake failed is not passed by
// the connection checker as healthy: it stays Unstable and is retried until the cap holds.
func TestMaxProtocolVersion_CheckerEnforcesCapAfterFailedRenegotiation(t *testing.T) {
	var refuse atomic.Bool
	upstream := server.NewStreamableHTTPServer(newMatrixUpstream())
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		if refuse.Load() && bytes.Contains(body, []byte(`"initialize"`)) {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		upstream.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	config := &schemas.MCPClientConfig{
		ID: "cap-enforced", Name: "matrix", ConnectionType: schemas.MCPConnectionTypeHTTP, ConnectionString: schemas.NewSecretVar(ts.URL),
		ToolsToExecute: []string{"*"}, NeedsSessionStickiness: schemas.Ptr(true),
	}
	require.NoError(t, m.AddClient(context.Background(), config))
	t.Cleanup(func() { _ = m.RemoveClient(config.ID) })
	negotiated := func() string {
		state, ok := snapshotClientState(m, config.ID)
		if !ok || state.Conn == nil {
			return ""
		}
		return state.Conn.ProtocolVersion()
	}
	require.Equal(t, mcpgo.ProtocolVersion20260728, negotiated())

	refuse.Store(true)
	m.UpdateMaxProtocolVersion(mcpgo.ProtocolVersion20250618)
	m.checkerManager.Kick(config.ID)
	assert.Eventually(t, func() bool {
		state, ok := snapshotClientState(m, config.ID)
		return ok && state.State == schemas.MCPConnectionStateUnstable && negotiated() == mcpgo.ProtocolVersion20260728
	}, 10*time.Second, 20*time.Millisecond, "a connection above the cap must not read as healthy")

	refuse.Store(false)
	m.checkerManager.Kick(config.ID)
	assert.Eventually(t, func() bool { return negotiated() == mcpgo.ProtocolVersion20250618 },
		15*time.Second, 20*time.Millisecond, "the checker must keep retrying until the cap holds")
}

// A raised cap whose upgrade handshake failed stays pending: the old connection, below the new cap,
// keeps serving but is not passed as healthy, and the upgrade is retried until it lands.
func TestMaxProtocolVersion_CheckerRetriesFailedUpgrade(t *testing.T) {
	var refuse atomic.Bool
	upstream := server.NewStreamableHTTPServer(newMatrixUpstream())
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		// Both handshakes: 2026-07-28 opens with server/discover, earlier revisions with initialize.
		if refuse.Load() && (bytes.Contains(body, []byte(`"initialize"`)) || bytes.Contains(body, []byte(`"server/discover"`))) {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		upstream.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	m := NewMCPManager(context.Background(), schemas.MCPConfig{MaxProtocolVersion: mcpgo.ProtocolVersion20250618}, nil, &MockLogger{}, nil)
	config := &schemas.MCPClientConfig{
		ID: "cap-upgrade", Name: "matrix", ConnectionType: schemas.MCPConnectionTypeHTTP, ConnectionString: schemas.NewSecretVar(ts.URL),
		ToolsToExecute: []string{"*"}, NeedsSessionStickiness: schemas.Ptr(true),
	}
	require.NoError(t, m.AddClient(context.Background(), config))
	t.Cleanup(func() { _ = m.RemoveClient(config.ID) })
	negotiated := func() string {
		state, ok := snapshotClientState(m, config.ID)
		if !ok || state.Conn == nil {
			return ""
		}
		return state.Conn.ProtocolVersion()
	}
	require.Equal(t, mcpgo.ProtocolVersion20250618, negotiated())

	refuse.Store(true)
	m.UpdateMaxProtocolVersion(schemas.MCPMaxProtocolVersionAuto)
	m.checkerManager.Kick(config.ID)
	assert.Eventually(t, func() bool {
		state, ok := snapshotClientState(m, config.ID)
		return ok && state.State == schemas.MCPConnectionStateUnstable && negotiated() == mcpgo.ProtocolVersion20250618
	}, 10*time.Second, 20*time.Millisecond, "a connection still owed an upgrade must not read as healthy")

	refuse.Store(false)
	m.checkerManager.Kick(config.ID)
	assert.Eventually(t, func() bool { return negotiated() == mcpgo.ProtocolVersion20260728 },
		15*time.Second, 20*time.Millisecond, "the checker must keep retrying until the upgrade lands")
}

func callMatrixEcho(t *testing.T, m *MCPManager) {
	t.Helper()
	msg, bErr := m.ExecuteChatTool(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), &schemas.ChatAssistantMessageToolCall{
		ID:       schemas.Ptr("call"),
		Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr("matrix-echo"), Arguments: `{"message":"hi"}`},
	})
	require.Nil(t, bErr)
	require.Equal(t, "hi", *msg.Content.ContentStr)
}
