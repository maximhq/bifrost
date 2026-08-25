package keyselectors

import (
	"fmt"
	"math"
	"math/rand"

	"github.com/maximhq/bifrost/core/schemas"
)

func WeightedRandom(ctx *schemas.BifrostContext, keys []schemas.Key, providerKey schemas.ModelProvider, model string) (schemas.Key, error) {
	if len(keys) == 0 {
		return schemas.Key{}, fmt.Errorf("no keys available for provider %s and model %s", providerKey, model)
	}

	return weightedRandomAt(keys, providerKey, model, rand.Float64())
}

// weightedRandomAt selects a key using unitRandom in [0, 1). It is split from
// WeightedRandom so the boundary behavior can be tested deterministically.
func weightedRandomAt(keys []schemas.Key, providerKey schemas.ModelProvider, model string, unitRandom float64) (schemas.Key, error) {
	maxWeight := 0.0
	eligibleCount := 0
	for _, key := range keys {
		weight := key.Weight
		if !isUsableWeight(weight) {
			continue
		}
		eligibleCount++
		if weight > maxWeight {
			maxWeight = weight
		}
	}

	if eligibleCount == 0 {
		return schemas.Key{}, fmt.Errorf("no keys with a positive finite weight available for provider %s and model %s", providerKey, model)
	}

	// Normalize by the largest weight before summing. This keeps the cumulative
	// range finite even when callers supply weights near math.MaxFloat64, while
	// preserving small positive weights without fixed-precision truncation.
	totalWeight := 0.0
	for _, key := range keys {
		if isUsableWeight(key.Weight) {
			totalWeight += key.Weight / maxWeight
		}
	}

	randomValue := unitRandom * totalWeight
	currentWeight := 0.0
	for _, key := range keys {
		if !isUsableWeight(key.Weight) {
			continue
		}
		currentWeight += key.Weight / maxWeight
		if randomValue < currentWeight {
			return key, nil
		}
	}

	return schemas.Key{}, fmt.Errorf("weighted key selection failed for provider %s and model %s", providerKey, model)
}

func isUsableWeight(weight float64) bool {
	return weight > 0 && !math.IsNaN(weight) && !math.IsInf(weight, 0)
}
