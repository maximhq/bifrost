package websocket

import (
	"sync"

	ws "github.com/fasthttp/websocket"
)

// Session tracks the binding between a client WebSocket connection and its upstream state.
// For Responses WS mode, it tracks previous_response_id → upstream connection pinning.
type Session struct {
	mu sync.RWMutex

	// Client connection
	clientConn *ws.Conn

	// Upstream connection currently pinned to this session (for native WS mode).
	// nil when using HTTP bridge.
	upstream *UpstreamConn

	// LastResponseID tracks the most recent response ID for previous_response_id chaining.
	lastResponseID string

	closed bool
}

// NewSession creates a new session for a client WebSocket connection.
func NewSession(clientConn *ws.Conn) *Session {
	return &Session{
		clientConn: clientConn,
	}
}

// ClientConn returns the client's WebSocket connection.
func (s *Session) ClientConn() *ws.Conn {
	return s.clientConn
}

// SetUpstream pins an upstream connection to this session.
func (s *Session) SetUpstream(conn *UpstreamConn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upstream = conn
}

// Upstream returns the currently pinned upstream connection, or nil.
func (s *Session) Upstream() *UpstreamConn {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.upstream
}

// SetLastResponseID updates the last response ID for chaining.
func (s *Session) SetLastResponseID(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastResponseID = id
}

// LastResponseID returns the last response ID.
func (s *Session) LastResponseID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastResponseID
}

// Close closes the session and its upstream connection if pinned.
func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	if s.upstream != nil {
		s.upstream.Close()
		s.upstream = nil
	}
}

// SessionManager tracks active sessions for connection limiting and cleanup.
type SessionManager struct {
	mu       sync.RWMutex
	sessions map[*ws.Conn]*Session
	maxConns int
}

// NewSessionManager creates a new session manager.
func NewSessionManager(maxConns int) *SessionManager {
	return &SessionManager{
		sessions: make(map[*ws.Conn]*Session),
		maxConns: maxConns,
	}
}

// Create creates and registers a new session for the given client connection.
// Returns an error if the connection limit would be exceeded.
func (m *SessionManager) Create(clientConn *ws.Conn) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.maxConns > 0 && len(m.sessions) >= m.maxConns {
		return nil, ErrConnectionLimitReached
	}

	session := NewSession(clientConn)
	m.sessions[clientConn] = session
	return session, nil
}

// Get returns the session for the given client connection.
func (m *SessionManager) Get(clientConn *ws.Conn) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[clientConn]
}

// Remove removes and closes a session.
func (m *SessionManager) Remove(clientConn *ws.Conn) {
	m.mu.Lock()
	session, ok := m.sessions[clientConn]
	if ok {
		delete(m.sessions, clientConn)
	}
	m.mu.Unlock()

	if session != nil {
		session.Close()
	}
}

// Count returns the number of active sessions.
func (m *SessionManager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.sessions)
}

// CloseAll closes all active sessions.
func (m *SessionManager) CloseAll() {
	m.mu.Lock()
	sessions := m.sessions
	m.sessions = make(map[*ws.Conn]*Session)
	m.mu.Unlock()

	for _, session := range sessions {
		session.Close()
	}
}
