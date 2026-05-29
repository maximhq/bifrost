package mcp

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// snapshotAddClientState copies the fields the assertions below care about while
// holding the manager lock.
func snapshotAddClientState(m *MCPManager, id string) (state schemas.MCPClientState, exists bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cs, ok := m.clientMap[id]
	if !ok {
		return schemas.MCPClientState{}, false
	}
	return *cs, true
}

// TestAddClient_PerUserHeaders_DiscoveredToolsStateSelection covers
// AddClient's per_user_headers registration path, where nil, non-nil-empty,
// and populated DiscoveredTools carry three different lifecycle meanings: a
// nil map means admin verification has never run (park in
// pending_verification, before ever reaching the RequiresPerCallConnection
// branch below); a non-nil empty map means verification ran but the server
// legitimately exposes zero tools (pending_tools); and a populated map means
// verification ran and discovered tools are restored (connected). All three
// cases must also preserve ConnectionURL from ConnectionString.
func TestAddClient_PerUserHeaders_DiscoveredToolsStateSelection(t *testing.T) {
	tests := []struct {
		name            string
		clientID        string
		clientName      string
		discoveredTools map[string]schemas.ChatTool
		wantState       schemas.MCPConnectionState
		wantToolCount   int
	}{
		{
			name:            "nil DiscoveredTools parks in pending_verification",
			clientID:        "discovered-tools-nil",
			clientName:      "discovered_tools_nil",
			discoveredTools: nil,
			wantState:       schemas.MCPConnectionStatePendingVerification,
			wantToolCount:   0,
		},
		{
			name:            "non-nil empty DiscoveredTools is pending_tools",
			clientID:        "discovered-tools-empty",
			clientName:      "discovered_tools_empty",
			discoveredTools: map[string]schemas.ChatTool{},
			wantState:       schemas.MCPConnectionStatePendingTools,
			wantToolCount:   0,
		},
		{
			name:       "populated DiscoveredTools restores tools as connected",
			clientID:   "discovered-tools-populated",
			clientName: "discovered_tools_populated",
			discoveredTools: map[string]schemas.ChatTool{
				"tool-a": {},
				"tool-b": {},
			},
			wantState:     schemas.MCPConnectionStateConnected,
			wantToolCount: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// nil credStore: NewMCPManager defaults to a real credstore.CredStore,
			// whose RequiresPerCallConnection is what production AddClient calls
			// resolve against for per_user_headers clients.
			m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)

			config := &schemas.MCPClientConfig{
				ID:                tt.clientID,
				Name:              tt.clientName,
				ConnectionType:    schemas.MCPConnectionTypeHTTP,
				ConnectionString:  schemas.NewSecretVar("https://example.invalid/mcp"),
				AuthType:          schemas.MCPAuthTypePerUserHeaders,
				PerUserHeaderKeys: []string{"X-User-Token"},
				DiscoveredTools:   tt.discoveredTools,
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
