package websocket

import (
	"strings"
	"sync"
	"time"

	ws "github.com/fasthttp/websocket"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
)

// Session tracks the binding between a client WebSocket connection and its upstream state.
// For Responses WS mode, it tracks previous_response_id → upstream connection pinning.
type Session struct {
	mu      sync.RWMutex
	writeMu sync.Mutex // serializes all WriteMessage calls to clientConn

	id string

	// Client connection
	clientConn *ws.Conn

	// Upstream connection currently pinned to this session (for native WS mode).
	// nil when using HTTP bridge.
	upstream *UpstreamConn

	// LastResponseID tracks the most recent response ID for previous_response_id chaining.
	lastResponseID string

	// providerSessionID tracks the upstream provider's session identifier when exposed.
	providerSessionID string

	// realtimeOutputText accumulates assistant/provider turn text until the terminal event.
	realtimeOutputText string

	// realtimeTurnInputs accumulates finalized user/tool inputs in arrival order so the
	// completed assistant turn can persist the full turn history instead of only the
	// latest finalized input event.
	realtimeTurnInputs []RealtimeTurnInput

	// realtimeConsumedTurnItemIDs tracks finalized item IDs that have already been
	// attached to a persisted turn, so late transcript updates do not pollute later turns.
	realtimeConsumedTurnItemIDs map[string]struct{}

	// realtimeTurnHooks tracks the active turn-scoped plugin pipeline between
	// response.create and response.done.
	realtimeTurnHooks *RealtimeTurnPluginState
	realtimeTurnBusy  bool

	closed bool
}

type RealtimeToolOutput struct {
	Summary string
	Raw     string
}

type RealtimeTurnInput struct {
	ItemID  string
	Role    string
	Summary string
	Raw     string
}

type RealtimeTurnPluginState struct {
	PostHookRunner schemas.PostHookRunner
	Cleanup        func()
	RequestID      string
	StartedAt      time.Time
	PreHookValues  map[any]any
}

// NewSession creates a new session for a client WebSocket connection.
func NewSession(clientConn *ws.Conn) *Session {
	return &Session{
		id:         uuid.NewString(),
		clientConn: clientConn,
	}
}

// ID returns the stable Bifrost session identifier for this websocket session.
func (s *Session) ID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.id
}

// ClientConn returns the client's WebSocket connection.
func (s *Session) ClientConn() *ws.Conn {
	return s.clientConn
}

// WriteMessage sends a message to the client WebSocket connection.
// It serializes concurrent writes via writeMu to prevent panics from
// simultaneous goroutine writes (e.g., heartbeat vs streaming relay).
func (s *Session) WriteMessage(messageType int, data []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.clientConn.WriteMessage(messageType, data)
}

// SetUpstream pins an upstream connection to this session.
func (s *Session) SetUpstream(conn *UpstreamConn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		if conn != nil {
			conn.Close()
		}
		return
	}
	if s.upstream != nil && s.upstream != conn {
		s.upstream.Close()
	}
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

// SetProviderSessionID stores the upstream provider session identifier when available.
func (s *Session) SetProviderSessionID(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.providerSessionID = id
}

// ProviderSessionID returns the upstream provider session identifier when known.
func (s *Session) ProviderSessionID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.providerSessionID
}

// AppendRealtimeOutputText appends provider output content for the current realtime turn.
func (s *Session) AppendRealtimeOutputText(text string) {
	if text == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.realtimeOutputText += text
}

// ConsumeRealtimeOutputText returns the accumulated provider output and clears it.
func (s *Session) ConsumeRealtimeOutputText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	text := s.realtimeOutputText
	s.realtimeOutputText = ""
	return text
}

// AddRealtimeInput stores a finalized user turn event in arrival order.
func (s *Session) AddRealtimeInput(summary, raw string) {
	if summary == "" && raw == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.realtimeTurnInputs = append(s.realtimeTurnInputs, RealtimeTurnInput{
		Role:    string(schemas.ChatMessageRoleUser),
		Summary: summary,
		Raw:     raw,
	})
}

// RecordRealtimeInput stores or updates a finalized user turn event keyed by item ID.
// Late updates for items already attached to a completed turn are ignored.
func (s *Session) RecordRealtimeInput(itemID, summary, raw string) {
	s.recordRealtimeTurnInput(itemID, string(schemas.ChatMessageRoleUser), summary, raw)
}

// AddRealtimeToolOutput stores a pending tool result for the next assistant turn.
func (s *Session) AddRealtimeToolOutput(summary, raw string) {
	if summary == "" && raw == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.realtimeTurnInputs = append(s.realtimeTurnInputs, RealtimeTurnInput{
		Role:    string(schemas.ChatMessageRoleTool),
		Summary: summary,
		Raw:     raw,
	})
}

// RecordRealtimeToolOutput stores or updates a finalized tool result keyed by item ID.
// Late updates for items already attached to a completed turn are ignored.
func (s *Session) RecordRealtimeToolOutput(itemID, summary, raw string) {
	s.recordRealtimeTurnInput(itemID, string(schemas.ChatMessageRoleTool), summary, raw)
}

func (s *Session) recordRealtimeTurnInput(itemID, role, summary, raw string) {
	if summary == "" && raw == "" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	itemID = strings.TrimSpace(itemID)
	if itemID != "" {
		if _, consumed := s.realtimeConsumedTurnItemIDs[itemID]; consumed {
			return
		}
		for idx := range s.realtimeTurnInputs {
			if s.realtimeTurnInputs[idx].ItemID != itemID || s.realtimeTurnInputs[idx].Role != role {
				continue
			}
			if strings.TrimSpace(summary) != "" {
				s.realtimeTurnInputs[idx].Summary = summary
			}
			if strings.TrimSpace(raw) != "" {
				existingRaw := strings.TrimSpace(s.realtimeTurnInputs[idx].Raw)
				incomingRaw := strings.TrimSpace(raw)
				switch {
				case existingRaw == "":
					s.realtimeTurnInputs[idx].Raw = raw
				case incomingRaw == "" || existingRaw == incomingRaw:
				default:
					s.realtimeTurnInputs[idx].Raw = existingRaw + "\n\n" + incomingRaw
				}
			}
			return
		}
	}

	s.realtimeTurnInputs = append(s.realtimeTurnInputs, RealtimeTurnInput{
		ItemID:  itemID,
		Role:    role,
		Summary: summary,
		Raw:     raw,
	})
}

// ConsumeRealtimeTurnInputs returns pending realtime turn inputs and clears them.
func (s *Session) ConsumeRealtimeTurnInputs() []RealtimeTurnInput {
	s.mu.Lock()
	defer s.mu.Unlock()
	inputs := append([]RealtimeTurnInput(nil), s.realtimeTurnInputs...)
	if len(inputs) > 0 {
		if s.realtimeConsumedTurnItemIDs == nil {
			s.realtimeConsumedTurnItemIDs = make(map[string]struct{}, len(inputs))
		}
		for _, input := range inputs {
			if strings.TrimSpace(input.ItemID) != "" {
				s.realtimeConsumedTurnItemIDs[input.ItemID] = struct{}{}
			}
		}
	}
	s.realtimeTurnInputs = nil
	return inputs
}

// PeekRealtimeTurnInputs returns pending realtime turn inputs without clearing them.
func (s *Session) PeekRealtimeTurnInputs() []RealtimeTurnInput {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]RealtimeTurnInput(nil), s.realtimeTurnInputs...)
}

// SetRealtimeTurnHooks stores the active turn-scoped plugin pipeline.
func (s *Session) SetRealtimeTurnHooks(state *RealtimeTurnPluginState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.realtimeTurnHooks != nil && s.realtimeTurnHooks.Cleanup != nil {
		s.realtimeTurnHooks.Cleanup()
	}
	s.realtimeTurnBusy = false
	if s.closed {
		if state != nil && state.Cleanup != nil {
			state.Cleanup()
		}
		s.realtimeTurnHooks = nil
		return
	}
	s.realtimeTurnHooks = state
}

// TryBeginRealtimeTurnHooks reserves the single active turn slot.
func (s *Session) TryBeginRealtimeTurnHooks() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.realtimeTurnBusy || s.realtimeTurnHooks != nil {
		return false
	}
	s.realtimeTurnBusy = true
	return true
}

// AbortRealtimeTurnHooks releases a reserved turn slot without installing hooks.
func (s *Session) AbortRealtimeTurnHooks() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.realtimeTurnBusy = false
}

// PeekRealtimeTurnHooks returns the active turn-scoped plugin pipeline without clearing it.
func (s *Session) PeekRealtimeTurnHooks() *RealtimeTurnPluginState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.realtimeTurnHooks
}

// ConsumeRealtimeTurnHooks returns the active turn-scoped plugin pipeline and clears it.
func (s *Session) ConsumeRealtimeTurnHooks() *RealtimeTurnPluginState {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.realtimeTurnHooks
	s.realtimeTurnHooks = nil
	s.realtimeTurnBusy = false
	return state
}

// ClearRealtimeTurnHooks cleans up and clears any active turn-scoped plugin pipeline.
func (s *Session) ClearRealtimeTurnHooks() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.realtimeTurnHooks != nil && s.realtimeTurnHooks.Cleanup != nil {
		s.realtimeTurnHooks.Cleanup()
	}
	s.realtimeTurnHooks = nil
	s.realtimeTurnBusy = false
}

// Close closes the session and its upstream connection if pinned.
func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	if s.realtimeTurnHooks != nil {
		if s.realtimeTurnHooks.Cleanup != nil {
			s.realtimeTurnHooks.Cleanup()
		}
		s.realtimeTurnHooks = nil
	}
	s.realtimeTurnBusy = false
	if s.clientConn != nil {
		_ = s.clientConn.Close()
	}
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

// Snapshot returns a copy of the currently tracked sessions.
func (m *SessionManager) Snapshot() []*Session {
	m.mu.RLock()
	defer m.mu.RUnlock()

	sessions := make([]*Session, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	return sessions
}
