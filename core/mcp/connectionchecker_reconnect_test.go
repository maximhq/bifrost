package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// ClientConnectionChecker.performCheck: the live-connection (conn != nil)
// branch's background reconnect.
// =============================================================================

// sessionInvalidator fronts a real streamable-HTTP MCP server and reproduces
// the upstream shape behind this regression: the server expires a persistent
// MCP session, so every later request carrying that Mcp-Session-Id is
// rejected permanently (422 "Unexpected message, expect initialize request"),
// while a sessionless initialize is still served normally. A dead session is
// therefore unrecoverable by re-probing, and recoverable only by dialling a
// fresh connection.
type sessionInvalidator struct {
	mu       sync.Mutex
	lastSeen string
	dead     map[string]bool
}

// expireCurrentSession invalidates whatever session the server last served,
// standing in for a server-side session timeout.
func (s *sessionInvalidator) expireCurrentSession() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastSeen != "" {
		s.dead[s.lastSeen] = true
	}
}

// reject reports whether r belongs to an expired session, writing the
// upstream's own error response when it does. Sessionless requests (the
// initialize of a brand-new connection) always pass through.
func (s *sessionInvalidator) reject(w http.ResponseWriter, r *http.Request) bool {
	sessionID := r.Header.Get("Mcp-Session-Id")
	if sessionID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dead[sessionID] {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte("Unexpected message, expect initialize request"))
		return true
	}
	s.lastSeen = sessionID
	return false
}

// buildExpiringSessionMCPServer starts a streamable-HTTP MCP server (one
// "echo" tool) whose sessions the test can invalidate at will.
func buildExpiringSessionMCPServer(t *testing.T) (*httptest.Server, *sessionInvalidator) {
	t.Helper()

	s := server.NewMCPServer("test-session-expiry", "1.0.0", server.WithToolCapabilities(true))
	s.AddTool(
		mcpgo.NewTool("echo", mcpgo.WithDescription("Echo tool")),
		func(_ context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return mcpgo.NewToolResultText("ok"), nil
		},
	)

	streamable := server.NewStreamableHTTPServer(s)
	invalidator := &sessionInvalidator{dead: map[string]bool{}}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if invalidator.reject(w, r) {
			return
		}
		streamable.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts, invalidator
}

// TestPerformCheck_LiveConn_FailedCheck_TriggersReconnect pins the behavior
// ClientConnectionChecker's own doc comment promises for the live-connection
// branch: a failed check of the current sticky connection marks the client
// Unstable AND starts a background reconnect. Without that reconnect the
// failed connection stays installed on the entry, so every later tick
// re-probes the same dead session and the conn == nil reconnect branch is
// never reached: the client stays Unstable until an administrator
// reconnects it by hand.
func TestPerformCheck_LiveConn_FailedCheck_TriggersReconnect(t *testing.T) {
	ts, upstream := buildExpiringSessionMCPServer(t)

	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	config := &schemas.MCPClientConfig{
		ID:                     "client-live-check-reconnect",
		Name:                   "sticky-http-client",
		AuthType:               schemas.MCPAuthTypeHeaders,
		ConnectionType:         schemas.MCPConnectionTypeHTTP,
		ConnectionString:       schemas.NewSecretVar(ts.URL),
		NeedsSessionStickiness: schemas.Ptr(true), // sticky: holds the persistent connection under test
		ToolsToExecute:         []string{"*"},
	}
	require.NoError(t, m.connectToMCPClient(context.Background(), config))

	before, ok := snapshotClientState(m, config.ID)
	require.True(t, ok)
	require.NotNil(t, before.Conn)
	require.Equal(t, schemas.MCPConnectionStateHealthy, before.State)

	// The upstream expires the session behind the live connection: list_tools
	// over it now fails permanently, while a fresh dial would still succeed.
	upstream.expireCurrentSession()

	checker := NewClientConnectionChecker(m, config.ID, time.Minute, false, &MockLogger{})
	next, onSteady := checker.performCheck()
	assert.Equal(t, UnstableConnectionCheckInterval, next, "a failed live check must stay on the tight recovery cadence")
	assert.False(t, onSteady)

	unstable, ok := snapshotClientState(m, config.ID)
	require.True(t, ok)
	require.Equal(t, schemas.MCPConnectionStateUnstable, unstable.State, "the failed check must mark the client unstable")

	// Observed through the manager's exclusive-op registry, the same guard
	// ReconnectClient dedupes concurrent attempts with, and what the conn ==
	// nil branch's own reconnect is asserted through elsewhere.
	require.Eventually(t, func() bool {
		_, inFlightOrDone := m.reconnectingClients.Load(config.ID)
		return inFlightOrDone
	}, 2*time.Second, 10*time.Millisecond, "a failed check of the live sticky connection must start the background reconnect")

	// Registering the attempt is not the point on its own: the dead session
	// must actually be replaced by a fresh one.
	require.Eventually(t, func() bool {
		after, exists := snapshotClientState(m, config.ID)
		return exists &&
			after.State == schemas.MCPConnectionStateHealthy &&
			after.ConnGeneration > before.ConnGeneration
	}, 10*time.Second, 20*time.Millisecond, "the background reconnect must install a fresh connection and return the client to healthy")

	after, ok := snapshotClientState(m, config.ID)
	require.True(t, ok)
	assert.NotSame(t, before.Conn, after.Conn, "the dead connection must be replaced, not re-probed forever")
}

// TestReconnectAfterFailedCheck_StaleOrAuthoritativeState_DoesNotReconnect
// covers the guard half. A check runs unlocked and can take seconds, so by the
// time it fails the entry may have moved on: a reconnect already swapped in a
// fresh connection (the failure proves only that the replaced one was dead,
// and redialling would tear down its healthy successor), or an authoritative
// state was written that must not be dialled out of. Disabled and NeedsReauth
// are checked separately from the generation on purpose, since neither
// DisableClient nor CloseAndMarkNeedsReauth bumps ConnGeneration when it
// clears the connection.
func TestReconnectAfterFailedCheck_StaleOrAuthoritativeState_DoesNotReconnect(t *testing.T) {
	const currentGeneration = 6

	tests := []struct {
		name            string
		state           schemas.MCPConnectionState
		checkGeneration uint64
		wantReconnect   bool
	}{
		{
			name:            "a check that spanned a reconnect must not redial its successor",
			state:           schemas.MCPConnectionStateUnstable,
			checkGeneration: currentGeneration - 1,
		},
		{
			name:            "a client disabled mid-check must not be dialled",
			state:           schemas.MCPConnectionStateDisabled,
			checkGeneration: currentGeneration,
		},
		{
			name:            "a credential confirmed dead has nothing to reconnect with",
			state:           schemas.MCPConnectionStateNeedsReauth,
			checkGeneration: currentGeneration,
		},
		{
			name:            "a current-generation failure on a live client does redial",
			state:           schemas.MCPConnectionStateUnstable,
			checkGeneration: currentGeneration,
			wantReconnect:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
			config := &schemas.MCPClientConfig{
				ID:                     "client-reconnect-guard",
				Name:                   "guarded-client",
				AuthType:               schemas.MCPAuthTypeHeaders,
				ConnectionType:         schemas.MCPConnectionTypeHTTP,
				ConnectionString:       schemas.NewSecretVar("http://127.0.0.1:0/mcp"), // unreachable: only the attempt matters
				NeedsSessionStickiness: schemas.Ptr(true),
			}
			m.mu.Lock()
			m.clientMap[config.ID] = &schemas.MCPClientState{
				Name:            config.Name,
				ExecutionConfig: config,
				State:           tc.state,
				ConnGeneration:  currentGeneration,
			}
			m.mu.Unlock()

			checker := NewClientConnectionChecker(m, config.ID, time.Minute, false, &MockLogger{})
			checker.reconnectAfterFailedCheck(config.Name, tc.checkGeneration)

			if tc.wantReconnect {
				require.Eventually(t, func() bool {
					_, inFlightOrDone := m.reconnectingClients.Load(config.ID)
					return inFlightOrDone
				}, 2*time.Second, 10*time.Millisecond, "the guards must not be a blanket no-op")
				return
			}
			require.Never(t, func() bool {
				_, inFlightOrDone := m.reconnectingClients.Load(config.ID)
				return inFlightOrDone
			}, 500*time.Millisecond, 25*time.Millisecond, "no reconnect may be started for this client")
		})
	}
}
