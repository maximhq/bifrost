package configstore

import (
	"slices"
	"strings"
	"testing"
)

func TestDefaultComplexityExemplars(t *testing.T) {
	exemplars := DefaultComplexityExemplars()
	tiers := []struct {
		name   string
		values []string
	}{
		{name: "simple", values: exemplars.SimpleKeywords},
		{name: "medium", values: exemplars.MediumKeywords},
		{name: "complex", values: exemplars.ComplexKeywords},
	}

	seen := make(map[string]string, 150)
	for _, tier := range tiers {
		if len(tier.values) != 50 {
			t.Fatalf("%s has %d default exemplars, want 50", tier.name, len(tier.values))
		}
		for index, value := range tier.values {
			normalized := strings.ToLower(strings.Join(strings.Fields(value), " "))
			if normalized == "" {
				t.Fatalf("%s exemplar %d is empty", tier.name, index)
			}
			if previous, ok := seen[normalized]; ok {
				t.Fatalf("%s exemplar %d duplicates %s: %q", tier.name, index, previous, value)
			}
			seen[normalized] = tier.name
		}
	}
}

// TestDefaultComplexityExemplarsBalanceSurfaceForm guards the property that
// makes the exemplars classify on requested work: no surface feature may
// concentrate in one tier. The bounds catch drift without blocking deliberate
// wording changes.
func TestDefaultComplexityExemplarsBalanceSurfaceForm(t *testing.T) {
	exemplars := DefaultComplexityExemplars()
	tiers := []struct {
		name   string
		values []string
	}{
		{name: "simple", values: exemplars.SimpleKeywords},
		{name: "medium", values: exemplars.MediumKeywords},
		{name: "complex", values: exemplars.ComplexKeywords},
	}

	type lengths struct{ min, median, max int }
	measured := make([]lengths, 0, len(tiers))
	for _, tier := range tiers {
		// TestDefaultComplexityExemplars already enforces the per-tier count, but
		// it is a separate test: an emptied tier would reach the min/median/max
		// indexing below and panic, taking the whole package's test binary with it
		// instead of naming the tier at fault.
		if len(tier.values) == 0 {
			t.Fatalf("%s has no default exemplars", tier.name)
		}
		counts := make([]int, 0, len(tier.values))
		for _, value := range tier.values {
			counts = append(counts, len(strings.Fields(value)))
		}
		slices.Sort(counts)
		measured = append(measured, lengths{min: counts[0], median: counts[len(counts)/2], max: counts[len(counts)-1]})
	}
	for index := 1; index < len(measured); index++ {
		lower, upper := measured[index-1], measured[index]
		if lower.max < upper.min {
			t.Errorf("%s (max %d words) and %s (min %d words) do not overlap in length; length alone would separate the tiers",
				tiers[index-1].name, lower.max, tiers[index].name, upper.min)
		}
		if upper.median > 2*lower.median {
			t.Errorf("%s median is %d words against %s's %d; a gradient that steep teaches the classifier that verbosity means difficulty",
				tiers[index].name, upper.median, tiers[index-1].name, lower.median)
		}
	}

	features := []struct {
		name    string
		matches func(string) bool
		minimum int
	}{
		{name: "question-form", matches: func(s string) bool { return strings.HasSuffix(strings.TrimSpace(s), "?") }, minimum: 5},
		{name: "all-lowercase", matches: func(s string) bool { return s == strings.ToLower(s) }, minimum: 5},
		{name: "embedded code", matches: func(s string) bool { return strings.Contains(s, "`") }, minimum: 2},
	}
	for _, feature := range features {
		perTier := make([]int, len(tiers))
		total := 0
		for index, tier := range tiers {
			for _, value := range tier.values {
				if feature.matches(value) {
					perTier[index]++
				}
			}
			total += perTier[index]
		}
		for index, tier := range tiers {
			if perTier[index] < feature.minimum {
				t.Errorf("%s has %d %s exemplars, want at least %d: the feature would identify the other tiers",
					tier.name, perTier[index], feature.name, feature.minimum)
			}
			if perTier[index]*10 > total*6 {
				t.Errorf("%s holds %d of %d %s exemplars; concentrating one that heavily makes it a tier signal",
					tier.name, perTier[index], total, feature.name)
			}
		}
	}
}

func TestDefaultComplexityExemplarsReturnsDeepCopy(t *testing.T) {
	first := DefaultComplexityExemplars()
	first.SimpleKeywords[0] = "changed"
	first.MediumKeywords[0] = "changed"
	first.ComplexKeywords[0] = "changed"

	second := DefaultComplexityExemplars()
	if second.SimpleKeywords[0] == "changed" {
		t.Fatal("simple exemplars expose mutable backing storage")
	}
	if second.MediumKeywords[0] == "changed" {
		t.Fatal("medium exemplars expose mutable backing storage")
	}
	if second.ComplexKeywords[0] == "changed" {
		t.Fatal("complex exemplars expose mutable backing storage")
	}
}
