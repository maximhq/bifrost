package network

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// cgnat is the RFC 6598 Carrier-Grade NAT shared address space (100.64.0.0/10).
// It is reserved for provider-internal networks and is used for pod IPs on some
// Kubernetes platforms (e.g. EKS), so it must not be reachable. netip's
// IsPrivate() does not cover it.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// teredo is the RFC 4380 Teredo tunnelling prefix. Note the /32: only
// 2001:0000::/32 is Teredo, so ordinary global unicast under 2001::/16 (e.g.
// 2001:4860::/32) is untouched.
//
// Rejected wholesale rather than unwrapped-and-reclassified the way 6to4 and
// NAT64 are, for two reasons. Teredo carries two IPv4 addresses - the client's
// in bits 96-127 (obfuscated by XOR with 0xFFFFFFFF) and the relay server's in
// bits 32-63 - so reclassifying on one still leaves the other attacker-chosen.
// And unlike 6to4, Teredo has no legitimate server-to-server use: it is a
// consumer NAT-traversal mechanism, deprecated and off by default on modern
// systems, so nothing is lost by refusing the range outright.
var teredo = netip.MustParsePrefix("2001:0000::/32")

// metadataEndpoints are cloud instance-metadata service addresses that sit
// outside the link-local range, so the link-local block alone does not cover
// them. 169.254.169.254 (AWS, GCP, Azure, Oracle, DigitalOcean) is already
// refused as link-local. These two fall inside ranges the private-network
// policy otherwise permits: Alibaba Cloud ECS serves metadata from CGNAT
// space, and the AWS IMDS IPv6 endpoint is a unique-local address.
var metadataEndpoints = []netip.Addr{
	netip.MustParseAddr("100.100.100.200"), // Alibaba Cloud ECS
	netip.MustParseAddr("fd00:ec2::254"),   // AWS IMDS over IPv6
}

// IsMetadataEndpoint reports whether ip is one of the cloud instance-metadata
// addresses that sit outside the link-local range (see metadataEndpoints).
func IsMetadataEndpoint(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	for _, endpoint := range metadataEndpoints {
		if addr == endpoint {
			return true
		}
	}
	return false
}

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
		// Teredo. Checked after the transition unwrap above so a Teredo address
		// is judged as itself: none of the stdlib predicates match it, and its
		// embedded IPv4 is obfuscated, so without this an internal target
		// wrapped as 2001:0000:...:5601:5601 reads as ordinary global unicast.
		if teredo.Contains(addr) {
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

// IPLookuper resolves a hostname to IPs. *net.Resolver satisfies it; tests
// substitute a fake to exercise the dial path without real DNS.
type IPLookuper interface {
	LookupIP(ctx context.Context, network, host string) ([]net.IP, error)
}

type ipLookuper = IPLookuper

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

// NewTargetCheckingTransport wraps next so a request whose target host resolves to
// any non-public address is refused before it is sent. It is for fetching
// user-controlled URLs through a proxy: once the dial goes to the proxy rather than
// the target, SSRFSafeDialContext can no longer see the target, so the check moves
// here. http.Client calls RoundTrip once per redirect hop, so redirects are judged
// the same way as the original request.
//
// The proxy resolves the target again on its own, so a DNS answer that changes
// between this check and the proxy's lookup is not caught. Past that point the
// proxy's egress policy governs, as for any other traffic routed through it. The
// check also needs the target to resolve locally: on a host with no external DNS,
// proxied fetches are refused rather than sent unchecked.
func NewTargetCheckingTransport(next http.RoundTripper) http.RoundTripper {
	return &targetCheckingTransport{check: publicTargetCheck(net.DefaultResolver, nil), next: next}
}

// publicTargetCheck resolves host and refuses it unless every address is public or
// permitted by allow (nil permits none).
func publicTargetCheck(resolver ipLookuper, allow *Allowlist) func(ctx context.Context, host string) error {
	return func(ctx context.Context, host string) error {
		ips, err := resolver.LookupIP(ctx, "ip", host)
		if err != nil {
			return fmt.Errorf("DNS lookup failed for %s: %w", host, err)
		}
		if len(ips) == 0 {
			return fmt.Errorf("DNS lookup for %s returned no addresses", host)
		}
		for _, ip := range ips {
			if !IsPublicIP(ip) && !allow.Permits(host, ip) {
				return fmt.Errorf("blocked connection to non-public address %s (host %s)", ip, host)
			}
		}
		return nil
	}
}

// targetCheckingTransport judges the target host of every request with check before
// handing it to next.
type targetCheckingTransport struct {
	check func(ctx context.Context, host string) error
	next  http.RoundTripper
}

func (t *targetCheckingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.check(req.Context(), req.URL.Hostname()); err != nil {
		return nil, err
	}
	return t.next.RoundTrip(req)
}

// CloseIdleConnections lets http.Client.CloseIdleConnections reach the wrapped
// transport's pool.
func (t *targetCheckingTransport) CloseIdleConnections() {
	if closer, ok := t.next.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
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

// checkPrivateNetworkPolicy applies the PrivateNetworkDialContext destination
// policy to one address: unspecified, link-local, and cloud metadata
// endpoints are refused; everything else, loopback and private ranges
// included, is permitted. IPv6 forms that embed an IPv4 address (IPv4-mapped,
// 6to4, NAT64) are judged by the embedded IPv4 as well, as IsPublicIP does,
// so a blocked endpoint cannot be reached through a transition
// representation.
func checkPrivateNetworkPolicy(ip net.IP, host string) error {
	if ip.IsUnspecified() {
		return fmt.Errorf("blocked connection to unspecified address %s (host %s)", ip, host)
	}
	if IsLinkLocal(ip) {
		return fmt.Errorf("blocked connection to link-local address %s (host %s)", ip, host)
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return fmt.Errorf("blocked connection to unparseable address %s (host %s)", ip, host)
	}
	addr = addr.Unmap()
	if embedded, ok := embeddedIPv4(addr); ok {
		addr = embedded
		if addr.IsLinkLocalUnicast() {
			return fmt.Errorf("blocked connection to link-local address %s (host %s)", ip, host)
		}
	}
	for _, ep := range metadataEndpoints {
		if addr == ep {
			return fmt.Errorf("blocked connection to cloud metadata endpoint %s (host %s)", ip, host)
		}
	}
	return nil
}

// ResolvePrivateNetworkTarget resolves host and applies the
// PrivateNetworkDialContext destination policy to every address it resolves
// to, returning the validated addresses for the caller to dial. It is the
// resolve step of that dialer, kept separate so it can be tested on its own.
// An IP literal resolves to itself without a DNS query.
func ResolvePrivateNetworkTarget(ctx context.Context, host string) ([]net.IP, error) {
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("DNS lookup failed for %s: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("DNS lookup for %s returned no addresses", host)
	}
	for _, ip := range ips {
		if err := checkPrivateNetworkPolicy(ip, host); err != nil {
			return nil, err
		}
	}
	return ips, nil
}

// CheckPrivateNetworkLiteral applies the PrivateNetworkDialContext destination
// policy to host without any DNS lookup: an IP literal (with or without IPv6
// brackets) is checked, and a hostname is accepted as-is. It exists for the
// case where the connection is handed to an intermediary, such as an HTTP
// proxy, that resolves names on its own side: resolving locally there would
// make the request depend on DNS the host may not have (a proxy-only
// deployment), and would still not bind what the proxy connects to. So the
// caller enforces what it can verify without DNS, and name resolution for the
// proxied path is left to the proxy, which is operator configuration.
func CheckPrivateNetworkLiteral(host string) error {
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		return nil
	}
	return checkPrivateNetworkPolicy(ip, host)
}

// PrivateNetworkDialContext returns a DialContext that, unlike
// SSRFSafeDialContext, permits loopback, RFC1918/unique-local, and CGNAT
// destinations. It still blocks link-local addresses (including the
// 169.254.169.254 cloud metadata endpoint), the cloud metadata endpoints that
// live outside link-local (see metadataEndpoints), and unspecified addresses,
// which have no legitimate destination under any deployment topology. Same
// DNS-rebinding protection as SSRFSafeDialContext: resolves once and dials
// the validated IP directly.
//
// Use this only for features where connecting to a self-hosted or
// private-network target is the primary, documented use case (e.g. an MCP
// server running on the same host or inside the same private network) AND
// that capability is already gated by genuine authentication elsewhere - this
// function is a defense-in-depth backstop against link-local/metadata
// targets, not an authorization check.
func PrivateNetworkDialContext(dialTimeout time.Duration) func(ctx context.Context, netw, addr string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: dialTimeout}
	return func(ctx context.Context, netw, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("invalid dial address %q: %w", addr, err)
		}
		ips, err := ResolvePrivateNetworkTarget(ctx, host)
		if err != nil {
			return nil, err
		}
		// Every address resolved above passed the policy, so trying them in
		// turn is no weaker than dialing just the first: nothing outside this
		// one validated set is ever dialed, which is what defeats rebinding.
		// Pinning ips[0] instead would break the feature's primary documented
		// target: "localhost" resolves to [::1, 127.0.0.1] on a dual-stack
		// host, and an MCP server bound only to IPv4 is unreachable at ::1.
		var lastErr error
		for _, ip := range ips {
			conn, err := dialer.DialContext(ctx, netw, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
}

// maxRedirectHops caps how many redirects a guarded HTTP client follows. Five
// is enough for any legitimate well-known/CDN hop chain and keeps a hostile
// server from using a long chain to probe many internal addresses per request.
const maxRedirectHops = 5

// guardedRedirectDNSTimeout bounds the resolver call made while vetting a
// redirect target. The dial that follows re-resolves under the request
// context; this is only the early check.
const guardedRedirectDNSTimeout = 5 * time.Second

// NewSSRFSafeHTTPClient returns an *http.Client for fetching URLs that an
// untrusted party can influence (a configured catalog URL, a discovery
// document, a redirect Location). It is the strict policy: every connection is
// dialed through SSRFSafeDialContext, so loopback, private, CGNAT, link-local,
// unique-local, and IPv6-transition-wrapped internal targets are refused at
// dial time (which also defeats DNS rebinding), and every redirect hop is
// vetted by the same rule before it is followed. Only http and https are
// dialable: the transport has no handler for file or any other scheme, and a
// redirect to one is refused in CheckRedirect. Redirects stop after
// maxRedirectHops. The process HTTP(S)_PROXY environment is honored the way
// http.DefaultTransport honors it; on a proxied request the dialer sees the
// proxy, so the destination policy runs in the proxy selector instead, against
// the local resolver's answer, and a name only the proxy can resolve is left to
// the proxy - see guardedProxySelector and vetGuardedDestination.
//
// Callers that must reach a self-hosted target on the operator's own network
// use NewPrivateNetworkHTTPClient instead; this client never does.
func NewSSRFSafeHTTPClient(timeout time.Duration) *http.Client {
	return newGuardedHTTPClient(timeout, SSRFSafeDialContext(timeout), func(ip net.IP, host string) error {
		if !IsPublicIP(ip) {
			return fmt.Errorf("blocked redirect to non-public address %s (host %s)", ip, host)
		}
		return nil
	})
}

// NewPrivateNetworkHTTPClient is the PrivateNetworkDialContext counterpart of
// NewSSRFSafeHTTPClient, for fetchers whose documented primary use includes
// targets on the operator's own network (a self-hosted MCP server and its
// authorization server, a datasheet mirror). Loopback, RFC 1918, unique-local,
// and CGNAT destinations are dialable; unspecified, link-local (169.254/16,
// fe80::/10, and the 6to4/NAT64/Teredo forms of 169.254/16) and the
// non-link-local cloud metadata endpoints are refused at dial time, on every
// redirect hop, and on an IP-literal destination handed to a proxy. Same hop
// cap and scheme rules as NewSSRFSafeHTTPClient. Like PrivateNetworkDialContext
// itself, this is defense in depth against the categorically illegitimate
// ranges, not an authorization check: callers remain responsible for gating
// who may configure the URL.
func NewPrivateNetworkHTTPClient(timeout time.Duration) *http.Client {
	return newGuardedHTTPClient(timeout, PrivateNetworkDialContext(timeout), checkPrivateNetworkPolicy)
}

// newGuardedHTTPClient assembles the client shared by NewSSRFSafeHTTPClient
// and NewPrivateNetworkHTTPClient: dial is the per-connection gate, checkIP is
// the same policy expressed as a per-address predicate, applied to redirect
// targets (every resolved address must pass) and, through
// guardedProxySelector, to IP-literal destinations on the proxied path. The
// proxy is the global proxy when it is enabled for API traffic, else the
// environment's (DefaultProxyFunc).
func newGuardedHTTPClient(timeout time.Duration, dial func(ctx context.Context, netw, addr string) (net.Conn, error), checkIP func(ip net.IP, host string) error) *http.Client {
	return newGuardedHTTPClientWith(timeout, dial, checkIP, DefaultProxyFunc(ClientPurposeAPI), net.DefaultResolver)
}

// newGuardedHTTPClientWith is the seam behind newGuardedHTTPClient with the
// proxy selector and resolver injectable for tests.
func newGuardedHTTPClientWith(timeout time.Duration, dial func(ctx context.Context, netw, addr string) (net.Conn, error), checkIP func(ip net.IP, host string) error, proxy func(*http.Request) (*url.URL, error), resolver ipLookuper) *http.Client {
	transport := &http.Transport{
		Proxy:                 guardedProxySelector(proxy, checkIP, resolver),
		DialContext:           dial,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		ForceAttemptHTTP2:     true,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("blocked redirect to unsupported scheme %q", req.URL.Scheme)
			}
			if len(via) >= maxRedirectHops {
				return fmt.Errorf("stopped after %d redirects", maxRedirectHops)
			}
			host := req.URL.Hostname()
			if host == "" {
				return fmt.Errorf("blocked redirect to URL without a host")
			}
			proxied := false
			if proxy != nil {
				proxyURL, err := proxy(req)
				if err != nil {
					return fmt.Errorf("blocked redirect to %s: %w", host, err)
				}
				proxied = proxyURL != nil
			}
			ctx, cancel := context.WithTimeout(req.Context(), guardedRedirectDNSTimeout)
			defer cancel()
			if err := vetGuardedDestination(ctx, host, proxied, resolver, checkIP); err != nil {
				return fmt.Errorf("blocked redirect to %s: %w", host, err)
			}
			return nil
		},
	}
}

// guardedProxySelector wraps an http.Transport Proxy selector so the
// destination policy still applies to a request that is routed through a
// proxy, where the dialer only ever sees the proxy address. It runs
// vetGuardedDestination in proxied mode: an IP-literal destination is checked
// without DNS, a hostname is checked against what the local resolver returns,
// and a hostname the local resolver cannot answer is handed to the proxy,
// which resolves it on its own side (a proxy-only egress deployment). The
// proxy itself is operator configuration (process environment) and is the
// policy boundary for anything resolved only there; the dialer still refuses a
// proxy that sits on a blocked address. A nil selector stays nil.
func guardedProxySelector(next func(*http.Request) (*url.URL, error), checkIP func(ip net.IP, host string) error, resolver ipLookuper) func(*http.Request) (*url.URL, error) {
	if next == nil {
		return nil
	}
	return func(req *http.Request) (*url.URL, error) {
		proxyURL, err := next(req)
		if err != nil || proxyURL == nil {
			return proxyURL, err
		}
		ctx, cancel := context.WithTimeout(req.Context(), guardedRedirectDNSTimeout)
		defer cancel()
		if err := vetGuardedDestination(ctx, req.URL.Hostname(), true, resolver, checkIP); err != nil {
			return nil, err
		}
		return proxyURL, nil
	}
}

// vetGuardedDestination applies checkIP to a destination host before a guarded
// client contacts it. An IP literal is checked as is. A hostname is resolved
// and every returned address must pass. When the lookup itself fails,
// deferUnresolved decides: false refuses with a clear reason (the direct path,
// where the dialer would fail the same way, and the strict proxied path, where
// nothing may reach the proxy unvetted); true leaves the name to the proxy,
// which resolves it on its own side (the private-network proxied path, where a
// proxy-only DNS deployment is the documented reason local resolution fails).
func vetGuardedDestination(ctx context.Context, host string, deferUnresolved bool, resolver ipLookuper, checkIP func(ip net.IP, host string) error) error {
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		return checkIP(ip, host)
	}
	ips, err := resolver.LookupIP(ctx, "ip", host)
	if err != nil {
		if deferUnresolved {
			return nil
		}
		return fmt.Errorf("DNS lookup failed: %w", err)
	}
	for _, ip := range ips {
		if err := checkIP(ip, host); err != nil {
			return err
		}
	}
	return nil
}

// CheckProxiedDestination applies the dial-time destination policy to a
// request that will travel through a proxy, where the dialer only ever sees
// the proxy's address. strict selects the public-only policy of
// SSRFSafeDialContext; otherwise the private-network policy of
// PrivateNetworkDialContext applies. An IP literal is checked as is and a
// hostname against the resolver's answer. A hostname the resolver cannot
// answer is treated differently by policy: the private-network policy leaves
// it to the proxy (a proxy-only egress deployment resolves names there), the
// strict policy refuses it, because strict guards a target chosen by a caller
// who passed no credential check and a name that stops resolving locally is
// also how such a caller would route a later private resolution through the
// proxy unchecked.
func CheckProxiedDestination(ctx context.Context, host string, strict bool, resolver IPLookuper) error {
	checkIP := checkPrivateNetworkPolicy
	if strict {
		checkIP = func(ip net.IP, host string) error {
			if !IsPublicIP(ip) {
				return fmt.Errorf("blocked connection to non-public address %s (host %s)", ip, host)
			}
			return nil
		}
	}
	return vetGuardedDestination(ctx, host, !strict, resolver, checkIP)
}

// ProxyAwareTransport routes each request, by that request's own proxy
// decision, to one of two transports: Direct, which never uses a proxy and
// dials the destination under the destination policy, and ViaProxy, which
// always uses the proxy and so dials nothing but the proxy, under the proxy
// policy. Keeping the two policies on separate transports is what makes the
// classification per request: there is no shared record of "addresses that
// are proxies" for a later direct request to match by coincidence, and
// http.Transport hands DialContext the proxy address on exactly the transport
// whose dialer expects it.
type ProxyAwareTransport struct {
	// Proxy decides the route: nil means direct. It is the operator's proxy
	// configuration (http.ProxyFromEnvironment), not a policy check; the
	// destination policy for the proxied path runs in ViaProxy.Proxy.
	Proxy    func(*http.Request) (*url.URL, error)
	Direct   *http.Transport
	ViaProxy *http.Transport
}

// RoundTrip implements http.RoundTripper.
func (t *ProxyAwareTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.Proxy != nil {
		proxyURL, err := t.Proxy(req)
		if err != nil {
			return nil, err
		}
		if proxyURL != nil {
			return t.ViaProxy.RoundTrip(req)
		}
	}
	return t.Direct.RoundTrip(req)
}

// CloseIdleConnections closes idle connections on both transports.
func (t *ProxyAwareTransport) CloseIdleConnections() {
	t.Direct.CloseIdleConnections()
	t.ViaProxy.CloseIdleConnections()
}
