package handlers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/livestore"
)

func TestSignedWSTicketValidatesAcrossStores(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	issuer := signedTicketsForTest(t, key)
	consumer := signedTicketsForTest(t, key)

	ticket, err := issuer.Issue("session-token")
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	if got := consumer.Consume(ticket); got != "session-token" {
		t.Fatalf("Consume() = %q, want %q", got, "session-token")
	}
	if got := consumer.Consume(ticket); got != "session-token" {
		t.Fatalf("second Consume() = %q, want %q for stateless signed tickets", got, "session-token")
	}
}

func TestSignedWSTicketRejectsWrongKey(t *testing.T) {
	issuer := signedTicketsForTest(t, []byte("0123456789abcdef0123456789abcdef"))
	consumer := signedTicketsForTest(t, []byte("abcdef0123456789abcdef0123456789"))

	ticket, err := issuer.Issue("session-token")
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	if got := consumer.Consume(ticket); got != "" {
		t.Fatalf("Consume() = %q, want empty string", got)
	}
}

func TestSignedWSTicketRejectsExpiredTicket(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	store := signedTicketsForTest(t, key)
	ticket := buildSignedWSTicketForTest(t, store.signingKey, signedWSTicketPayload{
		Version:      wsTicketVersion,
		SessionToken: "session-token",
		ExpiresAt:    time.Now().Add(-time.Second).Unix(),
		Nonce:        "nonce",
	})

	if got := store.Consume(ticket); got != "" {
		t.Fatalf("Consume() = %q, want empty string", got)
	}
}

func TestSignedWSTicketRejectsMalformedTicket(t *testing.T) {
	store := signedTicketsForTest(t, []byte("0123456789abcdef0123456789abcdef"))

	for _, ticket := range []string{"", "missing-dot", "payload.", ".signature", "payload.not-base64"} {
		if got := store.Consume(ticket); got != "" {
			t.Fatalf("Consume(%q) = %q, want empty string", ticket, got)
		}
	}
}

func TestSignedWSTicketRejectsTamperedTicket(t *testing.T) {
	store := signedTicketsForTest(t, []byte("0123456789abcdef0123456789abcdef"))

	ticket, err := store.Issue("session-token")
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	parts := strings.Split(ticket, ".")
	if len(parts) != 2 {
		t.Fatalf("ticket parts = %d, want 2", len(parts))
	}
	tampered := parts[0] + "a." + parts[1]

	if got := store.Consume(tampered); got != "" {
		t.Fatalf("Consume() = %q, want empty string", got)
	}
}

func TestLegacyWSTicketRemainsSingleUse(t *testing.T) {
	store := ticketsForTest(t)
	defer store.Stop()

	ticket, err := store.Issue("session-token")
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if got := store.Consume(ticket); got != "session-token" {
		t.Fatalf("Consume() = %q, want %q", got, "session-token")
	}
	if got := store.Consume(ticket); got != "" {
		t.Fatalf("second Consume() = %q, want empty string", got)
	}
}

func TestSignedWSTicketDoesNotExposeSessionToken(t *testing.T) {
	store := signedTicketsForTest(t, []byte("0123456789abcdef0123456789abcdef"))

	ticket, err := store.Issue("session-token")
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	if strings.Contains(ticket, "session-token") || strings.Contains(ticket, base64.RawURLEncoding.EncodeToString([]byte("session-token"))) {
		t.Fatalf("ticket exposes session token: %q", ticket)
	}
}

func buildSignedWSTicketForTest(t *testing.T, key []byte, payload signedWSTicketPayload) string {
	t.Helper()

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	encryptedPayload, err := encryptWSTicketPayload(key, payloadBytes)
	if err != nil {
		t.Fatalf("encryptWSTicketPayload() error = %v", err)
	}
	encodedPayload := base64.RawURLEncoding.EncodeToString(encryptedPayload)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(encodedPayload))
	return encodedPayload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func TestSignedWSTicketPayloadEncryptionRejectsShortPayload(t *testing.T) {
	store := signedTicketsForTest(t, []byte("0123456789abcdef0123456789abcdef"))
	encodedPayload := base64.RawURLEncoding.EncodeToString([]byte("short"))
	mac := hmac.New(sha256.New, store.signingKey)
	mac.Write([]byte(encodedPayload))
	ticket := encodedPayload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	if got := store.Consume(ticket); got != "" {
		t.Fatalf("Consume() = %q, want empty string", got)
	}
}

func TestSignedWSTicketNonceIsHex(t *testing.T) {
	store := signedTicketsForTest(t, []byte("0123456789abcdef0123456789abcdef"))

	ticket, err := store.Issue("session-token")
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	parts := strings.Split(ticket, ".")
	encryptedPayload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("DecodeString() error = %v", err)
	}
	payloadBytes, err := decryptWSTicketPayload(store.signingKey, encryptedPayload)
	if err != nil {
		t.Fatalf("decryptWSTicketPayload() error = %v", err)
	}
	var payload signedWSTicketPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if _, err := hex.DecodeString(payload.Nonce); err != nil {
		t.Fatalf("nonce is not hex: %v", err)
	}
}

// ticketsForTest returns an unsigned ticket store over a new live store.
func ticketsForTest(t *testing.T) *WSTicketStore {
	t.Helper()
	return signedTicketsForTest(t, nil)
}

// signedTicketsForTest returns a ticket store signing with key, unsigned when key is empty, over
// a new live store.
func signedTicketsForTest(t *testing.T, key []byte) *WSTicketStore {
	t.Helper()
	live, err := livestore.New()
	if err != nil {
		t.Fatalf("live store: %v", err)
	}
	t.Cleanup(live.Close)
	store, err := NewSignedWSTicketStore(live, key)
	if err != nil {
		t.Fatalf("ticket store: %v", err)
	}
	return store
}
