package utils

import (
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// TestDecisionProbabilitiesFromKeysLabelsScoreLevels pins that a score entry
// carries its level's label, and that a level written without one is labelled
// by its index, as DecisionLevel documents.
func TestDecisionProbabilitiesFromKeysLabelsScoreLevels(t *testing.T) {
	question := schemas.DecisionQuestion{
		Type: schemas.DecisionTypeScore,
		Levels: []schemas.DecisionLevel{
			{Description: schemas.NewDecisionText("can wait")},
			{Label: "high", Description: schemas.NewDecisionText("today")},
		},
	}
	got := DecisionProbabilitiesFromKeys(question, map[string]float64{"0": 0.25, "1": 0.75})
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2", len(got))
	}
	for i, want := range []string{"0", "high"} {
		if got[i].Label == nil || *got[i].Label != want {
			t.Errorf("entry %d label = %v, want %q", i, got[i].Label, want)
		}
		if got[i].Value.Num == nil || *got[i].Value.Num != float64(i) {
			t.Errorf("entry %d value = %+v, want %d", i, got[i].Value, i)
		}
	}
}
