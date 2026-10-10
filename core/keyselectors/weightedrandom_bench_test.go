package keyselectors

// Tests and benchmarks for key selection.
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
	"math"
	"strconv"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// pickCounts runs WeightedRandom n times over keys with the given weights and counts the picks by
// key ID. A panic inside the selector fails the test instead of killing the run, since the request
// worker that calls it has no recover and a panic there takes the gateway down.
func pickCounts(t *testing.T, weights []float64, n int) map[string]int {
	t.Helper()
	keys := make([]schemas.Key, len(weights))
	for i, w := range weights {
		keys[i] = schemas.Key{ID: "key-" + strconv.Itoa(i), Weight: w}
	}
	counts := map[string]int{}
	for range n {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("WeightedRandom panicked for weights %v: %v", weights, r)
				}
			}()
			key, err := WeightedRandom(nil, keys, schemas.OpenAI, "gpt-4o")
			if err != nil {
				t.Fatalf("WeightedRandom(%v): %v", weights, err)
			}
			counts[key.ID]++
		}()
	}
	return counts
}

func TestWeightedRandom(t *testing.T) {
	const n = 500
	cases := []struct {
		name    string
		weights []float64
		// only lists the key IDs that may be picked; every listed key must be picked at least once.
		only []string
	}{
		{"a zero-weight key beside a weighted one is never picked", []float64{0, 1}, []string{"key-1"}},
		{"all zero weights pick uniformly", []float64{0, 0}, []string{"key-0", "key-1"}},
		{"weights below 0.01 still count", []float64{0.005, 0}, []string{"key-0"}},
		{"a negative weight counts as zero", []float64{1, -2}, []string{"key-0"}},
		{"only negative weights pick uniformly", []float64{-1, -1}, []string{"key-0", "key-1"}},
		{"a NaN weight counts as zero", []float64{math.NaN(), 1}, []string{"key-1"}},
		{"an infinite weight counts as zero", []float64{math.Inf(1), 1}, []string{"key-1"}},
		{"weights too large for an int do not overflow", []float64{1e18, 1e18}, []string{"key-0", "key-1"}},
		{"finite weights whose sum overflows a float", []float64{0, 1e308, 1e308}, []string{"key-1", "key-2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			counts := pickCounts(t, tc.weights, n)
			allowed := map[string]bool{}
			for _, id := range tc.only {
				allowed[id] = true
				if counts[id] == 0 {
					t.Errorf("%s was never picked in %d draws: %v", id, n, counts)
				}
			}
			for id, c := range counts {
				if !allowed[id] {
					t.Errorf("%s was picked %d times, want never: %v", id, c, counts)
				}
			}
		})
	}
}

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
