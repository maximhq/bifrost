package network

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"time"
)

// cgnat is the RFC 6598 Carrier-Grade NAT shared address space (100.64.0.0/10).
// It is reserved for provider-internal networks and is used for pod IPs on some
// Kubernetes platforms (e.g. EKS), so it must not be reachable. netip's
// IsPrivate() does not cover it.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// IsPublicIP reports whether ip is safe to dial from server-side code that
// fetches user-controlled URLs: not loopback, private, CGNAT, link-local,
// unique-local, site-local, multicast, broadcast, or unspecified. IPv6 forms
// that embed an IPv4 address (IPv4-mapped, 6to4, NAT64) are reduced to that
// IPv4 and re-checked, so an internal IPv4 such as the 169.254.169.254
// metadata endpoint cannot be reached by wrapping it in an IPv6 transition
// representation.
//
// This is stricter than the complement of IsPrivateIP: IsPrivateIP is a
// coarse range check for save-time URL validation (where private targets may
// be deliberately allowed), while IsPublicIP is the dial-time gate.
func IsPublicIP(ip net.IP) bool {
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

// ipLookuper resolves a hostname to IPs. *net.Resolver satisfies it; tests
// substitute a fake to exercise the dial path without real DNS.
type ipLookuper interface {
	LookupIP(ctx context.Context, network, host string) ([]net.IP, error)
}

// SSRFSafeDialContext returns a DialContext for outbound requests to
// user-controlled URLs. On every dial it resolves the host, rejects the
// connection if any resolved address fails IsPublicIP, and then dials the
// first validated IP directly — so a second DNS resolution cannot swap in a
// private address after validation (DNS rebinding TOCTOU). Because the check
// runs per dial, it also holds across redirects and connection-pool re-dials.
func SSRFSafeDialContext(dialTimeout time.Duration) func(ctx context.Context, netw, addr string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: dialTimeout}
	return ssrfSafeDialContext(net.DefaultResolver, dialer.DialContext)
}

// ssrfSafeDialContext is the seam behind SSRFSafeDialContext with injectable
// resolver and dial for tests.
func ssrfSafeDialContext(resolver ipLookuper, dial func(ctx context.Context, network, addr string) (net.Conn, error)) func(ctx context.Context, netw, addr string) (net.Conn, error) {
	return func(ctx context.Context, netw, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("invalid dial address %q: %w", addr, err)
		}
		ips, err := resolver.LookupIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("DNS lookup failed for %s: %w", host, err)
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("DNS lookup for %s returned no addresses", host)
		}
		for _, ip := range ips {
			if !IsPublicIP(ip) {
				return nil, fmt.Errorf("blocked connection to non-public address %s (host %s)", ip, host)
			}
		}
		return dial(ctx, netw, net.JoinHostPort(ips[0].String(), port))
	}
}
