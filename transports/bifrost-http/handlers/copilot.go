// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains the Copilot GitHub OAuth device code flow endpoints.
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

const (
	// defaultGithubClientID is the GitHub OAuth client ID from the VS Code
	// Copilot extension. The Copilot API is not officially documented, so this
	// ID is required for the device-code flow. Override for a custom GitHub
	// OAuth app via the BIFROST_GITHUB_CLIENT_ID environment variable.
	defaultGithubClientID = "Iv1.b507a08c87ecfe98"

	// GitHub OAuth endpoints for device code flow
	githubDeviceCodeURL  = "https://github.com/login/device/code"
	githubAccessTokenURL = "https://github.com/login/oauth/access_token"
)

// githubClientID returns the effective OAuth client ID: env var if set, otherwise the default.
func githubClientID() string {
	if id := os.Getenv("BIFROST_GITHUB_CLIENT_ID"); id != "" {
		return id
	}
	return defaultGithubClientID
}

// CopilotHandler manages Copilot-specific HTTP requests like device code flow
type CopilotHandler struct {
	httpClient     *http.Client
	deviceCodeURL  string // GitHub device code endpoint; overridable in tests
	accessTokenURL string // GitHub access token endpoint; overridable in tests
}

// maxOAuthResponseBytes caps the body read from GitHub OAuth responses to prevent memory exhaustion.
const maxOAuthResponseBytes = 64 * 1024

// NewCopilotHandler creates a new Copilot handler instance.
// The provided config is used to configure outbound HTTP proxy settings for the OAuth device flow.
func NewCopilotHandler(config *lib.Config) *CopilotHandler {
	return &CopilotHandler{
		httpClient:     buildOAuthHTTPClient(config),
		deviceCodeURL:  githubDeviceCodeURL,
		accessTokenURL: githubAccessTokenURL,
	}
}

// buildOAuthHTTPClient builds an http.Client for GitHub OAuth calls, applying proxy
// settings from the Bifrost config when present.
func buildOAuthHTTPClient(config *lib.Config) *http.Client {
	if config == nil || config.ProxyConfig == nil || !config.ProxyConfig.Enabled || config.ProxyConfig.URL == "" {
		return &http.Client{Timeout: 30 * time.Second}
	}
	pc := config.ProxyConfig
	rawURL := pc.URL
	if pc.Username != "" {
		if u, err := url.Parse(pc.URL); err == nil {
			u.User = url.UserPassword(pc.Username, pc.Password)
			rawURL = u.String()
		}
	}
	proxyURL, err := url.Parse(rawURL)
	if err != nil {
		logger.Warn("invalid proxy URL %q, falling back to direct HTTP client: %v", rawURL, err)
		return &http.Client{Timeout: 30 * time.Second}
	}
	tr := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	return &http.Client{Timeout: 30 * time.Second, Transport: tr}
}

// RegisterRoutes registers all Copilot-specific routes
func (h *CopilotHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.POST("/api/providers/copilot/device-login/initiate", lib.ChainMiddlewares(h.initiateDeviceLogin, middlewares...))
	r.POST("/api/providers/copilot/device-login/poll", lib.ChainMiddlewares(h.pollDeviceLogin, middlewares...))
}

// DeviceLoginInitiateResponse is the response from initiating a device login flow
type DeviceLoginInitiateResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// initiateDeviceLogin handles POST /api/providers/copilot/device-login/initiate
// Starts the GitHub OAuth device code flow by requesting a device code.
func (h *CopilotHandler) initiateDeviceLogin(ctx *fasthttp.RequestCtx) {
	body := url.Values{
		"client_id": {githubClientID()},
		"scope":     {"read:user"},
	}.Encode()

	// Derive a stdlib context so that request deadlines propagate as cancellation.
	stdCtx, cancel := fasthttpStdContext(ctx)
	defer cancel()

	req, err := http.NewRequestWithContext(stdCtx, http.MethodPost, h.deviceCodeURL, strings.NewReader(body))
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to create device code request")
		return
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := h.httpClient.Do(req)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadGateway, "failed to contact GitHub for device code")
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxOAuthResponseBytes))
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to read GitHub response")
		return
	}

	if resp.StatusCode != http.StatusOK {
		SendError(ctx, resp.StatusCode, fmt.Sprintf("GitHub returned error: %s", string(respBody)))
		return
	}

	var deviceResp DeviceLoginInitiateResponse
	if err := json.Unmarshal(respBody, &deviceResp); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to parse GitHub device code response")
		return
	}

	if deviceResp.DeviceCode == "" {
		SendError(ctx, fasthttp.StatusBadGateway, "GitHub returned empty device code")
		return
	}

	SendJSON(ctx, deviceResp)
}

// DeviceLoginPollRequest is the request body for polling the device login status
type DeviceLoginPollRequest struct {
	DeviceCode string `json:"device_code"`
}

// DeviceLoginPollResponse is returned when polling succeeds with a token
type DeviceLoginPollResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Status      string `json:"status"` // "complete", "pending", "slow_down", "expired", "error"
	Error       string `json:"error,omitempty"`
}

// pollDeviceLogin handles POST /api/providers/copilot/device-login/poll
// Polls GitHub to check if the user has completed the device code authorization.
func (h *CopilotHandler) pollDeviceLogin(ctx *fasthttp.RequestCtx) {
	var pollReq DeviceLoginPollRequest
	if err := json.Unmarshal(ctx.PostBody(), &pollReq); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}

	if pollReq.DeviceCode == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "device_code is required")
		return
	}

	body := url.Values{
		"client_id":   {githubClientID()},
		"device_code": {pollReq.DeviceCode},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	}.Encode()

	stdCtx, cancel := fasthttpStdContext(ctx)
	defer cancel()

	req, err := http.NewRequestWithContext(stdCtx, http.MethodPost, h.accessTokenURL, strings.NewReader(body))
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to create token request")
		return
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := h.httpClient.Do(req)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadGateway, "failed to contact GitHub for access token")
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxOAuthResponseBytes))
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to read GitHub response")
		return
	}

	// Non-200 status codes (401, 429, 502, etc.) indicate upstream failures — not device-flow states.
	if resp.StatusCode != http.StatusOK {
		SendError(ctx, resp.StatusCode, fmt.Sprintf("GitHub returned unexpected status: %s", string(respBody)))
		return
	}

	// GitHub returns 200 even for pending/error states, with different JSON shapes
	var rawResp map[string]interface{}
	if err := json.Unmarshal(respBody, &rawResp); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to parse GitHub token response")
		return
	}

	// Check for error states (authorization_pending, slow_down, expired_token, etc.)
	if errField, ok := rawResp["error"].(string); ok {
		switch errField {
		case "authorization_pending":
			SendJSON(ctx, DeviceLoginPollResponse{Status: "pending"})
		case "slow_down":
			SendJSON(ctx, DeviceLoginPollResponse{Status: "slow_down"})
		case "expired_token":
			SendJSON(ctx, DeviceLoginPollResponse{Status: "expired", Error: "device code has expired, please restart the flow"})
		case "access_denied":
			SendJSON(ctx, DeviceLoginPollResponse{Status: "error", Error: "authorization was denied by the user"})
		default:
			SendJSON(ctx, DeviceLoginPollResponse{Status: "error", Error: fmt.Sprintf("GitHub error: %s", errField)})
		}
		return
	}

	// Success — extract access token
	accessToken, _ := rawResp["access_token"].(string)
	tokenType, _ := rawResp["token_type"].(string)

	if accessToken == "" {
		SendError(ctx, fasthttp.StatusBadGateway, "GitHub returned empty access token")
		return
	}

	SendJSON(ctx, DeviceLoginPollResponse{
		AccessToken: accessToken,
		TokenType:   tokenType,
		Status:      "complete",
	})
}

// fasthttpStdContext returns a stdlib context.Context that inherits any deadline
// set on the fasthttp.RequestCtx. This avoids using RequestCtx directly as a
// context.Context parent (which can panic in test harnesses where the connection
// is nil).
func fasthttpStdContext(ctx *fasthttp.RequestCtx) (context.Context, context.CancelFunc) {
	if deadline, ok := ctx.Deadline(); ok {
		return context.WithDeadline(context.Background(), deadline)
	}
	return context.Background(), func() {}
}
