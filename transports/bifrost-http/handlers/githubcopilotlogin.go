package handlers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/network"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// GitHub's OAuth device flow. It gives a GitHub token for a GitHub Copilot key without a
// redirect URL, so it works for a gateway that the browser reaches on any host.
// https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps#device-flow
var (
	githubDeviceCodeURL  = "https://github.com/login/device/code"
	githubAccessTokenURL = "https://github.com/login/oauth/access_token"

	githubDeviceLoginClient = &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			DialContext: network.SSRFSafeDialContext(10 * time.Second),
		},
	}
)

const (
	githubDeviceGrantType    = "urn:ietf:params:oauth:grant-type:device_code"
	githubDeviceLoginScope   = "read:user"
	maxGithubDeviceLoginBody = 64 * 1024
)

// Device login states returned by the poll endpoint.
const (
	GithubDeviceLoginPending  = "pending"
	GithubDeviceLoginComplete = "complete"
	GithubDeviceLoginExpired  = "expired"
	GithubDeviceLoginError    = "error"
)

// GithubDeviceLoginRequest starts a device login for an OAuth app.
type GithubDeviceLoginRequest struct {
	ClientID string `json:"client_id"`
}

// GithubDeviceLoginResponse carries the code that the user enters at VerificationURI.
type GithubDeviceLoginResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// GithubDeviceLoginPollRequest asks whether the user has approved the device login.
type GithubDeviceLoginPollRequest struct {
	ClientID   string `json:"client_id"`
	DeviceCode string `json:"device_code"`
}

// GithubDeviceLoginPollResponse reports the state of a device login. AccessToken is set
// only when Status is complete. Interval is set when GitHub asks for slower polling.
type GithubDeviceLoginPollResponse struct {
	Status      string `json:"status"`
	AccessToken string `json:"access_token,omitempty"`
	Interval    int    `json:"interval,omitempty"`
	Error       string `json:"error,omitempty"`
}

// githubDeviceLoginReply is the union of GitHub's replies on both device flow endpoints.
type githubDeviceLoginReply struct {
	GithubDeviceLoginResponse
	AccessToken      string `json:"access_token"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// initiateGithubDeviceLogin handles POST /api/providers/{provider}/device-login/initiate
func (h *ProviderHandler) initiateGithubDeviceLogin(ctx *fasthttp.RequestCtx) {
	if !isGithubCopilotRoute(ctx) {
		return
	}
	var req GithubDeviceLoginRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	clientID := strings.TrimSpace(req.ClientID)
	if clientID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "client_id is required")
		return
	}

	reply, err := postGithubDeviceLogin(ctx, githubDeviceCodeURL, url.Values{
		"client_id": {clientID},
		"scope":     {githubDeviceLoginScope},
	})
	if err != nil {
		logger.Warn("github device login could not be started: %v", err)
		SendError(ctx, fasthttp.StatusBadGateway, "could not reach GitHub to start the device login")
		return
	}
	if reply.DeviceCode == "" || reply.UserCode == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "GitHub refused the device login: "+githubDeviceLoginReason(reply))
		return
	}
	SendJSON(ctx, reply.GithubDeviceLoginResponse)
}

// pollGithubDeviceLogin handles POST /api/providers/{provider}/device-login/poll
func (h *ProviderHandler) pollGithubDeviceLogin(ctx *fasthttp.RequestCtx) {
	if !isGithubCopilotRoute(ctx) {
		return
	}
	var req GithubDeviceLoginPollRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}
	clientID, deviceCode := strings.TrimSpace(req.ClientID), strings.TrimSpace(req.DeviceCode)
	if clientID == "" || deviceCode == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "client_id and device_code are required")
		return
	}

	reply, err := postGithubDeviceLogin(ctx, githubAccessTokenURL, url.Values{
		"client_id":   {clientID},
		"device_code": {deviceCode},
		"grant_type":  {githubDeviceGrantType},
	})
	if err != nil {
		logger.Warn("github device login could not be polled: %v", err)
		SendError(ctx, fasthttp.StatusBadGateway, "could not reach GitHub to poll the device login")
		return
	}

	switch {
	case reply.AccessToken != "":
		SendJSON(ctx, GithubDeviceLoginPollResponse{Status: GithubDeviceLoginComplete, AccessToken: reply.AccessToken})
	case reply.Error == "authorization_pending":
		SendJSON(ctx, GithubDeviceLoginPollResponse{Status: GithubDeviceLoginPending})
	case reply.Error == "slow_down":
		SendJSON(ctx, GithubDeviceLoginPollResponse{Status: GithubDeviceLoginPending, Interval: reply.Interval})
	case reply.Error == "expired_token":
		SendJSON(ctx, GithubDeviceLoginPollResponse{Status: GithubDeviceLoginExpired})
	default:
		SendJSON(ctx, GithubDeviceLoginPollResponse{Status: GithubDeviceLoginError, Error: githubDeviceLoginReason(reply)})
	}
}

// isGithubCopilotRoute writes a 400 and returns false when the route is not for the GitHub
// Copilot provider. Device login exists only for that provider.
func isGithubCopilotRoute(ctx *fasthttp.RequestCtx) bool {
	provider, err := getProviderFromCtx(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid provider: %v", err))
		return false
	}
	if provider != schemas.GithubCopilot {
		SendError(ctx, fasthttp.StatusBadRequest, "device login is only available for the github-copilot provider")
		return false
	}
	return true
}

// postGithubDeviceLogin sends one device flow request to GitHub and decodes the reply.
// GitHub reports flow errors in the body, with a 200 or a 4xx, so the status is not checked.
func postGithubDeviceLogin(ctx context.Context, endpoint string, form url.Values) (*githubDeviceLoginReply, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := githubDeviceLoginClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxGithubDeviceLoginBody))
	if err != nil {
		return nil, err
	}
	var reply githubDeviceLoginReply
	if err := sonic.Unmarshal(body, &reply); err != nil {
		return nil, fmt.Errorf("unexpected reply from GitHub (status %d)", resp.StatusCode)
	}
	return &reply, nil
}

// githubDeviceLoginReason returns GitHub's own words for a refused device login.
func githubDeviceLoginReason(reply *githubDeviceLoginReply) string {
	switch {
	case reply.ErrorDescription != "":
		return reply.ErrorDescription
	case reply.Error != "":
		return reply.Error
	default:
		return "no reason given"
	}
}
