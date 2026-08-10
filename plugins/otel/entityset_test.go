package otel

import (
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

func TestGetStringSliceAttr_AnyPreservesIndex(t *testing.T) {
	got := getStringSliceAttr(map[string]any{"k": []any{"a", 1, "c"}}, "k")
	want := []string{"a", "", "c"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("index %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// A non-string ID element must not shift the aligned names: the entry with the
// bad ID is dropped along with its name, and remaining ids keep their own names.
func TestEntitySetFromAttrs_MixedAnyKeepsAlignment(t *testing.T) {
	attrs := map[string]any{
		schemas.AttrBifrostTeamIDs:   []any{"a1", 42, "c3"},
		schemas.AttrBifrostTeamNames: []any{"Alpha", "Bogus", "Gamma"},
	}
	ids, names := entitySetFromAttrs(attrs, schemas.AttrBifrostTeamIDs, schemas.AttrBifrostTeamNames, schemas.AttrTeamID, schemas.AttrTeamName)
	if ids != "a1,c3" || names != "Alpha,Gamma" {
		t.Errorf("got (%q,%q), want (\"a1,c3\",\"Alpha,Gamma\")", ids, names)
	}
}
