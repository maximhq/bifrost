package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIsEnableable pins the guard that decides whether EnableClient will act
// on an entry.
//
// The regression it exists for: a first enable whose dial fails leaves the
// entry un-disabled (ExecutionConfig.Disabled=false) while its state goes
// back to Disabled, on purpose — a connection checker keeps retrying and the
// caller keeps its persisted disabled=false. That combination is what the
// second clause below admits. Parking such an entry at Unstable instead
// would wedge it: a guard testing only State==Disabled rejects every
// subsequent enable with "is not disabled (current state: unstable)", with
// the admin's UI still showing the toggle off.
func TestIsEnableable(t *testing.T) {
	tests := []struct {
		name        string
		state       schemas.MCPConnectionState
		cfgDisabled bool
		nilConfig   bool
		want        bool
	}{
		{
			name:  "cleanly disabled",
			state: schemas.MCPConnectionStateDisabled, cfgDisabled: true, want: true,
		},
		{
			// The wedge, as EnableClient now leaves it: the dial failed, so
			// state went back to Disabled while ExecutionConfig.Disabled
			// stayed false (checker retrying, persisted row still enabled).
			// This has to stay enableable or the retry is rejected forever.
			name:  "disabled state with config already enabled (failed enable)",
			state: schemas.MCPConnectionStateDisabled, cfgDisabled: false, want: true,
		},
		{
			// Guards the inverse of the fix: if some future change parks a
			// failed enable at Unstable again while the config reads enabled,
			// the client is wedged — nothing can enable it and the badge
			// disagrees with the toggle. Kept as an explicit false so that
			// regression has to be an intentional edit to this expectation.
			name:  "unstable with config enabled is not enableable",
			state: schemas.MCPConnectionStateUnstable, cfgDisabled: false, want: false,
		},
		{
			// The DB row was rolled back to disabled while the runtime moved
			// on — enabling must remain possible so the two can reconverge.
			name:  "unstable with config still disabled",
			state: schemas.MCPConnectionStateUnstable, cfgDisabled: true, want: true,
		},
		{
			name:  "healthy client is not enableable",
			state: schemas.MCPConnectionStateHealthy, cfgDisabled: false, want: false,
		},
		{
			name:  "needs_reauth client is not enableable",
			state: schemas.MCPConnectionStateNeedsReauth, cfgDisabled: false, want: false,
		},
		{
			name:  "pending_verification client is not enableable",
			state: schemas.MCPConnectionStatePendingVerification, cfgDisabled: false, want: false,
		},
		{
			// Defensive: a Disabled entry is enableable on state alone, so a
			// missing config must not panic the guard.
			name:  "disabled with nil config",
			state: schemas.MCPConnectionStateDisabled, nilConfig: true, want: true,
		},
		{
			name:  "unstable with nil config is not enableable",
			state: schemas.MCPConnectionStateUnstable, nilConfig: true, want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cs := &schemas.MCPClientState{State: tt.state}
			if !tt.nilConfig {
				cs.ExecutionConfig = &schemas.MCPClientConfig{Disabled: tt.cfgDisabled}
			}
			if got := isEnableable(cs); got != tt.want {
				t.Errorf("isEnableable(state=%q, cfgDisabled=%v, nilConfig=%v) = %v, want %v",
					tt.state, tt.cfgDisabled, tt.nilConfig, got, tt.want)
			}
		})
	}
}

// TestEnableClient_NilExecutionConfig_ReturnsErrorNotPanic pins the guard for
// an entry that is Disabled by state but carries no ExecutionConfig.
// isEnableable admits it on the state clause alone (deliberately — see the
// nil-config cases above), so EnableClient is the layer that has to notice the
// missing config: without a check it dereferences the nil pointer to flip
// Disabled and takes the whole process down on what should be a per-client
// error.
func TestEnableClient_NilExecutionConfig_ReturnsErrorNotPanic(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, expiredOAuthCredStore{}, nil, nil)

	m.mu.Lock()
	m.clientMap["client-nil-config"] = &schemas.MCPClientState{
		Name:  "client-nil-config",
		State: schemas.MCPConnectionStateDisabled,
	}
	m.mu.Unlock()

	err := m.EnableClient("client-nil-config")
	require.Error(t, err, "an entry with no ExecutionConfig must be rejected, not dereferenced")
	assert.Contains(t, err.Error(), "execution config")

	m.mu.RLock()
	state := *m.clientMap["client-nil-config"]
	m.mu.RUnlock()
	assert.Equal(t, schemas.MCPConnectionStateDisabled, state.State, "a rejected enable must leave the entry untouched")
	assert.Nil(t, state.ExecutionConfig)
}

// TestEnableClient_ConnectFailure_ParksDisabledAndKeepsRetrying is the
// end-to-end companion to TestIsEnableable: it drives the real enable path
// with a dial that fails and pins every claim the failure branch's comment
// makes — the ErrMCPEnableConnectFailed sentinel callers match on to keep
// their persisted disabled=false, the un-disabled ExecutionConfig, the state
// parked back at Disabled (not Unstable, which would wedge every retry), and
// the connection checker left running so the client can still come up on its
// own.
func TestEnableClient_ConnectFailure_ParksDisabledAndKeepsRetrying(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, genericFailureCredStore{}, nil, nil)
	defer m.checkerManager.StopAll()

	config := newSharedOAuthClientConfig("client-enable-dial-fails")
	config.Disabled = true

	m.mu.Lock()
	m.clientMap[config.ID] = &schemas.MCPClientState{
		Name:            config.Name,
		ExecutionConfig: config,
		State:           schemas.MCPConnectionStateDisabled,
	}
	m.mu.Unlock()

	err := m.EnableClient(config.ID)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrMCPEnableConnectFailed),
		"callers key off this sentinel to keep the persisted disabled=false; got: %v", err)

	m.mu.RLock()
	state := *m.clientMap[config.ID]
	m.mu.RUnlock()

	assert.False(t, state.ExecutionConfig.Disabled, "the enable itself stands — only the dial failed")
	assert.Equal(t, schemas.MCPConnectionStateDisabled, state.State,
		"a failed enable parks at Disabled so isEnableable keeps matching and the admin can retry")
	assert.True(t, isEnableable(&state), "the entry must remain enableable, not wedged")

	m.checkerManager.mu.RLock()
	_, checking := m.checkerManager.checkers[config.ID]
	m.checkerManager.mu.RUnlock()
	assert.True(t, checking, "a connection checker must be left retrying the dial in the background")
}

// TestEnableClient_ConnectFailure_PreservesNeedsReauth covers the one state
// the failure branch does not overwrite with Disabled: connectToMCPClient
// already classified this failure as a dead OAuth2 credential, which is the
// more specific and more actionable badge. Unlike the Disabled parking case,
// the entry is deliberately NOT left re-enableable — retrying the same dead
// credential cannot succeed, so the way out is reauthorization. Pinned here
// so a future change can't quietly make needs_reauth enable-retryable and
// spin the admin on a dial that is guaranteed to fail.
func TestEnableClient_ConnectFailure_PreservesNeedsReauth(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, expiredOAuthCredStore{}, nil, nil)
	defer m.checkerManager.StopAll()

	config := newSharedOAuthClientConfig("client-enable-needs-reauth")
	config.Disabled = true

	m.mu.Lock()
	m.clientMap[config.ID] = &schemas.MCPClientState{
		Name:            config.Name,
		ExecutionConfig: config,
		State:           schemas.MCPConnectionStateDisabled,
	}
	m.mu.Unlock()

	err := m.EnableClient(config.ID)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrMCPEnableConnectFailed))

	m.mu.RLock()
	state := *m.clientMap[config.ID]
	m.mu.RUnlock()

	assert.Equal(t, schemas.MCPConnectionStateNeedsReauth, state.State,
		"a dead OAuth credential is a more specific signal than Disabled and must survive the failure branch")
	assert.False(t, state.ExecutionConfig.Disabled)
	assert.False(t, isEnableable(&state),
		"needs_reauth must not be enable-retryable: the credential is dead, so reauthorization is the only way out")
}
