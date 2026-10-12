package oauth2

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/network"
	"github.com/maximhq/bifrost/core/network/proxytest"
	"github.com/stretchr/testify/require"
)

// TestNewOAuthDiscoveryHTTPClientBlocksLoopback proves the OAuth discovery HTTP
// client is wired through the SSRF-safe dialer: a target the discovery chain
// picked up from a upstream MCP server's own response
// must not be reachable if it points at loopback or any other private/
// link-local/CGNAT address. TestMain overrides the dialer package-wide for
// this package's other tests (which talk to real httptest.Server loopback
// addresses) - restore the production dialer for the duration of this test so
// it exercises the actual guard.
func TestNewOAuthDiscoveryHTTPClientBlocksLoopback(t *testing.T) {
	prev := testDialContextOverride
	testDialContextOverride = nil
	t.Cleanup(func() { testDialContextOverride = prev })

	client := newOAuthDiscoveryHTTPClient(time.Second)
	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	require.NotNil(t, transport.DialContext)

	_, err := transport.DialContext(context.Background(), "tcp", "127.0.0.1:80")
	require.Error(t, err)
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

	client := newOAuthDiscoveryHTTPClient(2 * time.Second)
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

	transport := newOAuthDiscoveryTransport(oauthDialContext(2*time.Second, proxyURL))
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
