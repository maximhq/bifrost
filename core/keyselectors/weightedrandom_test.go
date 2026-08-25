package keyselectors

import (
	"math"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestWeightedRandomRejectsEmptyKeys(t *testing.T) {
	if _, err := WeightedRandom(nil, nil, schemas.OpenAI, "gpt-test"); err == nil {
		t.Fatal("expected empty key set to return an error")
	}
}

func TestWeightedRandomRejectsNonPositiveWeights(t *testing.T) {
	keys := []schemas.Key{
		{ID: "negative", Weight: -1},
		{ID: "zero", Weight: 0},
	}
	if _, err := WeightedRandom(nil, keys, schemas.OpenAI, "gpt-test"); err == nil {
		t.Fatal("expected a key set without positive weights to return an error")
	}
}

func TestWeightedRandomIgnoresNegativeWeightsWhenPositiveExists(t *testing.T) {
	keys := []schemas.Key{
		{ID: "negative", Weight: -100},
		{ID: "positive", Weight: 1},
	}
	selected, err := WeightedRandom(nil, keys, schemas.OpenAI, "gpt-test")
	if err != nil {
		t.Fatalf("select key: %v", err)
	}
	if selected.ID != "positive" {
		t.Fatalf("negative-weight key must not participate when a positive weight exists; got %q", selected.ID)
	}
}

func TestWeightedRandomIgnoresNonFiniteWeightsWhenPositiveExists(t *testing.T) {
	keys := []schemas.Key{
		{ID: "nan", Weight: math.NaN()},
		{ID: "positive-infinity", Weight: math.Inf(1)},
		{ID: "negative-infinity", Weight: math.Inf(-1)},
		{ID: "positive", Weight: 1},
	}

	selected, err := WeightedRandom(nil, keys, schemas.OpenAI, "gpt-test")
	if err != nil {
		t.Fatalf("select key: %v", err)
	}
	if selected.ID != "positive" {
		t.Fatalf("non-finite weights must not participate; got %q", selected.ID)
	}
}

func TestWeightedRandomRejectsOnlyNonFiniteWeights(t *testing.T) {
	keys := []schemas.Key{
		{ID: "nan", Weight: math.NaN()},
		{ID: "infinity", Weight: math.Inf(1)},
	}
	if _, err := WeightedRandom(nil, keys, schemas.OpenAI, "gpt-test"); err == nil {
		t.Fatal("expected a key set without finite positive weights to return an error")
	}
}

func TestWeightedRandomPreservesTinyPositiveWeights(t *testing.T) {
	keys := []schemas.Key{
		{ID: "normal", Weight: 1},
		{ID: "tiny", Weight: 0.001},
	}

	selected, err := weightedRandomAt(keys, schemas.OpenAI, "gpt-test", math.Nextafter(1, 0))
	if err != nil {
		t.Fatalf("select key: %v", err)
	}
	if selected.ID != "tiny" {
		t.Fatalf("tiny positive weight must remain reachable; got %q", selected.ID)
	}
}

func TestWeightedRandomHandlesVeryLargeWeights(t *testing.T) {
	keys := []schemas.Key{
		{ID: "first", Weight: math.MaxFloat64},
		{ID: "second", Weight: math.MaxFloat64},
	}

	selected, err := weightedRandomAt(keys, schemas.OpenAI, "gpt-test", 0.75)
	if err != nil {
		t.Fatalf("select key: %v", err)
	}
	if selected.ID != "second" {
		t.Fatalf("large finite weights must not overflow selection; got %q", selected.ID)
	}
}
