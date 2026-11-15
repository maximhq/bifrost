package livestore

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestTableExpiry checks the table TTL, a per-entry TTL that never expires, and the sweep.
func TestTableExpiry(t *testing.T) {
	clock := setClock(t, time.Unix(1_000_000, 0))
	s := newStore()
	defer s.Close()
	tb, err := s.NewTable[string, int](context.Background(), "ttl", WithTTL(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	tb.Put("a", 1)
	tb.PutWithTTL("b", 2, 0)
	if v, ok := tb.Get("a"); !ok || v != 1 {
		t.Fatalf("Get a = %v, %v", v, ok)
	}
	if at, ok := tb.Expiry("a"); !ok || !at.Equal(clock.Add(time.Minute)) {
		t.Fatalf("Expiry a = %v, %v, want the table TTL from now", at, ok)
	}
	if _, ok := tb.Expiry("b"); ok {
		t.Fatal("an entry that never expires must report no expiry")
	}
	if _, ok := tb.Expiry("missing"); ok {
		t.Fatal("a missing key must report no expiry")
	}
	*clock = clock.Add(2 * time.Minute)
	if _, ok := tb.Expiry("a"); ok {
		t.Fatal("an expired entry must report no expiry")
	}
	if _, ok := tb.Get("a"); ok {
		t.Fatal("an expired entry must read as missing")
	}
	if v, ok := tb.Get("b"); !ok || v != 2 {
		t.Fatal("a zero TTL must never expire")
	}
	tb.sweep(now().UnixNano())
	// Count what the shards store, not what Range shows: Range already skips expired entries.
	stored := 0
	for i := range tb.shards {
		sh := &tb.shards[i]
		sh.mu.RLock()
		stored += len(sh.plain) + len(sh.m)
		sh.mu.RUnlock()
	}
	if stored != 1 {
		t.Fatalf("after the sweep %d entries are stored, want only b", stored)
	}
	if v, ok := tb.Get("b"); !ok || v != 2 {
		t.Fatal("the sweep must keep an entry that never expires")
	}
}

// TestTableGeneration checks that a memo entry from an older generation misses.
func TestTableGeneration(t *testing.T) {
	s := newStore()
	defer s.Close()
	gen := uint64(1)
	tb, err := s.NewTable[string, string](context.Background(), "memo", WithGeneration(func() uint64 { return gen }))
	if err != nil {
		t.Fatal(err)
	}
	tb.Put("k", "v")
	if _, ok := tb.Get("k"); !ok {
		t.Fatal("a current entry must hit")
	}
	gen++
	if _, ok := tb.Get("k"); ok {
		t.Fatal("an entry from an older generation must miss")
	}
}

// TestTablePutIfAbsent checks leases: one holder until expiry.
func TestTablePutIfAbsent(t *testing.T) {
	clock := setClock(t, time.Unix(1_000_000, 0))
	s := newStore()
	defer s.Close()
	tb, err := s.NewTable[string, string](context.Background(), "leases")
	if err != nil {
		t.Fatal(err)
	}
	if !tb.PutIfAbsent("job", "owner-a", time.Minute) {
		t.Fatal("the first claim must win")
	}
	if tb.PutIfAbsent("job", "owner-b", time.Minute) {
		t.Fatal("a live claim must not be taken")
	}
	*clock = clock.Add(2 * time.Minute)
	if !tb.PutIfAbsent("job", "owner-b", time.Minute) {
		t.Fatal("an expired claim must be free")
	}
}

// TestStateTable checks compare-and-swap on a state table and that removing the entity
// removes its state.
func TestStateTable(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	f.load(t)
	usage, err := f.s.RegisterState[int](context.Background(), "usage", Kind("group"))
	if err != nil {
		t.Fatal(err)
	}
	one, two := new(int), new(int)
	*one, *two = 1, 2
	if !CompareAndSwap(usage, "g1", nil, one) {
		t.Fatal("nil old must insert when absent")
	}
	if CompareAndSwap(usage, "g1", nil, two) {
		t.Fatal("nil old must fail when present")
	}
	if !CompareAndSwap(usage, "g1", one, two) {
		t.Fatal("the current value must swap")
	}
	if CompareAndSwap(usage, "g1", one, two) {
		t.Fatal("a stale value must not swap")
	}
	if err := f.s.Refresh(context.Background(), []Ref{{Kind: "group", ID: "g1", Remove: true}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := usage.Get("g1"); ok {
		t.Fatal("removing the entity must remove its state")
	}
}

// TestRegisterStateNeedsAKind checks that a state table must name the kind whose records key it:
// with no kind, removing a record would never clear its entry. The refused name stays free.
func TestRegisterStateNeedsAKind(t *testing.T) {
	f := newFixture(t)
	f.load(t)
	ctx := context.Background()
	if _, err := f.s.RegisterState[int](ctx, "usage", Kind("")); err == nil {
		t.Fatal("RegisterState with an empty kind must fail")
	}
	if _, err := f.s.NewTable[string, int](ctx, "usage"); err != nil {
		t.Fatalf("a name RegisterState refused must stay free: %v", err)
	}
}

// TestDropTable checks that a dropped name can be registered again.
func TestDropTable(t *testing.T) {
	s := newStore()
	defer s.Close()
	tb, err := s.NewTable[string, int](context.Background(), "plugin")
	if err != nil {
		t.Fatal(err)
	}
	tb.Put("k", 1)
	if _, err := s.NewTable[string, int](context.Background(), "plugin"); err == nil {
		t.Fatal("a live name must not register twice")
	}
	if err := s.DropTable(context.Background(), "plugin"); err != nil {
		t.Fatal(err)
	}
	if _, ok := tb.Get("k"); ok {
		t.Fatal("dropping a table must clear it")
	}
	if _, err := s.NewTable[string, int](context.Background(), "plugin"); err != nil {
		t.Fatalf("a dropped name must register again: %v", err)
	}
}

// TestHoldsHandles checks that the marker changes nothing in memory: a table created with it reads
// as one created without it.
func TestHoldsHandles(t *testing.T) {
	s := newStore()
	t.Cleanup(s.Close)
	plain, err := s.NewTable[string, int](context.Background(), "plain")
	if err != nil {
		t.Fatal(err)
	}
	handles, err := s.NewTable[string, int](context.Background(), "handles", HoldsHandles())
	if err != nil {
		t.Fatal(err)
	}
	plain.Put("k", 1)
	handles.Put("k", 1)
	pv, pok := plain.Get("k")
	hv, hok := handles.Get("k")
	if !pok || !hok || pv != hv {
		t.Fatalf("plain got %v %v, handles got %v %v", pv, pok, hv, hok)
	}
}

// newGenerationTable returns a table with the generation gen reports, in a store of its own.
func newGenerationTable[V any](t *testing.T, gen func() uint64) *Table[string, V] {
	t.Helper()
	s, err := New()
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(s.Close)
	tb, err := s.NewTable[string, V](context.Background(), "test_generation", WithGeneration(gen))
	if err != nil {
		t.Fatalf("table: %v", err)
	}
	return tb
}

// storeAlways is a compute result kept with no TTL.
func storeAlways[V any](v V) (V, time.Duration, bool) { return v, 0, true }

// TestGetOrComputeCachesWithinGeneration checks a second read in one generation is a hit.
func TestGetOrComputeCachesWithinGeneration(t *testing.T) {
	gen := uint64(1)
	tb := newGenerationTable[int](t, func() uint64 { return gen })
	calls := 0
	compute := func() (int, time.Duration, bool) { calls++; return storeAlways(7) }
	if got := tb.GetOrCompute("k", compute); got != 7 {
		t.Fatalf("got %d", got)
	}
	if got := tb.GetOrCompute("k", compute); got != 7 || calls != 1 {
		t.Fatalf("got %d after %d computes, want a hit", got, calls)
	}
}

// TestGetOrComputeRecomputesOnGenerationChange checks a generation change makes the next read
// compute again.
func TestGetOrComputeRecomputesOnGenerationChange(t *testing.T) {
	gen := uint64(1)
	tb := newGenerationTable[int](t, func() uint64 { return gen })
	calls := 0
	compute := func() (int, time.Duration, bool) { calls++; return storeAlways(calls) }
	tb.GetOrCompute("k", compute)
	gen++
	if got := tb.GetOrCompute("k", compute); got != 2 || calls != 2 {
		t.Fatalf("got %d after %d computes, want a recompute", got, calls)
	}
}

// TestGetOrComputeStoresThePreComputeGeneration checks a value computed while the generation
// moved is not served afterwards.
func TestGetOrComputeStoresThePreComputeGeneration(t *testing.T) {
	gen := uint64(1)
	tb := newGenerationTable[int](t, func() uint64 { return gen })
	tb.GetOrCompute("k", func() (int, time.Duration, bool) {
		gen++ // the data changes while the value is computed
		return storeAlways(1)
	})
	if _, ok := tb.Get("k"); ok {
		t.Fatal("a value computed while the generation moved was served")
	}
	calls := 0
	if got := tb.GetOrCompute("k", func() (int, time.Duration, bool) { calls++; return storeAlways(2) }); got != 2 || calls != 1 {
		t.Fatalf("got %d after %d computes, want a fresh compute", got, calls)
	}
}

// TestGetOrComputeLeavesUnstoredResultsOut checks a result compute declines to store is returned
// but computed again next time.
func TestGetOrComputeLeavesUnstoredResultsOut(t *testing.T) {
	tb := newGenerationTable[[]int](t, func() uint64 { return 1 })
	calls := 0
	compute := func() ([]int, time.Duration, bool) { calls++; return []int{calls}, 0, false }
	if got := tb.GetOrCompute("k", compute); !slices.Equal(got, []int{1}) {
		t.Fatalf("first GetOrCompute = %v, want [1]", got)
	}
	if got := tb.GetOrCompute("k", compute); !slices.Equal(got, []int{2}) {
		t.Fatalf("second GetOrCompute = %v, want [2]", got)
	}
	if calls != 2 {
		t.Fatalf("computed %d times, want 2", calls)
	}
	if v, ok := tb.Get("k"); ok {
		t.Fatalf("an unstored result was kept: %v", v)
	}
}

// TestGetOrComputeKeepsAResultForItsTTL checks a result stored with a TTL is kept until it
// expires, while others stay.
func TestGetOrComputeKeepsAResultForItsTTL(t *testing.T) {
	tb := newGenerationTable[int](t, func() uint64 { return 1 })
	cur := time.Now()
	now = func() time.Time { return cur }
	t.Cleanup(func() { now = time.Now })
	tb.GetOrCompute("negative", func() (int, time.Duration, bool) { return 0, 5 * time.Second, true })
	tb.GetOrCompute("positive", func() (int, time.Duration, bool) { return storeAlways(9) })
	if v, ok := tb.Get("negative"); !ok || v != 0 {
		t.Fatalf("the TTL result was not kept before its TTL: got %d, %v", v, ok)
	}
	cur = cur.Add(6 * time.Second)
	if _, ok := tb.Get("negative"); ok {
		t.Fatal("the TTL result outlived its TTL")
	}
	if v, ok := tb.Get("positive"); !ok || v != 9 {
		t.Fatal("a result stored without a TTL expired")
	}
}

// TestGetOrComputeConcurrentReadsAndGenerationChanges checks concurrent computes and generation
// changes are race-free.
func TestGetOrComputeConcurrentReadsAndGenerationChanges(t *testing.T) {
	var gen atomic.Uint64
	tb := newGenerationTable[int](t, gen.Load)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range 200 {
				tb.GetOrCompute(fmt.Sprint(j%5), func() (int, time.Duration, bool) { return storeAlways(i) })
				if j%50 == 0 {
					gen.Add(1)
				}
			}
		}()
	}
	wg.Wait()
}

// TestTableLenAndFlush checks that Len counts only live entries and that Flush empties the table.
func TestTableLenAndFlush(t *testing.T) {
	clock := setClock(t, time.Unix(1_000_000, 0))
	s := newStore()
	defer s.Close()
	tb, err := s.NewTable[string, int](context.Background(), "len_flush", WithTTL(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	tb.Put("a", 1)
	tb.PutWithTTL("b", 2, 0)
	if n := tb.Len(); n != 2 {
		t.Fatalf("Len = %d, want 2", n)
	}
	*clock = clock.Add(2 * time.Minute)
	if n := tb.Len(); n != 1 {
		t.Fatalf("Len after the TTL = %d, want 1: an expired entry is not counted", n)
	}
	tb.Flush()
	if n := tb.Len(); n != 0 {
		t.Fatalf("Len after Flush = %d, want 0", n)
	}
	if _, ok := tb.Get("b"); ok {
		t.Fatal("Flush must drop every entry")
	}
}
