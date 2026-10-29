package websocket

import (
	"encoding/json"
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

	id        string
	startedAt time.Time // Immutable local creation time, before any turn log can be written.

	// Client connection
	clientConn *ws.Conn

	// Upstream connection currently pinned to this session (for native WS mode).
	// nil when using HTTP bridge.
	upstream *UpstreamConn

	// LastResponseID tracks the most recent response ID for previous_response_id chaining.
	lastResponseID string

	// responsesCompleted is true once at least one Responses WS turn has reached
	// a terminal event. EOF after that point is just client disconnect cleanup.
	responsesCompleted bool

	// providerSessionID tracks the upstream provider's session identifier when exposed.
	providerSessionID string

	responsesKeyProvider schemas.ModelProvider // provider of responsesKeyID
	responsesKeyID       string                // key that served this session's last Responses turn

	// realtimeOutputText accumulates assistant/provider turn text until the terminal event.
	realtimeOutputText string

	// realtimeTurnInputs accumulates finalized user/tool inputs in arrival order so the
	// completed assistant turn can persist the full turn history instead of only the
	// latest finalized input event.
	realtimeTurnInputs []RealtimeTurnInput

	// realtimeSessionTools holds the latest session tool definitions from
	// session.created / session.updated / session.update events, so that
	// each turn log can record which tools were available.
	realtimeSessionTools json.RawMessage

	// realtimeVoice holds the voice from the latest session configuration.
	realtimeVoice string
	// realtimeVoiceType holds that voice's type, such as Azure Voice Live's "azure-standard".
	realtimeVoiceType string

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
	ItemID             string
	Role               string
	Summary            string
	Raw                string
	TranscriptComplete bool // Prevent an item.done echo from replacing a completed transcript.
}

type RealtimeTurnPluginState struct {
	PostHookRunner schemas.PostHookRunner
	Cleanup        func()
	RequestID      string
	StartedAt      time.Time
	PreHookValues  map[any]any
	TraceID        string
	RawStore       bool
}

// NewSession creates a new session for a client WebSocket connection.
func NewSession(clientConn *ws.Conn) *Session {
	return &Session{
		id:         uuid.NewString(),
		startedAt:  time.Now().UTC(),
		clientConn: clientConn,
	}
}

// StartedAt returns the session creation time used to bound persisted turn lookups.
func (s *Session) StartedAt() time.Time {
	return s.startedAt
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
	s.responsesCompleted = true
}

// LastResponseID returns the last response ID.
func (s *Session) LastResponseID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastResponseID
}

// HasCompletedResponsesTurn reports whether this session already completed a Responses WS turn.
func (s *Session) HasCompletedResponsesTurn() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.responsesCompleted
}

// MarkResponsesTurnCompleted records terminal completion even when no response ID is available.
func (s *Session) MarkResponsesTurnCompleted() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.responsesCompleted = true
}

// SetResponsesKey records the key that served a Responses WS turn on provider.
func (s *Session) SetResponsesKey(provider schemas.ModelProvider, keyID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.responsesKeyProvider = provider
	s.responsesKeyID = keyID
}

// ResponsesKey returns the key that served this session's last Responses WS turn on provider,
// or "" when no turn on provider has been served yet.
func (s *Session) ResponsesKey(provider schemas.ModelProvider) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.responsesKeyProvider != provider {
		return ""
	}
	return s.responsesKeyID
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

// SetRealtimeSessionTools updates the tracked session tool definitions.
// Called when session.created, session.updated, or session.update events
// carry a tools array.
func (s *Session) SetRealtimeSessionTools(tools json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.realtimeSessionTools = tools
}

// RealtimeSessionTools returns the latest session tool definitions, or nil.
func (s *Session) RealtimeSessionTools() json.RawMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.realtimeSessionTools
}

// SetRealtimeVoice updates the tracked voice from session configuration.
func (s *Session) SetRealtimeVoice(voice string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.realtimeVoice = voice
}

// RealtimeVoice returns the current session voice, or empty string.
func (s *Session) RealtimeVoice() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.realtimeVoice
}

// SetRealtimeVoiceType updates the tracked voice type from session configuration.
func (s *Session) SetRealtimeVoiceType(voiceType string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.realtimeVoiceType = voiceType
}

// RealtimeVoiceType returns the current session voice's type, or empty string.
func (s *Session) RealtimeVoiceType() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.realtimeVoiceType
}

// AppendRealtimeOutputText appends provider output content for the current realtime turn.
func (s *Session) AppendRealtimeOutputText(text string) {
	if text == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
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
// Completion events must use UpdateRealtimeTurnInput so they cannot introduce an old item.
func (s *Session) RecordRealtimeInput(itemID, summary, raw string) {
	s.recordRealtimeTurnInput(RealtimeTurnInput{ItemID: itemID, Role: string(schemas.ChatMessageRoleUser), Summary: summary, Raw: raw}, true)
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
// Provider echoes must use UpdateRealtimeTurnInput after the client introduces the result.
func (s *Session) RecordRealtimeToolOutput(itemID, summary, raw string) {
	s.recordRealtimeTurnInput(RealtimeTurnInput{ItemID: itemID, Role: string(schemas.ChatMessageRoleTool), Summary: summary, Raw: raw}, true)
}

// UpdateRealtimeTurnInput updates only an existing pending item under the consume lock.
// It reports whether the item matched and whether the session has already closed.
func (s *Session) UpdateRealtimeTurnInput(input RealtimeTurnInput) (updated, closed bool) {
	return s.recordRealtimeTurnInput(input, false)
}

// recordRealtimeTurnInput merges pending state without retaining completed-item history.
func (s *Session) recordRealtimeTurnInput(input RealtimeTurnInput, allowNew bool) (updated, closed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false, true
	}
	if input.Summary == "" && input.Raw == "" {
		return false, false
	}
	input.ItemID = strings.TrimSpace(input.ItemID)
	if input.ItemID != "" {
		for idx := range s.realtimeTurnInputs {
			pending := &s.realtimeTurnInputs[idx]
			if pending.ItemID != input.ItemID || pending.Role != input.Role {
				continue
			}
			if pending.TranscriptComplete && !input.TranscriptComplete {
				return true, false
			}
			if strings.TrimSpace(input.Summary) != "" {
				pending.Summary = input.Summary
			}
			if strings.TrimSpace(input.Raw) != "" {
				pending.Raw = input.Raw
			}
			pending.TranscriptComplete = input.TranscriptComplete
			return true, false
		}
	}
	if allowNew {
		s.realtimeTurnInputs = append(s.realtimeTurnInputs, input)
		return true, false
	}
	return false, false
}

// ConsumeRealtimeTurnInputs returns pending realtime turn inputs and clears them.
func (s *Session) ConsumeRealtimeTurnInputs() []RealtimeTurnInput {
	s.mu.Lock()
	defer s.mu.Unlock()
	inputs := append([]RealtimeTurnInput(nil), s.realtimeTurnInputs...)
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

	// Release accumulated turn data so GC can reclaim memory even if a
	// goroutine briefly holds a reference to this session after close.
	s.realtimeTurnInputs = nil
	s.realtimeSessionTools = nil
	s.realtimeVoice = ""
	s.realtimeVoiceType = ""
	s.realtimeOutputText = ""

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
	closed   bool // set by CloseAll; Create refuses afterwards
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

	if m.closed {
		return nil, ErrSessionManagerClosed
	}
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
	m.closed = true
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
