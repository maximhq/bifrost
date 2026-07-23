package keyselectors

import (
	"math/rand"

	"github.com/maximhq/bifrost/core/schemas"
)

func WeightedRandom(ctx *schemas.BifrostContext, keys []schemas.Key, providerKey schemas.ModelProvider, model string) (schemas.Key, error) {
	// Use a weighted random selection based on key weights. Weights stay
	// float64 throughout: integer bucketing truncates any weight below the
	// bucket size to zero, silently starving that key. Each weight is
	// normalized by the largest positive weight before accumulating so that
	// extreme finite weights cannot overflow the running sum to +Inf.
	maxWeight := 0.0
	for _, key := range keys {
		if key.Weight > maxWeight {
			maxWeight = key.Weight
		}
	}

	// If all keys have zero weight, fall back to uniform random selection
	if maxWeight == 0 {
		return keys[rand.Intn(len(keys))], nil
	}

	totalWeight := 0.0
	for _, key := range keys {
		if key.Weight > 0 {
			totalWeight += key.Weight / maxWeight
		}
	}

	// Use global thread-safe random (Go 1.20+) - no allocation, no syscall
	randomValue := rand.Float64() * totalWeight

	// Select key based on weight
	currentWeight := 0.0
	for _, key := range keys {
		if key.Weight > 0 {
			currentWeight += key.Weight / maxWeight
		}
		if randomValue < currentWeight {
			return key, nil
		}
	}

	// Fallback to first key if something goes wrong
	return keys[0], nil
}
