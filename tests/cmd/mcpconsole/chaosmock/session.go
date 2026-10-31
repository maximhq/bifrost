package chaosmock

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
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

// Session is one client's connection state. Identity is resolved once at
// initialize and carried for the session's life, which lets per-tool counters
// attribute a call to a caller without re-authenticating every request.
type Session struct {
	ID              string
	Identity        string
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

func (s *sessionStore) create(identity, protocolVersion string) *Session {
	sess := &Session{
		ID:              randomID(),
		Identity:        identity,
		ProtocolVersion: protocolVersion,
		CreatedAt:       time.Now(),
		Initialized:     true,
	}
	s.mu.Lock()
	s.live[sess.ID] = sess
	s.mu.Unlock()
	return sess
}

// negotiated records the protocol version an initialize settled on, for a
// session that already existed. Done through the store so the write is under
// the same lock every reader takes.
func (s *sessionStore) negotiated(id, protocolVersion string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.live[id]; ok {
		sess.ProtocolVersion = protocolVersion
	}
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

// Auth describes what a virtual server demands of a caller. The zero value
// accepts everything, which is the "none" auth type under test.
type Auth struct {
	// RequireHeaders maps a header name to the value it must carry. An empty
	// expected value means any non-empty value passes, which models a shared
	// header credential when the test cares that a header arrived rather than
	// what it held.
	RequireHeaders map[string]string `json:"require_headers,omitempty"`

	// Bearer maps an Authorization bearer token to the identity it
	// authenticates. Populating this turns a server into one that can tell its
	// callers apart, which is what makes per-identity tool overlays and
	// per-identity counters meaningful.
	Bearer map[string]string `json:"bearer,omitempty"`

	// IdentityHeader names a header carrying the caller's identity directly,
	// for header-authenticated setups that have no token to map.
	IdentityHeader string `json:"identity_header,omitempty"`

	// Require rejects any request that resolved no identity and satisfied no
	// header requirement. Left false, an unauthenticated caller is served as
	// the anonymous identity.
	Require bool `json:"require,omitempty"`

	// OAuth, when set, makes the server an OAuth protected resource with its
	// own authorization server (see oauth.go). POST and GET then need an
	// access token that server issued, or one of the Bearer tokens above;
	// RequireHeaders, IdentityHeader and Require no longer apply.
	OAuth *OAuthConfig `json:"oauth,omitempty"`
}

// authState guards Auth for concurrent control-API writes.
type authState struct {
	mu sync.Mutex
	a  Auth
}

func newAuthState() *authState {
	return &authState{}
}

func (as *authState) set(a Auth) {
	as.mu.Lock()
	as.a = a
	as.mu.Unlock()
}

func (as *authState) get() Auth {
	as.mu.Lock()
	defer as.mu.Unlock()
	return as.a
}

// revoke drops one bearer token, leaving every other token working. This is
// the most important auth mutation the mock offers: it produces "user A's
// credential died and user B's did not", and that asymmetry is what separates
// a real per-user implementation from one that shares state.
func (as *authState) revoke(token string) bool {
	as.mu.Lock()
	defer as.mu.Unlock()
	if _, ok := as.a.Bearer[token]; !ok {
		return false
	}
	delete(as.a.Bearer, token)
	return true
}

// grant adds or replaces one bearer token.
func (as *authState) grant(token, identity string) {
	as.mu.Lock()
	defer as.mu.Unlock()
	if as.a.Bearer == nil {
		as.a.Bearer = map[string]string{}
	}
	as.a.Bearer[token] = identity
}

// authResult carries the outcome of checking one request's credentials.
type authResult struct {
	Identity string
	OK       bool
	Reason   string
}

// check resolves a request's identity and whether it may proceed. It never
// consults faults: an injected 401 is a separate thing from a genuine
// credential rejection, and conflating them would make it impossible to tell
// whether a client reacts to the status or to the credential.
func (as *authState) check(h http.Header) authResult {
	a := as.get()

	for name, want := range a.RequireHeaders {
		got := h.Get(name)
		if got == "" {
			return authResult{Reason: "missing required header " + name}
		}
		if want != "" && got != want {
			return authResult{Reason: "header " + name + " has unexpected value"}
		}
	}

	identity := ""
	if a.IdentityHeader != "" {
		identity = h.Get(a.IdentityHeader)
	}

	if len(a.Bearer) > 0 {
		token := bearerToken(h)
		if token == "" {
			if a.Require {
				return authResult{Reason: "missing bearer token"}
			}
		} else {
			mapped, ok := a.Bearer[token]
			if !ok {
				// An unrecognized token is always a rejection, even when
				// Require is false: presenting a credential the server does
				// not know is a different situation from presenting none.
				return authResult{Reason: "unknown bearer token"}
			}
			identity = mapped
		}
	}

	if a.Require && identity == "" {
		return authResult{Reason: "no identity resolved"}
	}
	return authResult{Identity: identity, OK: true}
}

func bearerToken(h http.Header) string {
	v := h.Get("Authorization")
	if v == "" {
		return ""
	}
	const prefix = "Bearer "
	if len(v) > len(prefix) && strings.EqualFold(v[:len(prefix)], prefix) {
		return strings.TrimSpace(v[len(prefix):])
	}
	return ""
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
