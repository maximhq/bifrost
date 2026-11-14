package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"github.com/maximhq/bifrost/core/network"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAgentPrivateDestinationsRequirePermission verifies card requests are
// stopped before the destination receives them and that auth bypass cannot
// borrow operator permissions.
func TestAgentPrivateDestinationsRequirePermission(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(&a2a.AgentCard{SupportedInterfaces: []*a2a.AgentInterface{{URL: "https://agent.example", ProtocolBinding: a2a.TransportProtocolJSONRPC, ProtocolVersion: a2a.Version}}})
	}))
	defer server.Close()
	entries := []string(nil)
	m := &Manager{httpClient: server.Client(), privateAllowlistFn: func() []string { return append([]string(nil), entries...) }}
	_, err := m.resolveCard(context.Background(), server.URL, nil, "fixture")
	require.ErrorContains(t, err, "blocked connection")
	require.ErrorContains(t, err, "client.a2a_private_allowlist")
	require.ErrorContains(t, err, "bifrost.client.a2aPrivateAllowlist")
	require.Zero(t, calls.Load())
	entries = []string{"127.0.0.1"}
	_, err = m.resolveCard(context.Background(), server.URL, nil, "fixture")
	require.NoError(t, err)
	require.EqualValues(t, 1, calls.Load())
	_, err = m.resolveCard(WithPublicTargetsOnly(context.Background(), true), server.URL, nil, "fixture")
	require.ErrorContains(t, err, "blocked connection")
	require.EqualValues(t, 1, calls.Load())
}

// TestAgentPermissionRevocationStopsPooledRequests verifies that a connection
// approved under old permissions cannot serve a new request after revocation.
func TestAgentPermissionRevocationStopsPooledRequests(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	entries := []string{"127.0.0.1"}
	m := &Manager{httpClient: server.Client(), privateAllowlistFn: func() []string { return append([]string(nil), entries...) }}
	client, err := m.clientFor(nil, nil, true)
	require.NoError(t, err)
	defer client.CloseIdleConnections()
	resp, err := client.Get(server.URL)
	require.NoError(t, err)
	_ = resp.Body.Close()
	entries = nil
	_, err = client.Get(server.URL)
	require.ErrorContains(t, err, "permissions changed")
	require.EqualValues(t, 1, calls.Load())
}

// TestAgentOAuthUsesDestinationPermissions verifies both discovery and token
// calls use the same policy and cached tokens cannot cross the auth boundary.
func TestAgentOAuthUsesDestinationPermissions(t *testing.T) {
	var calls atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/discover" {
			_ = json.NewEncoder(w).Encode(map[string]string{"token_endpoint": server.URL + "/token"})
			return
		}
		assert.Equal(t, "secret", r.FormValue("client_secret"))
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "token", "expires_in": 3600})
	}))
	defer server.Close()
	m := &Manager{httpClient: server.Client(), privateAllowlistFn: func() []string { return []string{"127.0.0.1"} }}
	auth := &schemas.UpstreamAuth{Type: schemas.MCPAuthTypeOauth, OAuth: &schemas.UpstreamOAuthConfig{DiscoveryURL: server.URL + "/discover", ClientID: schemas.NewSecretVar("client"), ClientSecret: schemas.NewSecretVar("secret")}}
	token, err := m.oauthAccessToken(context.Background(), "fixture", auth)
	require.NoError(t, err)
	require.Equal(t, "token", token)
	require.EqualValues(t, 2, calls.Load())
	_, err = m.oauthAccessToken(WithPublicTargetsOnly(context.Background(), true), "fixture", auth)
	require.ErrorContains(t, err, "blocked connection")
	require.EqualValues(t, 2, calls.Load())
	auth.OAuth.TokenURL = server.URL + "/token"
	_, _, err = m.fetchOAuthAccessToken(WithPublicTargetsOnly(context.Background(), true), auth)
	require.ErrorContains(t, err, "blocked connection")
	require.EqualValues(t, 2, calls.Load())
}

// TestAgentCredentialOrigins covers canonical service identity and rejects
// components that cannot identify an origin safely.
func TestAgentCredentialOrigins(t *testing.T) {
	origins, err := ValidateCredentialOrigins([]string{"https://AGENT.example:443", "http://[::1]:8080", "https://agent.example"})
	require.NoError(t, err)
	require.Equal(t, []string{"http://[::1]:8080", "https://agent.example"}, origins)
	for _, value := range []string{"ftp://host", "https://user:secret@host", "https://host/path", "https://host?", "https://host#", "https://host#secret", "host:9000"} {
		_, err := ValidateCredentialOrigins([]string{value})
		require.Error(t, err, value)
		require.NotContains(t, err.Error(), value)
	}
	target, err := url.Parse("https://runtime.example:9000/task")
	require.NoError(t, err)
	require.False(t, permitsCredentialOrigin("https://cards.example/card", nil, target))
	require.True(t, permitsCredentialOrigin("https://cards.example/card", []string{"https://runtime.example:9000"}, target))
}

// TestRuntimeOriginCheckedBeforeCredentialTransport proves even a permitted
// network destination receives no credentials until its origin is approved.
func TestRuntimeOriginCheckedBeforeCredentialTransport(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		assert.Equal(t, "Bearer secret", r.Header.Get("Authorization"))
	}))
	defer server.Close()
	m := &Manager{httpClient: server.Client(), privateAllowlistFn: func() []string { return []string{"127.0.0.1"} }}
	client, err := m.clientFor(nil, http.Header{"Authorization": {"Bearer secret"}}, true)
	require.NoError(t, err)
	defer client.CloseIdleConnections()
	transport := client.Transport.(*credentialTransport)
	transport.allowedOrigin = func(target *url.URL) bool { return permitsCredentialOrigin("https://cards.example/card", nil, target) }
	_, err = client.Get(server.URL)
	require.ErrorContains(t, err, "credential_origins")
	require.Zero(t, calls.Load())
	transport.allowedOrigin = func(target *url.URL) bool {
		return permitsCredentialOrigin("https://cards.example/card", []string{server.URL}, target)
	}
	resp, err := client.Get(server.URL)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.EqualValues(t, 1, calls.Load())
}

// BenchmarkAgentPolicyCurrent measures the permission check on the request path.
func BenchmarkAgentPolicyCurrent(b *testing.B) {
	m := &Manager{privateAllowlistFn: func() []string { return []string{"research.internal"} }}
	policy, err := m.outboundPolicy(context.Background())
	require.NoError(b, err)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if !m.policyCurrent(policy) {
			b.Fatal("policy changed")
		}
	}
}

// BenchmarkAgentAcquireNetworkPolicy compares the existing generation lease
// path with and without the new live permission check, excluding network I/O.
func BenchmarkAgentAcquireNetworkPolicy(b *testing.B) {
	for _, enabled := range []bool{false, true} {
		name := "baseline"
		if enabled {
			name = "guarded"
		}
		b.Run(name, func(b *testing.B) {
			m := &Manager{}
			generation := newClientGeneration(nil, &fakeSDKClient{}, "JSONRPC")
			if enabled {
				policy, err := m.outboundPolicy(context.Background())
				require.NoError(b, err)
				generation.networkPolicy = &policy
			}
			runtime := &runtimeAgent{generation: generation}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				lease, err := runtime.acquire(context.Background(), m)
				if err != nil {
					b.Fatal(err)
				}
				lease.release()
			}
		})
	}
}

// TestAgentProxyPinsDestination proves the proxy receives a checked IP rather
// than resolving an attacker-controlled hostname after validation.
func TestAgentProxyPinsDestination(t *testing.T) {
	var connectHost atomic.Value
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.Host, "research.example")
		_, _ = w.Write([]byte("ok"))
	}))
	defer target.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodConnect, r.Method)
		connectHost.Store(r.Host)
		upstream, err := net.Dial("tcp", r.Host)
		if err != nil {
			http.Error(w, "dial failed", 502)
			return
		}
		client, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			upstream.Close()
			return
		}
		_, _ = client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		go func() { _, _ = io.Copy(upstream, client); upstream.Close() }()
		_, _ = io.Copy(client, upstream)
		client.Close()
	}))
	defer proxy.Close()
	proxyURL, err := url.Parse(proxy.URL)
	require.NoError(t, err)
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = func(req *http.Request) (*url.URL, error) {
		assert.Equal(t, "http", req.URL.Scheme)
		return proxyURL, nil
	}
	m := &Manager{httpClient: &http.Client{Transport: tr}}
	policy := outboundPolicy{dial: network.NewDialPolicy((&net.Dialer{}).DialContext, func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("127.0.0.1")}, nil })}
	client := &http.Client{Transport: m.configureOutboundTransport(tr, policy)}
	defer client.CloseIdleConnections()
	targetURL, err := url.Parse(target.URL)
	require.NoError(t, err)
	resp, err := client.Get("http://research.example:" + targetURL.Port())
	require.NoError(t, err)
	_, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, targetURL.Host, connectHost.Load())
	client.CloseIdleConnections()
	// A refused resolution must not create a CONNECT tunnel at all.
	policy.dial = network.NewDialPolicy((&net.Dialer{}).DialContext, func(context.Context, string) ([]net.IP, error) { return nil, errors.New("blocked destination") })
	tr = http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = func(*http.Request) (*url.URL, error) { return proxyURL, nil }
	client = &http.Client{Transport: m.configureOutboundTransport(tr, policy)}
	defer client.CloseIdleConnections()
	_, err = client.Get("http://research.example:9999")
	require.ErrorContains(t, err, "blocked destination")
	require.Equal(t, targetURL.Host, connectHost.Load())
}

// TestAgentReplacedProxyClosesInFlightConnection verifies retirement closes sockets when active responses finish.
func TestAgentReplacedProxyClosesInFlightConnection(t *testing.T) {
	release := make(chan struct{})
	finish := sync.OnceFunc(func() { close(release) })
	closed := make(chan struct{}, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "2")
		_, _ = w.Write([]byte("a"))
		w.(http.Flusher).Flush()
		<-release
		_, _ = w.Write([]byte("b"))
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			closed <- struct{}{}
		}
	}
	server.Start()
	defer server.Close()
	defer finish()
	old := http.DefaultTransport.(*http.Transport).Clone()
	old.Proxy = nil
	defer old.CloseIdleConnections()
	proxyURL, err := url.Parse("http://proxy.example:8080")
	require.NoError(t, err)
	var replaced atomic.Bool
	transport := &outboundHTTPTransport{
		direct:   http.DefaultTransport.(*http.Transport).Clone(),
		proxied:  old,
		proxyURL: proxyURL.String(),
		proxy: func(*http.Request) (*url.URL, error) {
			if replaced.Load() {
				return url.Parse("http://replacement.example:8080")
			}
			return proxyURL, nil
		},
		policy: outboundPolicy{dial: network.NewDialPolicy((&net.Dialer{}).DialContext, func(context.Context, string) ([]net.IP, error) {
			return nil, errors.New("replacement destination blocked")
		})},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	resp, err := client.Get(server.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	replaced.Store(true)
	_, err = client.Get("http://runtime.example/rpc")
	require.ErrorContains(t, err, "replacement destination blocked")
	finish()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "ab", string(body))
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("retired transport retained the completed connection")
	}
}

// TestAgentRuntimeCredentialOriginBindings rejects unapproved credential
// destinations before HTTP, REST or gRPC transport construction.
func TestAgentRuntimeCredentialOriginBindings(t *testing.T) {
	for _, binding := range []a2a.TransportProtocol{a2a.TransportProtocolJSONRPC, a2a.TransportProtocolHTTPJSON, a2a.TransportProtocolGRPC} {
		t.Run(string(binding), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(&a2a.AgentCard{SupportedInterfaces: []*a2a.AgentInterface{{URL: "https://runtime.example:9000", ProtocolBinding: binding, ProtocolVersion: a2a.Version}}})
			}))
			defer server.Close()
			m := &Manager{httpClient: server.Client(), privateAllowlistFn: func() []string { return []string{"127.0.0.1"} }}
			config := runtimeConfig{agentCardURL: server.URL, runtimeAuth: &schemas.UpstreamAuth{Type: schemas.MCPAuthTypeHeaders, Headers: map[string]schemas.SecretVar{"Authorization": *schemas.NewSecretVar("secret")}}}
			_, err := m.buildGeneration(context.Background(), config)
			require.ErrorContains(t, err, "credential_origins")
			config.credentialOrigins = []string{"https://runtime.example:9000"}
			generation, err := m.buildGeneration(context.Background(), config)
			require.NoError(t, err)
			generation.retire()
			config.runtimeAuth = nil
			config.credentialOrigins = nil
			_, err = m.buildGeneration(context.Background(), config)
			require.ErrorContains(t, err, "credential_origins", "caller-supplied credentials must not bypass approval")
			config.forwardAcceptedCredential = true
			config.credentialOrigins = nil
			_, err = m.buildGeneration(context.Background(), config)
			require.ErrorContains(t, err, "credential_origins")
		})
	}
}

// TestAgentRuntimeMTLSOriginApproval checks an actual TLS client-certificate
// handshake and proves an unapproved endpoint receives neither a connection nor credentials.
func TestAgentRuntimeMTLSOriginApproval(t *testing.T) {
	var runtimeCalls atomic.Int32
	runtime := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimeCalls.Add(1)
		if r.TLS == nil || len(r.TLS.PeerCertificates) != 1 {
			t.Error("runtime did not receive a client certificate")
		}
		w.WriteHeader(http.StatusOK)
	}))
	runtime.TLS = &tls.Config{ClientAuth: tls.RequireAnyClientCert}
	runtime.StartTLS()
	defer runtime.Close()
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: runtime.Certificate().Raw})
	keyDER, err := x509.MarshalPKCS8PrivateKey(runtime.TLS.Certificates[0].PrivateKey)
	require.NoError(t, err)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	auth := &schemas.UpstreamAuth{Type: schemas.MCPAuthTypeNone, TLS: &schemas.UpstreamTLSConfig{CACertPEM: schemas.NewSecretVar(string(certPEM)), ClientCertPEM: schemas.NewSecretVar(string(certPEM)), ClientKeyPEM: schemas.NewSecretVar(string(keyPEM))}}
	card := httptest.NewServer(a2asrv.NewStaticAgentCardHandler(&a2a.AgentCard{SupportedInterfaces: []*a2a.AgentInterface{a2a.NewAgentInterface(runtime.URL, a2a.TransportProtocolJSONRPC)}}))
	defer card.Close()
	m := &Manager{httpClient: http.DefaultClient, privateAllowlistFn: func() []string { return []string{"127.0.0.1"} }}
	config := runtimeConfig{agentCardURL: card.URL, runtimeAuth: auth}
	_, err = m.buildGeneration(t.Context(), config)
	require.ErrorContains(t, err, "credential_origins")
	require.Zero(t, runtimeCalls.Load())
	config.credentialOrigins = []string{runtime.URL}
	generation, err := m.buildGeneration(t.Context(), config)
	require.NoError(t, err)
	generation.retire()
	client, err := m.clientFor(auth, nil, true)
	require.NoError(t, err)
	defer client.CloseIdleConnections()
	client.Transport.(*credentialTransport).allowedOrigin = func(u *url.URL) bool { return permitsCredentialOrigin(card.URL, config.credentialOrigins, u) }
	resp, err := client.Get(runtime.URL)
	require.NoError(t, err)
	resp.Body.Close()
	require.EqualValues(t, 1, runtimeCalls.Load())
}

// TestAgentDestinationErrorGuidance distinguishes permissions from permanently blocked targets.
func TestAgentDestinationErrorGuidance(t *testing.T) {
	blocked := &network.PrivateTargetNotAllowedError{Host: "internal.example", IP: net.ParseIP("10.0.0.1")}
	err := agentDestinationError(blocked, false)
	require.ErrorContains(t, err, "Agent Settings")
	require.ErrorIs(t, err, blocked)
	require.ErrorContains(t, agentDestinationError(blocked, true), "authenticated admin session")
	require.NotContains(t, agentDestinationError(blocked, true).Error(), "Helm")
	permanent := errors.New("blocked connection to link-local address")
	require.Same(t, permanent, agentDestinationError(permanent, false))
	require.NoError(t, agentDestinationError(nil, false))
}

// TestCardHandlerExplainsPrivateNetworkDenial reports an actionable reason without leaking upstream details.
func TestCardHandlerExplainsPrivateNetworkDenial(t *testing.T) {
	const cardURL = "http://127.0.0.1:19999/secret-card-path?token=secret-token"
	store := &memoryStore{regs: map[string]schemas.AgentRegistration{
		"private-agent": {Name: "private-agent", AgentCardURL: cardURL, Enabled: true, AllowByDefault: true},
	}}
	m, err := NewManager(context.Background(), store, nil, "http://gateway", nil)
	require.NoError(t, err)
	defer m.Close()
	handler, found := m.CardHandler("private-agent", "")
	require.True(t, found)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusBadGateway, response.Code)
	require.Equal(t, "application/json", response.Header().Get("Content-Type"))
	var bifrostErr schemas.BifrostError
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &bifrostErr))
	require.True(t, bifrostErr.IsBifrostError)
	require.NotNil(t, bifrostErr.Error)
	require.Contains(t, bifrostErr.Error.Message, "blocked by the Agent Gateway private network policy")
	body := response.Body.String()
	require.Contains(t, body, "blocked by the Agent Gateway private network policy")
	require.Contains(t, body, "Bifrost administrator")
	require.Contains(t, body, "Agent Settings")
	require.NotContains(t, body, "127.0.0.1")
	require.NotContains(t, body, "secret-card-path")
	require.NotContains(t, body, "secret-token")
}

// TestA2AOperationErrorAttribution keeps gateway policy failures separate from upstream failures.
func TestA2AOperationErrorAttribution(t *testing.T) {
	blocked := &network.PrivateTargetNotAllowedError{Host: "internal.example", IP: net.ParseIP("10.0.0.1")}
	policyError := agentDestinationError(blocked, false)
	wrapped := wrapA2AOpError(policyError, schemas.A2ARequestTypeGetAgentCard, "agent")
	require.True(t, wrapped.IsBifrostError)
	require.ErrorIs(t, wrapped.Error.Error, blocked)
	wrapped = wrapA2AOpError(gatewayPolicyError("upstream runtime origin is not permitted by credential_origins"), schemas.A2ARequestTypeGetAgentCard, "agent")
	require.True(t, wrapped.IsBifrostError)
	upstream := errors.New("upstream server failed")
	wrapped = wrapA2AOpError(upstream, schemas.A2ARequestTypeGetAgentCard, "agent")
	require.False(t, wrapped.IsBifrostError)
	require.ErrorIs(t, wrapped.Error.Error, upstream)
}
