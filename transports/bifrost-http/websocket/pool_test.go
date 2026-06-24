package websocket

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

func TestPoolDialIncludesHandshakeDetails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("invalid websocket token"))
	}))
	defer server.Close()

	config := &schemas.WSPoolConfig{}
	config.CheckAndSetDefaults()
	pool := NewPool(config)
	defer pool.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	key := PoolKey{Provider: schemas.OpenAI, KeyID: "test-key", Endpoint: wsURL}

	_, err := pool.Get(key, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401 Unauthorized")
	assert.Contains(t, err.Error(), "invalid websocket token")
}

func TestDialUpstreamCloseDoesNotAffectPoolCapacityCounters(t *testing.T) {
	server := startTestWSServer(t)
	defer server.Close()

	config := &schemas.WSPoolConfig{
		MaxIdlePerKey:                1,
		MaxTotalConnections:          1,
		IdleTimeoutSeconds:           300,
		MaxConnectionLifetimeSeconds: 3600,
	}
	pool := NewPool(config)
	defer pool.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	unpooled, err := DialUpstream(wsURL, nil, schemas.OpenAI, "test-key")
	require.NoError(t, err)
	require.NotNil(t, unpooled)
	require.NoError(t, unpooled.Close())

	pool.mu.Lock()
	inFlight := pool.inFlight
	pool.mu.Unlock()
	assert.Equal(t, 0, inFlight)

	key := PoolKey{Provider: schemas.OpenAI, KeyID: "test-key", Endpoint: wsURL}
	pooled, err := pool.Get(key, nil)
	require.NoError(t, err)
	require.NotNil(t, pooled)
	pool.Discard(pooled)
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

func TestPoolGetSkipsStaleIdleConnection(t *testing.T) {
	var connectionCount atomic.Int32
	serverClosed := make(chan struct{})
	upgrader := ws.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		connectionCount.Add(1)
		current := connectionCount.Load()
		if current == 1 {
			_ = conn.WriteControl(ws.CloseMessage, ws.FormatCloseMessage(ws.CloseNormalClosure, "done"), time.Now().Add(time.Second))
			_ = conn.Close()
			close(serverClosed)
			return
		}
		defer conn.Close()
		for {
			mt, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			_ = conn.WriteMessage(mt, msg)
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

	conn, err := pool.Get(key, nil)
	require.NoError(t, err)
	pool.Return(conn)

	// Wait for the server to finish sending the close frame before the next
	// borrow attempt so the close is in the client's TCP receive buffer.
	select {
	case <-serverClosed:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not close the first connection in time")
	}

	freshConn, err := pool.Get(key, nil)
	require.NoError(t, err)
	require.NotNil(t, freshConn)
	assert.NotSame(t, conn, freshConn)
	assert.EqualValues(t, 2, connectionCount.Load())
	pool.Discard(freshConn)
}
