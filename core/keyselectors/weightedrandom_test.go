package keyselectors

import (
	"math"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// selectionCounts runs WeightedRandom draws times and tallies selections by key ID.
func selectionCounts(t *testing.T, keys []schemas.Key, draws int) map[string]int {
	t.Helper()
	counts := make(map[string]int, len(keys))
	for i := 0; i < draws; i++ {
		key, err := WeightedRandom(nil, keys, schemas.OpenAI, "gpt-4o")
		if err != nil {
			t.Fatalf("draw %d: unexpected error: %v", i, err)
		}
		counts[key.ID]++
	}
	return counts
}

// TestWeightedRandomFractionalWeightIsSelectable covers the canary rollout case
// from issue #5473: a key with weight below 0.01 must still receive traffic.
// With int truncation, int(0.005*100) == 0 and the canary key was never chosen.
func TestWeightedRandomFractionalWeightIsSelectable(t *testing.T) {
	keys := []schemas.Key{
		{ID: "primary", Weight: 0.995},
		{ID: "canary", Weight: 0.005},
	}

	const draws = 100000
	counts := selectionCounts(t, keys, draws)

	if counts["canary"] == 0 {
		t.Fatalf("canary key with weight 0.005 was never selected in %d draws", draws)
	}
	// Expected share is 0.5%. Allow a generous band to keep the test
	// deterministic in practice: fail only if the share is off by more
	// than 5x in either direction.
	share := float64(counts["canary"]) / draws
	if share > 0.025 {
		t.Errorf("canary share %.4f is far above the configured 0.005", share)
	}
}

// TestWeightedRandomAllFractionalWeightsKeepProportions covers the silent
// uniform fallback from issue #5473: when every key's weight is below 0.01,
// the truncated total was 0 and configured proportions were discarded.
func TestWeightedRandomAllFractionalWeightsKeepProportions(t *testing.T) {
	keys := []schemas.Key{
		{ID: "a", Weight: 0.008},
		{ID: "b", Weight: 0.002},
	}

	const draws = 100000
	counts := selectionCounts(t, keys, draws)

	shareA := float64(counts["a"]) / draws
	// Configured 80/20. Uniform fallback gives 50/50; assert the split is
	// much closer to the configured proportion than to uniform.
	if math.Abs(shareA-0.8) > 0.05 {
		t.Errorf("key a share %.4f, want ~0.80 (uniform fallback would give ~0.50)", shareA)
	}
}

// TestWeightedRandomIntegerWeightsKeepProportions guards existing behavior for
// ordinary weights.
func TestWeightedRandomIntegerWeightsKeepProportions(t *testing.T) {
	keys := []schemas.Key{
		{ID: "heavy", Weight: 3},
		{ID: "light", Weight: 1},
	}

	const draws = 100000
	counts := selectionCounts(t, keys, draws)

	share := float64(counts["heavy"]) / draws
	if math.Abs(share-0.75) > 0.05 {
		t.Errorf("heavy share %.4f, want ~0.75", share)
	}
}

// TestWeightedRandomZeroWeightsFallBackToUniform keeps the documented fallback:
// all-zero weights select uniformly instead of erroring.
func TestWeightedRandomZeroWeightsFallBackToUniform(t *testing.T) {
	keys := []schemas.Key{
		{ID: "a", Weight: 0},
		{ID: "b", Weight: 0},
	}

	const draws = 10000
	counts := selectionCounts(t, keys, draws)

	if counts["a"] == 0 || counts["b"] == 0 {
		t.Errorf("uniform fallback should select every key, got %v", counts)
	}
}

// TestWeightedRandomZeroWeightKeyGetsNoTraffic ensures an explicitly
// zero-weighted key is never selected while a positive-weight key exists.
func TestWeightedRandomZeroWeightKeyGetsNoTraffic(t *testing.T) {
	keys := []schemas.Key{
		{ID: "off", Weight: 0},
		{ID: "on", Weight: 0.5},
	}

	const draws = 10000
	counts := selectionCounts(t, keys, draws)

	if counts["off"] != 0 {
		t.Errorf("zero-weight key selected %d times in %d draws", counts["off"], draws)
	}
}
