package websocket

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

const (
	idleHeartbeatTimeout = 1 * time.Second
	borrowProbeTimeout   = 250 * time.Millisecond
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
	mu            sync.Mutex
	idle          map[PoolKey][]*UpstreamConn
	inFlight      int
	probing       int             // connections temporarily removed from idle for heartbeat validation
	probingPerKey map[PoolKey]int // per-key probing count for MaxIdlePerKey enforcement

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
		idle:          make(map[PoolKey][]*UpstreamConn),
		probingPerKey: make(map[PoolKey]int),
		config:        config,
		done:          make(chan struct{}),
	}
	go p.evictLoop()
	go p.heartbeatLoop()
	return p
}

// Get retrieves an idle connection for the given key, or dials a new one.
// The returned connection is removed from the idle pool and must be returned
// via Return or discarded via Discard.
func (p *Pool) Get(key PoolKey, headers http.Header) (*UpstreamConn, error) {
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

		if conn.IsClosed() || p.isExpired(conn) || !conn.ValidateIdleHeartbeat(borrowProbeTimeout) {
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
	if totalIdle+p.inFlight+p.probing >= p.config.MaxTotalConnections {
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
	if len(conns)+p.probingPerKey[key] >= p.config.MaxIdlePerKey {
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

// dial establishes a new WebSocket connection to the upstream endpoint
// identified by key, forwarding the supplied HTTP headers during the handshake.
func (p *Pool) dial(key PoolKey, headers http.Header) (*UpstreamConn, error) {
	wsConn, resp, err := Dial(key.Endpoint, headers)
	if err != nil {
		return nil, fmt.Errorf("failed to dial upstream websocket %s: %w", key.Endpoint, wrapHandshakeError(resp, err))
	}
	return newUpstreamConn(wsConn, key.Provider, key.KeyID, key.Endpoint), nil
}

// wrapHandshakeError enriches a dial error with the HTTP status and a body
// snippet from the handshake response so callers get actionable diagnostics.
func wrapHandshakeError(resp *http.Response, err error) error {
	if resp == nil {
		return err
	}
	defer resp.Body.Close()

	status := strings.TrimSpace(resp.Status)
	if status == "" {
		status = fmt.Sprintf("status %d", resp.StatusCode)
	}

	bodySnippet := readHandshakeBodySnippet(resp.Body)
	if bodySnippet == "" {
		return fmt.Errorf("upstream handshake failed with %s: %w", status, err)
	}
	return fmt.Errorf("upstream handshake failed with %s: %s: %w", status, bodySnippet, err)
}

// readHandshakeBodySnippet reads up to 512 bytes from body and returns the
// trimmed result. It is used to attach a short error excerpt to failed
// WebSocket handshake diagnostics.
func readHandshakeBodySnippet(body io.Reader) string {
	if body == nil {
		return ""
	}

	const maxSnippetBytes = 512

	limited := io.LimitReader(body, maxSnippetBytes)
	snippet, err := io.ReadAll(limited)
	if err != nil {
		return ""
	}

	trimmed := strings.TrimSpace(string(snippet))
	if trimmed == "" {
		return ""
	}
	return trimmed
}

// isExpired reports whether conn has exceeded the pool's maximum connection
// lifetime or has been idle longer than the configured idle timeout.
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

// heartbeatLoop periodically probes all idle connections with ping/pong
// round trips, removing any that fail to respond.
func (p *Pool) heartbeatLoop() {
	ticker := time.NewTicker(p.heartbeatInterval())
	defer ticker.Stop()

	for {
		select {
		case <-p.done:
			return
		case <-ticker.C:
			p.refreshIdleConnections()
		}
	}
}

// heartbeatInterval returns the delay between heartbeat sweeps, derived as
// one-third of the idle timeout and clamped to [5s, 30s].
func (p *Pool) heartbeatInterval() time.Duration {
	idleTimeout := time.Duration(p.config.IdleTimeoutSeconds) * time.Second
	if idleTimeout <= 0 {
		idleTimeout = time.Duration(schemas.DefaultWSIdleTimeoutSeconds) * time.Second
	}
	interval := idleTimeout / 3
	if interval > 30*time.Second {
		interval = 30 * time.Second
	}
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	return interval
}

// refreshIdleConnections atomically removes all idle connections from the pool,
// probes each one outside the lock via ValidateIdleHeartbeat, and re-inserts
// survivors. Connections are tracked via the probing counter while removed so
// that capacity checks in Get and Return remain accurate.
func (p *Pool) refreshIdleConnections() {
	type idleConn struct {
		key  PoolKey
		conn *UpstreamConn
	}

	// Remove all idle connections from the pool under the lock so that a
	// concurrent Get() cannot check out a connection that the sweeper is
	// about to probe. This avoids probing in-flight sockets.
	// Track them via the probing counter so capacity checks remain accurate.
	p.mu.Lock()
	var candidates []idleConn
	for key, conns := range p.idle {
		count := len(conns)
		for _, conn := range conns {
			candidates = append(candidates, idleConn{key: key, conn: conn})
		}
		p.probing += count
		p.probingPerKey[key] += count
		delete(p.idle, key)
	}
	p.mu.Unlock()

	if len(candidates) == 0 {
		return
	}

	// Probe each candidate outside the lock. Collect survivors vs dead.
	var alive []idleConn
	for _, candidate := range candidates {
		conn := candidate.conn
		if conn == nil || conn.IsClosed() || p.isExpired(conn) || !conn.ValidateIdleHeartbeat(idleHeartbeatTimeout) {
			if conn != nil {
				conn.Close()
			}
			continue
		}
		alive = append(alive, candidate)
	}

	// Clear the probing counters and re-insert survivors under the lock.
	// This block must always run — even when len(alive) == 0 — otherwise
	// the probing counters leak and permanently inflate the capacity check
	// in Get(), eventually causing spurious "capacity exhausted" errors.
	p.mu.Lock()
	defer p.mu.Unlock()
	p.probing -= len(candidates)
	for _, candidate := range candidates {
		p.probingPerKey[candidate.key]--
		if p.probingPerKey[candidate.key] <= 0 {
			delete(p.probingPerKey, candidate.key)
		}
	}
	if p.closed || len(alive) == 0 {
		for _, candidate := range alive {
			candidate.conn.Close()
		}
		return
	}
	for _, candidate := range alive {
		conns := p.idle[candidate.key]
		p.idle[candidate.key] = append(conns, candidate.conn)
	}
}

// evictExpired removes and closes all idle connections that have exceeded the
// maximum lifetime or idle timeout.
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
