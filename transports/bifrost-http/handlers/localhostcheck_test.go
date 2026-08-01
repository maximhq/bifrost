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

func TestPrivateUseRedirectSchemes(t *testing.T) {
	tests := []struct {
		uri  string
		want bool
	}{
		// Allowlisted private-use scheme used by native apps (RFC 8252 §7.1).
		{"cursor://anysphere.cursor-mcp/oauth/callback", true},
		// https / loopback still allowed.
		{"https://example.com/cb", true},
		{"http://127.0.0.1:49152/cb", true},
		// Default-deny: custom schemes not on the allowlist are rejected.
		{"com.example.app://oauth/callback", false},
		{"myapp://callback", false},
		{"vscode://callback", false},
		// Dangerous / opaque schemes must stay rejected.
		{"javascript:alert(1)", false},
		{"javascript://alert(1)", false},
		{"data:text/html,<script>alert(1)</script>", false},
		{"file:///etc/passwd", false},
		{"http://example.com/cb", false},  // http on non-loopback
		{"cursor:oauth/callback", false},  // allowlisted scheme but no authority
		{"cursor:", false},                // scheme only
		{"", false},
	}
	for _, tt := range tests {
		if got := isAllowedRedirectScheme(tt.uri); got != tt.want {
			t.Errorf("isAllowedRedirectScheme(%q) = %v, want %v", tt.uri, got, tt.want)
		}
	}
}

func TestMatchRedirectURIPrivateUseSchemes(t *testing.T) {
	registered := []string{"cursor://anysphere.cursor-mcp/oauth/callback"}
	tests := []struct {
		candidate string
		want      bool
	}{
		// Exact match on a registered private-use URI.
		{"cursor://anysphere.cursor-mcp/oauth/callback", true},
		// Different path or host must not match.
		{"cursor://anysphere.cursor-mcp/oauth/other", false},
		{"cursor://evil.example/oauth/callback", false},
		// Private-use hosts are not loopback, so the RFC 8252 §7.3 any-port
		// leniency must not apply — a port makes it a different exact string.
		{"cursor://anysphere.cursor-mcp:49152/oauth/callback", false},
		// Scheme mismatch against the registration.
		{"https://anysphere.cursor-mcp/oauth/callback", false},
	}
	for _, tt := range tests {
		if got := matchRedirectURI(tt.candidate, registered); got != tt.want {
			t.Errorf("matchRedirectURI(%q) = %v, want %v", tt.candidate, got, tt.want)
		}
	}
}
