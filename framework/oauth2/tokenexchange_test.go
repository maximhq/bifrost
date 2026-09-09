package oauth2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
)

// fakeIdPResolver implements schemas.TokenExchangeIdPResolver against a test
// server.
type fakeIdPResolver struct {
	idp       *schemas.TokenExchangeIdP
	available bool
	err       error
}

func (f *fakeIdPResolver) Available() bool { return f.available }

func (f *fakeIdPResolver) Resolve(ctx context.Context, config *schemas.MCPClientConfig) (*schemas.TokenExchangeIdP, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.idp, nil
}

func exchangeTestClientConfig() *schemas.MCPClientConfig {
	return &schemas.MCPClientConfig{
		ID:       "client-1",
		Name:     "Exchange Client",
		AuthType: schemas.MCPAuthTypeTokenExchange,
		TokenExchange: &schemas.MCPTokenExchangeConfig{
			Audience:     "api://client-1",
			ClientID:     schemas.NewSecretVar("exchange-client"),
			ClientSecret: schemas.NewSecretVar("exchange-secret"),
			Scopes:       []string{"read", "write"},
		},
	}
}

// newExchangeProvider builds a provider wired to a resolver pointing at
// tokenURL, with fast retries so failure tests stay quick.
func newExchangeProvider(t *testing.T, tokenURL string, shape schemas.TokenExchangeGrantShape) *OAuth2Provider {
	t.Helper()
	p := NewOAuth2Provider(nil, nil)
	p.retryBaseDelay = time.Millisecond
	p.SetTokenExchangeIdPResolver(&fakeIdPResolver{
		available: true,
		idp: &schemas.TokenExchangeIdP{
			TokenEndpoint: tokenURL,
			GrantShape:    shape,
		},
	})
	return p
}

func userExchangeContext(subjectToken string) *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyUserID, "user-1")
	if subjectToken != "" {
		ctx.SetValue(schemas.BifrostContextKeyMCPInboundBearer, subjectToken)
	}
	return ctx
}

func tokenEndpointStub(t *testing.T, hits *atomic.Int64, capture *url.Values, accessToken string, expiresIn int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Errorf("failed to parse form: %v", err)
		}
		if capture != nil {
			*capture = r.PostForm
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": accessToken,
			"token_type":   "Bearer",
			"expires_in":   expiresIn,
		})
	}))
}

func TestGetExchangedAccessTokenRFC8693Form(t *testing.T) {
	var hits atomic.Int64
	var form url.Values
	server := tokenEndpointStub(t, &hits, &form, "exchanged-1", 3600)
	defer server.Close()

	p := newExchangeProvider(t, server.URL, schemas.TokenExchangeGrantRFC8693)
	token, err := p.GetExchangedAccessToken(userExchangeContext("subject-jwt"), exchangeTestClientConfig())
	if err != nil {
		t.Fatalf("GetExchangedAccessToken returned error: %v", err)
	}
	if token != "exchanged-1" {
		t.Fatalf("token = %q, want exchanged-1", token)
	}

	want := map[string]string{
		"grant_type":         "urn:ietf:params:oauth:grant-type:token-exchange",
		"subject_token":      "subject-jwt",
		"subject_token_type": "urn:ietf:params:oauth:token-type:access_token",
		"audience":           "api://client-1",
		"scope":              "read write",
		"client_id":          "exchange-client",
		"client_secret":      "exchange-secret",
	}
	for key, wantVal := range want {
		if got := form.Get(key); got != wantVal {
			t.Errorf("form[%s] = %q, want %q", key, got, wantVal)
		}
	}
}

func TestGetExchangedAccessTokenJWTBearerOBOForm(t *testing.T) {
	var hits atomic.Int64
	var form url.Values
	server := tokenEndpointStub(t, &hits, &form, "exchanged-2", 3600)
	defer server.Close()

	config := exchangeTestClientConfig()
	config.TokenExchange.Scopes = nil // exercise the audience/.default fallback

	p := newExchangeProvider(t, server.URL, schemas.TokenExchangeGrantJWTBearerOBO)
	if _, err := p.GetExchangedAccessToken(userExchangeContext("subject-jwt"), config); err != nil {
		t.Fatalf("GetExchangedAccessToken returned error: %v", err)
	}

	want := map[string]string{
		"grant_type":          "urn:ietf:params:oauth:grant-type:jwt-bearer",
		"assertion":           "subject-jwt",
		"requested_token_use": "on_behalf_of",
		"scope":               "api://client-1/.default",
	}
	for key, wantVal := range want {
		if got := form.Get(key); got != wantVal {
			t.Errorf("form[%s] = %q, want %q", key, got, wantVal)
		}
	}
	if form.Has("audience") {
		t.Error("jwt-bearer grant shape must not send an audience parameter")
	}
}

// TestGetExchangedAccessTokenJWTBearerOBOOfflineAccessCombinesWithDefault
// pins the one case where a configured scope must combine with, not replace,
// the audience-derived default: "offline_access" alone. Per Microsoft's own
// OBO docs, ".default" cannot be combined with other delegated scopes except
// offline_access — and our own docs recommend adding exactly that scope for
// a self-renewing admin credential, so silently dropping resource access in
// that one case would be a footgun of our own making.
func TestGetExchangedAccessTokenJWTBearerOBOOfflineAccessCombinesWithDefault(t *testing.T) {
	var form url.Values
	server := tokenEndpointStub(t, new(atomic.Int64), &form, "exchanged-offline", 3600)
	defer server.Close()

	config := exchangeTestClientConfig()
	config.TokenExchange.Scopes = []string{"offline_access"}

	p := newExchangeProvider(t, server.URL, schemas.TokenExchangeGrantJWTBearerOBO)
	if _, err := p.GetExchangedAccessToken(userExchangeContext("subject-jwt"), config); err != nil {
		t.Fatalf("GetExchangedAccessToken returned error: %v", err)
	}
	if got, want := form.Get("scope"), "api://client-1/.default offline_access"; got != want {
		t.Errorf("scope = %q, want %q (default combined with offline_access, not replaced)", got, want)
	}
}

// TestGetExchangedAccessTokenJWTBearerOBOCustomScopeReplacesDefault pins the
// opposite case: any scope other than exactly ["offline_access"] means the
// caller opted into naming custom scopes, and the audience-derived default
// must NOT be added alongside it — Microsoft's docs forbid combining
// .default with arbitrary delegated scopes (AADSTS70011).
func TestGetExchangedAccessTokenJWTBearerOBOCustomScopeReplacesDefault(t *testing.T) {
	var form url.Values
	server := tokenEndpointStub(t, new(atomic.Int64), &form, "exchanged-custom", 3600)
	defer server.Close()

	config := exchangeTestClientConfig()
	config.TokenExchange.Scopes = []string{"api://client-1/access_as_user"}

	p := newExchangeProvider(t, server.URL, schemas.TokenExchangeGrantJWTBearerOBO)
	if _, err := p.GetExchangedAccessToken(userExchangeContext("subject-jwt"), config); err != nil {
		t.Fatalf("GetExchangedAccessToken returned error: %v", err)
	}
	if got, want := form.Get("scope"), "api://client-1/access_as_user"; got != want {
		t.Errorf("scope = %q, want %q (custom scope must fully replace the default, not combine with it)", got, want)
	}
}

func TestGetExchangedAccessTokenCachesUntilExpiry(t *testing.T) {
	var hits atomic.Int64
	server := tokenEndpointStub(t, &hits, nil, "exchanged-3", 3600)
	defer server.Close()

	p := newExchangeProvider(t, server.URL, schemas.TokenExchangeGrantRFC8693)
	config := exchangeTestClientConfig()
	for range 3 {
		if _, err := p.GetExchangedAccessToken(userExchangeContext("subject-jwt"), config); err != nil {
			t.Fatalf("GetExchangedAccessToken returned error: %v", err)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("token endpoint hits = %d, want 1 (cached)", got)
	}

	// A distinct identity misses the cache and exchanges again.
	otherCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	otherCtx.SetValue(schemas.BifrostContextKeyUserID, "user-2")
	otherCtx.SetValue(schemas.BifrostContextKeyMCPInboundBearer, "subject-jwt-2")
	if _, err := p.GetExchangedAccessToken(otherCtx, config); err != nil {
		t.Fatalf("GetExchangedAccessToken returned error: %v", err)
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("token endpoint hits = %d, want 2 (per-identity keying)", got)
	}
}

func TestGetExchangedAccessTokenShortExpiryIsMiss(t *testing.T) {
	var hits atomic.Int64
	// expires_in below the safety margin: the margined TTL would be
	// non-positive, so the raw TTL is kept, but the entry still expires
	// almost immediately and the next call re-exchanges.
	server := tokenEndpointStub(t, &hits, nil, "exchanged-4", 1)
	defer server.Close()

	p := newExchangeProvider(t, server.URL, schemas.TokenExchangeGrantRFC8693)
	config := exchangeTestClientConfig()
	if _, err := p.GetExchangedAccessToken(userExchangeContext("subject-jwt"), config); err != nil {
		t.Fatalf("GetExchangedAccessToken returned error: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := p.GetExchangedAccessToken(userExchangeContext("subject-jwt"), config); err != nil {
		t.Fatalf("GetExchangedAccessToken returned error: %v", err)
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("token endpoint hits = %d, want 2 (expired entry is a miss)", got)
	}
}

func TestGetExchangedAccessTokenMissingSubject(t *testing.T) {
	p := newExchangeProvider(t, "http://unused.invalid", schemas.TokenExchangeGrantRFC8693)
	config := exchangeTestClientConfig()

	if _, err := p.GetExchangedAccessToken(userExchangeContext(""), config); !errors.Is(err, schemas.ErrExchangeSubjectTokenMissing) {
		t.Fatalf("err = %v, want ErrExchangeSubjectTokenMissing (no bearer)", err)
	}

	// Session-mode identity never anchors an exchange.
	sessionCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	sessionCtx.SetValue(schemas.BifrostContextKeyMCPSessionID, "sess-1")
	sessionCtx.SetValue(schemas.BifrostContextKeyMCPInboundBearer, "subject-jwt")
	if _, err := p.GetExchangedAccessToken(sessionCtx, config); !errors.Is(err, schemas.ErrExchangeSubjectTokenMissing) {
		t.Fatalf("err = %v, want ErrExchangeSubjectTokenMissing (session mode)", err)
	}
}

func TestGetExchangedAccessTokenUnavailable(t *testing.T) {
	p := NewOAuth2Provider(nil, nil)
	config := exchangeTestClientConfig()
	if _, err := p.GetExchangedAccessToken(userExchangeContext("subject-jwt"), config); !errors.Is(err, schemas.ErrTokenExchangeUnavailable) {
		t.Fatalf("err = %v, want ErrTokenExchangeUnavailable (no resolver)", err)
	}
	if p.TokenExchangeAvailable() {
		t.Fatal("TokenExchangeAvailable = true with no resolver")
	}

	p.SetTokenExchangeIdPResolver(&fakeIdPResolver{available: false})
	if _, err := p.GetExchangedAccessToken(userExchangeContext("subject-jwt"), config); !errors.Is(err, schemas.ErrTokenExchangeUnavailable) {
		t.Fatalf("err = %v, want ErrTokenExchangeUnavailable (unavailable resolver)", err)
	}
}

// vkExchangeContext builds a VK-mode context: a shared virtual key plus a
// caller-specific inbound bearer token, mirroring an on-behalf-of deployment
// where a Virtual Key routes/bills a team or service while each request
// still carries its own caller's identity-provider token to exchange.
func vkExchangeContext(vkID, subjectToken string) *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyID, vkID)
	ctx.SetValue(schemas.BifrostContextKeyMCPInboundBearer, subjectToken)
	return ctx
}

// TestGetExchangedAccessToken_VKMode_DifferentSubjectsDoNotCollide pins the
// security-relevant behavior: two callers sharing the same virtual key but
// presenting different bearer tokens must each get their own exchanged
// upstream token, never one caller's token served to the other. Before
// binding the cache key to a subject-token fingerprint, both calls resolved
// to the identical (mode, identity, mcpClientID) key and the second caller
// would have received the first caller's cached token.
func TestGetExchangedAccessToken_VKMode_DifferentSubjectsDoNotCollide(t *testing.T) {
	var hits atomic.Int64
	var lastSubjectToken atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_ = r.ParseForm()
		subjectToken := r.PostForm.Get("subject_token")
		lastSubjectToken.Store(subjectToken)
		w.Header().Set("Content-Type", "application/json")
		// Echo the subject token's identity into the minted access token so
		// the test can assert which caller's token exchange actually ran.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "exchanged-for:" + subjectToken,
			"token_type":   "Bearer",
			"expires_in":   3600,
		})
	}))
	defer server.Close()

	p := newExchangeProvider(t, server.URL, schemas.TokenExchangeGrantRFC8693)
	config := exchangeTestClientConfig()

	aliceToken, err := p.GetExchangedAccessToken(vkExchangeContext("vk-shared-123", "alice-jwt"), config)
	if err != nil {
		t.Fatalf("alice exchange failed: %v", err)
	}
	bobToken, err := p.GetExchangedAccessToken(vkExchangeContext("vk-shared-123", "bob-jwt"), config)
	if err != nil {
		t.Fatalf("bob exchange failed: %v", err)
	}

	if aliceToken == bobToken {
		t.Fatalf("alice and bob received the same exchanged token %q for the same shared VK with different bearer tokens", aliceToken)
	}
	if aliceToken != "exchanged-for:alice-jwt" {
		t.Errorf("alice's cached token = %q, want exchanged-for:alice-jwt", aliceToken)
	}
	if bobToken != "exchanged-for:bob-jwt" {
		t.Errorf("bob's cached token = %q, want exchanged-for:bob-jwt", bobToken)
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("token endpoint hits = %d, want 2 (each distinct subject token must exchange, not hit the other's cache entry)", got)
	}

	// A repeat call with alice's own token must still hit her cached entry.
	aliceAgain, err := p.GetExchangedAccessToken(vkExchangeContext("vk-shared-123", "alice-jwt"), config)
	if err != nil {
		t.Fatalf("alice repeat exchange failed: %v", err)
	}
	if aliceAgain != aliceToken {
		t.Errorf("alice's repeat call = %q, want cached %q", aliceAgain, aliceToken)
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("token endpoint hits = %d after alice's repeat call, want still 2 (cache hit)", got)
	}
}

func TestGetExchangedAccessTokenRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"subject token revoked"}`))
	}))
	defer server.Close()

	p := newExchangeProvider(t, server.URL, schemas.TokenExchangeGrantRFC8693)
	_, err := p.GetExchangedAccessToken(userExchangeContext("subject-jwt"), exchangeTestClientConfig())

	var rejected *schemas.TokenExchangeRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("expected *TokenExchangeRejectedError, got %T: %v", err, err)
	}
	if want := "invalid_grant: subject token revoked"; rejected.Detail != want {
		t.Fatalf("expected sanitized Detail %q, got %q", want, rejected.Detail)
	}

	// Rejections are never cached: a subsequent call re-attempts.
	if _, err := p.GetExchangedAccessToken(userExchangeContext("subject-jwt"), exchangeTestClientConfig()); err == nil {
		t.Fatal("expected the retry to hit the endpoint and fail again")
	}
}

// TestGetExchangedAccessTokenRejected_NonStandardBodyNotLeaked pins the
// security-relevant behavior: TokenExchangeRejectedError.Detail is
// externally serialized (MCPAuthRequiredError.ExchangeError) and appears in
// a debug log, so a raw identity-provider response that isn't the standard
// {error, error_description} shape must never pass through verbatim.
func TestGetExchangedAccessTokenRejected_NonStandardBodyNotLeaked(t *testing.T) {
	const sensitiveBody = "<html><body>Internal Server Error: db password is hunter2</body></html>"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(sensitiveBody))
	}))
	defer server.Close()

	p := newExchangeProvider(t, server.URL, schemas.TokenExchangeGrantRFC8693)
	_, err := p.GetExchangedAccessToken(userExchangeContext("subject-jwt"), exchangeTestClientConfig())

	var rejected *schemas.TokenExchangeRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("expected *TokenExchangeRejectedError, got %T: %v", err, err)
	}
	if strings.Contains(rejected.Detail, "hunter2") || strings.Contains(rejected.Detail, sensitiveBody) {
		t.Fatalf("raw response body leaked into Detail: %q", rejected.Detail)
	}
}

func TestForceRefreshEvictsAndReExchanges(t *testing.T) {
	var hits atomic.Int64
	server := tokenEndpointStub(t, &hits, nil, "exchanged-5", 3600)
	defer server.Close()

	p := newExchangeProvider(t, server.URL, schemas.TokenExchangeGrantRFC8693)
	config := exchangeTestClientConfig()
	ctx := userExchangeContext("subject-jwt")

	if _, err := p.GetExchangedAccessToken(ctx, config); err != nil {
		t.Fatalf("initial exchange failed: %v", err)
	}
	if err := p.ForceRefreshAccessToken(ctx, config); err != nil {
		t.Fatalf("ForceRefreshAccessToken returned error: %v", err)
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("token endpoint hits = %d, want 2 (evict + re-exchange)", got)
	}
}

func TestExchangeAdminCredentialUncached(t *testing.T) {
	var hits atomic.Int64
	var form url.Values
	server := tokenEndpointStub(t, &hits, &form, "verify-token", 3600)
	defer server.Close()

	p := newExchangeProvider(t, server.URL, schemas.TokenExchangeGrantRFC8693)
	config := exchangeTestClientConfig()

	for range 2 {
		response, err := p.ExchangeAdminCredential(context.Background(), config, "sample-jwt")
		if err != nil {
			t.Fatalf("ExchangeAdminCredential returned error: %v", err)
		}
		if response.AccessToken != "verify-token" {
			t.Fatalf("token = %q, want verify-token", response.AccessToken)
		}
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("token endpoint hits = %d, want 2 (never cached)", got)
	}
	if got := form.Get("grant_type"); got != "urn:ietf:params:oauth:grant-type:token-exchange" {
		t.Fatalf("grant_type = %q, want token-exchange for a subject-token verification", got)
	}

	// No subject token: nothing to exchange — the admin bootstrap is itself
	// a delegated exchange, always.
	if _, err := p.ExchangeAdminCredential(context.Background(), config, "  "); !errors.Is(err, schemas.ErrExchangeSubjectTokenMissing) {
		t.Fatalf("err = %v, want ErrExchangeSubjectTokenMissing for blank subject", err)
	}
}

// exchangeAdminStoreDoubles: the retained-admin-credential lifecycle needs a
// store; reuse testConfigStore with the two extra methods the exchange
// refresh path touches.

func (s *testConfigStore) CreateOauthToken(_ context.Context, token *tables.TableMCPOauthToken, _ ...*gorm.DB) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Mirror the real upsert: one admin row per MCP client.
	for _, existing := range s.oauthTokens {
		if existing.AuthMode == "admin" && token.AuthMode == "admin" && existing.MCPClientID == token.MCPClientID {
			token.ID = existing.ID
			break
		}
	}
	s.oauthTokens[token.ID] = bifrost.Ptr(*token)
	return nil
}

func (s *testConfigStore) GetMCPClientConfigByID(_ context.Context, id string) (*schemas.MCPClientConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mcpClients == nil {
		return nil, nil
	}
	cfg := s.mcpClients[id]
	if cfg == nil {
		return nil, nil
	}
	return cfg, nil
}

func TestRetainAndRefreshExchangeAdminCredential(t *testing.T) {
	var hits atomic.Int64
	var form url.Values
	server := tokenEndpointStub(t, &hits, &form, "minted-1", 3600)
	defer server.Close()

	store := newTestConfigStore()
	config := exchangeTestClientConfig()
	store.mcpClients = map[string]*schemas.MCPClientConfig{config.ID: config}

	p := NewOAuth2Provider(store, nil)
	p.retryBaseDelay = time.Millisecond
	p.SetTokenExchangeIdPResolver(&fakeIdPResolver{
		available: true,
		idp: &schemas.TokenExchangeIdP{
			TokenEndpoint: server.URL,
			GrantShape:    schemas.TokenExchangeGrantRFC8693,
		},
	})

	// Retain an already-expired credential that carries a refresh token, as
	// if verification just ran and time passed: the next admin lookup must
	// renew via the refresh-token grant.
	if err := p.RetainExchangeAdminCredential(context.Background(), config, &schemas.OAuth2TokenExchangeResponse{
		AccessToken:  "retained-1",
		RefreshToken: "refresh-1",
		TokenType:    "Bearer",
		ExpiresIn:    1,
	}); err != nil {
		t.Fatalf("RetainExchangeAdminCredential returned error: %v", err)
	}
	row, err := store.GetAdminOauthTokenByMCPClientID(context.Background(), config.ID)
	if err != nil || row == nil {
		t.Fatalf("expected a retained admin row, got %v / %v", row, err)
	}
	if row.Status != "active" || row.AccessToken != "retained-1" {
		t.Fatalf("retained row = %+v, want active retained-1", row)
	}

	time.Sleep(1100 * time.Millisecond)
	token, err := p.GetAdminAccessToken(context.Background(), config.ID)
	if err != nil {
		t.Fatalf("GetAdminAccessToken returned error: %v", err)
	}
	if token != "minted-1" {
		t.Fatalf("token = %q, want the renewed token", token)
	}
	if got := form.Get("grant_type"); got != "refresh_token" {
		t.Fatalf("grant_type = %q, want refresh_token renewal", got)
	}
	if got := form.Get("refresh_token"); got != "refresh-1" {
		t.Fatalf("refresh_token = %q, want the retained refresh token", got)
	}

	// Without a renewal path (no refresh token), an expired credential flips
	// to needs_reauth and surfaces the expiry sentinel.
	expired := time.Now().Add(-time.Minute)
	row.ExpiresAt = &expired
	row.RefreshToken = ""
	row.AccessToken = "dead"
	store.oauthTokens[row.ID] = row
	if _, err := p.GetAdminAccessToken(context.Background(), config.ID); !errors.Is(err, schemas.ErrOAuth2TokenExpired) {
		t.Fatalf("err = %v, want ErrOAuth2TokenExpired for an unrenewable credential", err)
	}
	reloaded, _ := store.GetAdminOauthTokenByMCPClientID(context.Background(), config.ID)
	if reloaded == nil || reloaded.Status != "needs_reauth" {
		t.Fatalf("row status = %+v, want needs_reauth", reloaded)
	}
}

func TestValidateExchangeConfig(t *testing.T) {
	p := newExchangeProvider(t, "http://unused.invalid", schemas.TokenExchangeGrantRFC8693)

	wrongType := exchangeTestClientConfig()
	wrongType.AuthType = schemas.MCPAuthTypePerUserOauth
	if _, err := p.GetExchangedAccessToken(userExchangeContext("subject-jwt"), wrongType); err == nil {
		t.Fatal("expected error for non-token_exchange auth type")
	}

	noAudience := exchangeTestClientConfig()
	noAudience.TokenExchange = &schemas.MCPTokenExchangeConfig{ClientID: schemas.NewSecretVar("exchange-client")}
	if _, err := p.GetExchangedAccessToken(userExchangeContext("subject-jwt"), noAudience); err == nil {
		t.Fatal("expected error for missing audience")
	}

	noClientID := exchangeTestClientConfig()
	noClientID.TokenExchange.ClientID = nil
	if _, err := p.GetExchangedAccessToken(userExchangeContext("subject-jwt"), noClientID); err == nil {
		t.Fatal("expected error for missing client_id")
	}
}

// useIdPCredentialsClientConfig builds a client that reuses the SSO login
// application's credentials instead of a dedicated exchange app — audience
// only, no client_id/client_secret of its own (validateExchangeConfig allows
// this; the resolved IdP is expected to supply the client ID instead).
func useIdPCredentialsClientConfig() *schemas.MCPClientConfig {
	return &schemas.MCPClientConfig{
		ID:       "client-idp",
		Name:     "IdP-Credentialed Exchange Client",
		AuthType: schemas.MCPAuthTypeTokenExchange,
		TokenExchange: &schemas.MCPTokenExchangeConfig{
			Audience:          "api://client-idp",
			UseIdPCredentials: true,
		},
	}
}

// TestGetExchangedAccessTokenUseIdPCredentialsMissingClientID pins the
// subject-token exchange path (GetExchangedAccessToken -> rawExchange) to a
// clear, actionable error — not a request sent upstream with client_id=
// (empty) — when UseIdPCredentials is set but the resolved IdP has no client
// ID configured.
func TestGetExchangedAccessTokenUseIdPCredentialsMissingClientID(t *testing.T) {
	var hits atomic.Int64
	server := tokenEndpointStub(t, &hits, nil, "unused", 3600)
	defer server.Close()

	p := NewOAuth2Provider(nil, nil)
	p.retryBaseDelay = time.Millisecond
	p.SetTokenExchangeIdPResolver(&fakeIdPResolver{
		available: true,
		idp: &schemas.TokenExchangeIdP{
			TokenEndpoint: server.URL,
			GrantShape:    schemas.TokenExchangeGrantRFC8693,
			// IdPClientID deliberately left empty.
		},
	})

	config := useIdPCredentialsClientConfig()
	if _, err := p.GetExchangedAccessToken(userExchangeContext("subject-jwt"), config); err == nil {
		t.Fatal("expected error when use_idp_credentials is set but the resolved IdP has no client ID")
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("token endpoint hits = %d, want 0 (must fail before sending client_id= empty)", got)
	}
}

// TestRefreshExchangeAdminTokenUseIdPCredentialsMissingClientID mirrors the
// above for the refresh-token renewal path (refreshExchangeAdminToken), the
// other caller of exchangeClientCredentials.
func TestRefreshExchangeAdminTokenUseIdPCredentialsMissingClientID(t *testing.T) {
	var hits atomic.Int64
	server := tokenEndpointStub(t, &hits, nil, "unused", 3600)
	defer server.Close()

	store := newTestConfigStore()
	config := useIdPCredentialsClientConfig()
	store.mcpClients = map[string]*schemas.MCPClientConfig{config.ID: config}

	p := NewOAuth2Provider(store, nil)
	p.retryBaseDelay = time.Millisecond
	p.SetTokenExchangeIdPResolver(&fakeIdPResolver{
		available: true,
		idp: &schemas.TokenExchangeIdP{
			TokenEndpoint: server.URL,
			GrantShape:    schemas.TokenExchangeGrantRFC8693,
			// IdPClientID deliberately left empty.
		},
	})

	if err := p.RetainExchangeAdminCredential(context.Background(), config, &schemas.OAuth2TokenExchangeResponse{
		AccessToken:  "retained-idp",
		RefreshToken: "refresh-idp",
		TokenType:    "Bearer",
		ExpiresIn:    1,
	}); err != nil {
		t.Fatalf("RetainExchangeAdminCredential returned error: %v", err)
	}

	time.Sleep(1100 * time.Millisecond)
	if _, err := p.GetAdminAccessToken(context.Background(), config.ID); err == nil {
		t.Fatal("expected error renewing when use_idp_credentials is set but the resolved IdP has no client ID")
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("token endpoint hits = %d, want 0 (must fail before sending client_id= empty)", got)
	}
}
