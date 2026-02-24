// Package websocket provides upstream WebSocket connection management for the Bifrost gateway.
// It manages pooled connections to provider WebSocket APIs (e.g., OpenAI Responses WS mode,
// Realtime API) and client session bindings.
package websocket

import (
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

	closed atomic.Bool
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
func (c *UpstreamConn) WriteMessage(messageType int, data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.lastUsed.Store(time.Now().UnixNano())
	return c.conn.WriteMessage(messageType, data)
}

// WriteJSON sends a JSON-encoded message to the upstream provider. Thread-safe.
func (c *UpstreamConn) WriteJSON(v interface{}) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.lastUsed.Store(time.Now().UnixNano())
	return c.conn.WriteJSON(v)
}

// ReadMessage reads a message from the upstream provider. Thread-safe.
func (c *UpstreamConn) ReadMessage() (messageType int, p []byte, err error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	c.lastUsed.Store(time.Now().UnixNano())
	return c.conn.ReadMessage()
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

// WritePing sends a ping message to the upstream. Thread-safe.
func (c *UpstreamConn) WritePing(data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.lastUsed.Store(time.Now().UnixNano())
	return c.conn.WriteMessage(ws.PingMessage, data)
}

// Dial creates a new WebSocket connection to the given URL with the provided headers.
func Dial(url string, headers map[string]string) (*ws.Conn, *http.Response, error) {
	dialer := ws.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}
	h := http.Header{}
	for k, v := range headers {
		h.Set(k, v)
	}
	return dialer.Dial(url, h)
}
