package mcp

import (
	"context"
	"encoding/pem"
	"errors"
	"github.com/mark3labs/mcp-go/client"
	protocol "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type rotatingOAuthCredentials struct {
	schemas.MCPCredentialStore
	token atomic.Value
}

func (c *rotatingOAuthCredentials) ConnectionHeaders(_ *schemas.BifrostContext, _ *schemas.MCPClientConfig) (http.Header, error) {
	return http.Header{"Authorization": {"Bearer " + c.token.Load().(string)}}, nil
}

func TestSharedOAuthConnectionUsesRefreshedToken(t *testing.T) {
	creds := &rotatingOAuthCredentials{}
	creds.token.Store("first")
	upstream := server.NewStreamableHTTPServer(server.NewMCPServer("rotation-test", "1", server.WithToolCapabilities(false)))
	defer upstream.Shutdown(context.Background())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+creds.token.Load().(string) {
			http.Error(w, "expired token", http.StatusUnauthorized)
			return
		}
		upstream.ServeHTTP(w, r)
	}))
	defer srv.Close()
	m := &MCPManager{credStore: creds}
	c, _, err := m.createHTTPConnection(context.Background(), &schemas.MCPClientConfig{
		AuthType: schemas.MCPAuthTypeOauth, ConnectionString: schemas.NewSecretVar(srv.URL),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.Initialize(ctx, protocol.InitializeRequest{Params: protocol.InitializeParams{
		ProtocolVersion: protocol.LATEST_PROTOCOL_VERSION,
		ClientInfo:      protocol.Implementation{Name: "test", Version: "1"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	creds.token.Store("refreshed")
	if err := c.Ping(ctx); err != nil {
		t.Fatalf("existing connection did not pick up refreshed token: %v", err)
	}
	if _, err := c.ListTools(ctx, protocol.ListToolsRequest{}); err != nil {
		t.Fatalf("tools/list lost live rotated credential: %v", err)
	}
}

type oauthTestRoundTripper func(*http.Request) (*http.Response, error)

func (f oauthTestRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type oauthTestBody struct{ closed atomic.Int32 }

func (b *oauthTestBody) Read([]byte) (int, error) { return 0, io.EOF }
func (b *oauthTestBody) Close() error             { b.closed.Add(1); return nil }

type oauthTestContextKey struct{}
type oauthTestCredentials struct {
	schemas.MCPCredentialStore
	token       atomic.Value
	calls       atomic.Int32
	contextSeen atomic.Int32
}

func (c *oauthTestCredentials) ConnectionHeaders(ctx *schemas.BifrostContext, _ *schemas.MCPClientConfig) (http.Header, error) {
	c.calls.Add(1)
	if ctx.Value(oauthTestContextKey{}) == "marker" {
		c.contextSeen.Add(1)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return http.Header{"Authorization": {"Bearer " + c.token.Load().(string)}}, nil
}
func connectOAuthTestClient(t *testing.T, ctx context.Context, m *MCPManager, cfg *schemas.MCPClientConfig, o *schemas.BifrostMCPConnectRequest) *client.Client {
	t.Helper()
	c, info, err := m.createHTTPConnection(ctx, cfg, o)
	require.NoError(t, err)
	require.NotNil(t, info)
	require.NoError(t, c.Start(ctx))
	t.Cleanup(func() { _ = c.Close() })
	_, err = c.Initialize(ctx, protocol.InitializeRequest{Params: protocol.InitializeParams{ProtocolVersion: protocol.LATEST_PROTOCOL_VERSION, ClientInfo: protocol.Implementation{Name: "v7-local", Version: "1"}}})
	require.NoError(t, err)
	return c
}

// This exercises the actual manager + live MCP connection with TLS, plugin URL
// and static headers, allowed per-request extras, stale auth overrides and the
// same connection after the credential store advances.
func TestRefreshingOAuthRealTLSConnectionAndHeaders(t *testing.T) {
	creds := &oauthTestCredentials{}
	creds.token.Store("first")
	upstream := server.NewStreamableHTTPServer(server.NewMCPServer("v7-tls", "1", server.WithToolCapabilities(false)))
	t.Cleanup(func() { _ = upstream.Shutdown(context.Background()) })
	var mu sync.Mutex
	var seen []http.Header
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Clone())
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+creds.token.Load().(string) {
			http.Error(w, "bad bearer", 401)
			return
		}
		upstream.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	cfg := &schemas.MCPClientConfig{AuthType: schemas.MCPAuthTypeOauth, ConnectionString: schemas.NewSecretVar("http://127.0.0.1:1/not-used"),
		TLSConfig: &schemas.MCPTLSConfig{CACertPEM: schemas.NewSecretVar(ca)}, AllowedExtraHeaders: schemas.WhiteList{"X-Allowed", "Authorization"}}
	pluginHeaders := map[string]string{"X-Static": "static", "X-Plugin": "plugin", "Authorization": "Bearer init-stale"}
	target := srv.URL
	o := &schemas.BifrostMCPConnectRequest{ConnectionString: &target, Headers: pluginHeaders}
	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), oauthTestContextKey{}, "marker"), 5*time.Second)
	defer cancel()
	bf := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
	bf.SetValue(schemas.BifrostContextKeyMCPExtraHeaders, map[string][]string{"X-Allowed": {"per-call"}, "X-Denied": {"must-not-send"}, "Authorization": {"Bearer extra-stale"}})
	c := connectOAuthTestClient(t, bf, &MCPManager{credStore: creds}, cfg, o)
	require.NoError(t, c.Ping(bf))
	creds.token.Store("refreshed")
	require.NoError(t, c.Ping(bf))
	_, err := c.ListTools(bf, protocol.ListToolsRequest{})
	require.NoError(t, err)
	require.Equal(t, "Bearer init-stale", pluginHeaders["Authorization"], "caller plugin map must remain unchanged")
	require.Greater(t, creds.calls.Load(), int32(2))
	require.Greater(t, creds.contextSeen.Load(), int32(0), "request context must reach live resolver")
	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, seen)
	for _, h := range seen {
		require.Equal(t, "static", h.Get("X-Static"))
		require.Equal(t, "plugin", h.Get("X-Plugin"))
		require.Equal(t, "per-call", h.Get("X-Allowed"))
		require.Empty(t, h.Get("X-Denied"))
		require.NotContains(t, h.Get("Authorization"), "stale")
	}
	// The extra context header must not override the refreshed credential.
	require.Equal(t, "Bearer refreshed", seen[len(seen)-1].Get("Authorization"))
}

// No trust must fail on real TLS, not a changed expectation or permissive TLS.
func TestRefreshingOAuthUntrustedTLSFails(t *testing.T) {
	creds := &oauthTestCredentials{}
	creds.token.Store("first")
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer srv.Close()
	m := &MCPManager{credStore: creds}
	cfg := &schemas.MCPClientConfig{AuthType: schemas.MCPAuthTypeOauth, ConnectionString: schemas.NewSecretVar(srv.URL)}
	c, _, err := m.createHTTPConnection(context.Background(), cfg, nil)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(t, c.Start(ctx))
	defer c.Close()
	_, err = c.Initialize(ctx, protocol.InitializeRequest{Params: protocol.InitializeParams{ProtocolVersion: protocol.LATEST_PROTOCOL_VERSION, ClientInfo: protocol.Implementation{Name: "v7-local", Version: "1"}}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "certificate")
}

// Client clone preserves timeout/redirect/jar/base transport, original client
// and original request. Trusted TLS uses the manager's real guarded transport.
func TestRefreshingOAuthHTTPClientCloneAndTLS(t *testing.T) {
	var headers http.Header
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { headers = r.Header.Clone(); w.WriteHeader(200) }))
	defer srv.Close()
	pemCA := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	orig, err := (&MCPManager{}).buildTLSHTTPClient(&schemas.MCPClientConfig{TLSConfig: &schemas.MCPTLSConfig{CACertPEM: schemas.NewSecretVar(pemCA)}})
	require.NoError(t, err)
	base := orig.Transport.(*http.Transport)
	require.False(t, base.TLSClientConfig.InsecureSkipVerify)
	require.NotNil(t, base.TLSClientConfig.RootCAs)
	orig.Timeout = 2 * time.Second
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	orig.Jar = jar
	redirects := 0
	orig.CheckRedirect = func(r *http.Request, via []*http.Request) error { redirects++; return http.ErrUseLastResponse }
	c, err := newRefreshingOAuthHTTPClient(orig, srv.URL, func(context.Context) (http.Header, error) {
		return http.Header{"Authorization": {"Bearer latest"}}, nil
	})
	require.NoError(t, err)
	require.NotSame(t, orig, c)
	require.Equal(t, orig.Timeout, c.Timeout)
	require.Same(t, jar, c.Jar)
	require.Same(t, base, orig.Transport)
	require.NotSame(t, orig.Transport, c.Transport)
	require.NotNil(t, c.CheckRedirect)
	require.ErrorIs(t, c.CheckRedirect(nil, nil), http.ErrUseLastResponse)
	require.Equal(t, 1, redirects)
	req, err := http.NewRequest("POST", srv.URL, strings.NewReader("synthetic"))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer stale")
	req.Header.Set("X-Static", "retain")
	resp, err := c.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, "Bearer latest", headers.Get("Authorization"))
	require.Equal(t, "retain", headers.Get("X-Static"))
	require.Equal(t, "Bearer stale", req.Header.Get("Authorization"))
	require.Equal(t, 2*time.Second, orig.Timeout)
	require.False(t, base.TLSClientConfig.InsecureSkipVerify)
}

func TestRefreshingOAuthRedirectBoundaries(t *testing.T) {
	var calls atomic.Int32
	var lastAuth atomic.Value
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		lastAuth.Store(r.Header.Get("Authorization"))
		w.WriteHeader(200)
	}))
	defer target.Close()
	var mode atomic.Value
	mode.Store("same")
	var source *httptest.Server
	source = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/same" {
			lastAuth.Store(r.Header.Get("Authorization"))
			w.WriteHeader(200)
			return
		}
		to := source.URL + "/same"
		if mode.Load().(string) == "cross" {
			to = target.URL
		}
		http.Redirect(w, r, to, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	resolve := func(context.Context) (http.Header, error) {
		return http.Header{"Authorization": {"Bearer current"}}, nil
	}
	c, err := newRefreshingOAuthHTTPClient(nil, source.URL, resolve)
	require.NoError(t, err)
	resp, err := c.Get(source.URL)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, "Bearer current", lastAuth.Load())
	mode.Store("cross")
	_, err = c.Get(source.URL)
	require.Error(t, err)
	require.Zero(t, calls.Load(), "different-port redirect must never reach destination")
	origin, err := url.Parse(source.URL)
	require.NoError(t, err)
	for _, change := range []string{"scheme", "host", "port"} {
		t.Run(change, func(t *testing.T) {
			u := *origin
			switch change {
			case "scheme":
				u.Scheme = "https"
			case "host":
				u.Host = "localhost:" + origin.Port()
			case "port":
				u.Host = "127.0.0.1:1"
			}
			body := &oauthTestBody{}
			req, err := http.NewRequest("POST", u.String(), body)
			require.NoError(t, err)
			_, err = c.Transport.RoundTrip(req)
			require.Error(t, err)
			require.Greater(t, body.closed.Load(), int32(0))
		})
	}
	// Existing CheckRedirect veto remains effective even on same origin.
	veto := errors.New("caller redirect veto")
	orig := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return veto }}
	cc, err := newRefreshingOAuthHTTPClient(orig, source.URL, resolve)
	require.NoError(t, err)
	mode.Store("same")
	_, err = cc.Get(source.URL)
	require.ErrorIs(t, err, veto)
}

func TestRefreshingOAuthFailClosedBodyAndCancellation(t *testing.T) {
	for _, kind := range []string{"resolve-error", "missing", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			var baseCalls atomic.Int32
			sentinel := errors.New("credential resolution failed")
			original := &http.Client{Transport: oauthTestRoundTripper(func(r *http.Request) (*http.Response, error) {
				baseCalls.Add(1)
				if r.Body != nil {
					_ = r.Body.Close()
				}
				return nil, errors.New("unexpected base")
			})}
			ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), oauthTestContextKey{}, "marker"), time.Second)
			defer cancel()
			if kind == "cancel" {
				cancel()
			}
			resolve := func(got context.Context) (http.Header, error) {
				require.Equal(t, "marker", got.Value(oauthTestContextKey{}))
				_, ok := got.Deadline()
				require.True(t, ok)
				if kind == "cancel" {
					return nil, got.Err()
				}
				if kind == "missing" {
					return http.Header{}, nil
				}
				return nil, sentinel
			}
			c, err := newRefreshingOAuthHTTPClient(original, "http://127.0.0.1:12345", resolve)
			require.NoError(t, err)
			body := &oauthTestBody{}
			req, err := http.NewRequestWithContext(ctx, "POST", "http://127.0.0.1:12345", body)
			require.NoError(t, err)
			req.Header.Set("Authorization", "Bearer stale")
			_, err = c.Transport.RoundTrip(req)
			require.Error(t, err)
			require.Zero(t, baseCalls.Load())
			require.Greater(t, body.closed.Load(), int32(0))
			if kind == "cancel" {
				require.ErrorIs(t, err, context.Canceled)
			}
			if kind == "resolve-error" {
				require.ErrorIs(t, err, sentinel)
			}
			require.Equal(t, "Bearer stale", req.Header.Get("Authorization"))
		})
	}
	for _, target := range []string{"not-a-url", "http:///missing-host", "file:///tmp/forbidden", "http://%"} {
		_, err := newRefreshingOAuthHTTPClient(nil, target, func(context.Context) (http.Header, error) {
			t.Fatal("invalid target must not resolve credentials")
			return nil, nil
		})
		require.Error(t, err)
	}
}

func TestRefreshingOAuthNonOAuthDoesNotResolvePerRequest(t *testing.T) {
	creds := &oauthTestCredentials{}
	creds.token.Store("unused")
	upstream := server.NewStreamableHTTPServer(server.NewMCPServer("v7-static", "1", server.WithToolCapabilities(false)))
	t.Cleanup(func() { _ = upstream.Shutdown(context.Background()) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// mcp-go v0.43.2 session teardown deliberately omits c.headers/headerFunc.
		// Preserve that existing non-OAuth behavior; ordinary POST requests retain headers.
		if r.Method == http.MethodDelete {
			require.Empty(t, r.Header.Get("Authorization"))
			require.Empty(t, r.Header.Get("X-Plugin"))
		} else {
			require.Equal(t, "Bearer fixed", r.Header.Get("Authorization"))
			require.Equal(t, "plugin", r.Header.Get("X-Plugin"))
		}
		upstream.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cfg := &schemas.MCPClientConfig{AuthType: schemas.MCPAuthTypeHeaders, ConnectionString: schemas.NewSecretVar(srv.URL)}
	overrides := &schemas.BifrostMCPConnectRequest{Headers: map[string]string{"Authorization": "Bearer fixed", "X-Plugin": "plugin"}}
	c := connectOAuthTestClient(t, ctx, &MCPManager{credStore: creds}, cfg, overrides)
	creds.token.Store("changed")
	require.NoError(t, c.Ping(ctx))
	_, err := c.ListTools(ctx, protocol.ListToolsRequest{})
	require.NoError(t, err)
	require.Zero(t, creds.calls.Load(), "new per-request resolver must apply only to OAuth")
}

// Cancellation must be enforced at the HTTP boundary even if the resolver
// ignores cancellation or cancels the context while returning valid headers.
func TestRefreshingOAuthCancellationCannotReachBase(t *testing.T) {
	for _, stage := range []string{"before-resolve", "during-resolve"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if stage == "before-resolve" {
				cancel()
			}
			var calls atomic.Int32
			original := &http.Client{Transport: oauthTestRoundTripper(func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				if r.Body != nil {
					_ = r.Body.Close()
				}
				return nil, errors.New("cancelled request reached base")
			})}
			resolve := func(context.Context) (http.Header, error) {
				if stage == "during-resolve" {
					cancel()
				}
				return http.Header{"Authorization": {"Bearer synthetic-current"}}, nil
			}
			c, err := newRefreshingOAuthHTTPClient(original, "http://127.0.0.1:12345", resolve)
			require.NoError(t, err)
			body := &oauthTestBody{}
			req, err := http.NewRequestWithContext(ctx, "POST", "http://127.0.0.1:12345", body)
			require.NoError(t, err)
			req.Header.Set("Authorization", "Bearer original")
			_, err = c.Transport.RoundTrip(req)
			require.ErrorIs(t, err, context.Canceled)
			require.Zero(t, calls.Load(), "cancelled request must not reach base transport")
			require.Greater(t, body.closed.Load(), int32(0), "cancelled request body must be closed")
			require.Equal(t, "Bearer original", req.Header.Get("Authorization"))
		})
	}
}
