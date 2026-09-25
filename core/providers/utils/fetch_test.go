package utils

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

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
	proxy := newForwardProxy(t, func(w http.ResponseWriter, r *http.Request) {
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
		client, err := fetchClientFor(ctx)
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

	env := &schemas.ProxyConfig{Type: schemas.EnvProxy}
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:3128")
	first := clientFor(env)
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:3129")
	if clientFor(env) == first {
		t.Error("type environment must get a new client when the proxy variables change")
	}
}
