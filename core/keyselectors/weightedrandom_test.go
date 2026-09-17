package keyselectors

import (
	"math"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// keysWithWeights builds the only part of schemas.Key this selector reads.
func keysWithWeights(weights ...float64) []schemas.Key {
	keys := make([]schemas.Key, len(weights))
	for i, w := range weights {
		keys[i] = schemas.Key{ID: string(rune('a' + i)), Weight: w}
	}
	return keys
}

// selectMany calls WeightedRandom enough times that a probabilistic branch is
// exercised, and reports which key IDs came back.
func selectMany(t *testing.T, keys []schemas.Key) map[string]int {
	t.Helper()
	seen := map[string]int{}
	for i := 0; i < 200; i++ {
		key, err := WeightedRandom(nil, keys, schemas.ModelProvider("openai"), "gpt-4o")
		if err != nil {
			t.Fatalf("WeightedRandom returned an error: %v", err)
		}
		seen[key.ID]++
	}
	return seen
}

// A negative weight makes the running total negative, and rand.Intn panics for
// n <= 0. Key.Weight is not validated on the write path, so this is reachable
// from a stored config, and it panics on the inference path.
func TestWeightedRandomSurvivesANegativeWeight(t *testing.T) {
	// -2 and 1 sum to -100 units. -1 and 1 would sum to exactly 0 and land on
	// the all-zero fallback, which is why the bug needs a sum, not just a sign.
	seen := selectMany(t, keysWithWeights(-2, 1))

	if seen["a"] != 0 {
		t.Errorf("key with a negative weight was selected %d times, want 0", seen["a"])
	}
	if seen["b"] == 0 {
		t.Error("the only key with a usable weight was never selected")
	}
}

// int(w*100) is implementation-defined when the result does not fit in an int,
// and on amd64 it yields the most negative int. Two keys are enough for the
// sum alone to overflow even when each conversion is in range, so the fallback
// has to test the sum, not only the per-key value.
func TestWeightedRandomSurvivesWeightsTooLargeForTheArithmetic(t *testing.T) {
	for name, keys := range map[string][]schemas.Key{
		"per-key overflow":  keysWithWeights(1e20, 1),
		"sum overflow":      keysWithWeights(5e16, 5e16),
		"NaN":               keysWithWeights(math.NaN(), 1),
		"positive infinity": keysWithWeights(math.Inf(1), 1),
		"negative infinity": keysWithWeights(math.Inf(-1), 1),
	} {
		t.Run(name, func(t *testing.T) {
			selectMany(t, keys)
		})
	}
}

// The weighting itself still has to work: an unusable weight must cost only
// the key that carries it.
func TestWeightedRandomStillFollowsTheConfiguredWeights(t *testing.T) {
	seen := selectMany(t, keysWithWeights(1, 0))
	if seen["b"] != 0 {
		t.Errorf("zero-weight key was selected %d times alongside a weighted sibling, want 0", seen["b"])
	}

	seen = selectMany(t, keysWithWeights(3, 1))
	if seen["a"] == 0 || seen["b"] == 0 {
		t.Errorf("both weighted keys should be reachable, got %v", seen)
	}
	if seen["a"] <= seen["b"] {
		t.Errorf("the heavier key should win more often, got %v", seen)
	}
}

// Pre-existing behaviour: with nothing to weight by, selection is uniform
// rather than always the first key.
func TestWeightedRandomIsUniformWhenNoKeyHasWeight(t *testing.T) {
	seen := selectMany(t, keysWithWeights(0, 0))
	if seen["a"] == 0 || seen["b"] == 0 {
		t.Errorf("all-zero weights should select uniformly, got %v", seen)
	}
}
