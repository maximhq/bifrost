package suggest

import "testing"

func TestLeaksRejectsIdentifiersAndKeepsVocabulary(t *testing.T) {
	cases := []struct {
		phrase string
		leaks  bool
	}{
		{"where is my refund for this order", false},
		{"build a 3-statement financial model for a fundraise", false},
		{"plan a migration from vms to k8s", false},
		{"analyse this a/b test", false},
		{"hello", false},
		{"where is my refund for order 12345", true},
		{"what changed between 4/20 and 5/1", true},
		{"why was i charged $49 twice", true},
		{"forecast a 10% drop in sales", true},
		{"email me at jane@example.org", true},
		{"check https://status.example.com", true},
		{`explain "undefined is not a function"`, true},
		{"fix `git reset` for me", true},
		{"summarise this: i conclude by noting that a name based on hermanubis", true},
		{"line one\nline two", true},
		{"one two three four five six seven eight nine ten eleven twelve thirteen fourteen fifteen sixteen seventeen eighteen nineteen twenty twentyone", true},
	}
	for _, c := range cases {
		if got := leaks(c.phrase); got != c.leaks {
			t.Errorf("leaks(%q) = %v, want %v", c.phrase, got, c.leaks)
		}
	}
}

func TestSanitizeStripsIdentifiersAndPastedTail(t *testing.T) {
	cases := []struct{ in, want string }{
		{"where is my refund for order 12345", "where is my refund for order"},
		{"summarise this: i conclude by noting that a name based on hermanubis", "summarise this"},
		{`explain "undefined" in my build`, "explain undefined in my build"},
		{"12345 67890", ""},
		{"constraints: 1. word limit for short term memory", ""},
	}
	for _, c := range cases {
		got := sanitize(c.in)
		if got != c.want {
			t.Errorf("sanitize(%q) = %q, want %q", c.in, got, c.want)
		}
		if got != "" && leaks(got) {
			t.Errorf("sanitize(%q) = %q still leaks", c.in, got)
		}
	}
}

func TestFilterLeaksSplitsPhrases(t *testing.T) {
	kept, dropped := filterLeaks([]string{"reset my password", "reset password for user 88812"})
	if len(kept) != 1 || kept[0] != "reset my password" {
		t.Fatalf("kept = %v", kept)
	}
	if len(dropped) != 1 {
		t.Fatalf("dropped = %v", dropped)
	}
}
