package keysortingalgos

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

var rngSeed uint64 = uint64(time.Now().UnixNano())

var rngPool = sync.Pool{
	New: func() any {
		seed := atomic.AddUint64(&rngSeed, 1)
		return rand.New(rand.NewSource(int64(seed)))
	},
}

var scoresPool = sync.Pool{
	New: func() any {
		// start small; we'll grow as needed and reuse
		buf := make([]float64, 0, 64)
		return &buf
	},
}

// WeightedRandomKeySorter shuffles keys in-place into a weighted-random order.
// Complexity: O(n log n). Allocations: typically 0 (after pools are warm).
func WeightedRandomKeySorter(
	_ *context.Context,
	keys []schemas.Key,
	_ schemas.ModelProvider,
	_ string,
) ([]schemas.Key, error) {
	n := len(keys)
	if n == 0 {
		return nil, fmt.Errorf("no keys provided")
	}

	// Grab pooled RNG and score buffer
	r := rngPool.Get().(*rand.Rand)
	defer rngPool.Put(r)

	sb := scoresPool.Get().(*[]float64)
	if cap(*sb) < n {
		*sb = make([]float64, n) // grows once, then reused
	} else {
		*sb = (*sb)[:n]
	}
	scores := *sb
	defer func() {
		// optional: shrink logical length to keep pool friendly
		*sb = scores[:0]
		scoresPool.Put(sb)
	}()

	// Compute scores; handle w<=0 cheaply
	// w<0 is usually a config bug; if you truly want speed, you can drop this check.
	for i := 0; i < n; i++ {
		w := keys[i].Weight
		if w < 0 {
			return nil, fmt.Errorf("key %q has negative weight %f", keys[i].ID, w)
		}
		if w == 0 {
			scores[i] = math.Inf(-1) // push to the end
			continue
		}

		// u in (0,1]; ensure non-zero so log(u) is finite.
		u := r.Float64()
		for u == 0 {
			u = r.Float64()
		}
		scores[i] = math.Log(u) / w
	}

	// Sort keys in-place by score descending (higher score first)
	sort.Slice(keys, func(i, j int) bool {
		return scores[i] > scores[j]
	})

	// Optional compromise: uniformly shuffle the tail of zero-weight keys
	// so they don't always appear in the same relative order.
	// Find first -Inf (zero-weight) position.
	z := n
	for i := 0; i < n; i++ {
		if math.IsInf(scores[i], -1) {
			z = i
			break
		}
	}
	// Fisher–Yates on keys[z:]
	for i := n - 1; i > z; i-- {
		j := z + r.Intn(i-z+1)
		keys[i], keys[j] = keys[j], keys[i]
	}

	return keys, nil
}
