package mcp

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// snapshotAddClientState copies the fields the assertions below care about
// while holding the manager lock, with the client's discovery read from the
// store (client state no longer carries discovery data). Reuses clientSnapshot
// from makebeforebreak_test.go (same package).
func snapshotAddClientState(m *MCPManager, id string) (state clientSnapshot, exists bool) {
	return snapshotClientState(m, id)
}

// TestAddClient_PerUserHeaders_StateSelection covers AddClient's
// per_user_headers registration path. The client's record of its admin step
// decides whether it parks in pending_verification, before ever reaching the
// RequiresPerCallConnection branch below; its tools do not. Any verified client
// lands in Healthy, with or without tools — an empty ToolMap already says "no
// tools discovered yet" (or "server legitimately exposes zero tools") on its
// own. Every case must also preserve ConnectionURL from ConnectionString.
func TestAddClient_PerUserHeaders_StateSelection(t *testing.T) {
	tests := []struct {
		name            string
		clientID        string
		clientName      string
		pending         bool
		discoveredTools map[string]schemas.ChatTool
		wantState       schemas.MCPConnectionState
		wantToolCount   int
	}{
		{
			name:            "pending its admin step parks in pending_verification",
			clientID:        "discovered-tools-pending",
			clientName:      "discovered_tools_pending",
			pending:         true,
			discoveredTools: nil,
			wantState:       schemas.MCPConnectionStatePendingVerification,
			wantToolCount:   0,
		},
		{
			name:            "verified with no stored tools is healthy",
			clientID:        "discovered-tools-nil",
			clientName:      "discovered_tools_nil",
			discoveredTools: nil,
			wantState:       schemas.MCPConnectionStateHealthy,
			wantToolCount:   0,
		},
		{
			name:            "non-nil empty DiscoveredTools is healthy with zero tools",
			clientID:        "discovered-tools-empty",
			clientName:      "discovered_tools_empty",
			discoveredTools: map[string]schemas.ChatTool{},
			wantState:       schemas.MCPConnectionStateHealthy,
			wantToolCount:   0,
		},
		{
			name:       "populated DiscoveredTools restores tools as healthy",
			clientID:   "discovered-tools-populated",
			clientName: "discovered_tools_populated",
			discoveredTools: map[string]schemas.ChatTool{
				"tool-a": {},
				"tool-b": {},
			},
			wantState:     schemas.MCPConnectionStateHealthy,
			wantToolCount: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// nil credStore: NewMCPManager defaults to a real credstore.CredStore,
			// whose RequiresPerCallConnection is what production AddClient calls
			// resolve against for per_user_headers clients.
			m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
			t.Cleanup(func() { _ = m.Cleanup() })

			config := &schemas.MCPClientConfig{
				ID:                  tt.clientID,
				Name:                tt.clientName,
				ConnectionType:      schemas.MCPConnectionTypeHTTP,
				ConnectionString:    schemas.NewSecretVar("https://example.invalid/mcp"),
				AuthType:            schemas.MCPAuthTypePerUserHeaders,
				PerUserHeaderKeys:   []string{"X-User-Token"},
				DiscoveredTools:     tt.discoveredTools,
				PendingVerification: tt.pending,
			}

			require.NoError(t, m.AddClient(context.Background(), config))

			state, ok := snapshotAddClientState(m, config.ID)
			require.True(t, ok)
			assert.Equal(t, tt.wantState, state.State)
			assert.Len(t, state.ToolMap, tt.wantToolCount)
			require.NotNil(t, state.ConnectionInfo)
			require.NotNil(t, state.ConnectionInfo.ConnectionURL)
			assert.Equal(t, "https://example.invalid/mcp", *state.ConnectionInfo.ConnectionURL)
		})
	}
}

// TestAddClient_PerCallConnection_StartsConnectionChecker pins the fix for
// the shared-per-call tool-discovery bug: AddClient's per-call branch (any
// auth type RequiresPerCallConnection resolves true for — per_user_headers
// here) must start a connection checker, exactly like the sticky success
// path (connectToMCPClient) already does. Without one, nothing ever
// revisits a client whose DiscoveredTools was empty/nil at add time — it
// would sit at Healthy/0-tools forever.
func TestAddClient_PerCallConnection_StartsConnectionChecker(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	t.Cleanup(func() { _ = m.Cleanup() })

	config := &schemas.MCPClientConfig{
		ID:                "per-call-checker-client",
		Name:              "per_call_checker_client",
		ConnectionType:    schemas.MCPConnectionTypeHTTP,
		ConnectionString:  schemas.NewSecretVar("https://example.invalid/mcp"),
		AuthType:          schemas.MCPAuthTypePerUserHeaders,
		PerUserHeaderKeys: []string{"X-User-Token"},
		DiscoveredTools:   map[string]schemas.ChatTool{},
	}

	require.NoError(t, m.AddClient(context.Background(), config))

	m.checkerManager.mu.RLock()
	_, hasChecker := m.checkerManager.checkers[config.ID]
	m.checkerManager.mu.RUnlock()
	assert.True(t, hasChecker, "AddClient's per-call branch must start a connection checker, or this client's tools are never (re)discovered")
}

// TestAddClient_PerCallConnection_SharedType_DiscoversToolsSynchronously pins
// the second half of the same bug report: a connection checker alone is not
// enough, because ClientConnectionChecker.Start uses the SLOW healthyInterval
// (not the fast Unstable one) for a client that already starts Healthy — so
// without a synchronous first discovery pass, a shared oauth/headers/none
// per-call client would sit at Healthy/0-tools for up to healthyInterval
// before its first real check ever ran. AddClient must discover tools
// synchronously, mirroring what per_user_headers/token_exchange already get
// from their own admin-verify-at-setup-time step.
func TestAddClient_PerCallConnection_SharedType_DiscoversToolsSynchronously(t *testing.T) {
	ts, _ := buildAdminDiscoveryHTTPServer(t)

	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	t.Cleanup(func() { _ = m.Cleanup() })

	config := &schemas.MCPClientConfig{
		ID:               "shared-sync-discovery-client",
		Name:             "shared_sync_discovery_client",
		ConnectionType:   schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar(ts.URL),
		AuthType:         schemas.MCPAuthTypeHeaders,
		Headers:          map[string]schemas.SecretVar{"Authorization": *schemas.NewSecretVar("Bearer shared-add-token")},
		// NeedsSessionStickiness left nil: the default per-call value.
		// DiscoveredTools left nil: nothing to restore, forcing the new
		// synchronous-discovery path this test targets.
	}

	require.NoError(t, m.AddClient(context.Background(), config))

	state, ok := snapshotAddClientState(m, config.ID)
	require.True(t, ok)
	assert.Equal(t, schemas.MCPConnectionStateHealthy, state.State)
	assert.Contains(t, state.ToolMap, "shared_sync_discovery_client-echo", "tools must be discovered synchronously during AddClient, not deferred entirely to the periodic checker")
}
