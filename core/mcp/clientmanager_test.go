package mcp

import (
	"context"
	"errors"
	"testing"

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

	m.failConnectAttempt(entry, config, nil, nil, nil, nil, true)

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

	m.failConnectAttempt(entry, config, nil, nil, nil, nil, false)

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

// TestUpdateClientCredentials_PerCallSharedOAuth_ReturnsReconnectNotApplicable
// pins the bug reported against a shared-OAuth client (auth_type='oauth')
// running in per-call mode: needs_session_stickiness nil/false — the default
// for a newly created or config.json-bootstrapped client that predates this
// field — routes RequiresPerCallConnection through the same per-call branch
// a genuine per-user client uses. UpdateClientCredentials's guard used to
// return a bare "per-user auth clients" error for this case even though the
// client is genuinely shared, just with no persistent connection to
// reconnect (the fresh OAuth token is already live for the next per-call
// dial). Callers must be able to tell this "nothing to do" case apart from a
// real failure via the same ErrMCPReconnectNotApplicable sentinel
// ReconnectClient/CloseAndMarkNeedsReauth already use for the analogous case.
func TestUpdateClientCredentials_PerCallSharedOAuth_ReturnsReconnectNotApplicable(t *testing.T) {
	// nil credStore: NewMCPManager defaults to a real credstore.CredStore,
	// whose RequiresPerCallConnection actually ANDs auth type with
	// NeedsSessionStickiness — unlike expiredOAuthCredStore (used elsewhere
	// in this file), which hardcodes false regardless of either.
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
	assert.True(t, errors.Is(err, schemas.ErrMCPReconnectNotApplicable), "a per-call shared-oauth client must report the same not-applicable sentinel as a genuine per-user client, not an opaque failure")
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
	// reauthorize case: genuinely nothing left to do.
	err = m.UpdateClientCredentials(config.ID, config)
	require.Error(t, err)
	assert.True(t, errors.Is(err, schemas.ErrMCPReconnectNotApplicable))
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
