package mcp

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// ClientConnectionChecker.performCheck — the per-call (conn==nil) branches.
// Uses fakeAdminCredStore / buildAdminDiscoveryHTTPServer from
// admin_tool_discovery_test.go (same package).
// =============================================================================

// newSyncTestClientState builds a minimal MCPClientState + config pair for
// the performCheck tests below, mirroring newAuthRetryClientState's shape
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

// TestPerformCheck_NilConn_PerUserOAuth_AttemptsAdminDiscovery confirms a
// per_user_oauth client with no live conn attempts admin tool discovery
// (observed here via the credStore.AdminConnectionHeaders call count, since
// RequiresPerCallConnection auth types never hold a conn to reuse in the
// first place).
func TestPerformCheck_NilConn_PerUserOAuth_AttemptsAdminDiscovery(t *testing.T) {
	cred := &fakeAdminCredStore{err: errors.New("admin credential unavailable")}
	manager := &MCPManager{
		credStore: cred,
		logger:    &MockLogger{},
		clientMap: map[string]*schemas.MCPClientState{},
	}

	existingTools := map[string]schemas.ChatTool{"oauth-client-existing": {}}
	state, config := newSyncTestClientState("client-1", "oauth-client", schemas.MCPAuthTypePerUserOauth, existingTools)
	manager.clientMap[config.ID] = state

	checker := NewClientConnectionChecker(manager, config.ID, time.Minute, false, &MockLogger{})
	checker.performCheck()

	// performCheck wraps admin discovery in ExecuteWithRetry(ProbeRetryConfig)
	// same as every other check — this unclassified error doesn't match
	// isTransientError's permanent list, so it retries (MaxRetries=3, 4 total
	// attempts) before finally giving up and marking Unstable.
	require.Equal(t, ProbeRetryConfig.MaxRetries+1, cred.callCount(), "performCheck should attempt admin tool discovery (calling credStore.AdminConnectionHeaders), retried like every other check, instead of silently skipping")
	// Admin discovery failed, so existing tools must be kept intact (same
	// "keep existing tools on error" contract as the live-conn path).
	require.Equal(t, existingTools, manager.clientMap[config.ID].ToolMap)
	require.Equal(t, schemas.MCPConnectionStateUnstable, manager.clientMap[config.ID].State)
}

// TestPerformCheck_NilConn_PerUserHeaders_AttemptsAdminDiscovery is the
// per_user_headers counterpart of the test above.
func TestPerformCheck_NilConn_PerUserHeaders_AttemptsAdminDiscovery(t *testing.T) {
	cred := &fakeAdminCredStore{err: errors.New("admin credential unavailable")}
	manager := &MCPManager{
		credStore: cred,
		logger:    &MockLogger{},
		clientMap: map[string]*schemas.MCPClientState{},
	}

	existingTools := map[string]schemas.ChatTool{"headers-client-existing": {}}
	state, config := newSyncTestClientState("client-2", "headers-client", schemas.MCPAuthTypePerUserHeaders, existingTools)
	manager.clientMap[config.ID] = state

	checker := NewClientConnectionChecker(manager, config.ID, time.Minute, false, &MockLogger{})
	checker.performCheck()

	require.Equal(t, ProbeRetryConfig.MaxRetries+1, cred.callCount(), "performCheck should attempt admin tool discovery instead of silently skipping, retried like every other check")
	require.Equal(t, existingTools, manager.clientMap[config.ID].ToolMap)
}

// TestPerformCheck_NilConn_SharedAuthType_NeverAttemptsAdminDiscovery is the
// regression-risk case: a shared-connection client (e.g. "headers" or
// "none") with Conn==nil is mid-(re)connect, not per-user. performCheck must
// never route it into admin discovery, which would be meaningless (and would
// call a credStore method shared resolvers don't meaningfully implement) —
// this branch instead attempts a direct reconnect, covered separately by
// TestPerformCheck_NilConn_SharedAuthType_TriggersReconnect below.
func TestPerformCheck_NilConn_SharedAuthType_NeverAttemptsAdminDiscovery(t *testing.T) {
	for _, authType := range []schemas.MCPAuthType{schemas.MCPAuthTypeHeaders, schemas.MCPAuthTypeNone, schemas.MCPAuthTypeOauth} {
		t.Run(string(authType), func(t *testing.T) {
			cred := &fakeAdminCredStore{err: errors.New("must never be called for shared auth types")}
			manager := NewMCPManager(context.Background(), schemas.MCPConfig{}, cred, &MockLogger{}, nil)

			existingTools := map[string]schemas.ChatTool{"shared-client-existing": {}}
			state, config := newSyncTestClientState("client-3", "shared-client", authType, existingTools)
			manager.mu.Lock()
			manager.clientMap[config.ID] = state
			manager.mu.Unlock()

			checker := NewClientConnectionChecker(manager, config.ID, time.Minute, false, &MockLogger{})
			checker.performCheck()

			// performCheck's default branch spawns the actual reconnect in a
			// background goroutine, which mutates clientMap under manager.mu —
			// read it back under the same lock rather than racing that goroutine.
			manager.mu.RLock()
			toolMap := manager.clientMap[config.ID].ToolMap
			_, stillExists := manager.clientMap[config.ID]
			manager.mu.RUnlock()

			require.Equal(t, 0, cred.callCount(), "shared clients mid-reconnect (Conn==nil) must never be routed into admin discovery")
			require.Equal(t, existingTools, toolMap, "must leave the existing tool map untouched — only a real reconnect's own tools/list replaces it")
			require.True(t, stillExists)
		})
	}
}

// TestPerformCheck_NilConn_SharedAuthType_TriggersReconnect covers the
// behavior change from the old ClientToolSyncer: a sticky client with no
// live connection (e.g. still Unstable from a prior failed connect) is no
// longer silently skipped by the periodic checker — it now attempts a
// reconnect directly, closing the gap where only a separate, slower
// consecutive-failure-counting health monitor used to eventually notice.
// Observed via the manager's exclusive-op registry (the same guard
// ReconnectClient/connectToMCPClient use), since the attempt itself runs in
// a background goroutine and — with no real server behind config's
// ConnectionString — fails fast, which is fine: only that it was attempted
// matters here.
func TestPerformCheck_NilConn_SharedAuthType_TriggersReconnect(t *testing.T) {
	manager := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)

	config := &schemas.MCPClientConfig{
		ID:               "client-reconnect-trigger",
		Name:             "shared-client",
		AuthType:         schemas.MCPAuthTypeHeaders,
		ConnectionType:   schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar("http://127.0.0.1:0/mcp"), // unreachable — attempt must still be made
	}
	manager.mu.Lock()
	manager.clientMap[config.ID] = &schemas.MCPClientState{
		Name:            config.Name,
		ExecutionConfig: config,
		State:           schemas.MCPConnectionStateUnstable,
		Conn:            nil,
	}
	manager.mu.Unlock()

	checker := NewClientConnectionChecker(manager, config.ID, time.Minute, false, &MockLogger{})
	checker.performCheck()

	require.Eventually(t, func() bool {
		_, inFlightOrDone := manager.reconnectingClients.Load(config.ID)
		return inFlightOrDone
	}, 2*time.Second, 10*time.Millisecond, "performCheck must trigger a reconnect attempt for a sticky client with no live connection")
}

// TestPerformCheck_NilConn_PerUserOAuth_SuccessUpdatesToolMap is an
// end-to-end happy-path check: when admin discovery succeeds, the
// discovered tools actually replace the client's ToolMap, using a real
// upstream HTTP MCP server (buildAdminDiscoveryHTTPServer, shared with
// admin_tool_discovery_test.go) rather than mocking the connect/discover
// cycle away.
func TestPerformCheck_NilConn_PerUserOAuth_SuccessUpdatesToolMap(t *testing.T) {
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

	checker := NewClientConnectionChecker(manager, config.ID, time.Minute, false, &MockLogger{})
	checker.performCheck()

	require.Equal(t, 1, cred.callCount())
	updated := manager.clientMap[config.ID].ToolMap
	require.Contains(t, updated, "oauth-client-echo", "a successful admin-discovery cycle should populate the client's tool map")
	require.Equal(t, schemas.MCPConnectionStateHealthy, manager.clientMap[config.ID].State)
}
