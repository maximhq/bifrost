package copilot

import (
	"fmt"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// copilotTokenManager handles the two-layer auth flow for a single key.
// It caches the short-lived Copilot JWT and the dynamic API base URL,
// refreshing them automatically when they expire.
type copilotTokenManager struct {
	mu               sync.Mutex
	apiToken         string    // Short-lived Copilot JWT
	apiBase          string    // Dynamic API base from token response
	expiresAt        time.Time // JWT expiry
	accessToken      string    // Long-lived GitHub OAuth access token (Key.Value)
	client           *fasthttp.Client
	logger           schemas.Logger
	tokenExchangeURL string // URL for token exchange; overridable in tests
}

// newCopilotTokenManager creates a token manager for a given OAuth access token.
func newCopilotTokenManager(accessToken string, client *fasthttp.Client, logger schemas.Logger) *copilotTokenManager {
	return &copilotTokenManager{
		accessToken:      accessToken,
		client:           client,
		logger:           logger,
		apiBase:          defaultAPIBaseURL,
		tokenExchangeURL: defaultTokenExchangeURL,
	}
}

func intPtr(i int) *int { return &i }

// getToken returns a valid Copilot JWT and the API base URL.
// It refreshes the token if expired or about to expire.
func (tm *copilotTokenManager) getToken() (token string, apiBase string, bifrostErr *schemas.BifrostError) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if tm.apiToken != "" && time.Now().Before(tm.expiresAt.Add(-time.Duration(tokenExpiryMargin)*time.Second)) {
		return tm.apiToken, tm.apiBase, nil
	}

	return tm.refreshTokenLocked()
}

// refreshTokenLocked exchanges the OAuth access token for a short-lived Copilot JWT.
// Must be called with tm.mu held.
func (tm *copilotTokenManager) refreshTokenLocked() (string, string, *schemas.BifrostError) {
	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(tm.tokenExchangeURL)
	req.Header.SetMethod(fasthttp.MethodGet)
	req.Header.Set("authorization", "Bearer "+tm.accessToken)
	req.Header.Set("accept", "application/json")
	for k, v := range copilotRequiredHeaders {
		req.Header.Set(k, v)
	}

	if err := tm.client.Do(req, resp); err != nil {
		if tm.apiToken != "" && time.Now().Before(tm.expiresAt) {
			if tm.logger != nil {
				tm.logger.Warn("copilot: token exchange transport failed; using cached token",
					"error", err.Error())
			}
			return tm.apiToken, tm.apiBase, nil
		}
		return "", "", &schemas.BifrostError{
			IsBifrostError: true,
			StatusCode:     intPtr(500),
			Error: &schemas.ErrorField{
				Message: fmt.Sprintf("copilot token exchange request failed: %s", err.Error()),
			},
		}
	}

	if resp.StatusCode() != 200 {
		sc := resp.StatusCode()
		body := resp.Body()
		if len(body) > 512 {
			body = body[:512]
		}
		if tm.logger != nil {
			tm.logger.Warn("copilot: token exchange failed; OAuth token may be revoked or invalid",
				"status", sc)
		}
		// Only clear cached JWT on definitive auth failures (401/403).
		// For transient errors (5xx, network issues), preserve the cached token
		// so it can still be used if it hasn't actually expired.
		if sc == 401 || sc == 403 {
			tm.apiToken = ""
		} else if tm.apiToken != "" && time.Now().Before(tm.expiresAt) {
			// Token is still valid — return it despite the refresh failure
			return tm.apiToken, tm.apiBase, nil
		}
		noFallback := false
		return "", "", &schemas.BifrostError{
			IsBifrostError: true,
			StatusCode:     &sc,
			AllowFallbacks: &noFallback,
			Error: &schemas.ErrorField{
				Message: fmt.Sprintf("copilot token exchange returned %d: %s", sc, string(body)),
			},
		}
	}

	var tokenResp CopilotTokenResponse
	if err := sonic.Unmarshal(resp.Body(), &tokenResp); err != nil {
		return "", "", &schemas.BifrostError{
			IsBifrostError: true,
			StatusCode:     intPtr(500),
			Error: &schemas.ErrorField{
				Message: fmt.Sprintf("failed to parse copilot token response: %s", err.Error()),
			},
		}
	}

	if tokenResp.Token == "" {
		return "", "", &schemas.BifrostError{
			IsBifrostError: true,
			StatusCode:     intPtr(401),
			Error: &schemas.ErrorField{
				Message: "copilot token exchange returned empty token; ensure the OAuth access token is valid and the GitHub account has an active Copilot subscription",
			},
		}
	}

	tm.apiToken = tokenResp.Token
	tm.expiresAt = time.Unix(tokenResp.ExpiresAt, 0)
	if tokenResp.Endpoints.API != "" {
		if isValidCopilotAPIBase(tokenResp.Endpoints.API) {
			tm.apiBase = tokenResp.Endpoints.API
		} else if tm.logger != nil {
			tm.logger.Warn("copilot: token exchange returned untrusted API base URL; using default",
				"url", tokenResp.Endpoints.API, "default", defaultAPIBaseURL)
		}
	}

	return tm.apiToken, tm.apiBase, nil
}
