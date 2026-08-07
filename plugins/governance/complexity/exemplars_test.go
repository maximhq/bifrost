package complexity

import (
	"slices"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/framework/configstore"
)

func TestDefaultEditableKeywordConfigIsExemplarsOnly(t *testing.T) {
	cfg := DefaultEditableKeywordConfig()
	exemplars := configstore.DefaultComplexityExemplars()
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
