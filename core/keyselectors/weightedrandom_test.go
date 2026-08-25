package keyselectors

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestWeightedRandomRejectsEmptyKeys(t *testing.T) {
	if _, err := WeightedRandom(nil, nil, schemas.OpenAI, "gpt-test"); err == nil {
		t.Fatal("expected empty key set to return an error")
	}
}

func TestWeightedRandomHandlesNonPositiveWeights(t *testing.T) {
	keys := []schemas.Key{
		{ID: "negative", Weight: -1},
		{ID: "zero", Weight: 0},
	}
	for range 100 {
		selected, err := WeightedRandom(nil, keys, schemas.OpenAI, "gpt-test")
		if err != nil {
			t.Fatalf("select key: %v", err)
		}
		if selected.ID != "negative" && selected.ID != "zero" {
			t.Fatalf("selected unknown key %q", selected.ID)
		}
	}
}

func TestWeightedRandomIgnoresNegativeWeightsWhenPositiveExists(t *testing.T) {
	keys := []schemas.Key{
		{ID: "negative", Weight: -100},
		{ID: "positive", Weight: 1},
	}
	for range 100 {
		selected, err := WeightedRandom(nil, keys, schemas.OpenAI, "gpt-test")
		if err != nil {
			t.Fatalf("select key: %v", err)
		}
		if selected.ID != "positive" {
			t.Fatalf("negative-weight key must not participate when a positive weight exists; got %q", selected.ID)
		}
	}
}
