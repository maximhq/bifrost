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
// connections to loopback, private, CGNAT, link-local, unique-local, site-local,
// and unspecified addresses (including IPv4 targets smuggled inside IPv6
// transition addresses). The IP check runs at dial time (not just lookup time)
// so DNS rebinding does not bypass it. Redirect targets are subject to the same
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

// cgnat is the RFC 6598 Carrier-Grade NAT shared address space (100.64.0.0/10).
// It is reserved for provider-internal networks and is used for pod IPs on some
// Kubernetes platforms (e.g. EKS), so it must not be reachable. netip's
// IsPrivate() does not cover it.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// isPublicIP reports whether ip is safe to fetch from: not loopback, private,
// CGNAT, link-local, unique-local, site-local, multicast, or unspecified.
// IPv6 forms that embed an IPv4 address (IPv4-mapped, 6to4, NAT64) are reduced
// to that IPv4 and re-checked, so an internal IPv4 such as the 169.254.169.254
// metadata endpoint cannot be reached by wrapping it in an IPv6 transition
// representation.
func isPublicIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()

	// If the address embeds an IPv4 via a transition mechanism (6to4 / NAT64),
	// classify the embedded IPv4 — otherwise an internal target like
	// 169.254.169.254 can be reached as 2002:a9fe:a9fe:: or 64:ff9b::a9fe:a9fe,
	// neither of which Unmap() collapses.
	if embedded, ok := embeddedIPv4(addr); ok {
		addr = embedded
	}

	if addr.IsLoopback() || addr.IsPrivate() || cgnat.Contains(addr) ||
		addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
		addr.IsMulticast() || addr.IsUnspecified() || addr.IsInterfaceLocalMulticast() ||
		addr == netip.AddrFrom4([4]byte{255, 255, 255, 255}) {
		return false
	}

	// Deprecated IPv6 site-local fec0::/10 (RFC 3879) isn't matched by any
	// helper above; reject it explicitly.
	if addr.Is6() {
		b := addr.As16()
		if b[0] == 0xfe && (b[1]&0xc0) == 0xc0 {
			return false
		}
	}

	return true
}

// embeddedIPv4 extracts the IPv4 address carried inside an IPv6 transition
// address: 6to4 (2002::/16) or NAT64 (RFC 6052 well-known 64:ff9b::/96 and
// RFC 8215 local-use 64:ff9b:1::/48). Returns false for anything else.
//
// Limitation: NAT64 with an operator-chosen Network-Specific Prefix (any other
// /32../96) is indistinguishable from a normal global address and is not
// unwrapped — only the two standard prefixes are covered.
func embeddedIPv4(addr netip.Addr) (netip.Addr, bool) {
	if !addr.Is6() {
		return netip.Addr{}, false
	}
	b := addr.As16()
	switch {
	case b[0] == 0x20 && b[1] == 0x02: // 6to4 2002::/16 -> bytes 2..5
		return netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}), true
	case b[0] == 0x00 && b[1] == 0x64 && b[2] == 0xff && b[3] == 0x9b:
		// NAT64 well-known 64:ff9b::/96 -> IPv4 in the low 32 bits (bytes 12..15).
		if b[4] == 0x00 && b[5] == 0x00 {
			return netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}), true
		}
		// NAT64 local-use 64:ff9b:1::/48 -> per RFC 6052 the IPv4 occupies
		// bytes 6,7,9,10 (byte 8 is the reserved u-octet, skipped).
		if b[4] == 0x00 && b[5] == 0x01 {
			return netip.AddrFrom4([4]byte{b[6], b[7], b[9], b[10]}), true
		}
	}
	return netip.Addr{}, false
}
