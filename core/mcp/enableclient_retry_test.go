package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
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

// TestEnableClient_ConnectFailure_ParksDisabledAndStaysEnableable is the
// end-to-end companion to TestIsEnableable: it drives the real enable path
// with a dial that fails and pins every claim the failure branch's comment
// makes — the ErrMCPEnableConnectFailed sentinel callers match on to keep
// their persisted disabled=false, the un-disabled ExecutionConfig, and the
// state parked back at Disabled (not Unstable, which would wedge every retry).
//
// Note what is NOT claimed: a checker is registered, but performCheck stops it
// on its first tick rather than dialling a Disabled client, so nothing retries
// this dial automatically. Recovery is the admin enabling again, which the
// Disabled parking state exists to keep possible.
func TestEnableClient_ConnectFailure_ParksDisabledAndStaysEnableable(t *testing.T) {
	cred := &countingFailureCredStore{}
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, cred, nil, nil)
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
	checker, checking := m.checkerManager.checkers[config.ID]
	m.checkerManager.mu.RUnlock()
	require.True(t, checking, "a checker is registered for the NeedsReauth case this branch shares")

	// The claim above is that this checker does not retry the dial, so assert
	// it rather than describing it. Driving performCheck directly keeps that
	// deterministic: waiting out the real first tick would add seconds and
	// still only observe the same call.
	dialsBeforeTick := cred.calls()
	require.Equal(t, 1, dialsBeforeTick, "the enable itself dialled exactly once")

	next, onSteady := checker.performCheck()

	// Two assertions, because neither covers the other. The dial counter catches
	// a redial made on this goroutine; the self-stop below is what rules out an
	// async one, since a checker that has stopped never ticks again and this
	// branch's only reconnect path is a goroutine the counter would race.
	//
	// reconnectingClients is deliberately not used: EnableClient took the same
	// exclusive-op slot for its own dial, and a finished op is left in the map
	// on purpose, so an entry there says nothing about the checker.
	assert.Equal(t, dialsBeforeTick, cred.calls(),
		"a Disabled client must not be redialled by its checker: recovery here is the admin enabling again")

	checker.mu.Lock()
	running := checker.isRunning
	checker.mu.Unlock()
	assert.False(t, running, "the checker stops itself on that first tick rather than staying armed")
	assert.True(t, onSteady, "and leaves the relaxed cadence behind it")
	assert.Equal(t, checker.healthyInterval, next)
}

// countingFailureCredStore is genericFailureCredStore with a dial counter, so a
// test can assert that no *further* dial happened rather than only that the
// first one failed. Kept local: genericFailureCredStore is shared by other
// tests that have no reason to carry a counter.
type countingFailureCredStore struct {
	mu sync.Mutex
	n  int
}

func (c *countingFailureCredStore) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func (c *countingFailureCredStore) ConnectionHeaders(_ *schemas.BifrostContext, _ *schemas.MCPClientConfig) (http.Header, error) {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
	return nil, fmt.Errorf("connection refused")
}

func (c *countingFailureCredStore) RequestHeaders(_ *schemas.BifrostContext, _ *schemas.MCPClientConfig) (http.Header, error) {
	return http.Header{}, nil
}

func (c *countingFailureCredStore) RequiresPerCallConnection(_ *schemas.MCPClientConfig) bool {
	return false
}

func (c *countingFailureCredStore) ForceRefresh(_ *schemas.BifrostContext, _ *schemas.MCPClientConfig) error {
	return nil
}

func (c *countingFailureCredStore) AdminConnectionHeaders(_ context.Context, _ *schemas.MCPClientConfig) (http.Header, error) {
	return nil, fmt.Errorf("not implemented")
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
