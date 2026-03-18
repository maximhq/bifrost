package copilot

import (
	"fmt"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
	"golang.org/x/sync/singleflight"
)

// tokenResult bundles the values returned from a token refresh so they can be
// transported through singleflight.Group.Do (which returns a single interface{}).
type tokenResult struct {
	token   string
	apiBase string
	err     *schemas.BifrostError
}

// copilotTokenManager handles the two-layer auth flow for a single key.
// It caches the short-lived Copilot JWT and the dynamic API base URL,
// refreshing them automatically when they expire.
//
// Concurrent callers are coalesced via singleflight: only one goroutine
// performs the HTTP token exchange while others wait for the same result.
type copilotTokenManager struct {
	mu               sync.RWMutex
	apiToken         string    // Short-lived Copilot JWT
	apiBase          string    // Dynamic API base from token response
	expiresAt        time.Time // JWT expiry
	accessToken      string    // Long-lived GitHub OAuth access token (Key.Value)
	client           *fasthttp.Client
	logger           schemas.Logger
	tokenExchangeURL string // URL for token exchange; overridable in tests

	sfGroup singleflight.Group // coalesces concurrent refresh attempts
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
// Concurrent callers share a single in-flight refresh via singleflight.
func (tm *copilotTokenManager) getToken() (token string, apiBase string, bifrostErr *schemas.BifrostError) {
	// Fast path: read-lock check for a cached, non-expired token.
	tm.mu.RLock()
	if tm.apiToken != "" && time.Now().Before(tm.expiresAt.Add(-time.Duration(tokenExpiryMargin)*time.Second)) {
		t, b := tm.apiToken, tm.apiBase
		tm.mu.RUnlock()
		return t, b, nil
	}
	tm.mu.RUnlock()

	// Slow path: coalesce concurrent refreshes through singleflight.
	val, _, _ := tm.sfGroup.Do("refresh", func() (interface{}, error) {
		return tm.refreshToken(), nil
	})
	res := val.(*tokenResult)
	return res.token, res.apiBase, res.err
}

// refreshToken exchanges the OAuth access token for a short-lived Copilot JWT.
// Called inside singleflight — at most one goroutine runs this at a time per key.
func (tm *copilotTokenManager) refreshToken() *tokenResult {
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
		tm.mu.RLock()
		cached := tm.apiToken != "" && time.Now().Before(tm.expiresAt)
		t, b := tm.apiToken, tm.apiBase
		tm.mu.RUnlock()
		if cached {
			if tm.logger != nil {
				tm.logger.Warn("copilot: token exchange transport failed; using cached token",
					"error", err.Error())
			}
			return &tokenResult{token: t, apiBase: b}
		}
		return &tokenResult{err: &schemas.BifrostError{
			IsBifrostError: true,
			StatusCode:     intPtr(500),
			Error: &schemas.ErrorField{
				Message: fmt.Sprintf("copilot token exchange request failed: %s", err.Error()),
			},
		}}
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

		tm.mu.Lock()
		// Only clear cached JWT on definitive auth failures (401/403).
		if sc == 401 || sc == 403 {
			tm.apiToken = ""
		} else if tm.apiToken != "" && time.Now().Before(tm.expiresAt) {
			t, b := tm.apiToken, tm.apiBase
			tm.mu.Unlock()
			return &tokenResult{token: t, apiBase: b}
		}
		tm.mu.Unlock()

		var allowFallbacks *bool
		if sc == 401 || sc == 403 {
			noFallback := false
			allowFallbacks = &noFallback
		}
		return &tokenResult{err: &schemas.BifrostError{
			IsBifrostError: true,
			StatusCode:     &sc,
			AllowFallbacks: allowFallbacks,
			Error: &schemas.ErrorField{
				Message: fmt.Sprintf("copilot token exchange returned %d: %s", sc, string(body)),
			},
		}}
	}

	var tokenResp CopilotTokenResponse
	if err := sonic.Unmarshal(resp.Body(), &tokenResp); err != nil {
		tm.mu.RLock()
		cached := tm.apiToken != "" && time.Now().Before(tm.expiresAt)
		t, b := tm.apiToken, tm.apiBase
		tm.mu.RUnlock()
		if cached {
			if tm.logger != nil {
				tm.logger.Warn("copilot: token exchange returned invalid JSON; using cached token",
					"error", err.Error())
			}
			return &tokenResult{token: t, apiBase: b}
		}
		return &tokenResult{err: &schemas.BifrostError{
			IsBifrostError: true,
			StatusCode:     intPtr(500),
			Error: &schemas.ErrorField{
				Message: fmt.Sprintf("failed to parse copilot token response: %s", err.Error()),
			},
		}}
	}

	if tokenResp.Token == "" {
		noFallback := false
		return &tokenResult{err: &schemas.BifrostError{
			IsBifrostError: true,
			StatusCode:     intPtr(401),
			AllowFallbacks: &noFallback,
			Error: &schemas.ErrorField{
				Message: "copilot token exchange returned empty token; ensure the OAuth access token is valid and the GitHub account has an active Copilot subscription",
			},
		}}
	}

	tm.mu.Lock()
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
	t, b := tm.apiToken, tm.apiBase
	tm.mu.Unlock()

	return &tokenResult{token: t, apiBase: b}
}
