package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/network"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// githubCopilotClientID is GitHub's own Copilot editor App. Copilot exchanges only tokens
// issued to the GitHub Apps it recognises, so a sign-in through any other App's client ID
// yields a token Copilot refuses. Its tokens do not expire, so nothing needs refreshing.
const githubCopilotClientID = "Iv1.b507a08c87ecfe98"

const (
	githubDeviceCodeURL      = "https://github.com/login/device/code"
	githubAccessTokenURL     = "https://github.com/login/oauth/access_token"
	githubUserURL            = "https://api.github.com/user"
	githubDeviceCodeGrant    = "urn:ietf:params:oauth:grant-type:device_code"
	githubSignInTimeout      = 30 * time.Second
	githubSignInMaxBodyBytes = 1 << 20
)

// githubSignInEndpoints are the GitHub URLs the device flow talks to; tests point them at a fake.
type githubSignInEndpoints struct {
	deviceCodeURL string
	tokenURL      string
	userURL       string
}

// GithubCopilotSignInHandler serves "Sign in with GitHub" for GitHub Copilot keys. The browser
// cannot run GitHub's device flow itself (GitHub sends no CORS headers), so the gateway calls
// GitHub and hands the resulting token back to the key form, which saves it as the key's value.
type GithubCopilotSignInHandler struct {
	client    *http.Client
	endpoints githubSignInEndpoints
}

// NewGithubCopilotSignInHandler creates the sign-in handler.
func NewGithubCopilotSignInHandler() *GithubCopilotSignInHandler {
	return &GithubCopilotSignInHandler{
		client: &http.Client{Timeout: githubSignInTimeout, Transport: network.DefaultTransport(network.ClientPurposeAPI)},
		endpoints: githubSignInEndpoints{
			deviceCodeURL: githubDeviceCodeURL,
			tokenURL:      githubAccessTokenURL,
			userURL:       githubUserURL,
		},
	}
}

// RegisterRoutes registers the sign-in routes.
func (h *GithubCopilotSignInHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.POST("/api/providers/{provider}/sign-in", lib.ChainMiddlewares(h.start, middlewares...))
	r.POST("/api/providers/{provider}/sign-in/poll", lib.ChainMiddlewares(h.poll, middlewares...))
}

// githubDeviceCode is what the operator needs to finish signing in on github.com.
type githubDeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// githubSignInPollResult is the outcome of one poll. Token is set only when Status is complete.
type githubSignInPollResult struct {
	Status   string `json:"status"`             // pending, complete, expired or denied
	Interval int    `json:"interval,omitempty"` // seconds to wait before the next poll
	Token    string `json:"token,omitempty"`
	Login    string `json:"login,omitempty"`
}

// githubOAuthError is an error GitHub returned in a 200 response body.
type githubOAuthError struct {
	Code        string
	Description string
	Interval    int // the poll interval GitHub asks for with slow_down
}

func (e *githubOAuthError) Error() string {
	if e.Description != "" {
		return e.Code + ": " + e.Description
	}
	return e.Code
}

// start asks GitHub for a device code.
//
// POST /api/providers/github-copilot/sign-in
func (h *GithubCopilotSignInHandler) start(ctx *fasthttp.RequestCtx) {
	if !isGithubCopilotRoute(ctx) {
		return
	}
	var code githubDeviceCode
	err := h.postForm(context.Background(), h.endpoints.deviceCodeURL, url.Values{"client_id": {githubCopilotClientID}, "scope": {"read:user"}}, &code)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadGateway, fmt.Sprintf("GitHub rejected the sign-in: %v", err))
		return
	}
	if code.DeviceCode == "" || code.UserCode == "" {
		SendError(ctx, fasthttp.StatusBadGateway, "GitHub did not return a device code")
		return
	}
	SendJSON(ctx, code)
}

// poll checks once whether the operator finished on github.com, and returns the token if so.
//
// POST /api/providers/github-copilot/sign-in/poll
func (h *GithubCopilotSignInHandler) poll(ctx *fasthttp.RequestCtx) {
	if !isGithubCopilotRoute(ctx) {
		return
	}
	var req struct {
		DeviceCode string `json:"device_code"`
	}
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil || strings.TrimSpace(req.DeviceCode) == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "device_code is required")
		return
	}
	var token struct {
		AccessToken string `json:"access_token"`
	}
	err := h.postForm(context.Background(), h.endpoints.tokenURL, url.Values{
		"client_id":   {githubCopilotClientID},
		"device_code": {req.DeviceCode},
		"grant_type":  {githubDeviceCodeGrant},
	}, &token)
	var oauthErr *githubOAuthError
	if errors.As(err, &oauthErr) {
		switch oauthErr.Code {
		case "authorization_pending":
			SendJSON(ctx, githubSignInPollResult{Status: "pending"})
			return
		case "slow_down":
			SendJSON(ctx, githubSignInPollResult{Status: "pending", Interval: oauthErr.Interval})
			return
		case "expired_token":
			SendJSON(ctx, githubSignInPollResult{Status: "expired"})
			return
		case "access_denied":
			SendJSON(ctx, githubSignInPollResult{Status: "denied"})
			return
		case "incorrect_device_code", "bad_verification_code":
			SendError(ctx, fasthttp.StatusBadRequest, "GitHub does not recognize this sign-in attempt; start the sign-in again")
			return
		}
	}
	if err != nil {
		SendError(ctx, fasthttp.StatusBadGateway, fmt.Sprintf("GitHub rejected the sign-in: %v", err))
		return
	}
	if token.AccessToken == "" {
		SendError(ctx, fasthttp.StatusBadGateway, "GitHub did not return an access token")
		return
	}
	SendJSON(ctx, githubSignInPollResult{
		Status: "complete",
		Token:  token.AccessToken,
		Login:  h.lookupLogin(context.Background(), token.AccessToken),
	})
}

// isGithubCopilotRoute refuses the sign-in routes for any other provider.
func isGithubCopilotRoute(ctx *fasthttp.RequestCtx) bool {
	if provider, _ := ctx.UserValue("provider").(string); schemas.ModelProvider(provider) != schemas.GithubCopilot {
		SendError(ctx, fasthttp.StatusBadRequest, "sign-in is only available for github-copilot")
		return false
	}
	return true
}

// postForm sends a form to a GitHub OAuth endpoint and decodes the JSON reply. GitHub reports
// OAuth failures as HTTP 200 with an error field, returned here as a githubOAuthError. Callers
// pass a background context, not the fasthttp request: the client's timeout bounds the call.
func (h *GithubCopilotSignInHandler) postForm(ctx context.Context, target string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := h.client.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach GitHub: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, githubSignInMaxBodyBytes))
	if err != nil {
		return fmt.Errorf("could not read GitHub's response: %w", err)
	}
	var probe struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
		Interval         int    `json:"interval"`
	}
	if json.Unmarshal(body, &probe) == nil && probe.Error != "" {
		return &githubOAuthError{Code: probe.Error, Description: probe.ErrorDescription, Interval: probe.Interval}
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub returned HTTP %d", resp.StatusCode)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("could not parse GitHub's response: %w", err)
	}
	return nil
}

// lookupLogin returns the GitHub login a token belongs to, or "" when it cannot tell. It only
// labels the sign-in in the UI, so a failure is not an error.
func (h *GithubCopilotSignInHandler) lookupLogin(ctx context.Context, token string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.endpoints.userURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := h.client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var user struct {
		Login string `json:"login"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, githubSignInMaxBodyBytes)).Decode(&user) != nil {
		return ""
	}
	return user.Login
}
