package utils

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// FetchAndEncodeURL downloads a remote resource (image, document, etc.) and
// returns its base64 encoding plus the response Content-Type. Used by providers
// (Bedrock Converse, Anthropic-on-Vertex) whose upstream surface only accepts
// inline bytes, not remote URLs. Bounded by a 20s timeout and a 25 MiB body cap;
// non-2xx responses error. The provided ctx is honored for cancellation and
// deadlines; pass context.Background() if no request context is available.
//
// SSRF-hardened: only http/https schemes are accepted, and the dialer rejects
// connections to loopback, private, link-local, unique-local, and unspecified
// addresses. The IP check runs at dial time (not just lookup time) so DNS
// rebinding does not bypass it. Redirect targets are subject to the same
// scheme + dial-time IP validation.
func FetchAndEncodeURL(ctx context.Context, resourceURL string) (mediaType string, encoded string, err error) {
	const maxBytes int64 = 25 * 1024 * 1024

	parsed, err := url.Parse(resourceURL)
	if err != nil {
		return "", "", fmt.Errorf("invalid resource URL %q: %w", resourceURL, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", "", fmt.Errorf("unsupported URL scheme %q (only http/https allowed)", parsed.Scheme)
	}

	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, splitErr := net.SplitHostPort(addr)
			if splitErr != nil {
				return nil, splitErr
			}
			ips, lookupErr := (&net.Resolver{}).LookupIP(ctx, "ip", host)
			if lookupErr != nil {
				return nil, lookupErr
			}
			for _, ip := range ips {
				if !isPublicIP(ip) {
					return nil, fmt.Errorf("blocked fetch to non-public address %s", ip.String())
				}
			}
			// Dial the first validated IP directly to close the DNS-rebinding TOCTOU.
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
		},
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
		return "", "", fmt.Errorf("invalid resource URL %q: %w", resourceURL, err)
	}
	req.Header.Set("User-Agent", "bifrost-fetch/1")

	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("failed to fetch from %q: %w", resourceURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("fetch %q returned non-2xx status %d", resourceURL, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return "", "", fmt.Errorf("failed to read body from %q: %w", resourceURL, err)
	}
	if int64(len(body)) > maxBytes {
		return "", "", fmt.Errorf("resource at %q exceeds %d-byte limit", resourceURL, maxBytes)
	}

	mediaType = resp.Header.Get("Content-Type")
	if i := strings.Index(mediaType, ";"); i != -1 {
		mediaType = strings.TrimSpace(mediaType[:i])
	}

	return mediaType, base64.StdEncoding.EncodeToString(body), nil
}

// isPublicIP reports whether the given IP address is safe to fetch from
// (i.e. not loopback, private, link-local, unique-local, or unspecified).
func isPublicIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
		addr.IsMulticast() || addr.IsUnspecified() || addr.IsInterfaceLocalMulticast() {
		return false
	}
	return true
}
