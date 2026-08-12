package keyselectors

import (
	"fmt"
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

	// If no key has a positive weight, fall back to uniform random selection
	// over zero-weight keys only: negative weights mean "exclude this key" and
	// must not receive traffic through the fallback either.
	if maxWeight == 0 {
		eligible := make([]schemas.Key, 0, len(keys))
		for _, key := range keys {
			if key.Weight == 0 {
				eligible = append(eligible, key)
			}
		}
		if len(eligible) == 0 {
			return schemas.Key{}, fmt.Errorf("no eligible keys for provider: %v: all key weights are negative", providerKey)
		}
		return eligible[rand.Intn(len(eligible))], nil
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
