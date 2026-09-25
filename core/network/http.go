// Package network provides centralized HTTP client management with proxy support.
// It allows runtime proxy configuration updates that propagate to all HTTP clients.
package network

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
	xproxy "golang.org/x/net/proxy"
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
	MaxConnDuration     time.Duration
	MaxConnsPerHost     int
}{
	ReadTimeout:         60 * time.Second,
	WriteTimeout:        60 * time.Second,
	MaxIdleConnDuration: 30 * time.Second,
	MaxConnDuration:     300 * time.Second,
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
//
// Clients handed out are live: GetFasthttpClient, GetHTTPClient, HTTPClientWithTLS
// and PolicyTransport return the same object for the life of the factory, and every
// request made through it runs on an inner client built for the proxy config current
// at that moment. UpdateProxyConfig therefore reaches every caller, including ones
// that stored the client when they were created, without anyone rebuilding anything.
type HTTPClientFactory struct {
	mu          sync.RWMutex
	proxyConfig *GlobalProxyConfig

	// Live clients returned to callers, one per key. Never replaced.
	liveFasthttp map[ClientPurpose]*fasthttp.Client
	liveHTTP     map[httpClientKey]*http.Client

	// Inner clients built for the current proxy config, lazily. UpdateProxyConfig
	// drops them so the next request builds new ones.
	fasthttpClients map[ClientPurpose]*fasthttp.Client
	httpClients     map[httpClientKey]*http.Client

	// Fasthttp read/write buffer sizes. Zero unless a caller opts in via
	// WithFasthttpBufferSizes; when set, applied to the SCIM client only
	// (see createFasthttpClient).
	readBufferSize  int
	writeBufferSize int

	logger schemas.Logger
}

// FactoryOption customizes an HTTPClientFactory at construction time.
type FactoryOption func(*HTTPClientFactory)

// WithFasthttpBufferSizes overrides the fasthttp read/write buffer sizes used for
// created clients. Non-positive values leave the corresponding field at zero, which
// selects fasthttp's default buffer size.
func WithFasthttpBufferSizes(read, write int) FactoryOption {
	return func(f *HTTPClientFactory) {
		if read > 0 {
			f.readBufferSize = read
		}
		if write > 0 {
			f.writeBufferSize = write
		}
	}
}

// NewHTTPClientFactory creates a new HTTP client factory with the given proxy configuration.
// Pass nil for proxyConfig if proxy is not yet configured.
func NewHTTPClientFactory(proxyConfig *GlobalProxyConfig, logger schemas.Logger, opts ...FactoryOption) *HTTPClientFactory {
	f := &HTTPClientFactory{
		proxyConfig:     proxyConfig,
		liveFasthttp:    make(map[ClientPurpose]*fasthttp.Client, 3),
		liveHTTP:        make(map[httpClientKey]*http.Client, 3),
		fasthttpClients: make(map[ClientPurpose]*fasthttp.Client, 3),
		httpClients:     make(map[httpClientKey]*http.Client, 3),
		logger:          logger,
	}
	for _, opt := range opts {
		opt(f)
	}
	return f
}

// UpdateProxyConfig updates the proxy configuration. The next request through any
// client this factory handed out runs on a new inner client built for it; idle
// connections of the old inner clients are closed. This is thread-safe and can be
// called at runtime.
func (f *HTTPClientFactory) UpdateProxyConfig(config *GlobalProxyConfig) {
	f.mu.Lock()
	oldFasthttp := f.fasthttpClients
	oldHTTP := f.httpClients
	f.proxyConfig = config
	f.fasthttpClients = make(map[ClientPurpose]*fasthttp.Client, 3)
	f.httpClients = make(map[httpClientKey]*http.Client, 3)
	f.mu.Unlock()

	// In-flight requests keep their connections; only idle ones go, so a request
	// that starts after the update can never reuse a connection to the old proxy.
	for _, client := range oldFasthttp {
		client.CloseIdleConnections()
	}
	for _, client := range oldHTTP {
		client.CloseIdleConnections()
	}
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

// httpClientKey identifies one kind of net/http client the factory builds.
type httpClientKey struct {
	purpose ClientPurpose
	tls     *tls.Config // HTTPClientWithTLS: the caller's TLS settings
	policy  *DialPolicy // PolicyTransport: which targets may be reached
}

// GetFasthttpClient returns the live fasthttp client for purpose. The same client is
// returned on every call; each request runs on an inner client configured with the
// current proxy settings for purpose (see HTTPClientFactory).
func (f *HTTPClientFactory) GetFasthttpClient(purpose ClientPurpose) *fasthttp.Client {
	f.mu.RLock()
	client, ok := f.liveFasthttp[purpose]
	f.mu.RUnlock()
	if ok {
		return client
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if client, ok := f.liveFasthttp[purpose]; ok {
		return client
	}
	// The outer client carries the pool settings callers may inspect, but every
	// request is handed to the inner client by liveFasthttpTransport.
	client = f.newFasthttpBaseClient(purpose)
	client.Transport = &liveFasthttpTransport{factory: f, purpose: purpose}
	f.liveFasthttp[purpose] = client
	return client
}

// currentFasthttpClient returns the inner fasthttp client for the current proxy config.
func (f *HTTPClientFactory) currentFasthttpClient(purpose ClientPurpose) *fasthttp.Client {
	f.mu.RLock()
	client, ok := f.fasthttpClients[purpose]
	f.mu.RUnlock()
	if ok {
		return client
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if client, ok := f.fasthttpClients[purpose]; ok {
		return client
	}
	client = f.createFasthttpClient(purpose)
	f.fasthttpClients[purpose] = client
	return client
}

// liveFasthttpTransport hands each request to the factory's current inner client.
// fasthttp keeps DoTimeout/DoDeadline deadlines on the request itself, so they reach
// the inner client unchanged. It never asks the outer client to retry: the inner
// client already applies its own retry policy.
type liveFasthttpTransport struct {
	factory *HTTPClientFactory
	purpose ClientPurpose
}

func (t *liveFasthttpTransport) RoundTrip(_ *fasthttp.HostClient, req *fasthttp.Request, resp *fasthttp.Response) (bool, error) {
	return false, t.factory.currentFasthttpClient(t.purpose).Do(req, resp)
}

// GetHTTPClient returns the live net/http client for purpose. The same client is
// returned on every call; each request runs on an inner client configured with the
// current proxy settings for purpose (see HTTPClientFactory).
//
// The client is shared by every caller for purpose: set per-request limits through
// the request context, never by changing the client's fields.
func (f *HTTPClientFactory) GetHTTPClient(purpose ClientPurpose) *http.Client {
	return f.liveHTTPClient(httpClientKey{purpose: purpose})
}

// HTTPClientWithTLS returns a live net/http client for purpose that uses tlsConfig
// for TLS to the target, for callers that bring their own CA or client certificate.
// Proxy settings follow purpose like GetHTTPClient. The global skip_tls_verify still
// applies on top. The factory keeps one client per tlsConfig pointer, so pass the same
// *tls.Config for the life of the caller rather than a new one per request.
func (f *HTTPClientFactory) HTTPClientWithTLS(purpose ClientPurpose, tlsConfig *tls.Config) *http.Client {
	return f.liveHTTPClient(httpClientKey{purpose: purpose, tls: tlsConfig})
}

// DialPolicy decides which targets a client may reach, for callers that fetch
// user-controlled URLs (webhooks, skills, plugin downloads). Build one with SSRFPolicy
// or NewDialPolicy, once per caller: the factory keeps one transport per policy pointer.
type DialPolicy struct {
	dial        func(ctx context.Context, netw, addr string) (net.Conn, error)
	checkTarget func(ctx context.Context, host string) error
}

// NewDialPolicy returns a policy that connects directly with dial, and judges the
// target host of every proxied hop with checkTarget before the request is sent.
func NewDialPolicy(dial func(ctx context.Context, netw, addr string) (net.Conn, error), checkTarget func(ctx context.Context, host string) error) *DialPolicy {
	return &DialPolicy{dial: dial, checkTarget: checkTarget}
}

// SSRFPolicy is the standard policy for user-controlled URLs: public targets only,
// plus the hosts in allow (nil permits none). Direct connections go through
// SSRFSafeDialContextWithAllowlist, which checks every resolved address at dial time;
// dial time is bounded by the request context.
func SSRFPolicy(allow *Allowlist) *DialPolicy {
	return SSRFPolicyWithDialTimeout(0, allow)
}

// SSRFPolicyWithDialTimeout is SSRFPolicy with each direct dial also bounded by
// dialTimeout (0 leaves it to the request context).
func SSRFPolicyWithDialTimeout(dialTimeout time.Duration, allow *Allowlist) *DialPolicy {
	return NewDialPolicy(SSRFSafeDialContextWithAllowlist(dialTimeout, allow), publicTargetCheck(net.DefaultResolver, allow))
}

// PolicyTransport returns a live RoundTripper that honours the global proxy for purpose
// while enforcing policy. Direct connections, including no_proxy matches, use the
// policy's dialer. Once a request is proxied the dial goes to the proxy, which is
// operator configuration and is let through; the target of every hop, redirects
// included, is judged by the policy's check instead (see NewTargetCheckingTransport for
// the DNS caveat). The caller keeps its own http.Client for timeouts and redirect rules:
// the transport adds no timeout of its own and requires TLS 1.2 or later.
func (f *HTTPClientFactory) PolicyTransport(purpose ClientPurpose, policy *DialPolicy) http.RoundTripper {
	return f.liveHTTPClient(httpClientKey{purpose: purpose, policy: policy}).Transport
}

// liveHTTPClient returns the live client for key, creating it on first use.
func (f *HTTPClientFactory) liveHTTPClient(key httpClientKey) *http.Client {
	f.mu.RLock()
	client, ok := f.liveHTTP[key]
	f.mu.RUnlock()
	if ok {
		return client
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if client, ok := f.liveHTTP[key]; ok {
		return client
	}
	client = &http.Client{Transport: &liveHTTPTransport{factory: f, key: key}}
	f.liveHTTP[key] = client
	return client
}

// currentHTTPClient returns the inner net/http client for key and the current proxy config.
func (f *HTTPClientFactory) currentHTTPClient(key httpClientKey) *http.Client {
	f.mu.RLock()
	client, ok := f.httpClients[key]
	f.mu.RUnlock()
	if ok {
		return client
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if client, ok := f.httpClients[key]; ok {
		return client
	}
	client = f.createHTTPClient(key)
	f.httpClients[key] = client
	return client
}

// liveHTTPTransport hands each request to the factory's current inner client, and
// applies that client's timeout (the global proxy timeout) through the request
// context, released when the response body is closed.
type liveHTTPTransport struct {
	factory *HTTPClientFactory
	key     httpClientKey
}

func (t *liveHTTPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	inner := t.factory.currentHTTPClient(t.key)
	if inner.Timeout <= 0 {
		return inner.Transport.RoundTrip(req)
	}
	ctx, cancel := context.WithTimeout(req.Context(), inner.Timeout)
	resp, err := inner.Transport.RoundTrip(req.WithContext(ctx))
	if err != nil {
		cancel()
		return nil, err
	}
	resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

// CloseIdleConnections lets http.Client.CloseIdleConnections reach the inner client.
func (t *liveHTTPTransport) CloseIdleConnections() {
	t.factory.currentHTTPClient(t.key).CloseIdleConnections()
}

// cancelOnClose releases a request context once the caller is done with the body.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

// createFasthttpClient creates an inner fasthttp client with the current proxy settings.
// The caller holds f.mu.
func (f *HTTPClientFactory) createFasthttpClient(purpose ClientPurpose) *fasthttp.Client {
	client := f.newFasthttpBaseClient(purpose)

	f.configureFasthttpProxy(client, purpose)

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

// newFasthttpBaseClient returns a fasthttp client with the factory's pool, timeout and
// buffer settings for purpose, and no proxy.
func (f *HTTPClientFactory) newFasthttpBaseClient(purpose ClientPurpose) *fasthttp.Client {
	client := &fasthttp.Client{
		ReadTimeout:         DefaultClientConfig.ReadTimeout,
		WriteTimeout:        DefaultClientConfig.WriteTimeout,
		MaxIdleConnDuration: DefaultClientConfig.MaxIdleConnDuration,
		MaxConnDuration:     DefaultClientConfig.MaxConnDuration,
		MaxConnsPerHost:     DefaultClientConfig.MaxConnsPerHost,
		MaxConnWaitTimeout:  DefaultClientConfig.ReadTimeout,
		ConnPoolStrategy:    fasthttp.FIFO,
		RetryIfErr:          StaleConnectionRetryIfErr,
	}

	// Larger header buffers only for SCIM/OAuth, and only when a caller opted in via
	// WithFasthttpBufferSizes: IdP token endpoints can return response headers
	// exceeding fasthttp's 4KB default ("small read buffer"). Every other purpose,
	// and any factory whose caller didn't set a size, keeps fasthttp's default.
	if purpose == ClientPurposeSCIM {
		if f.readBufferSize > 0 {
			client.ReadBufferSize = f.readBufferSize
		}
		if f.writeBufferSize > 0 {
			client.WriteBufferSize = f.writeBufferSize
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
// maxStaleConnRetries bounds how many times a single request will redial on a stale
// pooled connection. With FIFO pooling, the oldest connection is tried first, and when
// the upstream has a short keep-alive (e.g. vLLM's default 5s) several pooled connections
// can be dead at once - so a single retry can hit a second stale connection and still fail
// (see https://github.com/maximhq/bifrost/issues/4496). A small bound lets the request walk
// past a few dead connections to a live one while staying well under fasthttp's internal
// attempt cap. Retrying is safe here because the failure occurs before the server processes
// the request (during dial / response-header parsing).
//
// Since maximhq/bifrost#7035 this callback is only reached for failures on a
// connection reused from the pool: contextTransport.RoundTrip reports failures
// on a freshly dialed socket with retry=false, so a real upstream failure is
// counted against Bifrost's own max_retries instead of being retried here.
const maxStaleConnRetries = 3

func StaleConnectionRetryIfErr(_ *fasthttp.Request, attempts int, err error) (resetTimeout bool, retry bool) {
	if attempts > maxStaleConnRetries {
		return false, false
	}
	if err == nil {
		return false, false
	}
	errStr := strings.ToLower(err.Error())
	// ErrConnectionClosed — server closed the connection before returning the first
	//   response byte. fasthttp converts raw io.EOF to this AFTER the retry loop, so
	//   RetryIfErr normally sees raw io.EOF; we match both to stay robust across versions.
	// io.EOF / io.ErrUnexpectedEOF — server closed the connection.
	// "cannot find whitespace in the first line of response" — stale chunked data in buffer.
	// reset / broken pipe / closed connection variants — server or intermediary closed idle conn.
	if errors.Is(err, fasthttp.ErrConnectionClosed) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		strings.Contains(errStr, "cannot find whitespace") ||
		strings.Contains(errStr, "connection reset by peer") ||
		strings.Contains(errStr, "broken pipe") ||
		strings.Contains(errStr, "use of closed network connection") ||
		strings.Contains(errStr, "server closed connection") {
		return true, true
	}
	return false, false
}

// configureFasthttpProxy points a fasthttp client at the proxy for purpose. It sets
// DialTimeout rather than Dial: fasthttp calls a plain Dial without any deadline, so a
// proxy that accepts the connection and never answers CONNECT would hang the request
// past DoTimeout. DialTimeout receives the request's remaining time, which bounds the
// dial and the proxy handshake. The caller holds f.mu.
func (f *HTTPClientFactory) configureFasthttpProxy(client *fasthttp.Client, purpose ClientPurpose) {
	proxyURL := f.proxyURLForPurpose(purpose)
	if proxyURL == nil {
		return
	}
	proxyCfg := f.proxyConfig
	client.DialTimeout = func(addr string, timeout time.Duration) (net.Conn, error) {
		if timeout <= 0 || timeout > ProxyHandshakeTimeout {
			timeout = ProxyHandshakeTimeout
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if connectDirect(DialAddrHost(addr), proxyCfg) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
		}
		return DialViaProxy(ctx, proxyURL, addr)
	}
}

// MatchesNoProxy reports whether host matches any entry of a comma-separated
// no_proxy list, using shouldBypassProxy's pattern rules per entry. An empty list
// matches nothing.
func MatchesNoProxy(host, noProxy string) bool {
	if strings.TrimSpace(noProxy) == "" {
		return false
	}
	for _, pattern := range strings.Split(noProxy, ",") {
		if strings.TrimSpace(pattern) != "" && shouldBypassProxy(host, pattern) {
			return true
		}
	}
	return false
}

// DialAddrHost extracts the host from a dial target for no_proxy matching.
// SplitHostPort unwraps IPv6 brackets ("[::1]:8080" -> "::1"); naive splitting
// on ":" would mangle IPv6 literals.
func DialAddrHost(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		host = strings.Trim(addr, "[]")
	}
	return host
}

// createHTTPClient creates an inner net/http client for key with the current proxy
// settings. The caller holds f.mu.
func (f *HTTPClientFactory) createHTTPClient(key httpClientKey) *http.Client {
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		MaxIdleConns:          DefaultClientConfig.MaxConnsPerHost,
		MaxIdleConnsPerHost:   DefaultClientConfig.MaxConnsPerHost,
		IdleConnTimeout:       DefaultClientConfig.MaxIdleConnDuration,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: DefaultClientConfig.ReadTimeout,
		ExpectContinueTimeout: 1 * time.Second,
		DisableCompression:    false,
		DisableKeepAlives:     false,
		// Disable HTTP/2 — these clients are used for auxiliary purposes (proxy/SCIM/API)
		// where HTTP/1.1 is sufficient. Without this, Go's http2 package auto-registers
		// h2 via TLSNextProto in init(), causing unintended HTTP/2 connections.
		TLSNextProto: make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),
	}

	// TLS: the caller's settings (HTTPClientWithTLS), with the global
	// skip_tls_verify layered on top.
	if key.tls != nil {
		transport.TLSClientConfig = key.tls.Clone()
	}
	if f.proxyConfig != nil {
		if f.proxyConfig.SkipTLSVerify {
			f.logger.Warn("skipping TLS verification for net/http client because skip TLS verify is set to true. It's not recommended to use this in production.")
		}
		if transport.TLSClientConfig == nil {
			transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		}
		if f.proxyConfig.SkipTLSVerify {
			transport.TLSClientConfig.InsecureSkipVerify = true
		}
	}

	proxyURL := f.proxyURLForPurpose(key.purpose)
	if proxyURL != nil {
		f.configureHTTPProxy(transport, proxyURL)
	}

	var roundTripper http.RoundTripper = transport
	timeout := DefaultClientConfig.ReadTimeout
	if f.proxyConfig != nil && f.proxyConfig.Timeout > 0 {
		timeout = time.Duration(f.proxyConfig.Timeout) * time.Second
	}
	if policy := key.policy; policy != nil {
		// Policy transports are bounded by the caller's client and request context.
		timeout = 0
		transport.ResponseHeaderTimeout = 0
		if transport.TLSClientConfig == nil {
			transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		}
		transport.DialContext = policy.dial
		if proxyURL != nil {
			// Once proxied, the dial goes to the proxy: let that one address through
			// and judge the target per hop instead.
			proxyAddr := proxyDialAddr(proxyURL)
			transport.DialContext = func(ctx context.Context, netw, addr string) (net.Conn, error) {
				if addr == proxyAddr {
					return dialer.DialContext(ctx, netw, addr)
				}
				return policy.dial(ctx, netw, addr)
			}
			roundTripper = &targetCheckingTransport{check: policy.checkTarget, next: transport}
		}
	}

	return &http.Client{
		Transport: roundTripper,
		Timeout:   timeout,
	}
}

// proxyURLForPurpose returns the proxy URL (with credentials) for purpose, or nil when
// the proxy is off for purpose or not configured. The caller holds f.mu.
func (f *HTTPClientFactory) proxyURLForPurpose(purpose ClientPurpose) *url.URL {
	if !f.isProxyEnabledForPurpose(purpose) || f.proxyConfig.URL == "" {
		return nil
	}
	raw := f.proxyConfig.URL
	if !strings.Contains(raw, "://") {
		// A bare host:port takes its scheme from the proxy type.
		scheme := "http"
		if f.proxyConfig.Type == GlobalProxyTypeSOCKS5 {
			scheme = "socks5"
		}
		raw = scheme + "://" + raw
	}
	proxyURL, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	if f.proxyConfig.Username != "" && f.proxyConfig.Password != "" {
		proxyURL.User = url.UserPassword(f.proxyConfig.Username, f.proxyConfig.Password)
	}
	return proxyURL
}

// configureHTTPProxy points transport at proxyURL, honouring no_proxy and never
// proxying local or instance-metadata targets. The caller holds f.mu.
func (f *HTTPClientFactory) configureHTTPProxy(transport *http.Transport, proxyURL *url.URL) {
	// For HTTPS requests through an HTTP proxy, CONNECT establishes the tunnel and
	// proxy authentication must ride on it, or the proxy resets the connection before
	// the TLS handshake.
	if user := proxyURL.User; user != nil && (proxyURL.Scheme == "http" || proxyURL.Scheme == "https") {
		password, _ := user.Password()
		transport.ProxyConnectHeader = http.Header{
			"Proxy-Authorization": {"Basic " + base64.StdEncoding.EncodeToString([]byte(user.Username()+":"+password))},
		}
	}

	// Capture the config now: the closure runs per request and must not race with
	// UpdateProxyConfig.
	proxyCfg := f.proxyConfig
	transport.Proxy = func(req *http.Request) (*url.URL, error) {
		host := req.URL.Hostname()
		if host == "" {
			host = req.URL.Host
		}
		if connectDirect(host, proxyCfg) {
			return nil, nil
		}
		return proxyURL, nil
	}
}

// connectDirect reports whether a request for host skips the proxy: a no_proxy match,
// or a local or instance-metadata target.
func connectDirect(host string, proxyCfg *GlobalProxyConfig) bool {
	return MatchesNoProxy(host, proxyCfg.NoProxy) || IsLocalOrMetadataHost(host)
}

// IsLocalOrMetadataHost reports whether host is loopback, link-local, or a named
// cloud instance-metadata endpoint. Credential chains reach those on purpose (Azure
// managed identity calls IMDS at 169.254.169.254, Azure Arc's agent listens on
// localhost, Google's metadata server answers at metadata.google.internal), and a
// corporate proxy cannot reach any of them, so they always connect directly.
func IsLocalOrMetadataHost(host string) bool {
	switch strings.ToLower(strings.TrimSuffix(host, ".")) {
	case "localhost", "metadata.google.internal", "metadata":
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || IsMetadataEndpoint(ip)
}

// proxyDialAddr mirrors the host:port net/http dials for a proxy URL, defaulting the
// port by scheme when the URL leaves it out.
func proxyDialAddr(proxyURL *url.URL) string {
	port := proxyURL.Port()
	if port == "" {
		switch proxyURL.Scheme {
		case "https":
			port = "443"
		case "socks5", "socks5h":
			port = "1080"
		default:
			port = "80"
		}
	}
	return net.JoinHostPort(proxyURL.Hostname(), port)
}

// GRPCDialer returns a context dialer for gRPC clients (grpc.WithContextDialer) that
// honours the global proxy for purpose (see DialViaProxy); no_proxy, local and
// metadata targets connect directly. The proxy config is read on every dial, so a
// proxy change applies to the next connection. Setting a custom dialer turns off
// gRPC's own HTTPS_PROXY handling, which is intended: the global proxy is the one
// source.
func (f *HTTPClientFactory) GRPCDialer(purpose ClientPurpose) func(ctx context.Context, addr string) (net.Conn, error) {
	return func(ctx context.Context, addr string) (net.Conn, error) {
		f.mu.RLock()
		proxyURL := f.proxyURLForPurpose(purpose)
		proxyCfg := f.proxyConfig
		f.mu.RUnlock()

		if proxyURL == nil || connectDirect(DialAddrHost(addr), proxyCfg) {
			return (&net.Dialer{KeepAlive: 30 * time.Second}).DialContext(ctx, "tcp", addr)
		}
		return DialViaProxy(ctx, proxyURL, addr)
	}
}

// ProxyHandshakeTimeout bounds connecting to a proxy and completing its handshake when
// the caller's context sets no earlier deadline. A proxy that accepts connections but
// never answers would otherwise hang every request routed through it.
const ProxyHandshakeTimeout = 30 * time.Second

// DialViaProxy opens a connection to addr through proxyURL: CONNECT for an http proxy,
// the SOCKS5 handshake (with the URL's credentials, RFC 1929) for socks5 and socks5h.
// The proxy is dialed dual-stack, so IPv6 proxies work. ctx bounds the dial and the
// handshake, or ProxyHandshakeTimeout when ctx has no deadline; it does not govern the
// returned connection.
func DialViaProxy(ctx context.Context, proxyURL *url.URL, addr string) (net.Conn, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, ProxyHandshakeTimeout)
		defer cancel()
	}
	dialer := &net.Dialer{KeepAlive: 30 * time.Second}
	switch proxyURL.Scheme {
	case "socks5", "socks5h":
		var auth *xproxy.Auth
		if user := proxyURL.User; user != nil {
			password, _ := user.Password()
			auth = &xproxy.Auth{User: user.Username(), Password: password}
		}
		socks, err := xproxy.SOCKS5("tcp", proxyDialAddr(proxyURL), auth, dialer)
		if err != nil {
			return nil, fmt.Errorf("socks5 proxy: %w", err)
		}
		return socks.(xproxy.ContextDialer).DialContext(ctx, "tcp", addr)
	case "http", "":
		return dialHTTPConnect(ctx, dialer, proxyURL, addr)
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q", proxyURL.Scheme)
	}
}

// dialHTTPConnect opens a tunnel to addr through an HTTP proxy with CONNECT.
func dialHTTPConnect(ctx context.Context, dialer *net.Dialer, proxyURL *url.URL, addr string) (net.Conn, error) {
	conn, err := dialer.DialContext(ctx, "tcp", proxyDialAddr(proxyURL))
	if err != nil {
		return nil, fmt.Errorf("dial proxy: %w", err)
	}
	// Bound the handshake by the context (DialViaProxy always gives it a deadline),
	// and close the socket if the context ends mid-handshake.
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	var request strings.Builder
	request.WriteString("CONNECT " + addr + " HTTP/1.1\r\nHost: " + addr + "\r\n")
	if user := proxyURL.User; user != nil {
		password, _ := user.Password()
		request.WriteString("Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(user.Username()+":"+password)) + "\r\n")
	}
	request.WriteString("\r\n")
	if _, err := io.WriteString(conn, request.String()); err != nil {
		conn.Close()
		return nil, fmt.Errorf("proxy CONNECT to %s: %w", addr, err)
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("proxy CONNECT to %s: %w", addr, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		conn.Close()
		return nil, fmt.Errorf("proxy refused CONNECT to %s: %s", addr, resp.Status)
	}
	if !stop() {
		// The context ended as the handshake finished; the socket is closed.
		return nil, ctx.Err()
	}
	_ = conn.SetDeadline(time.Time{})
	if reader.Buffered() > 0 {
		return &bufferedConn{Conn: conn, reader: reader}, nil
	}
	return conn, nil
}

// bufferedConn serves bytes the CONNECT response reader already buffered before
// reading from the socket.
type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }
