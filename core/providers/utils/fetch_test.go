package utils

import (
	"net"
	"testing"
)

// TestIsPublicIP guards the SSRF denylist used by FetchAndEncodeURL. It locks
// in both the long-standing blocks (loopback / RFC 1918 / link-local / ULA) and
// the hardened cases: CGNAT, IPv6-transition-wrapped internal IPv4 (6to4 /
// NAT64), and deprecated IPv6 site-local.
func TestIsPublicIP(t *testing.T) {
	tests := []struct {
		name string
		ip   string
		want bool
	}{
		// Legitimate public targets must still be fetchable.
		{"public v4 google dns", "8.8.8.8", true},
		{"public v4 cloudflare", "1.1.1.1", true},
		{"public v6", "2606:4700:4700::1111", true},
		// 6to4 wrapping a *public* IPv4 is legitimately public.
		{"6to4 public 8.8.8.8", "2002:0808:0808::", true},

		// Existing blocks — regression guard.
		{"loopback v4", "127.0.0.1", false},
		{"private 10/8", "10.0.0.1", false},
		{"private 172.16/12", "172.16.5.4", false},
		{"private 192.168/16", "192.168.1.1", false},
		{"link-local / IMDS", "169.254.169.254", false},
		{"loopback v6", "::1", false},
		{"ula v6", "fc00::1", false},
		{"link-local v6", "fe80::1", false},
		{"unspecified v4", "0.0.0.0", false},

		// New: CGNAT (RFC 6598).
		{"cgnat low", "100.64.0.1", false},
		{"cgnat high", "100.127.255.255", false},
		{"cgnat boundary just outside (public)", "100.128.0.1", true},

		// New: IPv4 metadata endpoint smuggled inside IPv6 transition forms.
		{"6to4-wrapped IMDS", "2002:a9fe:a9fe::", false},
		{"nat64 well-known-wrapped IMDS", "64:ff9b::a9fe:a9fe", false},
		{"nat64 local-use-wrapped IMDS", "64:ff9b:1:a9fe:a9:fe00::", false},

		// New: deprecated IPv6 site-local (RFC 3879).
		{"site-local fec0::/10", "fec0::1", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			if ip == nil {
				t.Fatalf("could not parse IP %q", tt.ip)
			}
			if got := isPublicIP(ip); got != tt.want {
				t.Errorf("isPublicIP(%s) = %v, want %v", tt.ip, got, tt.want)
			}
		})
	}
}
