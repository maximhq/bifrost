package keyselectors

import (
	"math"
	"math/rand"

	"github.com/maximhq/bifrost/core/schemas"
)

func WeightedRandom(ctx *schemas.BifrostContext, keys []schemas.Key, providerKey schemas.ModelProvider, model string) (schemas.Key, error) {
	// Use a weighted random selection based on key weights. The weights are summed as floats: a
	// key's weight is any float its config allows, and converting it to an int both drops weights
	// below the conversion's resolution and lets a negative or overflowing sum reach rand, which
	// panics on it inside the request worker.
	totalWeight := 0.0
	for _, key := range keys {
		totalWeight += usableWeight(key.Weight)
	}

	// If all keys have zero weight, fall back to uniform random selection
	if totalWeight == 0 {
		return keys[rand.Intn(len(keys))], nil
	}

	// Finite weights near the float maximum can still sum to infinity, and then no draw lands and
	// every request falls through to the first key whatever its weight. Such a pool is drawn from
	// its weights divided by the largest one, which keeps the sum within the key count. The
	// largest weight is then far from zero, so dividing by it cannot overflow in turn.
	scale := 1.0
	if math.IsInf(totalWeight, 1) {
		maxWeight := 0.0
		for _, key := range keys {
			maxWeight = max(maxWeight, usableWeight(key.Weight))
		}
		scale = 1 / maxWeight
		totalWeight = 0
		for _, key := range keys {
			totalWeight += usableWeight(key.Weight) * scale
		}
	}

	// Use global thread-safe random (Go 1.20+) - no allocation, no syscall
	randomValue := rand.Float64() * totalWeight

	// Select key based on weight
	currentWeight := 0.0
	for _, key := range keys {
		currentWeight += usableWeight(key.Weight) * scale
		if randomValue < currentWeight {
			return key, nil
		}
	}

	// Fallback to first key if something goes wrong
	return keys[0], nil
}

// usableWeight is the weight a key is selected by. Write paths reject a negative or non-finite
// weight, but a key can still carry one from config.json or an older row, so it counts as zero
// here rather than corrupting the cumulative range.
func usableWeight(weight float64) float64 {
	if weight < 0 || math.IsNaN(weight) || math.IsInf(weight, 0) {
		return 0
	}
	return weight
}
