package schemas

import "testing"

func TestSanitizeDimensionLabel(t *testing.T) {
	t.Parallel()
	if got := SanitizeDimensionLabel("Team"); got != "team" {
		t.Errorf("SanitizeDimensionLabel(Team) = %q, want team", got)
	}
	if got := SanitizeDimensionLabel("weird name!"); got != "weird_name_" {
		t.Errorf("got %q", got)
	}
	if got := SanitizeDimensionLabel(""); got != "dimension" {
		t.Errorf("empty: got %q", got)
	}
}

func TestDimensionAttrKey(t *testing.T) {
	t.Parallel()
	if got := DimensionAttrKey("environment"); got != AttrGenAIDimensionPrefix+"environment" {
		t.Errorf("got %q", got)
	}
}
