package tables

import (
	"strings"
	"testing"
)

// TestLegacyRequestLimitID pins the derived ID: the plain suffix when it fits the column, and a cut,
// hashed form that stays within it, stays distinct per source ID and never changes, for longer ones.
func TestLegacyRequestLimitID(t *testing.T) {
	if got := LegacyRequestLimitID("rl-a"); got != "rl-a-requests" {
		t.Fatalf("short id = %q", got)
	}
	fits := strings.Repeat("a", maxRateLimitIDLength-len(legacyRequestLimitSuffix))
	if got := LegacyRequestLimitID(fits); got != fits+legacyRequestLimitSuffix || len(got) != maxRateLimitIDLength {
		t.Fatalf("an id that just fits keeps the plain suffix, got length %d", len(got))
	}
	for _, id := range []string{
		strings.Repeat("a", maxRateLimitIDLength-len(legacyRequestLimitSuffix)+1),
		strings.Repeat("a", maxRateLimitIDLength),
		strings.Repeat("é", maxRateLimitIDLength), // multi-byte: never cut mid-rune
	} {
		got := LegacyRequestLimitID(id)
		if len(got) > maxRateLimitIDLength {
			t.Fatalf("derived id is %d bytes for a %d-byte source", len(got), len(id))
		}
		if !strings.HasSuffix(got, legacyRequestLimitSuffix) || got == id+legacyRequestLimitSuffix {
			t.Fatalf("derived id %q is not the cut form", got)
		}
		if got != LegacyRequestLimitID(id) {
			t.Fatalf("derived id is not deterministic")
		}
		if strings.ToValidUTF8(got, "") != got {
			t.Fatalf("derived id is not valid UTF-8")
		}
	}
	a := LegacyRequestLimitID(strings.Repeat("a", 300) + "x")
	b := LegacyRequestLimitID(strings.Repeat("a", 300) + "y")
	if a == b {
		t.Fatalf("two long ids sharing a prefix collapsed to %q", a)
	}
}
