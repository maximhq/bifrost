package keyselectors

import (
	"fmt"
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

// weightUnits rejects out-of-range weights BEFORE converting, so the result
// does not depend on what the target's float-to-int conversion does with a
// value that does not fit. amd64 produces the most negative int and riscv64
// saturates to the most positive one; a check on the converted value would
// have to agree with both, and cannot.
func TestWeightUnitsRejectsWhatIntCannotHold(t *testing.T) {
	for name, weight := range map[string]float64{
		"NaN":                 math.NaN(),
		"positive infinity":   math.Inf(1),
		"negative infinity":   math.Inf(-1),
		"negative":            -2,
		"above int range":     math.MaxInt64,
		"far above int range": 1e300,
	} {
		t.Run(name, func(t *testing.T) {
			if got := weightUnits(schemas.Key{Weight: weight}); got != 0 {
				t.Errorf("weightUnits(%v) = %d, want 0", weight, got)
			}
		})
	}

	for name, tc := range map[string]struct {
		weight float64
		want   int
	}{
		"ordinary":     {1, 100},
		"fractional":   {0.5, 50},
		"below a unit": {0.005, 0},
		"zero":         {0, 0},
	} {
		t.Run(name, func(t *testing.T) {
			if got := weightUnits(schemas.Key{Weight: tc.weight}); got != tc.want {
				t.Errorf("weightUnits(%v) = %d, want %d", tc.weight, got, tc.want)
			}
		})
	}
}

// Clamping each key on its own leaves the SUM able to overflow: enough keys
// whose units are each in range wrap the total past math.MaxInt. Wrapping to a
// negative is caught by the <= 0 fallback, but wrapping far enough lands back
// on a positive number that no guard sees, and rand.Intn would then get a
// bound unrelated to the configured weights.
func TestAddWeightUnitsSaturatesInsteadOfWrapping(t *testing.T) {
	for name, tc := range map[string]struct {
		total, units, want int
	}{
		"ordinary":            {100, 200, 300},
		"exactly at the top":  {math.MaxInt - 1, 1, math.MaxInt},
		"one past the top":    {math.MaxInt - 1, 2, math.MaxInt},
		"both near the top":   {math.MaxInt, math.MaxInt, math.MaxInt},
		"already saturated":   {math.MaxInt, 0, math.MaxInt},
		"zero into saturated": {math.MaxInt, 1, math.MaxInt},
	} {
		t.Run(name, func(t *testing.T) {
			got := addWeightUnits(tc.total, tc.units)
			if got != tc.want {
				t.Errorf("addWeightUnits(%d, %d) = %d, want %d", tc.total, tc.units, got, tc.want)
			}
			if got < 0 {
				t.Errorf("addWeightUnits(%d, %d) went negative (%d) -- rand.Intn would panic", tc.total, tc.units, got)
			}
		})
	}
}

// Enough keys whose units are each in range still sum past math.MaxInt, and a
// plain += wraps. Whether a given count wraps to a negative (caught by the
// <= 0 fallback) or back to a positive (caught by nothing) depends on how many
// times it goes round, so the selector must not depend on which happened.
func TestWeightedRandomSurvivesASumThatOverflows(t *testing.T) {
	huge := math.Nextafter(float64(math.MaxInt)/100, 0) // largest weight weightUnits still accepts
	for _, n := range []int{2, 3, 4, 8} {
		weights := make([]float64, n)
		for i := range weights {
			weights[i] = huge
		}
		t.Run(fmt.Sprintf("%d keys", n), func(t *testing.T) {
			seen := selectMany(t, keysWithWeights(weights...))
			if len(seen) == 0 {
				t.Fatal("no key was ever selected")
			}
		})
	}
}
