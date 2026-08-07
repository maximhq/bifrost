package mcp

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// expiredOAuthCredStore simulates a shared OAuth2 credential store whose
// refresh has permanently failed: every ConnectionHeaders call returns the
// same error shape framework/oauth2's RefreshAccessToken/GetAccessToken
// produce for a dead refresh token, wrapping schemas.ErrOAuth2TokenExpired.
type expiredOAuthCredStore struct{}

func (expiredOAuthCredStore) ConnectionHeaders(_ *schemas.BifrostContext, _ *schemas.MCPClientConfig) (http.Header, error) {
	return nil, fmt.Errorf("refresh token rejected by upstream OAuth server, re-authentication required: %w", schemas.ErrOAuth2TokenExpired)
}

func (expiredOAuthCredStore) RequestHeaders(_ *schemas.BifrostContext, _ *schemas.MCPClientConfig) (http.Header, error) {
	return http.Header{}, nil
}

func (expiredOAuthCredStore) RequiresPerCallConnection(_ *schemas.MCPClientConfig) bool {
	return false
}

func (expiredOAuthCredStore) ForceRefresh(_ *schemas.BifrostContext, _ *schemas.MCPClientConfig) error {
	return fmt.Errorf("refresh token rejected by upstream OAuth server, re-authentication required: %w", schemas.ErrOAuth2TokenExpired)
}

func (expiredOAuthCredStore) AdminConnectionHeaders(_ context.Context, _ *schemas.MCPClientConfig) (http.Header, error) {
	return nil, fmt.Errorf("not implemented")
}

// newSharedOAuthClientConfig builds a minimal shared-OAuth (auth_type=oauth,
// persistent-connection) MCP client config for the connectToMCPClient tests
// below. ConnectionType is HTTP so the failure happens at the
// credStore.ConnectionHeaders call inside connectToMCPClient's op closure,
// before any real network dial is attempted.
func newSharedOAuthClientConfig(id string) *schemas.MCPClientConfig {
	oauthConfigID := "oauth-config-1"
	return &schemas.MCPClientConfig{
		ID:               id,
		Name:             "reauth-test-client-" + id,
		ConnectionType:   schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar("https://example.invalid/mcp"),
		AuthType:         schemas.MCPAuthTypeOauth,
		OauthConfigID:    &oauthConfigID,
	}
}

// TestConnectToMCPClient_OAuth2TokenExpired_SetsNeedsReauth verifies the
// core wiring point of this change: a connect failure whose underlying cause
// is schemas.ErrOAuth2TokenExpired (a dead shared-OAuth credential) lands the
// client in MCPConnectionStateNeedsReauth, not the generic Disconnected the
// entry was initialized to at the top of connectToMCPClient.
func TestConnectToMCPClient_OAuth2TokenExpired_SetsNeedsReauth(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, expiredOAuthCredStore{}, nil, nil)
	config := newSharedOAuthClientConfig("client-needs-reauth")

	// Confirmed precondition: only shared-connection auth types (
	// RequiresPerCallConnection()==false) ever reach connectToMCPClient in
	// the first place — AddClient/EnableClient/UpdateClientCredentials all
	// special-case per-call-connection (per-user) auth types before calling
	// it, and ReconnectClient refuses outright for them. expiredOAuthCredStore
	// mirrors that: RequiresPerCallConnection returns false.
	require.False(t, m.credStore.RequiresPerCallConnection(config))

	err := m.connectToMCPClient(context.Background(), config)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to connect MCP client")

	m.mu.RLock()
	state, exists := m.clientMap[config.ID]
	m.mu.RUnlock()
	require.True(t, exists)
	assert.Equal(t, schemas.MCPConnectionStateNeedsReauth, state.State)
}

// TestConnectToMCPClient_GenericFailure_StaysDisconnected is the control
// case: a connect failure that is NOT an ErrOAuth2TokenExpired-wrapped error
// (e.g. a plain connectivity error) must leave the client in the existing
// generic Disconnected state, not NeedsReauth.
type genericFailureCredStore struct{}

func (genericFailureCredStore) ConnectionHeaders(_ *schemas.BifrostContext, _ *schemas.MCPClientConfig) (http.Header, error) {
	return nil, fmt.Errorf("connection refused")
}

func (genericFailureCredStore) RequestHeaders(_ *schemas.BifrostContext, _ *schemas.MCPClientConfig) (http.Header, error) {
	return http.Header{}, nil
}

func (genericFailureCredStore) RequiresPerCallConnection(_ *schemas.MCPClientConfig) bool {
	return false
}

func (genericFailureCredStore) ForceRefresh(_ *schemas.BifrostContext, _ *schemas.MCPClientConfig) error {
	return nil
}

func (genericFailureCredStore) AdminConnectionHeaders(_ context.Context, _ *schemas.MCPClientConfig) (http.Header, error) {
	return nil, fmt.Errorf("not implemented")
}

func TestConnectToMCPClient_GenericFailure_StaysDisconnected(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, genericFailureCredStore{}, nil, nil)
	config := newSharedOAuthClientConfig("client-generic-failure")

	err := m.connectToMCPClient(context.Background(), config)
	require.Error(t, err)

	m.mu.RLock()
	state, exists := m.clientMap[config.ID]
	m.mu.RUnlock()
	require.True(t, exists)
	assert.Equal(t, schemas.MCPConnectionStateUnstable, state.State)
}

// TestUpdateClientState_PreservesNeedsReauth covers healthmonitor.go's
// updateClientState guard: a routine health-check ping success/failure must
// not silently flip a NeedsReauth client back to Connected/Disconnected —
// only a human reauthorizing the client (surfaced elsewhere as a fresh
// connectToMCPClient success, which sets Connected unconditionally) should
// move it out of this state.
func TestSetState_PreservesNeedsReauth(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, expiredOAuthCredStore{}, nil, nil)
	config := newSharedOAuthClientConfig("client-health-cycle")

	m.mu.Lock()
	m.clientMap[config.ID] = &schemas.MCPClientState{
		Name:            config.Name,
		ExecutionConfig: config,
		State:           schemas.MCPConnectionStateNeedsReauth,
	}
	m.mu.Unlock()

	checker := NewClientConnectionChecker(m, config.ID, DefaultConnectionCheckInterval, true, nil)

	// A failed check tick (recordFailure's path) tries to write Unstable first.
	checker.setState(schemas.MCPConnectionStateUnstable, 0)
	m.mu.RLock()
	stateAfterFailureTick := m.clientMap[config.ID].State
	m.mu.RUnlock()
	assert.Equal(t, schemas.MCPConnectionStateNeedsReauth, stateAfterFailureTick, "a failed check tick must not clobber NeedsReauth back to Unstable")

	// A successful check (recordSuccess's path) tries to write Healthy — this
	// must also be rejected: the check succeeding against a stale/absent
	// transport does not mean the credential is fixed.
	checker.setState(schemas.MCPConnectionStateHealthy, 0)
	m.mu.RLock()
	stateAfterSuccessTick := m.clientMap[config.ID].State
	m.mu.RUnlock()
	assert.Equal(t, schemas.MCPConnectionStateNeedsReauth, stateAfterSuccessTick, "a successful check tick must not clobber NeedsReauth back to Healthy")
}

// TestPerformCheck_SkipsNeedsReauthClients covers the connection checker's
// entry-point guard, one level up from TestSetState_PreservesNeedsReauth: a
// NeedsReauth client (e.g. CloseAndMarkNeedsReauth after OAuth credential
// rotation) has Conn == nil by design. Without a skip, performCheck would
// treat that nil Conn as a fresh failure and spawn a reconnect attempt —
// which dials with the already-known-dead credential for nothing. The tick
// must be a complete no-op: state untouched.
func TestPerformCheck_SkipsNeedsReauthClients(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, expiredOAuthCredStore{}, nil, nil)
	config := newSharedOAuthClientConfig("client-needs-reauth-healthcheck")

	m.mu.Lock()
	m.clientMap[config.ID] = &schemas.MCPClientState{
		Name:            config.Name,
		ExecutionConfig: config,
		State:           schemas.MCPConnectionStateNeedsReauth,
		Conn:            nil,
	}
	m.mu.Unlock()

	checker := NewClientConnectionChecker(m, config.ID, DefaultConnectionCheckInterval, true, nil)
	checker.performCheck()

	m.mu.RLock()
	state := m.clientMap[config.ID].State
	m.mu.RUnlock()
	assert.Equal(t, schemas.MCPConnectionStateNeedsReauth, state)
}

// TestSetState_StillPreservesDisabled is a regression guard for the
// pre-existing Disabled-preservation behavior setState had before this
// change — the NeedsReauth branch must be additive, not a replacement.
func TestSetState_StillPreservesDisabled(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, expiredOAuthCredStore{}, nil, nil)
	config := newSharedOAuthClientConfig("client-disabled")

	m.mu.Lock()
	m.clientMap[config.ID] = &schemas.MCPClientState{
		Name:            config.Name,
		ExecutionConfig: config,
		State:           schemas.MCPConnectionStateDisabled,
	}
	m.mu.Unlock()

	checker := NewClientConnectionChecker(m, config.ID, DefaultConnectionCheckInterval, true, nil)
	checker.setState(schemas.MCPConnectionStateHealthy, 0)

	m.mu.RLock()
	state := m.clientMap[config.ID].State
	m.mu.RUnlock()
	assert.Equal(t, schemas.MCPConnectionStateDisabled, state)
}
