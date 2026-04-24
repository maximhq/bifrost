package websocket

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	ws "github.com/fasthttp/websocket"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func startTestWSServer(t *testing.T) *httptest.Server {
	t.Helper()
	upgrader := ws.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			mt, msg, err := conn.ReadMessage()
			if err != nil {
				break
			}
			conn.WriteMessage(mt, msg)
		}
	}))
	return server
}

func TestPoolGetAndReturn(t *testing.T) {
	server := startTestWSServer(t)
	defer server.Close()

	config := &schemas.WSPoolConfig{
		MaxIdlePerKey:                5,
		MaxTotalConnections:          10,
		IdleTimeoutSeconds:           300,
		MaxConnectionLifetimeSeconds: 3600,
	}
	pool := NewPool(config)
	defer pool.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	key := PoolKey{Provider: schemas.OpenAI, KeyID: "test-key", Endpoint: wsURL}

	// Get a new connection (pool is empty, should dial)
	conn, err := pool.Get(key, nil)
	require.NoError(t, err)
	require.NotNil(t, conn)
	assert.Equal(t, schemas.OpenAI, conn.Provider())
	assert.Equal(t, "test-key", conn.KeyID())
	assert.False(t, conn.IsClosed())

	// Return to pool
	pool.Return(conn)

	// Get again — should reuse the same connection
	conn2, err := pool.Get(key, nil)
	require.NoError(t, err)
	require.NotNil(t, conn2)
	assert.Same(t, conn, conn2)
	pool.Return(conn2)
}

func TestPoolMaxIdlePerKey(t *testing.T) {
	server := startTestWSServer(t)
	defer server.Close()

	config := &schemas.WSPoolConfig{
		MaxIdlePerKey:                2,
		MaxTotalConnections:          10,
		IdleTimeoutSeconds:           300,
		MaxConnectionLifetimeSeconds: 3600,
	}
	pool := NewPool(config)
	defer pool.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	key := PoolKey{Provider: schemas.OpenAI, KeyID: "test-key", Endpoint: wsURL}

	// Get 3 connections
	var conns []*UpstreamConn
	for range 3 {
		conn, err := pool.Get(key, nil)
		require.NoError(t, err)
		conns = append(conns, conn)
	}

	// Return all 3 — only 2 should be kept (MaxIdlePerKey=2)
	for _, conn := range conns {
		pool.Return(conn)
	}

	pool.mu.Lock()
	idleCount := len(pool.idle[key])
	pool.mu.Unlock()

	assert.Equal(t, 2, idleCount)
}

func TestPoolClose(t *testing.T) {
	server := startTestWSServer(t)
	defer server.Close()

	config := &schemas.WSPoolConfig{}
	config.CheckAndSetDefaults()
	pool := NewPool(config)

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	key := PoolKey{Provider: schemas.OpenAI, KeyID: "test-key", Endpoint: wsURL}

	conn, err := pool.Get(key, nil)
	require.NoError(t, err)
	pool.Return(conn)

	pool.Close()

	// Getting from a closed pool should fail
	_, err = pool.Get(key, nil)
	assert.Error(t, err)
}

// TestPoolGetEvictsStaleSessionConn verifies that Pool.Get detects a
// server-side close via the liveness probe and dials a fresh connection
// instead of handing out the stale one (issue #3002).
func TestPoolGetEvictsStaleSessionConn(t *testing.T) {
	// closeCh is closed by the test to signal the server to close connection #1.
	closeCh := make(chan struct{})
	// dialCh receives a value each time the mock server accepts a new upgrade.
	dialCh := make(chan struct{}, 8)

	upgrader := ws.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		dialCh <- struct{}{}
		// Block until the test signals the server to close, or the client disconnects.
		select {
		case <-closeCh:
			// Send a normal close frame so the TCP socket carries the close before
			// the server's defer conn.Close() runs.
			_ = conn.WriteMessage(ws.CloseMessage,
				ws.FormatCloseMessage(ws.CloseNormalClosure, "done"))
			time.Sleep(10 * time.Millisecond)
		}
	}))
	defer server.Close()

	config := &schemas.WSPoolConfig{
		MaxIdlePerKey:                5,
		MaxTotalConnections:          10,
		IdleTimeoutSeconds:           300,
		MaxConnectionLifetimeSeconds: 3600,
	}
	pool := NewPool(config)
	defer pool.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	key := PoolKey{Provider: schemas.OpenAI, KeyID: "test-key", Endpoint: wsURL}

	// Dial the first connection and confirm it reaches the mock server.
	conn1, err := pool.Get(key, nil)
	require.NoError(t, err)
	require.NotNil(t, conn1)
	<-dialCh // wait until server has accepted connection #1

	// Return it to the idle pool.
	pool.Return(conn1)

	// Signal the server to close the upstream side of connection #1.
	close(closeCh)

	// Give the OS a moment to deliver the close frame into the socket buffer.
	time.Sleep(50 * time.Millisecond)

	// Pool.Get must detect the stale connection via the liveness probe and dial
	// a fresh one.  It should not return conn1.
	conn2, err := pool.Get(key, nil)
	require.NoError(t, err)
	require.NotNil(t, conn2)

	// A fresh dial was triggered — wait for the mock server to record it.
	select {
	case <-dialCh:
		// Good: server accepted a new upstream connection.
	case <-time.After(2 * time.Second):
		t.Fatal("expected a fresh upstream dial after stale-connection eviction, but none arrived")
	}

	// The returned connection must not be the stale one.
	assert.NotSame(t, conn1, conn2, "Pool.Get must not return the stale session-pinned connection")
	assert.True(t, conn1.IsClosed(), "stale connection must have been closed by the pool")

	pool.Discard(conn2)
}

func TestPoolExpiredConnection(t *testing.T) {
	server := startTestWSServer(t)
	defer server.Close()

	config := &schemas.WSPoolConfig{
		MaxIdlePerKey:                5,
		MaxTotalConnections:          10,
		IdleTimeoutSeconds:           1,
		MaxConnectionLifetimeSeconds: 1,
	}
	pool := NewPool(config)
	defer pool.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	key := PoolKey{Provider: schemas.OpenAI, KeyID: "test-key", Endpoint: wsURL}

	conn, err := pool.Get(key, nil)
	require.NoError(t, err)
	pool.Return(conn)

	// Wait for connection to expire
	time.Sleep(1500 * time.Millisecond)

	// Get should dial a new connection (old one expired)
	conn2, err := pool.Get(key, nil)
	require.NoError(t, err)
	require.NotNil(t, conn2)
	assert.NotSame(t, conn, conn2)
	pool.Discard(conn2)
}
