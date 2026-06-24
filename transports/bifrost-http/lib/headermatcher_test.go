package lib

import (
	"testing"

	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

func TestHeaderMatchesPattern(t *testing.T) {
	tests := []struct {
		pattern    string
		headerName string
		want       bool
	}{
		// Exact match
		{"anthropic-beta", "anthropic-beta", true},
		{"anthropic-beta", "anthropic-alpha", false},

		// Case insensitive exact match
		{"Anthropic-Beta", "anthropic-beta", true},
		{"anthropic-beta", "Anthropic-Beta", true},

		// Star matches all
		{"*", "anything", true},
		{"*", "", true},

		// Prefix wildcard
		{"anthropic-*", "anthropic-beta", true},
		{"anthropic-*", "anthropic-version", true},
		{"anthropic-*", "anthropic-", true},
		{"anthropic-*", "openai-version", false},
		{"anthropic-*", "anthropic", false},

		// Case insensitive prefix wildcard
		{"Anthropic-*", "anthropic-beta", true},
		{"anthropic-*", "Anthropic-Beta", true},

		// No match
		{"foo", "bar", false},
		{"", "foo", false},

		// Pattern without wildcard doesn't prefix match
		{"anthropic-", "anthropic-beta", false},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"_"+tt.headerName, func(t *testing.T) {
			got := HeaderMatchesPattern(tt.pattern, tt.headerName)
			if got != tt.want {
				t.Errorf("HeaderMatchesPattern(%q, %q) = %v, want %v", tt.pattern, tt.headerName, got, tt.want)
			}
		})
	}
}

func TestNewHeaderMatcher_Nil(t *testing.T) {
	m := NewHeaderMatcher(nil)
	if m != nil {
		t.Fatal("expected nil matcher for nil config")
	}
	// nil matcher should allow everything
	if !m.ShouldAllow("anything") {
		t.Error("nil matcher should allow all headers")
	}
	if m.HasAllowlist() {
		t.Error("nil matcher should have no allowlist")
	}
}

func TestNewHeaderMatcher_Empty(t *testing.T) {
	m := NewHeaderMatcher(&configstoreTables.GlobalHeaderFilterConfig{})
	if m == nil {
		t.Fatal("expected non-nil matcher for empty config")
	}
	if m.HasAllowlist() {
		t.Error("empty config should have no allowlist")
	}
	if !m.ShouldAllow("anything") {
		t.Error("empty config should allow all headers")
	}
}

func TestHeaderMatcher_ExactAllowlist(t *testing.T) {
	m := NewHeaderMatcher(&configstoreTables.GlobalHeaderFilterConfig{
		Allowlist: []string{"anthropic-beta", "custom-id"},
	})
	if !m.ShouldAllow("anthropic-beta") {
		t.Error("should allow anthropic-beta")
	}
	if !m.ShouldAllow("custom-id") {
		t.Error("should allow custom-id")
	}
	if m.ShouldAllow("openai-version") {
		t.Error("should not allow openai-version")
	}
	// Case insensitive
	if !m.ShouldAllow("Anthropic-Beta") {
		t.Error("should allow Anthropic-Beta (case insensitive)")
	}
}

func TestHeaderMatcher_WildcardAllowlist(t *testing.T) {
	m := NewHeaderMatcher(&configstoreTables.GlobalHeaderFilterConfig{
		Allowlist: []string{"anthropic-*"},
	})
	if !m.ShouldAllow("anthropic-beta") {
		t.Error("should allow anthropic-beta")
	}
	if !m.ShouldAllow("anthropic-version") {
		t.Error("should allow anthropic-version")
	}
	if m.ShouldAllow("openai-version") {
		t.Error("should not allow openai-version")
	}
}

func TestHeaderMatcher_StarAllowlist(t *testing.T) {
	m := NewHeaderMatcher(&configstoreTables.GlobalHeaderFilterConfig{
		Allowlist: []string{"*"},
	})
	if !m.ShouldAllow("anything") {
		t.Error("* should allow anything")
	}
	if !m.ShouldAllow("") {
		t.Error("* should allow empty string")
	}
}

func TestHeaderMatcher_ExactDenylist(t *testing.T) {
	m := NewHeaderMatcher(&configstoreTables.GlobalHeaderFilterConfig{
		Denylist: []string{"secret-token"},
	})
	if m.ShouldAllow("secret-token") {
		t.Error("should deny secret-token")
	}
	if !m.ShouldAllow("public-key") {
		t.Error("should allow public-key")
	}
}

func TestHeaderMatcher_WildcardDenylist(t *testing.T) {
	m := NewHeaderMatcher(&configstoreTables.GlobalHeaderFilterConfig{
		Denylist: []string{"x-internal-*"},
	})
	if m.ShouldAllow("x-internal-id") {
		t.Error("should deny x-internal-id")
	}
	if m.ShouldAllow("x-internal-secret") {
		t.Error("should deny x-internal-secret")
	}
	if !m.ShouldAllow("x-external-id") {
		t.Error("should allow x-external-id")
	}
}

func TestHeaderMatcher_StarDenylist(t *testing.T) {
	m := NewHeaderMatcher(&configstoreTables.GlobalHeaderFilterConfig{
		Denylist: []string{"*"},
	})
	if m.ShouldAllow("anything") {
		t.Error("* denylist should deny everything")
	}
}

func TestHeaderMatcher_AllowlistWithDenylist(t *testing.T) {
	m := NewHeaderMatcher(&configstoreTables.GlobalHeaderFilterConfig{
		Allowlist: []string{"*"},
		Denylist:  []string{"x-internal-*"},
	})
	if !m.ShouldAllow("anthropic-beta") {
		t.Error("should allow anthropic-beta")
	}
	if m.ShouldAllow("x-internal-id") {
		t.Error("should deny x-internal-id (denylist overrides)")
	}
}

func TestHeaderMatcher_AllowlistPrefixWithDenylistExact(t *testing.T) {
	m := NewHeaderMatcher(&configstoreTables.GlobalHeaderFilterConfig{
		Allowlist: []string{"anthropic-*"},
		Denylist:  []string{"anthropic-dangerous"},
	})
	if !m.ShouldAllow("anthropic-beta") {
		t.Error("should allow anthropic-beta")
	}
	if m.ShouldAllow("anthropic-dangerous") {
		t.Error("should deny anthropic-dangerous")
	}
	if m.ShouldAllow("openai-version") {
		t.Error("should not allow openai-version (not in allowlist)")
	}
}

func TestHeaderMatcher_CaseInsensitive(t *testing.T) {
	m := NewHeaderMatcher(&configstoreTables.GlobalHeaderFilterConfig{
		Allowlist: []string{"Anthropic-*"},
		Denylist:  []string{"X-Internal-*"},
	})
	if !m.ShouldAllow("anthropic-beta") {
		t.Error("should allow anthropic-beta (case insensitive)")
	}
	if m.ShouldAllow("x-internal-id") {
		t.Error("should deny x-internal-id (case insensitive)")
	}
}

func TestHeaderMatcher_MatchesAllow(t *testing.T) {
	m := NewHeaderMatcher(&configstoreTables.GlobalHeaderFilterConfig{
		Allowlist: []string{"anthropic-*", "custom-id"},
	})
	if !m.MatchesAllow("anthropic-beta") {
		t.Error("should match anthropic-beta")
	}
	if !m.MatchesAllow("custom-id") {
		t.Error("should match custom-id")
	}
	if m.MatchesAllow("openai-version") {
		t.Error("should not match openai-version")
	}
}

func TestHeaderMatcher_MatchesDeny(t *testing.T) {
	m := NewHeaderMatcher(&configstoreTables.GlobalHeaderFilterConfig{
		Denylist: []string{"secret-*", "blocked"},
	})
	if !m.MatchesDeny("secret-token") {
		t.Error("should match secret-token")
	}
	if !m.MatchesDeny("blocked") {
		t.Error("should match blocked")
	}
	if m.MatchesDeny("allowed") {
		t.Error("should not match allowed")
	}
}

func TestHeaderMatcher_HasAllowlist(t *testing.T) {
	m := NewHeaderMatcher(&configstoreTables.GlobalHeaderFilterConfig{
		Allowlist: []string{"foo"},
	})
	if !m.HasAllowlist() {
		t.Error("should have allowlist")
	}

	m2 := NewHeaderMatcher(&configstoreTables.GlobalHeaderFilterConfig{
		Denylist: []string{"bar"},
	})
	if m2.HasAllowlist() {
		t.Error("should not have allowlist")
	}
}
