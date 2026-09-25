package utils

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ws "github.com/fasthttp/websocket"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

func TestConfigureProxy_HTTPProxy_WithLiteralURL_ConfiguresDialer(t *testing.T) {
	client := &fasthttp.Client{}
	logger := testLogger{}
	cfg := &schemas.ProxyConfig{
		Type: schemas.HTTPProxy,
		URL:  schemas.NewSecretVar("http://127.0.0.1:1"),
	}

	ConfigureProxy(client, cfg, logger)

	if client.Dial == nil {
		t.Fatal("expected dialer to be configured for literal HTTP proxy URL")
	}
	_, err := client.Dial("example.com:80")
	if err == nil {
		t.Fatal("expected dial via test proxy to fail")
	}
	if !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Fatalf("expected dial error to include proxy address, got: %v", err)
	}
}

func TestConfigureProxy_HTTPProxy_WithEnvURL_ConfiguresDialer(t *testing.T) {
	t.Setenv("BIFROST_TEST_PROXY_URL", "http://127.0.0.1:1")

	client := &fasthttp.Client{}
	logger := testLogger{}
	cfg := &schemas.ProxyConfig{
		Type: schemas.HTTPProxy,
		URL:  schemas.NewSecretVar("env.BIFROST_TEST_PROXY_URL"),
	}

	ConfigureProxy(client, cfg, logger)

	if client.Dial == nil {
		t.Fatal("expected dialer to be configured for env-backed HTTP proxy URL")
	}
	_, err := client.Dial("example.com:80")
	if err == nil {
		t.Fatal("expected dial via test proxy to fail")
	}
	if !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Fatalf("expected dial error to include proxy address from env value, got: %v", err)
	}
}

func TestConfigureProxy_HTTPProxy_WithEmptyEnvValue_FailsFast(t *testing.T) {
	t.Setenv("BIFROST_TEST_PROXY_URL_EMPTY", "")

	client := &fasthttp.Client{}
	logger := testLogger{}
	cfg := &schemas.ProxyConfig{
		Type: schemas.HTTPProxy,
		URL:  schemas.NewSecretVar("env.BIFROST_TEST_PROXY_URL_EMPTY"),
	}

	ConfigureProxy(client, cfg, logger)

	if client.Dial == nil {
		t.Fatal("expected fail-fast dialer when env-backed proxy URL resolves empty")
	}
	_, err := client.Dial("example.com:80")
	if err == nil {
		t.Fatal("expected dial to fail with explicit configuration error")
	}
	if !strings.Contains(err.Error(), "proxy.url") || !strings.Contains(err.Error(), "env.BIFROST_TEST_PROXY_URL_EMPTY") {
		t.Fatalf("expected explicit proxy env configuration error, got: %v", err)
	}
}

func TestConfigureProxy_HTTPProxy_WithUnsetLiteralURL_KeepsDefaultBehavior(t *testing.T) {
	client := &fasthttp.Client{}
	logger := testLogger{}
	cfg := &schemas.ProxyConfig{
		Type: schemas.HTTPProxy,
		URL:  nil,
	}

	ConfigureProxy(client, cfg, logger)

	if client.Dial != nil {
		t.Fatal("expected dialer to remain unset when literal proxy URL is not provided")
	}
}

func TestConfigureWebSocketProxy_HTTPProxy_WithLiteralURL_ConfiguresDialer(t *testing.T) {
	dialer := &ws.Dialer{}
	cfg := &schemas.ProxyConfig{
		Type: schemas.HTTPProxy,
		URL:  schemas.NewSecretVar("http://127.0.0.1:1"),
	}

	_, err := ConfigureWebSocketProxy(dialer, cfg)
	if err != nil {
		t.Fatalf("expected no error configuring WebSocket proxy, got: %v", err)
	}
	if dialer.Proxy == nil {
		t.Fatal("expected dialer.Proxy to be configured for literal HTTP proxy URL")
	}
	proxyURL, err := dialer.Proxy(nil)
	if err != nil {
		t.Fatalf("expected proxy func to resolve without error, got: %v", err)
	}
	if proxyURL == nil || proxyURL.Host != "127.0.0.1:1" {
		t.Fatalf("expected resolved proxy URL host 127.0.0.1:1, got: %v", proxyURL)
	}
}

func TestConfigureWebSocketProxy_HTTPProxy_WithEnvURL_ConfiguresDialer(t *testing.T) {
	t.Setenv("BIFROST_TEST_WS_PROXY_URL", "http://127.0.0.1:1")

	dialer := &ws.Dialer{}
	cfg := &schemas.ProxyConfig{
		Type: schemas.HTTPProxy,
		URL:  schemas.NewSecretVar("env.BIFROST_TEST_WS_PROXY_URL"),
	}

	_, err := ConfigureWebSocketProxy(dialer, cfg)
	if err != nil {
		t.Fatalf("expected no error configuring WebSocket proxy, got: %v", err)
	}
	if dialer.Proxy == nil {
		t.Fatal("expected dialer.Proxy to be configured for env-backed HTTP proxy URL")
	}
}

func TestConfigureWebSocketProxy_HTTPProxy_WithEmptyEnvValue_FailsFast(t *testing.T) {
	t.Setenv("BIFROST_TEST_WS_PROXY_URL_EMPTY", "")

	dialer := &ws.Dialer{}
	cfg := &schemas.ProxyConfig{
		Type: schemas.HTTPProxy,
		URL:  schemas.NewSecretVar("env.BIFROST_TEST_WS_PROXY_URL_EMPTY"),
	}

	_, err := ConfigureWebSocketProxy(dialer, cfg)
	if err == nil {
		t.Fatal("expected fail-fast error when env-backed proxy URL resolves empty")
	}
	if !strings.Contains(err.Error(), "proxy.url") || !strings.Contains(err.Error(), "env.BIFROST_TEST_WS_PROXY_URL_EMPTY") {
		t.Fatalf("expected explicit proxy env configuration error, got: %v", err)
	}
}

func TestConfigureWebSocketProxy_HTTPProxy_WithUnsetLiteralURL_KeepsDefaultBehavior(t *testing.T) {
	dialer := &ws.Dialer{}
	cfg := &schemas.ProxyConfig{
		Type: schemas.HTTPProxy,
		URL:  nil,
	}

	_, err := ConfigureWebSocketProxy(dialer, cfg)
	if err != nil {
		t.Fatalf("expected no error when proxy URL is unset, got: %v", err)
	}
	if dialer.Proxy != nil {
		t.Fatal("expected dialer.Proxy to remain unset when literal proxy URL is not provided")
	}
}

func TestConfigureWebSocketProxy_Socks5Proxy_ConfiguresDialer(t *testing.T) {
	dialer := &ws.Dialer{}
	cfg := &schemas.ProxyConfig{
		Type: schemas.Socks5Proxy,
		URL:  schemas.NewSecretVar("socks5://127.0.0.1:1080"),
	}

	_, err := ConfigureWebSocketProxy(dialer, cfg)
	if err != nil {
		t.Fatalf("expected no error configuring WebSocket proxy, got: %v", err)
	}
	if dialer.Proxy == nil {
		t.Fatal("expected dialer.Proxy to be configured for SOCKS5 proxy URL")
	}
	proxyURL, err := dialer.Proxy(nil)
	if err != nil {
		t.Fatalf("expected proxy func to resolve without error, got: %v", err)
	}
	if proxyURL == nil || proxyURL.Scheme != "socks5" || proxyURL.Host != "127.0.0.1:1080" {
		t.Fatalf("expected resolved socks5://127.0.0.1:1080 proxy URL, got: %v", proxyURL)
	}
}

func TestConfigureWebSocketProxy_NoProxy_LeavesDialerUnset(t *testing.T) {
	dialer := &ws.Dialer{}
	cfg := &schemas.ProxyConfig{Type: schemas.NoProxy}

	_, err := ConfigureWebSocketProxy(dialer, cfg)
	if err != nil {
		t.Fatalf("expected no error for NoProxy type, got: %v", err)
	}
	if dialer.Proxy != nil {
		t.Fatal("expected dialer.Proxy to remain unset for NoProxy type")
	}
}

func TestConfigureWebSocketProxy_NilConfig_LeavesDialerUnset(t *testing.T) {
	dialer := &ws.Dialer{}

	_, err := ConfigureWebSocketProxy(dialer, nil)
	if err != nil {
		t.Fatalf("expected no error for nil proxy config, got: %v", err)
	}
	if dialer.Proxy != nil {
		t.Fatal("expected dialer.Proxy to remain unset for nil proxy config")
	}
}

func TestNetHTTPProxy_Types(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://aiplatform.googleapis.com/v1/x", nil)

	t.Run("nil, none and a missing URL leave the caller's default", func(t *testing.T) {
		for name, cfg := range map[string]*schemas.ProxyConfig{
			"nil":         nil,
			"none":        {Type: schemas.NoProxy},
			"http no url": {Type: schemas.HTTPProxy},
		} {
			proxy, tlsConfig, err := NetHTTPProxy(cfg)
			if err != nil || proxy != nil || tlsConfig != nil {
				t.Errorf("%s: got proxy=%v tls=%v err=%v, want all nil", name, proxy != nil, tlsConfig, err)
			}
		}
	})

	t.Run("http with credentials", func(t *testing.T) {
		proxy, _, err := NetHTTPProxy(&schemas.ProxyConfig{
			Type:     schemas.HTTPProxy,
			URL:      schemas.NewSecretVar("http://10.1.2.3:3128"),
			Username: schemas.NewSecretVar("alice"),
			Password: schemas.NewSecretVar("hunter2"),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		got, err := proxy(req)
		if err != nil || got == nil {
			t.Fatalf("proxy func returned %v, %v", got, err)
		}
		if got.Host != "10.1.2.3:3128" || got.User.Username() != "alice" {
			t.Errorf("proxy URL = %s, want alice@10.1.2.3:3128", got.Redacted())
		}
		if pw, _ := got.User.Password(); pw != "hunter2" {
			t.Errorf("proxy password not carried")
		}
	})

	t.Run("socks5", func(t *testing.T) {
		proxy, _, err := NetHTTPProxy(&schemas.ProxyConfig{Type: schemas.Socks5Proxy, URL: schemas.NewSecretVar("socks5://127.0.0.1:1080")})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got, _ := proxy(req); got == nil || got.Scheme != "socks5" {
			t.Errorf("proxy URL = %v, want a socks5 URL", got)
		}
	})

	t.Run("environment", func(t *testing.T) {
		proxy, _, err := NetHTTPProxy(&schemas.ProxyConfig{Type: schemas.EnvProxy})
		if err != nil || proxy == nil {
			t.Fatalf("got proxy=%v err=%v, want http.ProxyFromEnvironment", proxy != nil, err)
		}
	})

	t.Run("env reference that resolves empty fails", func(t *testing.T) {
		t.Setenv("BIFROST_TEST_EMPTY_PROXY", "")
		_, _, err := NetHTTPProxy(&schemas.ProxyConfig{Type: schemas.HTTPProxy, URL: schemas.NewSecretVar("env.BIFROST_TEST_EMPTY_PROXY")})
		if err == nil || !strings.Contains(err.Error(), "resolved to an empty value") {
			t.Fatalf("expected an empty-reference error, got %v", err)
		}
	})

	t.Run("unsupported type fails", func(t *testing.T) {
		_, _, err := NetHTTPProxy(&schemas.ProxyConfig{Type: "carrier-pigeon"})
		if err == nil || !strings.Contains(err.Error(), "unsupported proxy type") {
			t.Fatalf("expected an unsupported-type error, got %v", err)
		}
	})
}

// TestNewProviderHTTPClient_UsesProxyConfig pins that a provider's side calls (OAuth
// token exchange, credential refresh) leave through its proxy_config. Before this
// client existed they used http.DefaultTransport, which only reads HTTPS_PROXY.
func TestNewProviderHTTPClient_UsesProxyConfig(t *testing.T) {
	var hits atomic.Int32
	proxy := newForwardProxy(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host == "oauth2.example" {
			hits.Add(1)
			_, _ = w.Write([]byte(`{"access_token":"t"}`))
			return
		}
		http.Error(w, "unexpected target "+r.URL.Host, http.StatusBadGateway)
	})

	client := NewProviderHTTPClient(&schemas.ProxyConfig{
		Type: schemas.HTTPProxy,
		URL:  schemas.NewSecretVar(proxy.URL),
	}, schemas.DefaultNetworkConfig, testLogger{})

	resp, err := client.Get("http://oauth2.example/token")
	if err != nil {
		t.Fatalf("request through proxy failed: %v", err)
	}
	resp.Body.Close()
	if hits.Load() != 1 {
		t.Fatalf("proxy saw %d requests, want 1", hits.Load())
	}
}

func TestNewProviderHTTPClient_WithoutProxyConfigKeepsEnvironment(t *testing.T) {
	var hits atomic.Int32
	proxy := newForwardProxy(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	})
	for _, name := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy", "REQUEST_METHOD"} {
		t.Setenv(name, "")
	}
	t.Setenv("HTTP_PROXY", proxy.URL)

	for name, cfg := range map[string]*schemas.ProxyConfig{"nil": nil, "none": {Type: schemas.NoProxy}} {
		hits.Store(0)
		client := NewProviderHTTPClient(cfg, schemas.DefaultNetworkConfig, testLogger{})
		if resp, err := client.Get("http://oauth2.example/token"); err == nil {
			resp.Body.Close()
		}
		if hits.Load() != 1 {
			t.Errorf("%s: env proxy saw %d requests, want 1 (token calls keep proxying from the environment)", name, hits.Load())
		}
	}
}

func TestNewProviderHTTPClient_InvalidConfigFailsPerRequest(t *testing.T) {
	t.Setenv("BIFROST_TEST_EMPTY_AUTH_PROXY", "")
	client := NewProviderHTTPClient(&schemas.ProxyConfig{
		Type: schemas.HTTPProxy,
		URL:  schemas.NewSecretVar("env.BIFROST_TEST_EMPTY_AUTH_PROXY"),
	}, schemas.DefaultNetworkConfig, testLogger{})
	_, err := client.Get("http://oauth2.example/token")
	if err == nil || !strings.Contains(err.Error(), "resolved to an empty value") {
		t.Fatalf("expected the configuration error on the request, got %v", err)
	}
}

// TestNewProviderHTTPClient_NeverProxiesMetadataOrLoopback pins that credential chains
// reaching an instance-metadata service (Azure IMDS, GCE metadata) or a local agent
// (Azure Arc) connect directly even with a proxy configured. A corporate proxy cannot
// reach those, so proxying them would break managed and workload identity.
func TestNewProviderHTTPClient_NeverProxiesMetadataOrLoopback(t *testing.T) {
	for _, host := range []string{"169.254.169.254", "metadata.google.internal", "100.100.100.200", "fd00:ec2::254", "localhost", "127.0.0.1", "::1"} {
		if !isLocalOrMetadataHost(host) {
			t.Errorf("isLocalOrMetadataHost(%q) = false, want true", host)
		}
	}
	for _, host := range []string{"login.microsoftonline.com", "oauth2.googleapis.com", "10.0.0.5", "203.0.113.10"} {
		if isLocalOrMetadataHost(host) {
			t.Errorf("isLocalOrMetadataHost(%q) = true, want false", host)
		}
	}

	// End to end: a loopback target is reached directly, and the proxy never sees it.
	var proxyHits atomic.Int32
	proxy := newForwardProxy(t, func(w http.ResponseWriter, r *http.Request) { proxyHits.Add(1) })
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("imds"))
	}))
	defer target.Close()

	client := NewProviderHTTPClient(&schemas.ProxyConfig{
		Type: schemas.HTTPProxy,
		URL:  schemas.NewSecretVar(proxy.URL),
	}, schemas.DefaultNetworkConfig, testLogger{})
	resp, err := client.Get(target.URL + "/metadata/identity/oauth2/token")
	if err != nil {
		t.Fatalf("direct request to a local target failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "imds" {
		t.Errorf("body = %q, want the local target's response", body)
	}
	if proxyHits.Load() != 0 {
		t.Errorf("proxy saw %d requests for a local target, want 0", proxyHits.Load())
	}
}

// ---------------------------------------------------------------------------
// Proxy routing matrix
//
// Every combination of provider stack x proxy source x proxy env vars x target, with
// the expected route stated by proxyMatrixExpect. Each case sends one real request
// through recording proxies and asserts which proxy (if any) saw it.
//
// Stacks:
//   fasthttp - inference clients (ConfigureProxy + ConfigureDialer)
//   auth     - NewProviderHTTPClient (Vertex OAuth, Azure Entra ID)
//   fetch    - FetchAndEncodeURL (image and document URLs)
// Bedrock's net/http runtime client follows the auth rules and is covered in
// core/providers/bedrock/transport_test.go.
// ---------------------------------------------------------------------------

// recordingProxy answers as an HTTP forward proxy or a SOCKS5 proxy without
// forwarding anything, and records each target as host:port.
type recordingProxy struct {
	name     string
	listener net.Listener
	mu       sync.Mutex
	targets  []string
}

func (p *recordingProxy) record(target string) {
	p.mu.Lock()
	p.targets = append(p.targets, target)
	p.mu.Unlock()
}

func (p *recordingProxy) seen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.targets...)
}

func (p *recordingProxy) reset() {
	p.mu.Lock()
	p.targets = nil
	p.mu.Unlock()
}

func (p *recordingProxy) port() string {
	_, port, _ := net.SplitHostPort(p.listener.Addr().String())
	return port
}

// newRecordingHTTPProxy listens on network ("tcp4" or "tcp6") loopback. CONNECT gets
// a 200 and a closed tunnel; an absolute-URI request gets an empty 200.
func newRecordingHTTPProxy(t *testing.T, name, network string) *recordingProxy {
	t.Helper()
	addr := "127.0.0.1:0"
	if network == "tcp6" {
		addr = "[::1]:0"
	}
	listener, err := net.Listen(network, addr)
	if err != nil {
		t.Fatalf("%s: listen %s: %v", name, network, err)
	}
	p := &recordingProxy{name: name, listener: listener}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			p.record(r.Host)
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_, _ = conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
				conn.Close()
			}
			return
		}
		host := r.URL.Host
		if r.URL.Port() == "" {
			host = net.JoinHostPort(r.URL.Hostname(), "80")
		}
		p.record(host)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return p
}

// newRecordingSOCKS5Proxy accepts the no-auth method, records the CONNECT target and
// reports success, then closes the connection.
func newRecordingSOCKS5Proxy(t *testing.T, name string) *recordingProxy {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("%s: listen: %v", name, err)
	}
	p := &recordingProxy{name: name, listener: listener}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				if target, ok := readSOCKS5Connect(conn); ok {
					p.record(target)
					_, _ = conn.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
				}
			}(conn)
		}
	}()
	t.Cleanup(func() { _ = listener.Close() })
	return p
}

// readSOCKS5Connect runs the RFC 1928 greeting and reads one CONNECT request.
func readSOCKS5Connect(conn net.Conn) (string, bool) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(conn, header); err != nil || header[0] != 5 {
		return "", false
	}
	if _, err := io.ReadFull(conn, make([]byte, header[1])); err != nil {
		return "", false
	}
	if _, err := conn.Write([]byte{5, 0}); err != nil {
		return "", false
	}
	request := make([]byte, 4)
	if _, err := io.ReadFull(conn, request); err != nil || request[1] != 1 {
		return "", false
	}
	var host string
	switch request[3] {
	case 1:
		ip := make([]byte, 4)
		if _, err := io.ReadFull(conn, ip); err != nil {
			return "", false
		}
		host = net.IP(ip).String()
	case 3:
		size := make([]byte, 1)
		if _, err := io.ReadFull(conn, size); err != nil {
			return "", false
		}
		name := make([]byte, size[0])
		if _, err := io.ReadFull(conn, name); err != nil {
			return "", false
		}
		host = string(name)
	case 4:
		ip := make([]byte, 16)
		if _, err := io.ReadFull(conn, ip); err != nil {
			return "", false
		}
		host = net.IP(ip).String()
	default:
		return "", false
	}
	port := make([]byte, 2)
	if _, err := io.ReadFull(conn, port); err != nil {
		return "", false
	}
	return net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(port)))), true
}

// proxyMatrixProxies are the recording proxies one matrix run routes through.
type proxyMatrixProxies struct {
	config    *recordingProxy // named by proxy_config (http, IP or hostname URL)
	config6   *recordingProxy // named by proxy_config (http, IPv6 literal URL)
	socks     *recordingProxy // named by proxy_config (socks5)
	envHTTPS  *recordingProxy // HTTPS_PROXY / https_proxy
	envHTTPS6 *recordingProxy // HTTPS_PROXY as an IPv6 literal
	envHTTP   *recordingProxy // HTTP_PROXY / http_proxy
	all       []*recordingProxy
}

func newProxyMatrixProxies(t *testing.T) *proxyMatrixProxies {
	p := &proxyMatrixProxies{
		config:    newRecordingHTTPProxy(t, "config", "tcp4"),
		config6:   newRecordingHTTPProxy(t, "config6", "tcp6"),
		socks:     newRecordingSOCKS5Proxy(t, "socks"),
		envHTTPS:  newRecordingHTTPProxy(t, "env-https", "tcp4"),
		envHTTPS6: newRecordingHTTPProxy(t, "env-https6", "tcp6"),
		envHTTP:   newRecordingHTTPProxy(t, "env-http", "tcp4"),
	}
	p.all = []*recordingProxy{p.config, p.config6, p.socks, p.envHTTPS, p.envHTTPS6, p.envHTTP}
	return p
}

func (p *proxyMatrixProxies) byName(name string) *recordingProxy {
	for _, proxy := range p.all {
		if proxy.name == name {
			return proxy
		}
	}
	return nil
}

// proxyMatrixSource is where a provider's proxy comes from.
type proxyMatrixSource struct {
	name   string
	config func(p *proxyMatrixProxies, target proxyMatrixTarget) *schemas.ProxyConfig
}

var proxyMatrixSources = []proxyMatrixSource{
	{name: "unset", config: func(*proxyMatrixProxies, proxyMatrixTarget) *schemas.ProxyConfig { return nil }},
	{name: "none", config: func(*proxyMatrixProxies, proxyMatrixTarget) *schemas.ProxyConfig {
		return &schemas.ProxyConfig{Type: schemas.NoProxy}
	}},
	{name: "http-ip", config: func(p *proxyMatrixProxies, _ proxyMatrixTarget) *schemas.ProxyConfig {
		return &schemas.ProxyConfig{Type: schemas.HTTPProxy, URL: schemas.NewSecretVar("http://127.0.0.1:" + p.config.port())}
	}},
	{name: "http-hostname", config: func(p *proxyMatrixProxies, _ proxyMatrixTarget) *schemas.ProxyConfig {
		return &schemas.ProxyConfig{Type: schemas.HTTPProxy, URL: schemas.NewSecretVar("http://localhost:" + p.config.port())}
	}},
	{name: "http-ipv6", config: func(p *proxyMatrixProxies, _ proxyMatrixTarget) *schemas.ProxyConfig {
		return &schemas.ProxyConfig{Type: schemas.HTTPProxy, URL: schemas.NewSecretVar("http://[::1]:" + p.config6.port())}
	}},
	{name: "socks5", config: func(p *proxyMatrixProxies, _ proxyMatrixTarget) *schemas.ProxyConfig {
		return &schemas.ProxyConfig{Type: schemas.Socks5Proxy, URL: schemas.NewSecretVar("socks5://127.0.0.1:" + p.socks.port())}
	}},
	{name: "environment", config: func(*proxyMatrixProxies, proxyMatrixTarget) *schemas.ProxyConfig {
		return &schemas.ProxyConfig{Type: schemas.EnvProxy}
	}},
	// The shape an inherited global proxy takes: a proxy plus the global no_proxy
	// list, here naming the target, so every stack must connect directly.
	{name: "http-ip+no_proxy", config: func(p *proxyMatrixProxies, _ proxyMatrixTarget) *schemas.ProxyConfig {
		return &schemas.ProxyConfig{
			Type:    schemas.HTTPProxy,
			URL:     schemas.NewSecretVar("http://127.0.0.1:" + p.config.port()),
			NoProxy: "api.bifrost.test,203.0.113.10",
		}
	}},
}

// proxyMatrixEnv is one state of the proxy environment variables. Values name a
// recording proxy ("env-https", "env-http") or, for NO_PROXY, the literal "target".
type proxyMatrixEnv struct {
	name string
	vars map[string]string
}

var proxyMatrixEnvs = []proxyMatrixEnv{
	{name: "no-env", vars: map[string]string{}},
	{name: "HTTPS_PROXY", vars: map[string]string{"HTTPS_PROXY": "env-https"}},
	{name: "HTTP_PROXY", vars: map[string]string{"HTTP_PROXY": "env-http"}},
	{name: "HTTPS_PROXY-ipv6", vars: map[string]string{"HTTPS_PROXY": "env-https6"}},
	{name: "both", vars: map[string]string{"HTTPS_PROXY": "env-https", "HTTP_PROXY": "env-http"}},
	{name: "both+NO_PROXY", vars: map[string]string{"HTTPS_PROXY": "env-https", "HTTP_PROXY": "env-http", "NO_PROXY": "target"}},
	{name: "https_proxy-lowercase", vars: map[string]string{"https_proxy": "env-https"}},
	{name: "http_proxy-lowercase", vars: map[string]string{"http_proxy": "env-http"}},
}

// proxyMatrixTarget is the upstream a request is for.
type proxyMatrixTarget struct {
	name   string
	scheme string
	port   string
}

var proxyMatrixTargets = []proxyMatrixTarget{
	{name: "https", scheme: "https", port: "443"},
	{name: "http", scheme: "http", port: "80"},
	// A TLS upstream on a non-standard port: every stack here runs on fasthttp, whose
	// dialer sees only host:port and treats every port but 443 as plain HTTP, so it
	// picks HTTP_PROXY. (Bedrock's net/http client picks HTTPS_PROXY; see its matrix.)
	{name: "https-8443", scheme: "https", port: "8443"},
}

// proxyMatrixHost is the target host for each stack. fetch resolves the target
// locally for its SSRF check, so it needs a public IP literal; the others hand the
// hostname to the proxy, and a .test name never resolves if one dials it directly.
func proxyMatrixHost(stack string) string {
	if stack == "fetch" {
		return "203.0.113.10"
	}
	return "api.bifrost.test"
}

// proxyMatrixExpect is the routing spec: the name of the recording proxy a request
// must reach, or "" for a direct connection.
func proxyMatrixExpect(stack string, source proxyMatrixSource, env proxyMatrixEnv, target proxyMatrixTarget) string {
	switch source.name {
	case "unset", "none":
		// With no proxy configured, only the auth client keeps the environment
		// default it had on http.DefaultTransport. Inference and fetch go direct.
		if stack == "auth" {
			return proxyMatrixFasthttpEnvPick(env, target)
		}
		return ""
	case "http-ip", "http-hostname":
		return "config"
	case "http-ipv6":
		return "config6"
	case "http-ip+no_proxy":
		return ""
	case "socks5":
		return "socks"
	case "environment":
		return proxyMatrixFasthttpEnvPick(env, target)
	}
	panic("unknown source " + source.name)
}

// proxyMatrixEnvPick is httpproxy's rule: https uses HTTPS_PROXY, http uses
// HTTP_PROXY, with no fallback between them; a NO_PROXY match is direct.
// proxyMatrixFasthttpEnvPick feeds it the scheme fasthttp infers from the port.
func proxyMatrixEnvPick(env proxyMatrixEnv, https bool) string {
	if env.vars["NO_PROXY"] != "" {
		return ""
	}
	if https {
		return firstNonEmpty(env.vars["HTTPS_PROXY"], env.vars["https_proxy"])
	}
	return firstNonEmpty(env.vars["HTTP_PROXY"], env.vars["http_proxy"])
}

// proxyMatrixFasthttpEnvPick is fasthttpproxy's rule for the env dialer: it cannot
// see the scheme, so port 443 means HTTPS_PROXY and any other port means HTTP_PROXY.
func proxyMatrixFasthttpEnvPick(env proxyMatrixEnv, target proxyMatrixTarget) string {
	return proxyMatrixEnvPick(env, target.port == "443")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// setProxyMatrixEnv clears every variable httpproxy reads, then applies env.
func setProxyMatrixEnv(t *testing.T, p *proxyMatrixProxies, env proxyMatrixEnv, targetHost string) {
	t.Helper()
	for _, name := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy", "REQUEST_METHOD"} {
		t.Setenv(name, "")
	}
	for name, value := range env.vars {
		if name == "NO_PROXY" {
			t.Setenv(name, targetHost)
			continue
		}
		proxy := p.byName(value)
		host := "127.0.0.1"
		if proxy == p.config6 || proxy == p.envHTTPS6 {
			host = "[::1]"
		}
		t.Setenv(name, "http://"+host+":"+proxy.port())
	}
}

// sendProxyMatrixRequest sends one request for target through stack. The outcome is
// read from the recording proxies, so errors are expected and ignored: a proxy
// refuses to tunnel, and a direct connection has nowhere to go.
func sendProxyMatrixRequest(t *testing.T, stack string, proxyConfig *schemas.ProxyConfig, target proxyMatrixTarget, expectDirect bool) {
	t.Helper()
	host := proxyMatrixHost(stack)
	hostPort := net.JoinHostPort(host, target.port)
	targetURL := target.scheme + "://" + hostPort + "/resource"
	switch stack {
	case "fasthttp":
		client := &fasthttp.Client{}
		ConfigureProxy(client, proxyConfig, testLogger{})
		ConfigureDialer(client, false)
		if conn, err := client.Dial(hostPort); err == nil {
			conn.Close()
		}
	case "auth":
		client := NewProviderHTTPClient(proxyConfig, schemas.DefaultNetworkConfig, testLogger{})
		client.Timeout = 3 * time.Second
		if resp, err := client.Get(targetURL); err == nil {
			resp.Body.Close()
		}
	case "fetch":
		// A direct fetch to the documentation-range target never connects, so bound
		// it; a proxied fetch is answered by the recording proxy at once.
		timeout := 3 * time.Second
		if expectDirect {
			timeout = 250 * time.Millisecond
		}
		ctx, cancel := context.WithTimeout(t.Context(), timeout)
		defer cancel()
		if proxyConfig != nil {
			ctx2 := context.WithValue(ctx, schemas.BifrostContextKeyProviderProxyConfig, proxyConfig)
			_, _, _ = FetchAndEncodeURL(ctx2, targetURL)
		} else {
			_, _, _ = FetchAndEncodeURL(ctx, targetURL)
		}
	default:
		t.Fatalf("unknown stack %q", stack)
	}
}

// TestProxyRoutingMatrix pins, for every provider stack, which proxy a request
// reaches for every combination of proxy_config source, proxy env vars and target.
func TestProxyRoutingMatrix(t *testing.T) {
	proxies := newProxyMatrixProxies(t)
	for _, stack := range []string{"fasthttp", "auth", "fetch"} {
		for _, source := range proxyMatrixSources {
			for _, env := range proxyMatrixEnvs {
				for _, target := range proxyMatrixTargets {
					name := fmt.Sprintf("%s/%s/%s/%s", stack, source.name, env.name, target.name)
					t.Run(name, func(t *testing.T) {
						for _, proxy := range proxies.all {
							proxy.reset()
						}
						setProxyMatrixEnv(t, proxies, env, proxyMatrixHost(stack))
						want := proxyMatrixExpect(stack, source, env, target)
						sendProxyMatrixRequest(t, stack, source.config(proxies, target), target, want == "")

						wantTarget := net.JoinHostPort(proxyMatrixHost(stack), target.port)
						for _, proxy := range proxies.all {
							seen := proxy.seen()
							if proxy.name == want {
								if len(seen) != 1 || seen[0] != wantTarget {
									t.Errorf("proxy %q saw %v, want exactly [%s]", proxy.name, seen, wantTarget)
								}
								continue
							}
							if len(seen) != 0 {
								if want == "" {
									t.Errorf("want a direct connection, but proxy %q saw %v", proxy.name, seen)
								} else {
									t.Errorf("want proxy %q, but proxy %q saw %v", want, proxy.name, seen)
								}
							}
						}
					})
				}
			}
		}
	}
}

// TestConfigureProxy_NoProxyDialsDirectlyThroughConfigureDialer pins that a host on the
// proxy's no_proxy list connects directly, through ConfigureDialer's own checked dial,
// while every other host still goes to the proxy. An inherited global proxy relies on
// this to keep, say, a Bedrock VPC endpoint off the corporate proxy.
func TestConfigureProxy_NoProxyDialsDirectlyThroughConfigureDialer(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer target.Close()

	client := &fasthttp.Client{}
	ConfigureProxy(client, &schemas.ProxyConfig{
		Type:    schemas.HTTPProxy,
		URL:     schemas.NewSecretVar("http://127.0.0.1:1"),
		NoProxy: "127.0.0.1, .vpce.amazonaws.com",
	}, testLogger{})
	ConfigureDialer(client, false)

	conn, err := client.Dial(strings.TrimPrefix(target.URL, "http://"))
	if err != nil {
		t.Fatalf("no_proxy host must dial directly, got %v", err)
	}
	conn.Close()

	_, err = client.Dial("example.com:80")
	if err == nil || !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Fatalf("a host off the no_proxy list must go to the proxy, got %v", err)
	}
}

func TestNetHTTPProxy_NoProxyConnectsDirectly(t *testing.T) {
	proxy, _, err := NetHTTPProxy(&schemas.ProxyConfig{
		Type:    schemas.HTTPProxy,
		URL:     schemas.NewSecretVar("http://10.0.0.9:3128"),
		NoProxy: ".vpce.amazonaws.com",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	bypassed, _ := http.NewRequest(http.MethodPost, "https://vpce-0abc.bedrock-runtime.us-east-1.vpce.amazonaws.com/model/x/converse", nil)
	if got, _ := proxy(bypassed); got != nil {
		t.Errorf("no_proxy host: proxy = %v, want direct", got)
	}
	proxied, _ := http.NewRequest(http.MethodPost, "https://us-central1-aiplatform.googleapis.com/v1/x", nil)
	if got, _ := proxy(proxied); got == nil || got.Host != "10.0.0.9:3128" {
		t.Errorf("other host: proxy = %v, want 10.0.0.9:3128", got)
	}
}

// newForwardProxy starts an HTTP proxy that serves handler for both proxy styles:
// absolute-URI requests and CONNECT tunnels carrying plain HTTP requests. fasthttp's
// proxy dialer tunnels every target with CONNECT, http:// ones included, so a test
// proxy for the fasthttp-backed clients must speak both. handler sees r.URL.Host set
// to the target (the port is dropped when it is 80).
func newForwardProxy(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			handler(w, r)
			return
		}
		target := strings.TrimSuffix(r.Host, ":80")
		conn, buffered, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
		reader := bufio.NewReader(io.MultiReader(buffered.Reader, conn))
		for {
			inner, err := http.ReadRequest(reader)
			if err != nil {
				return
			}
			inner.URL.Scheme = "http"
			inner.URL.Host = target
			recorder := httptest.NewRecorder()
			handler(recorder, inner)
			// Result leaves the length unknown, which Write turns into a
			// close-delimited body; the tunnel stays open, so give it a length.
			resp := recorder.Result()
			resp.ContentLength = int64(recorder.Body.Len())
			if err := resp.Write(conn); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// TestConfigureProxy_EnvironmentNoProxyKeepsPrivateNetworkCheck pins that a target the
// environment says not to proxy is dialed by ConfigureDialer, with its private-network
// rules. fasthttpproxy's env dialer dialed such targets itself, so with type
// "environment" a NO_PROXY entry for a private address skipped the check entirely.
func TestConfigureProxy_EnvironmentNoProxyKeepsPrivateNetworkCheck(t *testing.T) {
	for _, name := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy", "REQUEST_METHOD"} {
		t.Setenv(name, "")
	}
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("NO_PROXY", "10.0.0.5")

	client := &fasthttp.Client{}
	ConfigureProxy(client, &schemas.ProxyConfig{Type: schemas.EnvProxy}, testLogger{})
	ConfigureDialer(client, false)

	_, err := client.Dial("10.0.0.5:443")
	if err == nil || !strings.Contains(err.Error(), "connection to private IP 10.0.0.5 is not allowed") {
		t.Fatalf("expected ConfigureDialer's private-network refusal, got %v", err)
	}
}
