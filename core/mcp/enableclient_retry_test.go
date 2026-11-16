package mcp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/server"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIsEnableable pins the guard that decides whether EnableClient will act
// on an entry: a Disabled state, or a config that still says disabled while
// the runtime state has moved on, so the two can reconverge.
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
			// A Disabled state is enableable on its own.
			name:  "disabled state with config already enabled",
			state: schemas.MCPConnectionStateDisabled, cfgDisabled: false, want: true,
		},
		{
			// Where a failed enable leaves a client: it is already enabled and
			// its checker redials it, so there is nothing for EnableClient to do.
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

// TestEnableClient_ConnectFailure_StaysUnstableAndItsCheckerRetries drives the
// real enable path with a dial that fails and pins what the failure branch
// leaves behind: the ErrMCPEnableConnectFailed sentinel callers match on to
// keep their persisted disabled=false, the un-disabled ExecutionConfig, an
// Unstable state carrying the failure, and a checker whose tick dials again.
// The client used to be parked back at Disabled, where its checker stopped
// instead of dialling, so only a manual reconnect could bring it back up.
func TestEnableClient_ConnectFailure_StaysUnstableAndItsCheckerRetries(t *testing.T) {
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

	assert.False(t, state.ExecutionConfig.Disabled, "the enable itself stands; only the dial failed")
	assert.Equal(t, schemas.MCPConnectionStateUnstable, state.State,
		"an enabled client whose dial failed is unstable, not disabled")
	require.NotNil(t, state.LastFailure, "the failure is recorded on the client")
	assert.Equal(t, schemas.MCPConnectionFailureStageConnect, state.LastFailure.Stage)

	m.checkerManager.mu.RLock()
	checker, checking := m.checkerManager.checkers[config.ID]
	m.checkerManager.mu.RUnlock()
	require.True(t, checking, "a checker is running for the client")
	require.Equal(t, 1, cred.calls(), "the enable itself dialled exactly once")

	// Driving performCheck directly keeps this deterministic: waiting out the
	// real first tick would add seconds and still only observe the same dial.
	next, onSteady := checker.performCheck()
	assert.Equal(t, UnstableConnectionCheckInterval, next, "the checker keeps the unstable cadence")
	assert.False(t, onSteady)
	require.Eventually(t, func() bool { return cred.calls() == 2 }, 5*time.Second, 10*time.Millisecond,
		"its tick dials the client again")
}

// toggleCredStore fails ConnectionHeaders while down is set, standing in for an
// upstream that cannot be reached, and resolves empty headers once it is cleared.
type toggleCredStore struct {
	down atomic.Bool
}

// ConnectionHeaders fails while the upstream is down.
func (c *toggleCredStore) ConnectionHeaders(_ *schemas.BifrostContext, _ *schemas.MCPClientConfig) (http.Header, error) {
	if c.down.Load() {
		return nil, fmt.Errorf("connection refused")
	}
	return http.Header{}, nil
}

// RequestHeaders adds nothing to a request.
func (c *toggleCredStore) RequestHeaders(_ *schemas.BifrostContext, _ *schemas.MCPClientConfig) (http.Header, error) {
	return http.Header{}, nil
}

// RequiresPerCallConnection keeps the client sticky.
func (c *toggleCredStore) RequiresPerCallConnection(_ *schemas.MCPClientConfig) bool {
	return false
}

// ForceRefresh has nothing to refresh.
func (c *toggleCredStore) ForceRefresh(_ *schemas.BifrostContext, _ *schemas.MCPClientConfig) error {
	return nil
}

// AdminConnectionHeaders is not used by these tests.
func (c *toggleCredStore) AdminConnectionHeaders(_ context.Context, _ *schemas.MCPClientConfig) (http.Header, error) {
	return nil, fmt.Errorf("not implemented")
}

// TestEnableClient_UpstreamBackAfterFailedEnable_RecoversWithoutAdmin pins that
// a sticky client enabled while its upstream is down comes up on its own once
// the upstream is back: its checker reconnects it with no admin action, and it
// can then be disabled again. It used to stay at state disabled with disabled
// false, its checker stopped, and a disable was refused as "already disabled".
func TestEnableClient_UpstreamBackAfterFailedEnable_RecoversWithoutAdmin(t *testing.T) {
	ts := buildGatedMCPServer(t, nil)
	cred := &toggleCredStore{}
	cred.down.Store(true)
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, cred, nil, nil)
	defer m.checkerManager.StopAll()

	config := newMakeBeforeBreakConfig("client-reenable", ts.URL)
	config.Disabled = true
	m.mu.Lock()
	m.clientMap[config.ID] = &schemas.MCPClientState{
		Name:            config.Name,
		ExecutionConfig: config,
		State:           schemas.MCPConnectionStateDisabled,
	}
	m.mu.Unlock()

	require.ErrorIs(t, m.EnableClient(config.ID), ErrMCPEnableConnectFailed)

	cred.down.Store(false)
	m.checkerManager.mu.RLock()
	checker, checking := m.checkerManager.checkers[config.ID]
	m.checkerManager.mu.RUnlock()
	require.True(t, checking, "a checker is running for the client")
	checker.performCheck()
	require.Eventually(t, func() bool {
		m.mu.RLock()
		defer m.mu.RUnlock()
		cs := m.clientMap[config.ID]
		return cs.State == schemas.MCPConnectionStateHealthy && cs.Conn != nil
	}, 10*time.Second, 20*time.Millisecond, "the checker reconnects the client once the upstream is back")

	require.NoError(t, m.DisableClient(config.ID), "the recovered client can be disabled again")
	m.mu.RLock()
	assert.Equal(t, schemas.MCPConnectionStateDisabled, m.clientMap[config.ID].State)
	m.mu.RUnlock()
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

// newStalledMCPServer is an upstream that accepts connections but never
// answers, so a dial to it runs until its context ends.
func newStalledMCPServer(t *testing.T) *httptest.Server {
	t.Helper()
	stop := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-stop:
		}
	}))
	// Cleanups run last-registered first: the stalled handlers are released
	// before Close waits for them.
	t.Cleanup(ts.Close)
	t.Cleanup(func() { close(stop) })
	return ts
}

// newCheckerReconnectFixture registers a sticky client on serverURL as Unstable
// with a recorded failure, starts a checker reconnect for it, and waits until
// that reconnect holds the client's operation slot.
func newCheckerReconnectFixture(t *testing.T, id, serverURL string, cred schemas.MCPCredentialStore) (*MCPManager, *schemas.MCPClientConfig, chan error) {
	t.Helper()
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, cred, nil, nil)
	t.Cleanup(m.checkerManager.StopAll)

	config := newMakeBeforeBreakConfig(id, serverURL)
	m.mu.Lock()
	m.clientMap[config.ID] = &schemas.MCPClientState{
		Name:            config.Name,
		ExecutionConfig: config,
		State:           schemas.MCPConnectionStateUnstable,
		LastFailure:     &schemas.MCPConnectionFailure{Stage: schemas.MCPConnectionFailureStageConnect, Message: "connection refused"},
	}
	m.mu.Unlock()

	reconnected := make(chan error, 1)
	go func() { reconnected <- m.reconnectFromChecker(config.ID) }()
	require.Eventually(t, func() bool {
		v, ok := m.reconnectingClients.Load(config.ID)
		if !ok {
			return false
		}
		op := v.(*inflightClientOp)
		select {
		case <-op.done:
			return false
		default:
			return op.cancel != nil
		}
	}, 5*time.Second, 10*time.Millisecond, "the checker reconnect holds the client's operation slot")
	return m, config, reconnected
}

// TestCheckerReconnect_GivesWayToDisable pins that disabling a client whose
// checker is redialling an unreachable upstream succeeds at once: the reconnect
// is cancelled instead of the disable being refused as already in progress.
func TestCheckerReconnect_GivesWayToDisable(t *testing.T) {
	m, config, reconnected := newCheckerReconnectFixture(t, "client-disable-during-reconnect", newStalledMCPServer(t).URL, &toggleCredStore{})

	started := time.Now()
	require.NoError(t, m.DisableClient(config.ID))
	assert.Less(t, time.Since(started), checkerReconnectYieldWait, "the disable does not wait out the dial")
	select {
	case err := <-reconnected:
		require.Error(t, err, "the cancelled reconnect reports that it did not connect")
	case <-time.After(5 * time.Second):
		t.Fatal("the checker reconnect did not stop")
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	assert.Equal(t, schemas.MCPConnectionStateDisabled, m.clientMap[config.ID].State)
}

// TestCheckerReconnect_GivesWayToAnEdit pins that editing such a client also
// goes ahead, and that the cancelled dial is not recorded as the client's
// failure: it keeps the reason of its last real one.
func TestCheckerReconnect_GivesWayToAnEdit(t *testing.T) {
	m, config, reconnected := newCheckerReconnectFixture(t, "client-edit-during-reconnect", newStalledMCPServer(t).URL, &toggleCredStore{})

	renamed := *config
	renamed.Name = "mbb_renamed"
	require.NoError(t, m.UpdateClient(config.ID, &renamed))
	select {
	case <-reconnected:
	case <-time.After(5 * time.Second):
		t.Fatal("the checker reconnect did not stop")
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	cs := m.clientMap[config.ID]
	assert.Equal(t, "mbb_renamed", cs.ExecutionConfig.Name)
	assert.Equal(t, schemas.MCPConnectionStateUnstable, cs.State)
	require.NotNil(t, cs.LastFailure)
	assert.Equal(t, "connection refused", cs.LastFailure.Message, "the cancelled dial does not replace the last real failure")
}

// TestCheckerReconnect_DoesNotTakeTheSlot pins the other direction: a checker
// reconnect never cancels another operation, it reports the client busy.
func TestCheckerReconnect_DoesNotTakeTheSlot(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, &toggleCredStore{}, nil, nil)
	defer m.checkerManager.StopAll()

	finish, ok := m.beginExclusiveClientOp("client-busy")
	require.True(t, ok)
	defer finish(nil)
	assert.ErrorIs(t, m.reconnectFromChecker("client-busy"), errReconnectInProgress)
}

// blockingCredStore resolves connection headers only when its context ends,
// standing in for a credential refresh against an identity provider that does
// not answer. entered is closed once a resolution has started.
type blockingCredStore struct {
	toggleCredStore
	once    sync.Once
	entered chan struct{}
}

// ConnectionHeaders waits for its context and reports why it ended.
func (c *blockingCredStore) ConnectionHeaders(ctx *schemas.BifrostContext, _ *schemas.MCPClientConfig) (http.Header, error) {
	c.once.Do(func() { close(c.entered) })
	<-ctx.Done()
	return nil, ctx.Err()
}

// newListStalledMCPServer answers the MCP handshake but never answers
// tools/list, so a reconnect to it stalls in discovery. listed is closed once a
// tools/list request has arrived.
func newListStalledMCPServer(t *testing.T) (*httptest.Server, chan struct{}) {
	t.Helper()
	s := server.NewMCPServer("list-stalled", "1.0.0", server.WithToolCapabilities(false))
	streamable := server.NewStreamableHTTPServer(s)
	stop := make(chan struct{})
	listed := make(chan struct{})
	var once sync.Once
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte(`"tools/list"`)) {
			once.Do(func() { close(listed) })
			select {
			case <-r.Context().Done():
			case <-stop:
			}
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		streamable.ServeHTTP(w, r)
	}))
	// Cleanups run last-registered first: the stalled handlers are released
	// before Close waits for them.
	t.Cleanup(ts.Close)
	t.Cleanup(func() { close(stop) })
	return ts, listed
}

// requireDisableOvertakes disables the client and pins that it went ahead at
// once, and that the checker reconnect it overtook stopped.
func requireDisableOvertakes(t *testing.T, m *MCPManager, id string, reconnected chan error) {
	t.Helper()
	started := time.Now()
	require.NoError(t, m.DisableClient(id))
	assert.Less(t, time.Since(started), checkerReconnectYieldWait, "the disable does not wait out the reconnect")
	select {
	case <-reconnected:
	case <-time.After(5 * time.Second):
		t.Fatal("the checker reconnect did not stop")
	}
}

// TestCheckerReconnect_GivesWayWhileResolvingCredentials pins that a checker
// reconnect waiting on its connection credentials, such as a token refresh
// against an identity provider that does not answer, stops for a disable.
func TestCheckerReconnect_GivesWayWhileResolvingCredentials(t *testing.T) {
	cred := &blockingCredStore{entered: make(chan struct{})}
	m, config, reconnected := newCheckerReconnectFixture(t, "client-credentials-during-reconnect", newStalledMCPServer(t).URL, cred)
	select {
	case <-cred.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the reconnect never resolved its credentials")
	}
	requireDisableOvertakes(t, m, config.ID, reconnected)
}

// TestCheckerReconnect_GivesWayDuringDiscovery pins that a checker reconnect
// whose handshake succeeded but whose tools/list is not answered stops for a
// disable too.
func TestCheckerReconnect_GivesWayDuringDiscovery(t *testing.T) {
	ts, listed := newListStalledMCPServer(t)
	m, config, reconnected := newCheckerReconnectFixture(t, "client-discovery-during-reconnect", ts.URL, &toggleCredStore{})
	select {
	case <-listed:
	case <-time.After(10 * time.Second):
		t.Fatal("the reconnect never reached discovery")
	}
	requireDisableOvertakes(t, m, config.ID, reconnected)
}
