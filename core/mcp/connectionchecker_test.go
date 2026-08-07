package mcp

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
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

// TestPerformCheck_NilConn_SharedAuthType_Sticky_NeverAttemptsAdminDiscovery
// is the regression-risk case for a genuinely sticky shared client (e.g.
// "headers" or "none") with Conn==nil because it's mid-(re)connect, not
// per-call. performCheck must never route it into admin discovery — this
// branch instead attempts a direct reconnect, covered separately by
// TestPerformCheck_NilConn_SharedAuthType_TriggersReconnect below. Uses the
// real credstore.CredStore (not a hardcoded-per-call fake) so the routing
// decision reflects actual NeedsSessionStickiness semantics: a shared type
// with needs_session_stickiness=false is legitimately per-call and DOES
// attempt admin discovery now — see
// TestPerformCheck_NilConn_SharedAuthType_PerCall_SuccessUpdatesToolMap.
func TestPerformCheck_NilConn_SharedAuthType_Sticky_NeverAttemptsAdminDiscovery(t *testing.T) {
	for _, authType := range []schemas.MCPAuthType{schemas.MCPAuthTypeHeaders, schemas.MCPAuthTypeNone, schemas.MCPAuthTypeOauth} {
		t.Run(string(authType), func(t *testing.T) {
			manager := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)

			existingTools := map[string]schemas.ChatTool{"shared-client-existing": {}}
			state, config := newSyncTestClientState("client-3", "shared-client", authType, existingTools)
			config.ConnectionType = schemas.MCPConnectionTypeHTTP
			config.NeedsSessionStickiness = schemas.Ptr(true)
			config.ConnectionString = schemas.NewSecretVar("http://127.0.0.1:0/mcp") // unreachable — the reconnect attempt fails fast, which is fine
			manager.mu.Lock()
			manager.clientMap[config.ID] = state
			manager.mu.Unlock()

			checker := NewClientConnectionChecker(manager, config.ID, time.Minute, false, &MockLogger{})
			checker.performCheck()

			require.Eventually(t, func() bool {
				_, inFlightOrDone := manager.reconnectingClients.Load(config.ID)
				return inFlightOrDone
			}, 2*time.Second, 10*time.Millisecond, "a sticky client with no live connection must trigger a direct reconnect attempt, not admin discovery")

			manager.mu.RLock()
			toolMap := manager.clientMap[config.ID].ToolMap
			manager.mu.RUnlock()
			require.Equal(t, existingTools, toolMap, "must leave the existing tool map untouched — only a real reconnect's own tools/list replaces it")
		})
	}
}

// TestPerformCheck_NilConn_SharedAuthType_PerCall_SuccessUpdatesToolMap pins
// the fix for the shared-per-call tool-discovery bug at the full
// performCheck level (not just performAdminToolDiscovery in isolation): a
// shared oauth/headers/none client running per-call
// (needs_session_stickiness nil/false) must have its tool map populated by
// the periodic checker, exactly like a per_user_oauth client already does
// (TestPerformCheck_NilConn_PerUserOAuth_SuccessUpdatesToolMap).
func TestPerformCheck_NilConn_SharedAuthType_PerCall_SuccessUpdatesToolMap(t *testing.T) {
	for _, authType := range []schemas.MCPAuthType{schemas.MCPAuthTypeHeaders, schemas.MCPAuthTypeOauth} {
		t.Run(string(authType), func(t *testing.T) {
			ts, _ := buildAdminDiscoveryHTTPServer(t)

			cred := &fakeAdminCredStore{headers: http.Header{"Authorization": []string{"Bearer shared-token"}}}
			manager := &MCPManager{
				credStore: cred,
				logger:    &MockLogger{},
				clientMap: map[string]*schemas.MCPClientState{},
			}

			state, config := newSyncTestClientState("client-shared-percall", "shared-client", authType, map[string]schemas.ChatTool{})
			config.ConnectionType = schemas.MCPConnectionTypeHTTP
			config.ConnectionString = schemas.NewSecretVar(ts.URL)
			manager.clientMap[config.ID] = state

			checker := NewClientConnectionChecker(manager, config.ID, time.Minute, false, &MockLogger{})
			checker.performCheck()

			require.Equal(t, 1, cred.callCount())
			updated := manager.clientMap[config.ID].ToolMap
			require.Contains(t, updated, "shared-client-echo", "a successful admin-discovery cycle should populate the client's tool map")
			require.Equal(t, schemas.MCPConnectionStateHealthy, manager.clientMap[config.ID].State)
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
		ID:                     "client-reconnect-trigger",
		Name:                   "shared-client",
		AuthType:               schemas.MCPAuthTypeHeaders,
		ConnectionType:         schemas.MCPConnectionTypeHTTP,
		ConnectionString:       schemas.NewSecretVar("http://127.0.0.1:0/mcp"), // unreachable — attempt must still be made
		NeedsSessionStickiness: schemas.Ptr(true),                              // sticky: routes through the reconnect-on-nil-conn branch under test, not checkPerCall
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

// TestSetState_StaleGeneration_DoesNotOverwriteState pins the gap CodeRabbit
// flagged: recordFailure/recordSuccess (via setState) had no generation
// guard at all, unlike writeBackTools' existing one for tool-map writes. A
// check that started before a sticky->per-call transition (which closes the
// connection but, before this fix, never bumped ConnGeneration) can complete
// afterward — StopChecking doesn't cancel an in-flight check — and its state
// write would land on an entry that's since moved on, e.g. marking Unstable
// a client that no longer has a persistent connection to be unstable about.
// setState must drop a write whose captured generation no longer matches the
// client's current one, mirroring writeBackTools' own guard exactly.
func TestSetState_StaleGeneration_DoesNotOverwriteState(t *testing.T) {
	manager := &MCPManager{
		logger:    &MockLogger{},
		clientMap: map[string]*schemas.MCPClientState{},
	}
	state, config := newSyncTestClientState("client-gen", "gen-client", schemas.MCPAuthTypeHeaders, nil)
	state.State = schemas.MCPConnectionStateHealthy
	state.ConnGeneration = 6 // already bumped past what the stale check captured
	manager.clientMap[config.ID] = state

	checker := NewClientConnectionChecker(manager, config.ID, time.Minute, false, &MockLogger{})

	// A check that captured generation 5 (before the transition) finishing
	// late must not move the state at all.
	checker.setState(schemas.MCPConnectionStateUnstable, 5)
	require.Equal(t, schemas.MCPConnectionStateHealthy, manager.clientMap[config.ID].State, "a stale-generation state write must be dropped")

	// A check whose captured generation still matches the client's current
	// one must apply normally — the guard isn't a blanket no-op.
	checker.setState(schemas.MCPConnectionStateUnstable, 6)
	require.Equal(t, schemas.MCPConnectionStateUnstable, manager.clientMap[config.ID].State, "a current-generation state write must still apply")
}

// TestSetState_FiresStateChangeCallbackOnGenuineTransition verifies
// ClientConnectionChecker.setState — the single writer every periodic-check
// transition in this file funnels through — fires the manager's registered
// state-change callback exactly once per genuine transition, with the
// correct old/new states, and not at all when the target state matches the
// current one.
func TestSetState_FiresStateChangeCallbackOnGenuineTransition(t *testing.T) {
	manager := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	config := &schemas.MCPClientConfig{ID: "client-checker-callback", Name: "checker-callback-client"}

	type call struct {
		clientID, name     string
		oldState, newState schemas.MCPConnectionState
	}
	var calls []call
	manager.SetStateChangeCallback(func(clientID, name string, oldState, newState schemas.MCPConnectionState) {
		calls = append(calls, call{clientID, name, oldState, newState})
	})

	manager.mu.Lock()
	manager.clientMap[config.ID] = &schemas.MCPClientState{
		Name:            config.Name,
		ExecutionConfig: config,
		State:           schemas.MCPConnectionStateHealthy,
	}
	manager.mu.Unlock()

	checker := NewClientConnectionChecker(manager, config.ID, time.Minute, false, &MockLogger{})

	// Healthy -> Unstable: a genuine transition, must fire once. Generation 0
	// matches this client's zero-value ConnGeneration (never bumped here).
	checker.recordFailure(config.Name, "ping", errors.New("boom"), 0)
	require.Len(t, calls, 1)
	assert.Equal(t, config.ID, calls[0].clientID)
	assert.Equal(t, config.Name, calls[0].name)
	assert.Equal(t, schemas.MCPConnectionStateHealthy, calls[0].oldState)
	assert.Equal(t, schemas.MCPConnectionStateUnstable, calls[0].newState)

	// Unstable -> Unstable: a no-op, must not fire again.
	checker.recordFailure(config.Name, "ping", errors.New("boom again"), 0)
	require.Len(t, calls, 1, "a repeat failure that doesn't change state must not re-fire the callback")

	// Unstable -> Healthy: a genuine transition, must fire again.
	checker.recordSuccess(config.Name, 0)
	require.Len(t, calls, 2)
	assert.Equal(t, schemas.MCPConnectionStateUnstable, calls[1].oldState)
	assert.Equal(t, schemas.MCPConnectionStateHealthy, calls[1].newState)
}
