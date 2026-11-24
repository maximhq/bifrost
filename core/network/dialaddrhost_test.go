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
		if got := DialAddrHost(tt.addr); got != tt.want {
			t.Errorf("DialAddrHost(%q) = %q, want %q", tt.addr, got, tt.want)
		}
	}
}

func TestNoProxyBypassIPv6(t *testing.T) {
	// An IPv6 literal listed in no_proxy must match after host extraction
	if !shouldBypassProxy(DialAddrHost("[::1]:8080"), "::1") {
		t.Error("[::1]:8080 should bypass proxy when no_proxy contains ::1")
	}
	if shouldBypassProxy(DialAddrHost("[2001:db8::1]:443"), "::1") {
		t.Error("non-listed IPv6 target must not bypass proxy")
	}
}

// TestMatchesNoProxyList pins that every entry of a comma-separated no_proxy list is
// honoured. The fasthttp factory path used to hand the whole list to shouldBypassProxy
// as one pattern, so "a.example,b.example" bypassed neither host.
func TestMatchesNoProxyList(t *testing.T) {
	list := "bedrock-runtime.us-east-1.amazonaws.com, .vpce.amazonaws.com,*.internal.corp"
	tests := map[string]bool{
		"bedrock-runtime.us-east-1.amazonaws.com":                true,
		"vpce-0abc.bedrock-runtime.us-east-1.vpce.amazonaws.com": true,
		"svc.internal.corp":                     true,
		"internal.corp":                         false,
		"us-central1-aiplatform.googleapis.com": false,
	}
	for host, want := range tests {
		if got := MatchesNoProxy(host, list); got != want {
			t.Errorf("MatchesNoProxy(%q) = %v, want %v", host, got, want)
		}
	}
	if MatchesNoProxy("anything.example", "") {
		t.Error("an empty list must match nothing")
	}
}
