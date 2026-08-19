package network

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
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
	return ssrfSafeDialContext(net.DefaultResolver, dialer.DialContext, nil)
}

// SSRFSafeDialContextWithAllowlist is the allowlist-aware variant of
// SSRFSafeDialContext, for callers that must reach specific
// operator-declared private hosts (e.g. downloading custom plugin binaries
// from an internal artifact server). allow may be nil, in which case
// behavior is identical to SSRFSafeDialContext.
//
// Every resolved IP must satisfy IsPublicIP OR allow.Permits(host, ip); the
// winning IP is dialed directly and this check re-runs on every dial
// (redirects, pooled-connection re-dials), preserving
// SSRFSafeDialContext's DNS-rebinding protection.
//
// This is the only place allowlist behavior exists - do not add an
// allowlist parameter to SSRFSafeDialContext itself, and do not use this
// function at SSRF-guarded call sites other than plugin downloads (provider
// fetch, webhooks, skills serving); those must keep blocking all private
// targets unconditionally.
func SSRFSafeDialContextWithAllowlist(dialTimeout time.Duration, allow *Allowlist) func(ctx context.Context, netw, addr string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: dialTimeout}
	return ssrfSafeDialContext(net.DefaultResolver, dialer.DialContext, allow)
}

// ssrfSafeDialContext is the seam behind SSRFSafeDialContext and
// SSRFSafeDialContextWithAllowlist, with injectable resolver and dial for
// tests. allow may be nil.
func ssrfSafeDialContext(resolver ipLookuper, dial func(ctx context.Context, network, addr string) (net.Conn, error), allow *Allowlist) func(ctx context.Context, netw, addr string) (net.Conn, error) {
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
			if !IsPublicIP(ip) && !allow.Permits(host, ip) {
				return nil, fmt.Errorf("blocked connection to non-public address %s (host %s)", ip, host)
			}
		}
		return dial(ctx, netw, net.JoinHostPort(ips[0].String(), port))
	}
}

// Allowlist is a static, deploy-time-validated set of hosts/CIDRs permitted
// to bypass IsPublicIP's private/loopback/CGNAT/link-local block, for
// callers that explicitly opt in via SSRFSafeDialContextWithAllowlist. It
// never affects SSRFSafeDialContext or its other callers.
type Allowlist struct {
	hostnames map[string]struct{}
	prefixes  []netip.Prefix
}

// NewAllowlist parses entries (each a bare hostname, bare IP, or CIDR) into
// an Allowlist. It fails on the first entry that is none of the three: this
// is security-relaxing config, so a typo must fail loudly (e.g. abort
// server startup) rather than silently becoming a no-op. Blank entries are
// skipped. There is no wildcard/glob support - unlike CORS allowlists,
// wrongly matching here means Bifrost fetches-and-dlopen()s from an
// unintended host, so operators must enumerate hosts exactly.
func NewAllowlist(entries []string) (*Allowlist, error) {
	al := &Allowlist{hostnames: make(map[string]struct{})}
	for _, raw := range entries {
		e := strings.TrimSpace(raw)
		if e == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(e); err == nil {
			al.prefixes = append(al.prefixes, prefix)
			continue
		}
		if addr, err := netip.ParseAddr(e); err == nil {
			bits := 32
			if addr.Is6() {
				bits = 128
			}
			al.prefixes = append(al.prefixes, netip.PrefixFrom(addr, bits))
			continue
		}
		if looksLikeIPLiteral(e) {
			return nil, fmt.Errorf("invalid allowlist entry %q: looks like an IP address but failed to parse", raw)
		}
		if isValidHostnameLiteral(e) {
			al.hostnames[strings.ToLower(e)] = struct{}{}
			continue
		}
		return nil, fmt.Errorf("invalid allowlist entry %q: not a valid hostname, IP address, or CIDR (schemes and ports are not supported, host only)", raw)
	}
	return al, nil
}

// looksLikeIPLiteral reports whether s has the shape of an IPv4/IPv6 address
// (four dot-separated all-numeric labels, or contains a colon) even though it
// failed to parse as one. NewAllowlist uses this to reject likely IP typos
// (e.g. an out-of-range octet) with a clear error instead of silently
// accepting them as a dead hostname entry that can never match a dial host.
func looksLikeIPLiteral(s string) bool {
	if strings.Contains(s, ":") {
		return true
	}
	labels := strings.Split(s, ".")
	if len(labels) != 4 {
		return false
	}
	for _, label := range labels {
		if label == "" {
			return false
		}
		for _, r := range label {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

// Permits reports whether host or resolvedIP is covered by the allowlist:
// host matches an allowlisted hostname exactly (case-insensitive), or
// resolvedIP falls inside an allowlisted CIDR/bare-IP entry. The same
// IPv4-in-IPv6 transition unwrapping IsPublicIP applies is applied to
// resolvedIP, so a 6to4/NAT64-wrapped form of an allowlisted IPv4 also
// matches. Nil-safe: a nil *Allowlist permits nothing.
func (a *Allowlist) Permits(host string, resolvedIP net.IP) bool {
	if a == nil {
		return false
	}
	if _, ok := a.hostnames[strings.ToLower(host)]; ok {
		return true
	}
	addr, ok := netip.AddrFromSlice(resolvedIP)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	if embedded, ok := embeddedIPv4(addr); ok {
		addr = embedded
	}
	for _, prefix := range a.prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// isValidHostnameLiteral reports whether s is a syntactically valid DNS
// hostname (RFC 1123 labels), rejecting anything with a scheme, port, path,
// or invalid characters so a config typo fails NewAllowlist loudly instead
// of silently parsing as a hostname that will never match anything.
func isValidHostnameLiteral(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if len(label) == 0 || len(label) > 63 {
			return false
		}
		for i, r := range label {
			switch {
			case r == '-':
				if i == 0 || i == len(label)-1 {
					return false
				}
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			default:
				return false
			}
		}
	}
	return true
}
