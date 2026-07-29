package mcp

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// ClientToolSyncer.performSync — the conn==nil branch widened by this
// change. Uses fakeAdminCredStore / buildAdminDiscoveryHTTPServer from
// admin_tool_discovery_test.go (same package).
// =============================================================================

// newSyncTestClientState builds a minimal MCPClientState + config pair for
// the performSync tests below, mirroring newAuthRetryClientState's shape
// (auth_retry_test.go) but with a settable AuthType and Conn.
func newSyncTestClientState(clientID, clientName string, authType schemas.MCPAuthType, tools map[string]schemas.ChatTool) (*schemas.MCPClientState, *schemas.MCPClientConfig) {
	config := &schemas.MCPClientConfig{
		ID:       clientID,
		Name:     clientName,
		AuthType: authType,
	}
	state := &schemas.MCPClientState{
		Name:            clientName,
		ExecutionConfig: config,
		Conn:            nil, // every test here exercises the conn==nil branch
		ToolMap:         tools,
		ToolNameMapping: map[string]string{},
	}
	return state, config
}

// TestClientToolSyncer_PerformSync_NilConn_PerUserOAuth_AttemptsAdminDiscovery
// confirms the new behavior: a per_user_oauth client with no live conn is
// no longer silently skipped — performSync now attempts admin tool
// discovery (observed here via the credStore.AdminConnectionHeaders call
// count, since RequiresPerCallConnection auth types never hold a conn to
// reuse in the first place).
func TestClientToolSyncer_PerformSync_NilConn_PerUserOAuth_AttemptsAdminDiscovery(t *testing.T) {
	cred := &fakeAdminCredStore{err: errors.New("admin credential unavailable")}
	manager := &MCPManager{
		credStore: cred,
		logger:    &MockLogger{},
		clientMap: map[string]*schemas.MCPClientState{},
	}

	existingTools := map[string]schemas.ChatTool{"oauth-client-existing": {}}
	state, config := newSyncTestClientState("client-1", "oauth-client", schemas.MCPAuthTypePerUserOauth, existingTools)
	manager.clientMap[config.ID] = state

	syncer := NewClientToolSyncer(manager, config.ID, config.Name, time.Minute, &MockLogger{})
	syncer.performSync()

	require.Equal(t, 1, cred.callCount(), "performSync should attempt admin tool discovery (calling credStore.AdminConnectionHeaders) instead of silently skipping")
	// Admin discovery failed, so existing tools must be kept intact (same
	// "keep existing tools on error" contract as the shared-conn path).
	require.Equal(t, existingTools, manager.clientMap[config.ID].ToolMap)
}

// TestClientToolSyncer_PerformSync_NilConn_PerUserHeaders_AttemptsAdminDiscovery
// is the per_user_headers counterpart of the test above.
func TestClientToolSyncer_PerformSync_NilConn_PerUserHeaders_AttemptsAdminDiscovery(t *testing.T) {
	cred := &fakeAdminCredStore{err: errors.New("admin credential unavailable")}
	manager := &MCPManager{
		credStore: cred,
		logger:    &MockLogger{},
		clientMap: map[string]*schemas.MCPClientState{},
	}

	existingTools := map[string]schemas.ChatTool{"headers-client-existing": {}}
	state, config := newSyncTestClientState("client-2", "headers-client", schemas.MCPAuthTypePerUserHeaders, existingTools)
	manager.clientMap[config.ID] = state

	syncer := NewClientToolSyncer(manager, config.ID, config.Name, time.Minute, &MockLogger{})
	syncer.performSync()

	require.Equal(t, 1, cred.callCount(), "performSync should attempt admin tool discovery instead of silently skipping")
	require.Equal(t, existingTools, manager.clientMap[config.ID].ToolMap)
}

// TestClientToolSyncer_PerformSync_NilConn_SharedAuthType_StillSkips is the
// regression-risk case: a shared-connection client (e.g. "headers" or
// "none") with Conn==nil is mid-(re)connect, not per-user. performSync must
// keep silently skipping it exactly as before this change — it must NOT be
// routed into admin discovery, which would be meaningless (and would call a
// credStore method shared resolvers don't meaningfully implement).
func TestClientToolSyncer_PerformSync_NilConn_SharedAuthType_StillSkips(t *testing.T) {
	for _, authType := range []schemas.MCPAuthType{schemas.MCPAuthTypeHeaders, schemas.MCPAuthTypeNone, schemas.MCPAuthTypeOauth} {
		t.Run(string(authType), func(t *testing.T) {
			cred := &fakeAdminCredStore{err: errors.New("must never be called for shared auth types")}
			manager := &MCPManager{
				credStore: cred,
				logger:    &MockLogger{},
				clientMap: map[string]*schemas.MCPClientState{},
			}

			existingTools := map[string]schemas.ChatTool{"shared-client-existing": {}}
			state, config := newSyncTestClientState("client-3", "shared-client", authType, existingTools)
			manager.clientMap[config.ID] = state

			syncer := NewClientToolSyncer(manager, config.ID, config.Name, time.Minute, &MockLogger{})
			syncer.performSync()

			require.Equal(t, 0, cred.callCount(), "shared clients mid-reconnect (Conn==nil) must still be silently skipped, not routed into admin discovery")
			require.Equal(t, existingTools, manager.clientMap[config.ID].ToolMap, "skipped sync must leave the existing tool map untouched")
			// The client must still be present (not removed) — performSync's
			// skip path is a no-op, distinct from the "client no longer in
			// clientMap" early-return above it.
			_, stillExists := manager.clientMap[config.ID]
			require.True(t, stillExists)
		})
	}
}

// TestClientToolSyncer_PerformSync_NilConn_PerUserOAuth_SuccessUpdatesToolMap
// is an end-to-end happy-path check: when admin discovery succeeds, the
// discovered tools actually replace the client's ToolMap, using a real
// upstream HTTP MCP server (buildAdminDiscoveryHTTPServer, shared with
// admin_tool_discovery_test.go) rather than mocking the connect/discover
// cycle away.
func TestClientToolSyncer_PerformSync_NilConn_PerUserOAuth_SuccessUpdatesToolMap(t *testing.T) {
	ts, _ := buildAdminDiscoveryHTTPServer(t)

	cred := &fakeAdminCredStore{headers: http.Header{"Authorization": []string{"Bearer admin-token"}}}
	manager := &MCPManager{
		credStore: cred,
		logger:    &MockLogger{},
		clientMap: map[string]*schemas.MCPClientState{},
	}

	state, config := newSyncTestClientState("client-4", "oauth-client", schemas.MCPAuthTypePerUserOauth, map[string]schemas.ChatTool{})
	config.ConnectionType = schemas.MCPConnectionTypeHTTP
	config.ConnectionString = schemas.NewSecretVar(ts.URL)
	manager.clientMap[config.ID] = state

	syncer := NewClientToolSyncer(manager, config.ID, config.Name, time.Minute, &MockLogger{})
	syncer.performSync()

	require.Equal(t, 1, cred.callCount())
	updated := manager.clientMap[config.ID].ToolMap
	require.Contains(t, updated, "oauth-client-echo", "a successful admin-discovery cycle should populate the client's tool map")
}
