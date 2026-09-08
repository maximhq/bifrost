package schemas

import (
	"testing"
)

func TestIsRegexEntry(t *testing.T) {
	cases := map[string]bool{
		"regex:^gpt-4.*":     true,
		"regex:":             true,
		"gpt-4o":             false,
		"*":                  false,
		"REGEX:^gpt-4.*":     false,
		"openai/regex:x":     false,
		"regex: ^gpt-4.*":    true,
		"regexp:^gpt-4.*":    false,
		"  regex:^gpt-4.*":   false,
		"regex:openai/gpt.*": true,
	}
	for in, want := range cases {
		if got := IsRegexEntry(in); got != want {
			t.Errorf("IsRegexEntry(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestCompileModelPattern(t *testing.T) {
	re1, err := CompileModelPattern("regex:^gpt-4.*")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	re2, err := CompileModelPattern("regex:^gpt-4.*")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if re1 != re2 {
		t.Errorf("expected the cached compiled pattern to be reused")
	}

	for _, bad := range []string{"regex:", "regex:   ", "regex:(", "regex:[a-", "gpt-4o"} {
		if _, err := CompileModelPattern(bad); err == nil {
			t.Errorf("CompileModelPattern(%q) expected error", bad)
		}
	}
}

func TestValidateModelEntry(t *testing.T) {
	if err := ValidateModelEntry("gpt-4o"); err != nil {
		t.Errorf("literal entry should validate: %v", err)
	}
	if err := ValidateModelEntry("*"); err != nil {
		t.Errorf("wildcard should validate: %v", err)
	}
	if err := ValidateModelEntry("regex:^claude-3-.*"); err != nil {
		t.Errorf("valid regex should validate: %v", err)
	}
	if err := ValidateModelEntry("regex:("); err == nil {
		t.Errorf("invalid regex should fail")
	}
	if err := ValidateModelEntry("regex:"); err == nil {
		t.Errorf("empty regex should fail")
	}
}

func TestMatchesEntry(t *testing.T) {
	tests := []struct {
		entry, model, provider string
		want                   bool
	}{
		// literal
		{"gpt-4o", "gpt-4o", "", true},
		{"GPT-4O", "gpt-4o", "", true},
		{"gpt-4o", "gpt-4o-mini", "", false},
		// anchoring: pattern must cover the whole name
		{"regex:gpt-4", "gpt-4", "", true},
		{"regex:gpt-4", "gpt-4o", "", false},
		{"regex:gpt-4.*", "gpt-4o-mini", "", true},
		{"regex:.*-preview$", "gpt-4o-preview", "", true},
		{"regex:.*-preview$", "gpt-4o-preview-2", "", false},
		// case-insensitive
		{"regex:^claude-3-.*", "Claude-3-Opus", "", true},
		// provider-qualified
		{"regex:openai/gpt-4.*", "gpt-4o", "openai", true},
		{"regex:openai/gpt-4.*", "gpt-4o", "anthropic", false},
		{"regex:openai/gpt-4.*", "gpt-4o", "", false},
		{"regex:(openai|azure)/gpt-4.*", "gpt-4o", "azure", true},
		// invalid regex never matches and never panics
		{"regex:(", "anything", "", false},
		{"regex:", "anything", "", false},
	}
	for _, tt := range tests {
		if got := MatchesEntry(tt.entry, tt.model, tt.provider); got != tt.want {
			t.Errorf("MatchesEntry(%q, %q, %q) = %v, want %v", tt.entry, tt.model, tt.provider, got, tt.want)
		}
	}
}

func TestWhiteListRegex(t *testing.T) {
	wl := WhiteList{"gpt-4o", "regex:^claude-3-.*"}

	if !wl.IsAllowed("gpt-4o") || !wl.IsAllowed("GPT-4o") {
		t.Errorf("literal entry should be allowed")
	}
	if !wl.IsAllowed("claude-3-opus") {
		t.Errorf("regex entry should allow matching model")
	}
	if wl.IsAllowed("claude-2") {
		t.Errorf("regex entry should not allow non-matching model")
	}
	if wl.Contains("claude-3-opus") {
		t.Errorf("Contains must stay literal")
	}
	if !wl.Contains("regex:^claude-3-.*") {
		t.Errorf("Contains should find the raw regex entry")
	}
	if got := wl.LiteralEntries(); len(got) != 1 || got[0] != "gpt-4o" {
		t.Errorf("LiteralEntries = %v, want [gpt-4o]", got)
	}

	pw := WhiteList{"regex:anthropic/claude-.*"}
	if !pw.AllowsModel("anthropic", "claude-3-opus") {
		t.Errorf("provider-qualified regex should allow via provider/model")
	}
	if pw.AllowsModel("openai", "claude-3-opus") {
		t.Errorf("provider-qualified regex should not allow a different provider")
	}
	if !(WhiteList{"*"}).AllowsModel("openai", "anything") {
		t.Errorf("wildcard should allow all")
	}
	if (WhiteList{}).AllowsModel("openai", "anything") {
		t.Errorf("empty list should deny")
	}
}

func TestWhiteListValidateRegex(t *testing.T) {
	if err := (WhiteList{"gpt-4o", "regex:^claude-3-.*"}).Validate(); err != nil {
		t.Errorf("valid list should pass: %v", err)
	}
	if err := (WhiteList{"*", "regex:^claude-3-.*"}).Validate(); err == nil {
		t.Errorf("wildcard mixed with regex should fail")
	}
	if err := (WhiteList{"regex:("}).Validate(); err == nil {
		t.Errorf("invalid regex should fail")
	}
	if err := (WhiteList{"regex:"}).Validate(); err == nil {
		t.Errorf("empty regex should fail")
	}
	if err := (WhiteList{"regex:^gpt.*", "regex:^gpt.*"}).Validate(); err == nil {
		t.Errorf("duplicate regex should fail")
	}
}

func TestBlackListRegex(t *testing.T) {
	bl := BlackList{"gpt-4o", "regex:.*-preview$"}
	if !bl.IsBlocked("gpt-4o") {
		t.Errorf("literal entry should be blocked")
	}
	if !bl.IsBlocked("gpt-4o-preview") {
		t.Errorf("regex entry should block matching model")
	}
	if bl.IsBlocked("gpt-4o-mini") {
		t.Errorf("non-matching model should not be blocked")
	}
	if bl.Contains("gpt-4o-preview") {
		t.Errorf("Contains must stay literal")
	}
	if !(BlackList{"regex:openai/.*"}).BlocksModel("openai", "gpt-4o") {
		t.Errorf("provider-qualified regex should block via provider/model")
	}
	if (BlackList{"regex:openai/.*"}).BlocksModel("anthropic", "gpt-4o") {
		t.Errorf("provider-qualified regex should not block another provider")
	}
	if !(BlackList{"*"}).BlocksModel("openai", "x") {
		t.Errorf("block-all should block")
	}
	if err := (BlackList{"regex:("}).Validate(); err == nil {
		t.Errorf("invalid regex should fail validation")
	}
	if err := (BlackList{"*", "regex:x"}).Validate(); err == nil {
		t.Errorf("wildcard mixed with regex should fail validation")
	}
}
