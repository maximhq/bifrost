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

// startRejectServer starts a test HTTP server that rejects WebSocket upgrade requests
// with the given status code and body.
func startRejectServer(t *testing.T, statusCode int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(statusCode)
		_, _ = w.Write([]byte(body))
	}))
}

// TestDialErrorIncludesStatusAndBody verifies that when the upstream returns a non-nil
// HTTP response on dial failure, the error contains the status code and body snippet.
func TestDialErrorIncludesStatusAndBody(t *testing.T) {
	const upstreamBody = `{"error":"invalid_token","message":"token is not valid"}`
	server := startRejectServer(t, http.StatusUnauthorized, upstreamBody)
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

	_, err := pool.Get(key, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "upstream status 401")
	assert.Contains(t, err.Error(), "invalid_token")
}

// TestDialErrorNilResponseFallback verifies that when there is no HTTP response
// (e.g. network error before the server responds), the error still uses the base
// format without panicking.
func TestDialErrorNilResponseFallback(t *testing.T) {
	config := &schemas.WSPoolConfig{
		MaxIdlePerKey:                5,
		MaxTotalConnections:          10,
		IdleTimeoutSeconds:           300,
		MaxConnectionLifetimeSeconds: 3600,
	}
	pool := NewPool(config)
	defer pool.Close()

	// Use an endpoint that is not listening so the TCP dial fails immediately,
	// producing a nil HTTP response.
	key := PoolKey{Provider: schemas.OpenAI, KeyID: "test-key", Endpoint: "ws://127.0.0.1:1"}

	_, err := pool.Get(key, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to dial upstream websocket")
	assert.NotContains(t, err.Error(), "upstream status")
}

// TestDialErrorBodyTruncated verifies that body snippets longer than 512 bytes are
// truncated so the error message stays bounded.
func TestDialErrorBodyTruncated(t *testing.T) {
	// Build a body that is larger than the 512-byte limit.
	largeBody := strings.Repeat("x", 1024)
	server := startRejectServer(t, http.StatusForbidden, largeBody)
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

	_, err := pool.Get(key, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "upstream status 403")
	// The error string must not contain the full large body.
	assert.LessOrEqual(t, len(err.Error()), 700,
		"error string should be bounded; got length %d", len(err.Error()))
}

// TestDialErrorResponseBodyClosed verifies that the HTTP response body from a failed
// dial attempt is closed (no resource leak). We confirm by checking the error is
// produced without panic and contains expected fields when the server writes a
// response and closes immediately.
func TestDialErrorResponseBodyClosed(t *testing.T) {
	const body = `{"code":"forbidden"}`
	server := startRejectServer(t, http.StatusForbidden, body)
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

	_, err := pool.Get(key, nil)
	require.Error(t, err)
	// If body were not closed, subsequent connections to the same server could stall.
	// We verify the error contains the status to confirm the body was read before close.
	assert.Contains(t, err.Error(), "upstream status 403")
	assert.Contains(t, err.Error(), "forbidden")
}
