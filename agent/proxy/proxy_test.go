package proxy

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/agent/config"
	"golang.org/x/net/http2"
)

func TestGenerateSelfSignedCA(t *testing.T) {
	caCert, caTLSCert, err := GenerateSelfSignedCA()
	if err != nil {
		t.Fatal(err)
	}
	if !caCert.IsCA {
		t.Error("CA cert should have IsCA=true")
	}
	if caCert.Subject.CommonName != "Bifrost Agent CA" {
		t.Errorf("unexpected CN: %s", caCert.Subject.CommonName)
	}
	if caTLSCert.PrivateKey == nil {
		t.Error("CA private key should not be nil")
	}
}

func TestGenerateCert(t *testing.T) {
	caCert, caTLSCert, err := GenerateSelfSignedCA()
	if err != nil {
		t.Fatal(err)
	}

	domain := "api.openai.com"
	cert, err := GenerateCert(domain, caCert, caTLSCert)
	if err != nil {
		t.Fatal(err)
	}

	if len(cert.Certificate) != 2 {
		t.Errorf("expected 2 certs in chain (leaf + CA), got %d", len(cert.Certificate))
	}

	// Parse the leaf cert and verify it
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if leaf.Subject.CommonName != domain {
		t.Errorf("leaf CN = %s, want %s", leaf.Subject.CommonName, domain)
	}
	if len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != domain {
		t.Errorf("leaf DNSNames = %v, want [%s]", leaf.DNSNames, domain)
	}

	// Verify the leaf is signed by the CA
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	_, err = leaf.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	if err != nil {
		t.Errorf("leaf cert verification failed: %v", err)
	}
}

func TestCertStore_GetOrCreate(t *testing.T) {
	caCert, caTLSCert, err := GenerateSelfSignedCA()
	if err != nil {
		t.Fatal(err)
	}
	store := NewCertStore(caCert, caTLSCert)

	// First call should generate
	cert1, err := store.GetOrCreate("api.openai.com")
	if err != nil {
		t.Fatal(err)
	}

	// Second call should return cached
	cert2, err := store.GetOrCreate("api.openai.com")
	if err != nil {
		t.Fatal(err)
	}

	// Should be the same pointer (cached)
	if cert1 != cert2 {
		t.Error("expected cached cert to be returned")
	}

	// Different domain should be different cert
	cert3, err := store.GetOrCreate("api.anthropic.com")
	if err != nil {
		t.Fatal(err)
	}
	if cert1 == cert3 {
		t.Error("expected different cert for different domain")
	}
}

func TestCertStore_TLSHandshake(t *testing.T) {
	caCert, caTLSCert, err := GenerateSelfSignedCA()
	if err != nil {
		t.Fatal(err)
	}
	store := NewCertStore(caCert, caTLSCert)

	cert, err := store.GetOrCreate("api.openai.com")
	if err != nil {
		t.Fatal(err)
	}

	// Verify the cert can be used in a TLS config
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{*cert},
	}
	if len(tlsConfig.Certificates) != 1 {
		t.Error("TLS config should have 1 certificate")
	}
}

func TestRewriteRequest(t *testing.T) {
	rule := &config.DomainRule{
		Hostname:          "api.openai.com",
		IntegrationPrefix: "/openai",
		PreservePath:      true,
	}

	req, err := http.NewRequest("POST", "https://api.openai.com/v1/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer sk-test-key")
	req.Header.Set("Content-Type", "application/json")

	err = RewriteRequest(req, rule, "https://gateway.example.com", "vk-test-123")
	if err != nil {
		t.Fatal(err)
	}

	// Verify URL rewriting
	if req.URL.Scheme != "https" {
		t.Errorf("scheme = %s, want https", req.URL.Scheme)
	}
	if req.URL.Host != "gateway.example.com" {
		t.Errorf("host = %s, want gateway.example.com", req.URL.Host)
	}
	if req.URL.Path != "/openai/v1/chat/completions" {
		t.Errorf("path = %s, want /openai/v1/chat/completions", req.URL.Path)
	}

	// Verify headers
	if req.Host != "gateway.example.com" {
		t.Errorf("Host header = %s, want gateway.example.com", req.Host)
	}
	if req.Header.Get("x-bf-vk") != "vk-test-123" {
		t.Errorf("x-bf-vk = %s, want vk-test-123", req.Header.Get("x-bf-vk"))
	}
	if req.Header.Get("X-Forwarded-Host") != "api.openai.com" {
		t.Errorf("X-Forwarded-Host = %s, want api.openai.com", req.Header.Get("X-Forwarded-Host"))
	}
	if req.Header.Get("X-Bifrost-Agent") != "bifrost-agent/0.1.0" {
		t.Errorf("X-Bifrost-Agent = %s, want bifrost-agent/0.1.0", req.Header.Get("X-Bifrost-Agent"))
	}

	// Original headers should be preserved
	if req.Header.Get("Authorization") != "Bearer sk-test-key" {
		t.Errorf("Authorization header was modified: %s", req.Header.Get("Authorization"))
	}
	if req.Header.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type header was modified: %s", req.Header.Get("Content-Type"))
	}
}

func TestRewriteRequest_AnthropicDomain(t *testing.T) {
	rule := &config.DomainRule{
		Hostname:          "api.anthropic.com",
		IntegrationPrefix: "/anthropic",
		PreservePath:      true,
	}

	req, err := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("x-api-key", "sk-ant-test")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", "messages-2024-12-19")

	err = RewriteRequest(req, rule, "https://gateway.example.com", "vk-test-456")
	if err != nil {
		t.Fatal(err)
	}

	if req.URL.Path != "/anthropic/v1/messages" {
		t.Errorf("path = %s, want /anthropic/v1/messages", req.URL.Path)
	}

	// Anthropic-specific headers must be preserved
	if req.Header.Get("x-api-key") != "sk-ant-test" {
		t.Errorf("x-api-key was modified")
	}
	if req.Header.Get("anthropic-version") != "2023-06-01" {
		t.Errorf("anthropic-version was modified")
	}
	if req.Header.Get("anthropic-beta") != "messages-2024-12-19" {
		t.Errorf("anthropic-beta was modified")
	}
}

type gatewayForwarderFunc func(req *http.Request) (*http.Response, error)

func (f gatewayForwarderFunc) ForwardRoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type requestDoerFunc func(req *http.Request) (*http.Response, error)

func (f requestDoerFunc) Do(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestMITMProxyRoundTripRequestClearsRequestURIForGateway(t *testing.T) {
	proxy, rule, _ := newTestMITMProxy(t, gatewayForwarderFunc(func(req *http.Request) (*http.Response, error) {
		if req.RequestURI != "" {
			t.Fatalf("gateway request RequestURI = %q, want empty", req.RequestURI)
		}
		if req.URL.Host != "gateway.example.com" {
			t.Fatalf("gateway host = %s, want gateway.example.com", req.URL.Host)
		}
		if req.URL.Path != "/chatgpt/backend-api/f/conversation" {
			t.Fatalf("gateway path = %s, want /chatgpt/backend-api/f/conversation", req.URL.Path)
		}
		if req.Header.Get("X-Forwarded-Host") != "chatgpt.com" {
			t.Fatalf("X-Forwarded-Host = %s, want chatgpt.com", req.Header.Get("X-Forwarded-Host"))
		}
		if req.Header.Get("Connection") != "" {
			t.Fatalf("Connection header should be stripped, got %q", req.Header.Get("Connection"))
		}

		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if string(body) != `{"prompt":"hi"}` {
			t.Fatalf("gateway body = %s", string(body))
		}

		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		}, nil
	}), requestDoerFunc(func(req *http.Request) (*http.Response, error) {
		t.Fatal("direct forwarder should not be used for backend-api path")
		return nil, nil
	}))

	req := httptest.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/f/conversation", strings.NewReader(`{"prompt":"hi"}`))
	req.RequestURI = "/backend-api/f/conversation"
	req.Header.Set("Connection", "keep-alive")

	resp, proxied, err := proxy.roundTripRequest(req, "chatgpt.com", rule)
	if err != nil {
		t.Fatalf("roundTripRequest: %v", err)
	}
	if !proxied {
		t.Fatal("expected request to be proxied")
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if string(body) != `{"ok":true}` {
		t.Fatalf("response body = %s", string(body))
	}
}

func TestMITMProxyHTTP2NegotiationAndProxying(t *testing.T) {
	var capturedPath string
	var capturedForwardedHost string

	proxy, rule, caCert := newTestMITMProxy(t, gatewayForwarderFunc(func(req *http.Request) (*http.Response, error) {
		capturedPath = req.URL.Path
		capturedForwardedHost = req.Header.Get("X-Forwarded-Host")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"status":"ok"}`)),
		}, nil
	}), requestDoerFunc(func(req *http.Request) (*http.Response, error) {
		t.Fatal("direct forwarder should not be used for backend-api path")
		return nil, nil
	}))

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()

	go proxy.HandleConnection(serverConn, "chatgpt.com", rule)

	tlsClient := tls.Client(clientConn, &tls.Config{
		ServerName: "chatgpt.com",
		RootCAs:    newCertPool(caCert),
		NextProtos: []string{http2.NextProtoTLS, "http/1.1"},
	})
	defer tlsClient.Close()

	if err := tlsClient.Handshake(); err != nil {
		t.Fatalf("client handshake: %v", err)
	}
	if tlsClient.ConnectionState().NegotiatedProtocol != http2.NextProtoTLS {
		t.Fatalf("negotiated protocol = %q, want %q", tlsClient.ConnectionState().NegotiatedProtocol, http2.NextProtoTLS)
	}

	h2Transport := &http2.Transport{}
	h2Conn, err := h2Transport.NewClientConn(tlsClient)
	if err != nil {
		t.Fatalf("new h2 client conn: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/f/conversation", strings.NewReader(`{"prompt":"hi"}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	resp, err := h2Conn.RoundTrip(req)
	if err != nil {
		t.Fatalf("h2 round trip: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read h2 response: %v", err)
	}
	if string(body) != `{"status":"ok"}` {
		t.Fatalf("response body = %s", string(body))
	}
	if capturedPath != "/chatgpt/backend-api/f/conversation" {
		t.Fatalf("captured path = %s", capturedPath)
	}
	if capturedForwardedHost != "chatgpt.com" {
		t.Fatalf("X-Forwarded-Host = %s", capturedForwardedHost)
	}
}

func TestMITMProxyHTTP2StreamingFlush(t *testing.T) {
	proxy, rule, caCert := newTestMITMProxy(t, gatewayForwarderFunc(func(req *http.Request) (*http.Response, error) {
		pr, pw := io.Pipe()
		go func() {
			_, _ = pw.Write([]byte("data: one\n\n"))
			time.Sleep(200 * time.Millisecond)
			_, _ = pw.Write([]byte("data: two\n\n"))
			_ = pw.Close()
		}()

		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       pr,
		}, nil
	}), requestDoerFunc(func(req *http.Request) (*http.Response, error) {
		t.Fatal("direct forwarder should not be used for backend-api path")
		return nil, nil
	}))

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()

	go proxy.HandleConnection(serverConn, "chatgpt.com", rule)

	tlsClient := tls.Client(clientConn, &tls.Config{
		ServerName: "chatgpt.com",
		RootCAs:    newCertPool(caCert),
		NextProtos: []string{http2.NextProtoTLS, "http/1.1"},
	})
	defer tlsClient.Close()

	if err := tlsClient.Handshake(); err != nil {
		t.Fatalf("client handshake: %v", err)
	}

	h2Transport := &http2.Transport{}
	h2Conn, err := h2Transport.NewClientConn(tlsClient)
	if err != nil {
		t.Fatalf("new h2 client conn: %v", err)
	}

	req, err := http.NewRequest(http.MethodGet, "https://chatgpt.com/backend-api/f/conversation", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	resp, err := h2Conn.RoundTrip(req)
	if err != nil {
		t.Fatalf("h2 round trip: %v", err)
	}
	defer resp.Body.Close()

	start := time.Now()
	firstChunk := make([]byte, len("data: one\n\n"))
	if _, err := io.ReadFull(resp.Body, firstChunk); err != nil {
		t.Fatalf("read first chunk: %v", err)
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatalf("first chunk took too long to arrive: %v", time.Since(start))
	}
	if string(firstChunk) != "data: one\n\n" {
		t.Fatalf("first chunk = %q", string(firstChunk))
	}

	remaining, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read remaining body: %v", err)
	}
	if string(remaining) != "data: two\n\n" {
		t.Fatalf("remaining body = %q", string(remaining))
	}
}

func newTestMITMProxy(t *testing.T, gateway gatewayForwarder, direct requestDoer) (*MITMProxy, *config.DomainRule, *x509.Certificate) {
	t.Helper()

	caCert, caTLSCert, err := GenerateSelfSignedCA()
	if err != nil {
		t.Fatalf("GenerateSelfSignedCA: %v", err)
	}

	proxy := newMITMProxyWithClients(
		NewCertStore(caCert, caTLSCert),
		"https://gateway.example.com",
		"vk-test-123",
		gateway,
		direct,
	)

	rule := &config.DomainRule{
		Hostname:          "chatgpt.com",
		IntegrationPrefix: "/chatgpt",
		PreservePath:      true,
		ProxyPathPrefixes: []string{"/backend-api/f/conversation"},
	}

	return proxy, rule, caCert
}

func newCertPool(cert *x509.Certificate) *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return pool
}
