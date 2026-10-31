package agent

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/dns/dnsmessage"
)

// TestAgentOAuthSecurityBoundaries exercises the two CodeQL-flagged requests with real local HTTP servers.
func TestAgentOAuthSecurityBoundaries(t *testing.T) {
	for _, proxied := range []bool{false, true} {
		mode := "direct"
		if proxied {
			mode = "proxy"
		}
		for _, tc := range []struct {
			name        string
			allow       []string
			directToken bool
			metadata    bool
			sameOrigin  bool
			wantError   string
			discovery   int32
			token       int32
		}{
			{name: "private discovery denied", wantError: "blocked connection"},
			{name: "private direct token denied", directToken: true, wantError: "blocked connection"},
			{name: "allowed discovery cannot authorize token host", allow: []string{"localhost"}, discovery: 1, wantError: "must use the discovery document origin"},
			{name: "even allowed hosts cannot override discovery origin", allow: []string{"localhost", "127.0.0.1"}, discovery: 1, wantError: "must use the discovery document origin"},
			{name: "same origin explicitly allowed", allow: []string{"localhost"}, sameOrigin: true, discovery: 1, token: 1},
			{name: "direct token explicitly allowed", allow: []string{"127.0.0.1"}, directToken: true, token: 1},
			{name: "metadata direct token denied", directToken: true, metadata: true, wantError: "link-local"},
			{name: "metadata allowlist rejected", allow: []string{"169.254.169.254"}, directToken: true, metadata: true, wantError: "invalid Agent Gateway private network allowlist"},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				var discoveryCalls, tokenCalls, tunnels atomic.Int32
				token := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					tokenCalls.Add(1)
					assert.Equal(t, "secret", r.FormValue("client_secret"))
					_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "fixture-token", "expires_in": 3600})
				}))
				defer token.Close()
				tokenURL := token.URL
				if tc.metadata {
					tokenURL = "http://169.254.169.254/token"
				}
				var discovery *httptest.Server
				discovery = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/token" {
						tokenCalls.Add(1)
						assert.Equal(t, "secret", r.FormValue("client_secret"))
						_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "fixture-token", "expires_in": 3600})
						return
					}
					discoveryCalls.Add(1)
					advertisedToken := tokenURL
					if tc.sameOrigin {
						advertisedToken = strings.Replace(discovery.URL, "127.0.0.1", "localhost", 1) + "/token"
					}
					_ = json.NewEncoder(w).Encode(map[string]string{"token_endpoint": advertisedToken})
				}))
				defer discovery.Close()
				transport := http.DefaultTransport.(*http.Transport).Clone()
				transport.Proxy = nil
				if proxied {
					proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						tunnels.Add(1)
						assert.Equal(t, http.MethodConnect, r.Method)
						host, _, err := net.SplitHostPort(r.Host)
						if !assert.NoError(t, err) || !assert.NotNil(t, net.ParseIP(host), "proxy must receive a validated IP") {
							http.Error(w, "invalid CONNECT", http.StatusBadRequest)
							return
						}
						upstream, err := net.DialTimeout("tcp", r.Host, time.Second)
						if err != nil {
							http.Error(w, "dial failed", http.StatusBadGateway)
							return
						}
						downstream, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							_ = upstream.Close()
							return
						}
						defer downstream.Close()
						defer upstream.Close()
						_, _ = downstream.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
						done := make(chan struct{})
						go func() { _, _ = io.Copy(upstream, downstream); _ = upstream.Close(); close(done) }()
						_, _ = io.Copy(downstream, upstream)
						_ = downstream.Close()
						<-done
					}))
					defer proxy.Close()
					proxyURL, err := url.Parse(proxy.URL)
					require.NoError(t, err)
					transport.Proxy = http.ProxyURL(proxyURL)
				}
				manager := &Manager{
					httpClient:         &http.Client{Transport: transport, Timeout: 3 * time.Second},
					privateAllowlistFn: func() []string { return tc.allow },
				}
				auth := &schemas.UpstreamAuth{Type: schemas.MCPAuthTypeOauth, OAuth: &schemas.UpstreamOAuthConfig{
					DiscoveryURL: strings.Replace(discovery.URL, "127.0.0.1", "localhost", 1),
					ClientID:     schemas.NewSecretVar("client"), ClientSecret: schemas.NewSecretVar("secret"),
				}}
				if tc.directToken {
					auth.OAuth.TokenURL = tokenURL
				}
				value, _, err := manager.fetchOAuthAccessToken(t.Context(), auth)
				if tc.wantError == "" {
					require.NoError(t, err)
					require.Equal(t, "fixture-token", value)
				} else {
					require.ErrorContains(t, err, tc.wantError)
				}
				require.Equal(t, tc.discovery, discoveryCalls.Load())
				require.Equal(t, tc.token, tokenCalls.Load())
				if proxied {
					require.Zero(t, tunnels.Load(), "loopback must bypass the configured proxy without bypassing destination checks")
				}
			})
		}
	}
}

// TestAgentOAuthSecurityRedirects verifies discovery and token redirects cannot forward requests to another service.
func TestAgentOAuthSecurityRedirects(t *testing.T) {
	for _, discoveryRedirect := range []bool{true, false} {
		name := "token"
		if discoveryRedirect {
			name = "discovery"
		}
		t.Run(name, func(t *testing.T) {
			var targetCalls atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				targetCalls.Add(1)
			}))
			defer target.Close()
			redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
			}))
			defer redirect.Close()
			transport := http.DefaultTransport.(*http.Transport).Clone()
			transport.Proxy = nil
			manager := &Manager{httpClient: &http.Client{Transport: transport}, privateAllowlistFn: func() []string { return []string{"127.0.0.1"} }}
			oauth := &schemas.UpstreamOAuthConfig{ClientID: schemas.NewSecretVar("client"), ClientSecret: schemas.NewSecretVar("secret")}
			if discoveryRedirect {
				oauth.DiscoveryURL = redirect.URL
			} else {
				oauth.TokenURL = redirect.URL
			}
			_, _, err := manager.fetchOAuthAccessToken(context.Background(), &schemas.UpstreamAuth{Type: schemas.MCPAuthTypeOauth, OAuth: oauth})
			require.ErrorContains(t, err, "307")
			require.Zero(t, targetCalls.Load(), "redirect destination must receive no request or client secret")
		})
	}
}

// TestAgentOAuthSecurityProxyDNS uses the real OAuth client with controlled DNS and a local CONNECT proxy.
// It is deliberately not parallel because the production dial policy uses net.DefaultResolver.
func TestAgentOAuthSecurityProxyDNS(t *testing.T) {
	for _, tc := range []struct {
		name      string
		private   bool
		rebind    bool
		discovery int32
		token     int32
	}{
		{name: "public discovery and token succeed", discovery: 1, token: 1},
		{name: "private discovery never reaches proxy", private: true},
		{name: "token DNS rebinding blocked before second tunnel", rebind: true, discovery: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var private atomic.Bool
			private.Store(tc.private)
			originalResolver := net.DefaultResolver
			net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(context.Context, string, string) (net.Conn, error) {
				client, server := net.Pipe()
				go func() {
					defer server.Close()
					var length [2]byte
					if _, err := io.ReadFull(server, length[:]); err != nil {
						return
					}
					body := make([]byte, binary.BigEndian.Uint16(length[:]))
					if _, err := io.ReadFull(server, body); err != nil {
						return
					}
					var query dnsmessage.Message
					if !assert.NoError(t, query.Unpack(body)) {
						return
					}
					response := dnsmessage.Message{Header: dnsmessage.Header{ID: query.ID, Response: true, Authoritative: true, RecursionAvailable: true}, Questions: query.Questions}
					for _, question := range query.Questions {
						if question.Type == dnsmessage.TypeA {
							address := [4]byte{203, 0, 113, 10}
							if private.Load() {
								address = [4]byte{127, 0, 0, 1}
							}
							response.Answers = append(response.Answers, dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: question.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}, Body: &dnsmessage.AResource{A: address}})
						}
					}
					packet, err := response.Pack()
					if !assert.NoError(t, err) {
						return
					}
					binary.BigEndian.PutUint16(length[:], uint16(len(packet)))
					_, _ = server.Write(append(length[:], packet...))
				}()
				return client, nil
			}}
			t.Cleanup(func() { net.DefaultResolver = originalResolver })
			var discoveryCalls, tokenCalls, tunnels atomic.Int32
			var origin string
			service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/discover" {
					discoveryCalls.Add(1)
					if tc.rebind {
						private.Store(true)
					}
					_ = json.NewEncoder(w).Encode(map[string]string{"token_endpoint": origin + "/token"})
					return
				}
				tokenCalls.Add(1)
				assert.Equal(t, "secret", r.FormValue("client_secret"))
				_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "fixture-token", "expires_in": 3600})
			}))
			defer service.Close()
			origin = strings.Replace(service.URL, "127.0.0.1", "oauth.example.test", 1)
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				tunnels.Add(1)
				assert.Equal(t, http.MethodConnect, r.Method)
				host, _, err := net.SplitHostPort(r.Host)
				if !assert.NoError(t, err) || !assert.Equal(t, "203.0.113.10", host) {
					http.Error(w, "unexpected destination", http.StatusBadRequest)
					return
				}
				// The mock proxy represents the public IP with a local service; no external connection is made.
				upstream, err := net.DialTimeout("tcp", strings.TrimPrefix(service.URL, "http://"), time.Second)
				if err != nil {
					http.Error(w, "dial failed", http.StatusBadGateway)
					return
				}
				downstream, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					_ = upstream.Close()
					return
				}
				defer downstream.Close()
				defer upstream.Close()
				_, _ = downstream.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
				done := make(chan struct{})
				go func() { _, _ = io.Copy(upstream, downstream); _ = upstream.Close(); close(done) }()
				_, _ = io.Copy(downstream, upstream)
				_ = downstream.Close()
				<-done
			}))
			defer proxy.Close()
			proxyURL, err := url.Parse(proxy.URL)
			require.NoError(t, err)
			transport := http.DefaultTransport.(*http.Transport).Clone()
			transport.Proxy = http.ProxyURL(proxyURL)
			manager := &Manager{httpClient: &http.Client{Transport: transport, Timeout: 3 * time.Second}}
			auth := &schemas.UpstreamAuth{Type: schemas.MCPAuthTypeOauth, OAuth: &schemas.UpstreamOAuthConfig{
				DiscoveryURL: origin + "/discover", ClientID: schemas.NewSecretVar("client"), ClientSecret: schemas.NewSecretVar("secret"),
			}}
			value, _, err := manager.fetchOAuthAccessToken(t.Context(), auth)
			if tc.private || tc.rebind {
				require.ErrorContains(t, err, "blocked connection")
			} else {
				require.NoError(t, err)
				require.Equal(t, "fixture-token", value)
			}
			require.Equal(t, tc.discovery, discoveryCalls.Load())
			require.Equal(t, tc.token, tokenCalls.Load())
			require.Equal(t, tc.discovery+tc.token, tunnels.Load(), "DNS policy must run before opening CONNECT")
		})
	}
}
