package livestore

import (
	"context"
	"fmt"
	"slices"
	"strings"
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
	usage, err := f.s.NewTable[string, *int](context.Background(), "usage", KeyedBy(Kind("group")))
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

// TestCompareAndSwapComparesThePointer checks what CompareAndSwap can tell: a new pointer for every
// write makes any write since a read fail the swap, even one storing an equal value, while storing
// a pointer that was stored before makes a read of it compare equal again.
func TestCompareAndSwapComparesThePointer(t *testing.T) {
	f := newFixture(t)
	f.load(t)
	usage, err := f.s.NewTable[string, *int](context.Background(), "usage", KeyedBy(Kind("group")))
	if err != nil {
		t.Fatal(err)
	}
	read, same, next := new(int), new(int), new(int)
	usage.Put("g1", read)
	usage.Put("g1", same) // a new pointer holding the same value
	if CompareAndSwap(usage, "g1", read, next) {
		t.Fatal("a write of a new pointer since the read must fail the swap")
	}
	usage.Put("g1", next)
	usage.Put("g1", read) // the pointer read earlier, stored again
	if !CompareAndSwap(usage, "g1", read, next) {
		t.Fatal("a pointer stored again compares equal to a read of it")
	}
}

// TestKeyedByNeedsAKindAndStringKeys checks that a table KeyedBy a kind must name a registered
// kind and have string keys: with no kind, or keys an entity ID cannot be, removing a record would
// never clear its entry. A refused name stays free.
func TestKeyedByNeedsAKindAndStringKeys(t *testing.T) {
	f := newFixture(t)
	f.load(t)
	ctx := context.Background()
	if _, err := f.s.NewTable[string, *int](ctx, "usage", KeyedBy(Kind(""))); err == nil || !strings.Contains(err.Error(), "keyed by no kind") {
		t.Fatalf("KeyedBy with an empty kind = %v, want an error", err)
	}
	if _, err := f.s.NewTable[string, *int](ctx, "usage", KeyedBy(Kind("nope"))); err == nil || !strings.Contains(err.Error(), "unknown kind") {
		t.Fatalf("KeyedBy with an unknown kind = %v, want an error", err)
	}
	if _, err := f.s.NewTable[int, *int](ctx, "usage", KeyedBy(Kind("group"))); err == nil || !strings.Contains(err.Error(), "keys must be strings") {
		t.Fatalf("KeyedBy with int keys = %v, want an error", err)
	}
	type groupID string
	if _, err := f.s.NewTable[groupID, *int](ctx, "usage", KeyedBy(Kind("group"))); err == nil || !strings.Contains(err.Error(), "keys must be strings") {
		t.Fatalf("KeyedBy with a named string key type = %v, want an error, since removal looks entries up by plain string", err)
	}
	if _, err := f.s.NewTable[string, int](ctx, "usage"); err != nil {
		t.Fatalf("a name KeyedBy refused must stay free: %v", err)
	}
}

// TestDropTable checks that a table reports its name and that a dropped name can be registered again.
func TestDropTable(t *testing.T) {
	s := newStore()
	defer s.Close()
	tb, err := s.NewTable[string, int](context.Background(), "plugin")
	if err != nil {
		t.Fatal(err)
	}
	tb.Put("k", 1)
	if tb.Name() != "plugin" {
		t.Fatalf("a table must report the name it was registered under, got %q", tb.Name())
	}
	if _, err := s.NewTable[string, int](context.Background(), "plugin"); err == nil {
		t.Fatal("a live name must not register twice")
	}
	if err := s.DropTable(context.Background(), tb.Name()); err != nil {
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

// observedSet is one call a set observer heard.
type observedSet struct {
	key, value           string
	writtenAt, expiresAt int64
}

// observedDelete is one call a delete observer heard.
type observedDelete struct {
	key       string
	deletedAt int64
}

// observed records what a table's observers hear.
type observed struct {
	mu      sync.Mutex
	sets    []observedSet
	deletes []observedDelete
}

// observe installs observers on tb that record into a new observed.
func observe(tb *Table[string, string]) *observed {
	o := &observed{}
	tb.OnSet(func(k, v string, writtenAt, expiresAt int64) {
		o.mu.Lock()
		defer o.mu.Unlock()
		o.sets = append(o.sets, observedSet{k, v, writtenAt, expiresAt})
	})
	tb.OnDelete(func(k string, deletedAt int64) {
		o.mu.Lock()
		defer o.mu.Unlock()
		o.deletes = append(o.deletes, observedDelete{k, deletedAt})
	})
	return o
}

// timedTableForTest creates a table of strings that keeps write times on a new store.
func timedTableForTest(t *testing.T) *Table[string, string] {
	t.Helper()
	s := newStore()
	t.Cleanup(s.Close)
	tb, err := s.NewTable[string, string](context.Background(), "timed", WithWriteTimes())
	if err != nil {
		t.Fatal(err)
	}
	return tb
}

// TestObserversHearEachWrite checks what each write hands the observers: the value, the write
// time and the absolute expiry; a claim or a swap only when it is stored; a delete only when the
// key was there.
func TestObserversHearEachWrite(t *testing.T) {
	clock := setClock(t, time.Unix(1_000_000, 0))
	tb := timedTableForTest(t)
	o := observe(tb)
	at := clock.UnixNano()

	tb.PutWithTTL("a", "openai/gpt", time.Minute)
	tb.PutWithTTL("b", "never", 0)
	if !tb.PutIfAbsent("c", "first", time.Minute) {
		t.Fatal("the first claim must be stored")
	}
	if tb.PutIfAbsent("c", "second", time.Minute) {
		t.Fatal("a live claim must not be taken")
	}
	if CompareAndSwap(tb, "b", "stale", "lost") {
		t.Fatal("a swap from a value the key does not hold must fail")
	}
	if !CompareAndSwap(tb, "b", "never", "swapped") {
		t.Fatal("a swap from the held value must be stored")
	}
	want := []observedSet{
		{"a", "openai/gpt", at, at + int64(time.Minute)},
		{"b", "never", at, 0},
		{"c", "first", at, at + int64(time.Minute)},
		{"b", "swapped", at, 0},
	}
	if len(o.sets) != len(want) {
		t.Fatalf("sets = %+v, want %+v", o.sets, want)
	}
	for i := range want {
		if o.sets[i] != want[i] {
			t.Fatalf("set %d = %+v, want %+v", i, o.sets[i], want[i])
		}
	}

	tb.Delete("a")
	tb.Delete("missing")
	if len(o.deletes) != 1 || o.deletes[0] != (observedDelete{"a", at}) {
		t.Fatalf("deletes = %+v, want only a", o.deletes)
	}
}

// TestDeleteOfAnExpiredEntryIsObserved checks that a delete is observed when the key was held,
// expired or not.
func TestDeleteOfAnExpiredEntryIsObserved(t *testing.T) {
	clock := setClock(t, time.Unix(1_000_000, 0))
	tb := timedTableForTest(t)
	o := observe(tb)
	tb.PutWithTTL("k", "v", time.Second)
	*clock = clock.Add(time.Minute)
	tb.Delete("k")
	if len(o.deletes) != 1 {
		t.Fatalf("deletes = %+v, want one", o.deletes)
	}
}

// TestGetAndDeleteTakesOnce checks the single-use take: one caller gets the value, the delete
// observer hears it once, and an expired value is removed without being returned or observed.
func TestGetAndDeleteTakesOnce(t *testing.T) {
	clock := setClock(t, time.Unix(1_000_000, 0))
	tb := timedTableForTest(t)
	o := observe(tb)

	tb.PutWithTTL("upload:1", "session", time.Minute)
	if v, ok := tb.GetAndDelete("upload:1"); !ok || v != "session" {
		t.Fatalf("first take = %q, %v", v, ok)
	}
	if _, ok := tb.GetAndDelete("upload:1"); ok {
		t.Fatal("a second take must find nothing")
	}
	if len(o.deletes) != 1 {
		t.Fatalf("deletes = %+v, want one", o.deletes)
	}

	tb.PutWithTTL("upload:2", "stale", time.Second)
	*clock = clock.Add(time.Minute)
	if _, ok := tb.GetAndDelete("upload:2"); ok {
		t.Fatal("an expired value must not be taken")
	}
	if _, ok := tb.Get("upload:2"); ok {
		t.Fatal("an expired value must be removed")
	}
	if len(o.deletes) != 1 {
		t.Fatalf("an expired take must not be observed: deletes = %+v", o.deletes)
	}
}

// TestObserversCanBeRemoved checks that a table without observers keeps working, and that nil
// removes an installed observer.
func TestObserversCanBeRemoved(t *testing.T) {
	setClock(t, time.Unix(1_000_000, 0))
	tb := timedTableForTest(t)
	tb.PutWithTTL("k", "v", time.Minute)
	if v, ok := tb.Get("k"); !ok || v != "v" {
		t.Fatalf("Get = %q, %v", v, ok)
	}
	tb.Delete("k")

	o := observe(tb)
	tb.OnSet(nil)
	tb.OnDelete(nil)
	tb.Put("k", "v")
	tb.Delete("k")
	if len(o.sets) != 0 || len(o.deletes) != 0 {
		t.Fatalf("removed observers heard %+v %+v", o.sets, o.deletes)
	}
}

// TestObserverHearsNoWriteTimeWithoutWriteTimes checks that a table without write times hands its
// set observer a zero write time and still the absolute expiry.
func TestObserverHearsNoWriteTimeWithoutWriteTimes(t *testing.T) {
	clock := setClock(t, time.Unix(1_000_000, 0))
	s := newStore()
	t.Cleanup(s.Close)
	tb, err := s.NewTable[string, string](context.Background(), "untimed")
	if err != nil {
		t.Fatal(err)
	}
	o := observe(tb)
	tb.PutWithTTL("k", "v", time.Minute)
	if len(o.sets) != 1 || o.sets[0] != (observedSet{"k", "v", 0, clock.Add(time.Minute).UnixNano()}) {
		t.Fatalf("sets = %+v", o.sets)
	}
}

// TestConditionalWritesKeepTheLaterTime checks the write-time rule both ways, for sets and
// deletes: an older write is skipped, one at the same time or later applies, and a given expiry
// is kept as the absolute time it names.
func TestConditionalWritesKeepTheLaterTime(t *testing.T) {
	clock := setClock(t, time.Unix(1_000_000, 0))
	tb := timedTableForTest(t)
	at := clock.UnixNano()

	tb.PutWithTTL("k:1", "local", 0)
	if tb.PutIfNewer("k:1", "older", at-1, 0) {
		t.Fatal("an older write must report that it stored nothing")
	}
	if v, _ := tb.Get("k:1"); v != "local" {
		t.Fatalf("an older write must be skipped, got %q", v)
	}
	if !tb.PutIfNewer("k:1", "tie", at, 0) {
		t.Fatal("a write at the same time must report that it stored")
	}
	if v, _ := tb.Get("k:1"); v != "tie" {
		t.Fatalf("a write at the same time must apply, got %q", v)
	}
	if tb.DeleteIfNewer("k:1", at-1) {
		t.Fatal("a delete older than the write must report that it removed nothing")
	}
	if _, ok := tb.Get("k:1"); !ok {
		t.Fatal("a delete older than the write must be skipped")
	}
	if !tb.DeleteIfNewer("k:1", at) {
		t.Fatal("a delete at the write time must report that it removed the entry")
	}
	if _, ok := tb.Get("k:1"); ok {
		t.Fatal("a delete at the write time must apply")
	}

	expires := at + int64(time.Second)
	tb.PutIfNewer("k:2", "given", at, expires)
	*clock = clock.Add(2 * time.Second)
	if _, ok := tb.Get("k:2"); ok {
		t.Fatal("an entry must expire at the absolute time it was given")
	}
	tb.PutWithTTL("k:3", "local", 0)
	tb.PutIfNewer("k:3", "newer", clock.UnixNano()+1, 0)
	if v, _ := tb.Get("k:3"); v != "newer" {
		t.Fatalf("a newer write must apply, got %q", v)
	}
	if tb.PutIfNewer("k:3", "older", clock.UnixNano(), 0) {
		t.Fatal("a write older than the one PutIfNewer stored must be skipped")
	}
}

// TestConditionalWritesAreNotObserved checks that PutIfNewer and DeleteIfNewer never reach the
// observers.
func TestConditionalWritesAreNotObserved(t *testing.T) {
	setClock(t, time.Unix(1_000_000, 0))
	tb := timedTableForTest(t)
	o := observe(tb)
	tb.PutIfNewer("k", "v", 5, 0)
	tb.DeleteIfNewer("k", 6)
	if len(o.sets) != 0 || len(o.deletes) != 0 {
		t.Fatalf("conditional writes were observed: %+v %+v", o.sets, o.deletes)
	}
}

// structValue is a struct value as a consumer stores it.
type structValue struct {
	KeyID string
	Count int
}

// TestConditionalWritesNeedWriteTimes checks that a table without write times takes no
// conditional write, and that any value type works in one that keeps them.
func TestConditionalWritesNeedWriteTimes(t *testing.T) {
	setClock(t, time.Unix(1_000_000, 0))
	s := newStore()
	t.Cleanup(s.Close)
	untimed, err := s.NewTable[string, string](context.Background(), "untimed")
	if err != nil {
		t.Fatal(err)
	}
	if untimed.PutIfNewer("k", "v", 5, 0) {
		t.Fatal("a table without write times must not take a conditional write")
	}
	if _, ok := untimed.Get("k"); ok {
		t.Fatal("a refused conditional write must store nothing")
	}
	untimed.Put("k", "v")
	if untimed.DeleteIfNewer("k", 5) {
		t.Fatal("a table without write times must not take a conditional delete")
	}
	if _, ok := untimed.Get("k"); !ok {
		t.Fatal("a refused conditional delete must leave the entry")
	}

	structs, err := s.NewTable[string, structValue](context.Background(), "structs", WithWriteTimes())
	if err != nil {
		t.Fatal(err)
	}
	if !structs.KeepsWriteTimes() || untimed.KeepsWriteTimes() {
		t.Fatal("KeepsWriteTimes must report the option")
	}
	structs.PutIfNewer("m", structValue{KeyID: "k1", Count: 3}, 5, 0)
	if v, ok := structs.Get("m"); !ok || v != (structValue{KeyID: "k1", Count: 3}) {
		t.Fatalf("Get = %+v, %v", v, ok)
	}
}

// TestWriteTimesRefuseAMemo checks that a table cannot keep write times and be a memo, and that
// the refused name stays free.
func TestWriteTimesRefuseAMemo(t *testing.T) {
	s := newStore()
	t.Cleanup(s.Close)
	if _, err := s.NewTable[string, string](context.Background(), "memo", WithWriteTimes(), WithGeneration(func() uint64 { return 1 })); err == nil {
		t.Fatal("a memo that keeps write times must fail")
	}
	if _, err := s.NewTable[string, string](context.Background(), "memo", WithWriteTimes()); err != nil {
		t.Fatalf("a refused table must leave its name free: %v", err)
	}
}

// TestTableWritesReadTheClockOnlyWhenTheyNeedIt checks that a write asks the clock only for what
// needs the time: an expiry, a write time or a delete observer. Tables with none of those serve
// per-request writes, which should pay no clock read for features they do not use.
func TestTableWritesReadTheClockOnlyWhenTheyNeedIt(t *testing.T) {
	var reads atomic.Int64
	at := time.Unix(1_000_000, 0)
	now = func() time.Time { reads.Add(1); return at }
	t.Cleanup(func() { now = time.Now })
	s := newStore()
	defer s.Close()
	ctx := context.Background()
	plain, err := s.NewTable[string, int](ctx, "clock_plain")
	if err != nil {
		t.Fatal(err)
	}
	memo, err := s.NewTable[string, int](ctx, "clock_memo", WithGeneration(func() uint64 { return 1 }))
	if err != nil {
		t.Fatal(err)
	}
	timed, err := s.NewTable[string, int](ctx, "clock_write_times", WithWriteTimes())
	if err != nil {
		t.Fatal(err)
	}

	// count runs op and checks it read the clock want times.
	count := func(op string, want int64, run func()) {
		t.Helper()
		before := reads.Load()
		run()
		if n := reads.Load() - before; n != want {
			t.Errorf("%s read the clock %d times, want %d", op, n, want)
		}
	}
	count("Put on a table with no TTL", 0, func() { plain.Put("a", 1) })
	count("PutIfAbsent with no TTL", 0, func() { plain.PutIfAbsent("b", 2, 0) })
	count("CompareAndSwap with no TTL", 0, func() { CompareAndSwap(plain, "a", 1, 3) })
	count("GetOrCompute on a memo with no TTL", 0, func() { memo.GetOrCompute("m", func() (int, time.Duration, bool) { return 4, 0, true }) })
	count("Delete with no delete observer", 0, func() { plain.Delete("a") })

	count("PutWithTTL", 1, func() { plain.PutWithTTL("c", 5, time.Minute) })
	count("Put on a table that keeps write times", 1, func() { timed.Put("d", 6) })
	plain.OnDelete(func(string, int64) {})
	count("Delete with a delete observer", 1, func() { plain.Delete("b") })
}
