// Package client provides the shared HTTP transport used by CLI commands.
package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/maximhq/bifrost/cli/internal/secrets"
)

const (
	maxResponseBytes        = 64 << 20
	maxErrorDisplayBytes    = 1024
	agentVirtualKeyIDHeader = "x-bf-agent-vk-id"
	agentIdentityHeader     = "X-Bifrost-Agent"
	appIdentityHeader       = "X-Bf-App"
	agentAuthRejectedHeader = "x-bf-agent-auth"
)

var agentPolicyRejectionMarkers = []string{"virtual_key_not_found", "virtual_key_required"}

// AuthMode controls which stored credential is attached to a request.
type AuthMode int

const (
	// AuthAuto chooses management auth for /api paths and inference auth otherwise.
	AuthAuto AuthMode = iota
	// AuthNone suppresses all stored credentials.
	AuthNone
	// AuthManagement attaches a management API key or session token.
	AuthManagement
	// AuthInference prefers Enterprise SSO and falls back to a configured virtual key.
	AuthInference
	// AuthAgent attaches the Enterprise browser SSO agent token.
	AuthAgent
)

// Credentials contains the credentials available for one gateway context.
type Credentials struct {
	VirtualKey        string
	ManagementKey     string
	SessionToken      string
	AgentToken        string
	AgentVirtualKeyID string
}

// Request describes one gateway request.
type Request struct {
	Method  string
	Path    string
	Query   url.Values
	Headers http.Header
	Body    []byte
	Auth    AuthMode
}

// Response contains the materialized gateway response.
type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

// APIError represents a non-successful response from the gateway.
type APIError struct {
	Method     string
	URL        string
	StatusCode int
	Header     http.Header
	Body       string
}

// Error formats a concise gateway error without exposing request credentials.
func (e *APIError) Error() string {
	body := truncateErrorBody(strings.TrimSpace(e.Body))
	if body == "" {
		return fmt.Sprintf("%s %s returned HTTP %d", e.Method, e.URL, e.StatusCode)
	}
	return fmt.Sprintf("%s %s returned HTTP %d: %s", e.Method, e.URL, e.StatusCode, body)
}

func truncateErrorBody(body string) string {
	if len(body) <= maxErrorDisplayBytes {
		return body
	}
	prefix := body[:maxErrorDisplayBytes]
	for len(prefix) > 0 && !utf8.ValidString(prefix) {
		prefix = prefix[:len(prefix)-1]
	}
	return fmt.Sprintf("%s… (truncated; %d bytes total)", prefix, len(body))
}

// Client performs authenticated requests against one Bifrost gateway.
type Client struct {
	BaseURL     string
	HTTPClient  *http.Client
	DebugWriter io.Writer
	UserAgent   string
	// HeaderTimeout bounds receipt of response headers. Streaming response
	// bodies are governed only by their request context after headers arrive.
	HeaderTimeout time.Duration
	// RefreshAgentToken rotates a stale Enterprise browser SSO token after an auth rejection.
	RefreshAgentToken  func(context.Context, string) (string, error)
	credentialsMu      sync.RWMutex
	credentials        Credentials
	refreshMu          sync.Mutex
	streamClientMu     sync.Mutex
	streamClient       *http.Client
	streamClientSource *http.Client
}

// New constructs a client with conservative defaults.
func New(baseURL string, credentials Credentials, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{
		BaseURL:       strings.TrimSpace(baseURL),
		HTTPClient:    &http.Client{Timeout: timeout, CheckRedirect: rejectInsecureRedirect},
		UserAgent:     "bifrost-cli/dev",
		HeaderTimeout: timeout,
		credentials:   credentials,
	}
}

// rejectInsecureRedirect blocks a redirect that changes scheme or host.
// Go's default client only strips the Authorization and Cookie-family
// headers, and only when a redirect changes host — a custom header this
// client sends, such as x-bf-vk (the virtual key), is always forwarded
// regardless of host or scheme. So both a same-host HTTPS to HTTP downgrade
// and a same-scheme cross-host redirect would otherwise leak a credential:
// the former over a plaintext connection, the latter to an arbitrary origin.
// A same-host HTTPS-to-HTTP redirect is allowed only for loopback development
// endpoints; changing hosts is never allowed, including redirects to loopback.
func rejectInsecureRedirect(req *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return nil
	}
	previous := via[len(via)-1].URL
	if !strings.EqualFold(previous.Hostname(), req.URL.Hostname()) {
		return fmt.Errorf("refusing to follow redirect to a different host: %s", req.URL)
	}
	if previous.Scheme == "https" && req.URL.Scheme != "https" && !isLoopbackHost(req.URL.Hostname()) {
		return fmt.Errorf("refusing to follow HTTPS to %s redirect to %s", req.URL.Scheme, req.URL)
	}
	return nil
}

// isLoopbackHost reports whether host refers to the local machine.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// CredentialsSnapshot returns a consistent copy of the client's current credentials.
func (c *Client) CredentialsSnapshot() Credentials {
	c.credentialsMu.RLock()
	defer c.credentialsMu.RUnlock()
	return c.credentials
}

// SetAgentToken replaces the in-memory Enterprise bearer after login or refresh.
func (c *Client) SetAgentToken(token string) {
	c.credentialsMu.Lock()
	c.credentials.AgentToken = token
	c.credentialsMu.Unlock()
}

// SetSessionToken replaces the in-memory dashboard session after password login.
func (c *Client) SetSessionToken(token string) {
	c.credentialsMu.Lock()
	c.credentials.SessionToken = token
	c.credentialsMu.Unlock()
}

// SetAgentVirtualKeyID replaces the non-secret assigned-key routing selection.
func (c *Client) SetAgentVirtualKeyID(id string) {
	c.credentialsMu.Lock()
	c.credentials.AgentVirtualKeyID = id
	c.credentialsMu.Unlock()
}

// Do executes a request and returns an APIError for non-2xx status codes.
func (c *Client) Do(ctx context.Context, input Request) (*Response, error) {
	usedAgentToken := strings.TrimSpace(c.CredentialsSnapshot().AgentToken)
	result, err := c.do(ctx, input)
	refresh, refreshErr := c.refreshRejectedAgent(ctx, input, err, usedAgentToken)
	if refreshErr != nil {
		return result, refreshErr
	}
	if !refresh {
		return result, err
	}
	return c.do(ctx, input)
}

// do executes one request attempt without automatic authentication recovery.
func (c *Client) do(ctx context.Context, input Request) (*Response, error) {
	req, err := c.buildRequest(ctx, input)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request gateway: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read gateway response: %w", err)
	}
	if len(body) > maxResponseBytes {
		return nil, fmt.Errorf("gateway response exceeds %d bytes", maxResponseBytes)
	}
	if c.DebugWriter != nil {
		if _, err := fmt.Fprintf(c.DebugWriter, "< HTTP %d (%d bytes)\n", resp.StatusCode, len(body)); err != nil {
			return nil, err
		}
	}
	result := &Response{StatusCode: resp.StatusCode, Header: resp.Header.Clone(), Body: body}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return result, &APIError{Method: req.Method, URL: req.URL.String(), StatusCode: resp.StatusCode, Header: resp.Header.Clone(), Body: string(body)}
	}
	return result, nil
}

// shouldRefreshAgent reports whether a failed request used a refreshable agent token.
func (c *Client) shouldRefreshAgent(input Request, err error, usedAgentToken string) bool {
	if c.RefreshAgentToken == nil || !strings.HasPrefix(usedAgentToken, secrets.AgentAccessTokenPrefix) {
		return false
	}
	mode := ResolveAuthMode(input.Auth, input.Path)
	if input.Headers.Get("Authorization") != "" || (mode == AuthInference && input.Headers.Get("x-bf-vk") != "") {
		return false
	}
	var apiErr *APIError
	if (mode != AuthInference && mode != AuthAgent) || !errors.As(err, &apiErr) {
		return false
	}
	if mode == AuthAgent {
		return apiErr.StatusCode == http.StatusUnauthorized || apiErr.Header.Get(agentAuthRejectedHeader) != ""
	}
	return isAgentTokenRejection(apiErr)
}

// isAgentTokenRejection distinguishes a stale bearer from virtual-key policy failures.
func isAgentTokenRejection(apiErr *APIError) bool {
	if apiErr == nil || (apiErr.StatusCode != http.StatusUnauthorized && apiErr.StatusCode != http.StatusForbidden) {
		return false
	}
	if apiErr.Header.Get(agentAuthRejectedHeader) != "" {
		return true
	}
	// Current gateways mark rejected agent credentials explicitly. Preserve a
	// bare-401 fallback for older gateways, but never rotate a refresh token for
	// an unmarked 403 because it may be a model, budget, or application policy.
	if apiErr.StatusCode == http.StatusForbidden {
		return false
	}
	body := strings.ToLower(apiErr.Body)
	for _, marker := range agentPolicyRejectionMarkers {
		if strings.Contains(body, marker) {
			return false
		}
	}
	return true
}

// ResolveAuthMode classifies gateway-relative paths independently of any base URL prefix.
func ResolveAuthMode(mode AuthMode, path string) AuthMode {
	if mode != AuthAuto {
		return mode
	}
	parsed, err := url.Parse(strings.TrimSpace(path))
	if err == nil && strings.HasPrefix("/"+strings.TrimPrefix(parsed.Path, "/"), "/api/") {
		return AuthManagement
	}
	return AuthInference
}

// refreshRejectedAgent performs the common one-shot authentication recovery
// used by buffered and streaming requests. usedAgentToken is the token the
// failed request actually carried, captured before the request was sent, so
// a rejection processed after a concurrent request has already rotated the
// token is not mistaken for a fresh one that still needs refreshing.
func (c *Client) refreshRejectedAgent(ctx context.Context, input Request, requestErr error, usedAgentToken string) (bool, error) {
	if !c.shouldRefreshAgent(input, requestErr, usedAgentToken) {
		return false, nil
	}
	if _, err := c.refreshAgent(ctx, usedAgentToken); err != nil {
		return false, fmt.Errorf("refresh Enterprise SSO session: %w", err)
	}
	return true, nil
}

// refreshAgent serializes token rotation and reuses a token refreshed by another request.
func (c *Client) refreshAgent(ctx context.Context, staleToken string) (string, error) {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	if current := strings.TrimSpace(c.CredentialsSnapshot().AgentToken); current != "" && current != staleToken {
		return current, nil
	}
	refreshed, err := c.RefreshAgentToken(ctx, staleToken)
	if err != nil {
		return "", err
	}
	refreshed = strings.TrimSpace(refreshed)
	if refreshed == "" {
		return "", errors.New("token refresh returned an empty access token")
	}
	c.SetAgentToken(refreshed)
	return refreshed, nil
}

// Stream executes a request, refreshes a rejected agent session once, and
// copies a successful response incrementally.
func (c *Client) Stream(ctx context.Context, input Request, writer io.Writer) error {
	usedAgentToken := strings.TrimSpace(c.CredentialsSnapshot().AgentToken)
	err := c.stream(ctx, input, writer)
	refresh, refreshErr := c.refreshRejectedAgent(ctx, input, err, usedAgentToken)
	if refreshErr != nil {
		return refreshErr
	}
	if !refresh {
		return err
	}
	return c.stream(ctx, input, writer)
}

// stream executes one streaming request attempt without auth recovery.
func (c *Client) stream(ctx context.Context, input Request, writer io.Writer) error {
	req, err := c.buildRequest(ctx, input)
	if err != nil {
		return err
	}
	resp, err := c.streamingHTTPClient().Do(req)
	if err != nil {
		return fmt.Errorf("request gateway: %w", err)
	}
	defer resp.Body.Close()
	if c.DebugWriter != nil {
		if _, err := fmt.Fprintf(c.DebugWriter, "< HTTP %d (streaming)\n", resp.StatusCode); err != nil {
			return err
		}
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		return &APIError{Method: req.Method, URL: req.URL.String(), StatusCode: resp.StatusCode, Header: resp.Header.Clone(), Body: string(body)}
	}
	if _, err := io.Copy(writer, resp.Body); err != nil {
		return fmt.Errorf("stream gateway response: %w", err)
	}
	return nil
}

// streamingHTTPClient removes the total exchange timeout that would otherwise
// terminate a healthy long-running body. The configured timeout is retained as
// ResponseHeaderTimeout when the transport supports it.
func (c *Client) streamingHTTPClient() *http.Client {
	c.streamClientMu.Lock()
	defer c.streamClientMu.Unlock()
	base := c.HTTPClient
	if base == nil {
		base = &http.Client{}
	}
	if c.streamClient != nil && c.streamClientSource == base {
		return c.streamClient
	}
	streaming := *base
	streaming.Timeout = 0

	var transport *http.Transport
	switch configured := base.Transport.(type) {
	case nil:
		transport = http.DefaultTransport.(*http.Transport).Clone()
	case *http.Transport:
		transport = configured.Clone()
	}
	if transport != nil {
		if transport.ResponseHeaderTimeout == 0 {
			transport.ResponseHeaderTimeout = c.HeaderTimeout
		}
		streaming.Transport = transport
	}
	c.streamClient = &streaming
	c.streamClientSource = base
	return c.streamClient
}

// buildRequest creates an authenticated request shared by buffered and streaming calls.
func (c *Client) buildRequest(ctx context.Context, input Request) (*http.Request, error) {
	endpoint, err := BuildEndpoint(c.BaseURL, input.Path, input.Query)
	if err != nil {
		return nil, err
	}
	method := strings.ToUpper(strings.TrimSpace(input.Method))
	if method == "" {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(input.Body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	for key, values := range input.Headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	if len(input.Body) > 0 && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	c.authorize(req, ResolveAuthMode(input.Auth, input.Path))
	if c.DebugWriter != nil {
		if _, err := fmt.Fprintf(c.DebugWriter, "> %s %s\n", method, endpoint); err != nil {
			return nil, err
		}
	}
	return req, nil
}

// BuildEndpoint resolves a gateway-relative path without permitting host changes.
func BuildEndpoint(baseURL, path string, query url.Values) (string, error) {
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", fmt.Errorf("invalid gateway base URL %q", baseURL)
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		return "", fmt.Errorf("unsupported gateway URL scheme %q", base.Scheme)
	}
	if base.Scheme == "http" && !isLoopbackHost(base.Hostname()) {
		return "", fmt.Errorf("insecure gateway URL %q: HTTPS is required for non-loopback hosts", baseURL)
	}
	requestPath, err := url.Parse(strings.TrimSpace(path))
	if err != nil {
		return "", fmt.Errorf("invalid request path %q: %w", path, err)
	}
	if requestPath.IsAbs() || requestPath.Host != "" {
		return "", fmt.Errorf("request path must be relative to the configured gateway")
	}
	if !strings.HasPrefix(requestPath.Path, "/") {
		requestPath.Path = "/" + requestPath.Path
	}
	baseEscapedPath := strings.TrimSuffix(base.EscapedPath(), "/")
	requestEscapedPath := requestPath.EscapedPath()
	if !strings.HasPrefix(requestEscapedPath, "/") {
		requestEscapedPath = "/" + requestEscapedPath
	}
	base.Path = strings.TrimSuffix(base.Path, "/") + requestPath.Path
	base.RawPath = baseEscapedPath + requestEscapedPath
	values := requestPath.Query()
	for key, entries := range query {
		for _, value := range entries {
			values.Add(key, value)
		}
	}
	base.RawQuery = values.Encode()
	base.Fragment = ""
	return base.String(), nil
}

// authorize applies the selected credentials unless the caller supplied an override.
func (c *Client) authorize(req *http.Request, mode AuthMode) {
	if mode == AuthNone {
		return
	}
	credentials := c.CredentialsSnapshot()
	if mode == AuthManagement && req.Header.Get("Authorization") == "" {
		token := strings.TrimSpace(credentials.ManagementKey)
		if token == "" {
			token = strings.TrimSpace(credentials.SessionToken)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	if mode == AuthInference && req.Header.Get("x-bf-vk") == "" && req.Header.Get("Authorization") == "" {
		if token := strings.TrimSpace(credentials.AgentToken); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
			if selectedID := strings.TrimSpace(credentials.AgentVirtualKeyID); selectedID != "" && req.Header.Get(agentVirtualKeyIDHeader) == "" {
				req.Header.Set(agentVirtualKeyIDHeader, selectedID)
			}
			if req.Header.Get(agentIdentityHeader) == "" {
				req.Header.Set(agentIdentityHeader, c.UserAgent)
			}
			if req.Header.Get(appIdentityHeader) == "" {
				req.Header.Set(appIdentityHeader, "bifrost-cli")
			}
		} else if key := strings.TrimSpace(credentials.VirtualKey); key != "" {
			req.Header.Set("x-bf-vk", key)
		}
	}
	if mode == AuthAgent && req.Header.Get("Authorization") == "" {
		if token := strings.TrimSpace(credentials.AgentToken); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
}
