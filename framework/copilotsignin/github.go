package copilotsignin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	defaultDeviceCodeURL = "https://github.com/login/device/code"
	defaultTokenURL      = "https://github.com/login/oauth/access_token"
	defaultUserURL       = "https://api.github.com/user"
	deviceCodeGrantType  = "urn:ietf:params:oauth:grant-type:device_code"
	maxResponseBytes     = 1 << 20
	userAgent            = "Bifrost-Copilot/1.0"
)

// endpoints are the GitHub URLs the device flow talks to; tests point them at a fake.
type endpoints struct {
	deviceCodeURL string
	tokenURL      string
	userURL       string
}

var githubEndpoints = endpoints{
	deviceCodeURL: defaultDeviceCodeURL,
	tokenURL:      defaultTokenURL,
	userURL:       defaultUserURL,
}

// DeviceCode is what the operator needs to finish signing in on github.com.
type DeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// githubTokenResponse is a successful device-flow poll or refresh grant. Failures arrive as
// HTTP 200 with an error field and are returned by postForm as a githubOAuthError.
type githubTokenResponse struct {
	AccessToken           string `json:"access_token"`
	ExpiresIn             int64  `json:"expires_in"`
	RefreshToken          string `json:"refresh_token"`
	RefreshTokenExpiresIn int64  `json:"refresh_token_expires_in"`
}

// githubOAuthError is an error GitHub returned in the response body. It is final for the
// request that produced it, unlike a transport failure, which is worth retrying.
type githubOAuthError struct {
	Code        string
	Description string
	Interval    int // the poll interval GitHub asks for with slow_down
}

func (e *githubOAuthError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Description)
	}
	return e.Code
}

// postForm sends a form to a GitHub OAuth endpoint and decodes the JSON reply.
func postForm(ctx context.Context, client *http.Client, target string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach GitHub: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("could not read GitHub's response: %w", err)
	}
	var probe struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
		Interval         int    `json:"interval"`
	}
	if jsonErr := json.Unmarshal(body, &probe); jsonErr == nil && probe.Error != "" {
		return &githubOAuthError{Code: probe.Error, Description: probe.ErrorDescription, Interval: probe.Interval}
	}
	if resp.StatusCode == http.StatusNotFound {
		// GitHub answers an unknown client ID with a bare 404.
		return &githubOAuthError{Code: "incorrect_client_credentials", Description: "GitHub does not recognize this client ID"}
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub returned HTTP %d", resp.StatusCode)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("could not parse GitHub's response: %w", err)
	}
	return nil
}

// lookupLogin returns the GitHub login a user token belongs to, or "" when it cannot tell.
// It only labels the sign-in in the UI, so a failure is not an error.
func lookupLogin(ctx context.Context, client *http.Client, userURL, accessToken string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, userURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
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
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&user); err != nil {
		return ""
	}
	return user.Login
}
