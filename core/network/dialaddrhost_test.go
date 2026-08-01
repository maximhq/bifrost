package network

import "testing"

func TestDialAddrHost(t *testing.T) {
	tests := []struct {
		addr string
		want string
	}{
		{"example.com:443", "example.com"},
		{"127.0.0.1:8080", "127.0.0.1"},
		{"[::1]:8080", "::1"},
		{"[2001:db8::1]:443", "2001:db8::1"},
		{"example.com", "example.com"}, // no port
		{"[::1]", "::1"},               // bracketed, no port
	}
	for _, tt := range tests {
		if got := dialAddrHost(tt.addr); got != tt.want {
			t.Errorf("dialAddrHost(%q) = %q, want %q", tt.addr, got, tt.want)
		}
	}
}

func TestNoProxyBypassIPv6(t *testing.T) {
	// An IPv6 literal listed in no_proxy must match after host extraction
	if !shouldBypassProxy(dialAddrHost("[::1]:8080"), "::1") {
		t.Error("[::1]:8080 should bypass proxy when no_proxy contains ::1")
	}
	if shouldBypassProxy(dialAddrHost("[2001:db8::1]:443"), "::1") {
		t.Error("non-listed IPv6 target must not bypass proxy")
	}
}
