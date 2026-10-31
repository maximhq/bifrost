package schemas

import "testing"

func TestMatchEntry(t *testing.T) {
	cases := []struct {
		name  string
		entry string
		value string
		want  bool
	}{
		{"plain exact", "gpt-4o", "gpt-4o", true},
		{"plain case-insensitive", "GPT-4o", "gpt-4O", true},
		{"plain no partial", "gpt-4", "gpt-4o", false},
		{"regex family", "regex:^gpt-4.*", "gpt-4o-mini", true},
		{"regex full match only", "regex:gpt-4", "gpt-4o", false},
		{"regex case-insensitive", "regex:^GPT-4.*", "gpt-4o", true},
		{"regex miss", "regex:^gpt-4.*", "gpt-3.5-turbo", false},
		{"regex unparsable never matches", "regex:(", "(", false},
		{"regex-looking value against a plain entry is exact", "regex:^gpt-4.*", "regex:^gpt-4.*", false},
		{"plain entry never treats value as a pattern", "gpt-4o", ".*", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MatchEntry(tc.entry, tc.value); got != tc.want {
				t.Fatalf("MatchEntry(%q, %q) = %v, want %v", tc.entry, tc.value, got, tc.want)
			}
		})
	}
}

func TestWhiteListRegexEntries(t *testing.T) {
	wl := WhiteList{"gpt-4o", "regex:^claude-3-.*"}
	cases := []struct {
		value string
		want  bool
	}{
		{"gpt-4o", true},
		{"GPT-4O", true},
		{"claude-3-opus-20240229", true},
		{"claude-2", false},
		{"gpt-4o-mini", false},
	}
	for _, tc := range cases {
		if got := wl.IsAllowed(tc.value); got != tc.want {
			t.Errorf("IsAllowed(%q) = %v, want %v", tc.value, got, tc.want)
		}
	}
	if wl.IsUnrestricted() {
		t.Fatal("a list with entries is restricted")
	}
	if err := wl.Validate(); err != nil {
		t.Fatalf("valid list rejected: %v", err)
	}
}

func TestBlackListRegexEntriesWin(t *testing.T) {
	allowed := WhiteList{"regex:^gpt-4.*"}
	blocked := BlackList{"regex:.*-preview$"}
	if !allowed.IsAllowed("gpt-4o-preview") || !blocked.IsBlocked("gpt-4o-preview") {
		t.Fatal("gpt-4o-preview must be allowed by the family and blocked by the suffix")
	}
	if blocked.IsBlocked("gpt-4o") {
		t.Fatal("gpt-4o is not a preview")
	}
	if blocked.IsBlockAll() {
		t.Fatal("a regex block entry is not the wildcard")
	}
}

func TestValidateRegexEntries(t *testing.T) {
	bad := []string{"regex:", "regex:*", "regex:(", "regex:(?<=a)b"}
	for _, entry := range bad {
		if err := (WhiteList{entry}).Validate(); err == nil {
			t.Errorf("WhiteList{%q}.Validate() accepted", entry)
		}
		if err := (BlackList{entry}).Validate(); err == nil {
			t.Errorf("BlackList{%q}.Validate() accepted", entry)
		}
	}
	good := WhiteList{"*"}
	if err := good.Validate(); err != nil {
		t.Fatalf("bare wildcard rejected: %v", err)
	}
	if err := (WhiteList{"regex:.*", "gpt-4o"}).Validate(); err != nil {
		t.Fatalf("a match-all regex next to a name is fine: %v", err)
	}
	if err := (WhiteList{"*", "regex:^gpt.*"}).Validate(); err == nil {
		t.Fatal("the bare wildcard still cannot be mixed with other entries")
	}
	if err := (WhiteList{"regex:^gpt.*", "regex:^GPT.*"}).Validate(); err == nil {
		t.Fatal("duplicate regex entries are still duplicates")
	}
}
