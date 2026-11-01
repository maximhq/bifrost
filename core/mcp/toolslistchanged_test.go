package mcp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An upstream that declares tools.listChanged and changes its tools is re-listed straight away,
// on every transport and revision a persistent connection can speak, without waiting for the
// connection checker (10 minutes once healthy).
func TestUpstreamToolsListChanged_RefreshesTools(t *testing.T) {
	httpUpstream := func(version string) func(t *testing.T) (*schemas.MCPClientConfig, func()) {
		return func(t *testing.T) (*schemas.MCPClientConfig, func()) {
			s := newMatrixUpstream()
			ts := httptest.NewServer(server.NewStreamableHTTPServer(s, server.WithStreamableHTTPProtocolVersions(version)))
			t.Cleanup(ts.Close)
			return &schemas.MCPClientConfig{ConnectionType: schemas.MCPConnectionTypeHTTP, ConnectionString: schemas.NewSecretVar(ts.URL)},
				func() { addMatrixTool(s) }
		}
	}
	cases := []struct {
		name     string
		upstream func(t *testing.T) (*schemas.MCPClientConfig, func())
	}{
		{"http/" + mcpgo.ProtocolVersion20250618, httpUpstream(mcpgo.ProtocolVersion20250618)},
		{"http/" + mcpgo.ProtocolVersion20251125, httpUpstream(mcpgo.ProtocolVersion20251125)},
		{"http/" + mcpgo.ProtocolVersion20260728, httpUpstream(mcpgo.ProtocolVersion20260728)},
		{"sse", func(t *testing.T) (*schemas.MCPClientConfig, func()) {
			s := newMatrixUpstream()
			ts := httptest.NewServer(server.NewSSEServer(s))
			t.Cleanup(ts.Close)
			return &schemas.MCPClientConfig{ConnectionType: schemas.MCPConnectionTypeSSE, ConnectionString: schemas.NewSecretVar(ts.URL + "/sse")},
				func() { addMatrixTool(s) }
		}},
		{"stdio", func(t *testing.T) (*schemas.MCPClientConfig, func()) {
			// The upstream runs in a child process and adds the tool on its own after a delay.
			return &schemas.MCPClientConfig{
				ConnectionType: schemas.MCPConnectionTypeSTDIO,
				StdioConfig:    &schemas.MCPStdioConfig{Command: os.Args[0], Envs: []string{stdioServerEnv + "=1", stdioAddToolAfterEnv + "=1s"}},
			}, func() {}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config, change := tc.upstream(t)
			config.ID = "list-changed-" + tc.name
			config.Name = "matrix"
			config.NeedsSessionStickiness = schemas.Ptr(true)
			config.ToolsToExecute = []string{"*"}

			m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
			t.Cleanup(func() { _ = m.Cleanup() })
			require.NoError(t, m.connectToMCPClient(context.Background(), config))
			state, ok := snapshotClientState(m, config.ID)
			require.True(t, ok)
			require.NotContains(t, state.ToolMap, "matrix-added")

			change()
			require.Eventually(t, func() bool {
				state, ok := snapshotClientState(m, config.ID)
				_, added := state.ToolMap["matrix-added"]
				return ok && added
			}, 5*time.Second, 50*time.Millisecond, "the upstream's tools/list_changed must trigger a re-list")
		})
	}
}

// The second dial that opens the 2025-era GET stream is only paid for upstreams that say they
// will announce tool changes.
func TestUpstreamToolsListChanged_ListensOnlyWhenAnnounced(t *testing.T) {
	for _, announces := range []bool{true, false} {
		t.Run(map[bool]string{true: "announces", false: "silent"}[announces], func(t *testing.T) {
			s := server.NewMCPServer("silent-upstream", "1.0.0", server.WithToolCapabilities(announces))
			addMatrixTool(s)
			counter := newWireCounter()
			ts := httptest.NewServer(counter.wrap(server.NewStreamableHTTPServer(s, server.WithStreamableHTTPProtocolVersions(mcpgo.ProtocolVersion20251125))))
			t.Cleanup(ts.Close)

			m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
			t.Cleanup(func() { _ = m.Cleanup() })
			config := &schemas.MCPClientConfig{
				ID: "listen-only-when-announced", Name: "matrix", ConnectionType: schemas.MCPConnectionTypeHTTP,
				ConnectionString: schemas.NewSecretVar(ts.URL), NeedsSessionStickiness: schemas.Ptr(true), ToolsToExecute: []string{"*"},
			}
			require.NoError(t, m.connectToMCPClient(context.Background(), config))

			if announces {
				assert.Equal(t, 2, counter.count("initialize"), "one extra handshake opens the listening connection")
				require.Eventually(t, func() bool { return counter.count(http.MethodGet) > 0 }, 2*time.Second, 20*time.Millisecond, "the GET stream opens")
				return
			}
			assert.Equal(t, 1, counter.count("initialize"), "no listening connection for an upstream that announces nothing")
			require.Never(t, func() bool { return counter.count(http.MethodGet) > 0 }, 500*time.Millisecond, 20*time.Millisecond, "no GET stream")
		})
	}
}

// A burst of notifications costs one re-list plus at most one follow-up, not one each.
func TestUpstreamToolsListChanged_CoalescesBursts(t *testing.T) {
	counter := newWireCounter()
	ts := httptest.NewServer(counter.wrap(server.NewStreamableHTTPServer(newMatrixUpstream(), server.WithStreamableHTTPProtocolVersions(mcpgo.ProtocolVersion20251125))))
	t.Cleanup(ts.Close)
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	t.Cleanup(func() { _ = m.Cleanup() })
	config := &schemas.MCPClientConfig{
		ID: "coalesce", Name: "matrix", ConnectionType: schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar(ts.URL), NeedsSessionStickiness: schemas.Ptr(true), ToolsToExecute: []string{"*"},
	}
	require.NoError(t, m.connectToMCPClient(context.Background(), config))
	state, ok := snapshotClientState(m, config.ID)
	require.True(t, ok)
	before := counter.count("tools/list")

	for range 50 {
		m.refreshOnUpstreamChange(config.ID, state.Conn, state.ConnGeneration)
	}
	require.Eventually(t, func() bool { return counter.count("tools/list") > before }, 2*time.Second, 10*time.Millisecond)
	time.Sleep(300 * time.Millisecond)
	assert.LessOrEqual(t, counter.count("tools/list")-before, 2, "50 notifications collapse into one re-list and one follow-up")

	// A notification from a connection that has since been replaced is ignored.
	stale := counter.count("tools/list")
	m.refreshOnUpstreamChange(config.ID, state.Conn, state.ConnGeneration-1)
	require.Never(t, func() bool { return counter.count("tools/list") != stale }, 300*time.Millisecond, 20*time.Millisecond)
}

// A change made between initial discovery and the 2026-07-28 listen stream opening was announced to
// nobody; the stream's acknowledgement re-lists it. That kick must reach the client's checker, so
// the checker is registered before the stream opens.
func TestUpstreamToolsListChanged_AckAfterConnectReachesChecker(t *testing.T) {
	for i := range 10 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			var s *server.MCPServer
			var once sync.Once
			hooks := &server.Hooks{}
			hooks.AddAfterListTools(func(context.Context, any, *mcpgo.ListToolsRequest, *mcpgo.ListToolsResult) {
				once.Do(func() { addMatrixTool(s) }) // after discovery, before anyone listens
			})
			s = newMatrixUpstream(server.WithHooks(hooks))
			ts := httptest.NewServer(server.NewStreamableHTTPServer(s, server.WithStreamableHTTPProtocolVersions(mcpgo.ProtocolVersion20260728)))
			t.Cleanup(ts.Close)

			m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
			t.Cleanup(func() { _ = m.Cleanup() })
			config := &schemas.MCPClientConfig{
				ID: "ack-after-connect", Name: "matrix", ConnectionType: schemas.MCPConnectionTypeHTTP,
				ConnectionString: schemas.NewSecretVar(ts.URL), NeedsSessionStickiness: schemas.Ptr(true), ToolsToExecute: []string{"*"},
			}
			require.NoError(t, m.connectToMCPClient(context.Background(), config))
			require.Eventually(t, func() bool {
				state, ok := snapshotClientState(m, config.ID)
				_, added := state.ToolMap["matrix-added"]
				return ok && added
			}, 3*time.Second, 20*time.Millisecond, "the listen acknowledgement must re-list a change made before the stream opened")
		})
	}
}

// A watcher is bound to the connection it was registered on: once that connection is closed
// (disable, needs_reauth) or the client is removed and added again under the same ID, it stops.
func TestUpstreamToolsListChanged_WatcherEndsWithItsConnection(t *testing.T) {
	upstream := server.NewStreamableHTTPServer(newMatrixUpstream(), server.WithStreamableHTTPProtocolVersions(mcpgo.ProtocolVersion20260728))
	var activeStreams atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(mcpgo.HeaderMethod) == string(mcpgo.MethodSubscriptionsListen) {
			activeStreams.Add(1)
			defer activeStreams.Add(-1)
		}
		upstream.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	callerCtx, cancelCaller := context.WithCancel(context.Background())
	t.Cleanup(cancelCaller)
	m := NewMCPManager(callerCtx, schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	t.Cleanup(func() { _ = m.Cleanup() })
	config := &schemas.MCPClientConfig{
		ID: "watcher-bound", Name: "matrix", ConnectionType: schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar(ts.URL), NeedsSessionStickiness: schemas.Ptr(true), ToolsToExecute: []string{"*"},
	}
	connect := func() (schemas.MCPClientState, bool) {
		t.Helper()
		require.NoError(t, m.connectToMCPClient(context.Background(), config))
		return snapshotClientState(m, config.ID)
	}

	first, ok := connect()
	require.True(t, ok)
	require.True(t, m.isCurrentConn(config.ID, first.Conn, first.ConnGeneration))
	require.Eventually(t, func() bool { return activeStreams.Load() == 1 }, time.Second, 10*time.Millisecond)

	t.Run("disabled", func(t *testing.T) {
		require.NoError(t, m.DisableClient(config.ID))
		assert.False(t, m.isCurrentConn(config.ID, first.Conn, first.ConnGeneration), "a closed connection's watcher must stop")
		require.Eventually(t, func() bool { return activeStreams.Load() == 0 }, time.Second, 10*time.Millisecond, "disable must end the subscription request")
	})

	t.Run("removed and added again", func(t *testing.T) {
		require.NoError(t, m.RemoveClient(config.ID))
		again, ok := connect()
		require.True(t, ok)
		require.Equal(t, first.ConnGeneration, again.ConnGeneration, "a fresh entry reaches the old generation number")
		assert.False(t, m.isCurrentConn(config.ID, first.Conn, first.ConnGeneration), "the old watcher must not pass for the new client")
		assert.True(t, m.isCurrentConn(config.ID, again.Conn, again.ConnGeneration))
		require.Eventually(t, func() bool { return activeStreams.Load() == 1 }, time.Second, 10*time.Millisecond)
	})

	t.Run("replacement owns its own stream", func(t *testing.T) {
		before, ok := snapshotClientState(m, config.ID)
		require.True(t, ok)
		require.NoError(t, m.ReconnectClient(config.ID))
		after, ok := snapshotClientState(m, config.ID)
		require.True(t, ok)
		require.NotSame(t, before.Conn, after.Conn)
		require.Eventually(t, func() bool { return activeStreams.Load() == 1 }, time.Second, 10*time.Millisecond, "the old stream must end without canceling the replacement")
	})

	t.Run("cleanup leaves caller usable", func(t *testing.T) {
		require.NoError(t, m.Cleanup())
		assert.NoError(t, callerCtx.Err(), "cleanup must not cancel a shared caller context")
		require.Eventually(t, func() bool { return activeStreams.Load() == 0 }, time.Second, 10*time.Millisecond, "cleanup must end the subscription request")
		require.ErrorIs(t, m.connectToMCPClient(context.Background(), config), context.Canceled)
		require.ErrorIs(t, m.AddClient(context.Background(), config), context.Canceled)
	})
}

// Cleanup must cancel discovery even before the candidate has been installed in clientMap.
func TestCleanupCancelsPendingMCPConnection(t *testing.T) {
	for _, mode := range []string{"sticky", "per-call", "boot"} {
		t.Run(mode, func(t *testing.T) {
			upstream := server.NewStreamableHTTPServer(newMatrixUpstream(), server.WithStreamableHTTPProtocolVersions(mcpgo.ProtocolVersion20260728))
			listed := make(chan struct{})
			var pendingRequests atomic.Int32
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				r.Body = io.NopCloser(bytes.NewReader(body))
				if bytes.Contains(body, []byte(`"tools/list"`)) {
					pendingRequests.Add(1)
					defer pendingRequests.Add(-1)
					close(listed)
					<-r.Context().Done()
					return
				}
				upstream.ServeHTTP(w, r)
			}))
			t.Cleanup(ts.Close)
			callerCtx, cancelCaller := context.WithCancel(context.Background())
			requestCtx, cancelRequest := context.WithCancel(context.Background())
			config := &schemas.MCPClientConfig{
				ID: "pending-cleanup", Name: "matrix", ConnectionType: schemas.MCPConnectionTypeHTTP, AuthType: schemas.MCPAuthTypeHeaders,
				ConnectionString: schemas.NewSecretVar(ts.URL), NeedsSessionStickiness: schemas.Ptr(mode != "per-call"), ToolsToExecute: []string{"*"},
			}
			m := NewMCPManager(callerCtx, schemas.MCPConfig{ClientConfigs: []*schemas.MCPClientConfig{config}}, nil, &MockLogger{}, nil)
			require.Equal(t, mode == "per-call", m.credStore.RequiresPerCallConnection(config), "fixture must take the intended connection path")
			connected, done := make(chan error, 1), make(chan struct{})
			t.Cleanup(func() {
				cancelRequest()
				cancelCaller()
				<-done
				_ = m.Cleanup()
			})
			go func() {
				defer close(done)
				if mode == "boot" {
					m.ConnectConfiguredClients(requestCtx)
					connected <- nil
					return
				}
				connected <- m.AddClient(requestCtx, config)
			}()
			select {
			case <-listed:
			case <-time.After(5 * time.Second):
				t.Fatal("the candidate never started discovery")
			}
			require.NoError(t, m.Cleanup())
			assert.NoError(t, callerCtx.Err(), "cleanup must not cancel a shared caller context")
			assert.NoError(t, requestCtx.Err(), "cleanup must not cancel the request's context")
			require.Eventually(t, func() bool { return pendingRequests.Load() == 0 }, time.Second, 10*time.Millisecond, "cleanup must cancel an uninstalled candidate")
			select {
			case err := <-connected:
				if mode != "boot" {
					require.Error(t, err, "a canceled candidate cannot become an installed connection")
				}
			case <-time.After(time.Second):
				t.Fatal("the candidate dial did not exit after cleanup")
			}
			_, exists := snapshotClientState(m, config.ID)
			assert.False(t, exists, "a canceled candidate must not restore a client after cleanup")
			assert.Nil(t, registeredChecker(m, config.ID), "a canceled candidate must not restart its checker")
		})
	}
}

// SSE startup has no request deadline, but it must still belong to the manager.
func TestCleanupCancelsPendingSSEConnection(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var pendingRequests atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pendingRequests.Add(1)
		defer pendingRequests.Add(-1)
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(ts.Close)
	callerCtx, cancelCaller := context.WithCancel(context.Background())
	t.Cleanup(cancelCaller)
	m := NewMCPManager(callerCtx, schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	t.Cleanup(func() { _ = m.Cleanup() })
	t.Cleanup(func() { close(release) })
	config := &schemas.MCPClientConfig{
		ID: "pending-sse-cleanup", Name: "matrix", ConnectionType: schemas.MCPConnectionTypeSSE,
		ConnectionString: schemas.NewSecretVar(ts.URL), NeedsSessionStickiness: schemas.Ptr(true), ToolsToExecute: []string{"*"},
	}
	connected := make(chan error, 1)
	go func() { connected <- m.connectToMCPClient(context.Background(), config) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the candidate never started its SSE stream")
	}
	require.NoError(t, m.Cleanup())
	assert.NoError(t, callerCtx.Err(), "cleanup must not cancel a shared caller context")
	require.Eventually(t, func() bool { return pendingRequests.Load() == 0 }, time.Second, 10*time.Millisecond, "cleanup must cancel SSE startup before installation")
	select {
	case err := <-connected:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("the SSE candidate did not exit after cleanup")
	}
}

// A tool change announced while the connect's first tools/list is in flight is not lost: that list
// predates the change, and the notification arrives before the connection is installed.
func TestUpstreamToolsListChanged_ChangeDuringDiscoveryIsNotMissed(t *testing.T) {
	s := newMatrixUpstream()
	upstream := server.NewStreamableHTTPServer(s, server.WithStreamableHTTPProtocolVersions(mcpgo.ProtocolVersion20251125))
	var holdNext atomic.Bool
	holdNext.Store(true)
	listed, release := make(chan struct{}), make(chan struct{})
	var heldList []byte // the held tools/list answer; written before listed closes
	var releaseOnce sync.Once
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		if !bytes.Contains(body, []byte(`"tools/list"`)) || !holdNext.CompareAndSwap(true, false) {
			upstream.ServeHTTP(w, r)
			return
		}
		// Answer from the upstream's tools as they are now, but deliver the answer only once released.
		recorded := httptest.NewRecorder()
		upstream.ServeHTTP(recorded, r)
		heldList = recorded.Body.Bytes()
		close(listed)
		<-release
		for k, v := range recorded.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(recorded.Code)
		_, _ = w.Write(recorded.Body.Bytes())
	}))
	t.Cleanup(ts.Close)
	// After ts.Close, so it runs first: a blocked handler is released before the server waits on it.
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	config := &schemas.MCPClientConfig{
		ID: "list-changed-during-discovery", Name: "matrix", ConnectionType: schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar(ts.URL), NeedsSessionStickiness: schemas.Ptr(true), ToolsToExecute: []string{"*"},
	}
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	t.Cleanup(func() { _ = m.Cleanup() })
	connected := make(chan error, 1)
	go func() { connected <- m.connectToMCPClient(context.Background(), config) }()
	select {
	case <-listed:
	case <-time.After(5 * time.Second):
		t.Fatal("the connect never listed tools")
	}
	// Checked on the held answer, not the installed state: the fix may re-list before connect returns.
	require.NotContains(t, string(heldList), `"added"`, "fixture check: the held list predates the change")
	addMatrixTool(s) // announced on the GET stream while the stale list is held
	time.Sleep(200 * time.Millisecond)
	releaseOnce.Do(func() { close(release) })
	require.NoError(t, <-connected)

	require.Eventually(t, func() bool {
		state, ok := snapshotClientState(m, config.ID)
		_, added := state.ToolMap["matrix-added"]
		return ok && added
	}, 5*time.Second, 50*time.Millisecond, "the change announced during discovery must trigger a re-list")
}
