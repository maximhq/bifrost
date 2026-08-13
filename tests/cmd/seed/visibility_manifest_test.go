package seed

import (
	"slices"
	"testing"
)

// TestComputeVisibleTo_ModelsOrgDimensions pins the manifest to the enterprise log
// scope. The org dimensions were missing, so the business-unit-only shape read as
// admin-only and the team reader looked like it saw 30 rows too many.
func TestComputeVisibleTo_ModelsOrgDimensions(t *testing.T) {
	shapes := BuildShapes("e2e")

	byName := make(map[string][]string, len(shapes))
	for _, s := range shapes {
		byName[s.Name] = s.VisibleTo
	}

	visible := func(name, persona string) bool {
		return slices.Contains(byName[name], persona)
	}

	if _, ok := byName["only-business-unit"]; !ok {
		t.Fatalf("expected an only-business-unit shape, got %v", byName)
	}
	if !visible("only-business-unit", "team_reader_tiggings") {
		t.Errorf("only-business-unit must be visible to team_reader_tiggings: %v", byName["only-business-unit"])
	}
	// Own-data personas are not widened by the org dimensions.
	if visible("only-business-unit", "own_reader_tiggings") {
		t.Errorf("only-business-unit must not be visible to own_reader_tiggings: %v", byName["only-business-unit"])
	}
	if visible("only-business-unit", "team_reader_outside") {
		t.Errorf("only-business-unit must not cross to the outside team reader: %v", byName["only-business-unit"])
	}

	// A row with no DAC dimensions at all stays admin-only.
	if got := byName["legacy-unowned"]; len(got) != 1 || got[0] != "all_data_admin" {
		t.Errorf("legacy-unowned visibility: got %v, want [all_data_admin]", got)
	}

	// Derived negative shapes must still match what was previously hardcoded.
	wantOutside := []string{"all_data_admin", "own_reader_outside", "team_reader_outside"}
	for _, name := range []string{"user-not-in-tiggings", "outside-team-virtual-key"} {
		got := slices.Sorted(slices.Values(byName[name]))
		if !slices.Equal(got, wantOutside) {
			t.Errorf("%s visibility: got %v, want %v", name, got, wantOutside)
		}
	}
}

// TestBuildShapes_TeamReaderVisibleCount guards the count the Postman assertion
// compares against: all 15 matrix shapes carry at least one dimension the team
// reader can see.
func TestBuildShapes_TeamReaderVisibleCount(t *testing.T) {
	count := 0
	for _, s := range BuildShapes("e2e") {
		if slices.Contains(s.VisibleTo, "team_reader_tiggings") {
			count++
		}
	}
	if count != 15 {
		t.Errorf("team_reader_tiggings visible shapes: got %d, want 15", count)
	}
}
