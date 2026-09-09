package mcp

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// registerHealthyClientWithChecker installs a Healthy client entry and a
// running checker for it, resolved against the manager's current global, the
// same way AddClient's per-call branch does. sticky selects
// needs_session_stickiness for an HTTP headers client.
func registerHealthyClientWithChecker(t *testing.T, m *MCPManager, id string, perClientInterval time.Duration, sticky bool) *ClientConnectionChecker {
	t.Helper()
	config := &schemas.MCPClientConfig{
		ID:                     id,
		Name:                   id,
		AuthType:               schemas.MCPAuthTypeHeaders,
		ConnectionType:         schemas.MCPConnectionTypeHTTP,
		ToolSyncInterval:       perClientInterval,
		NeedsSessionStickiness: &sticky,
	}
	m.mu.Lock()
	m.clientMap[id] = &schemas.MCPClientState{
		Name:            id,
		ExecutionConfig: config,
		State:           schemas.MCPConnectionStateHealthy,
		ToolMap:         map[string]schemas.ChatTool{},
		ToolNameMapping: map[string]string{},
	}
	m.mu.Unlock()
	checker := NewClientConnectionChecker(m, id, ResolveToolSyncInterval(config, m.checkerManager.GetGlobalInterval()), false, &MockLogger{})
	m.checkerManager.StartChecking(checker)
	t.Cleanup(func() { m.checkerManager.StopChecking(id) })
	return checker
}

// checkerState reads the checker's steady-state cadence and whether its armed
// timer is currently on that cadence, under the checker's lock.
func checkerState(c *ClientConnectionChecker) (healthy time.Duration, onSteady bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.healthyInterval, c.onSteadyCadence
}

func checkerRunning(c *ClientConnectionChecker) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.isRunning
}

func registeredChecker(m *MCPManager, id string) *ClientConnectionChecker {
	m.checkerManager.mu.RLock()
	defer m.checkerManager.mu.RUnlock()
	return m.checkerManager.checkers[id]
}

// TestResolveToolSyncInterval_InvalidOverridesFollowGlobal pins the resolver:
// a positive per-client value of at least one second wins, and everything a
// write path would have rejected (negative, sub-second) or zero follows the
// global, which itself falls back to the built-in default when unset.
func TestResolveToolSyncInterval_InvalidOverridesFollowGlobal(t *testing.T) {
	global := 7 * time.Minute
	cfg := func(d time.Duration) *schemas.MCPClientConfig { return &schemas.MCPClientConfig{ToolSyncInterval: d} }
	assert.Equal(t, 5*time.Minute, ResolveToolSyncInterval(cfg(5*time.Minute), global))
	assert.Equal(t, time.Second, ResolveToolSyncInterval(cfg(time.Second), global))
	assert.Equal(t, global, ResolveToolSyncInterval(cfg(0), global))
	assert.Equal(t, global, ResolveToolSyncInterval(cfg(-time.Minute), global), "legacy negative follows the global")
	assert.Equal(t, global, ResolveToolSyncInterval(cfg(500*time.Millisecond), global), "sub-second (e.g. a bare nanosecond integer) follows the global instead of spinning")
	assert.Equal(t, DefaultConnectionCheckInterval, ResolveToolSyncInterval(cfg(0), 0))
}

// TestUpdateToolSyncInterval_RetimesClientsFollowingGlobal pins the runtime
// half of the global setting: a change must re-time every running checker
// that follows the global (per-client 0) in place, leave explicit per-client
// overrides alone, and treat a legacy stored negative like zero.
func TestUpdateToolSyncInterval_RetimesClientsFollowingGlobal(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{ToolSyncInterval: time.Hour}, nil, nil, nil)
	follows := registerHealthyClientWithChecker(t, m, "follows-global", 0, false)
	explicit := registerHealthyClientWithChecker(t, m, "explicit", 5*time.Minute, false)
	legacyNegative := registerHealthyClientWithChecker(t, m, "legacy-negative", -time.Minute, false)

	healthy, onSteady := checkerState(follows)
	require.Equal(t, time.Hour, healthy, "sanity: follows the boot-time global")
	require.True(t, onSteady, "sanity: a Healthy client starts on the steady cadence")

	m.UpdateToolSyncInterval(20 * time.Minute)

	assert.Equal(t, 20*time.Minute, m.checkerManager.GetGlobalInterval())
	healthy, onSteady = checkerState(follows)
	assert.Equal(t, 20*time.Minute, healthy, "a client following the global must be re-timed")
	assert.True(t, onSteady)
	healthy, _ = checkerState(explicit)
	assert.Equal(t, 5*time.Minute, healthy, "an explicit per-client override must be untouched")
	healthy, _ = checkerState(legacyNegative)
	assert.Equal(t, 20*time.Minute, healthy, "a legacy negative (rejected at every write path now) follows the global like zero")

	// A non-positive global means "no override": back to the built-in default.
	m.UpdateToolSyncInterval(0)
	assert.Equal(t, DefaultConnectionCheckInterval, m.checkerManager.GetGlobalInterval())
	healthy, _ = checkerState(follows)
	assert.Equal(t, DefaultConnectionCheckInterval, healthy)
}

// TestSetHealthyInterval_LeavesUnstableCadenceAlone pins the one case a
// runtime interval change must not touch the armed timer: a client on the
// tight Unstable cadence keeps its fast recovery tick, and only the stored
// healthyInterval changes for the next successful check to pick up.
func TestSetHealthyInterval_LeavesUnstableCadenceAlone(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, nil, nil)
	config := &schemas.MCPClientConfig{ID: "unstable", Name: "unstable", AuthType: schemas.MCPAuthTypeHeaders, ConnectionType: schemas.MCPConnectionTypeHTTP}
	m.mu.Lock()
	m.clientMap[config.ID] = &schemas.MCPClientState{Name: config.Name, ExecutionConfig: config, State: schemas.MCPConnectionStateUnstable}
	m.mu.Unlock()

	checker := NewClientConnectionChecker(m, config.ID, time.Hour, false, &MockLogger{})
	m.checkerManager.StartChecking(checker)
	t.Cleanup(func() { m.checkerManager.StopChecking(config.ID) })

	_, onSteady := checkerState(checker)
	require.False(t, onSteady, "sanity: a non-Healthy client starts on the Unstable cadence")

	checker.SetHealthyInterval(5 * time.Minute)

	healthy, onSteady := checkerState(checker)
	assert.Equal(t, 5*time.Minute, healthy)
	assert.False(t, onSteady, "the fast recovery tick must not be re-armed by an interval edit")
}

// TestUpdateClient_RetimesCheckerOnToolSyncIntervalChange pins the per-client
// half: editing tool_sync_interval on a client whose connection mode does not
// change must re-time its running checker in place, in both directions
// (explicit override, and back to following the global).
func TestUpdateClient_RetimesCheckerOnToolSyncIntervalChange(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{ToolSyncInterval: time.Hour}, nil, nil, nil)
	checker := registerHealthyClientWithChecker(t, m, "retime", 0, false)

	update := func(interval time.Duration) {
		t.Helper()
		sticky := false
		require.NoError(t, m.UpdateClient("retime", &schemas.MCPClientConfig{
			ID:                     "retime",
			Name:                   "retime",
			AuthType:               schemas.MCPAuthTypeHeaders,
			ConnectionType:         schemas.MCPConnectionTypeHTTP,
			ToolSyncInterval:       interval,
			NeedsSessionStickiness: &sticky,
		}))
	}

	update(3 * time.Minute)
	require.Same(t, checker, registeredChecker(m, "retime"), "an interval-only edit must re-time the existing checker, not replace it")
	healthy, onSteady := checkerState(checker)
	assert.Equal(t, 3*time.Minute, healthy)
	assert.True(t, onSteady)

	update(0)
	healthy, _ = checkerState(checker)
	assert.Equal(t, time.Hour, healthy, "clearing the override must fall back to the global")
}

// TestUpdateClient_StickyToPerCall_KeepsCheckerRunning pins that flipping
// needs_session_stickiness off replaces the sticky checker with a per-call
// one instead of just stopping it: a per-call client depends on the checker's
// ephemeral discovery cycle for tool refresh, and used to lose it entirely
// until a restart after this flip.
func TestUpdateClient_StickyToPerCall_KeepsCheckerRunning(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{ToolSyncInterval: time.Hour}, nil, nil, nil)
	stickyChecker := registerHealthyClientWithChecker(t, m, "flip", 0, true)
	require.False(t, m.credStore.RequiresPerCallConnection(m.clientMap["flip"].ExecutionConfig), "sanity: starts sticky")

	perCall := false
	require.NoError(t, m.UpdateClient("flip", &schemas.MCPClientConfig{
		ID:                     "flip",
		Name:                   "flip",
		AuthType:               schemas.MCPAuthTypeHeaders,
		ConnectionType:         schemas.MCPConnectionTypeHTTP,
		NeedsSessionStickiness: &perCall,
	}))

	replacement := registeredChecker(m, "flip")
	require.NotNil(t, replacement, "a per-call checker must be registered after the flip")
	assert.NotSame(t, stickyChecker, replacement, "the sticky checker is replaced, not kept")
	assert.True(t, checkerRunning(replacement))
	healthy, _ := checkerState(replacement)
	assert.Equal(t, time.Hour, healthy, "resolved against the global like AddClient's per-call branch")
}

// TestUpdateClient_StickyToPerCall_DisabledClientGetsNoChecker pins the
// guard on that replacement: a disabled client keeps no checker (DisableClient
// stopped it and EnableClient starts a fresh one), so the flip must not
// resurrect one for it.
func TestUpdateClient_StickyToPerCall_DisabledClientGetsNoChecker(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, nil, nil)
	sticky := true
	config := &schemas.MCPClientConfig{
		ID:                     "flip-disabled",
		Name:                   "flipdisabled",
		AuthType:               schemas.MCPAuthTypeHeaders,
		ConnectionType:         schemas.MCPConnectionTypeHTTP,
		NeedsSessionStickiness: &sticky,
		Disabled:               true,
	}
	m.mu.Lock()
	m.clientMap[config.ID] = &schemas.MCPClientState{Name: config.Name, ExecutionConfig: config, State: schemas.MCPConnectionStateDisabled}
	m.mu.Unlock()

	perCall := false
	require.NoError(t, m.UpdateClient(config.ID, &schemas.MCPClientConfig{
		ID:                     config.ID,
		Name:                   config.Name,
		AuthType:               schemas.MCPAuthTypeHeaders,
		ConnectionType:         schemas.MCPConnectionTypeHTTP,
		NeedsSessionStickiness: &perCall,
		Disabled:               true,
	}))

	assert.Nil(t, registeredChecker(m, config.ID), "a disabled client must not get a checker from the flip")
	m.mu.RLock()
	state := m.clientMap[config.ID].State
	m.mu.RUnlock()
	assert.Equal(t, schemas.MCPConnectionStateDisabled, state)
}

// TestEnableClient_PerCall_RestartsChecker pins that a per-call client gets
// its periodic checker back after disable -> enable. DisableClient stops the
// checker; without EnableClient starting a fresh one, the client would never
// refresh its tools again until a restart, and interval edits would be
// silent no-ops for it.
func TestEnableClient_PerCall_RestartsChecker(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{ToolSyncInterval: time.Hour}, nil, nil, nil)
	registerHealthyClientWithChecker(t, m, "cycle", 0, false)

	require.NoError(t, m.DisableClient("cycle"))
	require.Nil(t, registeredChecker(m, "cycle"), "sanity: DisableClient stops the checker")

	require.NoError(t, m.EnableClient("cycle"))

	restarted := registeredChecker(m, "cycle")
	require.NotNil(t, restarted, "EnableClient must start a per-call checker again")
	assert.True(t, checkerRunning(restarted))
	healthy, onSteady := checkerState(restarted)
	assert.Equal(t, time.Hour, healthy, "resolved against the global, as at AddClient")
	assert.True(t, onSteady, "the client is Healthy again, so the checker starts on the steady cadence")
}
