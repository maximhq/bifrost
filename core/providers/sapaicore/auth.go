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
)

// TokenCache manages OAuth2 token caching for SAP AI Core
type TokenCache struct {
	mu     sync.RWMutex
	tokens map[string]*cachedToken
	client *fasthttp.Client
}

// cachedToken represents a cached OAuth2 token with expiration
type cachedToken struct {
	accessToken string
	expiresAt   time.Time
}

// NewTokenCache creates a new token cache
func NewTokenCache(client *fasthttp.Client) *TokenCache {
	return &TokenCache{
		tokens: make(map[string]*cachedToken),
		client: client,
	}
}

// cacheKey generates a unique key for the token cache based on auth config
func cacheKey(clientID, authURL string) string {
	return clientID + ":" + authURL
}

// GetToken retrieves a valid token from cache or fetches a new one
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

	// Need to fetch a new token (write lock)
	tc.mu.Lock()
	defer tc.mu.Unlock()

	// Double-check after acquiring write lock
	if cached, ok := tc.tokens[key]; ok {
		if time.Now().Add(30 * time.Second).Before(cached.expiresAt) {
			return cached.accessToken, nil
		}
	}

	// Fetch new token
	token, expiresIn, err := tc.fetchToken(clientID, clientSecret, authURL)
	if err != nil {
		return "", err
	}

	// Cache the token
	tc.tokens[key] = &cachedToken{
		accessToken: token,
		expiresAt:   time.Now().Add(time.Duration(expiresIn) * time.Second),
	}

	return token, nil
}

// fetchToken performs the OAuth2 client credentials flow
func (tc *TokenCache) fetchToken(clientID, clientSecret, authURL string) (string, int, *schemas.BifrostError) {
	// Ensure authURL ends with /oauth/token
	tokenURL := authURL
	if !strings.HasSuffix(tokenURL, "/oauth/token") {
		if strings.HasSuffix(tokenURL, "/") {
			tokenURL += "oauth/token"
		} else {
			tokenURL += "/oauth/token"
		}
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

	if err := tc.client.DoTimeout(req, resp, 30*time.Second); err != nil {
		return "", 0, providerUtils.NewBifrostOperationError(
			fmt.Sprintf("failed to fetch OAuth2 token from %s", tokenURL),
			err,
			schemas.SAPAICore,
		)
	}

	if resp.StatusCode() != fasthttp.StatusOK {
		return "", 0, providerUtils.NewBifrostOperationError(
			fmt.Sprintf("OAuth2 token request failed with status %d: %s", resp.StatusCode(), string(resp.Body())),
			fmt.Errorf("HTTP %d", resp.StatusCode()),
			schemas.SAPAICore,
		)
	}

	var tokenResp TokenResponse
	if err := sonic.Unmarshal(resp.Body(), &tokenResp); err != nil {
		return "", 0, providerUtils.NewBifrostOperationError(
			"failed to parse OAuth2 token response",
			err,
			schemas.SAPAICore,
		)
	}

	if tokenResp.AccessToken == "" {
		return "", 0, providerUtils.NewBifrostOperationError(
			"OAuth2 token response contains empty access_token",
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
