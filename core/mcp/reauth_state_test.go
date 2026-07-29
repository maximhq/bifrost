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

// TestIsOAuth2TokenExpiredErrorText covers the substring-matching fallback
// connectToMCPClient relies on to recognize schemas.ErrOAuth2TokenExpired at
// the failure site: runConnectWithPluginPipeline flattens the underlying Go
// error to a string (see the doc comment on isOAuth2TokenExpiredErrorText),
// so errors.Is/As isn't usable there, and text matching is the only option
// left, mirroring isTransientError.
func TestIsOAuth2TokenExpiredErrorText(t *testing.T) {
	wrappedOnce := fmt.Errorf("refresh token rejected by upstream OAuth server, re-authentication required: %w", schemas.ErrOAuth2TokenExpired)
	wrappedTwice := fmt.Errorf("token expired and refresh failed: %w", wrappedOnce)

	tests := []struct {
		name   string
		errStr string
		want   bool
	}{
		{"exact sentinel text", schemas.ErrOAuth2TokenExpired.Error(), true},
		{"single %w wrap (RefreshAccessToken permanent-rejection path)", wrappedOnce.Error(), true},
		{"double %w wrap (GetAccessToken's refresh-failed wrapper)", wrappedTwice.Error(), true},
		{"token-not-active wrap (GetAccessToken's inactive-status path)", fmt.Errorf("oauth token is not active, status: orphaned: %w", schemas.ErrOAuth2TokenExpired).Error(), true},
		{"unrelated connectivity error", "connection refused", false},
		{"unrelated config error", "oauth2 config not found", false},
		{"empty string", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isOAuth2TokenExpiredErrorText(tc.errStr))
		})
	}
}

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
	// the first place — AddClient/EnableClient/UpdateClientConnection all
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

func TestConnectToMCPClient_GenericFailure_StaysDisconnected(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, genericFailureCredStore{}, nil, nil)
	config := newSharedOAuthClientConfig("client-generic-failure")

	err := m.connectToMCPClient(context.Background(), config)
	require.Error(t, err)

	m.mu.RLock()
	state, exists := m.clientMap[config.ID]
	m.mu.RUnlock()
	require.True(t, exists)
	assert.Equal(t, schemas.MCPConnectionStateDisconnected, state.State)
}

// TestUpdateClientState_PreservesNeedsReauth covers healthmonitor.go's
// updateClientState guard: a routine health-check ping success/failure must
// not silently flip a NeedsReauth client back to Connected/Disconnected —
// only a human reauthorizing the client (surfaced elsewhere as a fresh
// connectToMCPClient success, which sets Connected unconditionally) should
// move it out of this state.
func TestUpdateClientState_PreservesNeedsReauth(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, expiredOAuthCredStore{}, nil, nil)
	config := newSharedOAuthClientConfig("client-health-cycle")

	m.mu.Lock()
	m.clientMap[config.ID] = &schemas.MCPClientState{
		Name:            config.Name,
		ExecutionConfig: config,
		State:           schemas.MCPConnectionStateNeedsReauth,
	}
	m.mu.Unlock()

	chm := NewClientHealthMonitor(m, config.ID, DefaultHealthCheckInterval, true, nil)

	// A failed health-check tick (performHealthCheck's failure branch) tries
	// to write Disconnected first.
	chm.updateClientState(schemas.MCPConnectionStateDisconnected)
	m.mu.RLock()
	stateAfterFailureTick := m.clientMap[config.ID].State
	m.mu.RUnlock()
	assert.Equal(t, schemas.MCPConnectionStateNeedsReauth, stateAfterFailureTick, "a failed health-check tick must not clobber NeedsReauth back to Disconnected")

	// A successful ping (performHealthCheck's success branch) tries to write
	// Connected — this must also be rejected: the ping succeeding against a
	// stale/absent transport does not mean the credential is fixed.
	chm.updateClientState(schemas.MCPConnectionStateConnected)
	m.mu.RLock()
	stateAfterSuccessTick := m.clientMap[config.ID].State
	m.mu.RUnlock()
	assert.Equal(t, schemas.MCPConnectionStateNeedsReauth, stateAfterSuccessTick, "a successful health-check tick must not clobber NeedsReauth back to Connected")
}

// TestUpdateClientState_StillPreservesDisabled is a regression guard for the
// pre-existing Disabled-preservation behavior updateClientState had before
// this change — the new NeedsReauth branch must be additive, not a
// replacement.
func TestUpdateClientState_StillPreservesDisabled(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, expiredOAuthCredStore{}, nil, nil)
	config := newSharedOAuthClientConfig("client-disabled")

	m.mu.Lock()
	m.clientMap[config.ID] = &schemas.MCPClientState{
		Name:            config.Name,
		ExecutionConfig: config,
		State:           schemas.MCPConnectionStateDisabled,
	}
	m.mu.Unlock()

	chm := NewClientHealthMonitor(m, config.ID, DefaultHealthCheckInterval, true, nil)
	chm.updateClientState(schemas.MCPConnectionStateConnected)

	m.mu.RLock()
	state := m.clientMap[config.ID].State
	m.mu.RUnlock()
	assert.Equal(t, schemas.MCPConnectionStateDisabled, state)
}
