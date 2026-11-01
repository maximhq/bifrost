package utils

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/network/proxytest"
	"github.com/maximhq/bifrost/core/schemas"
)

// The configured proxy serves a public target locally, preserving the production
// SSRF checks. Raw responses exercise length, chunked and close-delimited framing.
func fetchSizeTestProxy(t *testing.T) *schemas.ProxyConfig {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "expected CONNECT", 400)
			return
		}
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		if _, err := conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
			return
		}
		req, err := http.ReadRequest(bufio.NewReader(rw))
		if err != nil {
			return
		}
		parts := strings.Split(strings.Trim(req.URL.Path, "/"), "/")
		if len(parts) != 2 {
			return
		}
		size, err := strconv.Atoi(parts[1])
		if err != nil {
			return
		}
		body := strings.Repeat("x", size)
		fmt.Fprint(conn, "HTTP/1.1 200 OK\r\nContent-Type: application/pdf\r\nConnection: close\r\n")
		switch parts[0] {
		case "length":
			fmt.Fprintf(conn, "Content-Length: %d\r\n\r\n%s", size, body)
		case "chunked":
			fmt.Fprintf(conn, "Transfer-Encoding: chunked\r\n\r\n%x\r\n%s\r\n0\r\n\r\n", size, body)
		case "close":
			fmt.Fprintf(conn, "\r\n%s", body)
		}
	}))
	t.Cleanup(server.Close)
	return &schemas.ProxyConfig{Type: schemas.HTTPProxy, URL: schemas.NewSecretVar(server.URL)}
}

func TestFetchAndEncodeURL_ConfigurableLimit(t *testing.T) {
	proxy := fetchSizeTestProxy(t)
	for _, mode := range []string{"length", "chunked", "close"} {
		for _, size := range []int{15, 16, 17, 18} {
			t.Run(fmt.Sprintf("%s/%d", mode, size), func(t *testing.T) {
				ctx := context.WithValue(t.Context(), schemas.BifrostContextKeyProviderProxyConfig, proxy)
				ctx = context.WithValue(ctx, schemas.BifrostContextKeyProviderNetworkConfig, &schemas.NetworkConfig{MaxRemoteFileSizeBytes: 16})
				mediaType, encoded, err := FetchAndEncodeURL(ctx, fmt.Sprintf("http://203.0.113.10/%s/%d?sig=secret", mode, size))
				if size > 16 {
					if err == nil || !strings.Contains(err.Error(), "exceeds 16-byte limit") {
						t.Fatalf("expected size rejection, got %v", err)
					}
					if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "sig=") {
						t.Fatal("error leaked the signed URL")
					}
					if encoded != "" {
						t.Fatal("oversized resource returned data")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if mediaType != "application/pdf" || encoded != base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", size))) {
					t.Fatal("download content changed")
				}
			})
		}
	}

	// A real body above the historical cap verifies both the HTTP client's bound
	// and the final reader check, not just which limit appears in an error.
	for _, limit := range []int64{0, 64 << 20} {
		ctx := context.WithValue(t.Context(), schemas.BifrostContextKeyProviderProxyConfig, proxy)
		ctx = context.WithValue(ctx, schemas.BifrostContextKeyProviderNetworkConfig, &schemas.NetworkConfig{MaxRemoteFileSizeBytes: limit})
		size := int(schemas.DefaultMaxRemoteFileSizeBytes + 1)
		_, encoded, err := FetchAndEncodeURL(ctx, fmt.Sprintf("http://203.0.113.10/length/%d", size))
		if limit == 0 {
			if err == nil || !strings.Contains(err.Error(), "exceeds 26214400-byte limit") {
				t.Fatalf("default cap: %v", err)
			}
		} else {
			if err != nil {
				t.Fatal(err)
			}
			if len(encoded) != base64.StdEncoding.EncodedLen(size) {
				t.Fatal("large resource was truncated")
			}
		}
	}
}

func TestFetchClientFor_LimitIsolation(t *testing.T) {
	proxy := &schemas.ProxyConfig{Type: schemas.HTTPProxy, URL: schemas.NewSecretVar("http://127.0.0.1:3128")}
	for _, pc := range []*schemas.ProxyConfig{nil, proxy} {
		ctx := context.WithValue(t.Context(), schemas.BifrostContextKeyProviderProxyConfig, pc)
		var previous *http.Client
		for _, limit := range []int64{0, 64 << 20, 8} {
			ctx := context.WithValue(ctx, schemas.BifrostContextKeyProviderNetworkConfig, &schemas.NetworkConfig{MaxRemoteFileSizeBytes: limit})
			client, effective, err := fetchClientFor(ctx)
			if err != nil {
				t.Fatal(err)
			}
			want := limit
			if want == 0 {
				want = schemas.DefaultMaxRemoteFileSizeBytes
			}
			if effective != want || client.Transport.(*fasthttpRoundTripper).client.MaxResponseBodySize != int(want)+1 {
				t.Fatal("client and reader limits disagree")
			}
			if previous == client {
				t.Fatal("different limits shared a client")
			}
			previous = client
			var wg sync.WaitGroup
			for range 16 {
				wg.Go(func() {
					got, n, err := fetchClientFor(ctx)
					if err != nil || got != client || n != want {
						t.Error("concurrent fetch used another limit")
					}
				})
			}
			wg.Wait()
		}
	}
	for _, limit := range []int64{-1, schemas.MaxRemoteFileSizeBytesUpperBound + 1} {
		ctx := context.WithValue(t.Context(), schemas.BifrostContextKeyProviderNetworkConfig, &schemas.NetworkConfig{MaxRemoteFileSizeBytes: limit})
		if client, _, err := fetchClientFor(ctx); err == nil || client != nil {
			t.Fatal("invalid limit must not create a client")
		}
	}
}

func TestFetchAndEncodeURL_ConfiguredLimitKeepsProtections(t *testing.T) {
	ctx := context.WithValue(t.Context(), schemas.BifrostContextKeyProviderNetworkConfig, &schemas.NetworkConfig{MaxRemoteFileSizeBytes: 64 << 20})
	for _, resource := range []string{"http://127.0.0.1/private", "http://169.254.169.254/latest/meta-data/"} {
		if _, _, err := FetchAndEncodeURL(ctx, resource); err == nil || !strings.Contains(err.Error(), "non-public address") {
			t.Fatalf("configured cap bypassed SSRF protection for %s: %v", resource, err)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := FetchAndEncodeURL(canceled, "http://203.0.113.10/document.pdf"); !errors.Is(err, context.Canceled) {
		t.Fatalf("configured cap ignored cancellation: %v", err)
	}
}

func BenchmarkFetchClientFor(b *testing.B) {
	for _, name := range []string{"direct", "proxy"} {
		b.Run(name, func(b *testing.B) {
			var ctx context.Context = context.Background()
			if name == "proxy" {
				ctx = context.WithValue(ctx, schemas.BifrostContextKeyProviderProxyConfig, &schemas.ProxyConfig{
					Type: schemas.HTTPProxy, URL: schemas.NewSecretVar("http://127.0.0.1:3128"),
				})
			}
			if _, _, err := fetchClientFor(ctx); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, _, err := fetchClientFor(ctx); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// TestRedactURLForError pins that nothing a caller could authenticate with survives into
// an error string. AWS documents pre-signed URLs as bearer tokens valid for up to 7 days
// (docs.aws.amazon.com/AmazonS3/latest/userguide/using-presigned-url.html), so the query
// half is the credential, not a detail.
func TestRedactURLForError(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		secrets []string
	}{
		{
			name:    "s3 sigv4 pre-signed url",
			input:   "https://amzn-s3-demo-bucket.s3.us-east-1.amazonaws.com/reports/q3.pdf?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=AKIAIOSFODNN7EXAMPLE%2F20260812%2Fus-east-1%2Fs3%2Faws4_request&X-Amz-Signature=deadbeefcafe",
			want:    "https://amzn-s3-demo-bucket.s3.us-east-1.amazonaws.com/reports/q3.pdf",
			secrets: []string{"X-Amz-Signature", "deadbeefcafe", "AKIAIOSFODNN7EXAMPLE"},
		},
		{
			name:    "azure sas token",
			input:   "https://acct.blob.core.windows.net/c/doc.pdf?sv=2022-11-02&sig=Zm9vYmFyc2ln&se=2026-08-13T00%3A00%3A00Z",
			want:    "https://acct.blob.core.windows.net/c/doc.pdf",
			secrets: []string{"sig=", "Zm9vYmFyc2ln"},
		},
		{
			name:    "userinfo credentials",
			input:   "https://alice:hunter2@files.example.com/private/doc.pdf",
			want:    "https://files.example.com/private/doc.pdf",
			secrets: []string{"hunter2", "alice"},
		},
		{
			name:    "fragment is dropped",
			input:   "https://files.example.com/doc.pdf#token=abc123",
			want:    "https://files.example.com/doc.pdf",
			secrets: []string{"abc123"},
		},
		{
			name:    "plain url is unchanged",
			input:   "https://files.example.com/doc.pdf",
			want:    "https://files.example.com/doc.pdf",
			secrets: nil,
		},
		{
			// No host means nothing safe is identifiable, so nothing is echoed. Better a
			// useless-but-safe placeholder than a guess at which half was the secret.
			name:    "unparseable input yields a placeholder",
			input:   "://not a url?sig=leaked",
			want:    "[redacted url]",
			secrets: []string{"leaked"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RedactURLForError(tt.input)
			if got != tt.want {
				t.Errorf("RedactURLForError(%q) = %q, want %q", tt.input, got, tt.want)
			}
			for _, secret := range tt.secrets {
				if strings.Contains(got, secret) {
					t.Errorf("redacted URL still contains %q: %s", secret, got)
				}
			}
		})
	}
}

// TestSanitizeFetchError covers the half a clean format string cannot reach. net/http
// wraps transport failures in *url.Error, whose Error() prints the request URL verbatim
// apart from the password, so wrapping the cause with %w re-leaks the signed query even
// when the caller redacted its own copy of the URL.
func TestSanitizeFetchError(t *testing.T) {
	signed := "https://amzn-s3-demo-bucket.s3.amazonaws.com/q3.pdf?X-Amz-Signature=deadbeefcafe"
	redacted := RedactURLForError(signed)

	t.Run("rewrites the URL inside a *url.Error", func(t *testing.T) {
		cause := errors.New("dial tcp 203.0.113.10:443: i/o timeout")
		sanitized := sanitizeFetchError(&url.Error{Op: "Get", URL: signed, Err: cause}, redacted)

		msg := sanitized.Error()
		if strings.Contains(msg, "deadbeefcafe") || strings.Contains(msg, "X-Amz-Signature") {
			t.Errorf("sanitized error still leaks the signature: %s", msg)
		}
		if !strings.Contains(msg, "i/o timeout") {
			t.Errorf("expected the underlying cause to survive for diagnostics, got %s", msg)
		}
		if !errors.Is(sanitized, cause) {
			t.Error("expected errors.Is to still reach the original cause")
		}
	})

	t.Run("passes through a non-url error unchanged", func(t *testing.T) {
		cause := errors.New("unexpected EOF")
		if got := sanitizeFetchError(cause, redacted); got != cause {
			t.Errorf("expected the original error to be returned, got %v", got)
		}
	})
}

// TestFetchAndEncodeURL_ErrorsAreRedacted covers the paths reachable without a dial.
// FetchAndEncodeURL routes through network.SSRFSafeDialContext, which rejects loopback
// unconditionally and has no test seam, so an httptest server is unreachable by design
// (same constraint documented in core/providers/openai/chatfileurl_test.go). The scheme
// and parse guards run before any dial, and the dial rejection itself is reachable.
func TestFetchAndEncodeURL_ErrorsAreRedacted(t *testing.T) {
	secrets := []string{"X-Amz-Signature", "deadbeefcafe", "hunter2"}

	tests := []struct {
		name string
		url  string
	}{
		{
			name: "unsupported scheme",
			url:  "ftp://alice:hunter2@files.example.com/doc.pdf?X-Amz-Signature=deadbeefcafe",
		},
		{
			name: "blocked by the SSRF dialer",
			url:  "https://alice:hunter2@127.0.0.1/doc.pdf?X-Amz-Signature=deadbeefcafe",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := FetchAndEncodeURL(t.Context(), tt.url)
			if err == nil {
				t.Fatal("expected an error")
			}
			for _, secret := range secrets {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("error leaks %q: %s", secret, err.Error())
				}
			}
		})
	}
}

// TestFetchAndEncodeURL_UsesProviderProxy pins that a URL fetch made on a provider's behalf
// leaves through that provider's proxy_config, the same way its inference traffic does. A
// deployment whose only egress is a proxy otherwise times out on every image or document
// URL. The proxy here is a plain-HTTP forward proxy on loopback: an operator-configured
// proxy may sit on a private or loopback address, so the SSRF gate must let Bifrost dial it
// while still judging the target host.
func TestFetchAndEncodeURL_UsesProviderProxy(t *testing.T) {
	var hits atomic.Int32
	var secretHits atomic.Int32
	proxy := proxytest.ForwardProxy(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host == "10.0.0.5" {
			secretHits.Add(1)
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Host != "203.0.113.10" {
			http.Error(w, "unexpected target "+r.URL.Host, http.StatusBadGateway)
			return
		}
		hits.Add(1)
		switch r.URL.Path {
		case "/img.png":
			w.Header().Set("Content-Type", "image/png; charset=binary")
			_, _ = w.Write([]byte("PNGDATA"))
		case "/redirect":
			http.Redirect(w, r, "http://10.0.0.5/secret", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	})

	proxyCtx := func(t *testing.T) context.Context {
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		t.Cleanup(cancel)
		return context.WithValue(ctx, schemas.BifrostContextKeyProviderProxyConfig, &schemas.ProxyConfig{
			Type: schemas.HTTPProxy,
			URL:  schemas.NewSecretVar(proxy.URL),
		})
	}

	t.Run("public target goes through the proxy", func(t *testing.T) {
		hits.Store(0)
		mediaType, encoded, err := FetchAndEncodeURL(proxyCtx(t), "http://203.0.113.10/img.png")
		if err != nil {
			t.Fatalf("fetch through proxy failed: %v", err)
		}
		if hits.Load() != 1 {
			t.Fatalf("proxy saw %d requests, want 1", hits.Load())
		}
		if mediaType != "image/png" {
			t.Errorf("mediaType = %q, want image/png", mediaType)
		}
		if want := base64.StdEncoding.EncodeToString([]byte("PNGDATA")); encoded != want {
			t.Errorf("encoded = %q, want %q", encoded, want)
		}
	})

	t.Run("private target is refused before reaching the proxy", func(t *testing.T) {
		secretHits.Store(0)
		_, _, err := FetchAndEncodeURL(proxyCtx(t), "http://10.0.0.5/secret")
		if err == nil || !strings.Contains(err.Error(), "non-public address") {
			t.Fatalf("expected a non-public address error, got %v", err)
		}
		if secretHits.Load() != 0 {
			t.Fatalf("private target reached the proxy %d times", secretHits.Load())
		}
	})

	t.Run("redirect to a private target is refused", func(t *testing.T) {
		secretHits.Store(0)
		_, _, err := FetchAndEncodeURL(proxyCtx(t), "http://203.0.113.10/redirect")
		if err == nil || !strings.Contains(err.Error(), "non-public address") {
			t.Fatalf("expected a non-public address error, got %v", err)
		}
		if secretHits.Load() != 0 {
			t.Fatalf("redirected private target reached the proxy %d times", secretHits.Load())
		}
	})

	t.Run("no proxy in context keeps the direct SSRF dialer", func(t *testing.T) {
		_, _, err := FetchAndEncodeURL(t.Context(), proxy.URL+"/img.png")
		if err == nil || !strings.Contains(err.Error(), "non-public address") {
			t.Fatalf("expected the loopback proxy address itself to be refused as a direct target, got %v", err)
		}
	})
}

// TestFetchClientFor_OneClientPerResolvedProxy pins how fetch clients are cached: one
// client per distinct resolved proxy (so connections pool across fetches), a different
// client whenever any value that changes the route differs, the password included, and
// one shared direct client for every config that means "no proxy".
func TestFetchClientFor_OneClientPerResolvedProxy(t *testing.T) {
	clientFor := func(pc *schemas.ProxyConfig) *http.Client {
		t.Helper()
		ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyProviderProxyConfig, pc)
		client, _, err := fetchClientFor(ctx)
		if err != nil {
			t.Fatalf("fetchClientFor: %v", err)
		}
		return client
	}
	proxy := func(password string) *schemas.ProxyConfig {
		return &schemas.ProxyConfig{
			Type:     schemas.HTTPProxy,
			URL:      schemas.NewSecretVar("http://127.0.0.1:3128"),
			Username: schemas.NewSecretVar("svc"),
			Password: schemas.NewSecretVar(password),
		}
	}

	reused := clientFor(proxy("one"))
	if clientFor(proxy("one")) != reused {
		t.Error("the same resolved proxy must reuse one client")
	}
	if clientFor(proxy("one")) == clientFor(proxy("two")) {
		t.Error("a different proxy password must get its own client")
	}
	direct := clientFor(nil)
	for name, pc := range map[string]*schemas.ProxyConfig{
		"none":       {Type: schemas.NoProxy},
		"empty type": {},
	} {
		if clientFor(pc) != direct {
			t.Errorf("%s must share the direct client", name)
		}
	}
	if clientFor(proxy("one")) == direct {
		t.Error("a proxied config must not get the direct client")
	}
	// An inherited global skip_tls_verify changes the client's TLS, so toggling it
	// must not keep serving the client built with the old setting.
	skipping := proxy("one")
	skipping.SkipTLSVerify = true
	if clientFor(skipping) == clientFor(proxy("one")) {
		t.Error("a proxy config that skips TLS verification must get its own client")
	}

	env := &schemas.ProxyConfig{Type: schemas.EnvProxy}
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:3128")
	first := clientFor(env)
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:3129")
	if clientFor(env) == first {
		t.Error("type environment must get a new client when the proxy variables change")
	}
}

// TestFetchAndEncodeURL_ProxiedHostnameTargetNeedsLocalDNS pins a deliberate limit: the
// SSRF check resolves the target on the Bifrost host even when the fetch is proxied. A
// hostname only the proxy can resolve is refused, never sent unchecked, because a name
// that resolves only at a corporate proxy can just as well name an intranet host. The
// target is under .invalid, which RFC 6761 guarantees never resolves, so the local lookup
// fails on every resolver (unlike .test, which some local resolvers answer).
func TestFetchAndEncodeURL_ProxiedHostnameTargetNeedsLocalDNS(t *testing.T) {
	var hits atomic.Int32
	proxy := proxytest.ForwardProxy(t, func(w http.ResponseWriter, r *http.Request) { hits.Add(1) })
	ctx := context.WithValue(t.Context(), schemas.BifrostContextKeyProviderProxyConfig, &schemas.ProxyConfig{
		Type: schemas.HTTPProxy,
		URL:  schemas.NewSecretVar(proxy.URL),
	})

	_, _, err := FetchAndEncodeURL(ctx, "http://only-the-proxy-resolves.invalid/img.png")
	if err == nil || !strings.Contains(err.Error(), "DNS lookup failed") {
		t.Fatalf("expected a local DNS failure, got %v", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("an unchecked target reached the proxy %d times", hits.Load())
	}
}

// TestFetchAndEncodeURL_ProxiedTunnelIsBoundToTheCheckedAddress pins that a proxied
// fetch opens its tunnel to the address the SSRF check approved, not to the hostname.
// The proxy would otherwise resolve the name itself, so a DNS answer that changed after
// the check could send the fetch to an internal address.
func TestFetchAndEncodeURL_ProxiedTunnelIsBoundToTheCheckedAddress(t *testing.T) {
	prev := fetchResolveTarget
	fetchResolveTarget = func(_ context.Context, host string) ([]net.IP, error) {
		if host == "pinned.example" {
			return []net.IP{net.ParseIP("203.0.113.10")}, nil
		}
		return nil, fmt.Errorf("DNS lookup failed for %s", host)
	}
	t.Cleanup(func() { fetchResolveTarget = prev })

	var targets []string
	var mu sync.Mutex
	proxy := proxytest.ForwardProxy(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		targets = append(targets, r.URL.Host)
		mu.Unlock()
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("PNGDATA"))
	})
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, schemas.BifrostContextKeyProviderProxyConfig, &schemas.ProxyConfig{
		Type: schemas.HTTPProxy,
		URL:  schemas.NewSecretVar(proxy.URL),
	})

	if _, _, err := FetchAndEncodeURL(ctx, "http://pinned.example/img.png"); err != nil {
		t.Fatalf("fetch through proxy failed: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(targets) != 1 || targets[0] != "203.0.113.10" {
		t.Fatalf("proxy was asked for %v, want exactly the checked address [203.0.113.10]", targets)
	}
}
