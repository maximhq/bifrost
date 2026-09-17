package keyselectors

import (
	"math/rand"

	"github.com/maximhq/bifrost/core/schemas"
)

func WeightedRandom(ctx *schemas.BifrostContext, keys []schemas.Key, providerKey schemas.ModelProvider, model string) (schemas.Key, error) {
	// Use a weighted random selection based on key weights
	totalWeight := 0
	for _, key := range keys {
		totalWeight += weightUnits(key)
	}

	// If all keys have zero weight -- or the weights summed to something
	// rand.Intn cannot be called with -- fall back to uniform random selection
	if totalWeight <= 0 {
		return keys[rand.Intn(len(keys))], nil
	}

	// Use global thread-safe random (Go 1.20+) - no allocation, no syscall
	randomValue := rand.Intn(totalWeight)

	// Select key based on weight
	currentWeight := 0
	for _, key := range keys {
		currentWeight += weightUnits(key)
		if randomValue < currentWeight {
			return key, nil
		}
	}

	// Fallback to first key if something goes wrong
	return keys[0], nil
}

// weightUnits converts a key's weight into the integer units the selection
// arithmetic runs in, and never returns a negative number.
//
// Key.Weight is an unvalidated float64: the provider-key HTTP handlers, the
// schemas package and the configstore GORM hooks all leave it alone, so a
// stored weight can be negative, NaN, or large enough that int(w*100) does not
// fit in an int. Conversions that do not fit are implementation-defined in Go
// -- on amd64 they produce -9223372036854775808 -- so any of those cases can
// drive the running total negative, and rand.Intn panics for n <= 0. That
// happens on the inference path, where there is no recover().
//
// Treating an unusable weight as 0 keeps the consequence local to the key that
// carries it (a zero-weight key is simply not selected while a sibling has
// weight) instead of discarding the whole configured split.
func weightUnits(key schemas.Key) int {
	units := int(key.Weight * 100) // Convert float to int for better performance
	if units < 0 {
		return 0
	}
	return units
}
