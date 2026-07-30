package complexity

import (
	"slices"
	"strings"
	"testing"
)

func TestDefaultEditableKeywordConfigIncludesSemanticExemplars(t *testing.T) {
	cfg := DefaultEditableKeywordConfig()
	tiers := []struct {
		name      string
		values    []string
		keywords  []string
		exemplars []string
	}{
		{name: TierSimple, values: cfg.SimpleKeywords, keywords: simpleKeywords, exemplars: defaultSimpleExemplars},
		{name: TierMedium, values: cfg.MediumKeywords, keywords: mediumKeywords, exemplars: defaultMediumExemplars},
		{name: TierComplex, values: cfg.ComplexKeywords, keywords: complexKeywords, exemplars: defaultComplexExemplars},
	}

	for _, tier := range tiers {
		if len(tier.values) != len(tier.keywords)+len(tier.exemplars) {
			t.Fatalf("%s shared defaults have %d entries, want %d keywords + %d exemplars",
				tier.name, len(tier.values), len(tier.keywords), len(tier.exemplars))
		}
		if !slices.Equal(tier.values[:len(tier.keywords)], tier.keywords) {
			t.Fatalf("%s shared defaults do not preserve lexical keyword order", tier.name)
		}
		if !slices.Equal(tier.values[len(tier.keywords):], tier.exemplars) {
			t.Fatalf("%s shared defaults do not append semantic exemplars in review order", tier.name)
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
