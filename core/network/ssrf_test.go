package network

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestIsPublicIP guards the SSRF denylist used for dial-time validation. It
// locks in both the long-standing blocks (loopback / RFC 1918 / link-local /
// ULA) and the hardened cases: CGNAT, IPv6-transition-wrapped internal IPv4
// (6to4 / NAT64), and deprecated IPv6 site-local.
func TestIsPublicIP(t *testing.T) {
	tests := []struct {
		name string
		ip   string
		want bool
	}{
		// Legitimate public targets must still be dialable.
		{"public v4 google dns", "8.8.8.8", true},
		{"public v4 cloudflare", "1.1.1.1", true},
		{"public v6", "2606:4700:4700::1111", true},
		// 6to4 wrapping a *public* IPv4 is legitimately public.
		{"6to4 public 8.8.8.8", "2002:0808:0808::", true},

		// Baseline blocks — regression guard.
		{"loopback v4", "127.0.0.1", false},
		{"private 10/8", "10.0.0.1", false},
		{"private 172.16/12", "172.16.5.4", false},
		{"private 192.168/16", "192.168.1.1", false},
		{"link-local / IMDS", "169.254.169.254", false},
		{"loopback v6", "::1", false},
		{"ula v6", "fc00::1", false},
		{"link-local v6", "fe80::1", false},
		{"unspecified v4", "0.0.0.0", false},
		{"unspecified v6", "::", false},
		{"multicast v4", "224.0.0.1", false},
		{"broadcast v4", "255.255.255.255", false},
		{"interface-local multicast v6", "ff01::1", false},
		{"ipv4-mapped loopback", "::ffff:127.0.0.1", false},

		// CGNAT (RFC 6598).
		{"cgnat low", "100.64.0.1", false},
		{"cgnat high", "100.127.255.255", false},
		{"cgnat boundary just outside (public)", "100.128.0.1", true},

		// IPv4 metadata endpoint smuggled inside IPv6 transition forms.
		{"6to4-wrapped IMDS", "2002:a9fe:a9fe::", false},
		{"nat64 well-known-wrapped IMDS", "64:ff9b::a9fe:a9fe", false},
		{"nat64 local-use-wrapped IMDS", "64:ff9b:1:a9fe:a9:fe00::", false},

		// Deprecated IPv6 site-local (RFC 3879).
		{"site-local fec0::/10", "fec0::1", false},

		// Teredo (RFC 4380). The whole 2001:0000::/32 prefix is refused: the
		// embedded client IPv4 is XOR-obfuscated so none of the stdlib
		// predicates see through it, and the address carries a second,
		// separately attacker-chosen relay IPv4 in bits 32-63.
		// 2001:0000:4136:e378:8000:63bf:5601:5601 obfuscates 169.254.169.254
		// (0xa9fea9fe ^ 0xffffffff = 0x56015601) as the client address.
		{"teredo-wrapped IMDS", "2001:0000:4136:e378:8000:63bf:5601:5601", false},
		{"teredo prefix low", "2001:0::1", false},
		{"teredo prefix high", "2001:0:ffff:ffff:ffff:ffff:ffff:ffff", false},
		// The block is a /32, not a /16: ordinary global unicast that merely
		// starts with 2001: must stay reachable.
		{"global unicast 2001:4860 (Google) stays public", "2001:4860:4860::8888", true},
		{"global unicast 2001:1:: stays public", "2001:1::1", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			if ip == nil {
				t.Fatalf("could not parse IP %q", tt.ip)
			}
			if got := IsPublicIP(ip); got != tt.want {
				t.Errorf("IsPublicIP(%s) = %v, want %v", tt.ip, got, tt.want)
			}
		})
	}
}

// fakeResolver returns a fixed IP set (or error) and counts lookups so tests
// can assert that every dial triggers a fresh resolution.
type fakeResolver struct {
	ips     []net.IP
	err     error
	lookups int
}

func (f *fakeResolver) LookupIP(_ context.Context, _, _ string) ([]net.IP, error) {
	f.lookups++
	return f.ips, f.err
}

func TestSSRFSafeDialContextBlocksNonPublicResolution(t *testing.T) {
	// One public and one private A record: the private one must block the dial
	// entirely (an attacker controlling DNS can mix records).
	resolver := &fakeResolver{ips: []net.IP{net.ParseIP("93.184.216.34"), net.ParseIP("10.0.0.5")}}
	dial := ssrfSafeDialContext(resolver, func(_ context.Context, _, _ string) (net.Conn, error) {
		t.Fatal("dial must not be reached when a resolved IP is non-public")
		return nil, nil
	}, nil)

	_, err := dial(context.Background(), "tcp", "evil.example:443")
	if err == nil || !strings.Contains(err.Error(), "blocked connection to non-public address") {
		t.Fatalf("expected blocked-connection error, got %v", err)
	}
}

func TestSSRFSafeDialContextDialsValidatedIPDirectly(t *testing.T) {
	// The connection must go to the IP that passed validation, not through a
	// second hostname resolution — that closes the DNS-rebinding TOCTOU.
	resolver := &fakeResolver{ips: []net.IP{net.ParseIP("93.184.216.34"), net.ParseIP("93.184.216.35")}}
	var dialed string
	dialErr := errors.New("stop before real network")
	dial := ssrfSafeDialContext(resolver, func(_ context.Context, _, addr string) (net.Conn, error) {
		dialed = addr
		return nil, dialErr
	}, nil)

	_, err := dial(context.Background(), "tcp", "example.com:443")
	if !errors.Is(err, dialErr) {
		t.Fatalf("expected sentinel dial error, got %v", err)
	}
	if dialed != "93.184.216.34:443" {
		t.Fatalf("dialed %q, want first validated IP %q", dialed, "93.184.216.34:443")
	}
}

func TestSSRFSafeDialContextReResolvesPerDial(t *testing.T) {
	// Validation must run on every dial (redirects, pooled-connection re-dials),
	// so a record that turns private between attempts is caught.
	resolver := &fakeResolver{ips: []net.IP{net.ParseIP("127.0.0.1")}}
	dial := ssrfSafeDialContext(resolver, func(_ context.Context, _, _ string) (net.Conn, error) {
		t.Fatal("dial must not be reached")
		return nil, nil
	}, nil)

	for i := 0; i < 2; i++ {
		if _, err := dial(context.Background(), "tcp", "rebind.example:80"); err == nil {
			t.Fatal("expected blocked-connection error")
		}
	}
	if resolver.lookups != 2 {
		t.Fatalf("resolver invoked %d times, want one lookup per dial (2)", resolver.lookups)
	}
}

func TestSSRFSafeDialContextErrorPaths(t *testing.T) {
	t.Run("resolver error propagates", func(t *testing.T) {
		resolver := &fakeResolver{err: errors.New("nxdomain")}
		dial := ssrfSafeDialContext(resolver, nil, nil)
		if _, err := dial(context.Background(), "tcp", "gone.example:443"); err == nil || !strings.Contains(err.Error(), "DNS lookup failed") {
			t.Fatalf("expected DNS lookup error, got %v", err)
		}
	})

	t.Run("empty resolution rejected", func(t *testing.T) {
		resolver := &fakeResolver{}
		dial := ssrfSafeDialContext(resolver, nil, nil)
		if _, err := dial(context.Background(), "tcp", "empty.example:443"); err == nil || !strings.Contains(err.Error(), "no addresses") {
			t.Fatalf("expected no-addresses error, got %v", err)
		}
	})

	t.Run("address without port rejected", func(t *testing.T) {
		resolver := &fakeResolver{ips: []net.IP{net.ParseIP("93.184.216.34")}}
		dial := ssrfSafeDialContext(resolver, nil, nil)
		if _, err := dial(context.Background(), "tcp", "no-port.example"); err == nil || !strings.Contains(err.Error(), "invalid dial address") {
			t.Fatalf("expected invalid-address error, got %v", err)
		}
	})
}

func TestSSRFSafeDialContextBlocksLoopbackLiteral(t *testing.T) {
	// End-to-end through the exported constructor with the real resolver: an IP
	// literal resolves to itself and must be blocked before any connection.
	dial := SSRFSafeDialContext(time.Second)
	if _, err := dial(context.Background(), "tcp", "127.0.0.1:80"); err == nil || !strings.Contains(err.Error(), "blocked connection to non-public address") {
		t.Fatalf("expected blocked-connection error, got %v", err)
	}
}

func TestNewAllowlist_ParsesHostnamesIPsAndCIDRs(t *testing.T) {
	al, err := NewAllowlist([]string{"artifactory.internal.corp", "10.0.0.0/8", "192.168.1.5"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !al.Permits("artifactory.internal.corp", net.ParseIP("203.0.113.1")) {
		t.Fatal("expected hostname entry to permit its own name")
	}
	if !al.Permits("other.example", net.ParseIP("10.1.2.3")) {
		t.Fatal("expected CIDR entry to permit an IP inside the range")
	}
	if !al.Permits("other.example", net.ParseIP("192.168.1.5")) {
		t.Fatal("expected bare-IP entry to permit that exact IP")
	}
}

func TestNewAllowlist_RejectsInvalidEntry(t *testing.T) {
	for _, entry := range []string{"not a host!!", "10.0.0.0/999", "http://host", "host:9000", "999.168.1.1"} {
		t.Run(entry, func(t *testing.T) {
			if _, err := NewAllowlist([]string{entry}); err == nil {
				t.Fatalf("expected error for invalid entry %q", entry)
			}
		})
	}
}

// TestNewAllowlist_RejectsOutOfRangeIPOctetInsteadOfAcceptingAsHostname guards against a
// silent no-op: "999.168.1.1" is shaped like an IPv4 address (four dot-separated numeric
// labels) but fails to parse as one, and would otherwise be RFC-1123-valid as a hostname
// label, defeating the fail-loudly-on-typo goal NewAllowlist documents.
func TestNewAllowlist_RejectsOutOfRangeIPOctetInsteadOfAcceptingAsHostname(t *testing.T) {
	_, err := NewAllowlist([]string{"999.168.1.1"})
	if err == nil {
		t.Fatal("expected error for out-of-range IP octet, got none (silently accepted as hostname)")
	}
	if !strings.Contains(err.Error(), "looks like an IP address") {
		t.Fatalf("expected error to explain the IP-shaped rejection, got %v", err)
	}
}

func TestNewAllowlist_EmptyAndBlankEntriesSkipped(t *testing.T) {
	al, err := NewAllowlist([]string{"", "   ", "artifactory.internal.corp"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !al.Permits("artifactory.internal.corp", net.ParseIP("10.0.0.1")) {
		t.Fatal("expected the one real entry to still be parsed")
	}
}

func TestAllowlist_PermitsHostnameExactMatchCaseInsensitive(t *testing.T) {
	al, err := NewAllowlist([]string{"Artifactory.Internal.Corp"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !al.Permits("artifactory.internal.corp", net.ParseIP("10.0.0.1")) {
		t.Fatal("expected lowercased match")
	}
	if !al.Permits("ARTIFACTORY.INTERNAL.CORP", net.ParseIP("10.0.0.1")) {
		t.Fatal("expected uppercased match")
	}
	if al.Permits("other.internal.corp", net.ParseIP("10.0.0.1")) {
		t.Fatal("expected a different hostname to not match")
	}
}

func TestAllowlist_PermitsCIDRMatch(t *testing.T) {
	al, err := NewAllowlist([]string{"10.0.5.0/24"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !al.Permits("irrelevant.example", net.ParseIP("10.0.5.42")) {
		t.Fatal("expected IP inside allowlisted CIDR to be permitted")
	}
	if al.Permits("irrelevant.example", net.ParseIP("10.0.6.42")) {
		t.Fatal("expected IP outside allowlisted CIDR to be blocked")
	}
}

func TestAllowlist_PermitsBareIPAsSingleHostMatch(t *testing.T) {
	al, err := NewAllowlist([]string{"10.0.5.42"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !al.Permits("irrelevant.example", net.ParseIP("10.0.5.42")) {
		t.Fatal("expected the exact allowlisted IP to be permitted")
	}
	if al.Permits("irrelevant.example", net.ParseIP("10.0.5.43")) {
		t.Fatal("expected a neighboring IP to remain blocked")
	}
}

func TestAllowlist_NilReceiverPermitsNothing(t *testing.T) {
	var al *Allowlist
	if al.Permits("anything.example", net.ParseIP("10.0.0.1")) {
		t.Fatal("expected nil allowlist to permit nothing")
	}
}

func TestAllowlist_UnwrapsEmbeddedIPv4ForCIDRMatch(t *testing.T) {
	al, err := NewAllowlist([]string{"169.254.169.254/32"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 6to4-wrapped form of the allowlisted IPv4 must still match, mirroring
	// IsPublicIP's transition-address handling.
	if !al.Permits("irrelevant.example", net.ParseIP("2002:a9fe:a9fe::")) {
		t.Fatal("expected 6to4-wrapped allowlisted IPv4 to be permitted")
	}
}

func TestSSRFSafeDialContextWithAllowlist_PermitsAllowlistedPrivateHost(t *testing.T) {
	al, err := NewAllowlist([]string{"internal.example"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resolver := &fakeResolver{ips: []net.IP{net.ParseIP("10.0.0.5")}}
	var dialed string
	dialErr := errors.New("stop before real network")
	dial := ssrfSafeDialContext(resolver, func(_ context.Context, _, addr string) (net.Conn, error) {
		dialed = addr
		return nil, dialErr
	}, al)

	_, err = dial(context.Background(), "tcp", "internal.example:443")
	if !errors.Is(err, dialErr) {
		t.Fatalf("expected sentinel dial error, got %v", err)
	}
	if dialed != "10.0.0.5:443" {
		t.Fatalf("dialed %q, want allowlisted private IP %q", dialed, "10.0.0.5:443")
	}
}

func TestSSRFSafeDialContextWithAllowlist_StillBlocksNonAllowlistedPrivateHost(t *testing.T) {
	al, err := NewAllowlist([]string{"internal.example"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resolver := &fakeResolver{ips: []net.IP{net.ParseIP("10.0.0.5")}}
	dial := ssrfSafeDialContext(resolver, func(_ context.Context, _, _ string) (net.Conn, error) {
		t.Fatal("dial must not be reached for a non-allowlisted private host")
		return nil, nil
	}, al)

	_, err = dial(context.Background(), "tcp", "not-allowlisted.example:443")
	if err == nil || !strings.Contains(err.Error(), "blocked connection to non-public address") {
		t.Fatalf("expected blocked-connection error, got %v", err)
	}
}

func TestSSRFSafeDialContextWithAllowlist_ReResolvesPerDialEvenWhenAllowlisted(t *testing.T) {
	al, err := NewAllowlist([]string{"internal.example"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resolver := &fakeResolver{ips: []net.IP{net.ParseIP("10.0.0.5")}}
	dial := ssrfSafeDialContext(resolver, func(_ context.Context, _, _ string) (net.Conn, error) {
		return nil, errors.New("stop before real network")
	}, al)

	for i := 0; i < 2; i++ {
		_, _ = dial(context.Background(), "tcp", "internal.example:443")
	}
	if resolver.lookups != 2 {
		t.Fatalf("resolver invoked %d times, want one lookup per dial (2)", resolver.lookups)
	}
}

func TestSSRFSafeDialContextWithAllowlist_NilAllowlistMatchesPlainDialContext(t *testing.T) {
	resolver := &fakeResolver{ips: []net.IP{net.ParseIP("10.0.0.5")}}
	dial := ssrfSafeDialContext(resolver, func(_ context.Context, _, _ string) (net.Conn, error) {
		t.Fatal("dial must not be reached")
		return nil, nil
	}, nil)

	_, err := dial(context.Background(), "tcp", "no-allowlist.example:443")
	if err == nil || !strings.Contains(err.Error(), "blocked connection to non-public address") {
		t.Fatalf("expected blocked-connection error, got %v", err)
	}
}

func TestPrivateNetworkDialContextBlocksLinkLocal(t *testing.T) {
	dial := PrivateNetworkDialContext(time.Second)
	if _, err := dial(context.Background(), "tcp", "169.254.169.254:80"); err == nil || !strings.Contains(err.Error(), "blocked connection to link-local address") {
		t.Fatalf("expected blocked link-local error, got %v", err)
	}
}

func TestPrivateNetworkDialContextBlocksUnspecified(t *testing.T) {
	dial := PrivateNetworkDialContext(time.Second)
	if _, err := dial(context.Background(), "tcp", "0.0.0.0:80"); err == nil || !strings.Contains(err.Error(), "blocked connection to unspecified address") {
		t.Fatalf("expected blocked unspecified error, got %v", err)
	}
}

// TestPrivateNetworkDialContextAllowsLoopback locks in the deliberate
// difference from SSRFSafeDialContext: a self-hosted MCP server on the same
// host is the documented primary use case, so loopback must stay dialable.
func TestPrivateNetworkDialContextAllowsLoopback(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start test listener: %v", err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			conn.Close()
		}
	}()

	dial := PrivateNetworkDialContext(time.Second)
	conn, err := dial(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("expected loopback dial to succeed, got %v", err)
	}
	conn.Close()
}

// TestPrivateNetworkDialContextFallsBackAcrossResolvedAddresses locks in the
// multi-address behavior the MCP feature depends on: "localhost" resolves to
// [::1, 127.0.0.1] on a dual-stack host, and an MCP server bound only to IPv4
// must still be reachable. Pinning the first resolved address would make the
// documented http://localhost:PORT/mcp target undialable.
func TestPrivateNetworkDialContextFallsBackAcrossResolvedAddresses(t *testing.T) {
	ips, err := net.DefaultResolver.LookupIP(context.Background(), "ip", "localhost")
	if err != nil || len(ips) < 2 {
		t.Skipf("localhost does not resolve to multiple addresses here (%v, err=%v)", ips, err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start test listener: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("failed to split listener address: %v", err)
	}

	dial := PrivateNetworkDialContext(2 * time.Second)
	conn, err := dial(context.Background(), "tcp", net.JoinHostPort("localhost", port))
	if err != nil {
		t.Fatalf("expected dial to fall back to the reachable resolved address, got %v", err)
	}
	conn.Close()
}

// TestResolvePrivateNetworkTargetPolicy locks in the destination policy that
// PrivateNetworkDialContext and the MCP proxy selector share: link-local and
// unspecified are refused, loopback is permitted, an IP literal resolves to
// itself, and an empty host (a request with no authority) is refused rather
// than passed through.
func TestResolvePrivateNetworkTargetPolicy(t *testing.T) {
	ctx := context.Background()
	if _, err := ResolvePrivateNetworkTarget(ctx, "169.254.169.254"); err == nil || !strings.Contains(err.Error(), "blocked connection to link-local address") {
		t.Fatalf("expected blocked link-local error, got %v", err)
	}
	if _, err := ResolvePrivateNetworkTarget(ctx, "0.0.0.0"); err == nil || !strings.Contains(err.Error(), "blocked connection to unspecified address") {
		t.Fatalf("expected blocked unspecified error, got %v", err)
	}
	ips, err := ResolvePrivateNetworkTarget(ctx, "127.0.0.1")
	if err != nil || len(ips) != 1 || !ips[0].Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatalf("expected loopback literal to resolve to itself, got %v, %v", ips, err)
	}
	if _, err := ResolvePrivateNetworkTarget(ctx, ""); err == nil {
		t.Fatal("expected an empty host to be refused")
	}
	// Cloud metadata endpoints outside link-local: Alibaba sits in CGNAT and
	// the AWS IPv6 IMDS in unique-local, both ranges this policy otherwise
	// permits, so they need their own block. The transition-wrapped forms
	// must be caught too, as they are for link-local in IsPublicIP.
	for _, host := range []string{
		"100.100.100.200",
		"fd00:ec2::254",
		"::ffff:100.100.100.200", // IPv4-mapped
		"2002:6464:64c8::",       // 6to4 wrapping 100.100.100.200
		"64:ff9b::6464:64c8",     // NAT64 wrapping 100.100.100.200
	} {
		if _, err := ResolvePrivateNetworkTarget(ctx, host); err == nil || !strings.Contains(err.Error(), "blocked connection to cloud metadata endpoint") {
			t.Fatalf("%s: expected blocked metadata endpoint error, got %v", host, err)
		}
	}
	if _, err := ResolvePrivateNetworkTarget(ctx, "2002:a9fe:a9fe::"); err == nil || !strings.Contains(err.Error(), "blocked connection to link-local address") {
		t.Fatalf("expected 6to4-wrapped link-local to be blocked, got %v", err)
	}
	// Neighbours of the blocked endpoints stay permitted: the block is on the
	// endpoints themselves, not on the CGNAT or unique-local ranges.
	for _, host := range []string{"100.100.100.201", "fd00:ec2::255", "10.0.0.5"} {
		if _, err := ResolvePrivateNetworkTarget(ctx, host); err != nil {
			t.Fatalf("%s: expected permitted private target, got %v", host, err)
		}
	}
}

// TestCheckPrivateNetworkLiteral locks in the DNS-free variant used on the
// proxied path: literals are judged by the same policy, bracketed IPv6 is
// accepted, and a hostname is passed through untouched with no lookup.
func TestCheckPrivateNetworkLiteral(t *testing.T) {
	for _, tc := range []struct {
		host string
		want string
	}{
		{"169.254.169.254", "blocked connection to link-local address"},
		{"[fe80::1]", "blocked connection to link-local address"},
		{"0.0.0.0", "blocked connection to unspecified address"},
		{"100.100.100.200", "blocked connection to cloud metadata endpoint"},
		{"[fd00:ec2::254]", "blocked connection to cloud metadata endpoint"},
		{"fd00:ec2::254", "blocked connection to cloud metadata endpoint"},
	} {
		if err := CheckPrivateNetworkLiteral(tc.host); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: expected %q, got %v", tc.host, tc.want, err)
		}
	}
	for _, host := range []string{"127.0.0.1", "[::1]", "10.0.0.5", "100.100.100.201"} {
		if err := CheckPrivateNetworkLiteral(host); err != nil {
			t.Fatalf("%s: expected permitted literal, got %v", host, err)
		}
	}
	// A hostname is not resolved: .invalid never resolves (RFC 6761), and a
	// name that would resolve to a blocked address is the proxy's concern.
	for _, host := range []string{"mcp.invalid", "metadata.google.internal", ""} {
		if err := CheckPrivateNetworkLiteral(host); err != nil {
			t.Fatalf("%q: expected hostname to pass through without lookup, got %v", host, err)
		}
	}
}

// TestPrivateNetworkDialContextBlocksMetadataEndpoints proves the direct
// dialer refuses the non-link-local metadata endpoints before any dial.
func TestPrivateNetworkDialContextBlocksMetadataEndpoints(t *testing.T) {
	dial := PrivateNetworkDialContext(time.Second)
	for _, addr := range []string{"100.100.100.200:80", "[fd00:ec2::254]:80"} {
		if _, err := dial(context.Background(), "tcp", addr); err == nil || !strings.Contains(err.Error(), "blocked connection to cloud metadata endpoint") {
			t.Fatalf("%s: expected blocked metadata endpoint error, got %v", addr, err)
		}
	}
}

// TestIsLinkLocal pins the always-blocked link-local gate against IPv6
// transition forms: an IPv4 link-local address wrapped in 6to4 or NAT64 is
// still link-local, and the Teredo prefix is refused outright for the same
// reasons IsPublicIP refuses it.
func TestIsLinkLocal(t *testing.T) {
	tests := []struct {
		name string
		ip   string
		want bool
	}{
		{"ipv4 link-local / IMDS", "169.254.169.254", true},
		{"ipv4 link-local low", "169.254.0.1", true},
		{"ipv4-mapped link-local", "::ffff:169.254.169.254", true},
		{"ipv6 link-local", "fe80::1", true},
		{"6to4-wrapped IMDS", "2002:a9fe:a9fe::", true},
		{"nat64 well-known-wrapped IMDS", "64:ff9b::a9fe:a9fe", true},
		{"nat64 local-use-wrapped IMDS", "64:ff9b:1:a9fe:a9:fe00::", true},
		{"teredo-wrapped IMDS", "2001:0000:4136:e378:8000:63bf:5601:5601", true},
		{"teredo prefix low", "2001:0::1", true},

		{"public v4", "8.8.8.8", false},
		{"private v4 is not link-local", "10.0.0.1", false},
		{"loopback v4 is not link-local", "127.0.0.1", false},
		{"public v6", "2606:4700:4700::1111", false},
		{"ula v6 is not link-local", "fd00::1", false},
		{"6to4 public", "2002:0808:0808::", false},
		{"nat64 public", "64:ff9b::0808:0808", false},
		{"global unicast 2001:4860 stays public", "2001:4860:4860::8888", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			if ip == nil {
				t.Fatalf("could not parse IP %q", tt.ip)
			}
			if got := IsLinkLocal(ip); got != tt.want {
				t.Errorf("IsLinkLocal(%s) = %v, want %v", tt.ip, got, tt.want)
			}
		})
	}
}

// newRedirectingServer returns a loopback server whose every response is a
// 302 to target.
func newRedirectingServer(t *testing.T, target string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// mustFailGet issues a GET through client and requires it to fail with an
// error mentioning want. A *successful* response is a test failure: it means
// the client reached a destination it must refuse.
func mustFailGet(t *testing.T, client *http.Client, rawURL, want string) {
	t.Helper()
	resp, err := client.Get(rawURL)
	if err == nil {
		resp.Body.Close()
		t.Fatalf("GET %s: expected refusal, got HTTP %d", rawURL, resp.StatusCode)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("GET %s: expected error containing %q, got %v", rawURL, want, err)
	}
}

// TestNewSSRFSafeHTTPClientRefusesLoopbackEntry: the strict client's dial
// gate refuses a loopback destination outright, so the handler is never
// reached.
func TestNewSSRFSafeHTTPClientRefusesLoopbackEntry(t *testing.T) {
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("loopback target must never be reached through the strict client")
	}))
	defer internal.Close()
	mustFailGet(t, NewSSRFSafeHTTPClient(5*time.Second), internal.URL, "blocked connection to non-public address")
}

// TestGuardedClientRedirectToLoopbackRefusedEndToEnd drives a real redirect
// through http.Client with the strict redirect policy: the entry server
// answers 302 to a loopback target, and the target must never see a request.
// The strict dialer would refuse the loopback entry itself (see the test
// above), so the entry hop is dialed plainly here and only the redirect
// policy is strict - that isolates CheckRedirect as the layer under test.
func TestGuardedClientRedirectToLoopbackRefusedEndToEnd(t *testing.T) {
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("redirect target must never be reached")
	}))
	defer internal.Close()
	for _, target := range []string{internal.URL, "http://169.254.169.254/latest/meta-data/"} {
		entry := newRedirectingServer(t, target)
		client := newGuardedHTTPClient(5*time.Second, (&net.Dialer{}).DialContext, func(ip net.IP, host string) error {
			if !IsPublicIP(ip) {
				return fmt.Errorf("blocked redirect to non-public address %s (host %s)", ip, host)
			}
			return nil
		})
		mustFailGet(t, client, entry.URL, "blocked redirect to non-public address")
	}
}

// TestNewSSRFSafeHTTPClientRefusesRedirectTargets exercises the strict
// client's CheckRedirect directly against loopback, link-local, NAT64-wrapped
// link-local, and non-http targets.
func TestNewSSRFSafeHTTPClientRefusesRedirectTargets(t *testing.T) {
	client := NewSSRFSafeHTTPClient(5 * time.Second)
	for _, target := range []string{
		"http://127.0.0.1:9/internal",
		"http://169.254.169.254/latest/meta-data/",
		"http://[64:ff9b::a9fe:a9fe]/latest/meta-data/",
		"file:///etc/passwd",
	} {
		req, err := http.NewRequest(http.MethodGet, target, nil)
		if err != nil {
			t.Fatalf("new request %s: %v", target, err)
		}
		via := []*http.Request{{URL: &url.URL{Scheme: "https", Host: "entry.example"}}}
		if err := client.CheckRedirect(req, via); err == nil {
			t.Fatalf("redirect to %s must be refused", target)
		}
	}
}

// TestGuardedRedirectHopCap proves the hop cap: the sixth redirect is refused
// even when every hop is otherwise acceptable.
func TestGuardedRedirectHopCap(t *testing.T) {
	client := NewSSRFSafeHTTPClient(5 * time.Second)
	req, _ := http.NewRequest(http.MethodGet, "https://public.example/next", nil)
	via := make([]*http.Request, maxRedirectHops)
	for i := range via {
		via[i] = &http.Request{URL: &url.URL{Scheme: "https", Host: "entry.example"}}
	}
	if err := client.CheckRedirect(req, via); err == nil || !strings.Contains(err.Error(), "redirects") {
		t.Fatalf("expected hop cap error, got %v", err)
	}
}

// TestNewPrivateNetworkHTTPClient locks in the private-network policy: a
// loopback entry is dialable (self-hosted targets are the point), but a
// redirect from it to a link-local or NAT64-wrapped metadata address is
// refused, and the redirect target is never contacted.
func TestNewPrivateNetworkHTTPClient(t *testing.T) {
	client := NewPrivateNetworkHTTPClient(5 * time.Second)

	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ok.Close()
	resp, err := client.Get(ok.URL)
	if err != nil {
		t.Fatalf("loopback entry must be reachable through the private-network client: %v", err)
	}
	resp.Body.Close()

	for _, target := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://[64:ff9b::a9fe:a9fe]/latest/meta-data/",
		"http://[2002:a9fe:a9fe::]/latest/meta-data/",
	} {
		mustFailGet(t, client, newRedirectingServer(t, target).URL, "link-local")
	}
	mustFailGet(t, client, newRedirectingServer(t, "http://100.100.100.200/latest/meta-data/").URL, "cloud metadata endpoint")
	mustFailGet(t, client, newRedirectingServer(t, "file:///etc/passwd").URL, "unsupported scheme")
	mustFailGet(t, client, "http://169.254.169.254/latest/meta-data/", "link-local")
}

// fixedProxy is a Transport proxy selector that routes every request through
// one proxy, standing in for HTTP(S)_PROXY in the process environment.
func fixedProxy(t *testing.T, raw string) func(*http.Request) (*url.URL, error) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse proxy %q: %v", raw, err)
	}
	return func(*http.Request) (*url.URL, error) { return u, nil }
}

func redirectVia() []*http.Request {
	return []*http.Request{{URL: &url.URL{Scheme: "https", Host: "entry.example"}}}
}

// TestGuardedRedirectViaProxyToleratesProxyOnlyDNS: when the redirect hop is
// routed through a proxy, the proxy resolves the hostname on its own side, so a
// local resolver that cannot resolve it (a proxy-only egress deployment) must
// not block the hop. The direct path keeps failing closed on the same error.
func TestGuardedRedirectViaProxyToleratesProxyOnlyDNS(t *testing.T) {
	noDNS := &fakeResolver{err: errors.New("no route to resolver")}
	req, _ := http.NewRequest(http.MethodGet, "https://catalog.example/next", nil)

	proxied := newGuardedHTTPClientWith(5*time.Second, (&net.Dialer{}).DialContext, checkPrivateNetworkPolicy, fixedProxy(t, "http://proxy.internal:3128"), noDNS)
	if err := proxied.CheckRedirect(req, redirectVia()); err != nil {
		t.Fatalf("proxied redirect to an unresolvable hostname must be left to the proxy, got %v", err)
	}

	direct := newGuardedHTTPClientWith(5*time.Second, (&net.Dialer{}).DialContext, checkPrivateNetworkPolicy, nil, noDNS)
	if err := direct.CheckRedirect(req, redirectVia()); err == nil || !strings.Contains(err.Error(), "DNS lookup failed") {
		t.Fatalf("direct redirect with a failing resolver must be refused, got %v", err)
	}
}

// TestNewSSRFSafeHTTPClientReachesConfiguredPrivateProxy pins that the SSRF-safe client
// can use a global API proxy that sits on a private or loopback address, the normal
// self-hosted setup. The proxy is dialed with the private-network policy; direct
// destinations keep the public-only dialer, and the destination is still vetted before
// it is handed to the proxy.
func TestNewSSRFSafeHTTPClientReachesConfiguredPrivateProxy(t *testing.T) {
	var seen atomic.Value
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Store(r.URL.Host)
		_, _ = w.Write([]byte("via-proxy"))
	}))
	defer proxy.Close()
	SetDefaultHTTPClientFactory(NewHTTPClientFactory(&GlobalProxyConfig{
		Enabled: true, Type: GlobalProxyTypeHTTP, URL: proxy.URL, EnableForAPI: true,
	}, nil))
	t.Cleanup(func() { SetDefaultHTTPClientFactory(nil) })

	client := NewSSRFSafeHTTPClient(5 * time.Second)
	resp, err := client.Get("http://203.0.113.10/catalog.json")
	if err != nil {
		t.Fatalf("a loopback API proxy must be reachable, got %v", err)
	}
	resp.Body.Close()
	if got, _ := seen.Load().(string); got != "203.0.113.10" {
		t.Fatalf("proxy saw target %q, want 203.0.113.10", got)
	}

	if _, err := client.Get("http://10.0.0.5/catalog.json"); err == nil || !strings.Contains(err.Error(), "non-public") {
		t.Fatalf("a private destination must still be refused before the proxy, got %v", err)
	}
}

// TestGuardedRedirectViaProxyStillAppliesPolicyWhenResolvable: a proxied hop
// is not a policy bypass. When the local resolver does answer, a hostname that
// maps to a blocked address is refused before the proxy is asked for it.
func TestGuardedRedirectViaProxyStillAppliesPolicyWhenResolvable(t *testing.T) {
	metadata := &fakeResolver{ips: []net.IP{net.ParseIP("169.254.169.254")}}
	client := newGuardedHTTPClientWith(5*time.Second, (&net.Dialer{}).DialContext, checkPrivateNetworkPolicy, fixedProxy(t, "http://proxy.internal:3128"), metadata)
	req, _ := http.NewRequest(http.MethodGet, "http://metadata.internal/latest/meta-data/", nil)
	if err := client.CheckRedirect(req, redirectVia()); err == nil || !strings.Contains(err.Error(), "link-local") {
		t.Fatalf("proxied redirect to a hostname resolving to link-local must be refused, got %v", err)
	}
}

// TestGuardedProxySelectorAppliesPolicyToHostnames: on the proxied path the
// dialer only ever sees the proxy, so the selector is where the destination
// policy runs. A hostname that resolves locally to a blocked address is
// refused; one the local resolver cannot resolve is handed to the proxy, which
// resolves it on its own side; an IP literal is checked without DNS.
func TestGuardedProxySelectorAppliesPolicyToHostnames(t *testing.T) {
	proxy := fixedProxy(t, "http://proxy.internal:3128")
	cases := []struct {
		name     string
		target   string
		resolver *fakeResolver
		wantErr  string
	}{
		{"hostname resolving to metadata is refused", "http://metadata.internal/", &fakeResolver{ips: []net.IP{net.ParseIP("169.254.169.254")}}, "link-local"},
		{"hostname resolving to NAT64 metadata is refused", "http://metadata.internal/", &fakeResolver{ips: []net.IP{net.ParseIP("64:ff9b::a9fe:a9fe")}}, "link-local"},
		{"hostname resolving to a public address is proxied", "http://catalog.example/", &fakeResolver{ips: []net.IP{net.ParseIP("93.184.216.34")}}, ""},
		{"unresolvable hostname is left to the proxy", "http://only-the-proxy-knows.corp/", &fakeResolver{err: errors.New("no such host")}, ""},
		{"blocked IP literal is refused without DNS", "http://169.254.169.254/", &fakeResolver{err: errors.New("must not resolve")}, "link-local"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sel := guardedProxySelector(proxy, checkPrivateNetworkPolicy, tc.resolver)
			req, _ := http.NewRequest(http.MethodGet, tc.target, nil)
			got, err := sel(req)
			if tc.wantErr == "" {
				if err != nil || got == nil {
					t.Fatalf("expected the proxy to be selected, got url=%v err=%v", got, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected refusal containing %q, got url=%v err=%v", tc.wantErr, got, err)
			}
		})
	}
}

// TestProxyAwareTransportRoutesByEachRequestsProxyDecision: a request the
// selector routes through the proxy is served by ViaProxy (whose dialer only
// ever sees the proxy), one it does not goes through Direct, and the decision
// is made per request with nothing carried over - a direct request whose
// destination is the proxy's own address still gets the destination policy.
func TestProxyAwareTransportRoutesByEachRequestsProxyDecision(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Served-By", "proxy")
		w.WriteHeader(http.StatusOK)
	}))
	defer proxy.Close()
	proxyURL, _ := url.Parse(proxy.URL)

	direct := &http.Transport{DialContext: SSRFSafeDialContext(5 * time.Second)}
	via := &http.Transport{Proxy: func(*http.Request) (*url.URL, error) { return proxyURL, nil }, DialContext: PrivateNetworkDialContext(5 * time.Second)}
	rt := &ProxyAwareTransport{
		Proxy: func(req *http.Request) (*url.URL, error) {
			if req.URL.Hostname() == "mcp.example" {
				return proxyURL, nil
			}
			return nil, nil
		},
		Direct:   direct,
		ViaProxy: via,
	}
	client := &http.Client{Transport: rt}

	resp, err := client.Get("http://mcp.example/mcp")
	if err != nil {
		t.Fatalf("proxied request: %v", err)
	}
	resp.Body.Close()
	if resp.Header.Get("X-Served-By") != "proxy" {
		t.Fatalf("proxied request must be answered by the proxy, got %q", resp.Header.Get("X-Served-By"))
	}

	mustFailGet(t, client, proxy.URL+"/mcp", "blocked connection to non-public address")

	if _, err := (&ProxyAwareTransport{Proxy: func(*http.Request) (*url.URL, error) { return nil, errors.New("selector failed") }, Direct: direct, ViaProxy: via}).RoundTrip(&http.Request{URL: &url.URL{Scheme: "http", Host: "x.example"}}); err == nil || err.Error() != "selector failed" {
		t.Fatalf("a selector error must be returned as is, got %v", err)
	}
	rt.CloseIdleConnections()
}

// TestCheckProxiedDestination pins the two policies on the proxied path.
func TestCheckProxiedDestination(t *testing.T) {
	cases := []struct {
		name     string
		host     string
		strict   bool
		resolver *fakeResolver
		wantErr  string
	}{
		{"strict refuses private answer", "mcp.corp", true, &fakeResolver{ips: []net.IP{net.ParseIP("10.0.0.5")}}, "non-public"},
		{"strict refuses loopback literal", "127.0.0.1", true, &fakeResolver{err: errors.New("no dns")}, "non-public"},
		{"strict accepts public answer", "mcp.example", true, &fakeResolver{ips: []net.IP{net.ParseIP("93.184.216.34")}}, ""},
		{"strict refuses an unresolvable name rather than trusting the proxy", "only.proxy.knows", true, &fakeResolver{err: errors.New("no dns")}, "DNS lookup failed"},
		{"permissive leaves an unresolvable name to the proxy", "only.proxy.knows", false, &fakeResolver{err: errors.New("no dns")}, ""},
		{"permissive accepts private answer", "mcp.corp", false, &fakeResolver{ips: []net.IP{net.ParseIP("10.0.0.5")}}, ""},
		{"permissive refuses metadata answer", "md.corp", false, &fakeResolver{ips: []net.IP{net.ParseIP("169.254.169.254")}}, "link-local"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckProxiedDestination(context.Background(), tc.host, tc.strict, tc.resolver)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected acceptance, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected refusal containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// hostResolver answers per hostname, so a test can make the target public and
// everything else unreachable. IP literals resolve to themselves, like net.Resolver.
type hostResolver map[string][]net.IP

func (h hostResolver) LookupIP(_ context.Context, _, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	if ips, ok := h[host]; ok {
		return ips, nil
	}
	return nil, errors.New("no such host")
}

func TestPublicTargetCheck_PublicTargetReturnsItsAddresses(t *testing.T) {
	ips, err := publicTargetCheck(hostResolver{"files.example": {net.ParseIP("203.0.113.10")}}, nil)(context.Background(), "files.example")
	if err != nil {
		t.Fatalf("public target refused: %v", err)
	}
	if len(ips) != 1 || !ips[0].Equal(net.ParseIP("203.0.113.10")) {
		t.Fatalf("checked addresses = %v, want [203.0.113.10]: the caller tunnels to these", ips)
	}
}

func TestPublicTargetCheck_RefusesNonPublicTargets(t *testing.T) {
	tests := map[string]hostResolver{
		// One public and one private record: the private one blocks, as on the direct path.
		"internal.example": {"internal.example": {net.ParseIP("203.0.113.10"), net.ParseIP("10.0.0.5")}},
		"127.0.0.1":        {},
		"169.254.169.254":  {},
		"fd00:ec2::254":    {},
	}
	for host, resolver := range tests {
		ips, err := publicTargetCheck(resolver, nil)(context.Background(), host)
		if err == nil || !strings.Contains(err.Error(), "blocked connection to non-public address") {
			t.Errorf("%s: expected a non-public address error, got %v", host, err)
		}
		if ips != nil {
			t.Errorf("%s: a refused target must return no addresses to dial, got %v", host, ips)
		}
	}
}

func TestPublicTargetCheck_UnresolvableTargetIsRefused(t *testing.T) {
	_, err := publicTargetCheck(hostResolver{}, nil)(context.Background(), "only-the-proxy-knows.example")
	if err == nil || !strings.Contains(err.Error(), "DNS lookup failed") {
		t.Fatalf("expected a DNS failure, got %v", err)
	}
}
