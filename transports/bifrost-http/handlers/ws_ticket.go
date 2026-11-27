package handlers

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/maximhq/bifrost/framework/livestore"
)

const (
	wsTicketTTL     = 30 * time.Second
	wsTicketVersion = 1
	// wsTicketTable is the live-store table unsigned tickets live in, one per process.
	wsTicketTable = "ws_tickets"
)

var errInvalidWSTicketPayload = errors.New("invalid websocket ticket payload")

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
	// tickets maps an unsigned ticket to its session token until it expires; the
	// live store sweeps expired ones. Nil in signed mode.
	tickets    *livestore.Table[string, string]
	live       *livestore.Store
	signingKey []byte
}

// NewWSTicketStore creates a ticket store that keeps its tickets in live, each
// expiring after wsTicketTTL.
func NewWSTicketStore(live *livestore.Store) (*WSTicketStore, error) {
	if live == nil {
		return nil, fmt.Errorf("websocket tickets: %w", livestore.ErrNoLiveStore)
	}
	tickets, err := live.NewTable[string, string](context.Background(), wsTicketTable, livestore.WithTTL(wsTicketTTL))
	if err != nil {
		return nil, err
	}
	return &WSTicketStore{tickets: tickets, live: live}, nil
}

// NewSignedWSTicketStore creates a ticket store that signs self-verifying tickets.
// If signingKey is empty, falls back to the in-memory NewWSTicketStore flow:
// passing a zero-length key would otherwise derive a publicly-knowable HMAC key
// (sha256 of just the purpose label) and silently activate signed mode with a
// forgeable key. Falling back to the in-memory store keeps single-node
// deployments safe while letting multi-node deployments opt into signed mode by
// supplying a real shared key.
func NewSignedWSTicketStore(live *livestore.Store, signingKey []byte) (*WSTicketStore, error) {
	if len(signingKey) == 0 {
		return NewWSTicketStore(live)
	}
	key := deriveWSTicketKey("sig", signingKey)
	return &WSTicketStore{signingKey: key}, nil
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
	s.tickets.Put(ticket, sessionToken)
	return ticket, nil
}

// Consume validates and deletes a ticket, returning the underlying session token.
// Returns empty string if the ticket doesn't exist or has expired (single-use).
func (s *WSTicketStore) Consume(ticket string) string {
	if len(s.signingKey) > 0 {
		return s.consumeSigned(ticket)
	}

	sessionToken, _ := s.tickets.GetAndDelete(ticket)
	return sessionToken
}

// Stop drops the unsigned tickets' table, so a later store can create it again.
func (s *WSTicketStore) Stop() {
	if s.tickets != nil {
		_ = s.live.DropTable(context.Background(), wsTicketTable)
	}
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

// deriveWSTicketKey derives a 32-byte key for a WebSocket ticket purpose with
// HKDF-SHA-256 (NIST SP 800-56C), using the purpose as the info label so the
// signing and encryption subkeys are independent. hkdf.Key fails only for an
// output longer than 255 hash blocks, so the error path cannot be reached; a nil
// key there makes the callers fail closed.
func deriveWSTicketKey(purpose string, key []byte) []byte {
	derived, err := hkdf.Key(sha256.New, key, nil, "bifrost-ws-ticket/"+purpose, 32)
	if err != nil {
		return nil
	}
	return derived
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
