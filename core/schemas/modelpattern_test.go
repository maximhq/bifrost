package schemas

import (
	"testing"
)

func TestCompileModelPattern(t *testing.T) {
	re1, err := CompileModelPattern("^gpt-4.*")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	re2, err := CompileModelPattern("^gpt-4.*")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if re1 != re2 {
		t.Errorf("expected the cached compiled pattern to be reused")
	}
	for _, bad := range []string{"", "   ", "(", "[a-", "(?<=gpt-)4o"} {
		if _, err := CompileModelPattern(bad); err == nil {
			t.Errorf("CompileModelPattern(%q) expected error", bad)
		}
	}
}

func TestModelPatternListValidate(t *testing.T) {
	good := ModelPatternList{"^gpt-4.*", ".*-preview$", "^(gpt-4o|gpt-4-turbo)$"}
	if err := good.Validate(); err != nil {
		t.Errorf("valid list should pass: %v", err)
	}
	if err := (ModelPatternList{}).Validate(); err != nil {
		t.Errorf("empty list should pass: %v", err)
	}
	bad := map[string]ModelPatternList{
		"empty pattern":    {""},
		"blank pattern":    {"  "},
		"wildcard":         {"*"},
		"unparsable":       {"("},
		"lookbehind":       {"(?<=gpt-)4o"},
		"duplicate":        {"^gpt-4.*", "^gpt-4.*"},
		"mixed with valid": {"^gpt-4.*", "["},
	}
	for name, list := range bad {
		if err := list.Validate(); err == nil {
			t.Errorf("%s: expected error for %v", name, list)
		}
	}
}

func TestModelPatternListMatches(t *testing.T) {
	cases := []struct {
		name     string
		patterns ModelPatternList
		provider string
		model    string
		want     bool
	}{
		{"prefix wildcard", ModelPatternList{"^gpt-4.*"}, "", "gpt-4o", true},
		{"case-insensitive", ModelPatternList{"^gpt-4.*"}, "", "GPT-4-TURBO", true},
		{"full match only", ModelPatternList{"gpt-4"}, "", "gpt-4o", false},
		{"exact full match", ModelPatternList{"gpt-4"}, "", "GPT-4", true},
		{"suffix", ModelPatternList{".*-preview$"}, "", "gpt-4o-preview", true},
		{"no match", ModelPatternList{"^claude.*"}, "", "gpt-4o", false},
		{"alternation", ModelPatternList{"^(gpt-4o|gpt-4-turbo)$"}, "", "gpt-4-turbo", true},
		{"provider qualified", ModelPatternList{"^openai/gpt-4o$"}, "openai", "gpt-4o", true},
		{"provider qualified wrong provider", ModelPatternList{"^openai/gpt-4o$"}, "anthropic", "gpt-4o", false},
		{"provider qualified without provider", ModelPatternList{"^openai/gpt-4o$"}, "", "gpt-4o", false},
		{"bare pattern with provider", ModelPatternList{"^gpt-4.*"}, "openai", "gpt-4o", true},
		{"empty list", ModelPatternList{}, "openai", "gpt-4o", false},
		{"invalid never matches", ModelPatternList{"("}, "openai", "(", false},
		{"second pattern wins", ModelPatternList{"^claude.*", "^gpt.*"}, "", "gpt-4o", true},
	}
	for _, tc := range cases {
		if got := tc.patterns.Matches(tc.provider, tc.model); got != tc.want {
			t.Errorf("%s: Matches(%q, %q) = %v, want %v", tc.name, tc.provider, tc.model, got, tc.want)
		}
	}
}

func TestModelAccessRule(t *testing.T) {
	cases := []struct {
		name  string
		rule  ModelAccessRule
		model string
		want  bool
	}{
		{"wildcard admits", ModelAccessRule{Allowed: WhiteList{"*"}}, "gpt-4o", true},
		{"deny by default", ModelAccessRule{}, "gpt-4o", false},
		{"exact admits", ModelAccessRule{Allowed: WhiteList{"gpt-4o"}}, "GPT-4O", true},
		{"exact does not evaluate regex syntax", ModelAccessRule{Allowed: WhiteList{"regex:^gpt-4.*"}}, "gpt-4o", false},
		{"exact regex-looking literal matches itself", ModelAccessRule{Allowed: WhiteList{"regex:^gpt-4.*"}}, "regex:^gpt-4.*", true},
		{"pattern admits", ModelAccessRule{AllowedPatterns: ModelPatternList{"^gpt-4.*"}}, "gpt-4o", true},
		{"pattern is anchored", ModelAccessRule{AllowedPatterns: ModelPatternList{"gpt-4"}}, "gpt-4o", false},
		{"exact block wins", ModelAccessRule{Allowed: WhiteList{"*"}, Blocked: BlackList{"gpt-4o"}}, "gpt-4o", false},
		{"pattern block wins over exact allow", ModelAccessRule{Allowed: WhiteList{"gpt-4o-preview"}, BlockedPatterns: ModelPatternList{".*-preview$"}}, "gpt-4o-preview", false},
		{"pattern block wins over pattern allow", ModelAccessRule{AllowedPatterns: ModelPatternList{"^gpt-4.*"}, BlockedPatterns: ModelPatternList{".*-preview$"}}, "gpt-4o-preview", false},
		{"block all", ModelAccessRule{Allowed: WhiteList{"*"}, Blocked: BlackList{"*"}}, "gpt-4o", false},
		{"mixed allow: literal", ModelAccessRule{Allowed: WhiteList{"claude-3"}, AllowedPatterns: ModelPatternList{"^o[0-9].*"}}, "claude-3", true},
		{"mixed allow: pattern", ModelAccessRule{Allowed: WhiteList{"claude-3"}, AllowedPatterns: ModelPatternList{"^o[0-9].*"}}, "o3-mini", true},
		{"mixed allow: neither", ModelAccessRule{Allowed: WhiteList{"claude-3"}, AllowedPatterns: ModelPatternList{"^o[0-9].*"}}, "gpt-4o", false},
	}
	for _, tc := range cases {
		if got := tc.rule.Allows("openai", tc.model); got != tc.want {
			t.Errorf("%s: Allows(%q) = %v, want %v", tc.name, tc.model, got, tc.want)
		}
	}

	provQualified := ModelAccessRule{AllowedPatterns: ModelPatternList{"^openai/gpt-4o$"}}
	if !provQualified.Allows("openai", "gpt-4o") {
		t.Errorf("provider-qualified pattern should admit openai/gpt-4o")
	}
	if provQualified.Allows("anthropic", "gpt-4o") {
		t.Errorf("provider-qualified pattern should not admit anthropic/gpt-4o")
	}
}

func TestModelAccessRuleDeniesAll(t *testing.T) {
	if !(ModelAccessRule{}).DeniesAll() {
		t.Errorf("empty rule should deny all")
	}
	if (ModelAccessRule{AllowedPatterns: ModelPatternList{"^gpt.*"}}).DeniesAll() {
		t.Errorf("patterns-only rule should not deny all")
	}
	if (ModelAccessRule{Allowed: WhiteList{"gpt-4o"}}).DeniesAll() {
		t.Errorf("exact allow should not deny all")
	}
	if !(ModelAccessRule{Allowed: WhiteList{"*"}, Blocked: BlackList{"*"}}).DeniesAll() {
		t.Errorf("block all should deny all")
	}
}

func TestModelAccessRuleValidate(t *testing.T) {
	ok := ModelAccessRule{Allowed: WhiteList{"gpt-4o"}, Blocked: BlackList{"gpt-4o-preview"}, AllowedPatterns: ModelPatternList{"^gpt-4.*"}, BlockedPatterns: ModelPatternList{".*-preview$"}}
	if err := ok.Validate(); err != nil {
		t.Errorf("valid rule should pass: %v", err)
	}
	if err := (ModelAccessRule{AllowedPatterns: ModelPatternList{"("}}).Validate(); err == nil {
		t.Errorf("invalid allow pattern should fail")
	}
	if err := (ModelAccessRule{BlockedPatterns: ModelPatternList{"*"}}).Validate(); err == nil {
		t.Errorf("wildcard block pattern should fail")
	}
	if err := (ModelAccessRule{Allowed: WhiteList{"*", "gpt-4o"}}).Validate(); err == nil {
		t.Errorf("mixed wildcard allow list should fail")
	}
	// A regex-looking literal is an ordinary entry in the exact lists.
	if err := (ModelAccessRule{Allowed: WhiteList{"regex:("}}).Validate(); err != nil {
		t.Errorf("regex-looking literal should be accepted as an exact entry: %v", err)
	}
}

func TestWhiteListBlackListExactOnly(t *testing.T) {
	wl := WhiteList{"gpt-4o", "regex:^gpt-4.*"}
	if !wl.IsAllowed("GPT-4O") {
		t.Errorf("exact entries match case-insensitively")
	}
	if wl.IsAllowed("gpt-4-turbo") {
		t.Errorf("a regex-looking literal must not be evaluated as a pattern")
	}
	if !wl.IsAllowed("regex:^gpt-4.*") {
		t.Errorf("a regex-looking literal matches itself")
	}
	bl := BlackList{".*-preview$"}
	if bl.IsBlocked("gpt-4o-preview") {
		t.Errorf("a regex-looking literal must not block by pattern")
	}
	if !bl.IsBlocked(".*-preview$") {
		t.Errorf("a regex-looking literal blocks itself")
	}
}

func TestKeyAndPermitModelAccess(t *testing.T) {
	k := Key{Models: WhiteList{"gpt-4o"}, ModelsPatterns: ModelPatternList{"^o[0-9].*"}, BlacklistedModelsPatterns: ModelPatternList{".*-mini$"}}
	if !k.ModelAccess().Allows("openai", "o3") || k.ModelAccess().Allows("openai", "o3-mini") || !k.ModelAccess().Allows("openai", "gpt-4o") {
		t.Errorf("key rule should compose exact and pattern lists")
	}
	pp := ProviderPermit{Provider: "openai", AllowedModels: WhiteList{"*"}, BlacklistedModels: BlackList{"gpt-3.5-turbo"}, BlacklistedModelsPatterns: ModelPatternList{"^openai/.*-preview$"}}
	r := pp.ModelAccess()
	if !r.Allows("openai", "gpt-4o") || r.Allows("openai", "gpt-3.5-turbo") || r.Allows("openai", "gpt-4o-preview") {
		t.Errorf("permit rule should compose exact and pattern lists")
	}
}
