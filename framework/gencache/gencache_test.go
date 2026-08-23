package gencache

import (
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
)

// cloneInts is a WithClone helper for []int values.
func cloneInts(v []int) []int { return slices.Clone(v) }

func TestGetOrCompute_CachesWithinGeneration(t *testing.T) {
	var gen uint64
	var computes int
	c := New[int](func() uint64 { return gen }, 0)

	load := func() int { computes++; return 42 }
	if v := c.GetOrCompute("k", load); v != 42 {
		t.Fatalf("got %d, want 42", v)
	}
	if v := c.GetOrCompute("k", load); v != 42 {
		t.Fatalf("got %d, want 42", v)
	}
	if computes != 1 {
		t.Fatalf("compute ran %d times, want 1 (second call should hit)", computes)
	}
}

func TestGetOrCompute_RecomputesOnGenerationBump(t *testing.T) {
	var gen uint64
	var computes int
	c := New[int](func() uint64 { return gen }, 0)
	load := func() int { computes++; return computes }

	if v := c.GetOrCompute("k", load); v != 1 {
		t.Fatalf("first got %d, want 1", v)
	}
	gen++ // any store write bumps the generation
	if v := c.GetOrCompute("k", load); v != 2 {
		t.Fatalf("post-bump got %d, want 2 (must recompute)", v)
	}
	if computes != 2 {
		t.Fatalf("compute ran %d times, want 2", computes)
	}
}

func TestGetOrCompute_StampsWithPreComputeGeneration(t *testing.T) {
	// A generation bump that happens DURING compute must leave the entry stale,
	// so the next read recomputes rather than serving a value already outdated.
	var gen uint64
	c := New[int](func() uint64 { return gen }, 0)

	first := c.GetOrCompute("k", func() int {
		gen++ // data changed while we were computing
		return 1
	})
	if first != 1 {
		t.Fatalf("first got %d, want 1", first)
	}
	// The stored entry was stamped with gen=0 but current gen is 1 → miss.
	var recomputed bool
	second := c.GetOrCompute("k", func() int { recomputed = true; return 2 })
	if !recomputed || second != 2 {
		t.Fatalf("entry stamped during compute was not treated as stale (recomputed=%v, val=%d)", recomputed, second)
	}
}

func TestGetOrCompute_SkipStoreLeavesUncached(t *testing.T) {
	var gen uint64
	c := New[[]int](func() uint64 { return gen }, 0,
		WithSkipStore(func(v []int) bool { return len(v) == 0 }))

	if got := c.GetOrCompute("empty", func() []int { return nil }); len(got) != 0 {
		t.Fatalf("got %v, want empty", got)
	}
	if _, ok := c.Get("empty"); ok {
		t.Fatal("empty result must not be cached")
	}
	if c.Len() != 0 {
		t.Fatalf("Len=%d, want 0", c.Len())
	}
}

func TestGetOrCompute_BoundedFlushesOnOverflow(t *testing.T) {
	const max = 8
	var gen uint64
	c := New[int](func() uint64 { return gen }, max)

	for i := 0; i < max; i++ {
		c.GetOrCompute(fmt.Sprintf("k%d", i), func() int { return i })
	}
	if c.Len() != max {
		t.Fatalf("Len=%d, want %d (at cap)", c.Len(), max)
	}
	// One more distinct key must flush rather than grow past the cap.
	c.GetOrCompute("overflow", func() int { return 99 })
	if c.Len() > max {
		t.Fatalf("cache grew past cap: Len=%d, want <= %d", c.Len(), max)
	}
	if v, ok := c.Get("overflow"); !ok || v != 99 {
		t.Fatalf("new key missing after flush: v=%d ok=%v", v, ok)
	}
}

func TestWithClone_IsolatesCallerFromCache(t *testing.T) {
	var gen uint64
	c := New[[]int](func() uint64 { return gen }, 0, WithClone(cloneInts))

	first := c.GetOrCompute("k", func() []int { return []int{1, 2, 3} })
	for i := range first {
		first[i] = -1 // corrupt the returned slice
	}
	second, ok := c.Get("k")
	if !ok {
		t.Fatal("expected a live hit")
	}
	if !slices.Equal(second, []int{1, 2, 3}) {
		t.Fatalf("caller mutation leaked into cache: %v", second)
	}
}

func TestGet_MissOnStaleAndAbsent(t *testing.T) {
	var gen uint64
	c := New[int](func() uint64 { return gen }, 0)

	if _, ok := c.Get("absent"); ok {
		t.Fatal("absent key reported present")
	}
	c.GetOrCompute("k", func() int { return 1 })
	if _, ok := c.Get("k"); !ok {
		t.Fatal("fresh key reported absent")
	}
	gen++
	if _, ok := c.Get("k"); ok {
		t.Fatal("stale key reported present")
	}
}

func TestFlush(t *testing.T) {
	var gen uint64
	c := New[int](func() uint64 { return gen }, 0)
	c.GetOrCompute("a", func() int { return 1 })
	c.GetOrCompute("b", func() int { return 2 })
	c.Flush()
	if c.Len() != 0 {
		t.Fatalf("Len=%d after Flush, want 0", c.Len())
	}
}

func TestConcurrentGetOrCompute_NoRace(t *testing.T) {
	var gen atomic.Uint64
	c := New[int](func() uint64 { return gen.Load() }, 1024)

	var wg sync.WaitGroup
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				key := fmt.Sprintf("k%d", i%64)
				_ = c.GetOrCompute(key, func() int { return i })
				if i%50 == 0 {
					gen.Add(1) // interleave invalidations
				}
			}
		}(w)
	}
	wg.Wait()
}
