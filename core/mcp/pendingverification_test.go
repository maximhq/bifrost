package mcp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// pending_verification is authoritative: it means a human still has to
// complete a one-time setup step, and nothing automatic may overwrite it with
// a state that hides the CTA the UI shows for it.
// =============================================================================

// newPendingClientConfig builds a client that AddClient would have parked in
// pending_verification: an OAuth-based client carrying the inline oauth_config
// block from config.json that no admin has authorized yet.
func newPendingClientConfig(id string, authType schemas.MCPAuthType) *schemas.MCPClientConfig {
	return &schemas.MCPClientConfig{
		ID:                 id,
		Name:               "pending-" + id,
		AuthType:           authType,
		ConnectionType:     schemas.MCPConnectionTypeHTTP,
		ConnectionString:   schemas.NewSecretVar("http://127.0.0.1:0/mcp"),
		PendingOAuthConfig: &schemas.OAuth2Config{},
	}
}

// TestEnableClient_StillPendingVerification_KeepsThatState covers the path that
// loses the signal. AddClient's disabled branch runs before its
// pending_verification branches, so a client declared both disabled and
// unauthorized is registered as Disabled with its PendingOAuthConfig intact.
// Enabling it then took the ordinary route: the per-call branch marked it
// Healthy outright, and the sticky branch dialled with a credential that does
// not exist yet. Either way the "an admin must authorize this" state was gone,
// and with it the Verify CTA that is the only way to resolve it.
func TestEnableClient_StillPendingVerification_KeepsThatState(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(*schemas.MCPClientConfig)
		authType schemas.MCPAuthType
	}{
		{name: "per_user_oauth", authType: schemas.MCPAuthTypePerUserOauth},
		{name: "oauth", authType: schemas.MCPAuthTypeOauth},
		{
			// per_user_headers is pending only while DiscoveredTools is nil:
			// AddClient nil-checks it on purpose so a verified server that
			// legitimately exposes zero tools is not re-parked on every reload.
			name:     "per_user_headers with no discovery yet",
			authType: schemas.MCPAuthTypePerUserHeaders,
			mutate: func(c *schemas.MCPClientConfig) {
				c.PendingOAuthConfig = nil
				c.DiscoveredTools = nil
			},
		},
		{
			// token_exchange follows the same nil rule. An initialized empty map
			// would mean "verified, and the server has no tools", which is not
			// pending: see TestAwaitsAdminVerification_NilIsTheDiscriminator.
			name:     "token_exchange with no discovery yet",
			authType: schemas.MCPAuthTypeTokenExchange,
			mutate: func(c *schemas.MCPClientConfig) {
				c.PendingOAuthConfig = nil
				c.DiscoveredTools = nil
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
			defer m.checkerManager.StopAll()

			config := newPendingClientConfig("client-enable-pending", tc.authType)
			if tc.mutate != nil {
				tc.mutate(config)
			}
			config.Disabled = true
			m.mu.Lock()
			m.clientMap[config.ID] = &schemas.MCPClientState{
				Name:            config.Name,
				ExecutionConfig: config,
				State:           schemas.MCPConnectionStateDisabled,
				ToolMap:         map[string]schemas.ChatTool{},
				ToolNameMapping: map[string]string{},
				ConnectionInfo:  &schemas.MCPClientConnectionInfo{Type: config.ConnectionType},
			}
			m.mu.Unlock()

			require.NoError(t, m.EnableClient(config.ID), "enabling must succeed: the client is legitimately enabled, just not authorized yet")

			m.mu.RLock()
			state := m.clientMap[config.ID].State
			disabled := m.clientMap[config.ID].ExecutionConfig.Disabled
			m.mu.RUnlock()

			assert.Equal(t, schemas.MCPConnectionStatePendingVerification, state,
				"an enabled but never-authorized client is awaiting an admin, not healthy and not broken")
			assert.False(t, disabled, "the enable itself still stands")
		})
	}
}

// TestPerformCheck_PendingVerification_StaysQuiet pins the checker half. A
// client awaiting its one-time admin flow has no credential to check with, so
// a check can only rediscover what is already known and would overwrite the
// state with Unstable, replacing an actionable "authorize this" with a generic
// "something is wrong". Same treatment NeedsReauth already gets.
func TestPerformCheck_PendingVerification_StaysQuiet(t *testing.T) {
	cred := &fakeAdminCredStore{err: errors.New("admin credential unavailable")}
	m := &MCPManager{credStore: cred, logger: &MockLogger{}, clientMap: map[string]*schemas.MCPClientState{}}

	config := newPendingClientConfig("client-check-pending", schemas.MCPAuthTypePerUserOauth)
	m.clientMap[config.ID] = &schemas.MCPClientState{
		Name:            config.Name,
		ExecutionConfig: config,
		State:           schemas.MCPConnectionStatePendingVerification,
	}

	checker := NewClientConnectionChecker(m, config.ID, time.Minute, false, &MockLogger{})
	next, onSteady := checker.performCheck()

	assert.Equal(t, 0, cred.callCount(), "no discovery may be attempted without the credential the admin has yet to supply")
	assert.Equal(t, schemas.MCPConnectionStatePendingVerification, m.clientMap[config.ID].State)
	assert.True(t, onSteady, "and the timer stays on the relaxed cadence, like NeedsReauth")
	assert.Equal(t, time.Minute, next)
}

// TestSetState_PendingVerification_NotOverwritten guards the same invariant at
// the single writer, so a check already in flight when a client is parked in
// pending_verification cannot land on it afterwards.
func TestSetState_PendingVerification_NotOverwritten(t *testing.T) {
	m := &MCPManager{logger: &MockLogger{}, clientMap: map[string]*schemas.MCPClientState{}}
	config := newPendingClientConfig("client-setstate-pending", schemas.MCPAuthTypeOauth)
	m.clientMap[config.ID] = &schemas.MCPClientState{
		Name:            config.Name,
		ExecutionConfig: config,
		State:           schemas.MCPConnectionStatePendingVerification,
	}

	checker := NewClientConnectionChecker(m, config.ID, time.Minute, false, &MockLogger{})

	checker.setState(schemas.MCPConnectionStateUnstable, 0, schemas.MCPConnectionFailureStageListTools, errors.New("late failure"))
	assert.Equal(t, schemas.MCPConnectionStatePendingVerification, m.clientMap[config.ID].State)
	assert.Nil(t, m.clientMap[config.ID].LastFailure, "a dropped write must not leave a failure record behind either")

	checker.setState(schemas.MCPConnectionStateHealthy, 0, "", nil)
	assert.Equal(t, schemas.MCPConnectionStatePendingVerification, m.clientMap[config.ID].State,
		"a synthetic success must not silently promote an unauthorized client either")
}

// TestAddClient_TokenExchange_VerifiedWithZeroTools_IsNotReParked pins the
// discriminator between "never verified" and "verified, and the server exposes
// no tools". It is nil-ness, not length, and the store is what makes it so:
// BeforeSave marshals DiscoveredTools only when it is non-nil, so a verified
// zero-tool server persists "{}", and AfterFind unmarshals that back into a
// non-nil empty map. A client that never completed verification round-trips as
// nil instead.
//
// Reading an empty map as "not verified" therefore re-parks a perfectly
// verified client in pending_verification on every single reload, which is the
// re-park the per-user-headers branch nil-checks specifically to avoid. Token
// exchange was len-checking and had that bug.
func TestAddClient_TokenExchange_VerifiedWithZeroTools_IsNotReParked(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	defer m.checkerManager.StopAll()

	config := newPendingClientConfig("client-tokenexchange-zero-tools", schemas.MCPAuthTypeTokenExchange)
	// AddClient validates the name; the sibling tests here bypass it by writing
	// into clientMap directly, so they never needed a conforming one.
	config.Name = "tokenexchangezerotools"
	config.PendingOAuthConfig = nil
	config.TokenExchange = &schemas.MCPTokenExchangeConfig{
		Audience: "https://example.invalid/api",
		ClientID: schemas.NewSecretVar("exchange-app"),
	}
	// Exactly what AfterFind produces for a verified server with no tools.
	config.DiscoveredTools = map[string]schemas.ChatTool{}

	require.NoError(t, m.AddClient(context.Background(), config))

	m.mu.RLock()
	state := m.clientMap[config.ID].State
	m.mu.RUnlock()

	assert.NotEqual(t, schemas.MCPConnectionStatePendingVerification, state,
		"a verified token-exchange client that simply exposes no tools must not be sent back through admin verification")
}

// TestAwaitsAdminVerification_NilIsTheDiscriminator states the same rule
// directly, for both auth types whose pending-ness is decided by the tool map.
func TestAwaitsAdminVerification_NilIsTheDiscriminator(t *testing.T) {
	for _, authType := range []schemas.MCPAuthType{schemas.MCPAuthTypePerUserHeaders, schemas.MCPAuthTypeTokenExchange} {
		t.Run(string(authType), func(t *testing.T) {
			config := &schemas.MCPClientConfig{AuthType: authType}

			config.DiscoveredTools = nil
			assert.True(t, awaitsAdminVerification(config), "a nil tool map means verification never ran")

			config.DiscoveredTools = map[string]schemas.ChatTool{}
			assert.False(t, awaitsAdminVerification(config), "a non-nil empty map means verification ran and found nothing")

			config.DiscoveredTools = map[string]schemas.ChatTool{"a": {}}
			assert.False(t, awaitsAdminVerification(config))
		})
	}
}
