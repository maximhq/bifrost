package keyselectors

import (
	"math"
	"math/rand"

	"github.com/maximhq/bifrost/core/schemas"
)

func WeightedRandom(ctx *schemas.BifrostContext, keys []schemas.Key, providerKey schemas.ModelProvider, model string) (schemas.Key, error) {
	// Use a weighted random selection based on key weights
	totalWeight := 0
	for _, key := range keys {
		totalWeight = addWeightUnits(totalWeight, weightUnits(key))
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
		currentWeight = addWeightUnits(currentWeight, weightUnits(key))
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
// stored weight can be negative, NaN, or large enough that w*100 does not fit
// in an int. Any of those can drive the running total to something rand.Intn
// rejects, and it panics for n <= 0 on the inference path, where there is no
// recover().
//
// The range is checked BEFORE the conversion rather than after, because a
// float-to-int conversion whose result does not fit is implementation-defined
// in Go, and the implementations disagree in a way a post-hoc sign test cannot
// cover: amd64 yields the most negative int, while riscv64's FCVT.L.D
// saturates to the most positive one. Checking after would catch the first and
// silently hand the second an enormous selection range.
//
// Treating an unusable weight as 0 keeps the consequence local to the key that
// carries it (a zero-weight key is simply not selected while a sibling has
// weight) instead of discarding the whole configured split.
func weightUnits(key schemas.Key) int {
	scaled := key.Weight * 100 // Convert float to int for better performance
	// NaN compares false against everything, so it has to be tested on its own;
	// the range test then covers both infinities as well as finite overflow.
	// float64(math.MaxInt) rounds up to 2^63 on 64-bit, so >= rejects exactly
	// the values int cannot hold.
	if math.IsNaN(scaled) || scaled <= 0 || scaled >= float64(math.MaxInt) {
		return 0
	}
	return int(scaled)
}

// addWeightUnits returns total+units, saturating at math.MaxInt instead of
// wrapping.
//
// Clamping each key on its own is not enough: several keys whose individual
// units are all in range can still sum past math.MaxInt, and signed overflow
// wraps. Wrapping to a negative total is caught by the caller's <= 0 fallback,
// but enough keys wrap it back to a positive number, which is not caught --
// rand.Intn would then be called with a bound that bears no relation to the
// configured weights. Saturating keeps the sum monotonic, so the worst case is
// that the heaviest keys become indistinguishable rather than selection
// becoming arbitrary.
func addWeightUnits(total, units int) int {
	if total > math.MaxInt-units {
		return math.MaxInt
	}
	return total + units
}
