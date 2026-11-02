package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/server"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bootClient is a sticky, auth-none HTTP boot client named name on url.
func bootClient(name, url string) *schemas.MCPClientConfig {
	return &schemas.MCPClientConfig{
		ID:                     name,
		Name:                   name,
		ConnectionType:         schemas.MCPConnectionTypeHTTP,
		ConnectionString:       schemas.NewSecretVar(url),
		AuthType:               schemas.MCPAuthTypeNone,
		NeedsSessionStickiness: schemas.Ptr(true),
	}
}

// TestConnectConfiguredClients_ReportsEachClientAsItFinishes: the boot dial
// reports each client as its attempt finishes, so a caller running it in the
// background can serve a ready client's tools while a slow one is still being
// dialled. Each client is reported once.
func TestConnectConfiguredClients_ReportsEachClientAsItFinishes(t *testing.T) {
	upstream := server.NewStreamableHTTPServer(server.NewMCPServer("boot", "1.0.0", server.WithToolCapabilities(true)))
	fast := httptest.NewServer(upstream)
	t.Cleanup(fast.Close)
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(time.Second)
		upstream.ServeHTTP(w, r)
	}))
	t.Cleanup(slow.Close)
	m := NewMCPManager(context.Background(), schemas.MCPConfig{ClientConfigs: []*schemas.MCPClientConfig{
		bootClient("slow", slow.URL), bootClient("fast", fast.URL),
	}}, nil, &MockLogger{}, nil)
	t.Cleanup(func() { _ = m.Cleanup() })

	var mu sync.Mutex
	var done []string
	m.ConnectConfiguredClients(context.Background(), func(clientID string) {
		mu.Lock()
		defer mu.Unlock()
		done = append(done, clientID)
	})
	assert.Equal(t, []string{"fast", "slow"}, done)
}

// TestConnectConfiguredClients_FailedDialIsKeptAndRecovered: a boot client
// whose dial fails is kept, unstable with the failure recorded, and gets a
// connection checker that recovers it.
func TestConnectConfiguredClients_FailedDialIsKeptAndRecovered(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(ts.Close)
	m := NewMCPManager(context.Background(), schemas.MCPConfig{ClientConfigs: []*schemas.MCPClientConfig{bootClient("down", ts.URL)}}, nil, &MockLogger{}, nil)
	t.Cleanup(func() { _ = m.Cleanup() })

	m.ConnectConfiguredClients(context.Background(), nil)
	state, ok := snapshotAddClientState(m, "down")
	require.True(t, ok, "the client is kept")
	assert.Equal(t, schemas.MCPConnectionStateUnstable, state.State)
	require.NotNil(t, state.LastFailure, "the failure is recorded")
	assert.Equal(t, schemas.MCPConnectionFailureStageConnect, state.LastFailure.Stage)
	m.checkerManager.mu.RLock()
	_, hasChecker := m.checkerManager.checkers["down"]
	m.checkerManager.mu.RUnlock()
	assert.True(t, hasChecker, "a checker recovers it")
}

// TestConnectConfiguredClients_ClientRemovedDuringTheDialStaysRemoved: the API
// serves while the boot dial runs, so a client can be removed while it is
// still being dialled. When that dial then fails, the client stays removed,
// with no checker. It used to be put back, unstable, with a checker that would
// reconnect a client an admin had deleted.
func TestConnectConfiguredClients_ClientRemovedDuringTheDialStaysRemoved(t *testing.T) {
	arrived := make(chan struct{}, 1)
	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case arrived <- struct{}{}:
		default:
		}
		<-release
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(ts.Close)
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	m := NewMCPManager(context.Background(), schemas.MCPConfig{ClientConfigs: []*schemas.MCPClientConfig{bootClient("removed", ts.URL)}}, nil, &MockLogger{}, nil)
	t.Cleanup(func() { _ = m.Cleanup() })

	dialDone := make(chan struct{})
	go func() {
		m.ConnectConfiguredClients(context.Background(), nil)
		close(dialDone)
	}()
	select {
	case <-arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("the boot dial never reached the upstream")
	}
	require.NoError(t, m.RemoveClient("removed"))
	close(release)
	select {
	case <-dialDone:
	case <-time.After(10 * time.Second):
		t.Fatal("the boot dial did not finish")
	}

	_, exists := snapshotAddClientState(m, "removed")
	assert.False(t, exists, "the removed client is not put back")
	m.checkerManager.mu.RLock()
	_, hasChecker := m.checkerManager.checkers["removed"]
	m.checkerManager.mu.RUnlock()
	assert.False(t, hasChecker, "and nothing is left checking it")
}
