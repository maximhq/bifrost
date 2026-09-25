package utils

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ws "github.com/fasthttp/websocket"
	"github.com/maximhq/bifrost/core/network"
	"github.com/maximhq/bifrost/core/network/proxytest"
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
	assertWebSocketDialsProxy(t, dialer, "127.0.0.1:1")
}

// assertWebSocketDialsProxy pins that both of the dialer's ws:// and wss:// dials go to
// the proxy at proxyAddr (nothing listens there, so the error names it).
func assertWebSocketDialsProxy(t *testing.T, dialer *ws.Dialer, proxyAddr string) {
	t.Helper()
	if dialer.NetDialContext == nil || dialer.NetDialTLSContext == nil {
		t.Fatal("expected the dialer's ws:// and wss:// dials to be routed through the proxy")
	}
	for name, dial := range map[string]func(context.Context, string, string) (net.Conn, error){
		"ws": dialer.NetDialContext, "wss": dialer.NetDialTLSContext,
	} {
		conn, err := dial(context.Background(), "tcp", "example.com:443")
		if err == nil {
			conn.Close()
			t.Fatalf("%s: dial through the unreachable proxy %s succeeded", name, proxyAddr)
		}
		if !strings.Contains(err.Error(), proxyAddr) {
			t.Fatalf("%s: expected the dial to go to the proxy %s, got: %v", name, proxyAddr, err)
		}
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
	assertWebSocketDialsProxy(t, dialer, "127.0.0.1:1")
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
	if dialer.Proxy != nil || dialer.NetDialContext != nil || dialer.NetDialTLSContext != nil {
		t.Fatal("expected the dialer to stay on its defaults when literal proxy URL is not provided")
	}
}

func TestConfigureWebSocketProxy_Socks5Proxy_ConfiguresDialer(t *testing.T) {
	dialer := &ws.Dialer{}
	cfg := &schemas.ProxyConfig{
		Type: schemas.Socks5Proxy,
		URL:  schemas.NewSecretVar("socks5://127.0.0.1:1"),
	}

	_, err := ConfigureWebSocketProxy(dialer, cfg)
	if err != nil {
		t.Fatalf("expected no error configuring WebSocket proxy, got: %v", err)
	}
	assertWebSocketDialsProxy(t, dialer, "127.0.0.1:1")
}

func TestConfigureWebSocketProxy_NoProxy_LeavesDialerUnset(t *testing.T) {
	dialer := &ws.Dialer{}
	cfg := &schemas.ProxyConfig{Type: schemas.NoProxy}

	_, err := ConfigureWebSocketProxy(dialer, cfg)
	if err != nil {
		t.Fatalf("expected no error for NoProxy type, got: %v", err)
	}
	if dialer.Proxy != nil || dialer.NetDialContext != nil || dialer.NetDialTLSContext != nil {
		t.Fatal("expected the dialer to stay on its defaults for NoProxy type")
	}
}

func TestConfigureWebSocketProxy_NilConfig_LeavesDialerUnset(t *testing.T) {
	dialer := &ws.Dialer{}

	_, err := ConfigureWebSocketProxy(dialer, nil)
	if err != nil {
		t.Fatalf("expected no error for nil proxy config, got: %v", err)
	}
	if dialer.Proxy != nil || dialer.NetDialContext != nil || dialer.NetDialTLSContext != nil {
		t.Fatal("expected the dialer to stay on its defaults for nil proxy config")
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
	proxy := proxytest.ForwardProxy(t, func(w http.ResponseWriter, r *http.Request) {
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
	proxy := proxytest.ForwardProxy(t, func(w http.ResponseWriter, r *http.Request) {
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
		if !network.IsLocalOrMetadataHost(host) {
			t.Errorf("network.IsLocalOrMetadataHost(%q) = false, want true", host)
		}
	}
	for _, host := range []string{"login.microsoftonline.com", "oauth2.googleapis.com", "10.0.0.5", "203.0.113.10"} {
		if network.IsLocalOrMetadataHost(host) {
			t.Errorf("network.IsLocalOrMetadataHost(%q) = true, want false", host)
		}
	}

	// End to end: a loopback target is reached directly, and the proxy never sees it.
	var proxyHits atomic.Int32
	proxy := proxytest.ForwardProxy(t, func(w http.ResponseWriter, r *http.Request) { proxyHits.Add(1) })
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

// sendProxyMatrixRequest sends one request for target through stack and returns its
// error. The route is read from the recorders, so a failing request is not in itself a
// test failure: a recorder refuses to tunnel, and a direct connection has nowhere to go.
func sendProxyMatrixRequest(t *testing.T, stack string, proxyConfig *schemas.ProxyConfig, host string, target proxytest.Target, expectDirect bool) error {
	t.Helper()
	hostPort := net.JoinHostPort(host, target.Port)
	targetURL := target.Scheme + "://" + hostPort + "/resource"
	switch stack {
	case "fasthttp":
		client := &fasthttp.Client{}
		ConfigureProxy(client, proxyConfig, testLogger{})
		ConfigureDialer(client, false)
		conn, err := client.Dial(hostPort)
		if err == nil {
			conn.Close()
		}
		return err
	case "auth":
		client := NewProviderHTTPClient(proxyConfig, schemas.DefaultNetworkConfig, testLogger{})
		client.Timeout = 3 * time.Second
		resp, err := client.Get(targetURL)
		if err == nil {
			resp.Body.Close()
		}
		return err
	case "fetch":
		// A direct fetch to the documentation-range target never connects, so bound it;
		// a proxied fetch is answered by the recorder at once.
		timeout := 3 * time.Second
		if expectDirect {
			timeout = 150 * time.Millisecond
		}
		ctx, cancel := context.WithTimeout(t.Context(), timeout)
		defer cancel()
		if proxyConfig != nil {
			ctx = context.WithValue(ctx, schemas.BifrostContextKeyProviderProxyConfig, proxyConfig)
		}
		_, _, err := FetchAndEncodeURL(ctx, targetURL)
		return err
	}
	t.Fatalf("unknown stack %q", stack)
	return nil
}

// TestProxyRoutingMatrix pins, for every fasthttp provider stack, where a request goes
// for every combination of proxy_config source, proxy env vars and target: which proxy
// saw it, for which target, with which credentials, that it went direct, or that it
// failed rather than going direct when the proxy refused it. See core/network/proxytest
// for the sources, env states and targets.
//
// Stacks:
//
//	fasthttp - inference clients (ConfigureProxy + ConfigureDialer)
//	auth     - NewProviderHTTPClient (Vertex OAuth, Azure Entra ID, Databricks M2M)
//	fetch    - FetchAndEncodeURL (image and document URLs)
//
// All three choose HTTPS_PROXY vs HTTP_PROXY by port. Bedrock (TestBedrockTransportProxyMatrix)
// and the WebSocket dialer (TestWebSocketProxyMatrix) choose by scheme.
func TestProxyRoutingMatrix(t *testing.T) {
	set := proxytest.NewSet(t)
	for _, stack := range []string{"fasthttp", "auth", "fetch"} {
		host := proxytest.TargetHost
		if stack == "fetch" {
			host = proxytest.FetchTargetHost
		}
		for _, source := range proxytest.Sources {
			for _, env := range proxytest.Envs {
				for _, target := range proxytest.Targets {
					t.Run(fmt.Sprintf("%s/%s/%s/%s", stack, source.Name, env.Name, target.Name), func(t *testing.T) {
						set.Reset()
						proxytest.SetEnv(t, set, env, host)
						want := proxytest.Expect(set, source, env, target, proxytest.ByPort, stack == "auth")
						err := sendProxyMatrixRequest(t, stack, source.Config(set), host, target, want.Proxy == "")
						proxytest.AssertRoute(t, set, want, net.JoinHostPort(host, target.Port), err)
					})
				}
			}
		}
	}
}

// TestWebSocketProxyMatrix pins the realtime WebSocket dialer's route for every
// combination of proxy source, proxy env vars and target, dialing for real through the
// recorders. ConfigureWebSocketProxy uses NetHTTPProxy, so the dialer follows net/http's
// rule: the variable is picked by scheme (the websocket library turns wss into https and
// ws into http before asking), and with no proxy_config it connects directly, like the
// inference clients.
func TestWebSocketProxyMatrix(t *testing.T) {
	set := proxytest.NewSet(t)
	for _, source := range proxytest.Sources {
		for _, env := range proxytest.Envs {
			for _, target := range proxytest.Targets {
				t.Run(source.Name+"/"+env.Name+"/"+target.Name, func(t *testing.T) {
					set.Reset()
					proxytest.SetEnv(t, set, env, proxytest.TargetHost)
					want := proxytest.Expect(set, source, env, target, proxytest.ByScheme, false)

					dialer, err := ConfigureWebSocketProxy(&ws.Dialer{HandshakeTimeout: 3 * time.Second}, source.Config(set))
					if err != nil {
						t.Fatalf("ConfigureWebSocketProxy: %v", err)
					}
					scheme := "ws"
					if target.Scheme == "https" {
						scheme = "wss"
					}
					hostPort := net.JoinHostPort(proxytest.TargetHost, target.Port)
					conn, _, err := dialer.Dial(scheme+"://"+hostPort+"/v1/realtime", nil)
					if err == nil {
						conn.Close()
					}
					proxytest.AssertRoute(t, set, want, hostPort, err)
				})
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

// TestConfigureProxy_UnresponsiveProxyDoesNotHang pins that a proxy which accepts the
// connection and never answers CONNECT (or the SOCKS5 greeting) fails the dial once the
// handshake bound passes, for every proxy_config type that names a proxy. fasthttp calls
// a Dial func with no deadline and fasthttpproxy's dialers wait forever, so a black-holed
// corporate proxy used to hang inference indefinitely.
func TestConfigureProxy_UnresponsiveProxyDoesNotHang(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var held []net.Conn
	var mu sync.Mutex
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, conn)
			mu.Unlock()
		}
	}()
	defer func() {
		mu.Lock()
		for _, c := range held {
			c.Close()
		}
		mu.Unlock()
	}()

	previous := proxyHandshakeTimeout
	proxyHandshakeTimeout = 300 * time.Millisecond
	defer func() { proxyHandshakeTimeout = previous }()

	addr := listener.Addr().String()
	for _, name := range proxytest.EnvNames {
		t.Setenv(name, "")
	}
	t.Setenv("HTTPS_PROXY", "http://"+addr)

	for name, cfg := range map[string]*schemas.ProxyConfig{
		"http":        {Type: schemas.HTTPProxy, URL: schemas.NewSecretVar("http://" + addr)},
		"socks5":      {Type: schemas.Socks5Proxy, URL: schemas.NewSecretVar("socks5://" + addr)},
		"environment": {Type: schemas.EnvProxy},
	} {
		client := &fasthttp.Client{}
		ConfigureProxy(client, cfg, testLogger{})
		ConfigureDialer(client, false)
		start := time.Now()
		conn, err := client.Dial("api.bifrost.test:443")
		if err == nil {
			conn.Close()
			t.Errorf("%s: dial through an unresponsive proxy succeeded", name)
		}
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Errorf("%s: dial took %v, want it bounded by the handshake timeout", name, elapsed)
		}
	}
}

// TestConfigureProxy_EnvironmentHTTPSProxyTrustsProxyCA pins that proxy_config type
// "environment" reaches an https:// proxy named by HTTPS_PROXY, verifying it with the
// proxy_config CA, and that without that CA the private certificate is refused rather
// than the target being dialed directly.
func TestConfigureProxy_EnvironmentHTTPSProxyTrustsProxyCA(t *testing.T) {
	recorder := proxytest.NewHTTPSRecorder(t, "env-tls")
	for _, name := range proxytest.EnvNames {
		t.Setenv(name, "")
	}
	t.Setenv("HTTPS_PROXY", "https://127.0.0.1:"+recorder.Port())
	const target = "api.bifrost.test:443"

	client := ConfigureProxy(&fasthttp.Client{}, &schemas.ProxyConfig{
		Type:      schemas.EnvProxy,
		CACertPEM: schemas.NewSecretVar(recorder.CAPEM),
	}, testLogger{})
	conn, err := client.Dial(target)
	if err != nil {
		t.Fatalf("dial through the env https proxy: %v", err)
	}
	conn.Close()
	if seen := recorder.Seen(); len(seen) != 1 || seen[0].Target != target {
		t.Fatalf("proxy saw %+v, want one CONNECT to %s", seen, target)
	}

	recorder.Reset()
	untrusted := ConfigureProxy(&fasthttp.Client{}, &schemas.ProxyConfig{Type: schemas.EnvProxy}, testLogger{})
	if conn, err := untrusted.Dial(target); err == nil {
		conn.Close()
		t.Fatal("an https proxy with a private CA was accepted without that CA")
	} else if !strings.Contains(err.Error(), "proxy TLS handshake") {
		t.Fatalf("want a proxy TLS handshake error, got %v", err)
	}
	if seen := recorder.Seen(); len(seen) != 0 {
		t.Fatalf("an unverified proxy saw %+v", seen)
	}
}

// tunnelProxy is a CONNECT proxy that really tunnels, so a TLS client behind it
// handshakes with the upstream's own certificate. Every tunnel goes to upstream,
// whatever host the CONNECT names, the way a proxy resolves names the client cannot.
// It returns the proxy URL.
func tunnelProxy(t *testing.T, upstreamAddr string) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				req, err := http.ReadRequest(bufio.NewReader(conn))
				if err != nil || req.Method != http.MethodConnect {
					return
				}
				upstream, err := net.Dial("tcp", upstreamAddr)
				if err != nil {
					return
				}
				defer upstream.Close()
				_, _ = conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
				go func() { _, _ = io.Copy(upstream, conn) }()
				_, _ = io.Copy(conn, upstream)
			}()
		}
	}()
	return "http://" + listener.Addr().String()
}

// TestInheritedSkipTLSVerify_OnlyCoversProxiedTraffic pins what an inherited global
// skip_tls_verify covers: TLS sessions through the proxy (a TLS-inspecting proxy
// presents its own certificates), never a no_proxy host the provider reaches directly,
// which must still pass certificate verification. The provider's own
// network_config.insecure_skip_verify still turns verification off everywhere, and its
// network_config.ca_cert_pem is trusted for direct hosts.
func TestInheritedSkipTLSVerify_OnlyCoversProxiedTraffic(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(target.Close)
	targetCA := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: target.Certificate().Raw}))
	proxyURL := tunnelProxy(t, target.Listener.Addr().String())
	_, targetPort, _ := net.SplitHostPort(target.Listener.Addr().String())
	// Through the proxy, the target is named by a host (example.com is on the test
	// certificate), as a provider endpoint is; directly, it is the 127.0.0.1 listener.
	proxiedURL := "https://example.com:" + targetPort

	inherited := func(noProxy string) *schemas.ProxyConfig {
		return &schemas.ProxyConfig{Type: schemas.HTTPProxy, URL: schemas.NewSecretVar(proxyURL), NoProxy: noProxy, SkipTLSVerify: true}
	}
	withCA := schemas.DefaultNetworkConfig
	withCA.CACertPEM = schemas.NewSecretVar(targetCA)
	ownSkip := schemas.DefaultNetworkConfig
	ownSkip.InsecureSkipVerify = true

	for _, tc := range []struct {
		name    string
		url     string
		proxy   *schemas.ProxyConfig
		network schemas.NetworkConfig
		wantErr bool
	}{
		{"through the proxy: skipped", proxiedURL, inherited("127.0.0.1"), schemas.DefaultNetworkConfig, false},
		{"no_proxy host: verified", target.URL, inherited("127.0.0.1"), schemas.DefaultNetworkConfig, true},
		{"no_proxy host with network_config CA: verified against it", target.URL, inherited("127.0.0.1"), withCA, false},
		{"no_proxy host with the provider's own insecure_skip_verify", target.URL, inherited("127.0.0.1"), ownSkip, false},
	} {
		t.Run("fasthttp/"+tc.name, func(t *testing.T) {
			client := ConfigureProxy(&fasthttp.Client{}, tc.proxy, testLogger{})
			ConfigureDialer(client, false)
			client = ConfigureTLS(client, tc.network, testLogger{})
			req := fasthttp.AcquireRequest()
			resp := fasthttp.AcquireResponse()
			defer fasthttp.ReleaseRequest(req)
			defer fasthttp.ReleaseResponse(resp)
			req.SetRequestURI(tc.url)
			err := client.DoTimeout(req, resp, 5*time.Second)
			assertCertOutcome(t, err, tc.wantErr)
		})
	}

	for _, tc := range []struct {
		name    string
		url     string
		proxy   *schemas.ProxyConfig
		wantErr bool
	}{
		{"through the proxy: skipped", proxiedURL, inherited("127.0.0.1"), false},
		{"no_proxy host: verified", target.URL, inherited("127.0.0.1"), true},
	} {
		t.Run("net/http/"+tc.name, func(t *testing.T) {
			proxy, tlsConfig, err := NetHTTPProxy(tc.proxy)
			if err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: proxy, TLSClientConfig: tlsConfig}}
			resp, err := client.Get(tc.url)
			if err == nil {
				resp.Body.Close()
			}
			assertCertOutcome(t, err, tc.wantErr)
		})
	}
}

func assertCertOutcome(t *testing.T, err error, wantCertErr bool) {
	t.Helper()
	if !wantCertErr {
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		return
	}
	if err == nil {
		t.Fatal("a direct no_proxy host with an untrusted certificate was accepted: verification was skipped")
	}
	if !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("want a certificate verification error, got %v", err)
	}
}

// TestScopeSkipVerify_ByServerName pins the hostname rule of scopeSkipVerify: a host on
// no_proxy is verified (hostname included), any other host is the proxy's to skip.
func TestScopeSkipVerify_ByServerName(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(server.Close)
	selfSigned := server.Certificate()
	scoped := scopeSkipVerify(&tls.Config{InsecureSkipVerify: true}, "direct.example, .internal.example")

	for _, tc := range []struct {
		serverName string
		wantErr    bool
	}{
		{"proxied.example", false},
		{"direct.example", true},
		{"api.internal.example", true},
	} {
		err := scoped.VerifyConnection(tls.ConnectionState{ServerName: tc.serverName, PeerCertificates: []*x509.Certificate{selfSigned}})
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v, want error %v", tc.serverName, err, tc.wantErr)
		}
	}
	if scopeSkipVerify(&tls.Config{InsecureSkipVerify: true}, "").VerifyConnection != nil {
		t.Error("with no no_proxy list the proxy's skip covers everything")
	}
}
