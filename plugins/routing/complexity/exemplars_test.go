package complexity

import (
	"slices"
	"strings"
	"testing"
)

func TestDefaultEditableKeywordConfigIsExemplarsOnly(t *testing.T) {
	cfg := DefaultEditableKeywordConfig()
	exemplars := DefaultComplexityExemplars()
	tiers := []struct {
		name      string
		values    []string
		keywords  []string
		exemplars []string
	}{
		{name: TierSimple, values: cfg.SimpleKeywords, keywords: simpleKeywords, exemplars: exemplars.SimpleKeywords},
		{name: TierMedium, values: cfg.MediumKeywords, keywords: mediumKeywords, exemplars: exemplars.MediumKeywords},
		{name: TierComplex, values: cfg.ComplexKeywords, keywords: complexKeywords, exemplars: exemplars.ComplexKeywords},
	}

	for _, tier := range tiers {
		// The editable lists are the administrator-facing reference phrases, so
		// they hold the exemplars in review order and nothing else. The lexical
		// matcher's own keyword vocabulary must not leak in.
		if !slices.Equal(tier.values, tier.exemplars) {
			t.Fatalf("%s defaults are not exactly the semantic exemplars in review order", tier.name)
		}
		for _, keyword := range tier.keywords {
			if slices.Contains(tier.values, keyword) {
				t.Fatalf("%s defaults contain lexical keyword %q", tier.name, keyword)
			}
		}
	}
}

func TestDefaultEditableKeywordConfigHasNoCrossTierDuplicates(t *testing.T) {
	cfg := DefaultEditableKeywordConfig()
	tiers := []struct {
		name   string
		values []string
	}{
		{name: TierSimple, values: cfg.SimpleKeywords},
		{name: TierMedium, values: cfg.MediumKeywords},
		{name: TierComplex, values: cfg.ComplexKeywords},
	}

	seen := make(map[string]string)
	for _, tier := range tiers {
		for index, value := range tier.values {
			normalized := strings.ToLower(strings.Join(strings.Fields(value), " "))
			if normalized == "" {
				t.Fatalf("%s shared entry %d is empty", tier.name, index)
			}
			if previous, ok := seen[normalized]; ok {
				t.Fatalf("%s shared entry %d duplicates %s: %q", tier.name, index, previous, value)
			}
			seen[normalized] = tier.name
		}
	}
}

func TestDefaultEditableKeywordConfigFitsSemanticValidation(t *testing.T) {
	cfg := DefaultAnalyzerConfig()
	cfg.Semantic = &SemanticConfig{
		Provider:       "openai",
		EmbeddingModel: "test-embedding-model",
	}

	if _, err := ValidateAndNormalize(&cfg); err != nil {
		t.Fatalf("default shared phrases must remain valid when semantic routing is enabled: %v", err)
	}
}

func TestDefaultEditableKeywordConfigReturnsDeepCopy(t *testing.T) {
	first := DefaultEditableKeywordConfig()
	first.SimpleKeywords[0] = "changed"
	first.MediumKeywords[0] = "changed"
	first.ComplexKeywords[0] = "changed"

	second := DefaultEditableKeywordConfig()
	if second.SimpleKeywords[0] == "changed" {
		t.Fatal("simple shared defaults expose mutable backing storage")
	}
	if second.MediumKeywords[0] == "changed" {
		t.Fatal("medium shared defaults expose mutable backing storage")
	}
	if second.ComplexKeywords[0] == "changed" {
		t.Fatal("complex shared defaults expose mutable backing storage")
	}
}

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
