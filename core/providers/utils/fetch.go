package utils

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/network"
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
func FetchAndEncodeURL(ctx context.Context, resourceURL string) (mediaType string, encoded string, err error) {
	const maxBytes int64 = 25 * 1024 * 1024

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

	transport := &http.Transport{
		DialContext: network.SSRFSafeDialContext(10 * time.Second),
	}
	client := &http.Client{
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

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, resourceURL, nil)
	if err != nil {
		return "", "", fmt.Errorf("invalid resource URL %q: %w", redacted, sanitizeFetchError(err, redacted))
	}
	req.Header.Set("User-Agent", "bifrost-fetch/1")

	resp, err := DoHTTPRequest(client, req)
	if err != nil {
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
