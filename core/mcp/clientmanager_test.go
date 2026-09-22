package mcp

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateSTDIOConnectionAllowsInlineEnvAssignments(t *testing.T) {
	t.Parallel()

	config := &schemas.MCPClientConfig{
		Name:           "test-stdio-client",
		ConnectionType: schemas.MCPConnectionTypeSTDIO,
		StdioConfig: &schemas.MCPStdioConfig{
			Command: "echo",
			Envs:    []string{"TEST_STDIO_ENV_ASSIGNMENT=inline-value"},
		},
	}

	_, _, err := (&MCPManager{}).createSTDIOConnection(context.Background(), config, nil)
	require.NoError(t, err)
}

func TestCreateSTDIOConnectionAllowsSetReferencedEnvVars(t *testing.T) {
	t.Setenv("TEST_STDIO_ENV_REFERENCE_SET", "set-value")

	config := &schemas.MCPClientConfig{
		Name:           "test-stdio-client",
		ConnectionType: schemas.MCPConnectionTypeSTDIO,
		StdioConfig: &schemas.MCPStdioConfig{
			Command: "echo",
			Envs:    []string{"TEST_STDIO_ENV_REFERENCE_SET"},
		},
	}

	_, _, err := (&MCPManager{}).createSTDIOConnection(context.Background(), config, nil)
	require.NoError(t, err)
}

func TestCreateSTDIOConnectionRequiresReferencedEnvVars(t *testing.T) {
	t.Setenv("TEST_STDIO_ENV_REFERENCE_MISSING", "")

	config := &schemas.MCPClientConfig{
		Name:           "test-stdio-client",
		ConnectionType: schemas.MCPConnectionTypeSTDIO,
		StdioConfig: &schemas.MCPStdioConfig{
			Command: "echo",
			Envs:    []string{"TEST_STDIO_ENV_REFERENCE_MISSING"},
		},
	}

	_, _, err := (&MCPManager{}).createSTDIOConnection(context.Background(), config, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "environment variable TEST_STDIO_ENV_REFERENCE_MISSING is not set")
}

func TestCreateSTDIOConnectionRejectsEmptyEnvAssignmentName(t *testing.T) {
	t.Parallel()

	config := &schemas.MCPClientConfig{
		Name:           "test-stdio-client",
		ConnectionType: schemas.MCPConnectionTypeSTDIO,
		StdioConfig: &schemas.MCPStdioConfig{
			Command: "echo",
			Envs:    []string{"=inline-value"},
		},
	}

	_, _, err := (&MCPManager{}).createSTDIOConnection(context.Background(), config, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "environment variable name is empty")
}

// TestCloseAndMarkNeedsReauth_ClosesLiveConnectionAndFlipsState covers the
// core behavior this method exists for: after OAuth credential rotation, a
// shared client's live connection (still bound to the now-invalidated
// Authorization header) must be torn down and the entry flipped to
// needs_reauth, without attempting a new dial.
func TestCloseAndMarkNeedsReauth_ClosesLiveConnectionAndFlipsState(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, expiredOAuthCredStore{}, nil, nil)
	config := newSharedOAuthClientConfig("client-rotated")

	cancelCalled := false
	m.mu.Lock()
	m.clientMap[config.ID] = &schemas.MCPClientState{
		Name:            config.Name,
		ExecutionConfig: config,
		State:           schemas.MCPConnectionStateHealthy,
		CancelFunc:      func() { cancelCalled = true },
	}
	m.mu.Unlock()

	require.NoError(t, m.CloseAndMarkNeedsReauth("client-rotated"))

	m.mu.RLock()
	state := *m.clientMap["client-rotated"]
	m.mu.RUnlock()

	assert.True(t, cancelCalled, "the live connection's cancel func must be invoked")
	assert.Nil(t, state.CancelFunc)
	assert.Nil(t, state.Conn)
	assert.Equal(t, schemas.MCPConnectionStateNeedsReauth, state.State)
	require.NotNil(t, state.LastFailure, "needs_reauth carries its own explanation")
	assert.Equal(t, schemas.MCPConnectionFailureStageCredential, state.LastFailure.Stage)
	assert.Equal(t, errCredentialRotated.Error(), state.LastFailure.Message)
}

// TestFailConnectAttempt_NeedsReauthTransition_FiresStateChangeCallback
// verifies that failConnectAttempt's reactive classification of a connect
// failure as permanent (needsReauth=true) fires the registered state-change
// callback with the correct old/new states — this transition (unlike
// CloseAndMarkNeedsReauth) has no admin-API call site of its own for a
// caller to observe the change through otherwise.
func TestFailConnectAttempt_NeedsReauthTransition_FiresStateChangeCallback(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, expiredOAuthCredStore{}, nil, nil)
	config := newSharedOAuthClientConfig("client-fail-reauth")

	type call struct {
		clientID, name     string
		oldState, newState schemas.MCPConnectionState
	}
	var calls []call
	m.SetStateChangeCallback(func(clientID, name string, oldState, newState schemas.MCPConnectionState) {
		calls = append(calls, call{clientID, name, oldState, newState})
	})

	entry := &schemas.MCPClientState{
		Name:            config.Name,
		ExecutionConfig: config,
		State:           schemas.MCPConnectionStateUnstable,
	}
	m.mu.Lock()
	m.clientMap[config.ID] = entry
	m.mu.Unlock()

	m.failConnectAttempt(entry, config, nil, nil, nil, nil, true, errors.New("oauth2 token expired"))

	require.Len(t, calls, 1, "the callback must fire exactly once for a genuine state transition")
	assert.Equal(t, config.ID, calls[0].clientID)
	assert.Equal(t, config.Name, calls[0].name)
	assert.Equal(t, schemas.MCPConnectionStateUnstable, calls[0].oldState)
	assert.Equal(t, schemas.MCPConnectionStateNeedsReauth, calls[0].newState)

	m.mu.RLock()
	state := m.clientMap[config.ID].State
	m.mu.RUnlock()
	assert.Equal(t, schemas.MCPConnectionStateNeedsReauth, state)
}

// TestFailConnectAttempt_NoStateChange_DoesNotFireCallback verifies the
// callback is a pure transition signal — reclassifying a client that's
// already Unstable as Unstable again must not fire it.
func TestFailConnectAttempt_NoStateChange_DoesNotFireCallback(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, expiredOAuthCredStore{}, nil, nil)
	config := newSharedOAuthClientConfig("client-no-change")

	fired := false
	m.SetStateChangeCallback(func(clientID, name string, oldState, newState schemas.MCPConnectionState) {
		fired = true
	})

	entry := &schemas.MCPClientState{
		Name:            config.Name,
		ExecutionConfig: config,
		State:           schemas.MCPConnectionStateUnstable,
	}
	m.mu.Lock()
	m.clientMap[config.ID] = entry
	m.mu.Unlock()

	m.failConnectAttempt(entry, config, nil, nil, nil, nil, false, errors.New("connection refused"))

	assert.False(t, fired, "the callback must not fire when the transition is a no-op")
}

// TestCloseAndMarkNeedsReauth_MissingClient_Errors covers the not-found path.
func TestCloseAndMarkNeedsReauth_MissingClient_Errors(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, expiredOAuthCredStore{}, nil, nil)
	require.Error(t, m.CloseAndMarkNeedsReauth("does-not-exist"))
}

// TestCloseAndMarkNeedsReauth_PerUserAuth_ReturnsNotApplicable covers
// per_user_oauth/per_user_headers clients, which hold no persistent shared
// connection: rotation for these auth types must not error out the caller
// (the HTTP handler filters ErrMCPReconnectNotApplicable), and must not
// touch the entry's state.
func TestCloseAndMarkNeedsReauth_PerUserAuth_ReturnsNotApplicable(t *testing.T) {
	// nil credStore: NewMCPManager defaults to a real credstore.CredStore,
	// whose RequiresPerCallConnection actually varies by auth type — unlike
	// expiredOAuthCredStore (used by the other tests here), which hardcodes
	// false regardless of AuthType.
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, nil, nil)
	config := &schemas.MCPClientConfig{ID: "client-per-user", Name: "per-user-client", AuthType: schemas.MCPAuthTypePerUserOauth}

	m.mu.Lock()
	m.clientMap[config.ID] = &schemas.MCPClientState{
		Name:            config.Name,
		ExecutionConfig: config,
		State:           schemas.MCPConnectionStateHealthy,
	}
	m.mu.Unlock()

	err := m.CloseAndMarkNeedsReauth(config.ID)
	require.Error(t, err)
	assert.True(t, errors.Is(err, schemas.ErrMCPReconnectNotApplicable))

	m.mu.RLock()
	state := *m.clientMap[config.ID]
	m.mu.RUnlock()
	assert.Equal(t, schemas.MCPConnectionStateHealthy, state.State, "state must be untouched for a not-applicable auth type")
}

// TestCloseAndMarkNeedsReauth_Disabled_IsNoOp covers a rotation racing a
// disable: DisableClient's state is authoritative and must not be
// resurrected into needs_reauth by a rotation that started before it.
func TestCloseAndMarkNeedsReauth_Disabled_IsNoOp(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, expiredOAuthCredStore{}, nil, nil)
	config := newSharedOAuthClientConfig("client-disabled")

	m.mu.Lock()
	m.clientMap[config.ID] = &schemas.MCPClientState{
		Name:            config.Name,
		ExecutionConfig: config,
		State:           schemas.MCPConnectionStateDisabled,
	}
	m.mu.Unlock()

	require.NoError(t, m.CloseAndMarkNeedsReauth("client-disabled"))

	m.mu.RLock()
	state := *m.clientMap["client-disabled"]
	m.mu.RUnlock()
	assert.Equal(t, schemas.MCPConnectionStateDisabled, state.State)
}

// TestEnableClient_GuardLostLeavesDisabledUntouched pins the ordering
// CodeRabbit flagged: EnableClient must acquire the exclusive per-client
// operation guard (beginExclusiveClientOp) before flipping
// ExecutionConfig.Disabled, not after. Flipping it first and only then
// losing the guard to a concurrent operation (e.g. ReconnectClient) would
// leave the entry looking enabled to that other caller's connectToMCPClient
// call while State is still Disabled, letting it dial the entry out from
// under this failed EnableClient — which then returns "already in progress"
// even though the client was actually just connected by the race winner.
func TestEnableClient_GuardLostLeavesDisabledUntouched(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, expiredOAuthCredStore{}, nil, nil)
	config := newSharedOAuthClientConfig("client-enable-race")
	config.Disabled = true

	m.mu.Lock()
	m.clientMap[config.ID] = &schemas.MCPClientState{
		Name:            config.Name,
		ExecutionConfig: config,
		State:           schemas.MCPConnectionStateDisabled,
	}
	m.mu.Unlock()

	// Simulate a concurrent operation (e.g. ReconnectClient) already holding
	// the exclusive guard for this client when EnableClient is called.
	finish, ok := m.beginExclusiveClientOp(config.ID)
	require.True(t, ok)
	defer finish(nil)

	err := m.EnableClient(config.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already in progress")

	m.mu.RLock()
	state := *m.clientMap[config.ID]
	m.mu.RUnlock()
	assert.True(t, state.ExecutionConfig.Disabled, "a failed EnableClient (guard already held elsewhere) must not have flipped Disabled — otherwise a concurrent connectToMCPClient could dial the still-Disabled-State entry")
	assert.Equal(t, schemas.MCPConnectionStateDisabled, state.State)
}

// TestUpdateClientCredentials_PerCallShared_Healthy_RefreshesToolsSynchronously
// pins the per-call half of the reauthorize symmetry: a sticky client's
// credential update goes through connectToMCPClient, which always re-runs
// tool discovery, while a per-call shared client used to return the
// not-applicable sentinel and keep serving its stale tool list until the
// periodic checker's next tick (minutes away on a Healthy client). A
// credential update on an already-verified per-call shared client must
// instead refresh tools synchronously with the now-current credential.
func TestUpdateClientCredentials_PerCallShared_Healthy_RefreshesToolsSynchronously(t *testing.T) {
	ts, _ := buildAdminDiscoveryHTTPServer(t)

	// nil credStore: NewMCPManager defaults to a real credstore.CredStore,
	// whose RequiresPerCallConnection actually ANDs auth type with
	// NeedsSessionStickiness (unlike expiredOAuthCredStore, used elsewhere
	// in this file, which hardcodes false regardless of either).
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, nil, nil)
	config := &schemas.MCPClientConfig{
		ID:               "client-shared-percall-refresh",
		Name:             "shared-percall-client-refresh",
		AuthType:         schemas.MCPAuthTypeHeaders,
		ConnectionType:   schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar(ts.URL),
		Headers:          map[string]schemas.SecretVar{"Authorization": *schemas.NewSecretVar("Bearer rotated-token")},
		// NeedsSessionStickiness left nil on purpose: the default value.
	}

	m.mu.Lock()
	m.clientMap[config.ID] = &schemas.MCPClientState{
		Name:            config.Name,
		ExecutionConfig: config,
		State:           schemas.MCPConnectionStateHealthy,
		ToolMap:         make(map[string]schemas.ChatTool),
		ToolNameMapping: make(map[string]string),
	}
	m.mu.Unlock()

	err := m.UpdateClientCredentials(config.ID, config)
	require.NoError(t, err, "a per-call shared client's credential update must succeed by refreshing tools, not report not-applicable")

	m.mu.RLock()
	state := *m.clientMap[config.ID]
	m.mu.RUnlock()
	assert.Contains(t, state.ToolMap, "shared-percall-client-refresh-echo", "tools must be refreshed synchronously, not deferred to the periodic checker")
}

// TestSetClientTools_ReplacesStaleTools pins SetClientTools' replace
// semantics: every caller passes a complete discovery result, so a tool the
// upstream removed between discoveries must disappear from the in-memory
// ToolMap. The old merge behavior kept such tools alive in memory (and on
// the hosted MCP surface, which serves from ToolMap) while the DB, persisted
// from the passed-in set via the tools-change callback, had already dropped
// them.
func TestSetClientTools_ReplacesStaleTools(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, nil, nil)
	config := &schemas.MCPClientConfig{ID: "client-replace-tools", Name: "replace-tools-client"}

	m.mu.Lock()
	m.clientMap[config.ID] = &schemas.MCPClientState{
		Name:            config.Name,
		ExecutionConfig: config,
		State:           schemas.MCPConnectionStateHealthy,
		ToolMap: map[string]schemas.ChatTool{
			"replace-tools-client-removed": {},
		},
		ToolNameMapping: map[string]string{"replace-tools-client-removed": "removed"},
	}
	m.mu.Unlock()

	m.SetClientTools(config.ID,
		map[string]schemas.ChatTool{"replace-tools-client-kept": {}},
		map[string]string{"replace-tools-client-kept": "kept"},
	)

	m.mu.RLock()
	state := *m.clientMap[config.ID]
	m.mu.RUnlock()
	assert.Contains(t, state.ToolMap, "replace-tools-client-kept")
	assert.NotContains(t, state.ToolMap, "replace-tools-client-removed", "a tool absent from the fresh discovery result must not survive in memory")
	assert.Equal(t, map[string]string{"replace-tools-client-kept": "kept"}, state.ToolNameMapping)
}

// TestUpdateClientCredentials_PerCallSharedOAuth_DiscoveryFailureFailsUpdate
// pins the strict half of the same symmetry: a sticky reconnect whose
// tools/list fails is a failed reconnect, so a per-call refresh whose
// discovery fails must fail the update with a real error, not the
// not-applicable sentinel; the periodic checker retries on its own cadence
// afterwards. No OAuth provider is configured here, so resolving the shared
// credential for discovery fails deterministically.
func TestUpdateClientCredentials_PerCallSharedOAuth_DiscoveryFailureFailsUpdate(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, nil, nil)
	config := &schemas.MCPClientConfig{
		ID:             "client-shared-percall",
		Name:           "shared-percall-client",
		AuthType:       schemas.MCPAuthTypeOauth,
		ConnectionType: schemas.MCPConnectionTypeHTTP,
		// NeedsSessionStickiness left nil on purpose: the default value.
	}

	m.mu.Lock()
	m.clientMap[config.ID] = &schemas.MCPClientState{
		Name:            config.Name,
		ExecutionConfig: config,
		State:           schemas.MCPConnectionStateHealthy,
	}
	m.mu.Unlock()

	err := m.UpdateClientCredentials(config.ID, config)
	require.Error(t, err)
	assert.False(t, errors.Is(err, schemas.ErrMCPReconnectNotApplicable), "a failed tool refresh is a real failure, not the not-applicable no-op")
	assert.Contains(t, err.Error(), "tool discovery")
}

// TestUpdateClientCredentials_PerCallPerUser_ReturnsReconnectNotApplicable
// pins that per-user auth types keep the not-applicable sentinel: the
// credentials updated through here are never the retained admin discovery
// credential (the admin-verify paths own that flow and re-discover
// themselves), so there is genuinely nothing to refresh.
func TestUpdateClientCredentials_PerCallPerUser_ReturnsReconnectNotApplicable(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, nil, nil)
	config := &schemas.MCPClientConfig{
		ID:             "client-per-user-creds",
		Name:           "per-user-creds-client",
		AuthType:       schemas.MCPAuthTypePerUserOauth,
		ConnectionType: schemas.MCPConnectionTypeHTTP,
	}

	m.mu.Lock()
	m.clientMap[config.ID] = &schemas.MCPClientState{
		Name:            config.Name,
		ExecutionConfig: config,
		State:           schemas.MCPConnectionStateHealthy,
	}
	m.mu.Unlock()

	err := m.UpdateClientCredentials(config.ID, config)
	require.Error(t, err)
	assert.True(t, errors.Is(err, schemas.ErrMCPReconnectNotApplicable))
}

// TestUpdateClientCredentials_PerCallShared_Disabled_SkipsRefresh pins the
// Disabled guard on the per-call refresh: SetClientTools forces a client
// back to Healthy, so a disabled client must keep the sentinel (the fresh
// credential is picked up when the client is re-enabled, which runs its own
// discovery) rather than get resurrected by a credential update.
func TestUpdateClientCredentials_PerCallShared_Disabled_SkipsRefresh(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, nil, nil)
	config := &schemas.MCPClientConfig{
		ID:             "client-shared-percall-disabled",
		Name:           "shared-percall-client-disabled",
		AuthType:       schemas.MCPAuthTypeOauth,
		ConnectionType: schemas.MCPConnectionTypeHTTP,
	}

	m.mu.Lock()
	m.clientMap[config.ID] = &schemas.MCPClientState{
		Name:            config.Name,
		ExecutionConfig: config,
		State:           schemas.MCPConnectionStateDisabled,
	}
	m.mu.Unlock()

	err := m.UpdateClientCredentials(config.ID, config)
	require.Error(t, err)
	assert.True(t, errors.Is(err, schemas.ErrMCPReconnectNotApplicable))

	m.mu.RLock()
	state := *m.clientMap[config.ID]
	m.mu.RUnlock()
	assert.Equal(t, schemas.MCPConnectionStateDisabled, state.State, "a credential update must not resurrect a disabled client")
}

// TestUpdateClientCredentials_PerCallSharedOAuth_PendingVerification_TransitionsToHealthy
// pins the second half of the same bug report: a per-call shared-OAuth
// client's FIRST connection (still in PendingVerification — e.g. a
// config.json-bootstrapped client whose admin just completed the OAuth
// browser flow) must not be treated as "nothing to do" the way an
// already-Healthy per-call client's reauthorize is. Skipping the transition
// left the client permanently stuck showing pending_verification in the UI
// (with no way to retry, since the pending OAuth stash this endpoint
// requires is already cleared by the caller) until a restart re-ran
// AddClient from scratch and performed the transition correctly.
func TestUpdateClientCredentials_PerCallSharedOAuth_PendingVerification_TransitionsToHealthy(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, nil, nil)
	config := &schemas.MCPClientConfig{
		ID:             "client-pending-percall",
		Name:           "pending-percall-client",
		AuthType:       schemas.MCPAuthTypeOauth,
		ConnectionType: schemas.MCPConnectionTypeHTTP,
	}

	m.mu.Lock()
	m.clientMap[config.ID] = &schemas.MCPClientState{
		Name:            config.Name,
		ExecutionConfig: config,
		State:           schemas.MCPConnectionStatePendingVerification,
		ToolMap:         make(map[string]schemas.ChatTool),
		ToolNameMapping: make(map[string]string),
	}
	m.mu.Unlock()

	err := m.UpdateClientCredentials(config.ID, config)
	require.NoError(t, err, "the first connection out of pending_verification must succeed, not report not-applicable")

	m.mu.RLock()
	state := *m.clientMap[config.ID]
	m.mu.RUnlock()
	assert.Equal(t, schemas.MCPConnectionStateHealthy, state.State, "must transition out of pending_verification, mirroring AddClient's own per-call setup")

	// Without a connection checker, nothing would ever discover this
	// client's tools afterward (no persistent Conn, no DiscoveredTools to
	// restore for a client completing its first connection here) — see
	// TestAddClient_PerCallConnection_StartsConnectionChecker for the
	// AddClient-side counterpart of this same fix.
	m.checkerManager.mu.RLock()
	_, hasChecker := m.checkerManager.checkers[config.ID]
	m.checkerManager.mu.RUnlock()
	assert.True(t, hasChecker, "must start a connection checker so tools actually get discovered")

	// A second call, now that the client is already Healthy, is the plain
	// reauthorize case: it must attempt a synchronous tool refresh with the
	// updated credential rather than report not-applicable. No OAuth
	// provider is configured here, so resolving the credential for that
	// discovery fails, and the failure must surface as a real error (not
	// the sentinel).
	err = m.UpdateClientCredentials(config.ID, config)
	require.Error(t, err)
	assert.False(t, errors.Is(err, schemas.ErrMCPReconnectNotApplicable))
	assert.Contains(t, err.Error(), "tool discovery")
}

// TestUpdateClientCredentials_PerCallSharedType_PendingVerification_DiscoversToolsSynchronously
// pins the second half of the same bug report: a connection checker alone
// is not enough, because ClientConnectionChecker.Start uses the SLOW
// healthyInterval (not the fast Unstable one) for a client that already
// starts Healthy — so without a synchronous first discovery pass, a shared
// oauth/headers/none per-call client completing OAuth/verification here
// would sit at Healthy/0-tools for up to healthyInterval before its first
// real check ever ran.
func TestUpdateClientCredentials_PerCallSharedType_PendingVerification_DiscoversToolsSynchronously(t *testing.T) {
	ts, _ := buildAdminDiscoveryHTTPServer(t)

	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, nil, nil)
	config := &schemas.MCPClientConfig{
		ID:               "client-pending-percall-sync",
		Name:             "pending-percall-client-sync",
		AuthType:         schemas.MCPAuthTypeHeaders,
		ConnectionType:   schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar(ts.URL),
		Headers:          map[string]schemas.SecretVar{"Authorization": *schemas.NewSecretVar("Bearer shared-update-token")},
	}

	m.mu.Lock()
	m.clientMap[config.ID] = &schemas.MCPClientState{
		Name:            config.Name,
		ExecutionConfig: config,
		State:           schemas.MCPConnectionStatePendingVerification,
		ToolMap:         make(map[string]schemas.ChatTool),
		ToolNameMapping: make(map[string]string),
	}
	m.mu.Unlock()

	err := m.UpdateClientCredentials(config.ID, config)
	require.NoError(t, err)

	m.mu.RLock()
	state := *m.clientMap[config.ID]
	m.mu.RUnlock()
	assert.Equal(t, schemas.MCPConnectionStateHealthy, state.State)
	assert.Contains(t, state.ToolMap, "pending-percall-client-sync-echo", "tools must be discovered synchronously, not deferred entirely to the periodic checker")
}

// TestReconnectClient_PerCallSharedOAuth_ReturnsReconnectNotApplicable pins
// the same message-wording bug as TestUpdateClientCredentials's sibling:
// ReconnectClient's per-call guard used to unconditionally say "per-user
// auth clients", even though RequiresPerCallConnection is also true for a
// genuinely shared oauth/headers client running per-call via
// needs_session_stickiness nil/false. The sentinel was always correct; only
// the message text was misleading for this case.
func TestReconnectClient_PerCallSharedOAuth_ReturnsReconnectNotApplicable(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, nil, nil)
	config := &schemas.MCPClientConfig{
		ID:             "client-shared-percall-reconnect",
		Name:           "shared-percall-client-reconnect",
		AuthType:       schemas.MCPAuthTypeOauth,
		ConnectionType: schemas.MCPConnectionTypeHTTP,
		// NeedsSessionStickiness left nil on purpose: the default value.
	}

	m.mu.Lock()
	m.clientMap[config.ID] = &schemas.MCPClientState{
		Name:            config.Name,
		ExecutionConfig: config,
		State:           schemas.MCPConnectionStateHealthy,
	}
	m.mu.Unlock()

	err := m.ReconnectClient(config.ID)
	require.Error(t, err)
	assert.True(t, errors.Is(err, schemas.ErrMCPReconnectNotApplicable))
	assert.NotContains(t, err.Error(), "per-user", "a genuinely shared client must not be told it's a per-user auth client")
}

// TestCloseAndMarkNeedsReauth_PerCallSharedOAuth_ReturnsReconnectNotApplicable
// is the CloseAndMarkNeedsReauth counterpart of the same message-wording fix.
func TestCloseAndMarkNeedsReauth_PerCallSharedOAuth_ReturnsReconnectNotApplicable(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, nil, nil)
	config := &schemas.MCPClientConfig{
		ID:             "client-shared-percall-close",
		Name:           "shared-percall-client-close",
		AuthType:       schemas.MCPAuthTypeOauth,
		ConnectionType: schemas.MCPConnectionTypeHTTP,
	}

	m.mu.Lock()
	m.clientMap[config.ID] = &schemas.MCPClientState{
		Name:            config.Name,
		ExecutionConfig: config,
		State:           schemas.MCPConnectionStateHealthy,
	}
	m.mu.Unlock()

	err := m.CloseAndMarkNeedsReauth(config.ID)
	require.Error(t, err)
	assert.True(t, errors.Is(err, schemas.ErrMCPReconnectNotApplicable))
	assert.NotContains(t, err.Error(), "per-user", "a genuinely shared client must not be told it's a per-user auth client")
}

// TestBuildTLSHTTPClientNeverFallsBackToUnguardedDefault proves buildTLSHTTPClient
// always returns a non-nil client (with or without TLS configured) so callers
// never fall back to the mcp-go library's own default HTTP client, which
// carries no dial guard at all.
func TestBuildTLSHTTPClientNeverFallsBackToUnguardedDefault(t *testing.T) {
	for _, tlsCfg := range []*schemas.MCPTLSConfig{nil, {InsecureSkipVerify: true}} {
		httpClient, err := (&MCPManager{logger: defaultLogger}).buildTLSHTTPClient(tlsCfg)
		require.NoError(t, err)
		require.NotNil(t, httpClient)

		transport, ok := httpClient.Transport.(*http.Transport)
		require.True(t, ok)
		require.NotNil(t, transport.DialContext)
	}
}

// TestBuildTLSHTTPClientBlocksLinkLocal proves the dial-time guard refuses
// link-local destinations (including the 169.254.169.254 cloud metadata
// endpoint) - the one class of target with no legitimate MCP use case under
// any deployment topology, authenticated or not.
func TestBuildTLSHTTPClientBlocksLinkLocal(t *testing.T) {
	httpClient, err := (&MCPManager{logger: defaultLogger}).buildTLSHTTPClient(nil)
	require.NoError(t, err)
	transport := httpClient.Transport.(*http.Transport)

	_, err = transport.DialContext(context.Background(), "tcp", "169.254.169.254:80")
	require.Error(t, err)
	require.Contains(t, err.Error(), "blocked connection to link-local address")
}

// TestBuildTLSHTTPClientAllowsLoopback proves loopback MCP servers keep
// working: connecting to a local MCP tool server on the same host is the
// documented primary HTTP-client use case
// (docs/mcp/connecting-to-servers.mdx), so the dial-time guard here must not
// reject it. The unauthenticated-caller case is refused earlier, at the HTTP
// handler layer (rejectPrivateMCPTargetIfAuthBypassed), not here.
func TestBuildTLSHTTPClientAllowsLoopback(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := ln.Close(); err != nil {
			t.Errorf("failed to close test listener: %v", err)
		}
	})
	// The accepted server side is handed back to the test body so both ends are closed, and
	// their Close results checked, on the test goroutine rather than after it may have ended.
	accepted := make(chan net.Conn, 1)
	go func() {
		if conn, err := ln.Accept(); err == nil {
			accepted <- conn
		}
	}()

	httpClient, err := (&MCPManager{logger: defaultLogger}).buildTLSHTTPClient(nil)
	require.NoError(t, err)
	transport := httpClient.Transport.(*http.Transport)

	conn, err := transport.DialContext(context.Background(), "tcp", ln.Addr().String())
	require.NoError(t, err)
	require.NoError(t, conn.Close())
	select {
	case server := <-accepted:
		require.NoError(t, server.Close())
	case <-time.After(5 * time.Second):
		t.Fatal("listener never accepted the dialed connection")
	}
}

// TestBuildTLSHTTPClientRoutesThroughConfiguredProxy proves the client honors
// the transport's proxy selector for a permitted destination, which is what a
// deployment with no direct egress depends on: the request must reach the
// proxy, and the destination must never be dialed directly. This is the
// behavior core v1.8.5 through v1.9.1 broke by nulling the selector.
//
// The proxy is injected by swapping DefaultTransport.Proxy rather than via
// HTTP_PROXY, because ProxyFromEnvironment reads the environment once per
// process and would not see a t.Setenv made after any earlier test used it.
// Every test here that touches DefaultTransport.Proxy therefore stays
// sequential (no t.Parallel) and restores the original selector on cleanup.
func TestBuildTLSHTTPClientRoutesThroughConfiguredProxy(t *testing.T) {
	// The destination must never see a connection: with a proxy selected,
	// only the proxy may be dialed.
	targetLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := targetLn.Close(); err != nil {
			t.Errorf("failed to close destination listener: %v", err)
		}
	})
	var targetHits atomic.Int32
	go func() {
		for {
			conn, err := targetLn.Accept()
			if err != nil {
				return
			}
			targetHits.Add(1)
			_ = conn.Close()
		}
	}()

	// A minimal proxy: records what it was asked for, answers a plain-HTTP
	// absolute-URL request itself, and refuses a CONNECT, since tunnelling
	// TLS is not what this test is about; seeing the CONNECT is enough.
	var (
		proxyHits atomic.Int32
		mu        sync.Mutex
		lastReq   string
	)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		mu.Lock()
		lastReq = r.Method + " " + r.Host
		mu.Unlock()
		if r.Method == http.MethodConnect {
			http.Error(w, "no tunnel in this test", http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("via-proxy"))
	}))
	t.Cleanup(proxy.Close)
	proxyURL, err := url.Parse(proxy.URL)
	require.NoError(t, err)
	seen := func() string {
		mu.Lock()
		defer mu.Unlock()
		return lastReq
	}

	base, ok := http.DefaultTransport.(*http.Transport)
	require.True(t, ok)
	origProxy := base.Proxy
	base.Proxy = http.ProxyURL(proxyURL)
	t.Cleanup(func() { base.Proxy = origProxy })

	httpClient, err := (&MCPManager{logger: defaultLogger}).buildTLSHTTPClient(nil)
	require.NoError(t, err)
	httpClient.Timeout = 5 * time.Second
	target := targetLn.Addr().String()

	t.Run("http", func(t *testing.T) {
		resp, err := httpClient.Get("http://" + target + "/mcp")
		require.NoError(t, err)
		body, readErr := io.ReadAll(resp.Body)
		require.NoError(t, resp.Body.Close())
		require.NoError(t, readErr)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Equal(t, "via-proxy", string(body), "the response must come from the proxy")
		require.Equal(t, "GET "+target, seen(), "the proxy must be asked for the MCP destination")
	})
	t.Run("https", func(t *testing.T) {
		resp, err := httpClient.Get("https://" + target + "/mcp")
		if resp != nil {
			require.NoError(t, resp.Body.Close())
		}
		require.Error(t, err, "the test proxy refuses tunnels, so the request must fail at the proxy")
		require.Equal(t, "CONNECT "+target, seen(), "an https destination must be tunnelled through the proxy")
	})
	require.Equal(t, int32(2), proxyHits.Load(), "one proxy request per scheme")
	require.Zero(t, targetHits.Load(), "with a proxy selected the destination must never be dialed directly")
}

// TestBuildTLSHTTPClientRefusesProxyingBlockedLiteral proves the dial-time
// guard cannot be sidestepped by a proxy. When a proxy is selected,
// http.Transport hands DialContext the proxy's address rather than the MCP
// destination, so PrivateNetworkDialContext alone would validate the proxy
// and let the proxy forward to the blocked target. The selector wrapper must
// refuse a blocked IP-literal destination (link-local, and the cloud metadata
// endpoints that live outside link-local) before the proxy is ever contacted.
func TestBuildTLSHTTPClientRefusesProxyingBlockedLiteral(t *testing.T) {
	proxyLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := proxyLn.Close(); err != nil {
			t.Errorf("failed to close proxy listener: %v", err)
		}
	})
	// Any accepted connection is itself the failure; its Close result is passed back to the
	// test body rather than reported from the goroutine, which may outlive the subtest.
	var proxyHits atomic.Int32
	proxyCloseErrs := make(chan error, 8)
	go func() {
		for {
			conn, err := proxyLn.Accept()
			if err != nil {
				return
			}
			proxyHits.Add(1)
			if err := conn.Close(); err != nil {
				select {
				case proxyCloseErrs <- err:
				default:
				}
			}
		}
	}()
	proxyURL, err := url.Parse("http://" + proxyLn.Addr().String())
	require.NoError(t, err)

	base, ok := http.DefaultTransport.(*http.Transport)
	require.True(t, ok)
	origProxy := base.Proxy
	base.Proxy = http.ProxyURL(proxyURL)
	t.Cleanup(func() { base.Proxy = origProxy })

	targets := []struct {
		host string
		want string
	}{
		{"169.254.169.254", "blocked connection to link-local address"},
		{"100.100.100.200", "blocked connection to cloud metadata endpoint"},
		{"[fd00:ec2::254]", "blocked connection to cloud metadata endpoint"},
	}
	for _, scheme := range []string{"http", "https"} {
		for _, tc := range targets {
			t.Run(scheme+"/"+tc.host, func(t *testing.T) {
				httpClient, err := (&MCPManager{logger: defaultLogger}).buildTLSHTTPClient(nil)
				require.NoError(t, err)
				httpClient.Timeout = 5 * time.Second

				resp, err := httpClient.Get(scheme + "://" + tc.host + "/latest/meta-data/")
				if resp != nil {
					require.NoError(t, resp.Body.Close())
				}
				require.Error(t, err, "a blocked MCP target must be refused even when a proxy is configured")
				require.Contains(t, err.Error(), tc.want,
					"the refusal must come from the destination policy, not from the proxy")
				require.Zero(t, proxyHits.Load(), "the configured proxy must never be contacted for a guarded MCP request")
				select {
				case closeErr := <-proxyCloseErrs:
					t.Errorf("failed to close a proxy-side connection: %v", closeErr)
				default:
				}
			})
		}
	}
}

// TestBuildTLSHTTPClientProxiedHostnameNeedsNoLocalDNS proves a proxied
// request is handed to the proxy with its hostname unresolved. A proxy-only
// deployment resolves public names at the proxy, not on the Bifrost host, so
// the selector must not resolve locally: the target here is under .invalid
// (RFC 6761, guaranteed not to resolve) and the request must still reach the
// proxy and succeed.
func TestBuildTLSHTTPClientProxiedHostnameNeedsNoLocalDNS(t *testing.T) {
	var (
		mu      sync.Mutex
		lastReq string
	)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		lastReq = r.Method + " " + r.Host
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("via-proxy"))
	}))
	t.Cleanup(proxy.Close)
	proxyURL, err := url.Parse(proxy.URL)
	require.NoError(t, err)

	base, ok := http.DefaultTransport.(*http.Transport)
	require.True(t, ok)
	origProxy := base.Proxy
	base.Proxy = http.ProxyURL(proxyURL)
	t.Cleanup(func() { base.Proxy = origProxy })

	httpClient, err := (&MCPManager{logger: defaultLogger}).buildTLSHTTPClient(nil)
	require.NoError(t, err)
	httpClient.Timeout = 5 * time.Second

	resp, err := httpClient.Get("http://mcp.invalid/mcp")
	require.NoError(t, err, "a hostname the host cannot resolve must still be sent to the proxy")
	body, readErr := io.ReadAll(resp.Body)
	require.NoError(t, resp.Body.Close())
	require.NoError(t, readErr)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "via-proxy", string(body))
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, "GET mcp.invalid", lastReq, "the proxy must receive the original hostname")
}

// TestBuildTLSHTTPClientBypassedProxyDialsDirect covers the NO_PROXY shape: the
// selector is present but returns no proxy for the request, so the transport
// dials the destination itself and the dial-time guard is what applies. A
// loopback destination is reached directly and a link-local one is still
// blocked, exactly as when no proxy is configured at all.
func TestBuildTLSHTTPClientBypassedProxyDialsDirect(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("direct"))
	}))
	t.Cleanup(target.Close)

	base, ok := http.DefaultTransport.(*http.Transport)
	require.True(t, ok)
	origProxy := base.Proxy
	base.Proxy = func(*http.Request) (*url.URL, error) { return nil, nil }
	t.Cleanup(func() { base.Proxy = origProxy })

	httpClient, err := (&MCPManager{logger: defaultLogger}).buildTLSHTTPClient(nil)
	require.NoError(t, err)
	httpClient.Timeout = 5 * time.Second

	resp, err := httpClient.Get(target.URL + "/mcp")
	require.NoError(t, err)
	body, readErr := io.ReadAll(resp.Body)
	require.NoError(t, resp.Body.Close())
	require.NoError(t, readErr)
	require.Equal(t, "direct", string(body))
	require.Equal(t, int32(1), targetHits.Load(), "a bypassed proxy means the destination is dialed directly")

	resp, err = httpClient.Get("http://169.254.169.254/latest/meta-data/")
	if resp != nil {
		require.NoError(t, resp.Body.Close())
	}
	require.Error(t, err)
	require.Contains(t, err.Error(), "blocked connection to link-local address",
		"on the direct path the dial-time guard still refuses link-local")
}

// TestMCPProxySelectorPassthrough pins the wrapper's two pass-through
// behaviors: a nil selector stays nil so a transport without one is unchanged,
// and an error from the wrapped selector is returned as-is.
func TestMCPProxySelectorPassthrough(t *testing.T) {
	require.Nil(t, mcpProxySelector(nil))

	wantErr := errors.New("selector failed")
	sel := mcpProxySelector(func(*http.Request) (*url.URL, error) { return nil, wantErr })
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1/mcp", nil)
	require.NoError(t, err)
	_, err = sel(req)
	require.ErrorIs(t, err, wantErr)
}
