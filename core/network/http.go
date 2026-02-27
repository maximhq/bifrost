// Package network provides centralized HTTP client management with proxy support.
// It allows runtime proxy configuration updates that propagate to all HTTP clients.
package network

import (
	"crypto/tls"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttpproxy"
)

// ClientPurpose defines the intended use of an HTTP client for proxy filtering
type ClientPurpose string

const (
	// ClientPurposeSCIM is used for SCIM/OAuth provider requests
	ClientPurposeSCIM ClientPurpose = "scim"
	// ClientPurposeInference is used for LLM inference requests
	ClientPurposeInference ClientPurpose = "inference"
	// ClientPurposeAPI is used for general API requests (guardrails, etc.)
	ClientPurposeAPI ClientPurpose = "api"
)

// DefaultClientConfig holds default timeout values for HTTP clients
var DefaultClientConfig = struct {
	ReadTimeout         time.Duration
	WriteTimeout        time.Duration
	MaxIdleConnDuration time.Duration
	MaxConnsPerHost     int
}{
	ReadTimeout:         60 * time.Second,
	WriteTimeout:        60 * time.Second,
	MaxIdleConnDuration: 30 * time.Second,
	MaxConnsPerHost:     200,
}

// GlobalProxyType represents the type of global proxy
type GlobalProxyType string

const (
	GlobalProxyTypeHTTP   GlobalProxyType = "http"
	GlobalProxyTypeSOCKS5 GlobalProxyType = "socks5"
	GlobalProxyTypeTCP    GlobalProxyType = "tcp"
)

// GlobalProxyConfig represents the global proxy configuration
type GlobalProxyConfig struct {
	Enabled       bool            `json:"enabled"`
	Type          GlobalProxyType `json:"type"`                      // "http", "socks5", "tcp"
	URL           string          `json:"url"`                       // Proxy URL (e.g., http://proxy.example.com:8080)
	Username      string          `json:"username,omitempty"`        // Optional authentication username
	Password      string          `json:"password,omitempty"`        // Optional authentication password
	NoProxy       string          `json:"no_proxy,omitempty"`        // Comma-separated list of hosts to bypass proxy
	Timeout       int             `json:"timeout,omitempty"`         // Connection timeout in seconds
	SkipTLSVerify bool            `json:"skip_tls_verify,omitempty"` // Skip TLS certificate verification
	// Entity enablement flags
	EnableForSCIM      bool `json:"enable_for_scim"`      // Enable proxy for SCIM requests (enterprise only)
	EnableForInference bool `json:"enable_for_inference"` // Enable proxy for inference requests
	EnableForAPI       bool `json:"enable_for_api"`       // Enable proxy for API requests
}

// HTTPClientFactory manages HTTP clients with centralized proxy configuration.
// It supports both fasthttp and standard net/http clients with purpose-based
// proxy enablement (SCIM, Inference, API).
type HTTPClientFactory struct {
	mu          sync.RWMutex
	proxyConfig *GlobalProxyConfig

	// Cached clients per purpose - lazily initialized
	fasthttpClients map[ClientPurpose]*fasthttp.Client
	httpClients     map[ClientPurpose]*http.Client

	logger schemas.Logger
}

// NewHTTPClientFactory creates a new HTTP client factory with the given proxy configuration.
// Pass nil for proxyConfig if proxy is not yet configured.
func NewHTTPClientFactory(proxyConfig *GlobalProxyConfig, logger schemas.Logger) *HTTPClientFactory {
	return &HTTPClientFactory{
		proxyConfig:     proxyConfig,
		fasthttpClients: make(map[ClientPurpose]*fasthttp.Client, 3),
		httpClients:     make(map[ClientPurpose]*http.Client, 3),
		logger:          logger,
	}
}

// UpdateProxyConfig updates the proxy configuration and recreates all cached clients.
// This is thread-safe and can be called at runtime.
func (f *HTTPClientFactory) UpdateProxyConfig(config *GlobalProxyConfig) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.proxyConfig = config

	// Clear cached clients - they will be recreated on next request
	f.fasthttpClients = make(map[ClientPurpose]*fasthttp.Client, 3)
	f.httpClients = make(map[ClientPurpose]*http.Client, 3)
}

// GetProxyConfig returns the current proxy configuration (thread-safe read)
func (f *HTTPClientFactory) GetProxyConfig() *GlobalProxyConfig {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.proxyConfig
}

// isProxyEnabledForPurpose checks if proxy should be used for the given purpose
func (f *HTTPClientFactory) isProxyEnabledForPurpose(purpose ClientPurpose) bool {
	if f.proxyConfig == nil || !f.proxyConfig.Enabled {
		return false
	}

	switch purpose {
	case ClientPurposeSCIM:
		return f.proxyConfig.EnableForSCIM
	case ClientPurposeInference:
		return f.proxyConfig.EnableForInference
	case ClientPurposeAPI:
		return f.proxyConfig.EnableForAPI
	default:
		return false
	}
}

// shouldBypassProxy checks if a host matches a noProxy pattern
// Supported patterns:
//   - "*" matches all hosts
//   - ".example.com" matches example.com and all subdomains
//   - "*.example.com" matches subdomains of example.com only
//   - exact host match
func shouldBypassProxy(host, pattern string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	pattern = strings.ToLower(strings.TrimSpace(pattern))

	if pattern == "*" {
		return true
	}
	if pattern == host {
		return true
	}
	// .example.com matches example.com and *.example.com
	if strings.HasPrefix(pattern, ".") {
		suffix := pattern[1:] // remove leading dot
		return host == suffix || strings.HasSuffix(host, pattern)
	}
	// *.example.com matches subdomains only
	if strings.HasPrefix(pattern, "*.") {
		suffix := pattern[1:] // keep the dot, e.g., ".example.com"
		return strings.HasSuffix(host, suffix)
	}
	return false
}

// GetFasthttpClient returns a fasthttp client configured for the given purpose.
// If proxy is enabled for this purpose, the client will be configured with proxy settings.
// Clients are cached and reused until proxy config changes.
func (f *HTTPClientFactory) GetFasthttpClient(purpose ClientPurpose) *fasthttp.Client {
	f.mu.RLock()
	if client, ok := f.fasthttpClients[purpose]; ok {
		f.mu.RUnlock()
		return client
	}
	f.mu.RUnlock()

	f.mu.Lock()
	defer f.mu.Unlock()

	// Double-check after acquiring write lock
	if client, ok := f.fasthttpClients[purpose]; ok {
		return client
	}

	client := f.createFasthttpClient(purpose)
	f.fasthttpClients[purpose] = client
	return client
}

// GetHTTPClient returns a standard net/http client configured for the given purpose.
// If proxy is enabled for this purpose, the client will be configured with proxy settings.
// Clients are cached and reused until proxy config changes.
func (f *HTTPClientFactory) GetHTTPClient(purpose ClientPurpose) *http.Client {
	f.mu.RLock()
	if client, ok := f.httpClients[purpose]; ok {
		f.mu.RUnlock()
		return client
	}
	f.mu.RUnlock()

	f.mu.Lock()
	defer f.mu.Unlock()

	// Double-check after acquiring write lock
	if client, ok := f.httpClients[purpose]; ok {
		return client
	}

	client := f.createHTTPClient(purpose)
	f.httpClients[purpose] = client
	return client
}

// createFasthttpClient creates a new fasthttp client with appropriate proxy settings
func (f *HTTPClientFactory) createFasthttpClient(purpose ClientPurpose) *fasthttp.Client {
	client := &fasthttp.Client{
		ReadTimeout:         DefaultClientConfig.ReadTimeout,
		WriteTimeout:        DefaultClientConfig.WriteTimeout,
		MaxIdleConnDuration: DefaultClientConfig.MaxIdleConnDuration,
		MaxConnsPerHost:     DefaultClientConfig.MaxConnsPerHost,
		RetryIfErr:          StaleConnectionRetryIfErr,
	}

	// Configure proxy if enabled for this purpose
	if f.isProxyEnabledForPurpose(purpose) {
		f.configureFasthttpProxy(client)
	}

	// Configure TLS if skip verification is set
	if f.proxyConfig != nil {
		if f.proxyConfig.SkipTLSVerify {
			f.logger.Warn("skipping TLS verification for fasthttp client because skip TLS verify is set to true. It's not recommended to use this in production.")
		}
		client.TLSConfig = &tls.Config{
			InsecureSkipVerify: f.proxyConfig.SkipTLSVerify,
			MinVersion:         tls.VersionTLS12,
		}
	}

	return client
}

// StaleConnectionRetryIfErr is a RetryIfErr callback that retries requests when the failure
// is due to a stale/dead connection being reused from the pool. This addresses intermittent
// "cannot find whitespace in the first line of response" errors caused by connection reuse
// with leftover chunked transfer encoding data (see: https://github.com/valyala/fasthttp/issues/1743).
//
// By default fasthttp only retries idempotent requests (GET/HEAD/PUT). LLM inference requests
// use POST, so without this they fail immediately on stale connections. Retrying is safe here
// because the error occurs during response header parsing — before the server processes the
// new request, or on a connection the server has already closed.
func StaleConnectionRetryIfErr(_ *fasthttp.Request, attempts int, err error) (resetTimeout bool, retry bool) {
	if attempts > 1 {
		return false, false
	}
	if err == nil {
		return false, false
	}
	errStr := err.Error()
	// io.EOF — server closed the connection (fasthttp converts this to
	//   ErrConnectionClosed AFTER the retry loop, so RetryIfErr sees raw EOF)
	// "cannot find whitespace in the first line of response" — stale chunked data in buffer
	// "connection reset by peer" — server RST'd the idle connection
	if err == io.EOF ||
		strings.Contains(errStr, "cannot find whitespace") ||
		strings.Contains(errStr, "connection reset by peer") {
		return true, true
	}
	return false, false
}

// buildProxyURLWithAuth adds authentication to a proxy URL if credentials are provided
func (f *HTTPClientFactory) buildProxyURLWithAuth() string {
	proxyURL := f.proxyConfig.URL
	if f.proxyConfig.Username != "" && f.proxyConfig.Password != "" {
		parsedURL, err := url.Parse(f.proxyConfig.URL)
		if err == nil {
			parsedURL.User = url.UserPassword(f.proxyConfig.Username, f.proxyConfig.Password)
			proxyURL = parsedURL.String()
		}
	}
	return proxyURL
}

// configureFasthttpProxy configures proxy for a fasthttp client
func (f *HTTPClientFactory) configureFasthttpProxy(client *fasthttp.Client) {
	if f.proxyConfig == nil || f.proxyConfig.URL == "" {
		return
	}

	proxyURL := f.buildProxyURLWithAuth()
	var dialFunc fasthttp.DialFunc

	switch f.proxyConfig.Type {
	case GlobalProxyTypeHTTP:
		dialFunc = fasthttpproxy.FasthttpHTTPDialer(proxyURL)
	case GlobalProxyTypeSOCKS5:
		dialFunc = fasthttpproxy.FasthttpSocksDialer(proxyURL)
	}

	proxyCfg := f.proxyConfig
	if dialFunc != nil {
		client.Dial = func(addr string) (net.Conn, error) {
			if proxyCfg.NoProxy != "" {
				host := strings.Split(addr, ":")[0]
				if host == "" {
					host = addr
				}
				if shouldBypassProxy(host, proxyCfg.NoProxy) {
					return net.Dial("tcp", addr)
				}
			}
			return dialFunc(addr)
		}
	}
}

// createHTTPClient creates a new standard net/http client with appropriate proxy settings
func (f *HTTPClientFactory) createHTTPClient(purpose ClientPurpose) *http.Client {
	transport := &http.Transport{
		MaxIdleConns:          DefaultClientConfig.MaxConnsPerHost,
		MaxIdleConnsPerHost:   DefaultClientConfig.MaxConnsPerHost,
		IdleConnTimeout:       DefaultClientConfig.MaxIdleConnDuration,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: DefaultClientConfig.ReadTimeout,
		ExpectContinueTimeout: 1 * time.Second,
		DisableCompression:    false,
		DisableKeepAlives:     false,
	}

	// Configure proxy if enabled for this purpose
	if f.isProxyEnabledForPurpose(purpose) {
		f.configureHTTPProxy(transport)
	}

	// Configure TLS if skip verification is set
	if f.proxyConfig != nil {
		if f.proxyConfig.SkipTLSVerify {
			f.logger.Warn("skipping TLS verification for fasthttp client because skip TLS verify is set to true. It's not recommended to use this in production.")
		}
		transport.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: f.proxyConfig.SkipTLSVerify,
			MinVersion:         tls.VersionTLS12,
		}
	}

	timeout := DefaultClientConfig.ReadTimeout
	if f.proxyConfig != nil && f.proxyConfig.Timeout > 0 {
		timeout = time.Duration(f.proxyConfig.Timeout) * time.Second
	}

	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
	}
}

// configureHTTPProxy configures proxy for a standard net/http transport
func (f *HTTPClientFactory) configureHTTPProxy(transport *http.Transport) {
	if f.proxyConfig == nil || f.proxyConfig.URL == "" {
		return
	}

	proxyURL, err := url.Parse(f.proxyConfig.URL)
	if err != nil {
		return
	}

	// Add authentication if provided
	if f.proxyConfig.Username != "" && f.proxyConfig.Password != "" {
		proxyURL.User = url.UserPassword(f.proxyConfig.Username, f.proxyConfig.Password)

		// For HTTPS requests through HTTP proxy, the CONNECT method is used to establish a tunnel.
		// Proxy authentication must be sent via ProxyConnectHeader for the CONNECT request.
		// Without this, the proxy will reject/reset the connection before the TLS handshake.
		basicAuth := "Basic " + base64.StdEncoding.EncodeToString(
			[]byte(f.proxyConfig.Username+":"+f.proxyConfig.Password),
		)
		transport.ProxyConnectHeader = http.Header{
			"Proxy-Authorization": {basicAuth},
		}
	}

	// Capture noProxy patterns at creation time to avoid data race with UpdateProxyConfig.
	// The closure below is called for each request and would otherwise read f.proxyConfig
	// concurrently with writes from UpdateProxyConfig.
	var noProxyPatterns []string
	if f.proxyConfig.NoProxy != "" {
		noProxyPatterns = strings.Split(f.proxyConfig.NoProxy, ",")
	}

	proxyCfg := f.proxyConfig
	transport.Proxy = func(req *http.Request) (*url.URL, error) {
		// Use Hostname() to get the host without port for matching.
		// req.URL.Host is "host:port" but no_proxy patterns are host-only.
		if proxyCfg.NoProxy != "" {
			host := req.URL.Hostname()
			if host == "" {
				host = req.URL.Host
			}
			for _, np := range noProxyPatterns {
				if shouldBypassProxy(host, np) {
					return nil, nil
				}
			}
		}
		return proxyURL, nil
	}
}
