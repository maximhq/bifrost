// Package websocket provides upstream WebSocket connection management for the Bifrost gateway.
// It manages pooled connections to provider WebSocket APIs (e.g., OpenAI Responses WS mode,
// Realtime API) and client session bindings.
package websocket

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	ws "github.com/fasthttp/websocket"
	"github.com/maximhq/bifrost/core/schemas"
)

// UpstreamConn wraps a WebSocket connection to an upstream provider.
// Thread-safe for concurrent read/write via separate mutexes.
type UpstreamConn struct {
	conn      *ws.Conn
	provider  schemas.ModelProvider
	keyID     string
	endpoint  string
	createdAt time.Time
	lastUsed  atomic.Int64 // unix nano

	writeMu sync.Mutex
	readMu  sync.Mutex

	closed     atomic.Bool
	validating atomic.Bool // guards concurrent ValidateIdleHeartbeat calls
}

// newUpstreamConn creates a new UpstreamConn wrapping the given websocket connection.
func newUpstreamConn(conn *ws.Conn, provider schemas.ModelProvider, keyID, endpoint string) *UpstreamConn {
	uc := &UpstreamConn{
		conn:      conn,
		provider:  provider,
		keyID:     keyID,
		endpoint:  endpoint,
		createdAt: time.Now(),
	}
	uc.lastUsed.Store(time.Now().UnixNano())
	return uc
}

// WriteMessage sends a message to the upstream provider. Thread-safe.
// Closes the connection immediately on fatal write errors so resources are
// released deterministically rather than waiting for the caller to call Close.
func (c *UpstreamConn) WriteMessage(messageType int, data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.lastUsed.Store(time.Now().UnixNano())
	writeErr := c.conn.WriteMessage(messageType, data)
	if writeErr != nil && isConnectionDead(writeErr) {
		_ = c.Close()
	}
	return writeErr
}

// WriteJSON sends a JSON-encoded message to the upstream provider. Thread-safe.
func (c *UpstreamConn) WriteJSON(v interface{}) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.lastUsed.Store(time.Now().UnixNano())
	return c.conn.WriteJSON(v)
}

// ReadMessage reads a message from the upstream provider. Thread-safe.
// Closes the connection immediately on fatal read errors so resources are
// released deterministically rather than waiting for the caller to call Close.
func (c *UpstreamConn) ReadMessage() (messageType int, p []byte, err error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	c.lastUsed.Store(time.Now().UnixNano())
	msgType, data, readErr := c.conn.ReadMessage()
	if readErr != nil && isConnectionDead(readErr) {
		_ = c.Close()
	}
	return msgType, data, readErr
}

// Close closes the underlying WebSocket connection.
func (c *UpstreamConn) Close() error {
	if c.closed.CompareAndSwap(false, true) {
		return c.conn.Close()
	}
	return nil
}

// IsClosed returns whether the connection has been closed.
func (c *UpstreamConn) IsClosed() bool {
	return c.closed.Load()
}

// Provider returns the provider this connection is for.
func (c *UpstreamConn) Provider() schemas.ModelProvider {
	return c.provider
}

// KeyID returns the API key ID used for this connection.
func (c *UpstreamConn) KeyID() string {
	return c.keyID
}

// CreatedAt returns when this connection was established.
func (c *UpstreamConn) CreatedAt() time.Time {
	return c.createdAt
}

// LastUsed returns the last time this connection was used.
func (c *UpstreamConn) LastUsed() time.Time {
	return time.Unix(0, c.lastUsed.Load())
}

// Age returns how long this connection has been alive.
func (c *UpstreamConn) Age() time.Duration {
	return time.Since(c.createdAt)
}

// SetReadDeadline sets the read deadline on the underlying connection.
func (c *UpstreamConn) SetReadDeadline(t time.Time) error {
	return c.conn.SetReadDeadline(t)
}

// SetWriteDeadline sets the write deadline on the underlying connection.
func (c *UpstreamConn) SetWriteDeadline(t time.Time) error {
	return c.conn.SetWriteDeadline(t)
}

// SetPongHandler sets a handler for pong messages received from the upstream.
func (c *UpstreamConn) SetPongHandler(h func(appData string) error) {
	c.conn.SetPongHandler(h)
}

// WritePing sends a ping control frame to the upstream with an explicit deadline.
// Uses WriteControl instead of WriteMessage so the deadline is actually enforced
// (WriteMessage ignores SetWriteDeadline in fasthttp/websocket).
func (c *UpstreamConn) WritePing(data []byte, deadline time.Time) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.lastUsed.Store(time.Now().UnixNano())
	return c.conn.WriteControl(ws.PingMessage, data, deadline)
}

// ValidateIdleHeartbeat probes an idle connection with a ping/pong round trip.
// It returns true only when the connection stays usable for reuse.
// The probe intentionally does NOT update lastUsed so that the pool's idle
// timer reflects real application activity, not background health checks.
//
// Instead of blocking for the full timeout on healthy connections, it polls with
// short read deadlines and returns as soon as the pong handler fires. This keeps
// borrow-path latency low and avoids stalling the heartbeat sweep.
func (c *UpstreamConn) ValidateIdleHeartbeat(timeout time.Duration) bool {
	if c == nil || c.IsClosed() {
		return false
	}
	// Prevent concurrent validation from heartbeat loop and borrow path.
	if !c.validating.CompareAndSwap(false, true) {
		return false // another goroutine is already probing this conn
	}
	defer c.validating.Store(false)

	// Preserve the pre-probe idle timestamp so that heartbeat probes do not
	// artificially extend the connection's idle lifetime.
	savedLastUsed := c.lastUsed.Load()
	defer c.lastUsed.Store(savedLastUsed)

	var gotPong atomic.Bool
	c.SetPongHandler(func(string) error {
		gotPong.Store(true)
		return nil
	})
	defer c.SetPongHandler(func(string) error { return nil })

	if err := c.WritePing(nil, time.Now().Add(timeout)); err != nil {
		_ = c.Close()
		return false
	}

	// Poll with short read deadlines so we return as soon as the pong handler
	// fires, rather than blocking for the full timeout on healthy connections.
	// In fasthttp/websocket, pong frames are consumed internally by ReadMessage
	// (the handler fires, then ReadMessage continues waiting for data frames),
	// so we use short iterations to check gotPong between reads.
	const pollInterval = 10 * time.Millisecond
	deadline := time.Now().Add(timeout)

	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		d := pollInterval
		if remaining < d {
			d = remaining
		}

		_ = c.SetReadDeadline(time.Now().Add(d))
		_, _, err := c.ReadMessage()

		if gotPong.Load() {
			_ = c.SetReadDeadline(time.Time{})
			return true
		}

		if err != nil {
			if isHeartbeatTimeout(err) {
				continue // poll again until total deadline
			}
			// Non-timeout error (close frame, EOF, etc.)
			_ = c.Close()
			return false
		}

		// Idle pooled connections should not have buffered application frames.
		_ = c.Close()
		return false
	}

	// Total timeout expired without receiving pong.
	_ = c.Close()
	return false
}

// isHeartbeatTimeout reports whether err is a net.Error timeout, which indicates
// the read deadline fired before any data arrived (expected during ping/pong probes).
func isHeartbeatTimeout(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// isConnectionDead returns true for errors that indicate the connection is permanently broken
// (close frames, EOF, broken pipe) so callers can mark IsClosed() early.
func isConnectionDead(err error) bool {
	if err == nil {
		return false
	}
	if ws.IsCloseError(err, ws.CloseNormalClosure, ws.CloseGoingAway,
		ws.CloseAbnormalClosure, ws.CloseNoStatusReceived) {
		return true
	}
	if errors.Is(err, net.ErrClosed) {
		return true
	}
	msg := err.Error()
	return msg == "EOF" || msg == "unexpected EOF"
}

// DialUpstream creates a new upstream connection without adding it to the pool.
func DialUpstream(url string, headers http.Header, provider schemas.ModelProvider, keyID string) (*UpstreamConn, error) {
	wsConn, resp, err := Dial(url, headers)
	if err != nil {
		return nil, fmt.Errorf("failed to dial upstream websocket %s: %w", url, wrapHandshakeError(resp, err))
	}
	return newUpstreamConn(wsConn, provider, keyID, url), nil
}

// Dial creates a new WebSocket connection to the given URL with the provided headers.
func Dial(url string, headers http.Header) (*ws.Conn, *http.Response, error) {
	dialer := ws.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}
	return dialer.Dial(url, headers)
}
