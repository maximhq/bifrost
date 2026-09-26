package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewConnectionFailure_NormalizesMessage covers the record's message
// hygiene: an upstream error that arrives as a multi-line body collapses to
// one line, and one long enough to bloat every list response and heartbeat
// is cut at connectionFailureMessageMaxLen on a rune boundary.
func TestNewConnectionFailure_NormalizesMessage(t *testing.T) {
	multiline := errors.New("dial failed:\n\tconnection refused\n\n  (retry later)")
	got := newConnectionFailure(schemas.MCPConnectionFailureStageConnect, multiline, nil)
	assert.Equal(t, "dial failed: connection refused (retry later)", got.Message)

	long := errors.New(strings.Repeat("é", connectionFailureMessageMaxLen+50))
	got = newConnectionFailure(schemas.MCPConnectionFailureStageConnect, long, nil)
	assert.Equal(t, connectionFailureMessageMaxLen, len([]rune(got.Message)), "cut on a rune boundary, ellipsis included")
	assert.True(t, strings.HasSuffix(got.Message, "…"))

	assert.Empty(t, newConnectionFailure(schemas.MCPConnectionFailureStageConnect, nil, nil).Message)
}

// TestNewConnectionFailure_PreservesSince covers the two timestamps: At is
// always this attempt, Since is inherited from the record being replaced so
// it keeps marking where the current unhealthy run began.
func TestNewConnectionFailure_PreservesSince(t *testing.T) {
	first := newConnectionFailure(schemas.MCPConnectionFailureStagePing, errors.New("one"), nil)
	assert.Equal(t, first.At, first.Since, "a first failure starts its own run")

	time.Sleep(2 * time.Millisecond)
	second := newConnectionFailure(schemas.MCPConnectionFailureStageListTools, errors.New("two"), first)
	assert.Equal(t, first.Since, second.Since, "a follow-up failure keeps the run's start")
	assert.True(t, second.At.After(first.At), "At always reflects the latest attempt")
	assert.Equal(t, schemas.MCPConnectionFailureStageListTools, second.Stage)
	assert.Equal(t, "two", second.Message)
}

// TestSetState_RecordsFailureAndClearsOnHealthy walks the periodic checker's
// own record lifecycle: a failed tick writes the record, a second failed tick
// refreshes At while keeping Since and swaps in the new stage/message even
// though the state itself does not change, and the recovering tick clears
// it along with the flip to Healthy.
func TestSetState_RecordsFailureAndClearsOnHealthy(t *testing.T) {
	manager := &MCPManager{
		logger:    &MockLogger{},
		clientMap: map[string]*schemas.MCPClientState{},
	}
	state, config := newSyncTestClientState("client-rec", "rec-client", schemas.MCPAuthTypeHeaders, nil)
	state.State = schemas.MCPConnectionStateHealthy
	manager.clientMap[config.ID] = state
	checker := NewClientConnectionChecker(manager, config.ID, time.Minute, false, &MockLogger{})

	checker.recordFailure(config.Name, schemas.MCPConnectionFailureStagePing, errors.New("ping: i/o timeout"), 0)
	first := manager.clientMap[config.ID].LastFailure
	require.NotNil(t, first)
	assert.Equal(t, schemas.MCPConnectionStateUnstable, manager.clientMap[config.ID].State)
	assert.Equal(t, schemas.MCPConnectionFailureStagePing, first.Stage)
	assert.Equal(t, "ping: i/o timeout", first.Message)

	time.Sleep(2 * time.Millisecond)
	checker.recordFailure(config.Name, schemas.MCPConnectionFailureStageListTools, errors.New("context deadline exceeded"), 0)
	second := manager.clientMap[config.ID].LastFailure
	require.NotNil(t, second)
	assert.NotSame(t, first, second, "the record is replaced, never mutated in place")
	assert.Equal(t, schemas.MCPConnectionFailureStageListTools, second.Stage)
	assert.Equal(t, "context deadline exceeded", second.Message)
	assert.Equal(t, first.Since, second.Since)
	assert.True(t, second.At.After(first.At))
	assert.Equal(t, schemas.MCPConnectionFailureStagePing, first.Stage, "the snapshot a reader already holds is untouched")

	checker.recordSuccess(config.Name, 0)
	assert.Equal(t, schemas.MCPConnectionStateHealthy, manager.clientMap[config.ID].State)
	assert.Nil(t, manager.clientMap[config.ID].LastFailure, "Healthy never carries a stale explanation")
}

// TestFailConnectAttempt_RecordsFailureByClassification checks the connect
// path's stage choice: a dead credential is recorded as a credential failure
// alongside NeedsReauth, anything else as a connect failure alongside
// Unstable, and the not-a-failure exits (nil error) leave the existing record
// exactly as it was.
func TestFailConnectAttempt_RecordsFailureByClassification(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, expiredOAuthCredStore{}, nil, nil)
	config := newSharedOAuthClientConfig("client-fail-record")
	entry := &schemas.MCPClientState{Name: config.Name, ExecutionConfig: config, State: schemas.MCPConnectionStateHealthy}
	m.mu.Lock()
	m.clientMap[config.ID] = entry
	m.mu.Unlock()

	m.failConnectAttempt(entry, config, nil, nil, nil, nil, false, errors.New("dial tcp: connection refused"))
	require.NotNil(t, entry.LastFailure)
	assert.Equal(t, schemas.MCPConnectionStateUnstable, entry.State)
	assert.Equal(t, schemas.MCPConnectionFailureStageConnect, entry.LastFailure.Stage)
	assert.Equal(t, "dial tcp: connection refused", entry.LastFailure.Message)
	connectRecord := entry.LastFailure

	m.failConnectAttempt(entry, config, nil, nil, nil, nil, false, nil)
	assert.Same(t, connectRecord, entry.LastFailure, "a removed/disabled-during-setup exit is not a connection failure and must not touch the record")

	m.failConnectAttempt(entry, config, nil, nil, nil, nil, true, errors.New("oauth2 token expired"))
	require.NotNil(t, entry.LastFailure)
	assert.Equal(t, schemas.MCPConnectionStateNeedsReauth, entry.State)
	assert.Equal(t, schemas.MCPConnectionFailureStageCredential, entry.LastFailure.Stage)
	assert.Equal(t, connectRecord.Since, entry.LastFailure.Since, "the run started with the first connect failure")
}

// TestHandleSSEConnectionLost_RecordsTransportFailure covers the one failure
// path that happens outside any check or connect attempt: a live SSE stream
// dropping. The record names the stage so the UI can distinguish "the
// connection went away" from "a check found it unresponsive".
func TestHandleSSEConnectionLost_RecordsTransportFailure(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, expiredOAuthCredStore{}, nil, nil)
	config := newSharedOAuthClientConfig("client-sse-lost")
	entry := &schemas.MCPClientState{Name: config.Name, ExecutionConfig: config, State: schemas.MCPConnectionStateHealthy, ConnGeneration: 3}
	m.mu.Lock()
	m.clientMap[config.ID] = entry
	m.mu.Unlock()

	m.handleSSEConnectionLost(config.ID, config.Name, 3, errors.New("unexpected EOF"))
	require.NotNil(t, entry.LastFailure)
	assert.Equal(t, schemas.MCPConnectionStateUnstable, entry.State)
	assert.Equal(t, schemas.MCPConnectionFailureStageTransportLost, entry.LastFailure.Stage)
	assert.Equal(t, "unexpected EOF", entry.LastFailure.Message)

	// A stale callback (generation already bumped by a reconnect) is ignored
	// wholesale, record included.
	entry.State = schemas.MCPConnectionStateHealthy
	entry.LastFailure = nil
	m.handleSSEConnectionLost(config.ID, config.Name, 2, errors.New("stale"))
	assert.Equal(t, schemas.MCPConnectionStateHealthy, entry.State)
	assert.Nil(t, entry.LastFailure)
}

// TestGetClients_SnapshotCarriesFailure confirms the record survives the
// manager's snapshot copy, since that copy is what the client-list API and a
// distributed deployment's node-state heartbeat both read.
func TestGetClients_SnapshotCarriesFailure(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, expiredOAuthCredStore{}, nil, nil)
	config := newSharedOAuthClientConfig("client-snapshot")
	entry := &schemas.MCPClientState{Name: config.Name, ExecutionConfig: config, State: schemas.MCPConnectionStateUnstable}
	recordClientFailure(entry, schemas.MCPConnectionFailureStageListTools, errors.New("boom"))
	m.mu.Lock()
	m.clientMap[config.ID] = entry
	m.mu.Unlock()

	clients := m.GetClients()
	require.Len(t, clients, 1)
	require.NotNil(t, clients[0].LastFailure)
	assert.Equal(t, "boom", clients[0].LastFailure.Message)
}
