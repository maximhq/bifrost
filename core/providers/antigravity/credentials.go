package antigravity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// OAuth client of the Antigravity desktop IDE. These are public installed-app credentials
// (Google treats an installed app's client secret as non-confidential); operators can swap
// in their own OAuth client through the environment.
const (
	defaultClientID     = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	defaultClientSecret = "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"

	envClientID     = "ANTIGRAVITY_CLIENT_ID"
	envClientSecret = "ANTIGRAVITY_CLIENT_SECRET"

	// DefaultRedirectURI is the loopback redirect the Antigravity OAuth client is
	// registered with.
	DefaultRedirectURI = "http://127.0.0.1:51121/callback"

	authEndpoint = "https://accounts.google.com/o/oauth2/v2/auth"

	// Cloud Code Assist hosts. Inference and onboarding use the daily host; project
	// discovery (loadCodeAssist) answers on the production host.
	defaultBaseURL = "https://daily-cloudcode-pa.googleapis.com"
	prodAPIBaseURL = "https://cloudcode-pa.googleapis.com"

	ideVersion = "2.5.5"

	// userAgent must be IDE-shaped: Cloud Code Assist answers 404 NOT_FOUND on newer
	// models for CLI-shaped agents.
	userAgent = "antigravity/ide/" + ideVersion + " (os_type=windows; arch=amd64; aidev_client; auth_method=oauth)"

	oauthRequestTimeout = 30 * time.Second
	onboardAttempts     = 5
	maxOAuthBodyBytes   = 1 << 20
)

// oauthScopes are the scopes the Antigravity IDE requests.
var oauthScopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
	"https://www.googleapis.com/auth/cclog",
	"https://www.googleapis.com/auth/experimentsandconfigs",
}

// Endpoints the OAuth and discovery calls talk to. They are variables only so tests can
// point them at a local server; nothing in production reassigns them.
var (
	tokenEndpoint        = "https://oauth2.googleapis.com/token"
	userinfoEndpoint     = "https://www.googleapis.com/oauth2/v2/userinfo"
	loadCodeAssistURL    = prodAPIBaseURL + "/v1internal:loadCodeAssist"
	onboardUserURL       = defaultBaseURL + "/v1internal:onboardUser"
	onboardRetryInterval = 2 * time.Second
)

func clientID() string {
	if v := strings.TrimSpace(os.Getenv(envClientID)); v != "" {
		return v
	}
	return defaultClientID
}

func clientSecret() string {
	if v := strings.TrimSpace(os.Getenv(envClientSecret)); v != "" {
		return v
	}
	return defaultClientSecret
}

// Credentials is the content of an Antigravity key's value: a Google refresh token for the
// Antigravity OAuth client, plus the Cloud Code Assist project it is bound to.
type Credentials struct {
	RefreshToken string `json:"refresh_token"`
	ProjectID    string `json:"project_id,omitempty"`
	Email        string `json:"email,omitempty"`
}

// ParseCredentials reads a key value that is either a bare refresh token or the JSON form
// {"refresh_token":"...","project_id":"...","email":"..."}.
func ParseCredentials(value string) (*Credentials, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, errors.New("antigravity credential is empty")
	}
	if strings.HasPrefix(value, "{") {
		var creds Credentials
		if err := json.Unmarshal([]byte(value), &creds); err != nil {
			return nil, fmt.Errorf("antigravity credential is not valid JSON: %w", err)
		}
		creds.RefreshToken = strings.TrimSpace(creds.RefreshToken)
		creds.ProjectID = strings.TrimSpace(creds.ProjectID)
		creds.Email = strings.TrimSpace(creds.Email)
		if creds.RefreshToken == "" {
			return nil, errors.New("antigravity credential JSON is missing refresh_token")
		}
		return &creds, nil
	}
	if strings.ContainsAny(value, " \t\r\n\"") {
		return nil, errors.New("antigravity credential must be a refresh token or a JSON object")
	}
	return &Credentials{RefreshToken: value}, nil
}

// Encode renders the credentials as the compact JSON stored in a key's value.
func (c *Credentials) Encode() (string, error) {
	if c == nil || strings.TrimSpace(c.RefreshToken) == "" {
		return "", errors.New("antigravity credential has no refresh token")
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// NewPKCE returns an RFC 7636 S256 verifier (96 random bytes, base64url) and its challenge.
func NewPKCE() (verifier string, challenge string, err error) {
	buf := make([]byte, 96)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("generate PKCE verifier: %w", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

// BuildAuthURL builds the Google consent URL for the Antigravity OAuth client. An empty
// redirectURI uses DefaultRedirectURI.
func BuildAuthURL(redirectURI, state, codeChallenge string) string {
	if redirectURI == "" {
		redirectURI = DefaultRedirectURI
	}
	params := url.Values{}
	params.Set("response_type", "code")
	params.Set("client_id", clientID())
	params.Set("redirect_uri", redirectURI)
	params.Set("scope", strings.Join(oauthScopes, " "))
	params.Set("code_challenge", codeChallenge)
	params.Set("code_challenge_method", "S256")
	params.Set("access_type", "offline")
	params.Set("prompt", "consent")
	params.Set("state", state)
	return authEndpoint + "?" + params.Encode()
}

// ExchangeCode redeems an authorization code, then resolves the account's email and its
// Cloud Code Assist project (onboarding the account onto the free tier when it has none).
// The returned credentials are ready to be stored as a key value.
func ExchangeCode(ctx context.Context, code, redirectURI, codeVerifier string) (*Credentials, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, errors.New("authorization code is empty")
	}
	if redirectURI == "" {
		redirectURI = DefaultRedirectURI
	}
	doer := netHTTPDoer{client: &http.Client{Timeout: oauthRequestTimeout}}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", clientID())
	form.Set("client_secret", clientSecret())
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("code_verifier", codeVerifier)
	tok, err := requestToken(ctx, doer, form)
	if err != nil {
		return nil, err
	}
	if tok.RefreshToken == "" {
		return nil, errors.New("antigravity token response did not include a refresh token")
	}

	creds := &Credentials{RefreshToken: tok.RefreshToken}
	if email, err := fetchUserEmail(ctx, doer, tok.AccessToken); err == nil {
		creds.Email = email
	} else if email := emailFromIDToken(tok.IDToken); email != "" {
		creds.Email = email
	}

	project, err := discoverProject(ctx, doer, tok.AccessToken)
	if err != nil {
		return nil, err
	}
	creds.ProjectID = project

	// Warm the token cache for the value the caller is about to store, so the first
	// request on the new key does not redeem the refresh token again.
	if encoded, encErr := creds.Encode(); encErr == nil {
		seedTokenCache(encoded, &tokenState{
			accessToken:  tok.AccessToken,
			expiresAt:    tok.expiresAt(time.Now()),
			refreshToken: creds.RefreshToken,
			projectID:    project,
			email:        creds.Email,
		})
	}
	return creds, nil
}

// tokenResponse is Google's OAuth token endpoint response.
type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int64  `json:"expires_in"`
	IDToken          string `json:"id_token"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func (t *tokenResponse) expiresAt(now time.Time) time.Time {
	expiresIn := t.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	return now.Add(time.Duration(expiresIn) * time.Second)
}

// tokenError is a non-2xx answer from the token endpoint.
type tokenError struct {
	Status      int
	Code        string
	Description string
}

func (e *tokenError) Error() string {
	msg := fmt.Sprintf("antigravity token request failed with status %d", e.Status)
	if e.Code != "" {
		msg += ": " + e.Code
	}
	if e.Description != "" {
		msg += " (" + e.Description + ")"
	}
	return msg
}

// terminal reports whether the refresh token itself was refused, so retrying cannot help
// until the account is logged in again.
func (e *tokenError) terminal() bool {
	if e.Status != http.StatusBadRequest && e.Status != http.StatusUnauthorized {
		return false
	}
	switch e.Code {
	case "invalid_grant", "unauthorized_client", "invalid_client", "access_denied":
		return true
	}
	return e.Status == http.StatusUnauthorized
}

func requestToken(ctx context.Context, doer httpDoer, form url.Values) (*tokenResponse, error) {
	status, body, err := doer.do(ctx, http.MethodPost, tokenEndpoint, map[string]string{
		"Accept":       "application/json",
		"Content-Type": "application/x-www-form-urlencoded",
	}, []byte(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("antigravity token request failed: %w", err)
	}
	var tok tokenResponse
	_ = json.Unmarshal(body, &tok)
	if status < 200 || status > 299 {
		return nil, &tokenError{Status: status, Code: tok.Error, Description: tok.ErrorDescription}
	}
	if tok.AccessToken == "" {
		return nil, errors.New("antigravity token response did not include an access token")
	}
	return &tok, nil
}

// refreshAccessToken redeems a refresh token. Google keeps the refresh token unchanged
// unless the response carries a new one.
func refreshAccessToken(ctx context.Context, doer httpDoer, refreshToken string) (*tokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", clientID())
	form.Set("client_secret", clientSecret())
	form.Set("refresh_token", refreshToken)
	return requestToken(ctx, doer, form)
}

func fetchUserEmail(ctx context.Context, doer httpDoer, accessToken string) (string, error) {
	status, body, err := doer.do(ctx, http.MethodGet, userinfoEndpoint, map[string]string{
		"Accept":        "application/json",
		"Authorization": "Bearer " + accessToken,
	}, nil)
	if err != nil {
		return "", err
	}
	if status < 200 || status > 299 {
		return "", fmt.Errorf("userinfo request failed with status %d", status)
	}
	var info struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return "", err
	}
	email := strings.ToLower(strings.TrimSpace(info.Email))
	if email == "" {
		return "", errors.New("userinfo response has no email")
	}
	return email, nil
}

// emailFromIDToken reads the email claim of an unverified JWT. It is only a display
// fallback when userinfo is unreachable.
func emailFromIDToken(idToken string) string {
	parts := strings.Split(idToken, ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return ""
	}
	var claims struct {
		Email string `json:"email"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(claims.Email))
}

func discoveryHeaders(accessToken string) map[string]string {
	return map[string]string{
		"Authorization": "Bearer " + accessToken,
		"Accept":        "*/*",
		"Content-Type":  "application/json",
		"User-Agent":    userAgent,
	}
}

// discoverProject finds the Cloud Code Assist project of the account behind accessToken:
// loadCodeAssist first, then onboardUser onto the free tier.
func discoverProject(ctx context.Context, doer httpDoer, accessToken string) (string, error) {
	if project := loadCodeAssist(ctx, doer, accessToken); project != "" {
		return project, nil
	}
	if project := onboardUser(ctx, doer, accessToken); project != "" {
		return project, nil
	}
	return "", errors.New("antigravity could not discover a Cloud Code Assist project for this account; open Antigravity once with this Google account and try again")
}

func loadCodeAssist(ctx context.Context, doer httpDoer, accessToken string) string {
	status, body, err := doer.do(ctx, http.MethodPost, loadCodeAssistURL, discoveryHeaders(accessToken),
		[]byte(`{"metadata":{"ideType":"ANTIGRAVITY"}}`))
	if err != nil || status < 200 || status > 299 {
		return ""
	}
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		return ""
	}
	return extractProjectID(payload)
}

func onboardUser(ctx context.Context, doer httpDoer, accessToken string) string {
	reqBody := []byte(`{"tier_id":"free-tier","metadata":{"ide_type":"ANTIGRAVITY","ide_name":"antigravity","ide_version":"` + ideVersion + `"}}`)
	for attempt := range onboardAttempts {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ""
			case <-time.After(onboardRetryInterval):
			}
		}
		status, body, err := doer.do(ctx, http.MethodPost, onboardUserURL, discoveryHeaders(accessToken), reqBody)
		if err != nil {
			if ctx.Err() != nil {
				return ""
			}
			continue
		}
		if status == http.StatusTooManyRequests || status >= 500 {
			continue
		}
		if status < 200 || status > 299 {
			return ""
		}
		var op struct {
			Done     bool           `json:"done"`
			Response map[string]any `json:"response"`
		}
		if json.Unmarshal(body, &op) != nil {
			continue
		}
		if op.Done {
			return extractProjectID(op.Response)
		}
	}
	return ""
}

// extractProjectID reads the project from a loadCodeAssist / onboardUser payload, where it
// appears either as a string or as an object with an id.
func extractProjectID(payload map[string]any) string {
	for _, field := range []string{"cloudaicompanionProject", "projectId", "project"} {
		switch v := payload[field].(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		case map[string]any:
			if id, ok := v["id"].(string); ok && strings.TrimSpace(id) != "" {
				return strings.TrimSpace(id)
			}
		}
	}
	return ""
}
