package gigachat

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestGigaChatOAuthTokenClient(t *testing.T) {
	t.Parallel()

	t.Run("RequestShapeAndDefaultScope", testGigaChatOAuthRequestShapeAndDefaultScope)
	t.Run("ParsesMillisecondsExpiresAt", testGigaChatOAuthParsesMillisecondsExpiresAt)
	t.Run("CachesTokenBeforeLeeway", testGigaChatOAuthCachesTokenBeforeLeeway)
	t.Run("CacheIncludesCABundle", testGigaChatOAuthCacheIncludesCABundle)
	t.Run("RefreshesTokenInsideLeeway", testGigaChatOAuthRefreshesTokenInsideLeeway)
	t.Run("IgnoresClientCertificate", testGigaChatOAuthIgnoresClientCertificate)
	t.Run("HandlesProviderErrors", testGigaChatOAuthHandlesProviderErrors)
	t.Run("HandlesMalformedResponses", testGigaChatOAuthHandlesMalformedResponses)
	t.Run("MissingCredentials", testGigaChatOAuthMissingCredentials)
	t.Run("ContextCancellation", testGigaChatOAuthContextCancellation)
}

func TestGigaChatPasswordTokenClient(t *testing.T) {
	t.Parallel()

	t.Run("RequestShape", testGigaChatPasswordRequestShape)
	t.Run("ParsesSecondsExpiresAt", testGigaChatPasswordParsesSecondsExpiresAt)
	t.Run("CachesTokenBeforeLeeway", testGigaChatPasswordCachesTokenBeforeLeeway)
	t.Run("CacheIncludesCABundle", testGigaChatPasswordCacheIncludesCABundle)
	t.Run("RefreshesTokenInsideLeeway", testGigaChatPasswordRefreshesTokenInsideLeeway)
	t.Run("IgnoresClientCertificate", testGigaChatPasswordIgnoresClientCertificate)
	t.Run("RejectsExpiredToken", testGigaChatPasswordRejectsExpiredToken)
	t.Run("HandlesProviderErrors", testGigaChatPasswordHandlesProviderErrors)
	t.Run("HandlesMalformedResponses", testGigaChatPasswordHandlesMalformedResponses)
	t.Run("MissingUserPassword", testGigaChatPasswordMissingUserPassword)
	t.Run("AuthPriority", testGigaChatAuthPriority)
}

func TestParseGigaChatExpiresAt(t *testing.T) {
	t.Parallel()

	seconds := int64(1_700_001_800)
	if got := parseGigaChatExpiresAt(seconds); !got.Equal(time.Unix(seconds, 0)) {
		t.Fatalf("seconds expiry mismatch: got %s, want %s", got, time.Unix(seconds, 0))
	}

	milliseconds := seconds * 1000
	if got := parseGigaChatExpiresAt(milliseconds); !got.Equal(time.UnixMilli(milliseconds)) {
		t.Fatalf("milliseconds expiry mismatch: got %s, want %s", got, time.UnixMilli(milliseconds))
	}
}

func TestGigaChatTokenCache(t *testing.T) {
	t.Parallel()

	t.Run("PrunesExpiredEntriesAndKeepsReusableEntries", testGigaChatTokenCachePrunesExpiredEntriesAndKeepsReusableEntries)
	t.Run("ConcurrentAccess", testGigaChatTokenCacheConcurrentAccess)
}

func TestGigaChatTLSCacheKeys(t *testing.T) {
	t.Parallel()

	t.Run("TokenCacheKeysDoNotReadCABundleFiles", testGigaChatTokenCacheKeysDoNotReadCABundleFiles)
}

func testGigaChatTokenCachePrunesExpiredEntriesAndKeepsReusableEntries(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	cache := newGigaChatTokenCache(func() time.Time { return now })
	cache.entries["expired"] = &gigaChatTokenCacheEntry{
		token: gigaChatCachedToken{
			accessToken: "expired-token",
			expiresAt:   now.Add(-time.Second),
		},
	}
	cache.entries["valid"] = &gigaChatTokenCacheEntry{
		token: gigaChatCachedToken{
			accessToken: "valid-token",
			expiresAt:   now.Add(time.Second),
		},
	}
	cache.entries["idle-empty"] = &gigaChatTokenCacheEntry{}
	cache.entries["in-flight-empty"] = &gigaChatTokenCacheEntry{refCount: 1}

	failedEntry := cache.acquireEntry("failed")
	cache.releaseEntry("failed", failedEntry)

	entry := cache.acquireEntry("new")
	entry.mu.Lock()
	entry.token = gigaChatCachedToken{accessToken: "new-token", expiresAt: now.Add(time.Hour)}
	entry.mu.Unlock()
	cache.releaseEntry("new", entry)

	cache.mu.Lock()
	defer cache.mu.Unlock()
	if _, ok := cache.entries["expired"]; ok {
		t.Fatal("expired cache entry was not pruned")
	}
	if _, ok := cache.entries["valid"]; !ok {
		t.Fatal("valid cache entry was pruned")
	}
	if _, ok := cache.entries["idle-empty"]; ok {
		t.Fatal("idle empty cache entry was not pruned")
	}
	if _, ok := cache.entries["in-flight-empty"]; !ok {
		t.Fatal("in-flight empty cache entry was pruned")
	}
	if _, ok := cache.entries["failed"]; ok {
		t.Fatal("failed token exchange cache entry was not released")
	}
	if _, ok := cache.entries["new"]; !ok {
		t.Fatal("requested cache entry was not created")
	}
}

func testGigaChatTokenCacheConcurrentAccess(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	cache := newGigaChatTokenCache(func() time.Time { return now })

	var wg sync.WaitGroup
	for worker := 0; worker < 20; worker++ {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			for iteration := 0; iteration < 50; iteration++ {
				cacheKey := "key-" + strconv.Itoa((worker+iteration)%7)
				entry := cache.acquireEntry(cacheKey)
				entry.mu.Lock()
				entry.token = gigaChatCachedToken{
					accessToken: "token-" + strconv.Itoa(worker),
					expiresAt:   now.Add(time.Hour),
				}
				entry.mu.Unlock()
				cache.releaseEntry(cacheKey, entry)
			}
		}()
	}
	wg.Wait()

	finalEntry := cache.acquireEntry("final")
	cache.releaseEntry("final", finalEntry)
	cache.mu.Lock()
	defer cache.mu.Unlock()
	for cacheKey, entry := range cache.entries {
		entry.mu.Lock()
		valid := entry.token.isValid(now)
		entry.mu.Unlock()
		if !valid {
			t.Fatalf("unexpected stale cache entry after concurrent access: %s", cacheKey)
		}
	}
}

func testGigaChatTokenCacheKeysDoNotReadCABundleFiles(t *testing.T) {
	t.Parallel()

	certPEM, _ := generateGigaChatTestCertificate(t)
	caBundleFile := writeGigaChatTestFile(t, "ca.pem", certPEM)
	keyConfig := &schemas.GigaChatKeyConfig{CABundleFile: caBundleFile}

	oauthConfig := gigaChatOAuthConfig{
		authURL:     "https://auth.example/token",
		credentials: "test-credentials",
		scope:       schemas.DefaultGigaChatScope,
		keyConfig:   keyConfig,
	}
	oauthCacheKey := buildGigaChatOAuthCacheKey(oauthConfig)

	passwordConfig := gigaChatPasswordAuthConfig{
		tokenURL:  "https://api.example/token",
		user:      "test-user",
		password:  "test-password",
		keyConfig: keyConfig,
	}
	passwordCacheKey := buildGigaChatPasswordAuthCacheKey(passwordConfig)

	if err := os.Remove(caBundleFile); err != nil {
		t.Fatalf("failed to remove CA bundle file: %v", err)
	}

	repeatedOAuthCacheKey := buildGigaChatOAuthCacheKey(oauthConfig)
	if repeatedOAuthCacheKey != oauthCacheKey {
		t.Fatalf("OAuth token cache key changed after CA bundle removal: got %q, want %q", repeatedOAuthCacheKey, oauthCacheKey)
	}

	repeatedPasswordCacheKey := buildGigaChatPasswordAuthCacheKey(passwordConfig)
	if repeatedPasswordCacheKey != passwordCacheKey {
		t.Fatalf("password token cache key changed after CA bundle removal: got %q, want %q", repeatedPasswordCacheKey, passwordCacheKey)
	}
}

func TestGigaChatAuthHeaders(t *testing.T) {
	t.Parallel()

	t.Run("ExplicitAccessToken", testGigaChatAuthHeadersExplicitAccessToken)
	t.Run("UserAgentLiteral", testGigaChatAuthHeadersUserAgentLiteral)
	t.Run("KeyValueAccessToken", testGigaChatAuthHeadersKeyValueAccessToken)
	t.Run("TLSOnlyOmitsBearerAuth", testGigaChatAuthHeadersTLSOnlyOmitsBearerAuth)
	t.Run("CABundleOnlyDoesNotAuthenticate", testGigaChatAuthHeadersCABundleOnlyDoesNotAuthenticate)
	t.Run("OAuthToken", testGigaChatAuthHeadersOAuthToken)
	t.Run("BlocksProviderAuthorizationExtraHeader", testGigaChatAuthHeadersBlocksProviderAuthorizationExtraHeader)
	t.Run("RejectsRequestAuthorizationExtraHeader", testGigaChatAuthHeadersRejectsRequestAuthorizationExtraHeader)
	t.Run("PassesContextVars", testGigaChatAuthHeadersPassesContextVars)
	t.Run("ForcedRefreshBypassesCachedOAuthToken", testGigaChatAuthHeadersForcedRefreshBypassesCachedOAuthToken)
	t.Run("ForcedRefreshFallsBackFromExplicitTokenToOAuth", testGigaChatAuthHeadersForcedRefreshFallsBackFromExplicitTokenToOAuth)
}

func testGigaChatAuthHeadersExplicitAccessToken(t *testing.T) {
	t.Parallel()

	provider := newTestGigaChatProvider(t, time.Now)
	headers, bifrostErr := provider.buildAuthHeaders(testBifrostContext(), schemas.Key{
		GigaChatKeyConfig: &schemas.GigaChatKeyConfig{
			AccessToken: schemas.NewSecretVar("explicit-access-token"),
		},
	})
	if bifrostErr != nil {
		t.Fatalf("buildAuthHeaders returned error: %v", bifrostErr)
	}
	assertGigaChatDefaultHeaders(t, headers, "Bearer explicit-access-token")
}

func testGigaChatAuthHeadersUserAgentLiteral(t *testing.T) {
	t.Parallel()

	provider := newTestGigaChatProvider(t, time.Now)
	headers, bifrostErr := provider.buildAuthHeaders(testBifrostContext(), schemas.Key{
		GigaChatKeyConfig: &schemas.GigaChatKeyConfig{
			AccessToken: schemas.NewSecretVar("explicit-access-token"),
		},
	})
	if bifrostErr != nil {
		t.Fatalf("buildAuthHeaders returned error: %v", bifrostErr)
	}
	if got := headers[gigaChatUserAgentHeader]; got != "GigaChat-Bifrost-Provider" {
		t.Fatalf("user-agent header mismatch: got %q, want %q", got, "GigaChat-Bifrost-Provider")
	}
}

func testGigaChatAuthHeadersKeyValueAccessToken(t *testing.T) {
	t.Parallel()

	provider := newTestGigaChatProvider(t, time.Now)
	headers, bifrostErr := provider.buildAuthHeaders(testBifrostContext(), schemas.Key{
		Value: *schemas.NewSecretVar("key-value-access-token"),
	})
	if bifrostErr != nil {
		t.Fatalf("buildAuthHeaders returned error: %v", bifrostErr)
	}
	assertGigaChatDefaultHeaders(t, headers, "Bearer key-value-access-token")
}

func testGigaChatAuthHeadersTLSOnlyOmitsBearerAuth(t *testing.T) {
	t.Parallel()

	provider := newTestGigaChatProvider(t, time.Now)
	headers, bifrostErr := provider.buildAuthHeaders(testBifrostContext(), schemas.Key{
		GigaChatKeyConfig: &schemas.GigaChatKeyConfig{
			CertFile:     "/secure/client.pem",
			KeyFile:      "/secure/client.key",
			CABundleFile: "/secure/ca.pem",
		},
	})
	if bifrostErr != nil {
		t.Fatalf("buildAuthHeaders returned error: %v", bifrostErr)
	}
	assertGigaChatDefaultHeadersWithoutAuthorization(t, headers)
}

func testGigaChatAuthHeadersCABundleOnlyDoesNotAuthenticate(t *testing.T) {
	t.Parallel()

	provider := newTestGigaChatProvider(t, time.Now)
	_, bifrostErr := provider.buildAuthHeaders(testBifrostContext(), schemas.Key{
		GigaChatKeyConfig: &schemas.GigaChatKeyConfig{
			CABundleFile: "/secure/ca.pem",
		},
	})
	if bifrostErr == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(bifrostErr.GetErrorString(), "mTLS cert_file/key_file") {
		t.Fatalf("unexpected error: %v", bifrostErr)
	}
}

func testGigaChatAuthHeadersOAuthToken(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"oauth-access-token","expires_at":` + formatUnix(now.Add(30*time.Minute)) + `}`))
	}))
	defer server.Close()

	provider := newTestGigaChatProvider(t, func() time.Time { return now })
	headers, bifrostErr := provider.buildAuthHeaders(testBifrostContext(), testGigaChatOAuthKey(server.URL, "", "test-credentials"))
	if bifrostErr != nil {
		t.Fatalf("buildAuthHeaders returned error: %v", bifrostErr)
	}
	assertGigaChatDefaultHeaders(t, headers, "Bearer oauth-access-token")
}

func testGigaChatAuthHeadersBlocksProviderAuthorizationExtraHeader(t *testing.T) {
	t.Parallel()

	provider := newTestGigaChatProvider(t, time.Now)
	provider.networkConfig.ExtraHeaders = map[string]string{
		"authorization": "Bearer provider-authorization-token",
	}

	_, bifrostErr := provider.buildAuthHeaders(testBifrostContext(), schemas.Key{
		GigaChatKeyConfig: &schemas.GigaChatKeyConfig{
			AccessToken: schemas.NewSecretVar("explicit-access-token"),
		},
	})
	if bifrostErr == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(bifrostErr.GetErrorString(), "extra_headers") {
		t.Fatalf("unexpected error: %v", bifrostErr)
	}
	if strings.Contains(bifrostErr.GetErrorString(), "request extra headers") {
		t.Fatalf("unexpected request header bypass hint: %v", bifrostErr)
	}
	assertNoGigaChatSecretLeak(t, bifrostErr.String())
}

func testGigaChatAuthHeadersRejectsRequestAuthorizationExtraHeader(t *testing.T) {
	t.Parallel()

	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"oauth-access-token","expires_at":1893456000}`))
	}))
	defer server.Close()

	ctx := testBifrostContext()
	ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{
		"Authorization": {"Bearer context-authorization-token"},
	})

	provider := newTestGigaChatProvider(t, time.Now)
	_, bifrostErr := provider.buildAuthHeaders(ctx, testGigaChatOAuthKey(server.URL, "", "test-credentials"))
	if bifrostErr == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(bifrostErr.GetErrorString(), "request extra headers cannot include Authorization") {
		t.Fatalf("unexpected error: %v", bifrostErr)
	}
	if requestCount.Load() != 0 {
		t.Fatalf("request count mismatch: got %d, want 0", requestCount.Load())
	}
	assertNoGigaChatSecretLeak(t, bifrostErr.String())
}

func testGigaChatAuthHeadersPassesContextVars(t *testing.T) {
	t.Parallel()

	ctx := testBifrostContext()
	ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{
		"X-Session-ID":   {"session-id"},
		"X-Request-ID":   {"request-id"},
		"X-Service-ID":   {"service-id"},
		"X-Operation-ID": {"operation-id"},
		"X-Client-ID":    {"client-id"},
		"X-Trace-ID":     {"trace-id"},
		"X-Agent-ID":     {"agent-id"},
		"X-Ignored-ID":   {"ignored-id"},
	})

	provider := newTestGigaChatProvider(t, time.Now)
	provider.networkConfig.ExtraHeaders = map[string]string{
		"X-Service-ID": "provider-service-id",
	}

	headers, bifrostErr := provider.buildAuthHeaders(ctx, schemas.Key{
		GigaChatKeyConfig: &schemas.GigaChatKeyConfig{
			AccessToken: schemas.NewSecretVar("explicit-access-token"),
		},
	})
	if bifrostErr != nil {
		t.Fatalf("buildAuthHeaders returned error: %v", bifrostErr)
	}

	assertGigaChatDefaultHeaders(t, headers, "Bearer explicit-access-token")
	expectedHeaders := map[string]string{
		"X-Session-ID":   "session-id",
		"X-Request-ID":   "request-id",
		"X-Service-ID":   "service-id",
		"X-Operation-ID": "operation-id",
		"X-Client-ID":    "client-id",
		"X-Trace-ID":     "trace-id",
		"X-Agent-ID":     "agent-id",
	}
	for key, want := range expectedHeaders {
		if got := headers[key]; got != want {
			t.Fatalf("%s mismatch: got %q, want %q", key, got, want)
		}
	}
	if _, ok := headers["X-Ignored-ID"]; ok {
		t.Fatalf("unexpected ignored header: %v", headers)
	}
}

func testGigaChatAuthHeadersForcedRefreshBypassesCachedOAuthToken(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count := requestCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"oauth-refresh-token-` + formatInt32(count) + `","expires_at":` + formatUnix(now.Add(30*time.Minute)) + `}`))
	}))
	defer server.Close()

	provider := newTestGigaChatProvider(t, func() time.Time { return now })
	key := testGigaChatOAuthKey(server.URL, "", "test-credentials")

	headers, bifrostErr := provider.buildAuthHeaders(testBifrostContext(), key)
	if bifrostErr != nil {
		t.Fatalf("buildAuthHeaders returned error: %v", bifrostErr)
	}
	assertGigaChatDefaultHeaders(t, headers, "Bearer oauth-refresh-token-1")

	headers, bifrostErr = provider.refreshAuthHeaders(testBifrostContext(), key)
	if bifrostErr != nil {
		t.Fatalf("refreshAuthHeaders returned error: %v", bifrostErr)
	}
	assertGigaChatDefaultHeaders(t, headers, "Bearer oauth-refresh-token-2")
	if requestCount.Load() != 2 {
		t.Fatalf("request count mismatch: got %d, want 2", requestCount.Load())
	}
}

func testGigaChatAuthHeadersForcedRefreshFallsBackFromExplicitTokenToOAuth(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"oauth-refreshed-token","expires_at":` + formatUnix(now.Add(30*time.Minute)) + `}`))
	}))
	defer server.Close()

	provider := newTestGigaChatProvider(t, func() time.Time { return now })
	key := schemas.Key{
		GigaChatKeyConfig: &schemas.GigaChatKeyConfig{
			AccessToken: schemas.NewSecretVar("explicit-access-token"),
			Credentials: schemas.NewSecretVar("test-credentials"),
			AuthURL:     server.URL,
		},
	}

	headers, bifrostErr := provider.buildAuthHeaders(testBifrostContext(), key)
	if bifrostErr != nil {
		t.Fatalf("buildAuthHeaders returned error: %v", bifrostErr)
	}
	assertGigaChatDefaultHeaders(t, headers, "Bearer explicit-access-token")
	if requestCount.Load() != 0 {
		t.Fatalf("request count mismatch before refresh: got %d, want 0", requestCount.Load())
	}

	headers, bifrostErr = provider.refreshAuthHeaders(testBifrostContext(), key)
	if bifrostErr != nil {
		t.Fatalf("refreshAuthHeaders returned error: %v", bifrostErr)
	}
	assertGigaChatDefaultHeaders(t, headers, "Bearer oauth-refreshed-token")
	if requestCount.Load() != 1 {
		t.Fatalf("request count mismatch after refresh: got %d, want 1", requestCount.Load())
	}
}

func testGigaChatOAuthRequestShapeAndDefaultScope(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method mismatch: got %s", r.Method)
		}
		if r.URL.Path != "/api/v2/oauth" {
			t.Errorf("path mismatch: got %s", r.URL.Path)
		}
		if contentType := r.Header.Get("Content-Type"); !strings.Contains(contentType, "application/x-www-form-urlencoded") {
			t.Errorf("content type mismatch: got %q", contentType)
		}
		if accept := r.Header.Get("Accept"); accept != "application/json" {
			t.Errorf("accept mismatch: got %q", accept)
		}
		if userAgent := r.Header.Get("User-Agent"); userAgent != gigaChatUserAgent {
			t.Errorf("user-agent mismatch: got %q", userAgent)
		}
		if auth := r.Header.Get("Authorization"); auth != "Basic test-credentials" {
			t.Errorf("authorization mismatch: got %q", auth)
		}
		requestID := r.Header.Get("RqUID")
		parsedRequestID, err := uuid.Parse(requestID)
		if err != nil {
			t.Errorf("RqUID is not a UUID: %q", requestID)
		} else if parsedRequestID.Version() != 4 {
			t.Errorf("RqUID version mismatch: got %d", parsedRequestID.Version())
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}
		values, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatalf("failed to parse request body: %v", err)
		}
		if scope := values.Get("scope"); scope != schemas.DefaultGigaChatScope {
			t.Errorf("scope mismatch: got %q, want %q", scope, schemas.DefaultGigaChatScope)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"token-1","expires_at":` + formatUnix(now.Add(30*time.Minute)) + `}`))
	}))
	defer server.Close()

	provider := newTestGigaChatProvider(t, func() time.Time { return now })
	token, bifrostErr := provider.getOAuthAccessToken(testBifrostContext(), testGigaChatOAuthKey(server.URL+"/api/v2/oauth", "", "test-credentials"))
	if bifrostErr != nil {
		t.Fatalf("getOAuthAccessToken returned error: %v", bifrostErr)
	}
	if token != "token-1" {
		t.Fatalf("token mismatch: got %q", token)
	}
}

func testGigaChatPasswordRequestShape(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method mismatch: got %s", r.Method)
		}
		if r.URL.Path != "/api/v1/token" {
			t.Errorf("path mismatch: got %s", r.URL.Path)
		}
		if contentType := r.Header.Get("Content-Type"); !strings.Contains(contentType, "application/x-www-form-urlencoded") {
			t.Errorf("content type mismatch: got %q", contentType)
		}
		if accept := r.Header.Get("Accept"); accept != "application/json" {
			t.Errorf("accept mismatch: got %q", accept)
		}
		if userAgent := r.Header.Get("User-Agent"); userAgent != gigaChatUserAgent {
			t.Errorf("user-agent mismatch: got %q", userAgent)
		}
		wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("test-user:test-password"))
		if auth := r.Header.Get("Authorization"); auth != wantAuth {
			t.Errorf("authorization mismatch: got %q", auth)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}
		if len(body) != 0 {
			t.Errorf("body mismatch: got %q, want empty body", string(body))
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tok":"password-token-1","exp":` + formatUnixMilli(now.Add(30*time.Minute)) + `}`))
	}))
	defer server.Close()

	provider := newTestGigaChatProvider(t, func() time.Time { return now })
	token, bifrostErr := provider.getPasswordAccessToken(testBifrostContext(), testGigaChatPasswordKey(server.URL+"/api", "test-user", "test-password"))
	if bifrostErr != nil {
		t.Fatalf("getPasswordAccessToken returned error: %v", bifrostErr)
	}
	if token != "password-token-1" {
		t.Fatalf("token mismatch: got %q", token)
	}
}

func testGigaChatOAuthIgnoresClientCertificate(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	server, caBundleFile, certFile, keyFile := newGigaChatClientCertRequestingServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/oauth" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.TLS != nil && len(r.TLS.PeerCertificates) != 0 {
			t.Error("OAuth token request should not include client certificate")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"oauth-token","expires_at":` + formatUnix(now.Add(30*time.Minute)) + `}`))
	}))

	provider := newTestGigaChatProvider(t, func() time.Time { return now })
	key := testGigaChatOAuthKey(server.URL+"/api/v2/oauth", "", "test-credentials")
	key.GigaChatKeyConfig.CABundleFile = caBundleFile
	key.GigaChatKeyConfig.CertFile = certFile
	key.GigaChatKeyConfig.KeyFile = keyFile

	token, bifrostErr := provider.getOAuthAccessToken(testBifrostContext(), key)
	if bifrostErr != nil {
		t.Fatalf("getOAuthAccessToken returned error: %v", bifrostErr)
	}
	if token != "oauth-token" {
		t.Fatalf("token mismatch: got %q", token)
	}
}

func testGigaChatPasswordIgnoresClientCertificate(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	server, caBundleFile, certFile, keyFile := newGigaChatClientCertRequestingServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/token" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.TLS != nil && len(r.TLS.PeerCertificates) != 0 {
			t.Error("password token request should not include client certificate")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tok":"password-token","exp":` + formatUnixMilli(now.Add(30*time.Minute)) + `}`))
	}))

	provider := newTestGigaChatProvider(t, func() time.Time { return now })
	key := testGigaChatPasswordKey(server.URL+"/api", "test-user", "test-password")
	key.GigaChatKeyConfig.CABundleFile = caBundleFile
	key.GigaChatKeyConfig.CertFile = certFile
	key.GigaChatKeyConfig.KeyFile = keyFile

	token, bifrostErr := provider.getPasswordAccessToken(testBifrostContext(), key)
	if bifrostErr != nil {
		t.Fatalf("getPasswordAccessToken returned error: %v", bifrostErr)
	}
	if token != "password-token" {
		t.Fatalf("token mismatch: got %q", token)
	}
}

func newGigaChatMTLSServer(t *testing.T, handler http.Handler) (*httptest.Server, string, string, string) {
	t.Helper()

	clientCertPEM, clientKeyPEM := generateGigaChatTestCertificate(t)
	clientCAPool := x509.NewCertPool()
	if !clientCAPool.AppendCertsFromPEM(clientCertPEM) {
		t.Fatal("failed to parse client CA certificate")
	}

	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{
		MinVersion: tls.VersionTLS12,
		ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs:  clientCAPool,
	}
	server.StartTLS()
	t.Cleanup(server.Close)

	serverCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	caBundleFile := writeGigaChatTestFile(t, "token-server-ca.pem", serverCertPEM)
	certFile := writeGigaChatTestFile(t, "token-client.pem", clientCertPEM)
	keyFile := writeGigaChatTestFile(t, "token-client.key", clientKeyPEM)
	return server, caBundleFile, certFile, keyFile
}

func newGigaChatClientCertRequestingServer(t *testing.T, handler http.Handler) (*httptest.Server, string, string, string) {
	t.Helper()

	clientCertPEM, clientKeyPEM := generateGigaChatTestCertificate(t)
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{
		MinVersion: tls.VersionTLS12,
		ClientAuth: tls.RequestClientCert,
	}
	server.StartTLS()
	t.Cleanup(server.Close)

	serverCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	caBundleFile := writeGigaChatTestFile(t, "token-server-ca.pem", serverCertPEM)
	certFile := writeGigaChatTestFile(t, "token-client.pem", clientCertPEM)
	keyFile := writeGigaChatTestFile(t, "token-client.key", clientKeyPEM)
	return server, caBundleFile, certFile, keyFile
}

func testGigaChatOAuthParsesMillisecondsExpiresAt(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count := requestCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"token-` + formatInt32(count) + `","expires_at":` + formatUnixMilli(now.Add(30*time.Minute)) + `}`))
	}))
	defer server.Close()

	provider := newTestGigaChatProvider(t, func() time.Time { return now })
	key := testGigaChatOAuthKey(server.URL, "", "test-credentials")

	firstToken, bifrostErr := provider.getOAuthAccessToken(testBifrostContext(), key)
	if bifrostErr != nil {
		t.Fatalf("first getOAuthAccessToken returned error: %v", bifrostErr)
	}
	now = now.Add(31 * time.Minute)
	secondToken, bifrostErr := provider.getOAuthAccessToken(testBifrostContext(), key)
	if bifrostErr != nil {
		t.Fatalf("second getOAuthAccessToken returned error: %v", bifrostErr)
	}

	if firstToken != "token-1" || secondToken != "token-2" {
		t.Fatalf("token refresh mismatch: first=%q second=%q", firstToken, secondToken)
	}
	if requestCount.Load() != 2 {
		t.Fatalf("request count mismatch: got %d, want 2", requestCount.Load())
	}
}

func testGigaChatPasswordParsesSecondsExpiresAt(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count := requestCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tok":"password-token-` + formatInt32(count) + `","exp":` + formatUnix(now.Add(30*time.Minute)) + `}`))
	}))
	defer server.Close()

	provider := newTestGigaChatProvider(t, func() time.Time { return now })
	key := testGigaChatPasswordKey(server.URL, "test-user", "test-password")

	firstToken, bifrostErr := provider.getPasswordAccessToken(testBifrostContext(), key)
	if bifrostErr != nil {
		t.Fatalf("first getPasswordAccessToken returned error: %v", bifrostErr)
	}
	secondToken, bifrostErr := provider.getPasswordAccessToken(testBifrostContext(), key)
	if bifrostErr != nil {
		t.Fatalf("second getPasswordAccessToken returned error: %v", bifrostErr)
	}

	if firstToken != "password-token-1" || secondToken != "password-token-1" {
		t.Fatalf("cached token mismatch: first=%q second=%q", firstToken, secondToken)
	}
	if requestCount.Load() != 1 {
		t.Fatalf("request count mismatch: got %d, want 1", requestCount.Load())
	}
}

func testGigaChatPasswordCachesTokenBeforeLeeway(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count := requestCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tok":"password-token-` + formatInt32(count) + `","exp":` + formatUnixMilli(now.Add(30*time.Minute)) + `}`))
	}))
	defer server.Close()

	provider := newTestGigaChatProvider(t, func() time.Time { return now })
	key := testGigaChatPasswordKey(server.URL, "test-user", "test-password")

	firstToken, bifrostErr := provider.getPasswordAccessToken(testBifrostContext(), key)
	if bifrostErr != nil {
		t.Fatalf("first getPasswordAccessToken returned error: %v", bifrostErr)
	}
	secondToken, bifrostErr := provider.getPasswordAccessToken(testBifrostContext(), key)
	if bifrostErr != nil {
		t.Fatalf("second getPasswordAccessToken returned error: %v", bifrostErr)
	}

	if firstToken != "password-token-1" || secondToken != "password-token-1" {
		t.Fatalf("cached token mismatch: first=%q second=%q", firstToken, secondToken)
	}
	if requestCount.Load() != 1 {
		t.Fatalf("request count mismatch: got %d, want 1", requestCount.Load())
	}
}

func testGigaChatPasswordCacheIncludesCABundle(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count := requestCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tok":"password-token-` + formatInt32(count) + `","exp":` + formatUnixMilli(now.Add(30*time.Minute)) + `}`))
	}))
	defer server.Close()

	caBundlePEM1, _ := generateGigaChatTestCertificate(t)
	caBundlePEM2, _ := generateGigaChatTestCertificate(t)
	caBundleFile1 := writeGigaChatTestFile(t, "ca-1.pem", caBundlePEM1)
	caBundleFile2 := writeGigaChatTestFile(t, "ca-2.pem", caBundlePEM2)

	provider := newTestGigaChatProvider(t, func() time.Time { return now })
	key1 := testGigaChatPasswordKey(server.URL, "test-user", "test-password")
	key1.GigaChatKeyConfig.CABundleFile = caBundleFile1
	key2 := testGigaChatPasswordKey(server.URL, "test-user", "test-password")
	key2.GigaChatKeyConfig.CABundleFile = caBundleFile2

	firstToken, bifrostErr := provider.getPasswordAccessToken(testBifrostContext(), key1)
	if bifrostErr != nil {
		t.Fatalf("first getPasswordAccessToken returned error: %v", bifrostErr)
	}
	secondToken, bifrostErr := provider.getPasswordAccessToken(testBifrostContext(), key2)
	if bifrostErr != nil {
		t.Fatalf("second getPasswordAccessToken returned error: %v", bifrostErr)
	}
	thirdToken, bifrostErr := provider.getPasswordAccessToken(testBifrostContext(), key1)
	if bifrostErr != nil {
		t.Fatalf("third getPasswordAccessToken returned error: %v", bifrostErr)
	}

	if firstToken != "password-token-1" || secondToken != "password-token-2" || thirdToken != "password-token-1" {
		t.Fatalf("cache partition mismatch: first=%q second=%q third=%q", firstToken, secondToken, thirdToken)
	}
	if requestCount.Load() != 2 {
		t.Fatalf("request count mismatch: got %d, want 2", requestCount.Load())
	}
}

func testGigaChatPasswordRefreshesTokenInsideLeeway(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count := requestCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tok":"password-token-` + formatInt32(count) + `","exp":` + formatUnixMilli(now.Add(30*time.Minute)) + `}`))
	}))
	defer server.Close()

	provider := newTestGigaChatProvider(t, func() time.Time { return now })
	key := testGigaChatPasswordKey(server.URL, "test-user", "test-password")

	firstToken, bifrostErr := provider.getPasswordAccessToken(testBifrostContext(), key)
	if bifrostErr != nil {
		t.Fatalf("first getPasswordAccessToken returned error: %v", bifrostErr)
	}
	now = now.Add(29*time.Minute + time.Second)
	secondToken, bifrostErr := provider.getPasswordAccessToken(testBifrostContext(), key)
	if bifrostErr != nil {
		t.Fatalf("second getPasswordAccessToken returned error: %v", bifrostErr)
	}

	if firstToken != "password-token-1" || secondToken != "password-token-2" {
		t.Fatalf("token refresh mismatch: first=%q second=%q", firstToken, secondToken)
	}
	if requestCount.Load() != 2 {
		t.Fatalf("request count mismatch: got %d, want 2", requestCount.Load())
	}
}

func testGigaChatPasswordRejectsExpiredToken(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tok":"password-token-1","exp":` + formatUnixMilli(now.Add(-time.Second)) + `}`))
	}))
	defer server.Close()

	provider := newTestGigaChatProvider(t, func() time.Time { return now })
	_, bifrostErr := provider.getPasswordAccessToken(testBifrostContext(), testGigaChatPasswordKey(server.URL, "super-secret-user", "super-secret-password"))
	if bifrostErr == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(bifrostErr.GetErrorString(), "already expired") {
		t.Fatalf("unexpected error: %v", bifrostErr)
	}
	assertNoGigaChatSecretLeak(t, bifrostErr.String())
}

func testGigaChatPasswordHandlesProviderErrors(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":4,"message":"invalid password auth"}`))
	}))
	defer server.Close()

	provider := newTestGigaChatProvider(t, time.Now)
	_, bifrostErr := provider.getPasswordAccessToken(testBifrostContext(), testGigaChatPasswordKey(server.URL, "super-secret-user", "super-secret-password"))
	if bifrostErr == nil {
		t.Fatal("expected error, got nil")
	}
	if bifrostErr.Error == nil || bifrostErr.Error.Message != "invalid password auth" {
		t.Fatalf("unexpected error: %v", bifrostErr)
	}
	assertGigaChatTokenCacheEmpty(t, provider.tokenCache)
	assertNoGigaChatSecretLeak(t, bifrostErr.String())
}

func testGigaChatPasswordHandlesMalformedResponses(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		body string
	}{
		{name: "invalid json", body: `not-json`},
		{name: "missing token", body: `{"exp":1893456000000}`},
		{name: "missing expiry", body: `{"tok":"password-token-1"}`},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(testCase.body))
			}))
			defer server.Close()

			provider := newTestGigaChatProvider(t, func() time.Time { return time.Unix(100, 0) })
			_, bifrostErr := provider.getPasswordAccessToken(testBifrostContext(), testGigaChatPasswordKey(server.URL, "super-secret-user", "super-secret-password"))
			if bifrostErr == nil {
				t.Fatal("expected error, got nil")
			}
			assertNoGigaChatSecretLeak(t, bifrostErr.String())
		})
	}
}

func testGigaChatPasswordMissingUserPassword(t *testing.T) {
	t.Parallel()

	provider := newTestGigaChatProvider(t, time.Now)
	testCases := []struct {
		name string
		key  schemas.Key
	}{
		{
			name: "missing password",
			key: schemas.Key{
				GigaChatKeyConfig: &schemas.GigaChatKeyConfig{
					User: schemas.NewSecretVar("test-user"),
				},
			},
		},
		{
			name: "missing user",
			key: schemas.Key{
				GigaChatKeyConfig: &schemas.GigaChatKeyConfig{
					Password: schemas.NewSecretVar("test-password"),
				},
			},
		},
		{
			name: "empty user env",
			key: schemas.Key{
				GigaChatKeyConfig: &schemas.GigaChatKeyConfig{
					User:     schemas.NewSecretVar("env.MISSING_GIGACHAT_USER_FOR_TEST"),
					Password: schemas.NewSecretVar("test-password"),
				},
			},
		},
		{
			name: "empty password env",
			key: schemas.Key{
				GigaChatKeyConfig: &schemas.GigaChatKeyConfig{
					User:     schemas.NewSecretVar("test-user"),
					Password: schemas.NewSecretVar("env.MISSING_GIGACHAT_PASSWORD_FOR_TEST"),
				},
			},
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			_, bifrostErr := provider.getPasswordAccessToken(testBifrostContext(), testCase.key)
			if bifrostErr == nil {
				t.Fatal("expected error, got nil")
			}
			assertNoGigaChatSecretLeak(t, bifrostErr.String())
		})
	}
}

func testGigaChatAuthPriority(t *testing.T) {
	t.Parallel()

	t.Run("AccessTokenBeforeTokenFlows", func(t *testing.T) {
		t.Parallel()

		provider := newTestGigaChatProvider(t, time.Now)
		token, bifrostErr := provider.getGigaChatAccessToken(testBifrostContext(), schemas.Key{
			GigaChatKeyConfig: &schemas.GigaChatKeyConfig{
				AccessToken: schemas.NewSecretVar("explicit-token"),
				Credentials: schemas.NewSecretVar("test-credentials"),
				User:        schemas.NewSecretVar("test-user"),
				Password:    schemas.NewSecretVar("test-password"),
			},
		})
		if bifrostErr != nil {
			t.Fatalf("getGigaChatAccessToken returned error: %v", bifrostErr)
		}
		if token != "explicit-token" {
			t.Fatalf("token mismatch: got %q", token)
		}
	})

	t.Run("OAuthBeforePassword", func(t *testing.T) {
		t.Parallel()

		now := time.Unix(1_700_000_000, 0)
		var oauthRequests atomic.Int32
		var passwordRequests atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/v2/oauth":
				oauthRequests.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"access_token":"oauth-token","expires_at":` + formatUnix(now.Add(30*time.Minute)) + `}`))
			case "/api/v1/token":
				passwordRequests.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			default:
				t.Errorf("unexpected path: %s", r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer server.Close()

		provider := newTestGigaChatProvider(t, func() time.Time { return now })
		token, bifrostErr := provider.getGigaChatAccessToken(testBifrostContext(), schemas.Key{
			GigaChatKeyConfig: &schemas.GigaChatKeyConfig{
				Credentials: schemas.NewSecretVar("test-credentials"),
				AuthURL:     server.URL + "/api/v2/oauth",
				BaseURL:     server.URL + "/api",
				User:        schemas.NewSecretVar("test-user"),
				Password:    schemas.NewSecretVar("test-password"),
			},
		})
		if bifrostErr != nil {
			t.Fatalf("getGigaChatAccessToken returned error: %v", bifrostErr)
		}
		if token != "oauth-token" {
			t.Fatalf("token mismatch: got %q", token)
		}
		if oauthRequests.Load() != 1 {
			t.Fatalf("oauth request count mismatch: got %d, want 1", oauthRequests.Load())
		}
		if passwordRequests.Load() != 0 {
			t.Fatalf("password request count mismatch: got %d, want 0", passwordRequests.Load())
		}
	})
}

func testGigaChatOAuthCachesTokenBeforeLeeway(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count := requestCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"token-` + formatInt32(count) + `","expires_at":` + formatUnix(now.Add(30*time.Minute)) + `}`))
	}))
	defer server.Close()

	provider := newTestGigaChatProvider(t, func() time.Time { return now })
	key := testGigaChatOAuthKey(server.URL, "", "test-credentials")

	firstToken, bifrostErr := provider.getOAuthAccessToken(testBifrostContext(), key)
	if bifrostErr != nil {
		t.Fatalf("first getOAuthAccessToken returned error: %v", bifrostErr)
	}
	secondToken, bifrostErr := provider.getOAuthAccessToken(testBifrostContext(), key)
	if bifrostErr != nil {
		t.Fatalf("second getOAuthAccessToken returned error: %v", bifrostErr)
	}

	if firstToken != "token-1" || secondToken != "token-1" {
		t.Fatalf("cached token mismatch: first=%q second=%q", firstToken, secondToken)
	}
	if requestCount.Load() != 1 {
		t.Fatalf("request count mismatch: got %d, want 1", requestCount.Load())
	}
}

func testGigaChatOAuthCacheIncludesCABundle(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count := requestCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"token-` + formatInt32(count) + `","expires_at":` + formatUnix(now.Add(30*time.Minute)) + `}`))
	}))
	defer server.Close()

	caBundlePEM1, _ := generateGigaChatTestCertificate(t)
	caBundlePEM2, _ := generateGigaChatTestCertificate(t)
	caBundleFile1 := writeGigaChatTestFile(t, "ca-1.pem", caBundlePEM1)
	caBundleFile2 := writeGigaChatTestFile(t, "ca-2.pem", caBundlePEM2)

	provider := newTestGigaChatProvider(t, func() time.Time { return now })
	key1 := testGigaChatOAuthKey(server.URL, "", "test-credentials")
	key1.GigaChatKeyConfig.CABundleFile = caBundleFile1
	key2 := testGigaChatOAuthKey(server.URL, "", "test-credentials")
	key2.GigaChatKeyConfig.CABundleFile = caBundleFile2

	firstToken, bifrostErr := provider.getOAuthAccessToken(testBifrostContext(), key1)
	if bifrostErr != nil {
		t.Fatalf("first getOAuthAccessToken returned error: %v", bifrostErr)
	}
	secondToken, bifrostErr := provider.getOAuthAccessToken(testBifrostContext(), key2)
	if bifrostErr != nil {
		t.Fatalf("second getOAuthAccessToken returned error: %v", bifrostErr)
	}
	thirdToken, bifrostErr := provider.getOAuthAccessToken(testBifrostContext(), key1)
	if bifrostErr != nil {
		t.Fatalf("third getOAuthAccessToken returned error: %v", bifrostErr)
	}

	if firstToken != "token-1" || secondToken != "token-2" || thirdToken != "token-1" {
		t.Fatalf("cache partition mismatch: first=%q second=%q third=%q", firstToken, secondToken, thirdToken)
	}
	if requestCount.Load() != 2 {
		t.Fatalf("request count mismatch: got %d, want 2", requestCount.Load())
	}
}

func testGigaChatOAuthRefreshesTokenInsideLeeway(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count := requestCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"token-` + formatInt32(count) + `","expires_at":` + formatUnix(now.Add(30*time.Minute)) + `}`))
	}))
	defer server.Close()

	provider := newTestGigaChatProvider(t, func() time.Time { return now })
	key := testGigaChatOAuthKey(server.URL, "", "test-credentials")

	firstToken, bifrostErr := provider.getOAuthAccessToken(testBifrostContext(), key)
	if bifrostErr != nil {
		t.Fatalf("first getOAuthAccessToken returned error: %v", bifrostErr)
	}
	now = now.Add(29*time.Minute + time.Second)
	secondToken, bifrostErr := provider.getOAuthAccessToken(testBifrostContext(), key)
	if bifrostErr != nil {
		t.Fatalf("second getOAuthAccessToken returned error: %v", bifrostErr)
	}

	if firstToken != "token-1" || secondToken != "token-2" {
		t.Fatalf("token refresh mismatch: first=%q second=%q", firstToken, secondToken)
	}
	if requestCount.Load() != 2 {
		t.Fatalf("request count mismatch: got %d, want 2", requestCount.Load())
	}
}

func testGigaChatOAuthHandlesProviderErrors(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		statusCode  int
		body        string
		wantMessage string
		wantCode    string
	}{
		{
			name:        "bad request code shape",
			statusCode:  http.StatusBadRequest,
			body:        `{"code":5,"message":"scope is empty"}`,
			wantMessage: "scope is empty",
			wantCode:    "5",
		},
		{
			name:        "unauthorized code shape",
			statusCode:  http.StatusUnauthorized,
			body:        `{"code":4,"message":"Can't decode 'Authorization' header"}`,
			wantMessage: "Can't decode 'Authorization' header",
			wantCode:    "4",
		},
		{
			name:        "server status shape",
			statusCode:  http.StatusInternalServerError,
			body:        `{"status":500,"message":"Internal Server Error"}`,
			wantMessage: "Internal Server Error",
			wantCode:    "500",
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(testCase.statusCode)
				_, _ = w.Write([]byte(testCase.body))
			}))
			defer server.Close()

			provider := newTestGigaChatProvider(t, time.Now)
			_, bifrostErr := provider.getOAuthAccessToken(testBifrostContext(), testGigaChatOAuthKey(server.URL, "", "super-secret-credentials"))
			if bifrostErr == nil {
				t.Fatal("expected error, got nil")
			}
			if bifrostErr.Error == nil {
				t.Fatal("expected error field, got nil")
			}
			if bifrostErr.Error.Message != testCase.wantMessage {
				t.Fatalf("message mismatch: got %q, want %q", bifrostErr.Error.Message, testCase.wantMessage)
			}
			if bifrostErr.Error.Code == nil || *bifrostErr.Error.Code != testCase.wantCode {
				t.Fatalf("code mismatch: got %#v, want %q", bifrostErr.Error.Code, testCase.wantCode)
			}
			assertGigaChatTokenCacheEmpty(t, provider.tokenCache)
			assertNoGigaChatSecretLeak(t, bifrostErr.String())
		})
	}
}

func assertGigaChatTokenCacheEmpty(t *testing.T, cache *gigaChatTokenCache) {
	t.Helper()
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if len(cache.entries) != 0 {
		t.Fatalf("token cache retained %d entries after failed exchange", len(cache.entries))
	}
}

func testGigaChatOAuthHandlesMalformedResponses(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		body string
	}{
		{name: "invalid json", body: `not-json`},
		{name: "missing token", body: `{"expires_at":1893456000}`},
		{name: "missing expiry", body: `{"access_token":"token-1"}`},
		{name: "expired token", body: `{"access_token":"token-1","expires_at":1}`},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(testCase.body))
			}))
			defer server.Close()

			provider := newTestGigaChatProvider(t, func() time.Time { return time.Unix(100, 0) })
			_, bifrostErr := provider.getOAuthAccessToken(testBifrostContext(), testGigaChatOAuthKey(server.URL, "", "super-secret-credentials"))
			if bifrostErr == nil {
				t.Fatal("expected error, got nil")
			}
			assertNoGigaChatSecretLeak(t, bifrostErr.String())
		})
	}
}

func testGigaChatOAuthMissingCredentials(t *testing.T) {
	t.Parallel()

	provider := newTestGigaChatProvider(t, time.Now)
	_, bifrostErr := provider.getOAuthAccessToken(testBifrostContext(), schemas.Key{
		GigaChatKeyConfig: &schemas.GigaChatKeyConfig{},
	})
	if bifrostErr == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(bifrostErr.GetErrorString(), "credentials") {
		t.Fatalf("unexpected error: %v", bifrostErr)
	}

	_, bifrostErr = provider.getOAuthAccessToken(testBifrostContext(), schemas.Key{
		GigaChatKeyConfig: &schemas.GigaChatKeyConfig{
			Credentials: schemas.NewSecretVar("env.MISSING_GIGACHAT_CREDENTIALS_FOR_TEST"),
		},
	})
	if bifrostErr == nil {
		t.Fatal("expected unresolved env error, got nil")
	}
	if !strings.Contains(bifrostErr.GetErrorString(), "empty value") {
		t.Fatalf("unexpected unresolved env error: %v", bifrostErr)
	}
}

func testGigaChatOAuthContextCancellation(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(10 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"token-1","expires_at":1893456000}`))
	}))
	defer server.Close()

	cancelledContext, cancel := context.WithCancel(context.Background())
	cancel()

	provider := newTestGigaChatProvider(t, time.Now)
	_, bifrostErr := provider.getOAuthAccessToken(schemas.NewBifrostContext(cancelledContext, schemas.NoDeadline), testGigaChatOAuthKey(server.URL, "", "test-credentials"))
	if bifrostErr == nil {
		t.Fatal("expected cancellation error, got nil")
	}
	if bifrostErr.Error == nil || bifrostErr.Error.Type == nil || *bifrostErr.Error.Type != schemas.RequestCancelled {
		t.Fatalf("unexpected cancellation error: %v", bifrostErr)
	}
}

func newTestGigaChatProvider(t *testing.T, now func() time.Time) *GigaChatProvider {
	t.Helper()

	provider, err := NewGigaChatProvider(&schemas.ProviderConfig{}, nil)
	if err != nil {
		t.Fatalf("NewGigaChatProvider returned error: %v", err)
	}
	dialer := &net.Dialer{}
	provider.client.Dial = func(addr string) (net.Conn, error) {
		return dialer.Dial("tcp", addr)
	}
	provider.client.DialTimeout = nil
	provider.streamingClient.Dial = provider.client.Dial
	provider.streamingClient.DialTimeout = nil
	provider.tokenCache = newGigaChatTokenCache(now)
	return provider
}

func testGigaChatOAuthKey(authURL string, scope string, credentials string) schemas.Key {
	return schemas.Key{
		GigaChatKeyConfig: &schemas.GigaChatKeyConfig{
			Credentials: schemas.NewSecretVar(credentials),
			Scope:       scope,
			AuthURL:     authURL,
		},
	}
}

func testGigaChatPasswordKey(baseURL string, user string, password string) schemas.Key {
	return schemas.Key{
		GigaChatKeyConfig: &schemas.GigaChatKeyConfig{
			BaseURL:  baseURL,
			User:     schemas.NewSecretVar(user),
			Password: schemas.NewSecretVar(password),
		},
	}
}

func testBifrostContext() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
}

func assertNoGigaChatSecretLeak(t *testing.T, output string) {
	t.Helper()
	for _, secret := range []string{"super-secret-credentials", "test-credentials", "super-secret-user", "super-secret-password", "test-user", "test-password", "explicit-access-token", "key-value-access-token", "provider-authorization-token", "context-authorization-token"} {
		if strings.Contains(output, secret) {
			t.Fatalf("secret %q leaked in %s", secret, output)
		}
	}
}

func assertGigaChatDefaultHeaders(t *testing.T, headers map[string]string, wantAuthorization string) {
	t.Helper()

	if got := headers[gigaChatAuthorizationHeader]; got != wantAuthorization {
		t.Fatalf("authorization header mismatch: got %q, want %q", got, wantAuthorization)
	}
	if got := headers[gigaChatUserAgentHeader]; got != gigaChatUserAgent {
		t.Fatalf("user-agent header mismatch: got %q, want %q", got, gigaChatUserAgent)
	}
}

func assertGigaChatDefaultHeadersWithoutAuthorization(t *testing.T, headers map[string]string) {
	t.Helper()

	if got := headers[gigaChatAuthorizationHeader]; got != "" {
		t.Fatalf("unexpected authorization header: %q", got)
	}
	if got := headers[gigaChatUserAgentHeader]; got != gigaChatUserAgent {
		t.Fatalf("user-agent header mismatch: got %q, want %q", got, gigaChatUserAgent)
	}
}

func formatUnix(value time.Time) string {
	return formatInt64(value.Unix())
}

func formatUnixMilli(value time.Time) string {
	return formatInt64(value.UnixMilli())
}

func formatInt32(value int32) string {
	return formatInt64(int64(value))
}

func formatInt64(value int64) string {
	return strconv.FormatInt(value, 10)
}
