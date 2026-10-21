package oauth2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/grant"
	"gorm.io/gorm"
)

// fakeIdPResolver implements schemas.TokenExchangeIdPResolver against a test
// server.
type fakeIdPResolver struct {
	idp       *schemas.TokenExchangeIdP
	available bool
	err       error

	// stored answers StoredSubjectToken. After a reported rejection,
	// rejectedErr fails the call when set, else refreshed is returned when
	// set, else stored again (a refresh that cannot help). storedErr fails
	// every call.
	stored      string
	refreshed   string
	storedErr   error
	rejectedErr error
	// storedCalls records the rejected flag of each StoredSubjectToken call,
	// and storedUserID the user the last one was for, both under mu.
	mu           sync.Mutex
	storedCalls  []bool
	storedUserID string
}

func (f *fakeIdPResolver) Available() bool { return f.available }

func (f *fakeIdPResolver) Resolve(ctx context.Context, config *schemas.MCPClientConfig) (*schemas.TokenExchangeIdP, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.idp, nil
}

// StoredSubjectToken answers from the fake's stored-sign-in fields and
// records the call.
func (f *fakeIdPResolver) StoredSubjectToken(_ context.Context, userID string, rejected bool) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.storedCalls = append(f.storedCalls, rejected)
	f.storedUserID = userID
	switch {
	case f.storedErr != nil:
		return "", f.storedErr
	case rejected && f.rejectedErr != nil:
		return "", f.rejectedErr
	case rejected && f.refreshed != "":
		return f.refreshed, nil
	}
	return f.stored, nil
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

// A form-encoded rejection keeps its error and description in Detail, the
// same as a JSON one.
func TestGetExchangedAccessTokenRejected_FormEncodedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-www-form-urlencoded")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`error=invalid_grant&error_description=subject+token+revoked`))
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

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyMCPInboundBearer, "sample-jwt")
	for range 2 {
		response, err := p.ExchangeAdminCredential(ctx, config)
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
	blank := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	blank.SetValue(schemas.BifrostContextKeyMCPInboundBearer, "  ")
	if _, err := p.ExchangeAdminCredential(blank, config); !errors.Is(err, schemas.ErrExchangeSubjectTokenMissing) {
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

func (s *testConfigStore) GetMCPClientByOauthConfigID(_ context.Context, oauthConfigID string) (*tables.TableMCPClient, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, cfg := range s.mcpClients {
		if cfg.OauthConfigID != nil && *cfg.OauthConfigID == oauthConfigID {
			return &tables.TableMCPClient{ClientID: cfg.ID, OauthConfigID: cfg.OauthConfigID, RequirePublicTarget: cfg.RequirePublicTarget}, nil
		}
	}
	return nil, configstore.ErrNotFound
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

// newStoredSubjectProvider is newExchangeProvider with resolver installed, so a
// test can set what StoredSubjectToken answers.
func newStoredSubjectProvider(t *testing.T, tokenURL string, resolver *fakeIdPResolver) *OAuth2Provider {
	t.Helper()
	p := NewOAuth2Provider(nil, nil)
	p.retryBaseDelay = time.Millisecond
	resolver.available = true
	resolver.idp = &schemas.TokenExchangeIdP{TokenEndpoint: tokenURL, GrantShape: schemas.TokenExchangeGrantRFC8693}
	p.SetTokenExchangeIdPResolver(resolver)
	return p
}

// subjectEchoEndpoint mints "exchanged-for:<subject>" with refresh token
// "refresh-for:<subject>" and returns a reader for the subjects it was sent. A
// subject listed in fail is answered with that OAuth error code instead: 503
// for temporarily_unavailable, else 400.
func subjectEchoEndpoint(t *testing.T, fail map[string]string) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var subjects []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		subject := r.PostForm.Get("subject_token")
		mu.Lock()
		subjects = append(subjects, subject)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if code, ok := fail[subject]; ok {
			status := http.StatusBadRequest
			if code == "temporarily_unavailable" {
				status = http.StatusServiceUnavailable
			}
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": "declined by the test"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "exchanged-for:" + subject,
			"refresh_token": "refresh-for:" + subject,
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	}))
	return server, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(subjects)
	}
}

// storedCalls reads the resolver's call record under its lock.
func (f *fakeIdPResolver) calls() []bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.storedCalls)
}

// storedSubjectContext is a request by user-1 that carried no
// identity-provider token, authenticated by a credential of kind as its
// middleware records it.
func storedSubjectContext(kind grant.CredentialKind) *schemas.BifrostContext {
	ctx := userExchangeContext("")
	ctx.SetValue(schemas.BifrostContextKeyAuthCredential, grant.NewCredential(kind, "user-1"))
	return ctx
}

// settleCredential settles credential on ctx's grant, as SettleIdentity and
// the /mcp handler record it.
func settleCredential(ctx *schemas.BifrostContext, credential schemas.Credential) *schemas.BifrostContext {
	g := grant.New()
	g.SetIdentity(grant.NewIdentity(credential, &schemas.UserRef{ID: "user-1"}, nil, nil, nil, nil, nil))
	ctx.SetGrant(g)
	return ctx
}

// storedSubjectGateCase is one request for the stored sign-in gate: whether a
// request that carried no identity-provider token may exchange the stored one.
type storedSubjectGateCase struct {
	name   string
	ctx    func() *schemas.BifrostContext
	stored bool
}

// storedSubjectGateCases are the requests the gate is pinned on, shared by the
// tool-call and admin-verification tests, since both paths apply it.
func storedSubjectGateCases() []storedSubjectGateCase {
	return []storedSubjectGateCase{
		{
			name:   "Edge session",
			ctx:    func() *schemas.BifrostContext { return storedSubjectContext(grant.CredentialEdgeSession) },
			stored: true,
		},
		{
			name: "Edge session with a virtual key alongside",
			ctx: func() *schemas.BifrostContext {
				ctx := storedSubjectContext(grant.CredentialEdgeSession)
				ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-alongside")
				return settleCredential(ctx, grant.NewCredential(grant.CredentialVirtualKey, "sk-bf-alongside"))
			},
			stored: true,
		},
		{
			name:   "dashboard session",
			ctx:    func() *schemas.BifrostContext { return storedSubjectContext(grant.CredentialSessionToken) },
			stored: true,
		},
		{
			name: "/mcp user token",
			ctx: func() *schemas.BifrostContext {
				return settleCredential(userExchangeContext(""), grant.NewCredential(grant.CredentialMCPToken, "user-1"))
			},
			stored: true,
		},
		{
			name: "user replaced after authentication",
			ctx: func() *schemas.BifrostContext {
				ctx := storedSubjectContext(grant.CredentialEdgeSession)
				ctx.SetValue(schemas.BifrostContextKeyUserID, "user-2")
				return ctx
			},
		},
		{
			name: "/mcp user token of another user",
			ctx: func() *schemas.BifrostContext {
				return settleCredential(userExchangeContext(""), grant.NewCredential(grant.CredentialMCPToken, "user-2"))
			},
		},
		{
			name: "virtual key with an owner",
			ctx: func() *schemas.BifrostContext {
				return settleCredential(userExchangeContext(""), grant.NewCredential(grant.CredentialVirtualKey, "sk-bf-owned"))
			},
		},
		{
			name: "identity-provider token that failed validation",
			ctx: func() *schemas.BifrostContext {
				ctx := storedSubjectContext(grant.CredentialEdgeSession)
				ctx.SetValue(schemas.BifrostContextKeyMCPInboundBearerOmitted, schemas.MCPInboundBearerRejected)
				return ctx
			},
		},
		{
			name: "no credential recorded",
			ctx:  func() *schemas.BifrostContext { return userExchangeContext("") },
		},
		{
			name: "virtual-key mode",
			ctx: func() *schemas.BifrostContext {
				ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
				ctx.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyID, "vk-1")
				ctx.SetValue(schemas.BifrostContextKeyAuthCredential, grant.NewCredential(grant.CredentialEdgeSession, "user-1"))
				return ctx
			},
		},
	}
}

// TestGetExchangedAccessTokenStoredSubjectGate pins who may exchange the
// stored sign-in when the request carried no identity-provider token: only a
// request authenticated through that same user's own sign-in. An Edge session
// still qualifies when a virtual key rides along and the grant settles as that
// key; a virtual key alone never does, even with an owner, and a credential
// never exchanges the sign-in of a different user the request was moved to.
func TestGetExchangedAccessTokenStoredSubjectGate(t *testing.T) {
	tests := storedSubjectGateCases()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, subjects := subjectEchoEndpoint(t, nil)
			defer server.Close()
			resolver := &fakeIdPResolver{stored: "stored-access-token"}
			p := newStoredSubjectProvider(t, server.URL, resolver)

			token, err := p.GetExchangedAccessToken(tt.ctx(), exchangeTestClientConfig())
			if !tt.stored {
				if !errors.Is(err, schemas.ErrExchangeSubjectTokenMissing) {
					t.Fatalf("err = %v, want ErrExchangeSubjectTokenMissing", err)
				}
				if len(resolver.calls()) != 0 || len(subjects()) != 0 {
					t.Fatalf("stored calls = %v, exchanged subjects = %v, want neither", resolver.calls(), subjects())
				}
				return
			}
			if err != nil {
				t.Fatalf("GetExchangedAccessToken returned error: %v", err)
			}
			if token != "exchanged-for:stored-access-token" {
				t.Fatalf("token = %q, want the stored sign-in exchanged", token)
			}
			if resolver.storedUserID != "user-1" {
				t.Fatalf("stored sign-in read for %q, want user-1", resolver.storedUserID)
			}
		})
	}
}

// A token on the request wins over the stored sign-in, which is never read.
func TestGetExchangedAccessTokenRequestTokenWinsOverStored(t *testing.T) {
	server, subjects := subjectEchoEndpoint(t, nil)
	defer server.Close()
	resolver := &fakeIdPResolver{stored: "stored-access-token"}
	p := newStoredSubjectProvider(t, server.URL, resolver)

	ctx := storedSubjectContext(grant.CredentialEdgeSession)
	ctx.SetValue(schemas.BifrostContextKeyMCPInboundBearer, "request-jwt")
	if _, err := p.GetExchangedAccessToken(ctx, exchangeTestClientConfig()); err != nil {
		t.Fatalf("GetExchangedAccessToken returned error: %v", err)
	}
	if got := subjects(); !slices.Equal(got, []string{"request-jwt"}) {
		t.Fatalf("exchanged subjects = %v, want only the request's token", got)
	}
	if len(resolver.calls()) != 0 {
		t.Fatalf("stored calls = %v, want none", resolver.calls())
	}
}

// A cached exchange of the stored sign-in is served without reading the stored
// token again, and a force refresh (the tool manager's retry after a 401)
// evicts it and makes a new one from a fresh read.
func TestGetExchangedAccessTokenStoredSubjectCachedAndForceRefreshed(t *testing.T) {
	server, subjects := subjectEchoEndpoint(t, nil)
	defer server.Close()
	resolver := &fakeIdPResolver{stored: "stored-access-token"}
	p := newStoredSubjectProvider(t, server.URL, resolver)
	config := exchangeTestClientConfig()

	for range 3 {
		if _, err := p.GetExchangedAccessToken(storedSubjectContext(grant.CredentialEdgeSession), config); err != nil {
			t.Fatalf("GetExchangedAccessToken returned error: %v", err)
		}
	}
	if len(subjects()) != 1 || len(resolver.calls()) != 1 {
		t.Fatalf("exchanges = %d, stored reads = %d, want 1 each (cached)", len(subjects()), len(resolver.calls()))
	}

	if err := p.ForceRefreshAccessToken(storedSubjectContext(grant.CredentialEdgeSession), config); err != nil {
		t.Fatalf("ForceRefreshAccessToken returned error: %v", err)
	}
	if len(subjects()) != 2 || len(resolver.calls()) != 2 {
		t.Fatalf("exchanges = %d, stored reads = %d, want 2 each (evicted and re-made)", len(subjects()), len(resolver.calls()))
	}
}

// Concurrent calls for one user and MCP client share a single fill: one read
// of the stored sign-in and one exchange.
func TestGetExchangedAccessTokenStoredSubjectConcurrentFill(t *testing.T) {
	server, subjects := subjectEchoEndpoint(t, nil)
	defer server.Close()
	resolver := &fakeIdPResolver{stored: "stored-access-token"}
	p := newStoredSubjectProvider(t, server.URL, resolver)
	config := exchangeTestClientConfig()

	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := p.GetExchangedAccessToken(storedSubjectContext(grant.CredentialEdgeSession), config); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("GetExchangedAccessToken returned error: %v", err)
	}
	if len(subjects()) != 1 || len(resolver.calls()) != 1 {
		t.Fatalf("exchanges = %d, stored reads = %d, want 1 each", len(subjects()), len(resolver.calls()))
	}
}

// When the identity provider declines the stored token as a subject, the
// resolver is asked once whether a refresh helps, and the exchange is retried
// only with a token that changed. A refusal of the exchange client is returned
// without asking.
func TestGetExchangedAccessTokenStoredSubjectRejected(t *testing.T) {
	tests := []struct {
		name         string
		resolver     *fakeIdPResolver
		fail         map[string]string
		wantToken    string
		wantErr      error // nil = a *TokenExchangeRejectedError unless wantToken is set
		wantSubjects []string
		wantCalls    []bool
	}{
		{
			name:         "a refresh cannot help: the rejection is returned",
			resolver:     &fakeIdPResolver{stored: "a1"},
			fail:         map[string]string{"a1": "invalid_grant"},
			wantSubjects: []string{"a1"},
			wantCalls:    []bool{false, true},
		},
		{
			name:         "the refreshed token is exchanged",
			resolver:     &fakeIdPResolver{stored: "a1", refreshed: "a2"},
			fail:         map[string]string{"a1": "invalid_grant"},
			wantToken:    "exchanged-for:a2",
			wantSubjects: []string{"a1", "a2"},
			wantCalls:    []bool{false, true},
		},
		{
			name:         "the refreshed token is declined too",
			resolver:     &fakeIdPResolver{stored: "a1", refreshed: "a2"},
			fail:         map[string]string{"a1": "invalid_grant", "a2": "invalid_grant"},
			wantSubjects: []string{"a1", "a2"},
			wantCalls:    []bool{false, true},
		},
		{
			name:         "only a new sign-in can help",
			resolver:     &fakeIdPResolver{stored: "a1", rejectedErr: schemas.ErrStoredSignInExpired},
			fail:         map[string]string{"a1": "invalid_grant"},
			wantErr:      schemas.ErrStoredSignInExpired,
			wantSubjects: []string{"a1"},
			wantCalls:    []bool{false, true},
		},
		{
			name:         "the exchange client is refused: no refresh is asked for",
			resolver:     &fakeIdPResolver{stored: "a1", refreshed: "a2"},
			fail:         map[string]string{"a1": "invalid_client"},
			wantSubjects: []string{"a1"},
			wantCalls:    []bool{false},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, subjects := subjectEchoEndpoint(t, tt.fail)
			defer server.Close()
			p := newStoredSubjectProvider(t, server.URL, tt.resolver)

			token, err := p.GetExchangedAccessToken(storedSubjectContext(grant.CredentialEdgeSession), exchangeTestClientConfig())
			switch {
			case tt.wantToken != "":
				if err != nil || token != tt.wantToken {
					t.Fatalf("GetExchangedAccessToken = (%q, %v), want (%q, nil)", token, err, tt.wantToken)
				}
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
			default:
				var rejected *schemas.TokenExchangeRejectedError
				if !errors.As(err, &rejected) {
					t.Fatalf("err = %T %v, want *TokenExchangeRejectedError", err, err)
				}
			}
			if got := subjects(); !slices.Equal(got, tt.wantSubjects) {
				t.Fatalf("exchanged subjects = %v, want %v", got, tt.wantSubjects)
			}
			if got := tt.resolver.calls(); !slices.Equal(got, tt.wantCalls) {
				t.Fatalf("stored calls (rejected flag) = %v, want %v", got, tt.wantCalls)
			}
		})
	}
}

// A transient failure of the retried exchange surfaces as a plain error, not
// as a rejection or an expired sign-in, so the caller is not told to sign in
// again over an identity-provider outage.
func TestGetExchangedAccessTokenStoredSubjectRetryTransient(t *testing.T) {
	server, subjects := subjectEchoEndpoint(t, map[string]string{"a1": "invalid_grant", "a2": "temporarily_unavailable"})
	defer server.Close()
	resolver := &fakeIdPResolver{stored: "a1", refreshed: "a2"}
	p := newStoredSubjectProvider(t, server.URL, resolver)

	_, err := p.GetExchangedAccessToken(storedSubjectContext(grant.CredentialEdgeSession), exchangeTestClientConfig())
	var rejected *schemas.TokenExchangeRejectedError
	if err == nil || errors.As(err, &rejected) || errors.Is(err, schemas.ErrStoredSignInExpired) {
		t.Fatalf("err = %T %v, want a plain error", err, err)
	}
	if got := subjects(); len(got) < 2 || got[0] != "a1" || got[len(got)-1] != "a2" {
		t.Fatalf("exchanged subjects = %v, want a1 then a2", got)
	}
}

// A stored sign-in that can no longer produce a token surfaces as
// ErrStoredSignInExpired without an exchange, and is not cached: the next call
// reads the stored sign-in again, so signing in fixes it at once. A resolver
// that returns no token counts the same.
func TestGetExchangedAccessTokenStoredSignInExpired(t *testing.T) {
	tests := []struct {
		name     string
		resolver *fakeIdPResolver
	}{
		{name: "resolver reports it", resolver: &fakeIdPResolver{storedErr: schemas.ErrStoredSignInExpired}},
		{name: "resolver returns no token", resolver: &fakeIdPResolver{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, subjects := subjectEchoEndpoint(t, nil)
			defer server.Close()
			p := newStoredSubjectProvider(t, server.URL, tt.resolver)

			for range 2 {
				_, err := p.GetExchangedAccessToken(storedSubjectContext(grant.CredentialEdgeSession), exchangeTestClientConfig())
				if !errors.Is(err, schemas.ErrStoredSignInExpired) {
					t.Fatalf("err = %v, want ErrStoredSignInExpired", err)
				}
			}
			if len(subjects()) != 0 {
				t.Fatalf("exchanged subjects = %v, want none", subjects())
			}
			if len(tt.resolver.calls()) != 2 {
				t.Fatalf("stored reads = %d, want 2 (failures are not cached)", len(tt.resolver.calls()))
			}
		})
	}
}

// A qualifying request with no resolver available reports the exchange as
// unavailable, the same as a request that carried its own token, and never
// reads the stored sign-in.
func TestGetExchangedAccessTokenStoredSubjectUnavailable(t *testing.T) {
	resolver := &fakeIdPResolver{stored: "stored-access-token"}
	p := NewOAuth2Provider(nil, nil)
	p.SetTokenExchangeIdPResolver(resolver) // available is false

	_, err := p.GetExchangedAccessToken(storedSubjectContext(grant.CredentialEdgeSession), exchangeTestClientConfig())
	if !errors.Is(err, schemas.ErrTokenExchangeUnavailable) {
		t.Fatalf("err = %v, want ErrTokenExchangeUnavailable", err)
	}
	if len(resolver.calls()) != 0 {
		t.Fatalf("stored calls = %v, want none", resolver.calls())
	}
}

// TestExchangeAdminCredentialSubject pins what an admin verification
// exchanges: the request's own token when it carries one, else the admin's
// stored sign-in under the same gate a caller's tool use passes. The full
// response comes back, refresh token included, for the caller to retain.
func TestExchangeAdminCredentialSubject(t *testing.T) {
	type adminSubjectCase struct {
		name        string
		ctx         func() *schemas.BifrostContext
		wantSubject string // "" = refused as having no subject
		wantStored  int
	}
	tests := []adminSubjectCase{{
		name: "request token",
		ctx: func() *schemas.BifrostContext {
			ctx := storedSubjectContext(grant.CredentialSessionToken)
			ctx.SetValue(schemas.BifrostContextKeyMCPInboundBearer, "request-jwt")
			return ctx
		},
		wantSubject: "request-jwt",
	}}
	for _, gate := range storedSubjectGateCases() {
		tt := adminSubjectCase{name: gate.name, ctx: gate.ctx}
		if gate.stored {
			tt.wantSubject, tt.wantStored = "stored-access-token", 1
		}
		tests = append(tests, tt)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, subjects := subjectEchoEndpoint(t, nil)
			defer server.Close()
			resolver := &fakeIdPResolver{stored: "stored-access-token"}
			p := newStoredSubjectProvider(t, server.URL, resolver)

			response, err := p.ExchangeAdminCredential(tt.ctx(), exchangeTestClientConfig())
			if tt.wantSubject == "" {
				if !errors.Is(err, schemas.ErrExchangeSubjectTokenMissing) {
					t.Fatalf("err = %v, want ErrExchangeSubjectTokenMissing", err)
				}
				if len(resolver.calls()) != 0 || len(subjects()) != 0 {
					t.Fatalf("stored calls = %v, exchanged subjects = %v, want neither", resolver.calls(), subjects())
				}
				return
			}
			if err != nil {
				t.Fatalf("ExchangeAdminCredential returned error: %v", err)
			}
			if response.AccessToken != "exchanged-for:"+tt.wantSubject || response.RefreshToken != "refresh-for:"+tt.wantSubject || response.TokenType != "Bearer" || response.ExpiresIn != 3600 {
				t.Fatalf("response = %+v, want the full exchange of %q", response, tt.wantSubject)
			}
			if got := subjects(); !slices.Equal(got, []string{tt.wantSubject}) {
				t.Fatalf("exchanged subjects = %v, want [%s]", got, tt.wantSubject)
			}
			if got := len(resolver.calls()); got != tt.wantStored {
				t.Fatalf("stored reads = %d, want %d", got, tt.wantStored)
			}
		})
	}
}

// An admin verification from the stored sign-in is never cached: each call
// reads the stored sign-in and exchanges it again, as with a request token.
func TestExchangeAdminCredentialStoredSubjectUncached(t *testing.T) {
	server, subjects := subjectEchoEndpoint(t, nil)
	defer server.Close()
	resolver := &fakeIdPResolver{stored: "stored-access-token"}
	p := newStoredSubjectProvider(t, server.URL, resolver)

	for range 2 {
		if _, err := p.ExchangeAdminCredential(storedSubjectContext(grant.CredentialSessionToken), exchangeTestClientConfig()); err != nil {
			t.Fatalf("ExchangeAdminCredential returned error: %v", err)
		}
	}
	if len(subjects()) != 2 || len(resolver.calls()) != 2 {
		t.Fatalf("exchanges = %d, stored reads = %d, want 2 each (never cached)", len(subjects()), len(resolver.calls()))
	}
}

// A stored subject the identity provider declines is retried once with a
// token that changed, as for a caller's tool use; a refusal of the exchange
// client is returned without asking for a refresh.
func TestExchangeAdminCredentialStoredSubjectRejected(t *testing.T) {
	tests := []struct {
		name         string
		resolver     *fakeIdPResolver
		fail         map[string]string
		wantToken    string
		wantErr      error // nil = a *TokenExchangeRejectedError unless wantToken is set
		wantSubjects []string
		wantCalls    []bool
	}{
		{
			name:         "the refreshed token is exchanged",
			resolver:     &fakeIdPResolver{stored: "a1", refreshed: "a2"},
			fail:         map[string]string{"a1": "invalid_grant"},
			wantToken:    "exchanged-for:a2",
			wantSubjects: []string{"a1", "a2"},
			wantCalls:    []bool{false, true},
		},
		{
			name:         "a refresh cannot help: the rejection is returned",
			resolver:     &fakeIdPResolver{stored: "a1"},
			fail:         map[string]string{"a1": "invalid_grant"},
			wantSubjects: []string{"a1"},
			wantCalls:    []bool{false, true},
		},
		{
			name:         "only a new sign-in can help",
			resolver:     &fakeIdPResolver{stored: "a1", rejectedErr: schemas.ErrStoredSignInExpired},
			fail:         map[string]string{"a1": "invalid_grant"},
			wantErr:      schemas.ErrStoredSignInExpired,
			wantSubjects: []string{"a1"},
			wantCalls:    []bool{false, true},
		},
		{
			name:         "the exchange client is refused: no refresh is asked for",
			resolver:     &fakeIdPResolver{stored: "a1", refreshed: "a2"},
			fail:         map[string]string{"a1": "invalid_client"},
			wantSubjects: []string{"a1"},
			wantCalls:    []bool{false},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, subjects := subjectEchoEndpoint(t, tt.fail)
			defer server.Close()
			p := newStoredSubjectProvider(t, server.URL, tt.resolver)

			response, err := p.ExchangeAdminCredential(storedSubjectContext(grant.CredentialSessionToken), exchangeTestClientConfig())
			switch {
			case tt.wantToken != "":
				if err != nil || response.AccessToken != tt.wantToken {
					t.Fatalf("ExchangeAdminCredential = (%+v, %v), want %q", response, err, tt.wantToken)
				}
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
			default:
				var rejected *schemas.TokenExchangeRejectedError
				if !errors.As(err, &rejected) {
					t.Fatalf("err = %T %v, want *TokenExchangeRejectedError", err, err)
				}
			}
			if got := subjects(); !slices.Equal(got, tt.wantSubjects) {
				t.Fatalf("exchanged subjects = %v, want %v", got, tt.wantSubjects)
			}
			if got := tt.resolver.calls(); !slices.Equal(got, tt.wantCalls) {
				t.Fatalf("stored calls (rejected flag) = %v, want %v", got, tt.wantCalls)
			}
		})
	}
}

// A transient failure of the retried admin exchange surfaces as a plain error,
// so the handler answers 422 rather than telling the admin to sign in again.
func TestExchangeAdminCredentialStoredSubjectRetryTransient(t *testing.T) {
	server, subjects := subjectEchoEndpoint(t, map[string]string{"a1": "invalid_grant", "a2": "temporarily_unavailable"})
	defer server.Close()
	p := newStoredSubjectProvider(t, server.URL, &fakeIdPResolver{stored: "a1", refreshed: "a2"})

	_, err := p.ExchangeAdminCredential(storedSubjectContext(grant.CredentialSessionToken), exchangeTestClientConfig())
	var rejected *schemas.TokenExchangeRejectedError
	if err == nil || errors.As(err, &rejected) || errors.Is(err, schemas.ErrStoredSignInExpired) {
		t.Fatalf("err = %T %v, want a plain error", err, err)
	}
	if got := subjects(); len(got) < 2 || got[0] != "a1" || got[len(got)-1] != "a2" {
		t.Fatalf("exchanged subjects = %v, want a1 then a2", got)
	}
}

// With no resolver available, an admin verification from the stored sign-in
// reports the exchange as unavailable and never reads the sign-in.
func TestExchangeAdminCredentialStoredSubjectUnavailable(t *testing.T) {
	resolver := &fakeIdPResolver{stored: "stored-access-token"}
	p := NewOAuth2Provider(nil, nil)
	p.SetTokenExchangeIdPResolver(resolver) // available is false

	_, err := p.ExchangeAdminCredential(storedSubjectContext(grant.CredentialSessionToken), exchangeTestClientConfig())
	if !errors.Is(err, schemas.ErrTokenExchangeUnavailable) {
		t.Fatalf("err = %v, want ErrTokenExchangeUnavailable", err)
	}
	if len(resolver.calls()) != 0 {
		t.Fatalf("stored calls = %v, want none", resolver.calls())
	}
}

// ExchangeAdminCredential answers "no subject" before it checks the config:
// client create relies on that order to put an API-key request's client in
// pending_verification, whatever its exchange config, while a request that has
// a subject is told about a bad config before anything is exchanged or read.
func TestExchangeAdminCredentialChecksSubjectBeforeConfig(t *testing.T) {
	invalid := exchangeTestClientConfig()
	invalid.TokenExchange.ClientID = nil
	tests := []struct {
		name        string
		ctx         func() *schemas.BifrostContext
		wantMissing bool
	}{
		{name: "no subject, as with an API key", ctx: func() *schemas.BifrostContext { return userExchangeContext("") }, wantMissing: true},
		{name: "request token", ctx: func() *schemas.BifrostContext { return userExchangeContext("request-jwt") }},
		{name: "stored sign-in", ctx: func() *schemas.BifrostContext { return storedSubjectContext(grant.CredentialSessionToken) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, subjects := subjectEchoEndpoint(t, nil)
			defer server.Close()
			resolver := &fakeIdPResolver{stored: "stored-access-token"}
			p := newStoredSubjectProvider(t, server.URL, resolver)

			_, err := p.ExchangeAdminCredential(tt.ctx(), invalid)
			if missing := errors.Is(err, schemas.ErrExchangeSubjectTokenMissing); err == nil || missing != tt.wantMissing {
				t.Fatalf("err = %v, want the missing-subject error: %v", err, tt.wantMissing)
			}
			if len(resolver.calls()) != 0 || len(subjects()) != 0 {
				t.Fatalf("stored calls = %v, exchanged subjects = %v, want neither", resolver.calls(), subjects())
			}
		})
	}
}

// A stored sign-in that can no longer produce a token reaches the admin as
// ErrStoredSignInExpired, with no exchange, so the handler can tell them to
// sign in again rather than to send a token.
func TestExchangeAdminCredentialStoredSignInExpired(t *testing.T) {
	tests := []struct {
		name     string
		resolver *fakeIdPResolver
	}{
		{name: "resolver reports it", resolver: &fakeIdPResolver{storedErr: schemas.ErrStoredSignInExpired}},
		{name: "resolver returns no token", resolver: &fakeIdPResolver{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, subjects := subjectEchoEndpoint(t, nil)
			defer server.Close()
			p := newStoredSubjectProvider(t, server.URL, tt.resolver)

			_, err := p.ExchangeAdminCredential(storedSubjectContext(grant.CredentialSessionToken), exchangeTestClientConfig())
			if !errors.Is(err, schemas.ErrStoredSignInExpired) {
				t.Fatalf("err = %v, want ErrStoredSignInExpired", err)
			}
			if len(subjects()) != 0 {
				t.Fatalf("exchanged subjects = %v, want none", subjects())
			}
		})
	}
}
