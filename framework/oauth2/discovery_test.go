package oauth2

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/network"
	"github.com/maximhq/bifrost/core/network/proxytest"
	"github.com/stretchr/testify/require"
)

// TestNewOAuthDiscoveryHTTPClientBlocksLoopback proves the OAuth discovery HTTP
// client is wired through the SSRF-safe dialer: a target the discovery chain
// picked up from a upstream MCP server's own response
// must not be reachable if it points at loopback or any other private/
// link-local/CGNAT address. A context that records no destination policy, and
// one recording the public-only policy, both stay public-only even when the
// private-target allowlist names the address. TestMain overrides the dialer
// package-wide for this package's other tests (which talk to real
// httptest.Server loopback addresses) - restore the production dialer for the
// duration of this test so it exercises the actual guard.
func TestNewOAuthDiscoveryHTTPClientBlocksLoopback(t *testing.T) {
	useProductionOAuthDialer(t, "127.0.0.1")

	for _, ctx := range []context.Context{context.Background(), withRequirePublicTarget(context.Background(), true)} {
		client := newOAuthDiscoveryHTTPClient(ctx, time.Second)
		transport, ok := client.Transport.(*http.Transport)
		require.True(t, ok)
		require.NotNil(t, transport.DialContext)

		_, err := transport.DialContext(context.Background(), "tcp", "127.0.0.1:80")
		require.Error(t, err)
		require.Contains(t, err.Error(), "blocked connection to non-public address")
	}
}

// TestNewOAuthDiscoveryHTTPClientFollowsPrivateTargetAllowlist pins the
// allowlisted policy an authenticated admin's MCP client gets: a loopback OAuth
// server is reachable once allowlisted, refused when it is not, and the cloud
// metadata address is refused even with an allowlist in place.
func TestNewOAuthDiscoveryHTTPClientFollowsPrivateTargetAllowlist(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	ctx := withRequirePublicTarget(context.Background(), false)

	useProductionOAuthDialer(t)
	_, err := newOAuthDiscoveryHTTPClient(ctx, 2*time.Second).Get(server.URL)
	require.Error(t, err)
	require.Contains(t, err.Error(), "blocked connection to non-public address")

	useProductionOAuthDialer(t, "127.0.0.1")
	resp, err := newOAuthDiscoveryHTTPClient(ctx, 2*time.Second).Get(server.URL)
	require.NoError(t, err, "an allowlisted loopback OAuth server must be reachable")
	resp.Body.Close()

	transport, ok := newOAuthDiscoveryHTTPClient(ctx, time.Second).Transport.(*http.Transport)
	require.True(t, ok)
	_, err = transport.DialContext(context.Background(), "tcp", "169.254.169.254:80")
	require.Error(t, err)
	require.Contains(t, err.Error(), "blocked connection to link-local address")
}

// TestSetPrivateTargetAllowlistDropsConnectionsApprovedUnderTheOldList pins
// that removing a host from the allowlist takes effect for the next request:
// the dial-time guard only runs when a connection is created, so a keep-alive
// connection approved under the old list must not be reused afterwards.
func TestSetPrivateTargetAllowlistDropsConnectionsApprovedUnderTheOldList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	ctx := withRequirePublicTarget(context.Background(), false)

	useProductionOAuthDialer(t, "127.0.0.1")
	resp, err := newOAuthDiscoveryHTTPClient(ctx, 2*time.Second).Get(server.URL)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close() // the connection goes back to the pool for reuse

	SetPrivateTargetAllowlist(nil)
	resp, err = newOAuthDiscoveryHTTPClient(ctx, 2*time.Second).Get(server.URL)
	if resp != nil {
		resp.Body.Close()
	}
	require.Error(t, err, "a pooled connection approved under the old allowlist must not be reused")
	require.Contains(t, err.Error(), "blocked connection to non-public address")
}

// TestNewOAuthDiscoveryTransportHonorsProxyEnvironment pins that the shared
// transport still routes through HTTPS_PROXY/HTTP_PROXY the way the bare
// default-transport clients it replaced did: a proxy-only installation must
// keep discovery, registration and token exchange working.
func TestNewOAuthDiscoveryTransportHonorsProxyEnvironment(t *testing.T) {
	transport := newOAuthDiscoveryTransport((&net.Dialer{}).DialContext)
	require.NotNil(t, transport.Proxy, "transport must consult the proxy environment")
}

// TestNewOAuthDiscoveryHTTPClientRefusesRedirectForPOST pins that a 307/308 on
// the token or registration POST is not followed: Go would otherwise replay the
// credential-bearing body at whatever location the upstream server names.
func TestNewOAuthDiscoveryHTTPClientRefusesRedirectForPOST(t *testing.T) {
	hits := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	client := newOAuthDiscoveryHTTPClient(context.Background(), 2*time.Second)
	resp, err := client.Post(redirector.URL, "application/x-www-form-urlencoded", strings.NewReader("grant_type=authorization_code&code=secret"))
	if resp != nil {
		resp.Body.Close()
	}
	require.Error(t, err, "a redirected POST must fail rather than be replayed")
	require.Zero(t, hits, "the redirect target must never receive the replayed POST body")

	// Discovery GETs may still follow a redirect.
	resp, err = client.Get(redirector.URL)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, 1, hits)
}

// TestOAuthProxySelectorGuardsDestination pins the policy applied when a proxy
// is chosen: a public or hostname destination goes through the proxy, an
// IP-literal private, loopback or link-local destination is refused before the
// proxy could forward to it, and no proxy means no change.
func TestOAuthProxySelectorGuardsDestination(t *testing.T) {
	proxy, _ := url.Parse("http://proxy.internal:3128")
	viaProxy := oauthProxySelector(func(*http.Request) (*url.URL, error) { return proxy, nil })
	direct := oauthProxySelector(func(*http.Request) (*url.URL, error) { return nil, nil })
	mk := func(raw string) *http.Request {
		req, err := http.NewRequest(http.MethodGet, raw, nil)
		require.NoError(t, err)
		return req
	}

	got, err := viaProxy(mk("https://203.0.113.5/.well-known/oauth-authorization-server"))
	require.NoError(t, err)
	require.Equal(t, proxy, got)

	got, err = viaProxy(mk("https://auth.example.com/token"))
	require.NoError(t, err)
	require.Equal(t, proxy, got, "hostnames are resolved by the proxy")

	for _, raw := range []string{"http://127.0.0.1:8080/token", "http://10.0.0.5/register", "http://169.254.169.254/latest/meta-data/"} {
		_, err := viaProxy(mk(raw))
		require.Error(t, err, raw)
	}

	got, err = direct(mk("http://127.0.0.1:8080/token"))
	require.NoError(t, err)
	require.Nil(t, got, "with no proxy the dial-time guard remains the only check")

	// The allowlisted policy lets an allowlisted IP-literal destination through
	// the proxy, and still refuses unlisted private and link-local ones.
	al, err := network.ValidatePrivateTargetAllowlist([]string{"127.0.0.1"})
	require.NoError(t, err)
	allowlisted := oauthAllowlistedProxySelector(func(*http.Request) (*url.URL, error) { return proxy, nil }, func() *network.Allowlist { return al })
	got, err = allowlisted(mk("http://127.0.0.1:8080/token"))
	require.NoError(t, err)
	require.Equal(t, proxy, got)
	got, err = allowlisted(mk("https://auth.example.com/token"))
	require.NoError(t, err)
	require.Equal(t, proxy, got, "hostnames are resolved by the proxy")
	for _, raw := range []string{"http://10.0.0.5/register", "http://169.254.169.254/latest/meta-data/"} {
		_, err := allowlisted(mk(raw))
		require.Error(t, err, raw)
	}
}

// TestNewOAuthDiscoveryTransportDialsConfiguredProxyOnPrivateAddress pins the
// proxy-only deployment shape: an operator-configured proxy on a private or
// loopback address must be dialable, while the destination is never dialed
// directly and a non-proxy private address stays blocked. The production
// guarded dialer is used, so TestMain's override is cleared for this test.
func TestNewOAuthDiscoveryTransportDialsConfiguredProxyOnPrivateAddress(t *testing.T) {
	prev := testDialContextOverride
	testDialContextOverride = nil
	t.Cleanup(func() { testDialContextOverride = prev })

	var proxied []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// An HTTP proxy receives the absolute-form request; record the destination host.
		proxied = append(proxied, r.URL.Host)
		w.WriteHeader(http.StatusOK)
	}))
	defer proxy.Close()
	proxyURL, err := url.Parse(proxy.URL)
	require.NoError(t, err)

	transport := newOAuthDiscoveryTransport(oauthDialContext(2*time.Second, network.SSRFSafeDialContext(2*time.Second), proxyURL))
	transport.Proxy = oauthProxySelector(func(*http.Request) (*url.URL, error) { return proxyURL, nil })
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}

	// TEST-NET-3 is never routable; the only way this succeeds is through the proxy.
	resp, err := client.Get("http://203.0.113.5/.well-known/oauth-authorization-server")
	require.NoError(t, err, "a configured proxy on a private address must be dialable")
	resp.Body.Close()
	require.Equal(t, []string{"203.0.113.5"}, proxied)

	// Only the configured proxy address is trusted: with no proxy chosen, any
	// other private destination is still refused at dial time.
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("a private destination must never be dialed directly")
	}))
	defer other.Close()
	transport.Proxy = oauthProxySelector(func(*http.Request) (*url.URL, error) { return nil, nil })
	_, err = client.Get(other.URL)
	require.Error(t, err)
	require.Contains(t, err.Error(), "blocked connection to non-public address")
}

// TestDiscoverOAuthMetadataUsesGlobalProxy pins that MCP OAuth discovery honours the
// global proxy when it is enabled for API traffic. It used to follow only the
// environment proxy.
func TestDiscoverOAuthMetadataUsesGlobalProxy(t *testing.T) {
	set := proxytest.NewSet(t)
	network.SetDefaultHTTPClientFactory(network.NewHTTPClientFactory(&network.GlobalProxyConfig{
		Enabled: true, Type: network.GlobalProxyTypeHTTP, URL: "http://127.0.0.1:" + set.Config.Port(), EnableForAPI: true,
	}, nil))
	t.Cleanup(func() { network.SetDefaultHTTPClientFactory(nil) })

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = DiscoverOAuthMetadata(ctx, "https://mcp.bifrost.test/mcp")
	seen := set.Config.Seen()
	if len(seen) == 0 {
		t.Fatal("OAuth discovery never reached the global proxy")
	}
	for _, hit := range seen {
		if hit.Target != "mcp.bifrost.test:443" {
			t.Errorf("proxy saw %+v, want only CONNECTs to mcp.bifrost.test:443", hit)
		}
	}
}

// resourceMetadataServer is a fake MCP server that is also its own
// authorization server. advertised, when set, is served at /prm and named in the
// 401 WWW-Authenticate header; wellKnown maps a .well-known protected resource
// path to the document served there; asResource is put in the authorization
// server metadata.
type resourceMetadataServer struct {
	server     *httptest.Server
	advertised map[string]any
	wellKnown  map[string]map[string]any
	asResource string
}

func newResourceMetadataServer(t *testing.T, configure func(s *resourceMetadataServer)) *resourceMetadataServer {
	t.Helper()
	if logger == nil {
		logger = bifrost.NewNoOpLogger()
		t.Cleanup(func() { logger = nil })
	}
	s := &resourceMetadataServer{wellKnown: map[string]map[string]any{}}
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if s.advertised != nil {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+s.server.URL+`/prm"`)
		}
		w.WriteHeader(http.StatusUnauthorized)
	})
	mux.HandleFunc("/prm", func(w http.ResponseWriter, r *http.Request) {
		if s.advertised == nil {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, s.advertised)
	})
	mux.HandleFunc("/.well-known/", func(w http.ResponseWriter, r *http.Request) {
		if doc, ok := s.wellKnown[r.URL.Path]; ok {
			writeJSON(w, doc)
			return
		}
		if r.URL.Path == "/.well-known/oauth-authorization-server" {
			doc := map[string]any{
				"issuer":                 s.server.URL,
				"authorization_endpoint": s.server.URL + "/authorize",
				"token_endpoint":         s.server.URL + "/token",
			}
			if s.asResource != "" {
				doc["resource"] = s.asResource
			}
			writeJSON(w, doc)
			return
		}
		http.NotFound(w, r)
	})
	s.server = httptest.NewServer(mux)
	t.Cleanup(s.server.Close)
	configure(s)
	return s
}

// newOtherAuthServer is a second authorization server a protected resource
// metadata document can point at; its endpoints carry an "other-as" path.
func newOtherAuthServer(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 srv.URL,
			"authorization_endpoint": srv.URL + "/other-as/authorize",
			"token_endpoint":         srv.URL + "/other-as/token",
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestDiscoverOAuthMetadataIgnoresAdvertisedMetadataForAnotherResource pins
// RFC 9728 section 3.3 on the advertised path: a resource_metadata document
// whose resource names a different server is not used at all, neither its
// resource nor its authorization servers, and discovery falls back.
func TestDiscoverOAuthMetadataIgnoresAdvertisedMetadataForAnotherResource(t *testing.T) {
	otherAS := newOtherAuthServer(t)
	s := newResourceMetadataServer(t, func(s *resourceMetadataServer) {
		s.advertised = map[string]any{
			"resource":              "https://other-mcp.example/mcp",
			"authorization_servers": []string{otherAS.URL},
		}
	})

	metadata, err := DiscoverOAuthMetadata(context.Background(), s.server.URL+"/mcp")
	require.NoError(t, err)
	require.Empty(t, metadata.Resource, "a resource naming another server must not be adopted")
	require.Equal(t, s.server.URL+"/authorize", metadata.AuthorizationURL,
		"authorization servers from a non-matching document must not be used")
}

// TestDiscoverOAuthMetadataSkipsWellKnownMetadataForAnotherResource pins the
// same rule on the .well-known path: a non-matching candidate is skipped and the
// next, matching candidate is used.
func TestDiscoverOAuthMetadataSkipsWellKnownMetadataForAnotherResource(t *testing.T) {
	otherAS := newOtherAuthServer(t)
	s := newResourceMetadataServer(t, func(s *resourceMetadataServer) {
		s.wellKnown["/.well-known/oauth-protected-resource/mcp"] = map[string]any{
			"resource":              "https://other-mcp.example/mcp",
			"authorization_servers": []string{otherAS.URL},
		}
		s.wellKnown["/.well-known/oauth-protected-resource"] = map[string]any{
			"resource":              s.server.URL,
			"authorization_servers": []string{s.server.URL},
		}
	})

	metadata, err := DiscoverOAuthMetadata(context.Background(), s.server.URL+"/mcp")
	require.NoError(t, err)
	require.Equal(t, s.server.URL, metadata.Resource)
	require.Equal(t, s.server.URL+"/authorize", metadata.AuthorizationURL)
}

// TestDiscoverOAuthMetadataSkipsAllNonMatchingWellKnownMetadata pins the final
// fallback: when no candidate matches (here a path that only shares a prefix
// with the server path), no resource is adopted.
func TestDiscoverOAuthMetadataSkipsAllNonMatchingWellKnownMetadata(t *testing.T) {
	otherAS := newOtherAuthServer(t)
	s := newResourceMetadataServer(t, func(s *resourceMetadataServer) {
		doc := map[string]any{
			"resource":              s.server.URL + "/mcpx",
			"authorization_servers": []string{otherAS.URL},
		}
		s.wellKnown["/.well-known/oauth-protected-resource/mcp"] = doc
		s.wellKnown["/.well-known/oauth-protected-resource"] = doc
	})

	metadata, err := DiscoverOAuthMetadata(context.Background(), s.server.URL+"/mcp")
	require.NoError(t, err)
	require.Empty(t, metadata.Resource)
	require.Equal(t, s.server.URL+"/authorize", metadata.AuthorizationURL)
}

// TestDiscoverOAuthMetadataIgnoresMetadataWithoutResource pins RFC 9728 section
// 3.3 for a document that omits resource: it identifies no protected resource,
// so neither the advertised document nor a .well-known candidate without one is
// used, and its authorization servers are never adopted.
func TestDiscoverOAuthMetadataIgnoresMetadataWithoutResource(t *testing.T) {
	otherAS := newOtherAuthServer(t)
	noResource := map[string]any{"authorization_servers": []string{otherAS.URL}}
	emptyResource := map[string]any{"resource": "", "authorization_servers": []string{otherAS.URL}}
	for name, doc := range map[string]map[string]any{"absent": noResource, "empty": emptyResource} {
		t.Run(name+" advertised", func(t *testing.T) {
			s := newResourceMetadataServer(t, func(s *resourceMetadataServer) {
				s.advertised = doc
			})
			metadata, err := DiscoverOAuthMetadata(context.Background(), s.server.URL+"/mcp")
			require.NoError(t, err)
			require.Empty(t, metadata.Resource)
			require.Equal(t, s.server.URL+"/authorize", metadata.AuthorizationURL,
				"authorization servers from a document without a resource must not be used")
		})
		t.Run(name+" well-known", func(t *testing.T) {
			s := newResourceMetadataServer(t, func(s *resourceMetadataServer) {
				s.wellKnown["/.well-known/oauth-protected-resource/mcp"] = doc
				s.wellKnown["/.well-known/oauth-protected-resource"] = doc
			})
			metadata, err := DiscoverOAuthMetadata(context.Background(), s.server.URL+"/mcp")
			require.NoError(t, err)
			require.Empty(t, metadata.Resource)
			require.Equal(t, s.server.URL+"/authorize", metadata.AuthorizationURL,
				"authorization servers from a document without a resource must not be used")
		})
	}
}

// TestDiscoverOAuthMetadataAdoptsMatchingAdvertisedResource pins that a
// matching document is still honoured, including a differently cased scheme and
// a trailing slash.
func TestDiscoverOAuthMetadataAdoptsMatchingAdvertisedResource(t *testing.T) {
	otherAS := newOtherAuthServer(t)
	var resource string
	s := newResourceMetadataServer(t, func(s *resourceMetadataServer) {
		resource = "HTTP" + strings.TrimPrefix(s.server.URL, "http") + "/mcp/"
		s.advertised = map[string]any{
			"resource":              resource,
			"authorization_servers": []string{otherAS.URL},
		}
	})

	metadata, err := DiscoverOAuthMetadata(context.Background(), s.server.URL+"/mcp")
	require.NoError(t, err)
	require.Equal(t, resource, metadata.Resource)
	require.Equal(t, otherAS.URL+"/other-as/authorize", metadata.AuthorizationURL)
}

// TestDiscoverOAuthMetadataIgnoresAuthorizationServerResource pins that the
// resource indicator never comes from authorization server metadata, which RFC
// 8414 does not define a resource member for.
func TestDiscoverOAuthMetadataIgnoresAuthorizationServerResource(t *testing.T) {
	s := newResourceMetadataServer(t, func(s *resourceMetadataServer) {
		s.asResource = "https://other-mcp.example/mcp"
	})

	metadata, err := DiscoverOAuthMetadata(context.Background(), s.server.URL+"/mcp")
	require.NoError(t, err)
	require.Empty(t, metadata.Resource)
}

// TestFetchResourceMetadataMismatchErrorOmitsURLQueries pins that the mismatch
// error, which DiscoverOAuthMetadata logs at Warn, carries neither the server
// URL's query (MCP URLs can hold an API key there) nor the document's.
func TestFetchResourceMetadataMismatchErrorOmitsURLQueries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"resource":              "https://other-mcp.example/mcp?token=document-query-value",
			"authorization_servers": []string{"https://other-as.example"},
		})
	}))
	t.Cleanup(srv.Close)

	_, _, _, err := fetchResourceMetadata(context.Background(), srv.URL, "https://user:pass@mcp.example.com/mcp?api_key=server-query-secret")
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not match")
	require.Contains(t, err.Error(), "https://mcp.example.com/mcp", "the error should still name the server")
	require.NotContains(t, err.Error(), "server-query-secret")
	require.NotContains(t, err.Error(), "user:pass")
	require.NotContains(t, err.Error(), "document-query-value")
}

func TestResourceMatchesServerURL(t *testing.T) {
	const server = "https://mcp.example.com/api/mcp"
	cases := []struct {
		resource string
		want     bool
	}{
		{"https://mcp.example.com/api/mcp", true},
		{"https://mcp.example.com/api/mcp/", true},
		{"HTTPS://MCP.Example.com/api/mcp", true},
		{"https://mcp.example.com:443/api/mcp", true},
		{"https://mcp.example.com/api", true},
		{"https://mcp.example.com", true},
		{"https://mcp.example.com/", true},
		{"https://mcp.example.com/api/mcpx", false},
		{"https://mcp.example.com/ap", false},
		{"https://mcp.example.com/api/mcp/tools", false},
		{"https://other.example.com/api/mcp", false},
		{"https://mcp.example.com.other.test/api/mcp", false},
		{"http://mcp.example.com/api/mcp", false},
		{"https://mcp.example.com:8443/api/mcp", false},
		{"https://user@mcp.example.com/api/mcp", false},
		{"https://mcp.example.com/api/mcp#frag", false},
		{"mcp.example.com/api/mcp", false},
		{"", false},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, resourceMatchesServerURL(tc.resource, server), "resource %q", tc.resource)
	}
	require.True(t, resourceMatchesServerURL("http://localhost:80/mcp", "http://LOCALHOST/mcp"))

	// A resource carrying a query names that query's resource, so it must match
	// the server URL's query exactly; a resource without one still matches a
	// server URL that carries, say, an API key in its query.
	const queried = "https://mcp.example.com/mcp?tenant=a"
	for _, tc := range []struct {
		resource string
		want     bool
	}{
		{"https://mcp.example.com/mcp?tenant=a", true},
		{"https://mcp.example.com/mcp", true},
		{"https://mcp.example.com", true},
		{"https://mcp.example.com/mcp?tenant=b", false},
		{"https://mcp.example.com/mcp?tenant=a&x=1", false},
		{"https://mcp.example.com?tenant=b", false},
	} {
		require.Equal(t, tc.want, resourceMatchesServerURL(tc.resource, queried), "resource %q", tc.resource)
	}
	require.False(t, resourceMatchesServerURL("https://mcp.example.com/mcp?tenant=a", "https://mcp.example.com/mcp"),
		"a query-qualified resource does not identify the unqualified server")
}
