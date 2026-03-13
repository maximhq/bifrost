package copilot

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
)

// newTestTokenManager creates a copilotTokenManager wired to the given test server URL
// instead of the real GitHub token exchange endpoint.
func newTestTokenManager(accessToken string, serverURL string) *copilotTokenManager {
	tm := newCopilotTokenManager(accessToken, &fasthttp.Client{}, nil)
	tm.tokenExchangeURL = serverURL
	return tm
}

func TestTokenManager_CachesTokenAfterFirstCall(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(CopilotTokenResponse{
			Token:     "jwt-token-123",
			ExpiresAt: time.Now().Add(30 * time.Minute).Unix(),
		})
	}))
	defer srv.Close()

	tm := newTestTokenManager("oauth-token", srv.URL)

	token1, _, err1 := tm.getToken()
	token2, _, err2 := tm.getToken()

	if err1 != nil || err2 != nil {
		t.Fatalf("unexpected error: %v / %v", err1, err2)
	}
	if token1 != "jwt-token-123" || token2 != "jwt-token-123" {
		t.Errorf("expected token 'jwt-token-123', got %q / %q", token1, token2)
	}
	if calls != 1 {
		t.Errorf("expected 1 HTTP call for two getToken() calls (should be cached), got %d", calls)
	}
}

func TestTokenManager_RefreshesExpiredToken(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(CopilotTokenResponse{
			Token:     "jwt-refreshed",
			ExpiresAt: time.Now().Add(30 * time.Minute).Unix(),
		})
	}))
	defer srv.Close()

	tm := newTestTokenManager("oauth-token", srv.URL)
	// Pre-set an already-expired token.
	tm.apiToken = "stale-jwt"
	tm.expiresAt = time.Now().Add(-5 * time.Minute) // expired

	token, _, err := tm.getToken()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "jwt-refreshed" {
		t.Errorf("expected refreshed token, got %q", token)
	}
	if calls != 1 {
		t.Errorf("expected 1 HTTP call to refresh expired token, got %d", calls)
	}
}

func TestTokenManager_TransportErrorPreservesValidCachedToken(t *testing.T) {
	tm := newCopilotTokenManager("oauth-token", &fasthttp.Client{}, nil)
	tm.tokenExchangeURL = "http://127.0.0.1:1"
	tm.apiToken = "cached-jwt"
	tm.apiBase = "https://api.githubcopilot.com"
	tm.expiresAt = time.Now().Add(5 * time.Minute)

	token, apiBase, err := tm.getToken()
	if err != nil {
		t.Fatalf("expected cached token to be returned on transport failure, got error: %v", err)
	}
	if token != "cached-jwt" {
		t.Errorf("expected cached token, got %q", token)
	}
	if apiBase != "https://api.githubcopilot.com" {
		t.Errorf("expected cached api base to be preserved, got %q", apiBase)
	}
}

func TestTokenManager_NonOKResponseSetsAllowFallbacksFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer srv.Close()

	tm := newTestTokenManager("bad-oauth-token", srv.URL)
	_, _, bifrostErr := tm.getToken()

	if bifrostErr == nil {
		t.Fatal("expected error on non-200 response, got nil")
	}
	if bifrostErr.AllowFallbacks == nil || *bifrostErr.AllowFallbacks != false {
		t.Error("expected AllowFallbacks=false for token exchange failure (revoked token should not fall back)")
	}
	if *bifrostErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", *bifrostErr.StatusCode)
	}
	// Stale JWT must be cleared so the next request retries the exchange.
	if tm.apiToken != "" {
		t.Error("expected stale JWT to be cleared after failed token exchange")
	}
}

func TestTokenManager_ErrorBodyTruncatedAt512Bytes(t *testing.T) {
	bigBody := strings.Repeat("x", 600)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(bigBody))
	}))
	defer srv.Close()

	tm := newTestTokenManager("oauth-token", srv.URL)
	_, _, bifrostErr := tm.getToken()

	if bifrostErr == nil {
		t.Fatal("expected error for non-200 response")
	}
	if strings.Contains(bifrostErr.Error.Message, bigBody) {
		t.Error("full 600-byte body should not appear in error message (truncation expected at 512 bytes)")
	}
	// The truncated body (512 'x' chars) plus the prefix should be present.
	if !strings.Contains(bifrostErr.Error.Message, strings.Repeat("x", 512)) {
		t.Error("expected first 512 bytes of body to be present in error message")
	}
}

func TestTokenManager_EmptyTokenReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(CopilotTokenResponse{Token: "", ExpiresAt: 0})
	}))
	defer srv.Close()

	tm := newTestTokenManager("oauth-token", srv.URL)
	_, _, bifrostErr := tm.getToken()

	if bifrostErr == nil {
		t.Fatal("expected error for empty token in response")
	}
	if *bifrostErr.StatusCode != 401 {
		t.Errorf("expected status 401, got %d", *bifrostErr.StatusCode)
	}
	if !strings.Contains(bifrostErr.Error.Message, "empty token") {
		t.Errorf("expected 'empty token' in error message, got %q", bifrostErr.Error.Message)
	}
}

func TestTokenManager_UntrustedAPIBaseIsIgnored(t *testing.T) {
	untrustedBase := "https://evil.com"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(CopilotTokenResponse{
			Token:     "jwt-token",
			ExpiresAt: time.Now().Add(30 * time.Minute).Unix(),
			Endpoints: CopilotEndpoints{API: untrustedBase},
		})
	}))
	defer srv.Close()

	tm := newTestTokenManager("oauth-token", srv.URL)
	_, apiBase, err := tm.getToken()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if apiBase == untrustedBase {
		t.Error("untrusted API base URL should be rejected; default should be used instead")
	}
	if apiBase != defaultAPIBaseURL {
		t.Errorf("expected default API base %q, got %q", defaultAPIBaseURL, apiBase)
	}
}

func TestTokenManager_ValidAPIBaseIsAccepted(t *testing.T) {
	validBase := "https://api.enterprise.githubcopilot.com"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(CopilotTokenResponse{
			Token:     "jwt-enterprise",
			ExpiresAt: time.Now().Add(30 * time.Minute).Unix(),
			Endpoints: CopilotEndpoints{API: validBase},
		})
	}))
	defer srv.Close()

	tm := newTestTokenManager("oauth-token", srv.URL)
	_, apiBase, err := tm.getToken()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if apiBase != validBase {
		t.Errorf("expected trusted API base %q to be accepted, got %q", validBase, apiBase)
	}
}

func TestTokenManager_BearerHeaderSentWithOAuthToken(t *testing.T) {
	var receivedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(CopilotTokenResponse{
			Token:     "jwt-ok",
			ExpiresAt: time.Now().Add(30 * time.Minute).Unix(),
		})
	}))
	defer srv.Close()

	tm := newTestTokenManager("my-github-oauth-token", srv.URL)
	_, _, err := tm.getToken()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedAuth != "Bearer my-github-oauth-token" {
		t.Errorf("expected Authorization header 'Bearer my-github-oauth-token', got %q", receivedAuth)
	}
}
