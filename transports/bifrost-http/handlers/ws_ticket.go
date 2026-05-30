package handlers

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

const (
	wsTicketTTL       = 30 * time.Second
	wsTicketCleanupHz = 60 * time.Second
	wsTicketVersion   = 1
)

var errInvalidWSTicketPayload = errors.New("invalid websocket ticket payload")

type wsTicketEntry struct {
	sessionToken string
	expiresAt    time.Time
}

type signedWSTicketPayload struct {
	Version      int    `json:"v"`
	SessionToken string `json:"s"`
	ExpiresAt    int64  `json:"e"`
	Nonce        string `json:"n"`
}

// WSTicketStore provides short-lived, single-use tickets for WebSocket authentication.
// Instead of putting the long-lived session token in the WS URL (visible in logs/history),
// clients exchange their session for a 30-second one-time ticket via an authenticated endpoint.
type WSTicketStore struct {
	mu         sync.Mutex
	tickets    map[string]wsTicketEntry
	done       chan struct{}
	stopOnce   sync.Once
	signingKey []byte
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

// NewSignedWSTicketStore creates a ticket store that signs self-verifying tickets.
// If signingKey is empty, falls back to the in-memory NewWSTicketStore flow:
// passing a zero-length key would otherwise derive a publicly-knowable HMAC key
// (sha256 of just the purpose label) and silently activate signed mode with a
// forgeable key. Falling back to the in-memory store keeps single-node
// deployments safe while letting multi-node deployments opt into signed mode by
// supplying a real shared key.
func NewSignedWSTicketStore(signingKey []byte) *WSTicketStore {
	if len(signingKey) == 0 {
		return NewWSTicketStore()
	}
	key := deriveWSTicketKey("sig", signingKey)
	return &WSTicketStore{
		signingKey: key,
		done:       make(chan struct{}),
	}
}

// Issue generates a cryptographically random ticket bound to the given session token.
// The ticket expires after wsTicketTTL (30 seconds).
func (s *WSTicketStore) Issue(sessionToken string) (string, error) {
	if len(s.signingKey) > 0 {
		return s.issueSigned(sessionToken)
	}

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
	if len(s.signingKey) > 0 {
		return s.consumeSigned(ticket)
	}

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

// issueSigned generates an HMAC-signed ticket that any node with the key can verify.
func (s *WSTicketStore) issueSigned(sessionToken string) (string, error) {
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return "", err
	}
	payload := signedWSTicketPayload{
		Version:      wsTicketVersion,
		SessionToken: sessionToken,
		ExpiresAt:    time.Now().Add(wsTicketTTL).Unix(),
		Nonce:        hex.EncodeToString(nonceBytes),
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	encryptedPayload, err := encryptWSTicketPayload(s.signingKey, payloadBytes)
	if err != nil {
		return "", err
	}
	encodedPayload := base64.RawURLEncoding.EncodeToString(encryptedPayload)
	mac := hmac.New(sha256.New, s.signingKey)
	mac.Write([]byte(encodedPayload))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return encodedPayload + "." + signature, nil
}

// consumeSigned validates an HMAC-signed ticket and returns its session token.
func (s *WSTicketStore) consumeSigned(ticket string) string {
	dot := -1
	for i := 0; i < len(ticket); i++ {
		if ticket[i] == '.' {
			dot = i
			break
		}
	}
	if dot <= 0 || dot == len(ticket)-1 {
		return ""
	}

	encodedPayload := ticket[:dot]
	encodedSignature := ticket[dot+1:]
	signature, err := base64.RawURLEncoding.DecodeString(encodedSignature)
	if err != nil {
		return ""
	}
	mac := hmac.New(sha256.New, s.signingKey)
	mac.Write([]byte(encodedPayload))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return ""
	}

	encryptedPayload, err := base64.RawURLEncoding.DecodeString(encodedPayload)
	if err != nil {
		return ""
	}
	payloadBytes, err := decryptWSTicketPayload(s.signingKey, encryptedPayload)
	if err != nil {
		return ""
	}
	var payload signedWSTicketPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return ""
	}
	if payload.Version != wsTicketVersion || payload.SessionToken == "" || payload.ExpiresAt <= time.Now().Unix() {
		return ""
	}
	return payload.SessionToken
}

// deriveWSTicketKey derives a 32-byte key for a WebSocket ticket purpose.
func deriveWSTicketKey(purpose string, key []byte) []byte {
	sum := sha256.Sum256(append([]byte(purpose+":"), key...))
	return sum[:]
}

// encryptWSTicketPayload encrypts a signed WebSocket ticket payload with AES-GCM.
func encryptWSTicketPayload(key []byte, payload []byte) ([]byte, error) {
	block, err := aes.NewCipher(deriveWSTicketKey("enc", key))
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	ciphertext := aead.Seal(nil, nonce, payload, nil)
	return append(nonce, ciphertext...), nil
}

// decryptWSTicketPayload decrypts an AES-GCM WebSocket ticket payload.
func decryptWSTicketPayload(key []byte, encryptedPayload []byte) ([]byte, error) {
	block, err := aes.NewCipher(deriveWSTicketKey("enc", key))
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonceSize := aead.NonceSize()
	if len(encryptedPayload) < nonceSize {
		return nil, errInvalidWSTicketPayload
	}
	nonce := encryptedPayload[:nonceSize]
	ciphertext := encryptedPayload[nonceSize:]
	return aead.Open(nil, nonce, ciphertext, nil)
}

// cleanup periodically removes expired tickets to prevent unbounded memory growth.
func (s *WSTicketStore) cleanup() {
	if s.tickets == nil {
		<-s.done
		return
	}

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
