package utils

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/network"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// RedactURLForError reduces a resource URL to the part that is safe to echo back in an
// error: scheme, host, and path. Userinfo, query, and fragment are dropped.
//
// A pre-signed URL carries its credential in the query -- AWS SigV4 puts it in
// X-Amz-Signature, Azure in the SAS `sig` parameter -- and AWS documents pre-signed URLs
// as bearer tokens that "grant access to those who possess them", valid for up to 7 days
// (docs.aws.amazon.com/AmazonS3/latest/userguide/using-presigned-url.html). These errors
// reach the client as a BifrostError and land in logs, so echoing the query hands out
// working access to whoever reads either one.
//
// url.URL.Redacted() is deliberately not used: it masks the password and keeps the query,
// which is the half that actually carries the credential.
func RedactURLForError(resourceURL string) string {
	parsed, err := url.Parse(resourceURL)
	if err != nil || parsed.Host == "" {
		// Nothing safe is identifiable, so nothing is echoed. A useless-but-safe
		// placeholder beats guessing which half of an unparseable string was the secret.
		return "[redacted url]"
	}
	return (&url.URL{Scheme: parsed.Scheme, Host: parsed.Host, Path: parsed.Path}).String()
}

// sanitizeFetchError strips the URL out of the cause as well as the message. net/http
// wraps transport failures in *url.Error, whose Error() prints the request URL verbatim
// apart from the password (net/http's own stripPassword), so a pre-signed signature
// survives into the message even when the caller's format string is already clean.
//
// The Op and the underlying cause are kept: "dial tcp ...: i/o timeout" is where the
// diagnostic value lives, and it names a host at most.
func sanitizeFetchError(err error, redacted string) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return &url.Error{Op: urlErr.Op, URL: redacted, Err: urlErr.Err}
	}
	return err
}

// FetchAndEncodeURL downloads a remote resource (image, document, etc.) and
// returns its base64 encoding plus the response Content-Type. Used by providers
// (Bedrock Converse, Anthropic-on-Vertex) whose upstream surface only accepts
// inline bytes, not remote URLs. Bounded by a 20s timeout and a 25 MiB body cap;
// non-2xx responses error. The provided ctx is honored for cancellation and
// deadlines; pass context.Background() if no request context is available.
//
// SSRF-hardened: only http/https schemes are accepted, and the dialer rejects
// connections to loopback, private, CGNAT, link-local, unique-local, site-local,
// and unspecified addresses (including IPv4 targets smuggled inside IPv6
// transition addresses). The IP check runs at dial time (not just lookup time)
// so DNS rebinding does not bypass it. Redirect targets are subject to the same
// scheme + dial-time IP validation.
//
// Proxy: when ctx carries the serving provider's proxy config
// (schemas.BifrostContextKeyProviderProxyConfig, set by bifrost on every attempt),
// the fetch leaves through that proxy, the same egress as the provider's inference
// traffic. The target host is still refused if it resolves to a non-public address;
// see newFetchClient and network.NewTargetCheckingTransport for how the check works
// once the dial goes to a proxy. With no proxy configured the fetch connects
// directly, as before.
func FetchAndEncodeURL(ctx context.Context, resourceURL string) (mediaType string, encoded string, err error) {
	const maxBytes = maxFetchBytes

	// Every error below names the resource by its redacted form only; see
	// RedactURLForError for why the query and userinfo never make it into a message.
	redacted := RedactURLForError(resourceURL)

	parsed, err := url.Parse(resourceURL)
	if err != nil {
		return "", "", fmt.Errorf("invalid resource URL %q: %w", redacted, sanitizeFetchError(err, redacted))
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", "", fmt.Errorf("unsupported URL scheme %q (only http/https allowed)", parsed.Scheme)
	}

	client, err := fetchClientFor(ctx)
	if err != nil {
		return "", "", fmt.Errorf("failed to fetch from %q: %w", redacted, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, resourceURL, nil)
	if err != nil {
		return "", "", fmt.Errorf("invalid resource URL %q: %w", redacted, sanitizeFetchError(err, redacted))
	}
	req.Header.Set("User-Agent", "bifrost-fetch/1")

	resp, err := DoHTTPRequest(client, req)
	if err != nil {
		if errors.Is(err, fasthttp.ErrBodyTooLarge) {
			return "", "", fmt.Errorf("resource at %q exceeds %d-byte limit", redacted, maxBytes)
		}
		return "", "", fmt.Errorf("failed to fetch from %q: %w", redacted, sanitizeFetchError(err, redacted))
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("fetch %q returned non-2xx status %d", redacted, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return "", "", fmt.Errorf("failed to read body from %q: %w", redacted, sanitizeFetchError(err, redacted))
	}
	if int64(len(body)) > maxBytes {
		return "", "", fmt.Errorf("resource at %q exceeds %d-byte limit", redacted, maxBytes)
	}

	mediaType = resp.Header.Get("Content-Type")
	if i := strings.Index(mediaType, ";"); i != -1 {
		mediaType = strings.TrimSpace(mediaType[:i])
	}

	return mediaType, base64.StdEncoding.EncodeToString(body), nil
}

// maxFetchBytes caps a fetched resource at 25 MiB.
const maxFetchBytes int64 = 25 * 1024 * 1024

// proxyEnvVars are the variables golang.org/x/net/http/httpproxy reads.
var proxyEnvVars = []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy", "REQUEST_METHOD"}

// fetchClients holds one http.Client per distinct proxy, so connections are pooled
// across fetches instead of a fresh transport being built per call. Keyed by
// fetchClientKeyFor; the set is bounded by the number of distinct proxy configs.
var fetchClients sync.Map // fetchClientKey -> *http.Client

// fetchClientFor returns the client for the proxy config carried on ctx, or the
// direct client when there is none.
func fetchClientFor(ctx context.Context) (*http.Client, error) {
	proxyConfig, _ := ctx.Value(schemas.BifrostContextKeyProviderProxyConfig).(*schemas.ProxyConfig)
	key := fetchClientKeyFor(proxyConfig)
	if cached, ok := fetchClients.Load(key); ok {
		return cached.(*http.Client), nil
	}
	actual, _ := fetchClients.LoadOrStore(key, newFetchClient(proxyConfig))
	return actual.(*http.Client), nil
}

// newFetchClient builds the SSRF-hardened fetch client on fasthttp, with the proxy
// from the same ConfigureProxy the provider's inference clients use.
//
// Direct connections, including targets the proxy is told to skip (errBypassProxy),
// go through network.SSRFSafeDialContext, which checks the resolved target on every
// dial. Once a request is proxied the dial goes to the proxy, which is operator
// configuration and may well be on a private address, so it is not checked; the
// target is judged instead by network.NewTargetCheckingTransport before every hop.
// See NewTargetCheckingTransport for the DNS caveat that leaves.
func newFetchClient(proxyConfig *schemas.ProxyConfig) *http.Client {
	ssrfDial := network.SSRFSafeDialContext(10 * time.Second)
	direct := func(addr string) (net.Conn, error) {
		return ssrfDial(context.Background(), "tcp", addr)
	}
	client := &fasthttp.Client{
		ReadTimeout:              20 * time.Second,
		WriteTimeout:             20 * time.Second,
		MaxIdleConnDuration:      30 * time.Second,
		MaxResponseBodySize:      int(maxFetchBytes) + 1,
		NoDefaultUserAgentHeader: true,
	}
	proxied := false
	if namesProxy(proxyConfig) {
		client = ConfigureProxy(client, proxyConfig, getLogger())
		if proxyDial := client.Dial; proxyDial != nil {
			proxied = true
			client.Dial = func(addr string) (net.Conn, error) {
				conn, err := proxyDial(addr)
				if errors.Is(err, errBypassProxy) {
					return direct(addr)
				}
				return conn, err
			}
		}
	}
	if !proxied {
		client.Dial = direct
	}
	client.Transport = NewContextTransport()

	var transport http.RoundTripper = &fasthttpRoundTripper{client: client}
	if proxied {
		transport = network.NewTargetCheckingTransport(transport)
	}
	return &http.Client{
		Timeout:   20 * time.Second,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("blocked redirect to unsupported scheme %q", req.URL.Scheme)
			}
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			return nil
		},
	}
}

// fetchClientKey is the set of resolved values that change which client a fetch needs.
// It is a comparable struct used directly as the cache key: the values already live in
// memory on the provider's ProxyConfig, so keying by them exposes nothing new, and no
// hashing is needed. Everything that NetHTTPProxy treats as "no proxy" maps to the
// zero key.
type fetchClientKey struct {
	proxyType schemas.ProxyType
	url       string
	username  string
	password  string
	caCertPEM string
	noProxy   string
	// env holds the proxy variables for type "environment": EnvProxyFunc reads them
	// when the client is built, so the values it read are part of which client this is.
	env string
}

func fetchClientKeyFor(proxyConfig *schemas.ProxyConfig) fetchClientKey {
	if proxyConfig == nil || proxyConfig.Type == "" || proxyConfig.Type == schemas.NoProxy {
		return fetchClientKey{}
	}
	key := fetchClientKey{
		proxyType: proxyConfig.Type,
		url:       proxyConfig.URL.GetValue(),
		username:  proxyConfig.Username.GetValue(),
		password:  proxyConfig.Password.GetValue(),
		caCertPEM: proxyConfig.CACertPEM.GetValue(),
		noProxy:   proxyConfig.NoProxy,
	}
	if proxyConfig.Type == schemas.EnvProxy {
		var env strings.Builder
		for _, name := range proxyEnvVars {
			env.WriteString(os.Getenv(name))
			env.WriteByte(0)
		}
		key.env = env.String()
	}
	return key
}
