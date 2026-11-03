package oauth2

import (
	"cmp"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"github.com/golang-jwt/jwt/v5"
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

// validatingIdPResolver is a fakeIdPResolver that also validates token_exchange settings.
type validatingIdPResolver struct {
	fakeIdPResolver
	validateErr error
}

// ValidateTokenExchangeConfig returns the scripted verdict.
func (v *validatingIdPResolver) ValidateTokenExchangeConfig(context.Context, *schemas.MCPTokenExchangeConfig) error {
	return v.validateErr
}

// ValidateTokenExchangeConfig returns the resolver's verdict when it validates, and nil when there is
// no resolver or it does not validate, since there is nothing to check against.
func TestValidateTokenExchangeConfig(t *testing.T) {
	cfg := exchangeTestClientConfig().TokenExchange
	p := NewOAuth2Provider(nil, nil)
	if err := p.ValidateTokenExchangeConfig(context.Background(), cfg); err != nil {
		t.Fatalf("err = %v with no resolver, want nil", err)
	}
	p.SetTokenExchangeIdPResolver(&fakeIdPResolver{available: true})
	if err := p.ValidateTokenExchangeConfig(context.Background(), cfg); err != nil {
		t.Fatalf("err = %v with a resolver that does not validate, want nil", err)
	}
	refused := errors.New("synthetic refusal")
	p.SetTokenExchangeIdPResolver(&validatingIdPResolver{fakeIdPResolver: fakeIdPResolver{available: true}, validateErr: refused})
	if err := p.ValidateTokenExchangeConfig(context.Background(), cfg); !errors.Is(err, refused) {
		t.Fatalf("err = %v, want the resolver's refusal", err)
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

// idjagSubjectResolver is a fakeIdPResolver that also supplies stored SSO subjects.
type idjagSubjectResolver struct {
	fakeIdPResolver
	subject  *schemas.IDJAGSubjectToken
	err      error // returned instead of a subject, e.g. a failed session-store read
	inactive bool  // the user is no longer an active Bifrost user
	calls    atomic.Int64
}

// IsIDJAGUserActive reports the configured liveness.
func (r *idjagSubjectResolver) IsIDJAGUserActive(_ context.Context, _ string) (bool, error) {
	return !r.inactive, nil
}

// IDJAGSubjectForUser returns err, the configured subject, or ErrExchangeSubjectTokenMissing.
func (r *idjagSubjectResolver) IDJAGSubjectForUser(_ context.Context, _ string) (*schemas.IDJAGSubjectToken, error) {
	r.calls.Add(1)
	if r.err != nil {
		return nil, r.err
	}
	if r.subject == nil {
		return nil, schemas.ErrExchangeSubjectTokenMissing
	}
	return r.subject, nil
}

// idjagLegs is a fake identity provider (leg 1) and upstream authorization server (leg 2).
type idjagLegs struct {
	idp, as           *httptest.Server
	idpForm, asForm   url.Values
	idpHits, asHits   atomic.Int64
	issuedTokenType   string
	asStatus          int
	asError           string // OAuth error code of a refusal; default invalid_grant
	asExpiresIn       int
	asGrantTypes      []string
	asAuthMethods     []string // token_endpoint_auth_methods_supported the AS publishes; nil omits it
	asBasicID         string   // client ID the last leg-2 request sent with HTTP Basic
	asBasicSecret     string
	asTokenEndpoint   string   // overrides the token_endpoint the AS publishes
	asIssuer          string   // overrides the issuer the AS publishes
	asNoAuthorize     bool     // the AS publishes no authorization_endpoint (a jwt-bearer-only server)
	prmAuthServers    []string // overrides the authorization servers the MCP server advertises
	duringIdP         func()   // runs while the identity provider is answering leg 1
	upstreamDiscovery *httptest.Server
}

// newIDJAGLegs starts both token endpoints plus an MCP server whose discovery points at the AS.
func newIDJAGLegs(t *testing.T) *idjagLegs {
	t.Helper()
	l := &idjagLegs{issuedTokenType: "urn:ietf:params:oauth:token-type:id-jag", asStatus: http.StatusOK, asExpiresIn: 3600,
		asGrantTypes: []string{"authorization_code", "urn:ietf:params:oauth:grant-type:jwt-bearer"}}
	l.idp = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.idpHits.Add(1)
		_ = r.ParseForm()
		l.idpForm = r.PostForm
		if l.duringIdP != nil {
			l.duringIdP()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "id-jag-jwt", "issued_token_type": l.issuedTokenType, "token_type": "N_A", "expires_in": 300})
	}))
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		l.asHits.Add(1)
		_ = r.ParseForm()
		l.asForm = r.PostForm
		l.asBasicID, l.asBasicSecret, _ = r.BasicAuth()
		w.Header().Set("Content-Type", "application/json")
		if l.asStatus != http.StatusOK {
			w.WriteHeader(l.asStatus)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": cmp.Or(l.asError, "invalid_grant"), "error_description": "user not linked"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "saas-token", "token_type": "Bearer", "expires_in": l.asExpiresIn})
	})
	l.as = httptest.NewServer(mux)
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		doc := map[string]any{"issuer": cmp.Or(l.asIssuer, l.as.URL), "authorization_endpoint": l.as.URL + "/authorize", "token_endpoint": cmp.Or(l.asTokenEndpoint, l.as.URL+"/token"), "grant_types_supported": l.asGrantTypes, "token_endpoint_auth_methods_supported": l.asAuthMethods}
		if l.asNoAuthorize {
			delete(doc, "authorization_endpoint")
		}
		_ = json.NewEncoder(w).Encode(doc)
	})
	upstream := http.NewServeMux()
	upstream.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, _ *http.Request) {
		servers := l.prmAuthServers
		if servers == nil {
			servers = []string{l.as.URL}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"resource": l.upstreamDiscovery.URL + "/mcp", "authorization_servers": servers})
	})
	upstream.HandleFunc("/mcp", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+l.upstreamDiscovery.URL+`/.well-known/oauth-protected-resource"`)
		w.WriteHeader(http.StatusUnauthorized)
	})
	l.upstreamDiscovery = httptest.NewServer(upstream)
	t.Cleanup(func() { l.idp.Close(); l.as.Close(); l.upstreamDiscovery.Close() })
	return l
}

// idjagClientConfig is an id_jag token_exchange client for the fake upstream.
func (l *idjagLegs) idjagClientConfig(withOverride bool) *schemas.MCPClientConfig {
	cfg := &schemas.MCPClientConfig{
		ID: "client-xaa", Name: "Linear", AuthType: schemas.MCPAuthTypeTokenExchange,
		ConnectionString: schemas.NewSecretVar(l.upstreamDiscovery.URL + "/mcp"),
		TokenExchange: &schemas.MCPTokenExchangeConfig{
			Audience: l.as.URL, UseIdPCredentials: true, Profile: schemas.TokenExchangeProfileIDJAG,
			Scopes: []string{"issues.read"}, ResourceClientID: schemas.NewSecretVar("bifrost-at-linear"),
			ResourceClientSecret: schemas.NewSecretVar("linear-secret"),
		},
	}
	if withOverride {
		endpoint := l.as.URL + "/token"
		cfg.TokenExchange.ResourceTokenEndpoint = &endpoint
	}
	return cfg
}

// newIDJAGProvider wires a provider whose IdP is l.idp, with SSO app credentials.
func (l *idjagLegs) newIDJAGProvider(resolver *idjagSubjectResolver) *OAuth2Provider {
	return l.newIDJAGProviderWithStore(resolver, nil)
}

// newIDJAGProviderWithStore is newIDJAGProvider over a config store, for the
// retained admin credential paths.
func (l *idjagLegs) newIDJAGProviderWithStore(resolver *idjagSubjectResolver, store configstore.ConfigStore) *OAuth2Provider {
	p := NewOAuth2Provider(store, nil)
	p.retryBaseDelay = time.Millisecond
	resolver.available = true
	resolver.idp = &schemas.TokenExchangeIdP{TokenEndpoint: l.idp.URL, GrantShape: schemas.TokenExchangeGrantRFC8693, IdPClientID: "sso-app", IdPClientSecret: "sso-secret"}
	p.SetTokenExchangeIdPResolver(resolver)
	return p
}

// idjagUserContext is a user-mode request context carrying an inbound bearer
// that must never reach either leg.
func idjagUserContext() *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyUserID, "user-1")
	ctx.SetValue(schemas.BifrostContextKeyMCPInboundBearer, "inbound-access-token")
	return ctx
}

// subjectResolver is a resolver whose stored session yields token of tokenType.
func subjectResolver(token, tokenType string) *idjagSubjectResolver {
	return &idjagSubjectResolver{subject: &schemas.IDJAGSubjectToken{Token: token, TokenType: tokenType}}
}

func TestIDJAGExchange_BothLegsAndCaching(t *testing.T) {
	l := newIDJAGLegs(t)
	resolver := subjectResolver("okta-refresh", schemas.TokenTypeRefreshToken)
	p := l.newIDJAGProvider(resolver)
	cfg := l.idjagClientConfig(true)
	ctx := idjagUserContext()

	token, err := p.GetExchangedAccessToken(ctx, cfg)
	if err != nil || token != "saas-token" {
		t.Fatalf("token=%q err=%v, want saas-token", token, err)
	}
	for key, want := range map[string]string{
		"grant_type":           "urn:ietf:params:oauth:grant-type:token-exchange",
		"requested_token_type": "urn:ietf:params:oauth:token-type:id-jag",
		"subject_token":        "okta-refresh",
		"subject_token_type":   schemas.TokenTypeRefreshToken,
		"audience":             l.as.URL,
		"scope":                "issues.read",
		"client_id":            "sso-app",
		"client_secret":        "sso-secret",
	} {
		if got := l.idpForm.Get(key); got != want {
			t.Errorf("leg 1 form[%s] = %q, want %q", key, got, want)
		}
	}
	for key, want := range map[string]string{
		"grant_type": "urn:ietf:params:oauth:grant-type:jwt-bearer", "assertion": "id-jag-jwt",
	} {
		if got := l.asForm.Get(key); got != want {
			t.Errorf("leg 2 form[%s] = %q, want %q", key, got, want)
		}
	}
	if l.asBasicID != "bifrost-at-linear" || l.asBasicSecret != "linear-secret" || l.asForm.Has("client_secret") {
		t.Errorf("leg 2 must authenticate with HTTP Basic by default; basic=%q form=%v", l.asBasicID, l.asForm)
	}
	if l.idpForm.Has("resource") {
		t.Error("leg 1 must not send resource unless configured; Okta rejects it without a resource indicator")
	}
	if strings.Contains(l.idpForm.Encode(), "inbound-access-token") || strings.Contains(l.asForm.Encode(), "inbound-access-token") {
		t.Fatal("the inbound access token must never be sent on either leg")
	}

	if _, err := p.GetExchangedAccessToken(ctx, cfg); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if l.idpHits.Load() != 1 || l.asHits.Load() != 1 {
		t.Fatalf("hits idp=%d as=%d, want one each (cached)", l.idpHits.Load(), l.asHits.Load())
	}
	if resolver.calls.Load() != 1 {
		t.Fatalf("subject resolved %d times, want once: a cache hit must not load the stored session", resolver.calls.Load())
	}
}

func TestIDJAGExchange_ReusesGrantUntilExpiry(t *testing.T) {
	l := newIDJAGLegs(t)
	resolver := subjectResolver("okta-refresh", schemas.TokenTypeRefreshToken)
	p := l.newIDJAGProvider(resolver)
	cfg := l.idjagClientConfig(true)
	ctx := idjagUserContext()
	key := idjagGrantKey{userID: "user-1", clientID: cfg.ID}
	call := func(wantIdP, wantAS int64, step string) {
		t.Helper()
		if _, err := p.GetExchangedAccessToken(ctx, cfg); err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		if l.idpHits.Load() != wantIdP || l.asHits.Load() != wantAS {
			t.Fatalf("%s: hits idp=%d as=%d, want idp=%d as=%d", step, l.idpHits.Load(), l.asHits.Load(), wantIdP, wantAS)
		}
	}
	call(1, 1, "first exchange")
	if _, ok := p.idjagGrants.load(key); !ok {
		t.Fatal("the issued ID-JAG must be held for reuse")
	}

	// The upstream token lapsed: the held grant is re-presented and the
	// identity provider is not asked again, nor is the stored session loaded.
	p.userTokens.EvictByBinding(schemas.MCPAuthModeUser, "user-1", cfg.ID)
	call(1, 2, "re-present held grant")
	if resolver.calls.Load() != 1 {
		t.Fatalf("subject resolved %d times, want once: reusing the grant must not load the stored session", resolver.calls.Load())
	}

	// An expired grant is replaced.
	held, _ := p.idjagGrants.load(key)
	held.expiresAt = time.Now().Add(-time.Second)
	p.userTokens.EvictByBinding(schemas.MCPAuthModeUser, "user-1", cfg.ID)
	call(2, 3, "expired grant")

	// A forced refresh after an upstream rejection drops the grant so the
	// identity provider re-evaluates its policy.
	p.EvictExchangedToken(schemas.MCPAuthModeUser, "user-1", cfg.ID)
	call(3, 4, "forced refresh")

	// Client- and user-level evictions drop the grant too.
	p.EvictUserTokensByMCPClient(cfg.ID)
	if _, ok := p.idjagGrants.load(key); ok {
		t.Fatal("client eviction must drop the held grant")
	}
	call(4, 5, "after client eviction")
	p.EvictUserTokensByUser("user-1")
	if _, ok := p.idjagGrants.load(key); ok {
		t.Fatal("user eviction must drop the held grant")
	}
}

func TestIDJAGExchange_RejectedGrantIsReplaced(t *testing.T) {
	l := newIDJAGLegs(t)
	p := l.newIDJAGProvider(subjectResolver("okta-refresh", schemas.TokenTypeRefreshToken))
	cfg := l.idjagClientConfig(true)
	ctx := idjagUserContext()
	key := idjagGrantKey{userID: "user-1", clientID: cfg.ID}
	if _, err := p.GetExchangedAccessToken(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	// The upstream refuses the held grant: one fresh grant is tried, and when
	// that is refused too nothing stays held.
	p.userTokens.EvictByBinding(schemas.MCPAuthModeUser, "user-1", cfg.ID)
	l.asStatus = http.StatusBadRequest
	var rejected *schemas.TokenExchangeRejectedError
	if _, err := p.GetExchangedAccessToken(ctx, cfg); !errors.As(err, &rejected) {
		t.Fatalf("err = %v, want TokenExchangeRejectedError", err)
	}
	if l.idpHits.Load() != 2 || l.asHits.Load() != 3 {
		t.Fatalf("hits idp=%d as=%d, want idp=2 as=3 (held grant refused, one fresh grant tried)", l.idpHits.Load(), l.asHits.Load())
	}
	if _, ok := p.idjagGrants.load(key); ok {
		t.Fatal("a refused grant must not stay held")
	}

	l.asStatus = http.StatusOK
	if _, err := p.GetExchangedAccessToken(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if l.idpHits.Load() != 3 {
		t.Fatalf("idp hits = %d, want 3", l.idpHits.Load())
	}
}

func TestIDJAGExpiry(t *testing.T) {
	now := time.Now()
	if !idjagExpiry("opaque", 0, now).IsZero() {
		t.Fatal("no expires_in and no exp: the grant must not be held")
	}
	fromExpiresIn := idjagExpiry("opaque", 300, now)
	if want := now.Add(300*time.Second - exchangeExpirySafetyMargin); !fromExpiresIn.Equal(want) {
		t.Fatalf("expires_in only: %v, want %v", fromExpiresIn, want)
	}
	exp := now.Add(120 * time.Second).Truncate(time.Second)
	assertion, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"exp": exp.Unix()}).SignedString([]byte("k"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := idjagExpiry(assertion, 300, now), exp.Add(-exchangeExpirySafetyMargin); !got.Equal(want) {
		t.Fatalf("earlier exp wins: %v, want %v", got, want)
	}
}

// TestIDJAGExchange_ClientSecretPostWhenOnlyAdvertised checks leg 2 sends the
// secret in the body only when the upstream's metadata admits that and not
// HTTP Basic, and keeps Basic when both are listed.
func TestIDJAGExchange_ClientSecretPostWhenOnlyAdvertised(t *testing.T) {
	for name, tc := range map[string]struct {
		methods    []string
		configured bool   // resource_token_endpoint set, so there is no discovery
		method     string // resource_client_auth_method
		wantBasic  bool
	}{
		"post only":                             {methods: []string{"client_secret_post"}},
		"basic and post":                        {methods: []string{"client_secret_post", "client_secret_basic"}, wantBasic: true},
		"methods not listed":                    {wantBasic: true},
		"configured endpoint defaults to basic": {configured: true, wantBasic: true},
		"configured endpoint with post":         {configured: true, method: schemas.ResourceClientAuthPost},
		"explicit basic overrides discovery":    {methods: []string{"client_secret_post"}, method: schemas.ResourceClientAuthBasic, wantBasic: true},
	} {
		t.Run(name, func(t *testing.T) {
			l := newIDJAGLegs(t)
			l.asAuthMethods = tc.methods
			p := l.newIDJAGProvider(subjectResolver("t", schemas.TokenTypeIDToken))
			cfg := l.idjagClientConfig(tc.configured)
			cfg.TokenExchange.ResourceClientAuthMethod = tc.method
			if _, err := p.GetExchangedAccessToken(idjagUserContext(), cfg); err != nil {
				t.Fatalf("GetExchangedAccessToken: %v", err)
			}
			if tc.wantBasic {
				if l.asBasicID != "bifrost-at-linear" || l.asForm.Has("client_secret") {
					t.Fatalf("want HTTP Basic; basic=%q form=%v", l.asBasicID, l.asForm)
				}
				return
			}
			if l.asBasicID != "" || l.asForm.Get("client_id") != "bifrost-at-linear" || l.asForm.Get("client_secret") != "linear-secret" {
				t.Fatalf("want the secret in the body; basic=%q form=%v", l.asBasicID, l.asForm)
			}
		})
	}
}

// TestIDJAGGrantCache_BoundedAndExpiring checks held ID-JAGs stay within the
// cache capacity and an expired one is a miss.
func TestIDJAGGrantCache_BoundedAndExpiring(t *testing.T) {
	c := newIDJAGGrantCache(2)
	live := time.Now().Add(time.Minute)
	hold := func(key idjagGrantKey, grant *idjagGrant) {
		t.Helper()
		if _, err := c.issue(context.Background(), key, func() (*idjagGrant, error) { return grant, nil }); err != nil {
			t.Fatalf("issue: %v", err)
		}
	}
	for _, user := range []string{"u1", "u2", "u3"} {
		hold(idjagGrantKey{userID: user, clientID: "c"}, &idjagGrant{assertion: user, expiresAt: live})
	}
	if c.cache.Len() != 2 {
		t.Fatalf("len = %d, want the capacity of 2", c.cache.Len())
	}
	if _, ok := c.load(idjagGrantKey{userID: "u1", clientID: "c"}); ok {
		t.Fatal("the least recently used grant must be evicted")
	}
	hold(idjagGrantKey{userID: "old", clientID: "c"}, &idjagGrant{expiresAt: time.Now().Add(-time.Second)})
	if _, ok := c.load(idjagGrantKey{userID: "old", clientID: "c"}); ok {
		t.Fatal("an expired grant is a miss")
	}
	c.evictWhere(func(k idjagGrantKey) bool { return k.userID == "u3" })
	if _, ok := c.load(idjagGrantKey{userID: "u3", clientID: "c"}); ok {
		t.Fatal("evictWhere must drop matching grants")
	}
}

func TestIDJAGExchange_SendsConfiguredResource(t *testing.T) {
	l := newIDJAGLegs(t)
	p := l.newIDJAGProvider(subjectResolver("t", schemas.TokenTypeIDToken))
	cfg := l.idjagClientConfig(true)
	cfg.TokenExchange.Resource = "https://linear.app/mcp"
	if _, err := p.GetExchangedAccessToken(idjagUserContext(), cfg); err != nil {
		t.Fatalf("GetExchangedAccessToken: %v", err)
	}
	if got := l.idpForm.Get("resource"); got != "https://linear.app/mcp" {
		t.Fatalf("resource = %q, want the configured indicator", got)
	}
}

// TestIDJAGExchange_AlwaysUsesSSOApplication pins leg 1 to the SSO login
// application: the identity provider accepts the user's sign-in token only from
// the app it was issued to, so a dedicated exchange app is never used, and none
// is required.
func TestIDJAGExchange_AlwaysUsesSSOApplication(t *testing.T) {
	l := newIDJAGLegs(t)
	p := l.newIDJAGProvider(subjectResolver("t", schemas.TokenTypeIDToken))
	cfg := l.idjagClientConfig(true)
	cfg.TokenExchange.UseIdPCredentials = false
	cfg.TokenExchange.ClientID = nil
	if _, err := p.GetExchangedAccessToken(idjagUserContext(), cfg); err != nil {
		t.Fatalf("GetExchangedAccessToken without a dedicated app: %v", err)
	}
	if got := l.idpForm.Get("client_id"); got != "sso-app" {
		t.Fatalf("leg-1 client_id = %q, want the SSO application", got)
	}

	l = newIDJAGLegs(t)
	p = l.newIDJAGProvider(subjectResolver("t", schemas.TokenTypeIDToken))
	cfg = l.idjagClientConfig(true)
	cfg.TokenExchange.UseIdPCredentials = false
	cfg.TokenExchange.ClientID = schemas.NewSecretVar("dedicated-app")
	if _, err := p.GetExchangedAccessToken(idjagUserContext(), cfg); err != nil {
		t.Fatalf("GetExchangedAccessToken with a stale dedicated app: %v", err)
	}
	if got := l.idpForm.Get("client_id"); got != "sso-app" {
		t.Fatalf("leg-1 client_id = %q, want the SSO application, not the dedicated one", got)
	}
}

func TestIDJAGExchange_DiscoversUpstreamTokenEndpoint(t *testing.T) {
	l := newIDJAGLegs(t)
	p := l.newIDJAGProvider(subjectResolver("okta-id-token", schemas.TokenTypeIDToken))
	ctx := idjagUserContext()
	if _, err := p.GetExchangedAccessToken(ctx, l.idjagClientConfig(false)); err != nil {
		t.Fatalf("GetExchangedAccessToken: %v", err)
	}
	if l.asHits.Load() != 1 {
		t.Fatalf("discovered token endpoint was not called")
	}

	// Deleting or updating the client drops its cached endpoint, so the next
	// exchange discovers again and sees the upstream no longer offers the grant.
	l.asGrantTypes = []string{"authorization_code"}
	p.EvictUserTokensByMCPClient("client-xaa")
	if _, err := p.GetExchangedAccessToken(ctx, l.idjagClientConfig(false)); err == nil || !strings.Contains(err.Error(), "jwt-bearer") {
		t.Fatalf("err = %v, want rediscovery after the client is evicted", err)
	}

	p2 := l.newIDJAGProvider(subjectResolver("okta-id-token", schemas.TokenTypeIDToken))
	if _, err := p2.GetExchangedAccessToken(ctx, l.idjagClientConfig(false)); err == nil || !strings.Contains(err.Error(), "jwt-bearer") {
		t.Fatalf("err = %v, want a jwt-bearer support error", err)
	}

	// No grant list at all means the default grants only (RFC 8414), not jwt-bearer.
	l.asGrantTypes = nil
	p3 := l.newIDJAGProvider(subjectResolver("okta-id-token", schemas.TokenTypeIDToken))
	if _, err := p3.GetExchangedAccessToken(ctx, l.idjagClientConfig(false)); err == nil || !strings.Contains(err.Error(), "jwt-bearer") {
		t.Fatalf("err = %v, want a jwt-bearer support error for metadata without grant_types_supported", err)
	}
}

// TestIDJAGExchange_RejectsPlainHTTPDiscoveredEndpoint checks a discovered token
// endpoint meets the configured one's rule: the ID-JAG and client secret are
// never posted over plain http to a non-loopback host.
// TestIDJAGExchange_DiscoversFromTheAudience checks the token endpoint comes
// from the audience's own metadata, the server the identity provider addressed
// the ID-JAG to, never from what the MCP server advertises: a server pointing
// at another authorization server receives neither the ID-JAG nor Bifrost's
// client credentials.
func TestIDJAGExchange_DiscoversFromTheAudience(t *testing.T) {
	l := newIDJAGLegs(t)
	var evilHits atomic.Int64
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		evilHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/.well-known/") {
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": "http://" + r.Host, "authorization_endpoint": "http://" + r.Host + "/authorize",
				"token_endpoint": "http://" + r.Host + "/token", "grant_types_supported": []string{"urn:ietf:params:oauth:grant-type:jwt-bearer"}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "stolen", "token_type": "Bearer", "expires_in": 3600})
	}))
	t.Cleanup(evil.Close)
	l.prmAuthServers = []string{evil.URL}
	p := l.newIDJAGProvider(subjectResolver("t", schemas.TokenTypeIDToken))
	token, err := p.GetExchangedAccessToken(idjagUserContext(), l.idjagClientConfig(false))
	if err != nil || token != "saas-token" {
		t.Fatalf("token=%q err=%v, want the audience's own token endpoint used", token, err)
	}
	if evilHits.Load() != 0 {
		t.Fatalf("the server the MCP server advertised was contacted %d times", evilHits.Load())
	}
}

// TestIDJAGExchange_DiscoversJWTBearerOnlyServer checks a server that only
// redeems ID-JAGs, publishing a token endpoint but no authorization endpoint,
// is discovered: the exchange needs no interactive sign-in.
func TestIDJAGExchange_DiscoversJWTBearerOnlyServer(t *testing.T) {
	l := newIDJAGLegs(t)
	l.asNoAuthorize = true
	p := l.newIDJAGProvider(subjectResolver("t", schemas.TokenTypeIDToken))
	if token, err := p.GetExchangedAccessToken(idjagUserContext(), l.idjagClientConfig(false)); err != nil || token != "saas-token" {
		t.Fatalf("token=%q err=%v, want the discovered token endpoint used", token, err)
	}
}

// TestIDJAGExchange_RefusesIssuerMismatch checks metadata naming an issuer
// other than the audience is refused (RFC 8414 §3.3) before anything is sent,
// pointing the admin at resource_token_endpoint.
func TestIDJAGExchange_RefusesIssuerMismatch(t *testing.T) {
	l := newIDJAGLegs(t)
	l.asIssuer = "https://elsewhere.example"
	p := l.newIDJAGProvider(subjectResolver("t", schemas.TokenTypeIDToken))
	_, err := p.GetExchangedAccessToken(idjagUserContext(), l.idjagClientConfig(false))
	if err == nil || !strings.Contains(err.Error(), "resource_token_endpoint") {
		t.Fatalf("err = %v, want an issuer mismatch naming resource_token_endpoint", err)
	}
	if l.asHits.Load() != 0 {
		t.Fatal("nothing may be posted to a token endpoint whose issuer does not match the audience")
	}
}

// TestIDJAGExchange_EvictionDuringIssueWins checks an eviction that lands while
// the identity provider is issuing an ID-JAG (the client deleted, the user
// removed, a forced refresh) keeps that grant from being held afterwards.
func TestIDJAGExchange_EvictionDuringIssueWins(t *testing.T) {
	l := newIDJAGLegs(t)
	p := l.newIDJAGProvider(subjectResolver("t", schemas.TokenTypeIDToken))
	cfg := l.idjagClientConfig(true)
	l.duringIdP = func() { p.EvictUserTokensByMCPClient(cfg.ID) }
	if _, err := p.GetExchangedAccessToken(idjagUserContext(), cfg); err != nil {
		t.Fatalf("GetExchangedAccessToken: %v", err)
	}
	if p.cachedIDJAG(idjagGrantKey{userID: "user-1", clientID: cfg.ID}) != nil {
		t.Fatal("a grant evicted while it was being issued came back")
	}
}

// TestIDJAGAdminCredential_VerificationAsksTheIdP checks verification never
// shares a user request's ID-JAG still in flight: it asks the identity provider
// itself, so it reflects the policy at the time it was run.
func TestIDJAGAdminCredential_VerificationAsksTheIdP(t *testing.T) {
	l := newIDJAGLegs(t)
	p := l.newIDJAGProvider(subjectResolver("admin-id", schemas.TokenTypeIDToken))
	cfg := l.idjagClientConfig(true)
	entered, release := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	l.duringIdP = func() {
		if first.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
	}
	userDone := make(chan error, 1)
	go func() {
		_, err := p.GetExchangedAccessToken(idjagUserContext(), cfg)
		userDone <- err
	}()
	<-entered

	verified := make(chan error, 1)
	go func() {
		_, err := p.ExchangeAdminCredential(idjagUserContext(), cfg)
		verified <- err
	}()
	select {
	case err := <-verified:
		if err != nil {
			t.Fatalf("ExchangeAdminCredential: %v", err)
		}
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("verification waited for a user request's ID-JAG instead of asking the identity provider")
	}
	close(release)
	if err := <-userDone; err != nil {
		t.Fatalf("user exchange: %v", err)
	}
	if l.idpHits.Load() != 2 {
		t.Fatalf("idp hits = %d, want 2: the user request and the verification each ask", l.idpHits.Load())
	}
}

func TestIDJAGExchange_RejectsPlainHTTPDiscoveredEndpoint(t *testing.T) {
	l := newIDJAGLegs(t)
	l.asTokenEndpoint = "http://saas.example.com/token"
	p := l.newIDJAGProvider(subjectResolver("okta-id-token", schemas.TokenTypeIDToken))
	_, err := p.GetExchangedAccessToken(idjagUserContext(), l.idjagClientConfig(false))
	if err == nil || !strings.Contains(err.Error(), "not https") {
		t.Fatalf("err = %v, want a non-https token endpoint refusal", err)
	}
	if l.idpHits.Load() != 0 || l.asHits.Load() != 0 {
		t.Fatalf("hits idp=%d as=%d, want none: the endpoint is checked before any grant is requested", l.idpHits.Load(), l.asHits.Load())
	}
}

func TestIDJAGExchange_SubjectResolution(t *testing.T) {
	l := newIDJAGLegs(t)
	resolver := subjectResolver("stored-refresh", schemas.TokenTypeRefreshToken)
	p := l.newIDJAGProvider(resolver)
	if _, err := p.GetExchangedAccessToken(idjagUserContext(), l.idjagClientConfig(true)); err != nil {
		t.Fatalf("stored-session exchange: %v", err)
	}
	if resolver.calls.Load() != 1 || l.idpForm.Get("subject_token") != "stored-refresh" {
		t.Fatalf("stored session subject was not used")
	}

	empty := l.newIDJAGProvider(&idjagSubjectResolver{})
	if _, err := empty.GetExchangedAccessToken(idjagUserContext(), l.idjagClientConfig(true)); !errors.Is(err, schemas.ErrExchangeSubjectTokenMissing) {
		t.Fatalf("err = %v, want ErrExchangeSubjectTokenMissing", err)
	}

	// Only a user identity can anchor an id_jag exchange; no identity is refused.
	noIdentity := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	if _, err := p.GetExchangedAccessToken(noIdentity, l.idjagClientConfig(true)); !errors.Is(err, schemas.ErrExchangeSubjectTokenMissing) {
		t.Fatalf("no identity: err = %v, want ErrExchangeSubjectTokenMissing", err)
	}
}

func TestIDJAGExchange_Rejections(t *testing.T) {
	t.Run("identity provider issues the wrong token type", func(t *testing.T) {
		l := newIDJAGLegs(t)
		l.issuedTokenType = "urn:ietf:params:oauth:token-type:access_token"
		p := l.newIDJAGProvider(subjectResolver("t", schemas.TokenTypeIDToken))
		_, err := p.GetExchangedAccessToken(idjagUserContext(), l.idjagClientConfig(true))
		if err == nil || l.asHits.Load() != 0 {
			t.Fatalf("err=%v as hits=%d, want refusal before leg 2", err, l.asHits.Load())
		}
	})
	t.Run("upstream refuses the ID-JAG", func(t *testing.T) {
		l := newIDJAGLegs(t)
		l.asStatus = http.StatusBadRequest
		p := l.newIDJAGProvider(subjectResolver("t", schemas.TokenTypeIDToken))
		_, err := p.GetExchangedAccessToken(idjagUserContext(), l.idjagClientConfig(true))
		var rejected *schemas.TokenExchangeRejectedError
		if !errors.As(err, &rejected) || !strings.Contains(rejected.Detail, "user not linked") {
			t.Fatalf("err = %v, want TokenExchangeRejectedError naming the upstream reason", err)
		}
	})
}

func TestIDJAGAdminCredential_RetainsRefreshSubject(t *testing.T) {
	l := newIDJAGLegs(t)
	resolver := subjectResolver("admin-refresh", schemas.TokenTypeRefreshToken)
	p := l.newIDJAGProvider(resolver)
	response, err := p.ExchangeAdminCredential(idjagUserContext(), l.idjagClientConfig(true))
	if err != nil {
		t.Fatalf("ExchangeAdminCredential: %v", err)
	}
	if response.AccessToken != "saas-token" || response.RefreshToken != "admin-refresh" {
		t.Fatalf("response = %+v, want saas-token with the IdP refresh token retained for renewal", response)
	}
	if l.idpForm.Get("subject_token") != "admin-refresh" {
		t.Fatalf("admin exchange must use the admin's IdP subject, not the access token")
	}

	resolver.subject = &schemas.IDJAGSubjectToken{Token: "admin-id", TokenType: schemas.TokenTypeIDToken}
	response, err = p.ExchangeAdminCredential(idjagUserContext(), l.idjagClientConfig(true))
	if err != nil || response.RefreshToken != "" {
		t.Fatalf("an ID-token subject cannot renew; got refresh=%q err=%v", response.RefreshToken, err)
	}
	// A verification always asks the identity provider again, never reusing a held grant.
	if l.idpHits.Load() != 2 {
		t.Fatalf("idp hits = %d, want 2: each admin verification must run leg 1", l.idpHits.Load())
	}
}

func TestIDJAGAdminCredential_RenewsFromLiveSession(t *testing.T) {
	l := newIDJAGLegs(t)
	l.asExpiresIn = 1
	store := newTestConfigStore()
	cfg := l.idjagClientConfig(true)
	store.mcpClients = map[string]*schemas.MCPClientConfig{cfg.ID: cfg}
	resolver := subjectResolver("admin-refresh", schemas.TokenTypeRefreshToken)
	p := l.newIDJAGProviderWithStore(resolver, store)
	ctx := idjagUserContext()

	response, err := p.ExchangeAdminCredential(ctx, cfg)
	if err != nil {
		t.Fatalf("ExchangeAdminCredential: %v", err)
	}
	if err := p.RetainExchangeAdminCredential(ctx, cfg, response); err != nil {
		t.Fatalf("RetainExchangeAdminCredential: %v", err)
	}
	row := func() *tables.TableMCPOauthToken {
		store.mu.Lock()
		defer store.mu.Unlock()
		for _, tok := range store.oauthTokens {
			if tok.AuthMode == "admin" && tok.MCPClientID == cfg.ID {
				return tok
			}
		}
		return nil
	}
	if r := row(); r == nil || r.UserID == nil || *r.UserID != "user-1" || r.RefreshToken != "admin-refresh" {
		t.Fatalf("retained row = %+v, want the verifying admin's user ID and refresh token", r)
	}

	// The admin's session rotated its refresh token: renewal must use the
	// live session, not the retained copy, and keep the row current.
	resolver.subject = &schemas.IDJAGSubjectToken{Token: "rotated-refresh", TokenType: schemas.TokenTypeRefreshToken}
	p.idjagGrants.delete(idjagGrantKey{userID: "user-1", clientID: cfg.ID})
	time.Sleep(1100 * time.Millisecond)
	if token, err := p.GetAdminAccessToken(context.Background(), cfg.ID); err != nil || token != "saas-token" {
		t.Fatalf("renewal: token=%q err=%v", token, err)
	}
	if got := l.idpForm.Get("subject_token"); got != "rotated-refresh" {
		t.Fatalf("renewal subject = %q, want the live session's rotated-refresh", got)
	}
	if r := row(); r.RefreshToken != "rotated-refresh" {
		t.Fatalf("retained refresh token = %q, want it updated to the live one", r.RefreshToken)
	}

	// With no live session the retained copy still renews.
	resolver.subject = nil
	p.idjagGrants.delete(idjagGrantKey{userID: "user-1", clientID: cfg.ID})
	time.Sleep(1100 * time.Millisecond)
	if _, err := p.GetAdminAccessToken(context.Background(), cfg.ID); err != nil {
		t.Fatalf("fallback renewal: %v", err)
	}
	if got := l.idpForm.Get("subject_token"); got != "rotated-refresh" {
		t.Fatalf("fallback subject = %q, want the retained rotated-refresh", got)
	}

	// A failed session lookup is not "no session": the retained copy may be
	// stale, so nothing is sent and the row stays usable for a later retry.
	resolver.err = errors.New("session store unavailable")
	p.idjagGrants.delete(idjagGrantKey{userID: "user-1", clientID: cfg.ID})
	idpHits := l.idpHits.Load()
	time.Sleep(1100 * time.Millisecond)
	if _, err := p.GetAdminAccessToken(context.Background(), cfg.ID); err == nil {
		t.Fatal("renewal must fail while the session lookup fails")
	}
	if l.idpHits.Load() != idpHits {
		t.Fatal("a failed session lookup must not send the retained refresh token")
	}
	if r := row(); r.Status == "needs_reauth" {
		t.Fatal("a failed session lookup must not mark the credential needs_reauth")
	}
	resolver.err = nil
	if _, err := p.GetAdminAccessToken(context.Background(), cfg.ID); err != nil {
		t.Fatalf("renewal after the lookup recovers: %v", err)
	}

	// An admin who is no longer an active Bifrost user renews nothing, even
	// with a retained refresh token the identity provider would still honour.
	resolver.inactive = true
	resolver.subject = nil
	p.idjagGrants.delete(idjagGrantKey{userID: "user-1", clientID: cfg.ID})
	idpHits = l.idpHits.Load()
	time.Sleep(1100 * time.Millisecond)
	if _, err := p.GetAdminAccessToken(context.Background(), cfg.ID); err == nil {
		t.Fatal("renewal must fail once the admin is no longer active")
	}
	if l.idpHits.Load() != idpHits {
		t.Fatal("an inactive admin's retained refresh token must not be sent")
	}
	if r := row(); r.Status != "needs_reauth" {
		t.Fatalf("row status = %q, want needs_reauth so another admin re-verifies", r.Status)
	}
}

// TestIDJAGExchange_PrivateKeyJWTClientAuth checks leg 2 authenticates with a
// client assertion when a resource private key is set: iss and sub are the
// client ID, aud is the token endpoint, kid is carried, and no secret is sent.
func TestIDJAGExchange_PrivateKeyJWTClientAuth(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecDER, err := x509.MarshalECPrivateKey(ecKey)
	if err != nil {
		t.Fatal(err)
	}
	rsaKeyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rsaKey)}))
	for name, tc := range map[string]struct {
		pem, alg, wantAlg string
		pub               any
		audience          string // resource_assertion_audience; empty means the token endpoint
	}{
		"rsa default":        {pem: rsaKeyPEM, wantAlg: "RS256", pub: &rsaKey.PublicKey},
		"ec es256":           {pem: string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: ecDER})), alg: "ES256", wantAlg: "ES256", pub: &ecKey.PublicKey},
		"issuer as audience": {pem: rsaKeyPEM, wantAlg: "RS256", pub: &rsaKey.PublicKey, audience: "https://as.linear.example"},
	} {
		t.Run(name, func(t *testing.T) {
			l := newIDJAGLegs(t)
			p := l.newIDJAGProvider(subjectResolver("t", schemas.TokenTypeIDToken))
			cfg := l.idjagClientConfig(true)
			cfg.TokenExchange.ResourceClientSecret = nil
			cfg.TokenExchange.ResourcePrivateKey = schemas.NewSecretVar(tc.pem)
			cfg.TokenExchange.ResourceKeyID = "k1"
			cfg.TokenExchange.ResourceSigningAlgorithm = tc.alg
			cfg.TokenExchange.ResourceAssertionAudience = tc.audience
			wantAud := cmp.Or(tc.audience, l.as.URL+"/token")
			if token, err := p.GetExchangedAccessToken(idjagUserContext(), cfg); err != nil || token != "saas-token" {
				t.Fatalf("token=%q err=%v", token, err)
			}
			if l.asForm.Has("client_secret") {
				t.Fatal("no client secret is sent with private_key_jwt")
			}
			if got := l.asForm.Get("client_assertion_type"); got != "urn:ietf:params:oauth:client-assertion-type:jwt-bearer" {
				t.Fatalf("client_assertion_type = %q", got)
			}
			assertion, err := jwt.Parse(l.asForm.Get("client_assertion"), func(*jwt.Token) (any, error) { return tc.pub, nil },
				jwt.WithValidMethods([]string{tc.wantAlg}), jwt.WithIssuer("bifrost-at-linear"),
				jwt.WithSubject("bifrost-at-linear"), jwt.WithAudience(wantAud), jwt.WithExpirationRequired())
			if err != nil {
				t.Fatalf("client assertion does not verify: %v", err)
			}
			if assertion.Header["kid"] != "k1" {
				t.Fatalf("kid = %v, want k1", assertion.Header["kid"])
			}
		})
	}

	t.Run("unparseable key", func(t *testing.T) {
		l := newIDJAGLegs(t)
		p := l.newIDJAGProvider(subjectResolver("t", schemas.TokenTypeIDToken))
		cfg := l.idjagClientConfig(true)
		cfg.TokenExchange.ResourceClientSecret = nil
		cfg.TokenExchange.ResourcePrivateKey = schemas.NewSecretVar("not a pem")
		if _, err := p.GetExchangedAccessToken(idjagUserContext(), cfg); err == nil || !strings.Contains(err.Error(), "client assertion") {
			t.Fatalf("err = %v, want a client assertion signing error", err)
		}
		if l.idpHits.Load() != 0 || l.asHits.Load() != 0 {
			t.Fatalf("hits idp=%d as=%d, want none: no ID-JAG is spent when the client assertion cannot be signed", l.idpHits.Load(), l.asHits.Load())
		}
	})
}

// TestIDJAGExchange_CallerSignedInAsThemselves checks an id_jag exchange acts
// only for a user signed in as themselves, as the stored-subject path does: by
// their own IdP token or a credential minted through their own sign-in, never
// a request that merely names the user.
func TestIDJAGExchange_CallerSignedInAsThemselves(t *testing.T) {
	l := newIDJAGLegs(t)
	p := l.newIDJAGProvider(subjectResolver("t", schemas.TokenTypeIDToken))
	cfg := l.idjagClientConfig(true)
	if _, err := p.GetExchangedAccessToken(userExchangeContext(""), cfg); !errors.Is(err, schemas.ErrExchangeSubjectTokenMissing) {
		t.Fatalf("err = %v, want ErrExchangeSubjectTokenMissing for a request not signed in as the user", err)
	}
	if _, err := p.GetExchangedAccessToken(storedSubjectContext(grant.CredentialSessionToken), cfg); err != nil {
		t.Fatalf("a dashboard session of the user qualifies: %v", err)
	}
	if _, err := p.ExchangeAdminCredential(userExchangeContext(""), cfg); !errors.Is(err, schemas.ErrExchangeSubjectTokenMissing) {
		t.Fatalf("err = %v, want ErrExchangeSubjectTokenMissing for an admin not signed in as themselves", err)
	}
}

// TestIDJAGExchange_ClientRefusalKeepsGrant checks a refusal of Bifrost's own
// client credentials at the upstream neither replaces the held ID-JAG nor asks
// the identity provider for another: only a refused grant is worth a new one.
func TestIDJAGExchange_ClientRefusalKeepsGrant(t *testing.T) {
	l := newIDJAGLegs(t)
	p := l.newIDJAGProvider(subjectResolver("okta-refresh", schemas.TokenTypeRefreshToken))
	cfg := l.idjagClientConfig(true)
	ctx := idjagUserContext()
	key := idjagGrantKey{userID: "user-1", clientID: cfg.ID}

	// A fresh grant refused for the client: the ID-JAG stays held.
	l.asStatus, l.asError = http.StatusUnauthorized, "invalid_client"
	var rejected *schemas.TokenExchangeRejectedError
	if _, err := p.GetExchangedAccessToken(ctx, cfg); !errors.As(err, &rejected) || rejected.SubjectRejected {
		t.Fatalf("err = %v, want a client refusal", err)
	}
	if _, ok := p.idjagGrants.load(key); !ok {
		t.Fatal("a client refusal must keep the issued ID-JAG")
	}
	// The held grant refused for the client again: no new ID-JAG is requested.
	if _, err := p.GetExchangedAccessToken(ctx, cfg); err == nil {
		t.Fatal("want the client refusal again")
	}
	if l.idpHits.Load() != 1 {
		t.Fatalf("idp hits = %d, want 1: a client refusal must not spend another ID-JAG", l.idpHits.Load())
	}
	// Once the client credentials are fixed, the held grant redeems.
	l.asStatus, l.asError = http.StatusOK, ""
	if _, err := p.GetExchangedAccessToken(ctx, cfg); err != nil {
		t.Fatalf("after the fix: %v", err)
	}
	if l.idpHits.Load() != 1 {
		t.Fatalf("idp hits = %d, want 1: the held ID-JAG must be reused", l.idpHits.Load())
	}
}

// TestValidateProfile pins the id_jag field rules and the https-or-loopback URL check.
func TestValidateProfile(t *testing.T) {
	base := func() *schemas.MCPTokenExchangeConfig {
		return &schemas.MCPTokenExchangeConfig{Audience: "a", Profile: schemas.TokenExchangeProfileIDJAG, ResourceClientID: schemas.NewSecretVar("c")}
	}
	if err := base().ValidateProfile(); err != nil {
		t.Fatalf("valid id_jag config: %v", err)
	}
	missing := base()
	missing.ResourceClientID = nil
	if missing.ValidateProfile() == nil {
		t.Fatal("id_jag without resource_client_id must fail")
	}
	insecure := base()
	endpoint := "http://saas.example.com/token"
	insecure.ResourceTokenEndpoint = &endpoint
	if insecure.ValidateProfile() == nil {
		t.Fatal("a non-loopback http resource_token_endpoint must fail")
	}
	hostless := base()
	portOnly := "https://:443/token"
	hostless.ResourceTokenEndpoint = &portOnly
	if hostless.ValidateProfile() == nil {
		t.Fatal("a resource_token_endpoint with a port but no host must fail")
	}
	for raw, want := range map[string]bool{
		"https://saas.example.com/token":      true,
		"http://localhost:8080/token":         true,
		"http://127.0.0.2/token":              true,
		"http://[::1]:9000/token":             true,
		"http://127.attacker.example/token":   false,
		"https://user:pass@saas.example.com/": false,
		"ftp://saas.example.com/token":        false,
	} {
		if got := schemas.IsHTTPSOrLoopbackURL(raw); got != want {
			t.Errorf("IsHTTPSOrLoopbackURL(%q) = %v, want %v", raw, got, want)
		}
	}
	keyPEM := schemas.NewSecretVar("-----BEGIN PRIVATE KEY-----x-----END PRIVATE KEY-----")
	both := base()
	both.ResourceClientSecret, both.ResourcePrivateKey = schemas.NewSecretVar("s"), keyPEM
	if both.ValidateProfile() == nil {
		t.Fatal("a resource secret and a resource private key together must fail")
	}
	kidOnly := base()
	kidOnly.ResourceKeyID = "k1"
	if kidOnly.ValidateProfile() == nil {
		t.Fatal("resource_key_id without resource_private_key must fail")
	}
	hmac := base()
	hmac.ResourcePrivateKey, hmac.ResourceSigningAlgorithm = keyPEM, "HS256"
	if hmac.ValidateProfile() == nil {
		t.Fatal("a symmetric resource_signing_algorithm must fail")
	}
	withKey := base()
	withKey.ResourcePrivateKey, withKey.ResourceKeyID, withKey.ResourceSigningAlgorithm = keyPEM, "k1", "ES256"
	withKey.ResourceAssertionAudience = "https://as.example.com"
	if err := withKey.ValidateProfile(); err != nil {
		t.Fatalf("a private key with kid, ES256 and an issuer audience is valid: %v", err)
	}
	for name, mutate := range map[string]func(*schemas.MCPTokenExchangeConfig){
		"audience without a key": func(c *schemas.MCPTokenExchangeConfig) { c.ResourceAssertionAudience = "https://as.example.com" },
		"plain-http audience": func(c *schemas.MCPTokenExchangeConfig) {
			c.ResourcePrivateKey, c.ResourceAssertionAudience = keyPEM, "http://as.example.com"
		},
		"auth method without a secret": func(c *schemas.MCPTokenExchangeConfig) { c.ResourceClientAuthMethod = schemas.ResourceClientAuthPost },
		"auth method with a key": func(c *schemas.MCPTokenExchangeConfig) {
			c.ResourcePrivateKey, c.ResourceClientAuthMethod = keyPEM, schemas.ResourceClientAuthPost
		},
		"unknown auth method": func(c *schemas.MCPTokenExchangeConfig) {
			c.ResourceClientSecret, c.ResourceClientAuthMethod = schemas.NewSecretVar("s"), "private_key_jwt"
		},
	} {
		c := base()
		mutate(c)
		if c.ValidateProfile() == nil {
			t.Errorf("%s must fail validation", name)
		}
	}
	postOK := base()
	postOK.ResourceClientSecret, postOK.ResourceClientAuthMethod = schemas.NewSecretVar("s"), schemas.ResourceClientAuthPost
	if err := postOK.ValidateProfile(); err != nil {
		t.Fatalf("a secret sent with client_secret_post is valid: %v", err)
	}
	unknown := base()
	unknown.Profile = "other"
	if unknown.ValidateProfile() == nil {
		t.Fatal("unknown profile must fail")
	}
	if (&schemas.MCPTokenExchangeConfig{Audience: "a"}).ValidateProfile() != nil {
		t.Fatal("the direct profile needs no id_jag fields")
	}
}
