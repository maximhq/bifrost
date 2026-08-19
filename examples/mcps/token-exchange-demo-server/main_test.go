package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	josejwt "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/mark3labs/mcp-go/mcp"
)

const (
	testAudience = "api://token-exchange-demo-test"
	testKeyID    = "test-key-1"
)

// testIdP is a minimal fake OIDC identity provider: a discovery document and
// a JWKS endpoint backed by a single RSA key, plus a helper to mint signed
// tokens. Every bearerVerifyMiddleware test case exercises the real
// discovery + JWKS + signature/issuer/audience/expiry verification path
// go-oidc performs — nothing about the verifier itself is mocked.
type testIdP struct {
	server  *httptest.Server
	key     *rsa.PrivateKey
	issuer  string
	signKey *josejwt.SigningKey
}

func newTestIdP(t *testing.T) *testIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate RSA key: %v", err)
	}

	idp := &testIdP{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":   idp.issuer,
			"jwks_uri": idp.issuer + "/jwks",
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(josejwt.JSONWebKeySet{
			Keys: []josejwt.JSONWebKey{
				{Key: &key.PublicKey, KeyID: testKeyID, Algorithm: "RS256", Use: "sig"},
			},
		})
	})
	idp.server = httptest.NewServer(mux)
	t.Cleanup(idp.server.Close)
	idp.issuer = idp.server.URL
	return idp
}

// newVerifier runs real OIDC discovery against idp and returns a verifier
// configured for testAudience, exactly as main() does at startup.
func (idp *testIdP) newVerifier(t *testing.T) *oidc.IDTokenVerifier {
	t.Helper()
	provider, err := oidc.NewProvider(context.Background(), idp.issuer)
	if err != nil {
		t.Fatalf("OIDC discovery failed: %v", err)
	}
	return provider.Verifier(&oidc.Config{ClientID: testAudience})
}

// mintToken signs a JWT with the IdP's private key. Callers override
// individual claims via the claims map; sensible valid defaults are used for
// anything not overridden, so a test case only needs to specify what makes
// it interesting (wrong issuer, wrong audience, expired, etc).
func (idp *testIdP) mintToken(t *testing.T, overrides map[string]any) string {
	t.Helper()
	signer, err := josejwt.NewSigner(josejwt.SigningKey{Algorithm: josejwt.RS256, Key: idp.key}, (&josejwt.SignerOptions{}).WithType("JWT").WithHeader("kid", testKeyID))
	if err != nil {
		t.Fatalf("failed to build signer: %v", err)
	}
	claims := map[string]any{
		"iss":   idp.issuer,
		"aud":   testAudience,
		"sub":   "user-123",
		"email": "user@example.com",
		"name":  "Test User",
		"exp":   time.Now().Add(time.Hour).Unix(),
		"iat":   time.Now().Unix(),
	}
	for k, v := range overrides {
		claims[k] = v
	}
	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatalf("failed to sign token: %v", err)
	}
	return raw
}

// mintTokenWrongKey signs a structurally-valid token with a DIFFERENT RSA
// key than the one published in the IdP's JWKS — simulating a forged or
// tampered signature. Verification must fail on the signature check itself,
// before issuer/audience/expiry are even considered.
func mintTokenWrongKey(t *testing.T, idp *testIdP) string {
	t.Helper()
	wrongKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate wrong RSA key: %v", err)
	}
	signer, err := josejwt.NewSigner(josejwt.SigningKey{Algorithm: josejwt.RS256, Key: wrongKey}, (&josejwt.SignerOptions{}).WithType("JWT").WithHeader("kid", testKeyID))
	if err != nil {
		t.Fatalf("failed to build signer: %v", err)
	}
	claims := map[string]any{
		"iss": idp.issuer,
		"aud": testAudience,
		"sub": "user-123",
		"exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Unix(),
	}
	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatalf("failed to sign token: %v", err)
	}
	return raw
}

// TestBearerVerifyMiddleware covers the server's entire auth boundary: every
// rejection path (missing/malformed header, bad signature, wrong issuer,
// wrong audience, expired) must 401 before the wrapped handler ever runs,
// and a valid token must reach it with callerClaims attached to the request
// context.
func TestBearerVerifyMiddleware(t *testing.T) {
	idp := newTestIdP(t)
	verifier := idp.newVerifier(t)

	tests := []struct {
		name          string
		authHeader    func() string
		wantStatus    int
		wantClaimsSub string
	}{
		{
			name:       "missing Authorization header",
			authHeader: func() string { return "" },
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "malformed scheme (not Bearer)",
			authHeader: func() string { return "Basic " + idp.mintToken(t, nil) },
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "invalid signature (wrong signing key)",
			authHeader: func() string { return "Bearer " + mintTokenWrongKey(t, idp) },
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "wrong issuer",
			authHeader: func() string {
				return "Bearer " + idp.mintToken(t, map[string]any{"iss": "https://not-this-issuer.example"})
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "wrong audience",
			authHeader: func() string {
				return "Bearer " + idp.mintToken(t, map[string]any{"aud": "api://some-other-server"})
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "expired token",
			authHeader: func() string {
				return "Bearer " + idp.mintToken(t, map[string]any{"exp": time.Now().Add(-time.Hour).Unix()})
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:          "valid token propagates claims",
			authHeader:    func() string { return "Bearer " + idp.mintToken(t, nil) },
			wantStatus:    http.StatusOK,
			wantClaimsSub: "user-123",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotClaims *callerClaims
			downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if c, ok := r.Context().Value(claimsCtxKey).(callerClaims); ok {
					gotClaims = &c
				}
				w.WriteHeader(http.StatusOK)
			})
			handler := bearerVerifyMiddleware(verifier)(downstream)

			req := httptest.NewRequest(http.MethodPost, "/", nil)
			if h := tc.authHeader(); h != "" {
				req.Header.Set("Authorization", h)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus == http.StatusOK {
				if gotClaims == nil {
					t.Fatal("downstream handler did not observe claims in context")
				}
				if gotClaims.Subject != tc.wantClaimsSub {
					t.Errorf("claims.Subject = %q, want %q", gotClaims.Subject, tc.wantClaimsSub)
				}
			} else if gotClaims != nil {
				t.Error("downstream handler must not run on a rejected request")
			}
		})
	}
}

// TestWhoamiHandler_ReturnsClaimsFromContext covers the propagation half of
// "claim propagation to both tools": whoamiHandler must surface exactly the
// callerClaims bearerVerifyMiddleware attached to the context, not some
// other identity.
func TestWhoamiHandler_ReturnsClaimsFromContext(t *testing.T) {
	claims := callerClaims{Subject: "user-123", Email: "user@example.com", Issuer: "https://idp.example"}
	ctx := context.WithValue(context.Background(), claimsCtxKey, claims)

	result, err := whoamiHandler(ctx, mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("whoamiHandler returned error: %v", err)
	}
	if result.IsError {
		t.Fatalf("whoamiHandler returned an error result: %+v", result)
	}

	var got callerClaims
	text := result.Content[0].(mcp.TextContent).Text
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("failed to parse whoami result as callerClaims: %v", err)
	}
	if got.Subject != claims.Subject || got.Email != claims.Email {
		t.Errorf("got claims %+v, want %+v", got, claims)
	}
}

// TestWhoamiHandler_NoClaimsInContext covers the defensive branch: if
// bearerVerifyMiddleware somehow didn't run (should be unreachable in
// practice), whoamiHandler must return an error result, not panic or expose
// an empty/zero identity as if it were real.
func TestWhoamiHandler_NoClaimsInContext(t *testing.T) {
	result, err := whoamiHandler(context.Background(), mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("whoamiHandler returned error: %v", err)
	}
	if !result.IsError {
		t.Fatal("whoamiHandler must return an error result when no claims are in context")
	}
}

// TestEchoHandler_TagsMessageWithCallerIdentity covers echoHandler's half of
// claim propagation, including the email-over-subject preference when both
// are present.
func TestEchoHandler_TagsMessageWithCallerIdentity(t *testing.T) {
	tests := []struct {
		name   string
		claims callerClaims
		want   string
	}{
		{
			name:   "prefers email when present",
			claims: callerClaims{Subject: "user-123", Email: "user@example.com"},
			want:   "[as user@example.com] Echo: hello",
		},
		{
			name:   "falls back to subject when email is empty",
			claims: callerClaims{Subject: "user-123"},
			want:   "[as user-123] Echo: hello",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), claimsCtxKey, tc.claims)
			req := mcp.CallToolRequest{}
			req.Params.Arguments = map[string]any{"message": "hello"}

			result, err := echoHandler(ctx, req)
			if err != nil {
				t.Fatalf("echoHandler returned error: %v", err)
			}
			if result.IsError {
				t.Fatalf("echoHandler returned an error result: %+v", result)
			}
			got := result.Content[0].(mcp.TextContent).Text
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
