package sapaicore

import (
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
	"golang.org/x/sync/singleflight"
)

// TokenCache manages OAuth2 token caching for SAP AI Core
type TokenCache struct {
	mu      sync.RWMutex
	tokens  map[string]*cachedToken
	client  *fasthttp.Client
	timeout time.Duration
	group   singleflight.Group
}

// cachedToken represents a cached OAuth2 token with expiration
type cachedToken struct {
	accessToken string
	expiresAt   time.Time
}

// tokenResult carries both token string and error for singleflight return.
// Since BifrostError doesn't implement the error interface, we pass it through the result.
type tokenResult struct {
	token    string
	bifError *schemas.BifrostError
}

// newTokenCache creates a new token cache with the given HTTP request timeout.
func newTokenCache(client *fasthttp.Client, timeout time.Duration) *TokenCache {
	return &TokenCache{
		tokens:  make(map[string]*cachedToken),
		client:  client,
		timeout: timeout,
	}
}

// cacheKey generates a unique key for the token cache based on auth config.
// Uses length-prefixed format to avoid collisions when values contain ":"
func cacheKey(clientID, authURL string) string {
	normalized := normalizeAuthURL(authURL)
	return fmt.Sprintf("%d:%s:%s", len(clientID), clientID, normalized)
}

// normalizeAuthURL canonicalizes the auth URL so different forms of the same
// endpoint (e.g. "https://host", "https://host/", "https://host/oauth/token")
// produce the same cache key.
func normalizeAuthURL(authURL string) string {
	u := strings.TrimRight(authURL, "/")
	if !strings.HasSuffix(u, "/oauth/token") {
		u += "/oauth/token"
	}
	return u
}

// GetToken retrieves a valid token from cache or fetches a new one.
// Uses singleflight to coalesce concurrent refresh requests for the same key.
func (tc *TokenCache) GetToken(clientID, clientSecret, authURL string) (string, *schemas.BifrostError) {
	key := cacheKey(clientID, authURL)

	// Try to get from cache first (read lock)
	tc.mu.RLock()
	if cached, ok := tc.tokens[key]; ok {
		// Add 30-second buffer before expiration
		if time.Now().Add(30 * time.Second).Before(cached.expiresAt) {
			token := cached.accessToken
			tc.mu.RUnlock()
			return token, nil
		}
	}
	tc.mu.RUnlock()

	// Opportunistic cleanup: prune expired tokens during cache misses
	tc.pruneExpired()

	// Use singleflight to coalesce concurrent fetches for the same key.
	// BifrostError is passed through tokenResult (not the error return) since
	// BifrostError doesn't implement the error interface.
	result, _, _ := tc.group.Do(key, func() (interface{}, error) {
		// Double-check cache (another goroutine may have just refreshed)
		tc.mu.RLock()
		if cached, ok := tc.tokens[key]; ok {
			if time.Now().Add(30 * time.Second).Before(cached.expiresAt) {
				token := cached.accessToken
				tc.mu.RUnlock()
				return &tokenResult{token: token}, nil
			}
		}
		tc.mu.RUnlock()

		// Fetch new token
		token, expiresIn, fetchErr := tc.fetchToken(clientID, clientSecret, authURL)
		if fetchErr != nil {
			return &tokenResult{bifError: fetchErr}, nil
		}

		// Cache the result
		tc.mu.Lock()
		tc.tokens[key] = &cachedToken{
			accessToken: token,
			expiresAt:   time.Now().Add(time.Duration(expiresIn) * time.Second),
		}
		tc.mu.Unlock()

		return &tokenResult{token: token}, nil
	})

	tr := result.(*tokenResult)
	if tr.bifError != nil {
		return "", tr.bifError
	}
	return tr.token, nil
}

// fetchToken performs the OAuth2 client credentials flow
func (tc *TokenCache) fetchToken(clientID, clientSecret, authURL string) (string, int, *schemas.BifrostError) {
	// Ensure authURL ends with /oauth/token
	tokenURL := strings.TrimRight(authURL, "/")
	if !strings.HasSuffix(tokenURL, "/oauth/token") {
		tokenURL += "/oauth/token"
	}

	// Build form data
	data := url.Values{}
	data.Set("grant_type", "client_credentials")
	data.Set("client_id", clientID)
	data.Set("client_secret", clientSecret)

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(tokenURL)
	req.Header.SetMethod(fasthttp.MethodPost)
	req.Header.SetContentType("application/x-www-form-urlencoded")
	req.SetBodyString(data.Encode())

	if err := tc.client.DoTimeout(req, resp, tc.timeout); err != nil {
		return "", 0, providerUtils.NewBifrostOperationError(
			fmt.Sprintf("failed to fetch oauth2 token from %s", tokenURL),
			err,
			schemas.SAPAICore,
		)
	}

	if resp.StatusCode() != fasthttp.StatusOK {
		return "", 0, providerUtils.NewBifrostOperationError(
			fmt.Sprintf("oauth2 token request failed with status %d", resp.StatusCode()),
			fmt.Errorf("http %d", resp.StatusCode()),
			schemas.SAPAICore,
		)
	}

	var tokenResp SAPAICoreTokenResponse
	if err := sonic.Unmarshal(resp.Body(), &tokenResp); err != nil {
		return "", 0, providerUtils.NewBifrostOperationError(
			"failed to parse oauth2 token response",
			err,
			schemas.SAPAICore,
		)
	}

	if tokenResp.AccessToken == "" {
		return "", 0, providerUtils.NewBifrostOperationError(
			"oauth2 token response contains empty access_token",
			fmt.Errorf("empty access_token"),
			schemas.SAPAICore,
		)
	}

	// Default to 1 hour if expires_in is not provided
	expiresIn := tokenResp.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}

	return tokenResp.AccessToken, expiresIn, nil
}

// ClearToken removes a token from the cache (useful when token is rejected)
func (tc *TokenCache) ClearToken(clientID, authURL string) {
	key := cacheKey(clientID, authURL)
	tc.mu.Lock()
	delete(tc.tokens, key)
	tc.mu.Unlock()
}

// Cleanup removes all expired tokens from the cache
func (tc *TokenCache) Cleanup() {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	now := time.Now()
	for key, token := range tc.tokens {
		if now.After(token.expiresAt) {
			delete(tc.tokens, key)
		}
	}
}

// pruneExpired is a non-blocking opportunistic cleanup that removes expired tokens
// if the write lock can be acquired without contention.
func (tc *TokenCache) pruneExpired() {
	if !tc.mu.TryLock() {
		return
	}
	defer tc.mu.Unlock()

	now := time.Now()
	for key, token := range tc.tokens {
		if now.After(token.expiresAt) {
			delete(tc.tokens, key)
		}
	}
}
