package keyselectors

// Benchmarks for key selection.
//
// WeightedRandom runs once per request attempt (and again on every fallback
// hop) to pick which provider credential serves it, so it sits directly on the
// request path. It is O(keys) twice over - once to total the weights, once to
// walk them - which is why the benchmark covers a small key set as well as the
// large pools high-throughput deployments configure.
//
// Run:
//
//	go test ./core/keyselectors/ -bench WeightedRandom -benchmem

import (
	"strconv"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func benchKeys(n int) []schemas.Key {
	keys := make([]schemas.Key, n)
	for i := range keys {
		keys[i] = schemas.Key{
			ID:     "key-" + strconv.Itoa(i),
			Name:   "bench-key-" + strconv.Itoa(i),
			Models: schemas.WhiteList{"gpt-4o", "gpt-4o-mini", "o3-mini"},
			Weight: 1.0 / float64(n),
		}
	}
	return keys
}

func benchmarkWeightedRandom(b *testing.B, keyCount int) {
	keys := benchKeys(keyCount)
	b.ReportAllocs()
	for b.Loop() {
		key, err := WeightedRandom(nil, keys, schemas.OpenAI, "gpt-4o")
		if err != nil {
			b.Fatalf("select key: %v", err)
		}
		if key.ID == "" {
			b.Fatal("empty key selected")
		}
	}
}

func BenchmarkWeightedRandom3Keys(b *testing.B)   { benchmarkWeightedRandom(b, 3) }
func BenchmarkWeightedRandom25Keys(b *testing.B)  { benchmarkWeightedRandom(b, 25) }
func BenchmarkWeightedRandom200Keys(b *testing.B) { benchmarkWeightedRandom(b, 200) }
