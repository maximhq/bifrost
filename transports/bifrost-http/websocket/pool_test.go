package websocket

import (
	"io"
	"net"
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

// startTestHTTPProxy starts a minimal CONNECT-based forward proxy. Used to prove that a
// configured HTTP proxy actually sits in the WebSocket dial path (via the CONNECT tunnel),
// not just that a Dialer.Proxy func was set.
func startTestHTTPProxy(t *testing.T) (proxyURL string, connectCount *atomic.Int32) {
	t.Helper()
	var count atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "only CONNECT supported", http.StatusMethodNotAllowed)
			return
		}
		count.Add(1)

		destConn, err := net.Dial("tcp", r.Host)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer destConn.Close()

		hijacker, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "hijacking not supported", http.StatusInternalServerError)
			return
		}
		clientConn, _, err := hijacker.Hijack()
		if err != nil {
			return
		}
		defer clientConn.Close()

		if _, err := clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
			return
		}

		go io.Copy(destConn, clientConn) //nolint:errcheck
		io.Copy(clientConn, destConn)    //nolint:errcheck
	}))
	t.Cleanup(proxy.Close)
	return proxy.URL, &count
}

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
	conn, err := pool.Get(key, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, conn)
	assert.Equal(t, schemas.OpenAI, conn.Provider())
	assert.Equal(t, "test-key", conn.KeyID())
	assert.False(t, conn.IsClosed())

	// Return to pool
	pool.Return(conn)

	// Get again — should reuse the same connection
	conn2, err := pool.Get(key, nil, nil)
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
		conn, err := pool.Get(key, nil, nil)
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

	conn, err := pool.Get(key, nil, nil)
	require.NoError(t, err)
	pool.Return(conn)

	pool.Close()

	// Getting from a closed pool should fail
	_, err = pool.Get(key, nil, nil)
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

	_, err := pool.Get(key, nil, nil)
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
	unpooled, err := DialUpstream(wsURL, nil, schemas.OpenAI, "test-key", nil)
	require.NoError(t, err)
	require.NotNil(t, unpooled)
	require.NoError(t, unpooled.Close())

	pool.mu.Lock()
	inFlight := pool.inFlight
	pool.mu.Unlock()
	assert.Equal(t, 0, inFlight)

	key := PoolKey{Provider: schemas.OpenAI, KeyID: "test-key", Endpoint: wsURL}
	pooled, err := pool.Get(key, nil, nil)
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

	conn, err := pool.Get(key, nil, nil)
	require.NoError(t, err)
	pool.Return(conn)

	// Wait for connection to expire
	time.Sleep(1500 * time.Millisecond)

	// Get should dial a new connection (old one expired)
	conn2, err := pool.Get(key, nil, nil)
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

	conn, err := pool.Get(key, nil, nil)
	require.NoError(t, err)
	pool.Return(conn)

	// Wait for the server to finish sending the close frame before the next
	// borrow attempt so the close is in the client's TCP receive buffer.
	select {
	case <-serverClosed:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not close the first connection in time")
	}

	freshConn, err := pool.Get(key, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, freshConn)
	assert.NotSame(t, conn, freshConn)
	assert.EqualValues(t, 2, connectionCount.Load())
	pool.Discard(freshConn)
}

// TestPoolGetDialsThroughConfiguredHTTPProxy proves the WebSocket proxy fix actually works
// end-to-end: the dial goes through the configured HTTP CONNECT proxy (not directly to the
// origin), and application data still round-trips correctly over the tunneled connection.
func TestPoolGetDialsThroughConfiguredHTTPProxy(t *testing.T) {
	server := startTestWSServer(t)
	defer server.Close()

	proxyURL, connectCount := startTestHTTPProxy(t)

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
	proxyConfig := &schemas.ProxyConfig{
		Type: schemas.HTTPProxy,
		URL:  schemas.NewSecretVar(proxyURL),
	}

	conn, err := pool.Get(key, nil, proxyConfig)
	require.NoError(t, err)
	require.NotNil(t, conn)
	assert.EqualValues(t, 1, connectCount.Load(), "expected the WebSocket dial to route through the configured HTTP proxy via CONNECT")

	require.NoError(t, conn.WriteMessage(ws.TextMessage, []byte("ping")))
	_, msg, err := conn.ReadMessage()
	require.NoError(t, err)
	assert.Equal(t, "ping", string(msg))

	pool.Discard(conn)
}

// TestPoolGetFailsWithUnreachableProxy is the regression guard: a broken proxy must fail
// the dial, not silently fall back to a direct connection.
func TestPoolGetFailsWithUnreachableProxy(t *testing.T) {
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
	proxyConfig := &schemas.ProxyConfig{
		Type: schemas.HTTPProxy,
		URL:  schemas.NewSecretVar("http://127.0.0.1:1"), // reserved, always-unreachable port
	}

	_, err := pool.Get(key, nil, proxyConfig)
	require.Error(t, err)
}
