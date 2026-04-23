package websocket

import (
	"errors"
	"net"
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

// ---------------------------------------------------------------------------
// Idle-timeout / SetReadDeadline behaviour tests.
// These tests exercise UpstreamConn.SetReadDeadline directly so that the
// tryNativeWSUpstream idle-timeout logic (which calls SetReadDeadline before
// each ReadMessage) is covered at the lowest possible level.
// ---------------------------------------------------------------------------

// TestUpstreamConnReadDeadline_Timeout verifies that a read that is given a
// very short deadline fails with a timeout error when the server never sends.
func TestUpstreamConnReadDeadline_Timeout(t *testing.T) {
	// Server that upgrades but never writes any frames (simulates a stalling
	// upstream: e.g. silent rate-limit hold after accepting the WS upgrade).
	upgrader := ws.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		// Intentionally block forever — simulates upstream stall.
		select {}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	wsConn, _, err := Dial(wsURL, nil)
	require.NoError(t, err)
	uc := newUpstreamConn(wsConn, schemas.OpenAI, "k1", wsURL)
	defer uc.Close()

	const shortDeadline = 100 * time.Millisecond
	start := time.Now()
	require.NoError(t, uc.SetReadDeadline(time.Now().Add(shortDeadline)))
	_, _, readErr := uc.ReadMessage()
	elapsed := time.Since(start)

	require.Error(t, readErr, "expected read to fail with timeout")

	var netErr net.Error
	require.True(t, errors.As(readErr, &netErr) && netErr.Timeout(),
		"expected a net.Error with Timeout()=true, got: %v", readErr)

	// Elapsed time should be close to the deadline, not many seconds.
	assert.Less(t, elapsed, shortDeadline+500*time.Millisecond,
		"read should have timed out quickly")
}

// TestUpstreamConnReadDeadline_PeriodicFramesNoTimeout verifies that an
// upstream that sends a frame every shortInterval does not trigger a timeout
// when the deadline is longer than the interval.  Each successful read clears
// the deadline (as tryNativeWSUpstream does) so the stream stays alive.
func TestUpstreamConnReadDeadline_PeriodicFramesNoTimeout(t *testing.T) {
	const frameInterval = 80 * time.Millisecond
	const idleTimeout = 300 * time.Millisecond
	const numFrames = 4

	upgrader := ws.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for range numFrames {
			time.Sleep(frameInterval)
			if werr := conn.WriteMessage(ws.TextMessage, []byte(`{"type":"ping"}`)); werr != nil {
				return
			}
		}
		// After sending all frames, close normally.
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	wsConn, _, err := Dial(wsURL, nil)
	require.NoError(t, err)
	uc := newUpstreamConn(wsConn, schemas.OpenAI, "k1", wsURL)
	defer uc.Close()

	received := 0
	for {
		// Replicate the per-read deadline pattern from tryNativeWSUpstream.
		require.NoError(t, uc.SetReadDeadline(time.Now().Add(idleTimeout)))
		_, data, readErr := uc.ReadMessage()
		if readErr != nil {
			// Server closed cleanly after numFrames — not a timeout.
			var netErr net.Error
			if errors.As(readErr, &netErr) && netErr.Timeout() {
				t.Fatalf("unexpected timeout after %d frames (interval %v < deadline %v)", received, frameInterval, idleTimeout)
			}
			break
		}
		// Clear deadline after successful read (mirrors tryNativeWSUpstream).
		_ = uc.SetReadDeadline(time.Time{})
		_ = data
		received++
	}
	assert.Equal(t, numFrames, received, "expected to receive all frames without timeout")
}

// TestUpstreamConnReadDeadline_OneThenSilent verifies that a timeout fires
// after idleness FOLLOWING the first frame, not at request-start + timeout.
// The server sends one frame immediately and then goes silent.
func TestUpstreamConnReadDeadline_OneThenSilent(t *testing.T) {
	const idleTimeout = 150 * time.Millisecond

	upgrader := ws.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		// Send exactly one frame, then stall.
		conn.WriteMessage(ws.TextMessage, []byte(`{"type":"response.created"}`)) //nolint:errcheck
		// Block indefinitely — simulates upstream stall after initial frame.
		select {}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	wsConn, _, err := Dial(wsURL, nil)
	require.NoError(t, err)
	uc := newUpstreamConn(wsConn, schemas.OpenAI, "k1", wsURL)
	defer uc.Close()

	// First read: should succeed within idleTimeout.
	require.NoError(t, uc.SetReadDeadline(time.Now().Add(idleTimeout)))
	_, _, firstErr := uc.ReadMessage()
	require.NoError(t, firstErr, "first read (one frame sent) should succeed")
	// Clear deadline — mirrors tryNativeWSUpstream on a successful read.
	_ = uc.SetReadDeadline(time.Time{})

	// Second read: server is now silent. Set a new idle deadline.
	start := time.Now()
	require.NoError(t, uc.SetReadDeadline(time.Now().Add(idleTimeout)))
	_, _, secondErr := uc.ReadMessage()
	elapsed := time.Since(start)

	require.Error(t, secondErr, "second read should fail (upstream stalled)")
	var netErr net.Error
	require.True(t, errors.As(secondErr, &netErr) && netErr.Timeout(),
		"expected timeout error on second read, got: %v", secondErr)

	// The timeout should have fired approximately idleTimeout after the SECOND
	// read attempt, not at request-start + idleTimeout.  We verify it did NOT
	// fire instantly (i.e. the first read succeeded and reset the clock).
	assert.GreaterOrEqual(t, elapsed, idleTimeout/2,
		"timeout should not fire before the idle deadline expires")
	assert.Less(t, elapsed, idleTimeout+500*time.Millisecond,
		"timeout should fire close to idleTimeout after stall begins")
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
