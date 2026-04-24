package websocket

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// PoolKey uniquely identifies a group of upstream connections.
type PoolKey struct {
	Provider schemas.ModelProvider
	KeyID    string
	Endpoint string
}

// Pool manages a pool of upstream WebSocket connections keyed by (provider, keyID, endpoint).
// Idle connections are cached for reuse. Connections exceeding max lifetime are discarded.
type Pool struct {
	mu       sync.Mutex
	idle     map[PoolKey][]*UpstreamConn
	inFlight int

	config *schemas.WSPoolConfig

	closed bool
	done   chan struct{}
}

// NewPool creates a new upstream WebSocket connection pool.
func NewPool(config *schemas.WSPoolConfig) *Pool {
	if config == nil {
		config = &schemas.WSPoolConfig{}
	}
	config.CheckAndSetDefaults()
	p := &Pool{
		idle:   make(map[PoolKey][]*UpstreamConn),
		config: config,
		done:   make(chan struct{}),
	}
	go p.evictLoop()
	return p
}

// Get retrieves an idle connection for the given key, or dials a new one.
// The returned connection is removed from the idle pool and must be returned
// via Return or discarded via Discard.
func (p *Pool) Get(key PoolKey, headers map[string]string) (*UpstreamConn, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, fmt.Errorf("pool is closed")
	}

	conns := p.idle[key]
	for len(conns) > 0 {
		// Pop from the back (most recently returned)
		conn := conns[len(conns)-1]
		conns = conns[:len(conns)-1]
		p.idle[key] = conns

		p.mu.Unlock()

		if conn.IsClosed() || p.isExpired(conn) {
			conn.Close()
			p.mu.Lock()
			conns = p.idle[key]
			continue
		}

		// Liveness probe: attempt a non-blocking read with a 1ms deadline.
		// An idle connection should produce no frames while sitting in the pool.
		// If the upstream already sent a close frame, ReadMessage returns
		// immediately with a close/EOF error, revealing the stale state before
		// the caller uses the connection.  A timeout error means the connection
		// is alive and has nothing queued.
		if !p.isLive(conn) {
			conn.Close()
			p.mu.Lock()
			conns = p.idle[key]
			continue
		}

		p.mu.Lock()
		p.inFlight++
		p.mu.Unlock()
		return conn, nil
	}

	// Check total capacity (idle + in-flight) before dialing
	totalIdle := 0
	for _, c := range p.idle {
		totalIdle += len(c)
	}
	if totalIdle+p.inFlight >= p.config.MaxTotalConnections {
		p.mu.Unlock()
		return nil, fmt.Errorf("pool capacity exhausted: %d idle + %d in-flight >= %d max", totalIdle, p.inFlight, p.config.MaxTotalConnections)
	}

	// Reserve a slot before unlocking to dial
	p.inFlight++
	p.mu.Unlock()

	conn, err := p.dial(key, headers)
	if err != nil {
		p.mu.Lock()
		p.inFlight--
		p.mu.Unlock()
		return nil, err
	}
	return conn, nil
}

// Return puts a connection back into the idle pool for reuse.
// If the connection is expired or the pool is full, it is closed instead.
func (p *Pool) Return(conn *UpstreamConn) {
	if conn == nil || conn.IsClosed() {
		return
	}
	if p.isExpired(conn) {
		conn.Close()
		p.mu.Lock()
		p.inFlight--
		p.mu.Unlock()
		return
	}

	key := PoolKey{
		Provider: conn.provider,
		KeyID:    conn.keyID,
		Endpoint: conn.endpoint,
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	p.inFlight--

	if p.closed {
		conn.Close()
		return
	}

	conns := p.idle[key]
	if len(conns) >= p.config.MaxIdlePerKey {
		conn.Close()
		return
	}

	p.idle[key] = append(conns, conn)
}

// Discard closes a connection without returning it to the pool.
func (p *Pool) Discard(conn *UpstreamConn) {
	if conn != nil {
		conn.Close()
		p.mu.Lock()
		p.inFlight--
		p.mu.Unlock()
	}
}

// Close shuts down the pool and closes all idle connections.
func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return
	}
	p.closed = true
	close(p.done)

	for key, conns := range p.idle {
		for _, conn := range conns {
			conn.Close()
		}
		delete(p.idle, key)
	}
}

func (p *Pool) dial(key PoolKey, headers map[string]string) (*UpstreamConn, error) {
	wsConn, _, err := Dial(key.Endpoint, headers)
	if err != nil {
		return nil, fmt.Errorf("failed to dial upstream websocket %s: %w", key.Endpoint, err)
	}
	return newUpstreamConn(wsConn, key.Provider, key.KeyID, key.Endpoint), nil
}

func (p *Pool) isExpired(conn *UpstreamConn) bool {
	maxLifetime := time.Duration(p.config.MaxConnectionLifetimeSeconds) * time.Second
	if conn.Age() >= maxLifetime {
		return true
	}
	idleTimeout := time.Duration(p.config.IdleTimeoutSeconds) * time.Second
	return time.Since(conn.LastUsed()) >= idleTimeout
}

// evictLoop periodically removes expired idle connections.
func (p *Pool) evictLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-p.done:
			return
		case <-ticker.C:
			p.evictExpired()
		}
	}
}

// isLive performs a network-level liveness probe on conn.
// It sets a 1ms read deadline and attempts a ReadMessage.  A net.Error with
// Timeout() == true means the connection is alive with nothing queued, which
// is the expected state for an idle pooled connection.  Any other outcome
// (close frame, EOF, protocol error) means the connection is stale.
// The read deadline is always reset to zero before the function returns so
// that a live connection can be used without deadline constraints.
func (p *Pool) isLive(conn *UpstreamConn) bool {
	if err := conn.SetReadDeadline(time.Now().Add(time.Millisecond)); err != nil {
		return false
	}
	_, _, err := conn.ReadMessage()
	// Always clear the deadline regardless of outcome.
	_ = conn.SetReadDeadline(time.Time{})
	if err == nil {
		// Received a frame while idle — unexpected; treat as stale because
		// we cannot safely discard the frame content here.
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		// Deadline fired with no data: connection is alive.
		return true
	}
	// Close frame, EOF, or other error: connection is stale.
	return false
}

func (p *Pool) evictExpired() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for key, conns := range p.idle {
		alive := conns[:0]
		for _, conn := range conns {
			if conn.IsClosed() || p.isExpired(conn) {
				conn.Close()
			} else {
				alive = append(alive, conn)
			}
		}
		if len(alive) == 0 {
			delete(p.idle, key)
		} else {
			p.idle[key] = alive
		}
	}
}
