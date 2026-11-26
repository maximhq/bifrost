package livestore

import (
	"context"
	"crypto/md5"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// blob is an interned value the table tests reference from their entries.
type blob struct {
	ID   Hash
	Body string
}

// refRec is a table value whose B field is interned.
type refRec struct{ B *blob }

// refOf builds a value holding its own copy of the blob with body, as a writer builds it.
func refOf(body string) *refRec { return &refRec{B: &blob{ID: md5.Sum([]byte(body)), Body: body}} }

// blobHash is a blob's key in its pool.
func blobHash(body string) Hash { return md5.Sum([]byte(body)) }

// blobPart is the Interns part of a refRec.
func blobPart(r *refRec) **blob { return &r.B }

// wantUses fails the test unless the copy under h in in has want uses.
func wantUses[T any](t *testing.T, in *Interned[T], h Hash, want int, what string) {
	t.Helper()
	if got, _ := usesOf(in, h); got != want {
		t.Fatalf("%s: %d uses, want %d", what, got, want)
	}
}

// newBlobStore returns a store with an interned blob pool.
func newBlobStore(t *testing.T) (*Store, *Interned[blob]) {
	t.Helper()
	s := newStore()
	t.Cleanup(s.Close)
	blobs, err := s.RegisterInterned(Kind("blob"), func(b *blob) Hash { return b.ID })
	if err != nil {
		t.Fatal(err)
	}
	return s, blobs
}

// newRefTable creates a table of refRec values interning their blobs.
func newRefTable(t *testing.T, s *Store, blobs *Interned[blob], name string, opts ...TableOption) *Table[string, *refRec] {
	t.Helper()
	tbl, err := s.NewTable[string, *refRec](context.Background(), name, append(opts, Interns(blobs, blobPart))...)
	if err != nil {
		t.Fatal(err)
	}
	return tbl
}

// TestTableInternsSharesAndCountsWritesAndDeletes checks that entries written with their own copies
// hold one shared copy, counted per entry; that replacing a value moves its use; that a delete gives
// it back; and that GetOrCompute returns the value it stored, holding the shared copy.
func TestTableInternsSharesAndCountsWritesAndDeletes(t *testing.T) {
	s, blobs := newBlobStore(t)
	tbl := newRefTable(t, s, blobs, "refs")
	one, two := blobHash("one"), blobHash("two")

	tbl.Put("a", refOf("one"))
	tbl.Put("b", refOf("one"))
	a, _ := tbl.Get("a")
	b, _ := tbl.Get("b")
	if a.B != b.B {
		t.Fatal("entries written with equal copies must share one")
	}
	wantUses(t, blobs, one, 2, "two entries on one")
	tbl.Put("a", refOf("two"))
	wantUses(t, blobs, one, 1, "a moved off one")
	wantUses(t, blobs, two, 1, "a moved onto two")
	tbl.Put("a", refOf("two"))
	wantUses(t, blobs, two, 1, "the same value again")
	tbl.Put("z", &refRec{})
	tbl.Delete("b")
	wantUses(t, blobs, one, 0, "b deleted")
	if _, ok := tbl.GetAndDelete("a"); !ok {
		t.Fatal("a must be there")
	}
	wantUses(t, blobs, two, 0, "a taken")
	tbl.Delete("absent")
	got := tbl.GetOrCompute("c", func() (*refRec, time.Duration, bool) { return refOf("one"), 0, true })
	tbl.GetOrCompute("d", func() (*refRec, time.Duration, bool) { return refOf("two"), 0, false })
	wantUses(t, blobs, one, 1, "a computed value that is stored")
	wantUses(t, blobs, two, 0, "a computed value that is not stored")
	if shared, _ := sharedCopy(blobs, one); got.B != shared {
		t.Fatal("GetOrCompute must return the value holding the shared copy")
	}
}

// TestTableInternsConditionalWrites checks that a conditional write counts only what it stores, and
// that one replacing an expired entry gives back that entry's use: the entry was still in the table.
func TestTableInternsConditionalWrites(t *testing.T) {
	clock := setClock(t, time.Unix(1_000_000, 0))
	s, blobs := newBlobStore(t)
	tbl := newRefTable(t, s, blobs, "refs")
	one, two, three := blobHash("one"), blobHash("two"), blobHash("three")

	tbl.Put("a", refOf("one"))
	held, _ := tbl.Get("a")
	if tbl.PutIfAbsent("a", refOf("two"), 0) {
		t.Fatal("a holds a live value")
	}
	if CompareAndSwap(tbl, "a", refOf("one"), refOf("two")) {
		t.Fatal("a holds another pointer")
	}
	wantUses(t, blobs, one, 1, "refused writes")
	wantUses(t, blobs, two, 0, "refused writes")
	if !CompareAndSwap(tbl, "a", held, refOf("two")) {
		t.Fatal("a holds the value read")
	}
	wantUses(t, blobs, one, 0, "swapped off one")
	wantUses(t, blobs, two, 1, "swapped onto two")

	tbl.PutWithTTL("b", refOf("one"), time.Second)
	tbl.PutWithTTL("c", refOf("one"), time.Second)
	tbl.PutWithTTL("d", refOf("one"), time.Second)
	*clock = clock.Add(2 * time.Second)
	wantUses(t, blobs, one, 3, "expired entries still in the table")
	if !tbl.PutIfAbsent("b", refOf("three"), 0) {
		t.Fatal("b's value expired")
	}
	if !CompareAndSwap(tbl, "c", nil, refOf("three")) {
		t.Fatal("c's value expired, so it compares as nil")
	}
	if _, ok := tbl.GetAndDelete("d"); ok {
		t.Fatal("d's value expired")
	}
	wantUses(t, blobs, one, 0, "the expired entries were replaced or taken")
	wantUses(t, blobs, three, 2, "the replacements")
}

// TestTableInternsWriteTimes checks PutIfNewer and DeleteIfNewer: a refused write or delete touches
// no count.
func TestTableInternsWriteTimes(t *testing.T) {
	s, blobs := newBlobStore(t)
	tbl := newRefTable(t, s, blobs, "replicated", WithWriteTimes())
	one, two := blobHash("one"), blobHash("two")

	tbl.PutIfNewer("a", refOf("one"), 10, 0)
	if tbl.PutIfNewer("a", refOf("two"), 5, 0) {
		t.Fatal("an older write must be refused")
	}
	wantUses(t, blobs, one, 1, "a refused older write")
	wantUses(t, blobs, two, 0, "a refused older write")
	if tbl.DeleteIfNewer("a", 5) {
		t.Fatal("an older delete must be refused")
	}
	wantUses(t, blobs, one, 1, "a refused older delete")
	tbl.PutIfNewer("a", refOf("two"), 20, 0)
	wantUses(t, blobs, one, 0, "a newer write moved off one")
	if !tbl.DeleteIfNewer("a", 30) {
		t.Fatal("a newer delete removes")
	}
	wantUses(t, blobs, two, 0, "a newer delete")
}

// TestTableInternsReleaseAsEntriesLeave checks that expiry, a stale generation, Flush and DropTable
// give back every use, and that the sweep drops a copy a grace period after its last entry left
// and keeps one an entry holds.
func TestTableInternsReleaseAsEntriesLeave(t *testing.T) {
	clock := setClock(t, time.Unix(1_000_000, 0))
	s, blobs := newBlobStore(t)
	var gen atomic.Uint64
	expiring := newRefTable(t, s, blobs, "expiring", WithTTL(time.Minute))
	memo := newRefTable(t, s, blobs, "memo", WithGeneration(gen.Load))
	flushed := newRefTable(t, s, blobs, "flushed")
	dropped := newRefTable(t, s, blobs, "dropped")
	kept := newRefTable(t, s, blobs, "kept")
	expiring.Put("a", refOf("e"))
	memo.Put("a", refOf("m"))
	flushed.Put("a", refOf("f"))
	dropped.Put("a", refOf("d"))
	kept.Put("a", refOf("k"))

	*clock = clock.Add(2 * time.Minute)
	gen.Add(1)
	wantUses(t, blobs, blobHash("e"), 1, "expired, not swept yet")
	wantUses(t, blobs, blobHash("m"), 1, "stale generation, not swept yet")
	s.sweep()
	wantUses(t, blobs, blobHash("e"), 0, "expired and swept")
	wantUses(t, blobs, blobHash("m"), 0, "stale and swept")
	flushed.Flush()
	wantUses(t, blobs, blobHash("f"), 0, "flushed")
	if err := s.DropTable(context.Background(), "dropped"); err != nil {
		t.Fatal(err)
	}
	wantUses(t, blobs, blobHash("d"), 0, "dropped")
	if _, ok := sharedCopy(blobs, blobHash("e")); !ok {
		t.Fatal("a copy released now must survive the grace period")
	}

	*clock = clock.Add(internedGrace + time.Second)
	s.sweep()
	for _, body := range []string{"e", "m", "f", "d"} {
		if _, ok := sharedCopy(blobs, blobHash(body)); ok {
			t.Fatalf("copy %s: a copy no entry used for the grace period must be dropped", body)
		}
	}
	if _, ok := sharedCopy(blobs, blobHash("k")); !ok {
		t.Fatal("a copy a table entry holds must survive every sweep")
	}
}

// tagHolder is a table value holding a tag set from the fixture's pool.
type tagHolder struct{ T *tagSet }

// TestInternsSharesOneCountAcrossCollectionsAndTables checks that a pool a collection uses counts a
// table's and a state table's entries in the same count, that they are handed the collection's
// copy, that a state entry gives its use back when its record is removed, and that the copy lives
// while anything holds it.
func TestInternsSharesOneCountAcrossCollectionsAndTables(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	clock := setClock(t, time.Unix(1_000_000, 0))
	f.load(t)
	g1, _ := f.groups.Get("g1")
	shared := g1.Set
	ha := f.tags.hash(shared)
	wantUses(t, f.tags, ha, 2, "g1 and g2 share tag a")

	part := func(h *tagHolder) **tagSet { return &h.T }
	state, err := f.s.RegisterState[tagHolder](context.Background(), "group_tag_copy", Kind("group"), Interns(f.tags, part))
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.s.NewTable[string, *tagHolder](context.Background(), "tag_refs", Interns(f.tags, part))
	if err != nil {
		t.Fatal(err)
	}
	state.Put("g3", &tagHolder{T: &tagSet{Tag: "a"}})
	other.Put("x", &tagHolder{T: &tagSet{Tag: "a"}})
	if x, _ := other.Get("x"); x.T != shared {
		t.Fatal("a table entry must be handed the copy the records hold")
	}
	wantUses(t, f.tags, ha, 4, "two records and two table entries")

	f.refresh(t, Ref{Kind: "group", ID: "g1", Remove: true}, Ref{Kind: "group", ID: "g2", Remove: true})
	f.refresh(t, Ref{Kind: "group", ID: "g3", Remove: true})
	wantUses(t, f.tags, ha, 1, "the records and g3's state entry are gone")
	*clock = clock.Add(internedGrace + time.Second)
	f.s.sweep()
	if !f.held(shared) {
		t.Fatal("a copy a table entry holds must outlive the records that used it")
	}
	other.Delete("x")
	*clock = clock.Add(internedGrace + time.Second)
	f.s.sweep()
	if f.held(shared) {
		t.Fatal("a copy nothing used for the grace period must be dropped")
	}
}

// pairVal is a table value held by value, not by pointer, with an interned part.
type pairVal struct {
	Name string
	A    *tagSet
}

// TestTableInternsStructValues checks a table holding values rather than pointers: the copy it
// stores takes the shared part, so entries written with equal copies read back one.
func TestTableInternsStructValues(t *testing.T) {
	s, _ := newBlobStore(t)
	tags, err := s.RegisterInterned(Kind("tag"), func(ts *tagSet) Hash { return md5.Sum([]byte(ts.Tag)) })
	if err != nil {
		t.Fatal(err)
	}
	tbl, err := s.NewTable[string, pairVal](context.Background(), "pairs", Interns(tags, func(p *pairVal) **tagSet { return &p.A }))
	if err != nil {
		t.Fatal(err)
	}
	tbl.Put("a", pairVal{Name: "a", A: &tagSet{Tag: "x"}})
	tbl.Put("b", pairVal{Name: "b", A: &tagSet{Tag: "x"}})
	a, _ := tbl.Get("a")
	b, _ := tbl.Get("b")
	if a.A != b.A {
		t.Fatal("stored values written with equal copies must share one")
	}
	wantUses(t, tags, md5.Sum([]byte("x")), 2, "two stored values")
	tbl.Delete("a")
	wantUses(t, tags, md5.Sum([]byte("x")), 1, "one deleted")
}

// TestTableInternsSeveralPartsAndTheWrongType checks that a table interning two parts counts each in
// its own pool, and that an Interns for another type is refused at creation, naming the table.
func TestTableInternsSeveralPartsAndTheWrongType(t *testing.T) {
	s, blobs := newBlobStore(t)
	hash := func(ts *tagSet) Hash { return md5.Sum([]byte(ts.Tag)) }
	left, err := s.RegisterInterned(Kind("left"), hash)
	if err != nil {
		t.Fatal(err)
	}
	right, err := s.RegisterInterned(Kind("right"), hash)
	if err != nil {
		t.Fatal(err)
	}
	tbl, err := s.NewTable[string, *pairRec](context.Background(), "pairs",
		Interns(left, func(p *pairRec) **tagSet { return &p.A }),
		Interns(right, func(p *pairRec) **tagSet { return &p.B }))
	if err != nil {
		t.Fatal(err)
	}
	tbl.Put("k", &pairRec{ID: "k", A: &tagSet{Tag: "x"}, B: &tagSet{Tag: "y"}})
	wantUses(t, left, hash(&tagSet{Tag: "x"}), 1, "first part")
	wantUses(t, right, hash(&tagSet{Tag: "y"}), 1, "second part")
	tbl.Delete("k")
	wantUses(t, left, hash(&tagSet{Tag: "x"}), 0, "first part after delete")
	wantUses(t, right, hash(&tagSet{Tag: "y"}), 0, "second part after delete")

	if _, err := s.NewTable[string, string](context.Background(), "wrong", Interns(blobs, blobPart)); err == nil || !strings.Contains(err.Error(), `"wrong"`) {
		t.Fatalf("NewTable with an Interns for another type = %v, want an error naming the table", err)
	}
	if _, err := s.Register(Kind("blob_owner"), nil, emptyLoader[owner]()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterState[blob](context.Background(), "wrong_state", Kind("blob_owner"), Interns(blobs, blobPart)); err == nil || !strings.Contains(err.Error(), `"wrong_state"`) {
		t.Fatalf("RegisterState with an Interns for another type = %v, want an error naming the table", err)
	}
	if _, err := s.NewTable[string, *refRec](context.Background(), "wrong", Interns(blobs, blobPart)); err != nil {
		t.Fatalf("a refused table must not keep its name: %v", err)
	}
}

// TestCacheInterns checks that every cache write path counts through its entries table: an
// installed fill shares its value's part, eviction, the validator, Flush and expiry give the use
// back, and a fill not installed holds nothing.
func TestCacheInterns(t *testing.T) {
	clock := setClock(t, time.Unix(1_000_000, 0))
	s, blobs := newBlobStore(t)
	valid := atomic.Bool{}
	valid.Store(true)
	c, err := s.NewCache[*refRec](context.Background(), "blob_cache",
		Interns(blobs, blobPart),
		WithValidator(func(*refRec) bool { return valid.Load() }),
		WithCacheTTL(func(r *refRec) time.Duration {
			if r.B.Body == "run out" {
				return -1 // run out already: not installed
			}
			return time.Minute
		}))
	if err != nil {
		t.Fatal(err)
	}
	fill := func(key, body string) *refRec {
		t.Helper()
		v, err := c.Fill(context.Background(), key, func() (*refRec, string, error) { return refOf(body), "", nil })
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	one := blobHash("cached")
	a, b := fill("a", "cached"), fill("b", "cached")
	fill("c", "cached")
	fill("d", "cached")
	fill("gone", "run out")
	if a.B != b.B {
		t.Fatal("installed fills of equal copies must share one")
	}
	wantUses(t, blobs, one, 4, "four installed fills")
	wantUses(t, blobs, blobHash("run out"), 0, "a fill that was not installed")
	c.Evict("a")
	c.EvictWhere(func(k string) bool { return k == "b" })
	wantUses(t, blobs, one, 2, "two evicted")
	valid.Store(false)
	c.Get("c")
	valid.Store(true)
	wantUses(t, blobs, one, 1, "one the validator rejected")
	*clock = clock.Add(2 * time.Minute)
	s.sweep()
	wantUses(t, blobs, one, 0, "the last one expired and was swept")
	fill("e", "cached")
	c.Flush()
	wantUses(t, blobs, one, 0, "flushed")
}

// TestTableInternsRewritesNoPublishedField checks that writing a value the table already holds,
// as refreshing its TTL or storing it under a second key does, leaves its interned field alone:
// the field already holds the shared copy, and readers of the published value may be reading it.
// Run with -race, a rewrite of the field races with the reader.
func TestTableInternsRewritesNoPublishedField(t *testing.T) {
	s, blobs := newBlobStore(t)
	tbl := newRefTable(t, s, blobs, "refs", WithTTL(time.Minute))
	tbl.Put("a", refOf("one"))
	held, _ := tbl.Get("a")
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				if held.B.Body != "one" {
					t.Error("a published value's interned field changed")
					return
				}
			}
		}
	}()
	for range 1000 {
		tbl.PutWithTTL("a", held, time.Minute)
		tbl.Put("b", held)
	}
	close(stop)
	wg.Wait()
	wantUses(t, blobs, blobHash("one"), 2, "two entries hold the value")
}

// TestTableInternsGetDoesNotAllocate checks that reading a table that interns costs what reading
// any table costs: entries hold the shared copy, so a read looks nothing up.
func TestTableInternsGetDoesNotAllocate(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector allocates")
	}
	s, blobs := newBlobStore(t)
	tbl := newRefTable(t, s, blobs, "refs")
	tbl.Put("a", refOf("one"))
	if allocs := testing.AllocsPerRun(100, func() { tbl.Get("a") }); allocs != 0 {
		t.Fatalf("Get allocated %v times, want 0", allocs)
	}
}

// TestTableInternsCountsBalanceUnderConcurrentWrites runs every write path of every table flavour
// at once, with the table sweep running beside them, and checks that each copy's count equals the
// entries holding it, expired ones included, and that Flush brings every count to zero.
func TestTableInternsCountsBalanceUnderConcurrentWrites(t *testing.T) {
	s, blobs := newBlobStore(t)
	var gen atomic.Uint64
	tables := map[string]*Table[string, *refRec]{
		"plain":       newRefTable(t, s, blobs, "plain"),
		"ttl":         newRefTable(t, s, blobs, "ttl", WithTTL(time.Millisecond)),
		"write_times": newRefTable(t, s, blobs, "write_times", WithWriteTimes()),
		"memo":        newRefTable(t, s, blobs, "memo", WithGeneration(gen.Load)),
	}
	bodies := []string{"v0", "v1", "v2", "v3"}
	for name, tbl := range tables {
		t.Run(name, func(t *testing.T) {
			stop := make(chan struct{})
			var sweeper sync.WaitGroup
			sweeper.Add(1)
			go func() {
				defer sweeper.Done()
				for {
					select {
					case <-stop:
						return
					default:
						tbl.sweep(time.Now().UnixNano())
						gen.Add(1)
					}
				}
			}()
			var wg sync.WaitGroup
			for w := range 8 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					r := rand.New(rand.NewPCG(uint64(w), 1))
					for range 10_000 {
						k := fmt.Sprint("k", r.IntN(16))
						v := refOf(bodies[r.IntN(len(bodies))])
						switch r.IntN(9) {
						case 0:
							tbl.Put(k, v)
						case 1:
							tbl.PutWithTTL(k, v, time.Duration(r.IntN(2))*time.Millisecond)
						case 2:
							tbl.PutIfAbsent(k, v, 0)
						case 3:
							cur, _ := tbl.Get(k)
							CompareAndSwap(tbl, k, cur, v)
						case 4:
							tbl.Delete(k)
						case 5:
							tbl.GetAndDelete(k)
						case 6:
							tbl.GetOrCompute(k, func() (*refRec, time.Duration, bool) { return v, 0, r.IntN(2) == 0 })
						case 7:
							tbl.PutIfNewer(k, v, time.Now().UnixNano()-int64(r.IntN(1000)), 0)
						case 8:
							tbl.DeleteIfNewer(k, time.Now().UnixNano()-int64(r.IntN(1000)))
						}
					}
				}()
			}
			wg.Wait()

			// The sweep keeps running while the counts are checked, as the store's own sweep may
			// once the test outlives its first tick, so the check holds every shard lock from the
			// snapshot through the comparison and sees one state.
			func() {
				for i := range tbl.shards {
					tbl.shards[i].mu.Lock()
				}
				defer func() {
					for i := range tbl.shards {
						tbl.shards[i].mu.Unlock()
					}
				}()
				held := map[Hash]int{}
				for i := range tbl.shards {
					sh := &tbl.shards[i]
					for _, v := range sh.plain {
						held[v.B.ID]++
					}
					for _, e := range sh.m {
						held[e.v.B.ID]++
					}
				}
				for _, body := range bodies {
					wantUses(t, blobs, blobHash(body), held[blobHash(body)], "count against the entries in the table")
				}
			}()
			close(stop)
			sweeper.Wait()
			tbl.Flush()
			for _, body := range bodies {
				wantUses(t, blobs, blobHash(body), 0, "after Flush")
			}
		})
	}
}

// BenchmarkTablePut measures a write to a table without Interns, the cost Interns must not add to
// tables that do not use it.
func BenchmarkTablePut(b *testing.B) {
	s := newStore()
	defer s.Close()
	tbl, err := s.NewTable[string, *refRec](context.Background(), "bench")
	if err != nil {
		b.Fatal(err)
	}
	keys := benchKeys()
	v := refOf("v")
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		tbl.Put(keys[i%len(keys)], v)
	}
}

// BenchmarkTableInterns measures writes and reads of a table that interns part of its values: a
// write keeping its copy, one moving between two copies, and a read.
func BenchmarkTableInterns(b *testing.B) {
	s := newStore()
	defer s.Close()
	blobs, err := s.RegisterInterned(Kind("blob"), func(v *blob) Hash { return v.ID })
	if err != nil {
		b.Fatal(err)
	}
	tbl, err := s.NewTable[string, *refRec](context.Background(), "bench", Interns(blobs, blobPart))
	if err != nil {
		b.Fatal(err)
	}
	keys := benchKeys()
	one, two := refOf("one"), refOf("two")
	b.Run("put same value", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; b.Loop(); i++ {
			tbl.Put(keys[i%len(keys)], one)
		}
	})
	b.Run("put other value", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; b.Loop(); i++ {
			// Each pass over the keys writes the other value, so every write moves a use.
			if (i/len(keys))%2 == 0 {
				tbl.Put(keys[i%len(keys)], one)
			} else {
				tbl.Put(keys[i%len(keys)], two)
			}
		}
	})
	b.Run("get", func(b *testing.B) {
		// The keys are written here, outside the measured loop, so the reads find values when
		// this sub-benchmark runs alone.
		for _, k := range keys {
			tbl.Put(k, one)
		}
		if _, ok := tbl.Get(keys[len(keys)-1]); !ok {
			b.Fatal("the reads must find the keys they look up")
		}
		b.ReportAllocs()
		for i := 0; b.Loop(); i++ {
			tbl.Get(keys[i%len(keys)])
		}
	})
}

// benchKeys returns the keys the table benchmarks write.
func benchKeys() []string {
	keys := make([]string, 1024)
	for i := range keys {
		keys[i] = fmt.Sprint("k", i)
	}
	return keys
}
