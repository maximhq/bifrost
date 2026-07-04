package handlers

import "testing"

func TestIsLocalhost(t *testing.T) {
	tests := []struct {
		host string
		want bool
	}{
		{"localhost", true},
		{"localhost:8080", true},
		{"127.0.0.1", true},
		{"127.0.0.1:8080", true},
		{"::1", true},
		{"[::1]", true},
		{"[::1]:8080", true},
		{"::ffff:127.0.0.1", true},
		{"", false},
		{"[::1", false},
		{"::1]", false},
		{"[localhost]", false},
		{"example.com", false},
		{"example.com:8080", false},
		{"evil-localhost.com:80", false},
		{"192.168.1.10:8080", false},
		{"[2001:db8::1]:8080", false},
		{"2001:db8::1", false},
	}
	for _, tt := range tests {
		if got := isLocalhost(tt.host); got != tt.want {
			t.Errorf("isLocalhost(%q) = %v, want %v", tt.host, got, tt.want)
		}
	}
}

func TestIsLocalhostOrigin(t *testing.T) {
	tests := []struct {
		origin string
		want   bool
	}{
		{"http://localhost:3000", true},
		{"https://localhost:3000", true},
		{"http://127.0.0.1:3000", true},
		{"https://127.0.0.1:3000", true},
		{"http://0.0.0.0:3000", true},
		{"http://[::1]:3000", true},
		{"https://[::1]:3000", true},
		{"http://[::]:3000", true},
		{"http://localhost", true},
		{"http://example.com:3000", false},
		{"https://evil.com", false},
		{"http://[2001:db8::1]:3000", false},
		{"ftp://localhost:3000", false},
		{"not-a-url", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isLocalhostOrigin(tt.origin); got != tt.want {
			t.Errorf("isLocalhostOrigin(%q) = %v, want %v", tt.origin, got, tt.want)
		}
	}
}

func TestLoopbackRedirectURIsIPv6(t *testing.T) {
	if !isAllowedRedirectScheme("http://[::1]:49152/cb") {
		t.Error("http://[::1]:49152/cb should be an allowed redirect scheme (RFC 8252 §7.3)")
	}
	if isAllowedRedirectScheme("http://example.com/cb") {
		t.Error("http on a non-loopback host must not be allowed")
	}
	// Loopback matching ignores the port and matches across loopback hosts
	if !matchRedirectURI("http://[::1]:49152/cb", []string{"http://127.0.0.1:1234/cb"}) {
		t.Error("IPv6 loopback redirect should match a registered IPv4 loopback URI (port-agnostic)")
	}
	if matchRedirectURI("http://[2001:db8::1]:49152/cb", []string{"http://[2001:db8::1]:1234/cb"}) {
		t.Error("non-loopback IPv6 must require an exact match")
	}
}
