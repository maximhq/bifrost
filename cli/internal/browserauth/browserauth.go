// Package browserauth implements Enterprise browser SSO for the Bifrost CLI.
package browserauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/maximhq/bifrost/cli/internal/client"
)

const (
	clientID               = "bifrost-cli"
	callbackPath           = "/callback"
	defaultLoginWait       = 5 * time.Minute
	defaultExchangeTimeout = 30 * time.Second
	defaultShutdownTimeout = 2 * time.Second
	maxResponseBytes       = 1 << 20
)

const signInCompletePage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Bifrost CLI sign-in complete</title>
<style>
:root { color-scheme: light dark; font-family: ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; }
* { box-sizing: border-box; }
body { margin: 0; min-height: 100vh; display: grid; place-items: center; padding: 24px; background: #f6f7f8; color: #111827; }
main { width: min(576px, 100%); padding: 32px; text-align: center; background: #fff; border: 1px solid #dfe3e8; border-radius: 4px; }
.icon { width: 48px; height: 48px; margin: 0 auto 20px; display: grid; place-items: center; border-radius: 50%; background: #e7f4ef; color: #087f5b; }
.icon svg { width: 24px; height: 24px; }
h1 { margin: 0; font-size: 20px; line-height: 28px; font-weight: 600; letter-spacing: -.025em; }
p { margin: 8px 0 0; color: #6b7280; font-size: 14px; line-height: 20px; }
@media (max-width: 639px) {
  body { padding: 12px; }
  main { padding: 16px; }
}
@media (prefers-color-scheme: dark) {
  body { background: #151719; color: #f9fafb; }
  main { background: #1f2225; border-color: #34383d; box-shadow: none; }
  .icon { background: #123b31; color: #55d6a9; }
  p { color: #a9afb8; }
}
</style>
</head>
<body><main><div class="icon" aria-hidden="true"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"/><path d="m9 11 3 3L22 4"/></svg></div><h1>Bifrost CLI sign-in complete</h1><p>You can close this window</p></main></body>
</html>`

// User is the Enterprise identity returned after browser authentication.
type User struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// TokenResponse contains the opaque agent credentials issued to the CLI.
type TokenResponse struct {
	AccessToken          string `json:"agent_access_token"`
	RefreshToken         string `json:"refresh_token"`
	ExpiresIn            int    `json:"expires_in"`
	RefreshExpiresIn     int    `json:"refresh_expires_in"`
	User                 User   `json:"user"`
	SelectedVirtualKeyID string `json:"selected_virtual_key_id"`
	ForcedVirtualKeyID   string `json:"forced_virtual_key_id"`
}

// Status describes the browser authentication capability of a gateway.
type Status struct {
	IdPConfigured         bool  `json:"idp_configured"`
	VirtualKeyAuthEnabled *bool `json:"virtual_key_auth_enabled"`
}

// Client performs the native browser authentication flow against one gateway.
type Client struct {
	BaseURL         string
	HTTPClient      *http.Client
	UserAgent       string
	Version         string
	DeviceName      string
	CallbackWait    time.Duration
	exchangeTimeout time.Duration
	shutdownTimeout time.Duration
	OpenBrowser     func(string) error
	Authorization   func(string)
	Warning         func(error)
}

// callbackResult carries one validated callback response back to SignIn.
type callbackResult struct {
	response TokenResponse
	err      error
}

// CheckStatus discovers whether the gateway supports Enterprise browser SSO.
func (c *Client) CheckStatus(ctx context.Context) (Status, error) {
	var status Status
	if err := c.doJSON(ctx, http.MethodGet, "/api/agent/auth/status", nil, &status); err != nil {
		return status, err
	}
	return status, nil
}

// SignIn opens the browser and exchanges a loopback callback code using PKCE.
func (c *Client) SignIn(ctx context.Context, noBrowser bool) (TokenResponse, error) {
	var response TokenResponse
	status, err := c.CheckStatus(ctx)
	if err != nil {
		return response, fmt.Errorf("discover browser sign-in: %w", err)
	}
	if !status.IdPConfigured {
		return response, errors.New("browser SSO is not enabled on this gateway")
	}

	verifier, err := randomValue(32)
	if err != nil {
		return response, fmt.Errorf("create PKCE verifier: %w", err)
	}
	state, err := randomValue(32)
	if err != nil {
		return response, fmt.Errorf("create login state: %w", err)
	}
	challengeBytes := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeBytes[:])

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return response, fmt.Errorf("listen for browser callback: %w", err)
	}
	defer listener.Close()
	redirectURI := "http://" + listener.Addr().String() + callbackPath
	deviceName := strings.TrimSpace(c.DeviceName)
	if deviceName == "" {
		deviceName = "Bifrost CLI"
	}
	resultChannel := make(chan callbackResult, 1)
	server := callbackServer(state, resultChannel, func(code string) (TokenResponse, error) {
		payload := map[string]string{
			"code": code, "code_verifier": verifier, "redirect_uri": redirectURI,
			"platform": runtime.GOOS, "agent_version": c.Version, "device_name": deviceName,
		}
		var exchanged TokenResponse
		exchangeTimeout := c.exchangeTimeout
		if exchangeTimeout <= 0 {
			exchangeTimeout = defaultExchangeTimeout
		}
		exchangeCtx, cancelExchange := context.WithTimeout(ctx, exchangeTimeout)
		defer cancelExchange()
		if exchangeErr := c.doJSON(exchangeCtx, http.MethodPost, "/api/agent/auth/token", payload, &exchanged); exchangeErr != nil {
			return TokenResponse{}, fmt.Errorf("exchange browser authorization code: %w", exchangeErr)
		}
		if strings.TrimSpace(exchanged.AccessToken) == "" || strings.TrimSpace(exchanged.RefreshToken) == "" {
			return TokenResponse{}, errors.New("gateway returned an incomplete CLI session")
		}
		return exchanged, nil
	})
	go func() {
		_ = server.Serve(listener)
	}()
	defer func() {
		shutdownTimeout := c.shutdownTimeout
		if shutdownTimeout <= 0 {
			shutdownTimeout = defaultShutdownTimeout
		}
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancelShutdown()
		if shutdownErr := server.Shutdown(shutdownCtx); shutdownErr != nil {
			_ = server.Close()
		}
	}()

	authorizeURL, err := c.authorizationURL(redirectURI, state, challenge)
	if err != nil {
		return response, err
	}
	if c.Authorization != nil {
		c.Authorization(authorizeURL)
	}
	if !noBrowser {
		opener := c.OpenBrowser
		if opener == nil {
			opener = openBrowser
		}
		if err := opener(authorizeURL); err != nil {
			if c.Warning != nil {
				c.Warning(fmt.Errorf("could not open the browser automatically: %w", err))
			}
		}
	}

	wait := c.CallbackWait
	if wait <= 0 {
		wait = defaultLoginWait
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	var callback callbackResult
	select {
	case <-ctx.Done():
		return response, ctx.Err()
	case <-timer.C:
		return response, errors.New("timed out waiting for browser sign-in")
	case callback = <-resultChannel:
	}
	if callback.err != nil {
		return response, callback.err
	}
	return callback.response, nil
}

// Refresh rotates an Enterprise agent session using its refresh token.
func (c *Client) Refresh(ctx context.Context, refreshToken string) (TokenResponse, error) {
	var response TokenResponse
	if strings.TrimSpace(refreshToken) == "" {
		return response, errors.New("no Enterprise SSO refresh token is stored")
	}
	if err := c.doJSON(ctx, http.MethodPost, "/api/agent/auth/refresh", map[string]string{"refresh_token": refreshToken}, &response); err != nil {
		return response, err
	}
	if strings.TrimSpace(response.AccessToken) == "" || strings.TrimSpace(response.RefreshToken) == "" {
		return response, errors.New("gateway returned an incomplete refreshed Enterprise SSO session")
	}
	return response, nil
}

// Logout revokes the supplied Enterprise agent access token.
func (c *Client) Logout(ctx context.Context, accessToken string) error {
	return c.doJSONWithToken(ctx, http.MethodPost, "/api/agent/auth/logout", nil, accessToken, nil)
}

// authorizationURL constructs the gateway URL without permitting a host override.
func (c *Client) authorizationURL(redirectURI, state, challenge string) (string, error) {
	return client.BuildEndpoint(c.BaseURL, "/api/agent/auth/authorize", url.Values{
		"client_id": {clientID}, "redirect_uri": {redirectURI}, "state": {state},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"},
	})
}

// callbackServer validates state, exchanges the one-time authorization code,
// and shows a CLI-specific completion page only after the exchange succeeds.
func callbackServer(expectedState string, results chan<- callbackResult, exchange func(string) (TokenResponse, error)) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		writer.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		if request.Method != http.MethodGet {
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		actualState := request.URL.Query().Get("state")
		if subtle.ConstantTimeCompare([]byte(actualState), []byte(expectedState)) != 1 {
			http.Error(writer, "Invalid sign-in state. Return to the terminal and try again.", http.StatusBadRequest)
			sendCallbackResult(results, callbackResult{err: errors.New("browser callback state did not match")})
			return
		}
		if providerError := request.URL.Query().Get("error"); providerError != "" {
			http.Error(writer, "Sign-in was not completed. Return to the terminal.", http.StatusBadRequest)
			sendCallbackResult(results, callbackResult{err: fmt.Errorf("browser sign-in failed: %s", providerError)})
			return
		}
		code := strings.TrimSpace(request.URL.Query().Get("code"))
		if code == "" {
			http.Error(writer, "Missing authorization code. Return to the terminal and try again.", http.StatusBadRequest)
			sendCallbackResult(results, callbackResult{err: errors.New("browser callback did not contain an authorization code")})
			return
		}
		response, err := exchange(code)
		if err != nil {
			http.Error(writer, "Sign-in could not be completed. Return to the terminal and try again.", http.StatusBadGateway)
			sendCallbackResult(results, callbackResult{err: err})
			return
		}
		_, _ = io.WriteString(writer, signInCompletePage)
		sendCallbackResult(results, callbackResult{response: response})
	})
	return &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
}

// sendCallbackResult reports only the first terminal callback outcome.
func sendCallbackResult(results chan<- callbackResult, result callbackResult) {
	select {
	case results <- result:
	default:
	}
}

// doJSON sends an unauthenticated JSON request.
func (c *Client) doJSON(ctx context.Context, method, path string, input any, output any) error {
	return c.doJSONWithToken(ctx, method, path, input, "", output)
}

// doJSONWithToken sends a bounded JSON request with an optional bearer token.
func (c *Client) doJSONWithToken(ctx context.Context, method, path string, input any, token string, output any) error {
	endpoint, err := client.BuildEndpoint(c.BaseURL, path, nil)
	if err != nil {
		return err
	}
	var body io.Reader
	if input != nil {
		encoded, marshalErr := json.Marshal(input)
		if marshalErr != nil {
			return marshalErr
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if c.UserAgent != "" {
		request.Header.Set("User-Agent", c.UserAgent)
	}
	if strings.TrimSpace(token) != "" {
		request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	}
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxResponseBytes {
		return errors.New("gateway authentication response is too large")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return &client.APIError{Method: method, URL: endpoint, StatusCode: response.StatusCode, Header: response.Header.Clone(), Body: string(data)}
	}
	if output != nil && len(data) > 0 {
		if err := json.Unmarshal(data, output); err != nil {
			return fmt.Errorf("decode authentication response: %w", err)
		}
	}
	return nil
}

// randomValue returns a base64url token with the requested byte entropy.
func randomValue(length int) (string, error) {
	value := make([]byte, length)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

// openBrowser launches the platform browser without invoking a shell.
func openBrowser(target string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", target)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		command = exec.Command("xdg-open", target)
	}
	if err := command.Start(); err != nil {
		return err
	}
	go func() { _ = command.Wait() }()
	return nil
}
