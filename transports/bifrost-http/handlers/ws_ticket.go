package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

const (
	wsTicketTTL       = 30 * time.Second
	wsTicketCleanupHz = 60 * time.Second
)

type wsTicketEntry struct {
	sessionToken string
	expiresAt    time.Time
}

// WSTicketStore provides short-lived, single-use tickets for WebSocket authentication.
// Instead of putting the long-lived session token in the WS URL (visible in logs/history),
// clients exchange their session for a 30-second one-time ticket via an authenticated endpoint.
type WSTicketStore struct {
	mu       sync.Mutex
	tickets  map[string]wsTicketEntry
	done     chan struct{}
	stopOnce sync.Once
}

// NewWSTicketStore creates a new ticket store and starts a background goroutine
// that periodically purges expired tickets.
func NewWSTicketStore() *WSTicketStore {
	s := &WSTicketStore{
		tickets: make(map[string]wsTicketEntry),
		done:    make(chan struct{}),
	}
	go s.cleanup()
	return s
}

// Issue generates a cryptographically random ticket bound to the given session token.
// The ticket expires after wsTicketTTL (30 seconds).
func (s *WSTicketStore) Issue(sessionToken string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	ticket := hex.EncodeToString(b)

	s.mu.Lock()
	s.tickets[ticket] = wsTicketEntry{
		sessionToken: sessionToken,
		expiresAt:    time.Now().Add(wsTicketTTL),
	}
	s.mu.Unlock()
	return ticket, nil
}

// Consume validates and deletes a ticket, returning the underlying session token.
// Returns empty string if the ticket doesn't exist or has expired (single-use).
func (s *WSTicketStore) Consume(ticket string) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.tickets[ticket]
	if !ok {
		return ""
	}
	delete(s.tickets, ticket)
	if time.Now().After(entry.expiresAt) {
		return ""
	}
	return entry.sessionToken
}

// Stop terminates the background cleanup goroutine.
func (s *WSTicketStore) Stop() {
	s.stopOnce.Do(func() {
		close(s.done)
	})
}

// cleanup periodically removes expired tickets to prevent unbounded memory growth.
func (s *WSTicketStore) cleanup() {
	ticker := time.NewTicker(wsTicketCleanupHz)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			now := time.Now()
			s.mu.Lock()
			for k, v := range s.tickets {
				if now.After(v.expiresAt) {
					delete(s.tickets, k)
				}
			}
			s.mu.Unlock()
		}
	}
}
