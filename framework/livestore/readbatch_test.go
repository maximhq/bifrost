package livestore

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
)

// batchFixture is a store over the test schema whose items link to their group (Cascade) and owner
// (SetNull). With a batch size the items are read with ReadInBatches and linked with ChildIDsOf;
// with none, they are linked with ChildOf, so the two declarations can be run on one scenario. The
// fixture records the IDs of every items ByID call and the limit of every items Page call.
type batchFixture struct {
	db     *gorm.DB
	s      *Store
	groups *Collection[group]
	owners *Collection[owner]
	items  *Collection[item]

	mu       sync.Mutex
	byID     [][]string // the IDs of each items ByID call since the last reset
	limits   []int      // the limit of each items Page call
	passNext int        // how many items ByID calls succeed before failNext starts counting
	failNext int        // how many items ByID calls fail before one succeeds
}

// newBatchFixture registers groups, owners and items on a fresh store over a fresh database, the
// items read in batches of size, or linked with ChildOf when size is 0.
func newBatchFixture(t *testing.T, size int) *batchFixture {
	t.Helper()
	f := &batchFixture{db: openDB(t), s: newBatchStore()}
	t.Cleanup(f.s.Close)
	var err error
	if f.owners, err = f.s.Register(Kind("owner"), nil, ownerLoader()); err != nil {
		t.Fatal(err)
	}
	if f.groups, err = f.s.Register(Kind("group"), nil, plainGroupLoader()); err != nil {
		t.Fatal(err)
	}
	items := itemLoader()
	page, byID := items.Page, items.ByID
	items.Page = func(ctx context.Context, db *gorm.DB, after string, limit int) ([]*item, string, error) {
		f.mu.Lock()
		f.limits = append(f.limits, limit)
		f.mu.Unlock()
		return page(ctx, db, after, limit)
	}
	items.ByID = func(ctx context.Context, db *gorm.DB, ids []string) ([]*item, error) {
		f.mu.Lock()
		f.byID = append(f.byID, append([]string(nil), ids...))
		fail := false
		switch {
		case f.passNext > 0:
			f.passNext--
		case f.failNext > 0:
			f.failNext--
			fail = true
		}
		f.mu.Unlock()
		if fail {
			return nil, errors.New("read failed")
		}
		return byID(ctx, db, ids)
	}
	opts := []Option[item]{UniqueIndex("label", func(i *item) string { return i.Label })}
	groupOf := func(i *item) string { return i.GroupID }
	ownerOf := func(i *item) string { return i.OwnerID }
	if size > 0 {
		opts = append(opts, ReadInBatches[item](size),
			ChildIDsOf(Kind("group"), groupOf, itemIDsWhere("group_id"), Cascade),
			ChildIDsOf(Kind("owner"), ownerOf, itemIDsWhere("owner_id"), SetNull))
	} else {
		opts = append(opts,
			ChildOf(Kind("group"), groupOf, itemsWhere("group_id"), Cascade),
			ChildOf(Kind("owner"), ownerOf, itemsWhere("owner_id"), SetNull))
	}
	if f.items, err = f.s.Register(Kind("item"), []Kind{"group", "owner"}, items, opts...); err != nil {
		t.Fatal(err)
	}
	return f
}

// newBatchStore returns a store with nothing registered, for the kinds these tests register.
func newBatchStore() *Store { return New() }

// plainGroupLoader reads groups without interning their tags.
func plainGroupLoader() Loader[group] {
	build := func(rows []groupRow) []*group {
		out := make([]*group, 0, len(rows))
		for _, r := range rows {
			out = append(out, &group{ID: r.ID, Name: r.Name, Tag: r.Tag})
		}
		return out
	}
	return Loader[group]{
		ID: func(g *group) string { return g.ID },
		Page: func(ctx context.Context, db *gorm.DB, after string, limit int) ([]*group, string, error) {
			var rows []groupRow
			if err := db.WithContext(ctx).Where("id > ?", after).Order("id").Limit(limit).Find(&rows).Error; err != nil {
				return nil, "", err
			}
			return build(rows), nextCursor(len(rows), limit, func() string { return rows[len(rows)-1].ID }), nil
		},
		ByID: func(ctx context.Context, db *gorm.DB, ids []string) ([]*group, error) {
			var rows []groupRow
			if err := db.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
				return nil, err
			}
			return build(rows), nil
		},
	}
}

// itemIDsWhere reads the IDs of the items whose column is one of values.
func itemIDsWhere(column string) func(ctx context.Context, db *gorm.DB, values []string) ([]string, error) {
	return func(ctx context.Context, db *gorm.DB, values []string) ([]string, error) {
		var ids []string
		err := db.WithContext(ctx).Model(&itemRow{}).Where(column+" IN ?", values).Pluck("id", &ids).Error
		return ids, err
	}
}

// seedItems inserts groups g1 and g2, owner o1, and n items in g1, the first three owned by o1.
func (f *batchFixture) seedItems(t *testing.T, n int) {
	t.Helper()
	o1 := "o1"
	mustCreate(t, f.db, &ownerRow{ID: "o1", Name: "owner one"})
	mustCreate(t, f.db, []groupRow{{ID: "g1", Name: "n1"}, {ID: "g2", Name: "n2"}})
	for i := range n {
		row := itemRow{ID: fmt.Sprintf("i%d", i), GroupID: "g1", Label: fmt.Sprintf("l%d", i)}
		if i < 3 {
			row.OwnerID = &o1
		}
		mustCreate(t, f.db, &row)
	}
}

// mustCreate inserts rows, failing the test on an error.
func mustCreate(t *testing.T, db *gorm.DB, rows any) {
	t.Helper()
	if err := db.Create(rows).Error; err != nil {
		t.Fatal(err)
	}
}

// reads returns the IDs of every items ByID call since the last call to reads, and resets them.
func (f *batchFixture) reads() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.byID
	f.byID = nil
	return out
}

// snapshot returns every item record held, keyed by ID, for comparing two stores.
func (f *batchFixture) snapshot() map[string]item {
	out := make(map[string]item)
	f.items.Range(func(it *item) bool {
		out[it.ID] = *it
		return true
	})
	return out
}

// requireBatched fails the test unless every ByID call held at most size IDs and the calls held more
// than size IDs between them, so the read was one the batches had to split.
func requireBatched(t *testing.T, what string, calls [][]string, size int) {
	t.Helper()
	total := 0
	for _, c := range calls {
		if len(c) > size {
			t.Fatalf("%s: a ByID call was given %d IDs, more than the batch of %d: %v", what, len(c), size, calls)
		}
		total += len(c)
	}
	if total <= size {
		t.Fatalf("%s: the calls held %d IDs in all, too few to need a split: %v", what, total, calls)
	}
}

// TestReadInBatchesSplitsEveryReadByID checks that a collection read in batches never hands its
// ByID more IDs than the batch size, on a refresh, its RetryMissing and RetryErrors retries, a
// parent's refresh through ChildIDsOf, the re-read of a held child that moved, the re-read of a
// removed parent's SetNull children, and the re-read of records holding a contested unique key.
func TestReadInBatchesSplitsEveryReadByID(t *testing.T) {
	const size = 2
	ctx := context.Background()
	f := newBatchFixture(t, size)
	f.seedItems(t, 7)
	if err := f.s.LoadAll(ctx, f.db); err != nil {
		t.Fatal(err)
	}
	f.reads()

	all := make([]Ref, 0, 7)
	for i := range 7 {
		all = append(all, Ref{Kind: "item", ID: fmt.Sprintf("i%d", i)})
	}
	if err := f.s.Refresh(ctx, all); err != nil {
		t.Fatal(err)
	}
	requireBatched(t, "refresh", f.reads(), size)

	missing := append(all[:1:1], Ref{Kind: "item", ID: "m1"}, Ref{Kind: "item", ID: "m2"}, Ref{Kind: "item", ID: "m3"})
	var missErr *MissingError
	if err := f.s.Refresh(ctx, missing, RetryMissing(1, time.Millisecond)); !errors.As(err, &missErr) || len(missErr.Refs) != 3 {
		t.Fatalf("Refresh of missing rows = %v, want a MissingError naming three", err)
	}
	calls := f.reads()
	requireBatched(t, "RetryMissing", calls, size)
	var missRetried []string
	for _, c := range calls[len(calls)-2:] {
		missRetried = append(missRetried, c...)
	}
	slices.Sort(missRetried)
	if !slices.Equal(missRetried, []string{"m1", "m2", "m3"}) {
		t.Fatalf("RetryMissing re-read %v in its last two calls, want exactly m1, m2 and m3: %v", missRetried, calls)
	}

	// Every row takes a new label, so the store holds the new ones only if the retry publishes what
	// it re-read. The first batch succeeds and the second fails, so a retry that re-read only the
	// failed batch would leave IDs on both sides of it unread.
	for i := range 7 {
		if err := f.db.Model(&itemRow{}).Where("id = ?", fmt.Sprintf("i%d", i)).Update("label", fmt.Sprintf("r%d", i)).Error; err != nil {
			t.Fatal(err)
		}
	}
	f.mu.Lock()
	f.passNext, f.failNext = 1, 1
	f.mu.Unlock()
	if err := f.s.Refresh(ctx, all, RetryErrors(2, time.Millisecond)); err != nil {
		t.Fatalf("Refresh after a failed batch = %v, want the retry to read every row", err)
	}
	calls = f.reads()
	requireBatched(t, "RetryErrors", calls, size)
	if len(calls) < 2 {
		t.Fatalf("RetryErrors made %d ByID calls, want the passing and the failing batch first: %v", len(calls), calls)
	}
	var retried, want []string
	for _, c := range calls[2:] {
		retried = append(retried, c...)
	}
	for _, r := range all {
		want = append(want, r.ID)
	}
	slices.Sort(retried)
	slices.Sort(want)
	if !slices.Equal(retried, want) {
		t.Fatalf("RetryErrors re-read %v after the failed batch, want all seven IDs: %v", retried, calls)
	}
	for i := range 7 {
		if it, _ := f.items.Get(fmt.Sprintf("i%d", i)); it.Label != fmt.Sprintf("r%d", i) {
			t.Fatalf("i%d = %+v after RetryErrors, want the re-read label r%d", i, it, i)
		}
	}

	if err := f.s.Refresh(ctx, []Ref{{Kind: "group", ID: "g1"}}); err != nil {
		t.Fatal(err)
	}
	requireBatched(t, "a parent's refresh", f.reads(), size)

	if err := f.db.Model(&itemRow{}).Where("id IN ?", []string{"i3", "i4", "i5"}).Update("group_id", "g2").Error; err != nil {
		t.Fatal(err)
	}
	if err := f.s.Refresh(ctx, []Ref{{Kind: "group", ID: "g1"}}); err != nil {
		t.Fatal(err)
	}
	requireBatched(t, "the re-read of moved children", f.reads(), size)
	for _, id := range []string{"i3", "i4", "i5"} {
		if it, ok := f.items.Get(id); !ok || it.GroupID != "g2" {
			t.Fatalf("%s = %+v, %v; want it kept under g2", id, it, ok)
		}
	}

	if err := f.db.Model(&itemRow{}).Where("owner_id = ?", "o1").Update("owner_id", nil).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.s.Refresh(ctx, []Ref{{Kind: "owner", ID: "o1", Remove: true}}); err != nil {
		t.Fatal(err)
	}
	requireBatched(t, "the re-read of SetNull children", f.reads(), size)

	// i0, i1 and i2 take the labels i4, i5 and i6 hold in memory, whose rows moved on to new labels:
	// the store re-reads the three holders to settle the contested keys.
	for i := range 3 {
		if err := f.db.Model(&itemRow{}).Where("id = ?", fmt.Sprintf("i%d", i+4)).Update("label", fmt.Sprintf("new%d", i)).Error; err != nil {
			t.Fatal(err)
		}
		if err := f.db.Model(&itemRow{}).Where("id = ?", fmt.Sprintf("i%d", i)).Update("label", fmt.Sprintf("r%d", i+4)).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := f.s.Refresh(ctx, all[:3]); err != nil {
		t.Fatal(err)
	}
	var holders [][]string
	for _, c := range f.reads() {
		if slices.ContainsFunc(c, func(id string) bool { return id == "i4" || id == "i5" || id == "i6" }) {
			holders = append(holders, c)
		}
	}
	requireBatched(t, "the re-read of contested key holders", holders, size)
	for i := range 3 {
		if it, _ := f.items.Get(fmt.Sprintf("i%d", i+4)); it.Label != fmt.Sprintf("new%d", i) {
			t.Fatalf("i%d = %+v, want its new label", i+4, it)
		}
	}
}

// TestChildIDsOfMatchesChildOf checks that a link declared with ChildIDsOf leaves the same records as
// one declared with ChildOf: a new child found through its parent, a deleted one dropped, a moved
// one kept under its new parent, SetNull children re-read with the reference cleared, and Cascade
// children removed with their parent.
func TestChildIDsOfMatchesChildOf(t *testing.T) {
	ctx := context.Background()
	stores := map[string]*batchFixture{"ChildOf": newBatchFixture(t, 0), "ChildIDsOf": newBatchFixture(t, 2)}
	steps := []struct {
		name  string
		write func(db *gorm.DB) error
		ref   Ref
	}{
		{"a new child", func(db *gorm.DB) error { return db.Create(&itemRow{ID: "i9", GroupID: "g1", Label: "l9"}).Error }, Ref{Kind: "group", ID: "g1"}},
		{"a deleted child", func(db *gorm.DB) error { return db.Delete(&itemRow{ID: "i4"}).Error }, Ref{Kind: "group", ID: "g1"}},
		{"a moved child", func(db *gorm.DB) error {
			return db.Model(&itemRow{}).Where("id = ?", "i3").Update("group_id", "g2").Error
		}, Ref{Kind: "group", ID: "g1"}},
		{"SetNull", func(db *gorm.DB) error {
			if err := db.Model(&itemRow{}).Where("owner_id = ?", "o1").Update("owner_id", nil).Error; err != nil {
				return err
			}
			return db.Delete(&ownerRow{ID: "o1"}).Error
		}, Ref{Kind: "owner", ID: "o1", Remove: true}},
		{"Cascade", func(db *gorm.DB) error { return db.Where("group_id = ?", "g1").Delete(&itemRow{}).Error }, Ref{Kind: "group", ID: "g1", Remove: true}},
	}
	for _, f := range stores {
		f.seedItems(t, 5)
		if err := f.s.LoadAll(ctx, f.db); err != nil {
			t.Fatal(err)
		}
	}
	for _, step := range steps {
		for name, f := range stores {
			if err := step.write(f.db); err != nil {
				t.Fatal(err)
			}
			if err := f.s.Refresh(ctx, []Ref{step.ref}); err != nil {
				t.Fatalf("%s, %s: %v", step.name, name, err)
			}
		}
		want, got := stores["ChildOf"].snapshot(), stores["ChildIDsOf"].snapshot()
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("after %s, ChildIDsOf holds %v; ChildOf holds %v", step.name, got, want)
		}
	}
	if left := stores["ChildIDsOf"].snapshot(); len(left) != 1 || left["i3"].GroupID != "g2" {
		t.Fatalf("after every step the store holds %v, want only i3 under g2", left)
	}
}

// TestReadInBatchesLimitsPages checks that LoadAll and RefreshKind ask Page for the batch size when it
// is under the page they read, and for the page otherwise.
func TestReadInBatchesLimitsPages(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		size, want int
	}{{2, 2}, {pageSize * 5, pageSize}} {
		f := newBatchFixture(t, tc.size)
		f.seedItems(t, 5)
		if err := f.s.LoadAll(ctx, f.db); err != nil {
			t.Fatal(err)
		}
		f.checkLimits(t, "LoadAll", tc.size, tc.want)
		loaded := slices.Sorted(maps.Keys(f.snapshot()))
		if !slices.Equal(loaded, []string{"i0", "i1", "i2", "i3", "i4"}) {
			t.Fatalf("batch %d: LoadAll loaded %v, want i0 to i4", tc.size, loaded)
		}
		// The refresh must read the changed label from its own pages.
		if err := f.db.Model(&itemRow{}).Where("id = ?", "i4").Update("label", "l4b").Error; err != nil {
			t.Fatal(err)
		}
		if err := f.s.RefreshKind(ctx, Kind("item")); err != nil {
			t.Fatal(err)
		}
		f.checkLimits(t, "RefreshKind", tc.size, tc.want)
		items := f.snapshot()
		if n := len(items); n != 5 {
			t.Fatalf("batch %d: the store holds %d items, want 5", tc.size, n)
		}
		if got := items["i4"].Label; got != "l4b" {
			t.Fatalf("batch %d: i4's label is %q after RefreshKind, want the changed row's l4b", tc.size, got)
		}
	}
}

// checkLimits fails the test unless Page was called since the last check and asked for want rows
// each time, then forgets the calls, so the next check sees only the calls made after this one.
func (f *batchFixture) checkLimits(t *testing.T, during string, size, want int) {
	t.Helper()
	f.mu.Lock()
	limits := f.limits
	f.limits = nil
	f.mu.Unlock()
	if len(limits) == 0 {
		t.Fatalf("batch %d: %s read no page", size, during)
	}
	for _, l := range limits {
		if l != want {
			t.Fatalf("batch %d: %s asked Page for %v rows, want %d each time", size, during, limits, want)
		}
	}
}

// TestReadInBatchesRegistrationErrors checks that Register refuses a batch size that is not positive,
// and a ChildOf link on a collection read in batches, whichever option comes first, naming the kind
// and the parent.
func TestReadInBatchesRegistrationErrors(t *testing.T) {
	byGroup := ChildOf(Kind("group"), func(i *item) string { return i.GroupID }, itemsWhere("group_id"), Cascade)
	for _, tc := range []struct {
		name string
		opts []Option[item]
		want []string
	}{
		{"a zero batch", []Option[item]{ReadInBatches[item](0)}, []string{`"item"`, "positive"}},
		{"a negative batch", []Option[item]{ReadInBatches[item](-1)}, []string{`"item"`, "positive"}},
		{"ChildOf after ReadInBatches", []Option[item]{ReadInBatches[item](2), byGroup}, []string{`"item"`, `"group"`, "ChildIDsOf"}},
		{"ChildOf before ReadInBatches", []Option[item]{byGroup, ReadInBatches[item](2)}, []string{`"item"`, `"group"`, "ChildIDsOf"}},
	} {
		s := newBatchStore()
		_, err := s.Register(Kind("item"), nil, itemLoader(), tc.opts...)
		s.Close()
		if err == nil {
			t.Fatalf("%s: Register succeeded, want an error", tc.name)
		}
		for _, w := range tc.want {
			if !strings.Contains(err.Error(), w) {
				t.Fatalf("%s: Register = %v, want it to name %s", tc.name, err, w)
			}
		}
	}
}

// TestParentRefreshKeepsOrDropsAChildItNoLongerLists checks what a parent's refresh does with a held
// child it no longer lists whose row is still there, for ChildOf and ChildIDsOf alike: a Cascade
// child moved to another parent is kept under it, a Cascade child left with no parent is removed,
// as the parent's removal would have removed it, and a SetNull child left with no parent is kept
// with the reference cleared.
func TestParentRefreshKeepsOrDropsAChildItNoLongerLists(t *testing.T) {
	ctx := context.Background()
	for name, size := range map[string]int{"ChildOf": 0, "ChildIDsOf": 2} {
		t.Run(name, func(t *testing.T) {
			f := newBatchFixture(t, size)
			f.seedItems(t, 5)
			if err := f.s.LoadAll(ctx, f.db); err != nil {
				t.Fatal(err)
			}
			for id, groupID := range map[string]string{"i3": "g2", "i4": ""} {
				if err := f.db.Model(&itemRow{}).Where("id = ?", id).Update("group_id", groupID).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := f.db.Model(&itemRow{}).Where("id = ?", "i0").Update("owner_id", nil).Error; err != nil {
				t.Fatal(err)
			}
			if err := f.s.Refresh(ctx, []Ref{{Kind: "group", ID: "g1"}, {Kind: "owner", ID: "o1"}}); err != nil {
				t.Fatal(err)
			}
			if it, ok := f.items.Get("i3"); !ok || it.GroupID != "g2" {
				t.Fatalf("i3 = %+v, %v; want the Cascade child that moved kept under g2", it, ok)
			}
			if it, ok := f.items.Get("i4"); ok {
				t.Fatalf("i4 = %+v; want the Cascade child left with no parent removed", it)
			}
			if it, ok := f.items.Get("i0"); !ok || it.OwnerID != "" || it.GroupID != "g1" {
				t.Fatalf("i0 = %+v, %v; want the SetNull child left with no owner kept, the owner cleared", it, ok)
			}
		})
	}
}
