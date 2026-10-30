package chaosmock

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// HeaderSessionID and HeaderProtocolVersion are the streamable HTTP transport
// headers, spelled as the specification does. Clients compare them
// case-insensitively via net/http, but the mock echoes this exact casing so a
// client reading raw headers still finds them.
const (
	HeaderSessionID       = "Mcp-Session-Id"
	HeaderProtocolVersion = "Mcp-Protocol-Version"
)

// DefaultProtocolVersion is what the mock advertises when a client asks for
// something it does not recognize.
const DefaultProtocolVersion = "2025-06-18"

// supportedProtocolVersions are echoed back verbatim when a client requests
// one of them, so the mock does not force a downgrade on a client that is
// merely newer than this list.
var supportedProtocolVersions = map[string]bool{
	"2024-11-05": true,
	"2025-03-26": true,
	"2025-06-18": true,
}

// Session is one client's connection state.
type Session struct {
	ID              string
	ProtocolVersion string
	CreatedAt       time.Time
	Initialized     bool
}

// sessionStore tracks live sessions. A killed session is removed rather than
// flagged: the client's contract is that an unknown session id gets a 404 and
// triggers re-initialization, and keeping a tombstone would only invite
// answering it differently.
type sessionStore struct {
	mu   sync.Mutex
	live map[string]*Session
}

func newSessionStore() *sessionStore {
	return &sessionStore{live: map[string]*Session{}}
}

func (s *sessionStore) create(protocolVersion string) *Session {
	sess := &Session{
		ID:              randomID(),
		ProtocolVersion: protocolVersion,
		CreatedAt:       time.Now(),
		Initialized:     true,
	}
	s.mu.Lock()
	s.live[sess.ID] = sess
	s.mu.Unlock()
	return sess
}

func (s *sessionStore) get(id string) (*Session, bool) {
	if id == "" {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.live[id]
	return sess, ok
}

// killAll invalidates every session, reproducing a server restart or a session
// eviction without dropping the TCP connection. The distinction matters: the
// transport stays up, so a client that only watches for transport errors will
// not notice until its next request 404s.
func (s *sessionStore) killAll() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.live)
	s.live = map[string]*Session{}
	return n
}

func (s *sessionStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.live)
}

func randomID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// A mock that cannot generate an id is broken in a way no test should
		// paper over, but panicking in a request path is worse than a degraded
		// id, so fall back to a time-derived one.
		return hex.EncodeToString([]byte(time.Now().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(b)
}
