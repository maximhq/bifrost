package handlers

import (
	"testing"
)

func TestMatchesWildcardPattern(t *testing.T) {
	tests := []struct {
		name    string
		origin  string
		pattern string
		want    bool
	}{
		// Basic subdomain wildcard
		{"subdomain match", "https://app.example.com", "https://*.example.com", true},
		{"subdomain no match", "https://app.other.com", "https://*.example.com", false},

		// Scheme-less wildcard
		{"scheme-less no match with scheme prefix", "https://api.mysite.io", "*.mysite.io", false},
		{"scheme-less exact subdomain", "sub.mysite.io", "*.mysite.io", true},

		// Wildcard should not match dots (no nested subdomains)
		{"no nested subdomain", "https://a.b.example.com", "https://*.example.com", false},

		// Wildcard must match at least one character
		{"empty subdomain no match", "https://.example.com", "https://*.example.com", false},

		// Exact match (no wildcard) should not match
		{"no wildcard in pattern", "https://app.example.com", "https://app.example.com", true},

		// Different schemes
		{"http vs https", "http://app.example.com", "https://*.example.com", false},

		// Wildcard with http
		{"http wildcard", "http://app.example.com", "http://*.example.com", true},

		// Multiple wildcards
		{"multiple wildcards", "https://a.b.example.com", "https://*.*.example.com", true},

		// Empty inputs
		{"empty origin", "", "https://*.example.com", false},
		{"empty pattern", "https://app.example.com", "", false},
		{"both empty", "", "", true}, // ^$ matches ""

		// Wildcard should not match slashes
		{"no slash in wildcard", "https://app/example.com", "https://*.example.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesWildcardPattern(tt.origin, tt.pattern)
			if got != tt.want {
				t.Errorf("matchesWildcardPattern(%q, %q) = %v, want %v", tt.origin, tt.pattern, got, tt.want)
			}
		})
	}
}

func TestMatchesWildcardPattern_CacheConsistency(t *testing.T) {
	pattern := "https://*.cached-test.com"

	// First call populates the cache
	r1 := matchesWildcardPattern("https://a.cached-test.com", pattern)
	// Second call should hit the cache and produce the same result
	r2 := matchesWildcardPattern("https://a.cached-test.com", pattern)
	// Different origin, same pattern — should also use cached regexp
	r3 := matchesWildcardPattern("https://b.cached-test.com", pattern)
	r4 := matchesWildcardPattern("https://no.other.com", pattern)

	if !r1 || !r2 || !r3 {
		t.Errorf("expected matches to succeed, got r1=%v r2=%v r3=%v", r1, r2, r3)
	}
	if r4 {
		t.Error("expected non-matching origin to fail")
	}
}

func TestIsOriginAllowed(t *testing.T) {
	tests := []struct {
		name           string
		origin         string
		allowedOrigins []string
		want           bool
	}{
		// Localhost always allowed
		{"localhost http", "http://localhost:3000", nil, true},
		{"localhost https", "https://localhost:8080", nil, true},
		{"127.0.0.1", "http://127.0.0.1:3000", nil, true},

		// Exact match
		{"exact match", "https://app.example.com", []string{"https://app.example.com"}, true},
		{"exact no match", "https://other.com", []string{"https://app.example.com"}, false},

		// Global wildcard
		{"star allows all", "https://anything.com", []string{"*"}, true},

		// Wildcard patterns
		{"wildcard match", "https://sub.example.com", []string{"https://*.example.com"}, true},
		{"wildcard no match", "https://sub.other.com", []string{"https://*.example.com"}, false},

		// Mixed
		{"exact before wildcard", "https://specific.com", []string{"https://specific.com", "https://*.example.com"}, true},
		{"wildcard after exact miss", "https://sub.example.com", []string{"https://specific.com", "https://*.example.com"}, true},

		// Empty
		{"empty origins", "https://app.com", nil, false},
		{"empty origins slice", "https://app.com", []string{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsOriginAllowed(tt.origin, tt.allowedOrigins)
			if got != tt.want {
				t.Errorf("IsOriginAllowed(%q, %v) = %v, want %v", tt.origin, tt.allowedOrigins, got, tt.want)
			}
		})
	}
}
