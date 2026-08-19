package network

import (
	"context"
	"errors"
	"net"
	"strings"
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
