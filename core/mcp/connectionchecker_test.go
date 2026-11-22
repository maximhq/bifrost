package mcp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/server"
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
func newSyncTestClientState(clientID, clientName string, authType schemas.MCPAuthType) (*schemas.MCPClientState, *schemas.MCPClientConfig) {
	config := &schemas.MCPClientConfig{
		ID:       clientID,
		Name:     clientName,
		AuthType: authType,
	}
	state := &schemas.MCPClientState{
		Name:            clientName,
		ExecutionConfig: config,
		Conn:            nil, // every test here exercises the conn==nil branch
	}
	return state, config
}

// installSyncTestClient registers state in the manager and seeds its
// discovery in the store (live entries no longer carry tool maps).
func installSyncTestClient(manager *MCPManager, state *schemas.MCPClientState, config *schemas.MCPClientConfig, tools map[string]schemas.ChatTool) {
	manager.clientMap[config.ID] = state
	if tools != nil {
		manager.discoveryStore.SetDiscovery(context.Background(), config, &schemas.MCPDiscovery{
			Tools: tools,
		})
	}
}

// storedTools reads a client's tool map back from the manager's store.
func storedTools(manager *MCPManager, clientID string) map[string]schemas.ChatTool {
	return manager.discoveryStore.GetDiscovery(context.Background(), &schemas.MCPClientConfig{ID: clientID}).GetTools()
}

// TestPerformCheck_NilConn_PerUserOAuth_AttemptsAdminDiscovery confirms a
// per_user_oauth client with no live conn attempts admin tool discovery
// (observed here via the credStore.AdminConnectionHeaders call count, since
// RequiresPerCallConnection auth types never hold a conn to reuse in the
// first place).
func TestPerformCheck_NilConn_PerUserOAuth_AttemptsAdminDiscovery(t *testing.T) {
	cred := &fakeAdminCredStore{err: errors.New("admin credential unavailable")}
	manager := &MCPManager{
		discoveryStore: NewDiscoveryStore(nil, nil),
		credStore:      cred,
		logger:         &MockLogger{},
		clientMap:      map[string]*schemas.MCPClientState{},
	}

	existingTools := map[string]schemas.ChatTool{"oauth-client-existing": {}}
	state, config := newSyncTestClientState("client-1", "oauth-client", schemas.MCPAuthTypePerUserOauth)
	installSyncTestClient(manager, state, config, existingTools)

	checker := NewClientConnectionChecker(manager, config.ID, time.Minute, false, &MockLogger{})
	checker.performCheck()

	// performCheck wraps admin discovery in ExecuteWithRetry(ProbeRetryConfig)
	// same as every other check — this unclassified error doesn't match
	// isTransientError's permanent list, so it retries (MaxRetries=3, 4 total
	// attempts) before finally giving up and marking Unstable.
	require.Equal(t, ProbeRetryConfig.MaxRetries+1, cred.callCount(), "performCheck should attempt admin tool discovery (calling credStore.AdminConnectionHeaders), retried like every other check, instead of silently skipping")
	// Admin discovery failed, so existing tools must be kept intact (same
	// "keep existing tools on error" contract as the live-conn path).
	require.Equal(t, existingTools, storedTools(manager, config.ID))
	require.Equal(t, schemas.MCPConnectionStateUnstable, manager.clientMap[config.ID].State)
}

// TestPerformCheck_NilConn_PerUserHeaders_AttemptsAdminDiscovery is the
// per_user_headers counterpart of the test above.
func TestPerformCheck_NilConn_PerUserHeaders_AttemptsAdminDiscovery(t *testing.T) {
	cred := &fakeAdminCredStore{err: errors.New("admin credential unavailable")}
	manager := &MCPManager{
		discoveryStore: NewDiscoveryStore(nil, nil),
		credStore:      cred,
		logger:         &MockLogger{},
		clientMap:      map[string]*schemas.MCPClientState{},
	}

	existingTools := map[string]schemas.ChatTool{"headers-client-existing": {}}
	state, config := newSyncTestClientState("client-2", "headers-client", schemas.MCPAuthTypePerUserHeaders)
	installSyncTestClient(manager, state, config, existingTools)

	checker := NewClientConnectionChecker(manager, config.ID, time.Minute, false, &MockLogger{})
	checker.performCheck()

	require.Equal(t, ProbeRetryConfig.MaxRetries+1, cred.callCount(), "performCheck should attempt admin tool discovery instead of silently skipping, retried like every other check")
	require.Equal(t, existingTools, storedTools(manager, config.ID))
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
			state, config := newSyncTestClientState("client-3", "shared-client", authType)
			config.ConnectionType = schemas.MCPConnectionTypeHTTP
			config.NeedsSessionStickiness = schemas.Ptr(true)
			config.ConnectionString = schemas.NewSecretVar("http://127.0.0.1:0/mcp") // unreachable — the reconnect attempt fails fast, which is fine
			manager.mu.Lock()
			installSyncTestClient(manager, state, config, existingTools)
			manager.mu.Unlock()

			checker := NewClientConnectionChecker(manager, config.ID, time.Minute, false, &MockLogger{})
			checker.performCheck()

			require.Eventually(t, func() bool {
				_, inFlightOrDone := manager.reconnectingClients.Load(config.ID)
				return inFlightOrDone
			}, 2*time.Second, 10*time.Millisecond, "a sticky client with no live connection must trigger a direct reconnect attempt, not admin discovery")

			manager.mu.RLock()
			toolMap := storedTools(manager, config.ID)
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
				discoveryStore: NewDiscoveryStore(nil, nil),
				credStore:      cred,
				logger:         &MockLogger{},
				clientMap:      map[string]*schemas.MCPClientState{},
			}

			state, config := newSyncTestClientState("client-shared-percall", "shared-client", authType)
			config.ConnectionType = schemas.MCPConnectionTypeHTTP
			config.ConnectionString = schemas.NewSecretVar(ts.URL)
			installSyncTestClient(manager, state, config, map[string]schemas.ChatTool{})

			checker := NewClientConnectionChecker(manager, config.ID, time.Minute, false, &MockLogger{})
			checker.performCheck()

			require.Equal(t, 1, cred.callCount())
			updated := storedTools(manager, config.ID)
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
		discoveryStore: NewDiscoveryStore(nil, nil),
		credStore:      cred,
		logger:         &MockLogger{},
		clientMap:      map[string]*schemas.MCPClientState{},
	}

	state, config := newSyncTestClientState("client-4", "oauth-client", schemas.MCPAuthTypePerUserOauth)
	config.ConnectionType = schemas.MCPConnectionTypeHTTP
	config.ConnectionString = schemas.NewSecretVar(ts.URL)
	installSyncTestClient(manager, state, config, map[string]schemas.ChatTool{})

	checker := NewClientConnectionChecker(manager, config.ID, time.Minute, false, &MockLogger{})
	checker.performCheck()

	require.Equal(t, 1, cred.callCount())
	updated := storedTools(manager, config.ID)
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
		discoveryStore: NewDiscoveryStore(nil, nil),
		logger:         &MockLogger{},
		clientMap:      map[string]*schemas.MCPClientState{},
	}
	state, config := newSyncTestClientState("client-gen", "gen-client", schemas.MCPAuthTypeHeaders)
	state.State = schemas.MCPConnectionStateHealthy
	state.ConnGeneration = 6 // already bumped past what the stale check captured
	installSyncTestClient(manager, state, config, nil)

	checker := NewClientConnectionChecker(manager, config.ID, time.Minute, false, &MockLogger{})

	// A check that captured generation 5 (before the transition) finishing
	// late must not move the state at all.
	checker.setState(schemas.MCPConnectionStateUnstable, 5, schemas.MCPConnectionFailureStagePing, errors.New("stale"))
	require.Equal(t, schemas.MCPConnectionStateHealthy, manager.clientMap[config.ID].State, "a stale-generation state write must be dropped")
	require.Nil(t, manager.clientMap[config.ID].LastFailure, "a dropped state write must not leave a failure record behind either")

	// A check whose captured generation still matches the client's current
	// one must apply normally — the guard isn't a blanket no-op.
	checker.setState(schemas.MCPConnectionStateUnstable, 6, schemas.MCPConnectionFailureStagePing, errors.New("current"))
	require.Equal(t, schemas.MCPConnectionStateUnstable, manager.clientMap[config.ID].State, "a current-generation state write must still apply")
	require.NotNil(t, manager.clientMap[config.ID].LastFailure, "an applied failure write records its reason")
	require.Equal(t, "current", manager.clientMap[config.ID].LastFailure.Message)
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
	checker.recordFailure(config.Name, schemas.MCPConnectionFailureStagePing, errors.New("boom"), 0)
	require.Len(t, calls, 1)
	assert.Equal(t, config.ID, calls[0].clientID)
	assert.Equal(t, config.Name, calls[0].name)
	assert.Equal(t, schemas.MCPConnectionStateHealthy, calls[0].oldState)
	assert.Equal(t, schemas.MCPConnectionStateUnstable, calls[0].newState)

	// Unstable -> Unstable: a no-op, must not fire again.
	checker.recordFailure(config.Name, schemas.MCPConnectionFailureStagePing, errors.New("boom again"), 0)
	require.Len(t, calls, 1, "a repeat failure that doesn't change state must not re-fire the callback")

	// Unstable -> Healthy: a genuine transition, must fire again.
	checker.recordSuccess(config.Name, 0)
	require.Len(t, calls, 2)
	assert.Equal(t, schemas.MCPConnectionStateUnstable, calls[1].oldState)
	assert.Equal(t, schemas.MCPConnectionStateHealthy, calls[1].newState)
}

// Kick runs a check ahead of the timer (10 minutes once healthy), and a burst of kicks costs one
// check plus at most one follow-up.
func TestClientConnectionChecker_KickChecksAheadOfTheTimer(t *testing.T) {
	counter := newWireCounter()
	ts := httptest.NewServer(counter.wrap(server.NewStreamableHTTPServer(newMatrixUpstream())))
	t.Cleanup(ts.Close)
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	t.Cleanup(func() { _ = m.Cleanup() })
	config := &schemas.MCPClientConfig{
		ID: "kick", Name: "matrix", ConnectionType: schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar(ts.URL), NeedsSessionStickiness: schemas.Ptr(true), ToolsToExecute: []string{"*"},
	}
	require.NoError(t, m.connectToMCPClient(context.Background(), config))
	// Settle any re-list the connect itself triggers (the 2026-07-28 listen stream's acknowledgement).
	time.Sleep(200 * time.Millisecond)
	before := counter.count("tools/list")

	m.checkerManager.Kick(config.ID)
	require.Eventually(t, func() bool { return counter.count("tools/list") == before+1 }, 2*time.Second, 10*time.Millisecond, "a kick runs a check now")

	before = counter.count("tools/list")
	for range 20 {
		m.checkerManager.Kick(config.ID)
	}
	require.Eventually(t, func() bool { return counter.count("tools/list") > before }, 2*time.Second, 10*time.Millisecond)
	time.Sleep(300 * time.Millisecond)
	assert.LessOrEqual(t, counter.count("tools/list")-before, 2, "20 kicks collapse into one check and one follow-up")

	m.checkerManager.Kick("no-such-client") // no checker: nothing happens
}

// TestClientConnectionCheckerStart_DoesNotDeadlockAgainstAStop: RemoveClient and
// DisableClient hold MCPManager.mu while StopChecking waits for the checker's own
// lock, so Start must not hold that lock while it waits for MCPManager.mu, or the
// two wait on each other forever. A checker stopped before it got going does not
// start afterwards, whether its client was removed or only disabled, since nothing
// could stop it again.
func TestClientConnectionCheckerStart_DoesNotDeadlockAgainstAStop(t *testing.T) {
	for _, tc := range []struct {
		name string
		stop func(m *MCPManager, id string) // runs with MCPManager.mu held, as in RemoveClient or DisableClient
	}{
		{"removed", func(m *MCPManager, id string) { delete(m.clientMap, id) }},
		{"disabled", func(m *MCPManager, id string) { m.clientMap[id].State = schemas.MCPConnectionStateDisabled }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
			defer m.checkerManager.StopAll()
			config := newVerifiedClientConfig("client", schemas.MCPAuthTypePerUserHeaders, map[string]schemas.ChatTool{})
			m.mu.Lock()
			m.clientMap[config.ID] = &schemas.MCPClientState{
				Name:            config.Name,
				ExecutionConfig: config,
				State:           schemas.MCPConnectionStateHealthy,
				ConnectionInfo:  &schemas.MCPClientConnectionInfo{Type: config.ConnectionType},
			}
			m.mu.Unlock()
			checker := NewClientConnectionChecker(m, config.ID, time.Hour, true, m.logger)

			m.mu.Lock()
			started := make(chan struct{})
			go func() { m.checkerManager.StartChecking(checker); close(started) }()
			time.Sleep(100 * time.Millisecond) // Start is now waiting for MCPManager.mu
			tc.stop(m, config.ID)
			stopped := make(chan struct{})
			go func() { m.checkerManager.StopChecking(config.ID); close(stopped) }()
			select {
			case <-stopped:
				m.mu.Unlock()
			case <-time.After(2 * time.Second):
				m.mu.Unlock() // let both goroutines finish before failing
				<-stopped
				<-started
				t.Fatal("StopChecking under MCPManager.mu deadlocked against a starting checker")
			}
			<-started
			assert.False(t, checkerRunning(checker), "a checker stopped before it started stays stopped")
		})
	}
}

// TestUpdateClient_PingEditReachesTheRunningChecker: an is_ping_available edit
// reaches the client's running checker, so its next check stops pinging. The
// checker used to keep the value it was built with until a reconnect replaced
// it. The client is capped at 2025-06-18, a revision whose check still pings.
func TestUpdateClient_PingEditReachesTheRunningChecker(t *testing.T) {
	var pings, lists atomic.Int32
	upstream := server.NewStreamableHTTPServer(server.NewMCPServer("ping-edit", "1.0.0", server.WithToolCapabilities(true)))
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		if bytes.Contains(body, []byte(`"method":"ping"`)) {
			pings.Add(1)
		}
		if bytes.Contains(body, []byte(`"method":"tools/list"`)) {
			lists.Add(1)
		}
		upstream.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	t.Cleanup(func() { _ = m.Cleanup() })
	config := &schemas.MCPClientConfig{
		ID:                     "ping-edit",
		Name:                   "ping_edit",
		ConnectionType:         schemas.MCPConnectionTypeHTTP,
		ConnectionString:       schemas.NewSecretVar(ts.URL),
		AuthType:               schemas.MCPAuthTypeNone,
		NeedsSessionStickiness: schemas.Ptr(true),
		MaxProtocolVersion:     "2025-06-18",
	}
	require.NoError(t, m.AddClient(context.Background(), config))

	// checkNow kicks one check and reports how many pings it sent.
	checkNow := func() int32 {
		listsBefore, pingsBefore := lists.Load(), pings.Load()
		m.checkerManager.Kick(config.ID)
		require.Eventually(t, func() bool { return lists.Load() > listsBefore }, 5*time.Second, 20*time.Millisecond, "the kicked check never listed tools")
		return pings.Load() - pingsBefore
	}
	require.Equal(t, int32(1), checkNow(), "a check pings while is_ping_available is on")

	edited := *config
	edited.IsPingAvailable = schemas.Ptr(false)
	require.NoError(t, m.UpdateClient(config.ID, &edited))
	assert.Equal(t, int32(0), checkNow(), "the next check sends no ping")
}

// TestReconnectClient_FailedReconnectChecksAgainSoon: a manual reconnect that
// fails keeps the client's old, still working connection and marks it
// unstable, and the checker moves onto the unstable cadence: the next check
// runs within UnstableConnectionCheckInterval, finds the old connection
// working and marks the client healthy. It used to wait out the full steady
// interval, ten minutes by default, still showing the failed reconnect. The
// client is capped at 2025-06-18 so its requests carry a session the upstream
// can tell apart from a new connection's.
func TestReconnectClient_FailedReconnectChecksAgainSoon(t *testing.T) {
	upstream := server.NewStreamableHTTPServer(server.NewMCPServer("failed-reconnect", "1.0.0", server.WithToolCapabilities(true)))
	var refuseNewConnections atomic.Bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if refuseNewConnections.Load() && r.Header.Get("Mcp-Session-Id") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		upstream.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	t.Cleanup(func() { _ = m.Cleanup() })
	config := &schemas.MCPClientConfig{
		ID:                     "failed-reconnect",
		Name:                   "failed_reconnect",
		ConnectionType:         schemas.MCPConnectionTypeHTTP,
		ConnectionString:       schemas.NewSecretVar(ts.URL),
		AuthType:               schemas.MCPAuthTypeNone,
		NeedsSessionStickiness: schemas.Ptr(true),
		MaxProtocolVersion:     "2025-06-18",
	}
	require.NoError(t, m.AddClient(context.Background(), config))

	refuseNewConnections.Store(true)
	require.Error(t, m.ReconnectClient(config.ID))
	state, ok := snapshotAddClientState(m, config.ID)
	require.True(t, ok)
	require.Equal(t, schemas.MCPConnectionStateUnstable, state.State)
	require.NotNil(t, state.LastFailure)
	require.Equal(t, schemas.MCPConnectionFailureStageConnect, state.LastFailure.Stage)

	assert.Eventually(t, func() bool {
		state, _ := snapshotAddClientState(m, config.ID)
		return state.State == schemas.MCPConnectionStateHealthy
	}, UnstableConnectionCheckInterval+5*time.Second, 100*time.Millisecond, "the next check runs on the unstable cadence and finds the old connection working")
}

// TestHandleSSEConnectionLost_MovesTheCheckerOntoTheUnstableCadence: a dropped
// SSE stream marks the client unstable and moves its checker off the steady
// cadence, so the reconnect the next check starts comes within
// UnstableConnectionCheckInterval rather than a full steady interval later.
func TestHandleSSEConnectionLost_MovesTheCheckerOntoTheUnstableCadence(t *testing.T) {
	ts := httptest.NewServer(server.NewSSEServer(server.NewMCPServer("sse-lost", "1.0.0", server.WithToolCapabilities(true))))
	t.Cleanup(ts.Close)
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	t.Cleanup(func() { _ = m.Cleanup() })
	config := &schemas.MCPClientConfig{
		ID:               "sse-lost",
		Name:             "sse_lost",
		ConnectionType:   schemas.MCPConnectionTypeSSE,
		ConnectionString: schemas.NewSecretVar(ts.URL + "/sse"),
		AuthType:         schemas.MCPAuthTypeNone,
	}
	require.NoError(t, m.AddClient(context.Background(), config))
	m.checkerManager.mu.RLock()
	checker := m.checkerManager.checkers[config.ID]
	m.checkerManager.mu.RUnlock()
	require.NotNil(t, checker)
	onSteady := func() bool {
		checker.mu.Lock()
		defer checker.mu.Unlock()
		return checker.onSteadyCadence
	}
	require.True(t, onSteady(), "a healthy client's checker starts on the steady cadence")

	state, _ := snapshotAddClientState(m, config.ID)
	m.handleSSEConnectionLost(config.ID, config.Name, state.ConnGeneration, errors.New("stream dropped"))
	state, _ = snapshotAddClientState(m, config.ID)
	require.Equal(t, schemas.MCPConnectionStateUnstable, state.State)
	assert.False(t, onSteady(), "the checker is on the unstable cadence")
}
