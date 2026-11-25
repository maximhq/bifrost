package livestore

import (
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// groupRow, itemRow and ownerRow are the test schema: items belong to a group (deleted with
// it) and optionally to an owner (cleared when the owner goes).
type groupRow struct {
	ID   string `gorm:"primaryKey"`
	Name string
	Tag  string
}

// TableName names the groups table.
func (groupRow) TableName() string { return "groups" }

type itemRow struct {
	ID      string `gorm:"primaryKey"`
	GroupID string
	OwnerID *string
	Label   string
}

// TableName names the items table.
func (itemRow) TableName() string { return "items" }

type ownerRow struct {
	ID   string `gorm:"primaryKey"`
	Name string
}

// TableName names the owners table.
func (ownerRow) TableName() string { return "owners" }

// group, item and owner are the compact records built from the rows; tagSet is interned.
type group struct {
	ID, Name, Tag string
	Set           Hash
}

type item struct {
	ID, GroupID, OwnerID, Label string
}

type owner struct {
	ID, Name string
}

type tagSet struct {
	Tag string
}

// fixture is a store over the test schema with every option the engine has.
type fixture struct {
	db     *gorm.DB
	s      *Store
	tags   *Interned[tagSet]
	owners *Collection[owner]
	groups *Collection[group]
	items  *Collection[item]
	byID   atomic.Int32           // calls to the items ByID read
	idOf   atomic.Int32           // calls to the items ID extractor
	hook   atomic.Pointer[func()] // runs inside the groups ByID read, after the query
	// childHook runs inside the read of a group's items, after the query.
	childHook atomic.Pointer[func()]
	// readErr runs before the groups ByID query; an error it returns fails the read.
	readErr atomic.Pointer[func() error]
	// pageHook runs inside the groups Page read, after the page's tag sets are interned.
	pageHook atomic.Pointer[func()]
	// nameHook runs each time the groups name index reads a record's key, with that record.
	nameHook atomic.Pointer[func(*group)]
}

// openDB opens an in-memory SQLite database with the test schema on one connection.
func openDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&groupRow{}, &itemRow{}, &ownerRow{}); err != nil {
		t.Fatal(err)
	}
	return db
}

// newFixture registers owners, interned tags, groups and items on a fresh store over a fresh
// database. Call load once the rows the test needs are in.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{db: openDB(t), s: newStore()}
	t.Cleanup(f.s.Close)
	var err error
	if f.owners, err = f.s.Register(Kind("owner"), nil, ownerLoader()); err != nil {
		t.Fatal(err)
	}
	if f.tags, err = f.s.RegisterInterned(Kind("tag_set"), func(ts *tagSet) Hash { return md5.Sum([]byte(ts.Tag)) }); err != nil {
		t.Fatal(err)
	}
	if f.groups, err = f.s.Register(Kind("group"), nil, f.groupLoader(),
		UniqueIndex("name", func(g *group) string {
			if h := f.nameHook.Load(); h != nil {
				(*h)(g)
			}
			return g.Name
		}),
		MultiIndex("tag", func(g *group) []string { return []string{g.Tag} }),
		Uses(f.tags, func(g *group) Hash { return g.Set }),
	); err != nil {
		t.Fatal(err)
	}
	items := itemLoader()
	byID := items.ByID
	items.ByID = func(ctx context.Context, db *gorm.DB, ids []string) ([]*item, error) {
		f.byID.Add(1)
		return byID(ctx, db, ids)
	}
	idOf := items.ID
	items.ID = func(i *item) string {
		f.idOf.Add(1)
		return idOf(i)
	}
	if f.items, err = f.s.Register(Kind("item"), []Kind{"group", "owner"}, items,
		ChildOf(Kind("group"), func(i *item) string { return i.GroupID }, f.itemsOfGroups(), Cascade),
		ChildOf(Kind("owner"), func(i *item) string { return i.OwnerID }, itemsWhere("owner_id"), SetNull),
		UniqueIndex("label", func(i *item) string { return i.Label }),
		WithNegativeTTL[item](time.Minute),
	); err != nil {
		t.Fatal(err)
	}
	return f
}

// load boots the fixture's store from its database.
func (f *fixture) load(t *testing.T) {
	t.Helper()
	if err := f.s.LoadAll(context.Background(), f.db); err != nil {
		t.Fatal(err)
	}
}

// seed inserts the standard rows: two groups sharing tag "a", a third with no tag, an owner,
// and three items.
func (f *fixture) seed(t *testing.T) {
	t.Helper()
	o1 := "o1"
	f.mustExec(t, f.db.Create(&ownerRow{ID: "o1", Name: "owner one"}))
	f.mustExec(t, f.db.Create([]groupRow{{ID: "g1", Name: "n1", Tag: "a"}, {ID: "g2", Name: "n2", Tag: "a"}, {ID: "g3", Name: "n3"}}))
	f.mustExec(t, f.db.Create([]itemRow{{ID: "i1", GroupID: "g1", OwnerID: &o1, Label: "l1"}, {ID: "i2", GroupID: "g1", Label: "l2"}, {ID: "i3", GroupID: "g2", Label: "l3"}}))
}

// mustExec fails the test on a database error.
func (f *fixture) mustExec(t *testing.T, tx *gorm.DB) {
	t.Helper()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
}

// refresh runs Refresh and fails the test on an error.
func (f *fixture) refresh(t *testing.T, refs ...Ref) {
	t.Helper()
	if err := f.s.Refresh(context.Background(), refs); err != nil {
		t.Fatal(err)
	}
}

// groupLoader reads groups and interns their tag sets.
func (f *fixture) groupLoader() Loader[group] {
	build := func(rows []groupRow) []*group {
		out := make([]*group, 0, len(rows))
		for _, r := range rows {
			g := &group{ID: r.ID, Name: r.Name, Tag: r.Tag}
			if r.Tag != "" {
				g.Set = f.tags.Intern(&tagSet{Tag: r.Tag})
			}
			out = append(out, g)
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
			page := build(rows)
			if h := f.pageHook.Load(); h != nil {
				(*h)()
			}
			return page, nextCursor(len(rows), limit, func() string { return rows[len(rows)-1].ID }), nil
		},
		ByID: func(ctx context.Context, db *gorm.DB, ids []string) ([]*group, error) {
			if h := f.readErr.Load(); h != nil {
				if err := (*h)(); err != nil {
					return nil, err
				}
			}
			var rows []groupRow
			if err := db.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
				return nil, err
			}
			if h := f.hook.Load(); h != nil {
				(*h)()
			}
			return build(rows), nil
		},
	}
}

// itemsOfGroups reads the items of groups, running childHook after the query.
func (f *fixture) itemsOfGroups() func(ctx context.Context, db *gorm.DB, groupIDs []string) ([]*item, error) {
	byGroup := itemsWhere("group_id")
	return func(ctx context.Context, db *gorm.DB, groupIDs []string) ([]*item, error) {
		items, err := byGroup(ctx, db, groupIDs)
		if h := f.childHook.Load(); h != nil {
			(*h)()
		}
		return items, err
	}
}

// ownerLoader reads owners.
func ownerLoader() Loader[owner] {
	build := func(rows []ownerRow) []*owner {
		out := make([]*owner, 0, len(rows))
		for _, r := range rows {
			out = append(out, &owner{ID: r.ID, Name: r.Name})
		}
		return out
	}
	return Loader[owner]{
		ID: func(o *owner) string { return o.ID },
		Page: func(ctx context.Context, db *gorm.DB, after string, limit int) ([]*owner, string, error) {
			var rows []ownerRow
			if err := db.WithContext(ctx).Where("id > ?", after).Order("id").Limit(limit).Find(&rows).Error; err != nil {
				return nil, "", err
			}
			return build(rows), nextCursor(len(rows), limit, func() string { return rows[len(rows)-1].ID }), nil
		},
		ByID: func(ctx context.Context, db *gorm.DB, ids []string) ([]*owner, error) {
			var rows []ownerRow
			if err := db.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
				return nil, err
			}
			return build(rows), nil
		},
	}
}

// buildItems turns item rows into records.
func buildItems(rows []itemRow) []*item {
	out := make([]*item, 0, len(rows))
	for _, r := range rows {
		it := &item{ID: r.ID, GroupID: r.GroupID, Label: r.Label}
		if r.OwnerID != nil {
			it.OwnerID = *r.OwnerID
		}
		out = append(out, it)
	}
	return out
}

// itemLoader reads items.
func itemLoader() Loader[item] {
	return Loader[item]{
		ID: func(i *item) string { return i.ID },
		Page: func(ctx context.Context, db *gorm.DB, after string, limit int) ([]*item, string, error) {
			var rows []itemRow
			if err := db.WithContext(ctx).Where("id > ?", after).Order("id").Limit(limit).Find(&rows).Error; err != nil {
				return nil, "", err
			}
			return buildItems(rows), nextCursor(len(rows), limit, func() string { return rows[len(rows)-1].ID }), nil
		},
		ByID: itemsWhere("id"),
	}
}

// itemsWhere reads the items whose column is one of values.
func itemsWhere(column string) func(ctx context.Context, db *gorm.DB, values []string) ([]*item, error) {
	return func(ctx context.Context, db *gorm.DB, values []string) ([]*item, error) {
		var rows []itemRow
		if err := db.WithContext(ctx).Where(column+" IN ?", values).Find(&rows).Error; err != nil {
			return nil, err
		}
		return buildItems(rows), nil
	}
}

// nextCursor returns the cursor after a full page, or "" after the last one.
func nextCursor(n, limit int, last func() string) string {
	if n < limit {
		return ""
	}
	return last()
}

// setClock replaces the package clock with one the test moves, restoring it afterwards.
func setClock(t *testing.T, at time.Time) *time.Time {
	t.Helper()
	cur := at
	now = func() time.Time { return cur }
	t.Cleanup(func() { now = time.Now })
	return &cur
}

// ids returns the IDs of records, for comparison.
func ids[T any](recs []*T, id func(*T) string) []string {
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, id(r))
	}
	return out
}

// TestLoadAllIndexesAndInterned checks boot, both index kinds, children indexes and interning.
func TestLoadAllIndexesAndInterned(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	f.load(t)

	if g, ok := f.groups.Get("g1"); !ok || g.Name != "n1" {
		t.Fatalf("Get g1 = %+v, %v", g, ok)
	}
	if g, ok := f.groups.GetBy("name", "n2"); !ok || g.ID != "g2" {
		t.Fatalf("GetBy name n2 = %+v, %v", g, ok)
	}
	if got := ids(f.groups.List("tag", "a"), func(g *group) string { return g.ID }); !slices.Equal(got, []string{"g1", "g2"}) {
		t.Fatalf("List tag a = %v", got)
	}
	if _, ok := f.groups.GetBy("tag", ""); ok {
		t.Fatal("an empty key must not be indexed")
	}
	if _, ok := f.groups.GetBy("missing", "x"); ok {
		t.Fatal("an unknown index must find nothing")
	}
	g1, _ := f.groups.Get("g1")
	g2, _ := f.groups.Get("g2")
	if g1.Set != g2.Set {
		t.Fatal("equal tags must intern to one hash")
	}
	if ts, ok := f.tags.Get(g1.Set); !ok || ts.Tag != "a" {
		t.Fatalf("interned tag = %+v, %v", ts, ok)
	}
	if got := ids(f.items.List("parent:group", "g1"), func(i *item) string { return i.ID }); !slices.Equal(got, []string{"i1", "i2"}) {
		t.Fatalf("children of g1 = %v", got)
	}
}

// TestListOfOneRecordSkipsIDAndSort checks that a key naming one record lists it with one small
// allocation: no ID is computed and nothing is sorted. A key naming several still lists them in ID
// order, from the IDs its slot already holds, so no List calls the ID extractor.
func TestListOfOneRecordSkipsIDAndSort(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	f.load(t)
	if f.idOf.Load() == 0 {
		t.Fatal("LoadAll never called the counted ID extractor, so counting its calls would prove nothing")
	}
	f.idOf.Store(0)

	if got := ids(f.items.List("parent:group", "g2"), func(i *item) string { return i.ID }); !slices.Equal(got, []string{"i3"}) {
		t.Fatalf("children of g2 = %v", got)
	}
	if allocs := testing.AllocsPerRun(100, func() { f.items.List("parent:group", "g2") }); !raceEnabled && allocs > 1 {
		t.Fatalf("listing one record allocated %v times, want 1", allocs)
	}
	if got := ids(f.items.List("parent:group", "g1"), func(i *item) string { return i.ID }); !slices.Equal(got, []string{"i1", "i2"}) {
		t.Fatalf("children of g1 = %v", got)
	}
	if n := f.idOf.Load(); n != 0 {
		t.Fatalf("List called the ID extractor %d times, want 0", n)
	}
}

// emptyLoader is a loader that reads nothing, for registration tests.
func emptyLoader[T any]() Loader[T] {
	return Loader[T]{
		ID:   func(*T) string { return "" },
		Page: func(context.Context, *gorm.DB, string, int) ([]*T, string, error) { return nil, "", nil },
		ByID: func(context.Context, *gorm.DB, []string) ([]*T, error) { return nil, nil },
	}
}

// TestRegistrationErrors checks that every misuse returns an error instead of panicking.
func TestRegistrationErrors(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)

	s := newStore()
	defer s.Close()
	if _, err := s.Register(Kind("a"), nil, emptyLoader[owner]()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Register(Kind("a"), nil, emptyLoader[owner]()); err == nil {
		t.Fatal("a duplicate kind must fail")
	}
	if _, err := s.Register(Kind("b"), nil, Loader[owner]{}); err == nil {
		t.Fatal("a loader without functions must fail")
	}
	if _, err := s.Register(Kind("c"), nil, emptyLoader[owner](),
		UniqueIndex("x", func(o *owner) string { return o.Name }),
		MultiIndex("x", func(o *owner) []string { return nil })); err == nil {
		t.Fatal("a duplicate index must fail")
	}
	if _, err := s.Register(Kind("d"), nil, emptyLoader[owner](), UniqueIndex("", func(o *owner) string { return o.ID })); err == nil {
		t.Fatal("an empty index name must fail")
	}
	if _, err := s.NewTable[string, int](context.Background(), "t"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NewTable[string, int](context.Background(), "t"); err == nil {
		t.Fatal("a duplicate table name must fail")
	}
	if _, err := s.RegisterState[int](context.Background(), "st", Kind("nope")); err == nil {
		t.Fatal("state of an unknown kind must fail")
	}
	if err := s.Refresh(ctx, []Ref{{Kind: "a", ID: "x"}}); !errors.Is(err, ErrNotLoaded) {
		t.Fatalf("Refresh before load = %v", err)
	}
	if err := s.LoadAll(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Register(Kind("e"), nil, emptyLoader[owner]()); !errors.Is(err, ErrLoaded) {
		t.Fatalf("Register after LoadAll = %v", err)
	}
	if err := s.LoadAll(ctx, db); !errors.Is(err, ErrLoaded) {
		t.Fatalf("second LoadAll = %v", err)
	}

	cyc := newStore()
	defer cyc.Close()
	if _, err := cyc.Register(Kind("x"), []Kind{"y"}, emptyLoader[owner]()); err != nil {
		t.Fatal(err)
	}
	if _, err := cyc.Register(Kind("y"), []Kind{"x"}, emptyLoader[owner]()); err != nil {
		t.Fatal(err)
	}
	if err := cyc.LoadAll(ctx, db); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle = %v", err)
	}

	unknown := newStore()
	defer unknown.Close()
	if _, err := unknown.Register(Kind("x"), []Kind{"missing"}, emptyLoader[owner]()); err != nil {
		t.Fatal(err)
	}
	if err := unknown.LoadAll(ctx, db); err == nil || !strings.Contains(err.Error(), "unknown kind") {
		t.Fatalf("unknown dependency = %v", err)
	}
}

// TestRefreshOnWriter checks that a refresh replaces a changed row, removes a gone one, and
// drops a Remove ref without reading.
func TestRefreshOnWriter(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	f.load(t)

	f.mustExec(t, f.db.Model(&groupRow{}).Where("id = ?", "g1").Update("name", "renamed"))
	f.refresh(t, Ref{Kind: "group", ID: "g1"})
	if _, ok := f.groups.GetBy("name", "renamed"); !ok {
		t.Fatal("the new name must be indexed")
	}
	if _, ok := f.groups.GetBy("name", "n1"); ok {
		t.Fatal("the old name must leave the index")
	}

	f.mustExec(t, f.db.Delete(&groupRow{ID: "g3"}))
	f.refresh(t, Ref{Kind: "group", ID: "g3"})
	if _, ok := f.groups.Get("g3"); ok {
		t.Fatal("a row that is gone must be removed on the writer")
	}

	f.refresh(t, Ref{Kind: "group", ID: "g2", Remove: true})
	if _, ok := f.groups.Get("g2"); ok {
		t.Fatal("a Remove ref must drop the record")
	}
	if _, ok := f.items.Get("i3"); ok {
		t.Fatal("removing g2 must cascade to its item")
	}
}

// TestUnchangedRecordKeepsPointer checks that re-reading an equal row neither swaps the
// pointer nor notifies.
func TestUnchangedRecordKeepsPointer(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	f.load(t)
	before, _ := f.groups.Get("g1")
	calls := 0
	f.groups.Subscribe(func(context.Context, []Change[group]) { calls++ })
	f.refresh(t, Ref{Kind: "group", ID: "g1"})
	after, _ := f.groups.Get("g1")
	if before != after {
		t.Fatal("an unchanged record must keep its pointer")
	}
	if calls != 0 {
		t.Fatalf("an unchanged record notified %d times", calls)
	}
}

// TestRemoveNeedsNoDatabase checks that a delete takes effect while the database is down.
func TestRemoveNeedsNoDatabase(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	f.load(t)
	sqlDB, _ := f.db.DB()
	_ = sqlDB.Close()
	if err := f.s.Refresh(context.Background(), []Ref{{Kind: "group", ID: "g1", Remove: true}}); err != nil {
		t.Fatalf("Remove with the database down = %v", err)
	}
	if _, ok := f.groups.Get("g1"); ok {
		t.Fatal("the record must be gone")
	}
	if err := f.s.Refresh(context.Background(), []Ref{{Kind: "group", ID: "g2"}}); err == nil {
		t.Fatal("a re-read with the database down must return an error")
	}
}

// TestRetryMissing checks that a refresh with RetryMissing waits for a row that is not readable
// yet, and never removes a row it cannot find.
func TestRetryMissing(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	f.load(t)
	ctx := context.Background()

	// The row lands right after the first read's query, so only a retry can find it.
	var reads atomic.Int32
	var insertErr error
	late := func() {
		if reads.Add(1) == 1 {
			insertErr = f.db.Create(&groupRow{ID: "g9", Name: "late"}).Error
		}
	}
	f.hook.Store(&late)
	if err := f.s.Refresh(ctx, []Ref{{Kind: "group", ID: "g9"}}, RetryMissing(20, time.Millisecond)); err != nil {
		t.Fatalf("late row = %v", err)
	}
	f.hook.Store(nil)
	if insertErr != nil {
		t.Fatalf("inserting the late row: %v", insertErr)
	}
	if _, ok := f.groups.Get("g9"); !ok {
		t.Fatal("the late row must be loaded")
	}
	if n := reads.Load(); n < 2 {
		t.Fatalf("made %d reads, want the read that missed the row and a retry", n)
	}

	f.mustExec(t, f.db.Delete(&groupRow{ID: "g1"}))
	err := f.s.Refresh(ctx, []Ref{{Kind: "group", ID: "g1"}}, RetryMissing(2, time.Millisecond))
	var missing *MissingError
	if !errors.As(err, &missing) || len(missing.Refs) != 1 || missing.Refs[0].ID != "g1" {
		t.Fatalf("missing row = %v", err)
	}
	if _, ok := f.groups.Get("g1"); !ok {
		t.Fatal("RetryMissing must never remove a record")
	}
}

// failReads makes the groups ByID read fail its first n calls, running before on each failure,
// and returns the count of reads.
func failReads(f *fixture, n int32, before func()) *atomic.Int32 {
	reads := &atomic.Int32{}
	hook := func() error {
		if reads.Add(1) > n {
			return nil
		}
		if before != nil {
			before()
		}
		return errors.New("read failed")
	}
	f.readErr.Store(&hook)
	return reads
}

// TestRetryErrorsRereadsAFailedRead checks that a refresh with RetryErrors reads again after a read
// fails, publishes what the retry finds, and composes with RetryMissing in either order.
func TestRetryErrorsRereadsAFailedRead(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	f.load(t)
	ctx := context.Background()

	f.mustExec(t, f.db.Model(&groupRow{}).Where("id = ?", "g1").Update("name", "n1b"))
	reads := failReads(f, 1, nil)
	if err := f.s.Refresh(ctx, []Ref{{Kind: "group", ID: "g1"}}, RetryErrors(3, time.Millisecond)); err != nil {
		t.Fatalf("Refresh = %v, want the retry to read the row", err)
	}
	if g, _ := f.groups.Get("g1"); g.Name != "n1b" {
		t.Fatalf("g1 = %+v, want the row the retry read", g)
	}
	if n := reads.Load(); n != 2 {
		t.Fatalf("made %d reads, want the failed one and one retry", n)
	}

	f.mustExec(t, f.db.Delete(&groupRow{ID: "g2"}))
	for _, opts := range [][]RefreshOption{
		{RetryErrors(2, time.Millisecond), RetryMissing(1, time.Millisecond)},
		{RetryMissing(1, time.Millisecond), RetryErrors(2, time.Millisecond)},
	} {
		failReads(f, 1, nil)
		err := f.s.Refresh(ctx, []Ref{{Kind: "group", ID: "g2"}}, opts...)
		var missing *MissingError
		if !errors.As(err, &missing) || len(missing.Refs) != 1 || missing.Refs[0].ID != "g2" {
			t.Fatalf("a missing row read after a failed read = %v, want a MissingError", err)
		}
		if _, ok := f.groups.Get("g2"); !ok {
			t.Fatal("RetryMissing must still keep the record when RetryErrors is given too")
		}
	}
}

// TestRetryErrorsStopsWhenTheContextEnds checks that a refresh with RetryErrors never reads again
// once its context is cancelled or past its deadline, and returns the read's error.
func TestRetryErrorsStopsWhenTheContextEnds(t *testing.T) {
	for _, tc := range []struct {
		name string
		// setup returns the refresh's context, its cancel, and what the failing read runs: the
		// read fails as the context ends.
		setup func() (context.Context, context.CancelFunc, func())
	}{
		{name: "cancelled", setup: func() (context.Context, context.CancelFunc, func()) {
			ctx, cancel := context.WithCancel(context.Background())
			return ctx, cancel, cancel
		}},
		{name: "past its deadline", setup: func() (context.Context, context.CancelFunc, func()) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			return ctx, cancel, func() { <-ctx.Done() }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.seed(t)
			f.load(t)
			ctx, cancel, during := tc.setup()
			defer cancel()
			reads := failReads(f, 10, during)
			err := f.s.Refresh(ctx, []Ref{{Kind: "group", ID: "g1"}}, RetryErrors(5, time.Millisecond))
			if err == nil || !strings.Contains(err.Error(), "read failed") {
				t.Fatalf("Refresh = %v, want the read's error", err)
			}
			if n := reads.Load(); n != 1 {
				t.Fatalf("made %d reads, want no retry once the context ended", n)
			}
		})
	}
}

// endsAfterCheck is a context that is still live at the first Err check after it is armed and ends
// right after that check, so it ends during the wait that follows a retry loop's check.
type endsAfterCheck struct {
	context.Context
	cancel context.CancelFunc
	armed  atomic.Bool
}

// Err reports the context's error, ending the context just after the check once armed.
func (c *endsAfterCheck) Err() error {
	err := c.Context.Err()
	if c.armed.CompareAndSwap(true, false) {
		c.cancel()
	}
	return err
}

// TestRetriesStopWhenTheContextEndsDuringTheWait checks that neither retry loop reads again when
// the context ends during the wait between reads. The wait is one nanosecond, so its timer has
// fired by the time the wait looks, and a wait that picked the timer over the ended context would
// start a retry. Each case runs many times since the two are picked at random when both are ready.
func TestRetriesStopWhenTheContextEndsDuringTheWait(t *testing.T) {
	const runs = 100
	t.Run("RetryErrors", func(t *testing.T) {
		f := newFixture(t)
		f.seed(t)
		f.load(t)
		for range runs {
			base, cancel := context.WithCancel(context.Background())
			ctx := &endsAfterCheck{Context: base, cancel: cancel}
			reads := failReads(f, 10, func() { ctx.armed.Store(true) })
			err := f.s.Refresh(ctx, []Ref{{Kind: "group", ID: "g1"}}, RetryErrors(3, time.Nanosecond))
			cancel()
			if err == nil || !strings.Contains(err.Error(), "read failed") {
				t.Fatalf("Refresh = %v, want the read's error", err)
			}
			if n := reads.Load(); n != 1 {
				t.Fatalf("made %d reads, want no retry once the context ended during the wait", n)
			}
		}
	})
	t.Run("RetryMissing", func(t *testing.T) {
		f := newFixture(t)
		f.seed(t)
		f.load(t)
		for range runs {
			ctx, cancel := context.WithCancel(context.Background())
			var reads atomic.Int32
			count := func() error { reads.Add(1); return nil }
			f.readErr.Store(&count)
			// The read finds no g9 and the context ends before the wait for the next read.
			end := func() { cancel() }
			f.hook.Store(&end)
			err := f.s.Refresh(ctx, []Ref{{Kind: "group", ID: "g9"}}, RetryMissing(3, time.Nanosecond))
			cancel()
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Refresh = %v, want the context's error", err)
			}
			if n := reads.Load(); n != 1 {
				t.Fatalf("made %d reads, want no retry once the context ended during the wait", n)
			}
		}
	})
}

// TestChildren checks that refreshing a parent finds new children and drops gone ones, and
// that removing a parent cascades or re-reads as its link says.
func TestChildren(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	f.load(t)

	f.mustExec(t, f.db.Create(&itemRow{ID: "i4", GroupID: "g1", Label: "l4"}))
	f.mustExec(t, f.db.Delete(&itemRow{ID: "i2"}))
	f.refresh(t, Ref{Kind: "group", ID: "g1"})
	if _, ok := f.items.Get("i4"); !ok {
		t.Fatal("a new child must be found through its parent")
	}
	if _, ok := f.items.Get("i2"); ok {
		t.Fatal("a child no longer under its parent must be dropped")
	}

	f.mustExec(t, f.db.Model(&itemRow{}).Where("owner_id = ?", "o1").Update("owner_id", nil))
	f.mustExec(t, f.db.Delete(&ownerRow{ID: "o1"}))
	f.refresh(t, Ref{Kind: "owner", ID: "o1", Remove: true})
	if it, ok := f.items.Get("i1"); !ok || it.OwnerID != "" {
		t.Fatalf("SetNull must re-read the child: %+v, %v", it, ok)
	}

	f.refresh(t, Ref{Kind: "group", ID: "g1", Remove: true})
	for _, id := range []string{"i1", "i4"} {
		if _, ok := f.items.Get(id); ok {
			t.Fatalf("Cascade must remove %s", id)
		}
	}
}

// TestParentRefreshKeepsAChildThatMovedToAnotherParent checks that a parent's refresh does not
// remove a child its query no longer returns while the child's row still exists under another
// parent: the child is kept, under its new parent, and its state survives.
func TestParentRefreshKeepsAChildThatMovedToAnotherParent(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	f.load(t)
	usage, err := f.s.RegisterState[int](context.Background(), "usage", Kind("item"))
	if err != nil {
		t.Fatal(err)
	}
	spent := 7
	usage.Put("i2", &spent)

	f.mustExec(t, f.db.Model(&itemRow{}).Where("id = ?", "i2").Update("group_id", "g2"))
	f.refresh(t, Ref{Kind: "group", ID: "g1"})
	if it, ok := f.items.Get("i2"); !ok || it.GroupID != "g2" {
		t.Fatalf("i2 = %+v, %v; want it kept under its new parent g2", it, ok)
	}
	if got := ids(f.items.List("parent:group", "g2"), func(i *item) string { return i.ID }); !slices.Equal(got, []string{"i2", "i3"}) {
		t.Fatalf("children of g2 = %v", got)
	}
	if got := ids(f.items.List("parent:group", "g1"), func(i *item) string { return i.ID }); !slices.Equal(got, []string{"i1"}) {
		t.Fatalf("children of g1 = %v", got)
	}
	if v, ok := usage.Get("i2"); !ok || *v != 7 {
		t.Fatal("the moved child's state must survive its old parent's refresh")
	}
}

// TestLoad checks reading on a miss: one query for concurrent misses, the negative cache, the
// primary key, and the errors for indexes that cannot load.
func TestLoad(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	f.load(t)
	ctx := context.Background()
	clock := setClock(t, time.Unix(1_000_000, 0))

	f.mustExec(t, f.db.Create(&itemRow{ID: "i9", GroupID: "g1", Label: "l9"}))
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if it, ok, err := f.items.Load(ctx, "i9"); err != nil || !ok || it.Label != "l9" {
				t.Errorf("Load i9 = %+v, %v, %v", it, ok, err)
			}
		}()
	}
	wg.Wait()
	if n := f.byID.Load(); n != 1 {
		t.Fatalf("concurrent misses ran %d queries", n)
	}
	if it, ok := f.items.GetBy("label", "l9"); !ok || it.ID != "i9" {
		t.Fatalf("a loaded record must be indexed too, got %+v, %v", it, ok)
	}

	for range 2 {
		if _, ok, err := f.items.Load(ctx, "nope"); err != nil || ok {
			t.Fatalf("Load nope = %v, %v", ok, err)
		}
	}
	if n := f.byID.Load(); n != 2 {
		t.Fatalf("the negative cache must stop the second query, got %d queries", n)
	}
	*clock = clock.Add(2 * time.Minute)
	if _, _, err := f.items.Load(ctx, "nope"); err != nil {
		t.Fatal(err)
	}
	if n := f.byID.Load(); n != 3 {
		t.Fatalf("an expired miss must query again, got %d queries", n)
	}
}

// TestRefreshKind checks that re-reading a whole kind publishes only the difference.
func TestRefreshKind(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	f.load(t)
	before, _ := f.groups.Get("g1")
	f.mustExec(t, f.db.Delete(&groupRow{ID: "g3"}))
	f.mustExec(t, f.db.Create(&groupRow{ID: "g5", Name: "n5"}))
	if err := f.s.RefreshKind(context.Background(), Kind("group")); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.groups.Get("g3"); ok {
		t.Fatal("a row gone from the kind must be removed")
	}
	if _, ok := f.groups.Get("g5"); !ok {
		t.Fatal("a new row must be added")
	}
	if after, _ := f.groups.Get("g1"); after != before {
		t.Fatal("an unchanged record must keep its pointer")
	}
}

// TestSubscribers checks boot delivery, dependency order, one call per kind, cancel, and the
// error for a write from inside a subscriber.
func TestSubscribers(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	var boot []Change[group]
	cancelBoot := f.groups.Subscribe(func(_ context.Context, ch []Change[group]) { boot = append(boot, ch...) })
	f.load(t)
	cancelBoot()
	if len(boot) != 3 || boot[0].Old != nil {
		t.Fatalf("boot delivery = %+v", boot)
	}

	var order []string
	var groupCalls, itemCalls int
	cancelGroups := f.groups.Subscribe(func(_ context.Context, ch []Change[group]) {
		groupCalls++
		order = append(order, "group")
	})
	f.items.Subscribe(func(_ context.Context, ch []Change[item]) {
		itemCalls++
		order = append(order, "item")
	})
	f.mustExec(t, f.db.Model(&groupRow{}).Where("id = ?", "g2").Update("name", "n2b"))
	f.mustExec(t, f.db.Model(&itemRow{}).Where("id = ?", "i3").Update("label", "l3b"))
	f.refresh(t, Ref{Kind: "item", ID: "i3"}, Ref{Kind: "group", ID: "g2"})
	if !slices.Equal(order, []string{"group", "item"}) || groupCalls != 1 || itemCalls != 1 {
		t.Fatalf("order = %v, group calls %d, item calls %d", order, groupCalls, itemCalls)
	}

	cancelGroups()
	f.mustExec(t, f.db.Model(&groupRow{}).Where("id = ?", "g2").Update("name", "n2c"))
	f.refresh(t, Ref{Kind: "group", ID: "g2"})
	if groupCalls != 1 {
		t.Fatal("a cancelled subscriber must not be called")
	}

	var inner error
	f.groups.Subscribe(func(ctx context.Context, _ []Change[group]) {
		inner = f.s.Refresh(ctx, []Ref{{Kind: "group", ID: "g1"}})
	})
	f.mustExec(t, f.db.Model(&groupRow{}).Where("id = ?", "g2").Update("name", "n2d"))
	f.refresh(t, Ref{Kind: "group", ID: "g2"})
	if !errors.Is(inner, ErrInSubscriber) {
		t.Fatalf("Refresh from a subscriber = %v", inner)
	}
}

// TestInternedLifetime checks that a value is shared while used and dropped a grace period
// after the last record stops using it.
func TestInternedLifetime(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	clock := setClock(t, time.Unix(1_000_000, 0))
	f.load(t)
	g2, _ := f.groups.Get("g2")
	shared := g2.Set

	f.mustExec(t, f.db.Model(&groupRow{}).Where("id = ?", "g1").Update("tag", "b"))
	f.refresh(t, Ref{Kind: "group", ID: "g1"})
	if _, ok := f.tags.Get(shared); !ok {
		t.Fatal("g2 still uses tag a")
	}

	f.refresh(t, Ref{Kind: "group", ID: "g2", Remove: true})
	if _, ok := f.tags.Get(shared); !ok {
		t.Fatal("an unused value must survive the grace period")
	}
	*clock = clock.Add(internedGrace + time.Second)
	f.s.sweep() // what the background sweeper runs every sweepInterval
	if _, ok := f.tags.Get(shared); ok {
		t.Fatal("an unused value must be dropped after the grace period")
	}
}

// TestInternedValueOutlivesASlowRefresh: a refresh whose read outlasts the grace period publishes
// a record whose value it interned before the sweep ran. No record uses the value until the
// publish, and the sweep must keep it all the same: a record published without its value resolves
// to nothing until some later read interns the value again.
func TestInternedValueOutlivesASlowRefresh(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	clock := setClock(t, time.Unix(1_000_000, 0))
	f.load(t)
	// g1 moves to a tag no other group has, so nothing else keeps its value.
	f.mustExec(t, f.db.Model(&groupRow{}).Where("id = ?", "g1").Update("tag", "c"))
	// The child read runs after g1's read interned the value and before the refresh publishes.
	slow := func() {
		*clock = clock.Add(internedGrace + time.Minute)
		f.s.sweep()
	}
	f.childHook.Store(&slow)
	f.refresh(t, Ref{Kind: "group", ID: "g1"})
	f.childHook.Store(nil)

	g, ok := f.groups.Get("g1")
	if !ok || g.Tag != "c" {
		t.Fatalf("g1 = %+v, %v, want it published with tag c", g, ok)
	}
	if _, ok := f.tags.Get(g.Set); !ok {
		t.Fatal("the sweep dropped the value a refresh interned before it published the record that uses it")
	}
	*clock = clock.Add(internedGrace + time.Minute)
	f.s.sweep()
	if _, ok := f.tags.Get(g.Set); !ok {
		t.Fatal("the sweep dropped a value g1 still uses")
	}
}

// TestInternedValueOfAFailedSlowRefreshIsSwept: the sweep keeps what a refresh interned only while
// that refresh runs. One that fails publishes nothing, so once it returns no record uses its value,
// and the value is swept a grace period later like any other.
func TestInternedValueOfAFailedSlowRefreshIsSwept(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	clock := setClock(t, time.Unix(1_000_000, 0))
	f.load(t)
	f.mustExec(t, f.db.Model(&groupRow{}).Where("id = ?", "g1").Update("tag", "c"))
	c := f.tags.hash(&tagSet{Tag: "c"})
	var reads atomic.Int32
	var heldWhileReading atomic.Bool
	// The first read finds g1 and interns its value. The retry for the missing row runs a grace
	// period later, after a sweep, and fails the refresh.
	fail := func() error {
		if reads.Add(1) == 1 {
			return nil
		}
		*clock = clock.Add(internedGrace + time.Minute)
		f.s.sweep()
		_, ok := f.tags.Get(c)
		heldWhileReading.Store(ok)
		return errors.New("connection reset")
	}
	f.readErr.Store(&fail)
	err := f.s.Refresh(context.Background(), []Ref{{Kind: "group", ID: "g1"}, {Kind: "group", ID: "gone"}}, RetryMissing(1, 0))
	f.readErr.Store(nil)
	if err == nil {
		t.Fatal("Refresh = nil, want the retry's error")
	}
	if !heldWhileReading.Load() {
		t.Fatal("the sweep dropped a value a refresh still reading had interned")
	}
	if g, _ := f.groups.Get("g1"); g.Tag != "a" {
		t.Fatalf("g1 tag = %q, want a: a failed refresh publishes nothing", g.Tag)
	}
	*clock = clock.Add(internedGrace + time.Minute)
	f.s.sweep()
	if _, ok := f.tags.Get(c); ok {
		t.Fatal("a value only a failed refresh interned must be swept once that refresh has returned")
	}
}

// TestRefreshKindKeepsInternedValuesUntilItPublishes is TestInternedValueOutlivesASlowRefresh for
// RefreshKind, which reads every page before it publishes any.
func TestRefreshKindKeepsInternedValuesUntilItPublishes(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	clock := setClock(t, time.Unix(1_000_000, 0))
	f.load(t)
	// g1 moves to a tag no other group has, and the read outlasts the grace period once the page's
	// values are interned.
	f.mustExec(t, f.db.Model(&groupRow{}).Where("id = ?", "g1").Update("tag", "c"))
	slow := func() {
		*clock = clock.Add(internedGrace + time.Minute)
		f.s.sweep()
	}
	f.pageHook.Store(&slow)
	if err := f.s.RefreshKind(context.Background(), Kind("group")); err != nil {
		t.Fatalf("RefreshKind: %v", err)
	}
	f.pageHook.Store(nil)

	g, ok := f.groups.Get("g1")
	if !ok || g.Tag != "c" {
		t.Fatalf("g1 = %+v, %v, want it published with tag c", g, ok)
	}
	if _, ok := f.tags.Get(g.Set); !ok {
		t.Fatal("the sweep dropped the value RefreshKind interned before it published the record that uses it")
	}
}

// TestLoadRecords checks file-only boot: records from the caller, the database calls that fail
// without a database, and a Remove, which needs none and drops the record from every index.
func TestLoadRecords(t *testing.T) {
	ctx := context.Background()
	s := newStore()
	defer s.Close()
	groupLoad := emptyLoader[group]()
	groupLoad.ID = func(g *group) string { return g.ID }
	groups, err := s.Register(Kind("group"), nil, groupLoad, UniqueIndex("name", func(g *group) string { return g.Name }))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LoadRecords(ctx, []Record{Rec(groups, &group{ID: "g1", Name: "n1"})}); err != nil {
		t.Fatal(err)
	}
	if g, ok := groups.Get("g1"); !ok || g.Name != "n1" {
		t.Fatalf("Get after LoadRecords = %+v, %v", g, ok)
	}
	if g, ok := groups.GetBy("name", "n1"); !ok || g.ID != "g1" {
		t.Fatalf("GetBy after LoadRecords = %+v, %v", g, ok)
	}
	if err := s.Refresh(ctx, []Ref{{Kind: "group", ID: "g1"}}); !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("Refresh without a database = %v", err)
	}
	if err := s.RefreshKind(ctx, Kind("group")); !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("RefreshKind without a database = %v", err)
	}
	if err := s.Refresh(ctx, []Ref{{Kind: "group", ID: "g1", Remove: true}}); err != nil {
		t.Fatalf("Remove without a database = %v", err)
	}
	if g, ok := groups.Get("g1"); ok {
		t.Fatalf("Get after a Remove without a database = %+v, want it gone", g)
	}
	if g, ok := groups.GetBy("name", "n1"); ok {
		t.Fatalf("GetBy after a Remove without a database = %+v, want it gone", g)
	}
	if err := s.LoadRecords(ctx, nil); !errors.Is(err, ErrLoaded) {
		t.Fatalf("second LoadRecords = %v", err)
	}
}

// loadRecordsNoPanic runs LoadRecords and fails the test on a panic, so a misuse that panics is
// reported as a failure instead of ending the run.
func loadRecordsNoPanic(t *testing.T, s *Store, recs []Record) error {
	t.Helper()
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("LoadRecords panicked: %v", p)
		}
	}()
	return s.LoadRecords(context.Background(), recs)
}

// TestLoadRecordsRefusesARecordOfAnotherStore checks that LoadRecords returns an error and publishes
// nothing for a record built from a collection another store registered, whether that collection
// holds this store's record type for the kind or another type.
func TestLoadRecordsRefusesARecordOfAnotherStore(t *testing.T) {
	groupLoad := emptyLoader[group]()
	groupLoad.ID = func(g *group) string { return g.ID }
	ownerLoad := emptyLoader[owner]()
	ownerLoad.ID = func(o *owner) string { return o.ID }

	sameStore, otherStore := newStore(), newStore()
	t.Cleanup(sameStore.Close)
	t.Cleanup(otherStore.Close)
	sameType, err := sameStore.Register(Kind("group"), nil, groupLoad)
	if err != nil {
		t.Fatal(err)
	}
	otherType, err := otherStore.Register(Kind("group"), nil, ownerLoad)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		rec  Record
	}{
		{name: "same type", rec: Rec(sameType, &group{ID: "g1"})},
		{name: "other type", rec: Rec(otherType, &owner{ID: "g1"})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStore()
			t.Cleanup(s.Close)
			groups, err := s.Register(Kind("group"), nil, groupLoad)
			if err != nil {
				t.Fatal(err)
			}
			good := Rec(groups, &group{ID: "g2"})
			if err := loadRecordsNoPanic(t, s, []Record{good, tc.rec}); err == nil {
				t.Fatal("LoadRecords accepted a record built from another store's collection")
			}
			if _, ok := groups.Get("g2"); ok {
				t.Fatal("a refused LoadRecords must publish nothing")
			}
		})
	}
}

// newGate returns a channel and a release that closes it once however often it runs. The release
// is also a cleanup, so a test that fails before releasing never leaves a goroutine parked on it.
func newGate(t *testing.T) (gate <-chan struct{}, release func()) {
	t.Helper()
	ch := make(chan struct{})
	var once sync.Once
	release = func() { once.Do(func() { close(ch) }) }
	t.Cleanup(release)
	return ch, release
}

// waitStarted waits for started to close, failing the test when a refresh returns on errs first
// or ten seconds pass, so a refresh that never reaches its hook fails the test instead of hanging.
func waitStarted(t *testing.T, started <-chan struct{}, errs <-chan error) {
	t.Helper()
	select {
	case <-started:
	case err := <-errs:
		t.Fatalf("the refresh returned %v before reaching its hook", err)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the refresh to reach its hook")
	}
}

// waitRefreshes receives n refresh results from errs and fails the test on an error, or once ten
// seconds pass in all, so a refresh that deadlocks after its gate opens fails instead of hanging.
func waitRefreshes(t *testing.T, errs <-chan error, n int) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for i := range n {
		select {
		case err := <-errs:
			if err != nil {
				t.Fatalf("Refresh = %v", err)
			}
		case <-deadline:
			t.Fatalf("timed out waiting for the refreshes: %d of %d results still pending", n-i, n)
		}
	}
}

// TestRowLockKeepsLatestRead checks that of two refreshes of one row, the later read
// publishes last: the first refresh reads the old value and stalls, the row changes, and the
// second refresh must wait for the first rather than publish ahead of it.
func TestRowLockKeepsLatestRead(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	f.load(t)

	started := make(chan struct{})
	gate, release := newGate(t)
	// Only the first read stalls. A sync.Once would also hold the second read in Do until the
	// first returns, serializing the two refreshes even without the record lock.
	var reads atomic.Int32
	stall := func() {
		if reads.Add(1) == 1 {
			close(started)
			<-gate
		}
	}
	f.hook.Store(&stall)

	errs := make(chan error, 2)
	go func() { errs <- f.s.Refresh(context.Background(), []Ref{{Kind: "group", ID: "g1"}}) }()
	waitStarted(t, started, errs)
	f.mustExec(t, f.db.Model(&groupRow{}).Where("id = ?", "g1").Update("name", "latest"))
	go func() { errs <- f.s.Refresh(context.Background(), []Ref{{Kind: "group", ID: "g1"}}) }()
	select {
	case err := <-errs:
		t.Fatalf("the second refresh returned (%v) while the first still held the row", err)
	case <-time.After(50 * time.Millisecond):
	}
	release()
	waitRefreshes(t, errs, 2)

	if g, _ := f.groups.Get("g1"); g.Name != "latest" {
		t.Fatalf("the older read published last: name = %q", g.Name)
	}
}

// TestChildLockKeepsLatestRead checks a parent's refresh against a child's own: a child the parent's
// refresh read before the child's row was deleted must not come back once the child's refresh has
// removed it.
func TestChildLockKeepsLatestRead(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	f.load(t)

	started := make(chan struct{})
	gate, release := newGate(t)
	var once sync.Once
	stall := func() {
		once.Do(func() {
			close(started)
			<-gate
		})
	}
	f.childHook.Store(&stall)

	errs := make(chan error, 2)
	go func() { errs <- f.s.Refresh(context.Background(), []Ref{{Kind: "group", ID: "g1"}}) }()
	waitStarted(t, started, errs)
	f.mustExec(t, f.db.Delete(&itemRow{ID: "i1"}))
	go func() { errs <- f.s.Refresh(context.Background(), []Ref{{Kind: "item", ID: "i1"}}) }()
	time.Sleep(50 * time.Millisecond)
	release()
	waitRefreshes(t, errs, 2)

	if _, ok := f.items.Get("i1"); ok {
		t.Fatal("the parent's older read of a child published after the child's own refresh removed it")
	}
}

// TestConcurrentReadsDuringRefresh exercises readers against writers under the race detector.
func TestConcurrentReadsDuringRefresh(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	f.load(t)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					f.groups.Get("g1")
					f.groups.GetBy("name", "n1")
					f.groups.List("tag", "a")
					f.items.List("parent:group", "g1")
				}
			}
		}()
	}
	// Registered before any assertion, so a failing one still stops the readers.
	t.Cleanup(func() {
		close(stop)
		wg.Wait()
	})
	for i := range 50 {
		name := "n1"
		if i%2 == 1 {
			name = "n1x"
		}
		f.mustExec(t, f.db.Model(&groupRow{}).Where("id = ?", "g1").Update("name", name))
		f.refresh(t, Ref{Kind: "group", ID: "g1"})
	}
}

// TestIndexWriteDoesNotCopyTheKeysRecords checks that adding a record under a key many records
// already share costs what it costs under a new key. An index that copied the key's records on
// every write made loading n members of one parent cost O(n²).
func TestIndexWriteDoesNotCopyTheKeysRecords(t *testing.T) {
	f := newFixture(t)
	f.load(t)
	for i := range 20000 {
		id := fmt.Sprintf("i%05d", i)
		f.items.put(id, &item{ID: id, GroupID: "g"})
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := range 100 {
		id := fmt.Sprintf("n%03d", i)
		f.items.put(id, &item{ID: id, GroupID: "g"})
	}
	runtime.ReadMemStats(&after)
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 1<<20 {
		t.Fatalf("adding 100 records under a key holding 20000 allocated %d bytes; an index write must not copy the key's records", grew)
	}
	if got := len(f.items.List("parent:group", "g")); got != 20100 {
		t.Fatalf("the key lists %d records, want 20100", got)
	}
}

// TestIndexReadsDuringWritesToTheSameKey runs List and GetBy on a key while records join and
// leave it, for the race detector: a write changes the key's map in place, so a read must see
// it only under the shard lock, and every read must find the record that never left.
func TestIndexReadsDuringWritesToTheSameKey(t *testing.T) {
	f := newFixture(t)
	f.load(t)
	f.items.put("a-stays", &item{ID: "a-stays", GroupID: "g"})
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			id := fmt.Sprintf("b%04d", i%500)
			f.items.put(id, &item{ID: id, GroupID: "g"})
			f.items.remove(id)
		}
	}()
	// Registered before any assertion, so a failing one still stops the writer.
	t.Cleanup(func() {
		close(stop)
		wg.Wait()
	})
	for range 2000 {
		list := f.items.List("parent:group", "g")
		if len(list) == 0 || list[0].ID != "a-stays" {
			t.Fatalf("List lost the record that never left: %d records", len(list))
		}
		if got, ok := f.items.GetBy("parent:group", "g"); !ok || got.ID != "a-stays" {
			t.Fatalf("GetBy = %v, %v; want the lowest ID, a-stays", got, ok)
		}
	}
}

// TestTableGetReadsTheClockOnlyForEntriesThatExpire checks that reading an entry with no TTL does
// not ask the clock. Tables without a TTL serve per-request lookups, and the clock read was most
// of a lookup's cost; an entry that can expire still has its expiry checked.
func TestTableGetReadsTheClockOnlyForEntriesThatExpire(t *testing.T) {
	var reads atomic.Int64
	at := time.Unix(1_000_000, 0)
	now = func() time.Time { reads.Add(1); return at }
	t.Cleanup(func() { now = time.Now })
	f := newFixture(t)
	tb, err := f.s.NewTable[string, int](context.Background(), "clock")
	if err != nil {
		t.Fatal(err)
	}
	tb.Put("forever", 1)
	tb.PutWithTTL("soon", 2, time.Minute)

	before := reads.Load()
	if v, ok := tb.Get("forever"); !ok || v != 1 {
		t.Fatalf("Get(forever) = %d, %v", v, ok)
	}
	if n := reads.Load() - before; n != 0 {
		t.Fatalf("reading an entry with no TTL read the clock %d times", n)
	}
	before = reads.Load()
	if v, ok := tb.Get("soon"); !ok || v != 2 {
		t.Fatalf("Get(soon) = %d, %v", v, ok)
	}
	if n := reads.Load() - before; n != 1 {
		t.Fatalf("reading an entry with a TTL read the clock %d times, want 1", n)
	}
}

// TestSubscriberCannotChangeTheStore checks that a subscriber is refused every call that would
// change the store, instead of deadlocking on the publish it runs inside, and that a table call
// it makes with another context still returns.
func TestSubscriberCannotChangeTheStore(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	f.load(t)
	type result struct {
		newTable, state, drop, register, interned, load, refreshKind error
		background                                                   error
	}
	got := make(chan result, 1)
	f.groups.Subscribe(func(ctx context.Context, _ []Change[group]) {
		var r result
		_, r.newTable = f.s.NewTable[string, int](ctx, "from-subscriber")
		_, r.state = f.s.RegisterState[int](ctx, "state-from-subscriber", Kind("group"))
		r.drop = f.s.DropTable(ctx, "from-subscriber")
		_, r.register = f.s.Register(Kind("late"), nil, emptyLoader[owner]())
		_, r.interned = f.s.RegisterInterned(Kind("late_set"), func(ts *tagSet) Hash { return Hash{} })
		_, _, r.load = f.items.Load(ctx, "i1")
		r.refreshKind = f.s.RefreshKind(ctx, Kind("owner"))
		_, r.background = f.s.NewTable[string, int](context.Background(), "background-from-subscriber")
		select {
		case got <- r:
		default:
		}
	})
	// The worker only reports its errors: t.Fatal may run on the test goroutine alone.
	done := make(chan error, 1)
	go func() {
		if err := f.db.Model(&groupRow{}).Where("id = ?", "g1").Update("tag", "b").Error; err != nil {
			done <- err
			return
		}
		done <- f.s.Refresh(context.Background(), []Ref{{Kind: "group", ID: "g1"}})
	}()
	deadline := time.After(10 * time.Second)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-deadline:
		t.Fatal("a store call from a subscriber deadlocked")
	}
	var r result
	select {
	case r = <-got:
	case <-deadline:
		t.Fatal("the subscriber never ran")
	}
	for name, err := range map[string]error{"NewTable": r.newTable, "RegisterState": r.state, "DropTable": r.drop, "Load": r.load, "RefreshKind": r.refreshKind} {
		if !errors.Is(err, ErrInSubscriber) {
			t.Errorf("%s from a subscriber = %v, want ErrInSubscriber", name, err)
		}
	}
	if !errors.Is(r.register, ErrLoaded) || !errors.Is(r.interned, ErrLoaded) {
		t.Errorf("registration from a subscriber = %v, %v, want ErrLoaded", r.register, r.interned)
	}
	if r.background != nil {
		t.Errorf("a table call with another context must still work, got %v", r.background)
	}
}

// TestUniqueIndexRefusesASecondRecord checks that a refresh which would give a unique key to a
// second record publishes nothing and reports both records, after re-reading the first in case
// it had been renamed.
func TestUniqueIndexRefusesASecondRecord(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	f.load(t)
	f.mustExec(t, f.db.Create(&groupRow{ID: "g4", Name: "n1"}))
	err := f.s.Refresh(context.Background(), []Ref{{Kind: "group", ID: "g4"}})
	var conflict *UniqueConflictError
	if !errors.As(err, &conflict) || !errors.Is(err, ErrUniqueConflict) {
		t.Fatalf("Refresh = %v, want a UniqueConflictError", err)
	}
	if conflict.Index != "name" || conflict.Key != "n1" || !slices.Equal(conflict.IDs, []string{"g1", "g4"}) {
		t.Fatalf("conflict = %+v", conflict)
	}
	if _, ok := f.groups.Get("g4"); ok {
		t.Fatal("a refused refresh must publish nothing")
	}
	if g, ok := f.groups.GetBy("name", "n1"); !ok || g.ID != "g1" {
		t.Fatalf("the key must stay with its holder, got %+v", g)
	}
}

// TestUniqueIndexFollowsARename checks that a key another record held in memory moves to the new
// record when the database shows the holder renamed: the holder is re-read in the same refresh.
func TestUniqueIndexFollowsARename(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	f.load(t)
	f.mustExec(t, f.db.Model(&groupRow{}).Where("id = ?", "g1").Update("name", "n9"))
	f.mustExec(t, f.db.Create(&groupRow{ID: "g4", Name: "n1"}))
	f.refresh(t, Ref{Kind: "group", ID: "g4"})
	if g, ok := f.groups.GetBy("name", "n1"); !ok || g.ID != "g4" {
		t.Fatalf("n1 = %+v, want g4", g)
	}
	if g, ok := f.groups.GetBy("name", "n9"); !ok || g.ID != "g1" {
		t.Fatalf("n9 = %+v, want the renamed g1", g)
	}
}

// TestUniqueIndexFollowsAChainOfRenames checks that a refresh follows the holders of a unique key as
// far as they go: the new record's key is held in memory by a record since renamed to a key a third
// record held before its own rename, and no refresh has brought either rename in yet.
func TestUniqueIndexFollowsAChainOfRenames(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	f.load(t)
	f.mustExec(t, f.db.Model(&groupRow{}).Where("id = ?", "g2").Update("name", "n9"))
	f.mustExec(t, f.db.Model(&groupRow{}).Where("id = ?", "g1").Update("name", "n2"))
	f.mustExec(t, f.db.Create(&groupRow{ID: "g4", Name: "n1"}))
	f.refresh(t, Ref{Kind: "group", ID: "g4"})
	for _, want := range []struct{ name, id string }{{"n1", "g4"}, {"n2", "g1"}, {"n9", "g2"}} {
		if g, ok := f.groups.GetBy("name", want.name); !ok || g.ID != want.id {
			t.Fatalf("%s = %+v, want %s", want.name, g, want.id)
		}
	}
}

// TestUniqueIndexHandsAKeyOverInOneStep checks that while one publish moves a unique key from one
// record to another, a reader finds one record under the key, never both. A publish writes the
// record taking the key before the one giving it up when the taker's ID sorts first, and writes every
// removal after the upserts and their subscribers, so a key that held both for that span would list
// two records and GetBy would return whichever ID sorts first.
func TestUniqueIndexHandsAKeyOverInOneStep(t *testing.T) {
	groupID := func(g *group) string { return g.ID }
	t.Run("from a renamed record", func(t *testing.T) {
		f := newFixture(t)
		f.seed(t)
		f.load(t)
		// g2 gives n2 up and g0 takes it. g0's refresh re-reads g2, the key's holder in memory, so one
		// publish writes g0 and then g2; the hook reads the key as g2's old record leaves the index.
		f.mustExec(t, f.db.Model(&groupRow{}).Where("id = ?", "g2").Update("name", "n9"))
		f.mustExec(t, f.db.Create(&groupRow{ID: "g0", Name: "n2"}))
		held, _ := f.groups.Get("g2")
		var seen [][]string
		hook := func(g *group) {
			if g == held {
				seen = append(seen, ids(f.groups.List("name", "n2"), groupID))
			}
		}
		f.nameHook.Store(&hook)
		f.refresh(t, Ref{Kind: "group", ID: "g0"})
		f.nameHook.Store(nil)
		if len(seen) == 0 {
			t.Fatal("the publish never moved g2 off n2")
		}
		for _, got := range seen {
			if !slices.Equal(got, []string{"g0"}) {
				t.Fatalf("n2 listed %v between the writes of g0 and g2, want only g0", got)
			}
		}
		if g, ok := f.groups.GetBy("name", "n9"); !ok || g.ID != "g2" {
			t.Fatalf("n9 = %+v, want the renamed g2", g)
		}
	})
	t.Run("from a removed record", func(t *testing.T) {
		f := newFixture(t)
		f.seed(t)
		f.load(t)
		// g3 is deleted and g4 takes its name. g4's refresh re-reads g3 and finds its row gone, so one
		// publish writes g4, calls the group subscribers, and only then removes g3.
		f.mustExec(t, f.db.Delete(&groupRow{ID: "g3"}))
		f.mustExec(t, f.db.Create(&groupRow{ID: "g4", Name: "n3"}))
		var listed []string
		var found *group
		f.groups.Subscribe(func(_ context.Context, changes []Change[group]) {
			for _, ch := range changes {
				if ch.ID == "g4" && ch.Old == nil {
					listed = ids(f.groups.List("name", "n3"), groupID)
					found, _ = f.groups.GetBy("name", "n3")
				}
			}
		})
		f.refresh(t, Ref{Kind: "group", ID: "g4"})
		if !slices.Equal(listed, []string{"g4"}) {
			t.Fatalf("n3 listed %v while subscribers learned of g4, want only g4", listed)
		}
		if found == nil || found.ID != "g4" {
			t.Fatalf("n3 = %+v while subscribers learned of g4, want g4", found)
		}
		if _, ok := f.groups.Get("g3"); ok {
			t.Fatal("g3's row is gone, so its record must be removed")
		}
	})
}

// TestLoadRefusesDuplicateUniqueKeys checks that boot fails, naming both records, when the rows
// give one unique key to two records, from the database and from records alike.
func TestLoadRefusesDuplicateUniqueKeys(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	f.mustExec(t, f.db.Create(&groupRow{ID: "g4", Name: "n2"}))
	if err := f.s.LoadAll(context.Background(), f.db); !errors.Is(err, ErrUniqueConflict) {
		t.Fatalf("LoadAll = %v, want ErrUniqueConflict", err)
	}

	s := newStore()
	t.Cleanup(s.Close)
	load := emptyLoader[group]()
	load.ID = func(g *group) string { return g.ID }
	groups, err := s.Register(Kind("group"), nil, load, UniqueIndex("name", func(g *group) string { return g.Name }))
	if err != nil {
		t.Fatal(err)
	}
	recs := []Record{Rec(groups, &group{ID: "a", Name: "same"}), Rec(groups, &group{ID: "b", Name: "same"})}
	if err := s.LoadRecords(context.Background(), recs); !errors.Is(err, ErrUniqueConflict) {
		t.Fatalf("LoadRecords = %v, want ErrUniqueConflict", err)
	}
}

// TestForgetNegative checks that a remembered miss stops Load from reading the database until it
// is forgotten.
func TestForgetNegative(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	f.load(t)
	ctx := context.Background()
	if _, ok, err := f.items.Load(ctx, "i7"); err != nil || ok {
		t.Fatalf("first Load = %v, %v", ok, err)
	}
	f.mustExec(t, f.db.Create(&itemRow{ID: "i7", GroupID: "g1", Label: "later"}))
	if _, ok, err := f.items.Load(ctx, "i7"); err != nil || ok {
		t.Fatalf("a remembered miss must not read again, got %v, %v", ok, err)
	}
	f.items.ForgetNegative("i7")
	if it, ok, err := f.items.Load(ctx, "i7"); err != nil || !ok || it.Label != "later" {
		t.Fatalf("Load after ForgetNegative = %+v, %v, %v", it, ok, err)
	}
}

// TestInternalTablesAreReserved checks that no public call can create or drop a table in the
// store's internal: range, and that the store's own negative table keeps working after a refused
// drop.
func TestInternalTablesAreReserved(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	f.load(t)
	ctx := context.Background()
	if _, err := f.s.NewTable[string, int](ctx, "internal:mine"); err == nil {
		t.Fatal("NewTable must refuse an internal: name")
	}
	if _, err := f.s.RegisterState[int](ctx, "internal:mine", Kind("group")); err == nil {
		t.Fatal("RegisterState must refuse an internal: name")
	}
	if err := f.s.DropTable(ctx, "internal:negative_item"); err == nil {
		t.Fatal("DropTable must refuse an internal: name")
	}
	if _, ok, err := f.items.Load(ctx, "gone"); err != nil || ok {
		t.Fatalf("Load = %v, %v", ok, err)
	}
	if _, ok, err := f.items.Load(ctx, "gone"); err != nil || ok || f.byID.Load() != 1 {
		t.Fatalf("the miss must still be remembered: %v, %v, %d reads", ok, err, f.byID.Load())
	}
}

// TestIndexHoldsALoneRecordInline checks that a key with one record costs a pointer, not a map:
// 20k single-record keys stay well under 100 bytes of index per key (a map per key cost about 285). In
// a multi index, a second record turns the key into a map, and dropping it turns the key inline again.
func TestIndexHoldsALoneRecordInline(t *testing.T) {
	const n = 20000
	ids := make([]string, n)
	recs := make([]*item, n)
	for i := range recs {
		ids[i] = fmt.Sprintf("i%05d", i)
		recs[i] = &item{ID: ids[i], Label: "l" + ids[i]}
	}
	ix := &keyIndex[item]{name: "label", keys: singleKey(func(it *item) string { return it.Label }), idOf: func(it *item) string { return it.ID }}
	ix.data = newShardedMap[keySlot[item]]()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := range recs {
		ix.move(ids[i], nil, recs[i])
	}
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&after)
	perKey := float64(int64(after.HeapAlloc)-int64(before.HeapAlloc)) / n
	t.Logf("index bytes per single-record key: %.1f", perKey)
	if perKey > 100 {
		t.Fatalf("a single-record key costs %.1f bytes of index; want well under 100", perKey)
	}

	label := recs[0].Label
	extra := &item{ID: "z", Label: label}
	ix.move("z", nil, extra)
	ix.data.view(label, func(s keySlot[item], ok bool) {
		if !ok || s.many == nil || len(s.many) != 2 {
			t.Fatalf("a second record must turn the key into a map: %+v", s)
		}
	})
	ix.move("z", extra, nil)
	ix.data.view(label, func(s keySlot[item], ok bool) {
		if !ok || s.many != nil || s.rec != recs[0] {
			t.Fatalf("dropping back to one record must hold it inline again: %+v", s)
		}
	})
	runtime.KeepAlive(recs)
	runtime.KeepAlive(ix)
}

// TestLoadMissFollowsChildren checks a record loaded on a miss carries its ChildOf children, as one a
// refresh brings in does.
func TestLoadMissFollowsChildren(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	f.load(t)
	ctx := context.Background()
	f.mustExec(t, f.db.Create(&groupRow{ID: "g9"}))
	f.mustExec(t, f.db.Create(&itemRow{ID: "i9", GroupID: "g9", Label: "l9"}))
	if _, ok, err := f.groups.Load(ctx, "g9"); err != nil || !ok {
		t.Fatalf("Load of a new row = %v, %v, want found", ok, err)
	}
	if _, ok := f.items.Get("i9"); !ok {
		t.Fatal("a group loaded on a miss must bring its children")
	}
	if _, ok, err := f.groups.Load(ctx, "g-missing"); err != nil || ok {
		t.Fatalf("Load of a missing row = %v, %v, want not found", ok, err)
	}
}

// holdFirstGroupRead makes the first groups read after its query wait until release is called,
// and returns a channel closed once that read has queried. Later reads run straight through.
// reads counts every groups read.
func holdFirstGroupRead(t *testing.T, f *fixture) (queried <-chan struct{}, release func(), reads *atomic.Int32) {
	t.Helper()
	reads = &atomic.Int32{}
	done, gate := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	release = func() { releaseOnce.Do(func() { close(gate) }) }
	t.Cleanup(release)
	hook := func() {
		if reads.Add(1) == 1 {
			close(done)
			<-gate
		}
	}
	f.hook.Store(&hook)
	return done, release, reads
}

// waitForReads waits until key has handed out want request numbers in g, which proves that many
// callers have registered, failing the test after two seconds instead of hanging.
func waitForReads(t *testing.T, g *readGroup, key string, want uint64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		g.mu.Lock()
		st := g.state[key]
		var got uint64
		if st != nil {
			got = st.requested.Load()
		}
		g.mu.Unlock()
		if got >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d registered reads of %q, have %d", want, key, got)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestLoadMissFindsARowWrittenDuringAnEarlierRead checks that a miss is never answered by a read
// that started before the call: a caller that arrives while another caller's read of the same ID
// runs, after the row was written, gets a read of its own and finds the row. What the first caller
// returns is not checked: the follow-up read can publish the row before its final lookup.
func TestLoadMissFindsARowWrittenDuringAnEarlierRead(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	f.load(t)
	ctx := context.Background()
	queried, release, reads := holdFirstGroupRead(t, f)

	first := make(chan error, 1)
	go func() {
		_, _, err := f.groups.Load(ctx, "g9")
		first <- err
	}()
	<-queried
	f.mustExec(t, f.db.Create(&groupRow{ID: "g9", Name: "n9"}))
	second := make(chan *group, 1)
	go func() {
		g, ok, err := f.groups.Load(ctx, "g9")
		if err != nil || !ok {
			t.Errorf("the late Load = %v, %v; want the row written before it was called", ok, err)
		}
		second <- g
	}()
	waitForReads(t, &f.groups.reads, "g9", 2)
	release()

	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if g := <-second; g == nil || g.Name != "n9" {
		t.Fatalf("the late caller got %+v, want g9", g)
	}
	if n := reads.Load(); n != 2 {
		t.Fatalf("made %d reads, want the first and one follow-up", n)
	}
}

// TestConcurrentReadsCostAtMostTwoReads checks that a burst of callers that arrive while a read
// runs share one follow-up read, for misses of a row that does not exist.
func TestConcurrentReadsCostAtMostTwoReads(t *testing.T) {
	for _, tc := range []struct {
		name, id, key string
	}{
		{name: "miss", id: "g8", key: "g8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.seed(t)
			f.load(t)
			ctx := context.Background()
			queried, release, reads := holdFirstGroupRead(t, f)
			const callers = 8
			var wg sync.WaitGroup
			load := func() {
				defer wg.Done()
				if _, _, err := f.groups.Load(ctx, tc.id); err != nil {
					t.Error(err)
				}
			}
			wg.Add(1)
			go load()
			<-queried
			for range callers - 1 {
				wg.Add(1)
				go load()
			}
			waitForReads(t, &f.groups.reads, tc.key, callers)
			release()
			wg.Wait()
			if n := reads.Load(); n != 2 {
				t.Fatalf("%d callers made %d reads, want the first and one shared follow-up", callers, n)
			}
		})
	}
}

// TestReadGroupCallDuringRunGetsFreshRun checks that a caller arriving while a run is in flight
// does not share that run's result but waits for a run that started after its own call.
func TestReadGroupCallDuringRunGetsFreshRun(t *testing.T) {
	var g readGroup
	var runs atomic.Int32
	started := make(chan struct{})
	gate, release := newGate(t)
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- g.Do(context.Background(), "k", func() error {
			runs.Add(1)
			close(started)
			<-gate
			return errors.New("first run")
		})
	}()
	<-started
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- g.Do(context.Background(), "k", func() error {
			runs.Add(1)
			return nil
		})
	}()
	waitForReads(t, &g, "k", 2)
	release()
	if err := <-firstDone; err == nil || err.Error() != "first run" {
		t.Fatalf("first = %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("the second caller got %v, want its own run's result", err)
	}
	if n := runs.Load(); n != 2 {
		t.Fatalf("runs = %d, want 2", n)
	}
}

// TestReadGroupBurstSharesOneFollowUpRun checks that callers piling up behind an in-flight run
// share a single follow-up run.
func TestReadGroupBurstSharesOneFollowUpRun(t *testing.T) {
	var g readGroup
	var runs atomic.Int32
	started := make(chan struct{})
	gate, release := newGate(t)
	const waiters = 10
	results := make(chan error, 1+waiters)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		results <- g.Do(context.Background(), "k", func() error {
			if runs.Add(1) == 1 {
				close(started)
				<-gate
			}
			return nil
		})
	}()
	<-started
	for range waiters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- g.Do(context.Background(), "k", func() error {
				runs.Add(1)
				return nil
			})
		}()
	}
	waitForReads(t, &g, "k", 1+waiters)
	release()
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("a caller got %v, want nil: both runs returned nil", err)
		}
	}
	if n := runs.Load(); n != 2 {
		t.Fatalf("runs = %d, want one in-flight run plus one shared follow-up", n)
	}
}

// TestReadGroupCallerAfterFollowUpStartsGetsThirdRun checks the limit of coalescing: a follow-up
// covers only callers registered before it started, so a caller registering during it gets a
// third run of its own, never the follow-up's result.
func TestReadGroupCallerAfterFollowUpStartsGetsThirdRun(t *testing.T) {
	var g readGroup
	var runs atomic.Int32
	firstStarted, releaseFirstCh := make(chan struct{}), make(chan struct{})
	followUpStarted, releaseFollowUpCh := make(chan struct{}), make(chan struct{})
	var firstOnce, followUpOnce sync.Once
	releaseFirst := func() { firstOnce.Do(func() { close(releaseFirstCh) }) }
	releaseFollowUp := func() { followUpOnce.Do(func() { close(releaseFollowUpCh) }) }
	t.Cleanup(func() {
		releaseFirst()
		releaseFollowUp()
	})
	results := make(chan error, 3)
	go func() {
		results <- g.Do(context.Background(), "k", func() error {
			runs.Add(1)
			close(firstStarted)
			<-releaseFirstCh
			return nil
		})
	}()
	<-firstStarted
	go func() {
		results <- g.Do(context.Background(), "k", func() error {
			runs.Add(1)
			close(followUpStarted)
			<-releaseFollowUpCh
			return errors.New("follow-up run")
		})
	}()
	waitForReads(t, &g, "k", 2)
	releaseFirst()
	received := 0
	for waiting := true; waiting; {
		select {
		case <-followUpStarted:
			waiting = false
		case <-results:
			received++
			if received == 2 {
				t.Fatal("the second caller returned without running a follow-up")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for the follow-up run to start")
		}
	}
	lateDone := make(chan error, 1)
	go func() {
		lateDone <- g.Do(context.Background(), "k", func() error {
			runs.Add(1)
			return nil
		})
	}()
	waitForReads(t, &g, "k", 3)
	releaseFollowUp()
	select {
	case err := <-lateDone:
		if err != nil {
			t.Fatalf("the late caller got %v, want its own run, not the follow-up's error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the late caller")
	}
	if n := runs.Load(); n != 3 {
		t.Fatalf("runs = %d, want 3", n)
	}
	for ; received < 2; received++ {
		select {
		case <-results:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for the first two callers")
		}
	}
}

// TestReadGroupLiveCallerDoesNotInheritCancellation checks that when a shared run failed only
// because its runner's context was canceled, a covered caller with a live context runs its own.
func TestReadGroupLiveCallerDoesNotInheritCancellation(t *testing.T) {
	var g readGroup
	lockedFirst := make(chan struct{})
	gate, releaseFirst := newGate(t)
	var hookOnce sync.Once
	g.runLockedHook = func() {
		hookOnce.Do(func() {
			close(lockedFirst)
			<-gate
		})
	}
	canceledCtx, cancel := context.WithCancel(context.Background())
	runnerDone := make(chan error, 1)
	go func() {
		runnerDone <- g.Do(canceledCtx, "k", func() error {
			cancel()
			return canceledCtx.Err()
		})
	}()
	<-lockedFirst
	var liveRan atomic.Bool
	liveDone := make(chan error, 1)
	go func() {
		liveDone <- g.Do(context.Background(), "k", func() error {
			liveRan.Store(true)
			return nil
		})
	}()
	waitForReads(t, &g, "k", 2)
	releaseFirst()
	if err := <-runnerDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("runner = %v, want context.Canceled", err)
	}
	if err := <-liveDone; err != nil {
		t.Fatalf("a live caller got %v, want no other caller's cancellation", err)
	}
	if !liveRan.Load() {
		t.Fatal("the live caller must run its own read")
	}
}

// TestReadGroupSharesOtherErrors checks that a covered caller shares a run's error when it is not
// the runner's cancellation.
func TestReadGroupSharesOtherErrors(t *testing.T) {
	var g readGroup
	lockedFirst := make(chan struct{})
	gate, releaseFirst := newGate(t)
	var hookOnce sync.Once
	g.runLockedHook = func() {
		hookOnce.Do(func() {
			close(lockedFirst)
			<-gate
		})
	}
	storeErr := errors.New("store unavailable")
	runnerDone := make(chan error, 1)
	go func() {
		runnerDone <- g.Do(context.Background(), "k", func() error { return storeErr })
	}()
	<-lockedFirst
	var secondRan atomic.Bool
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- g.Do(context.Background(), "k", func() error {
			secondRan.Store(true)
			return nil
		})
	}()
	waitForReads(t, &g, "k", 2)
	releaseFirst()
	if err := <-runnerDone; !errors.Is(err, storeErr) {
		t.Fatalf("runner = %v", err)
	}
	if err := <-secondDone; !errors.Is(err, storeErr) {
		t.Fatalf("a covered caller got %v, want the shared store error", err)
	}
	if secondRan.Load() {
		t.Fatal("a covered caller must not run again after a store error")
	}
}

// TestReadGroupSharesARunsOwnTimeout checks that a covered caller shares a deadline error the run
// hit on a timeout of its own while the runner's context stayed live: that is the run's answer,
// not the runner's cancellation, so the caller does not run again.
func TestReadGroupSharesARunsOwnTimeout(t *testing.T) {
	var g readGroup
	lockedFirst := make(chan struct{})
	gate, releaseFirst := newGate(t)
	var hookOnce sync.Once
	g.runLockedHook = func() {
		hookOnce.Do(func() {
			close(lockedFirst)
			<-gate
		})
	}
	timeoutErr := fmt.Errorf("upstream: %w", context.DeadlineExceeded)
	runnerDone := make(chan error, 1)
	go func() {
		runnerDone <- g.Do(context.Background(), "k", func() error { return timeoutErr })
	}()
	<-lockedFirst
	var secondRan atomic.Bool
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- g.Do(context.Background(), "k", func() error {
			secondRan.Store(true)
			return nil
		})
	}()
	waitForReads(t, &g, "k", 2)
	releaseFirst()
	if err := <-runnerDone; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("runner = %v", err)
	}
	if err := <-secondDone; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a covered caller got %v, want the run's own timeout", err)
	}
	if secondRan.Load() {
		t.Fatal("a covered caller must not run again after the run's own timeout")
	}
}

// TestReadGroupKeysAreIndependent checks that a slow run for one key does not block another key.
func TestReadGroupKeysAreIndependent(t *testing.T) {
	var g readGroup
	started, release := make(chan struct{}), make(chan struct{})
	go func() {
		_ = g.Do(context.Background(), "slow", func() error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	defer close(release)
	done := make(chan struct{})
	go func() {
		_ = g.Do(context.Background(), "fast", func() error { return nil })
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a run for another key was blocked")
	}
}

// TestReadGroupDropsIdleState checks that per-key state is removed once no caller holds it.
func TestReadGroupDropsIdleState(t *testing.T) {
	var g readGroup
	for _, key := range []string{"a", "b", "c"} {
		if err := g.Do(context.Background(), key, func() error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.state) != 0 {
		t.Fatalf("state = %v, want empty", g.state)
	}
}

// TestTableKeepsEntriesThatNeverExpireAsTheirValueAlone checks where a table keeps an entry: one
// that neither expires nor carries a generation as its value alone, any other with its expiry and
// generation, moving between the two as it is written, and that every read and sweep sees both.
func TestTableKeepsEntriesThatNeverExpireAsTheirValueAlone(t *testing.T) {
	clock := setClock(t, time.Unix(1000, 0))
	tb := newTable[string, int](tableOptions{})
	plain := func(k string) bool {
		sh := tb.shard(k)
		_, inPlain := sh.plain[k]
		_, inM := sh.m[k]
		return inPlain && !inM
	}
	tb.Put("a", 1)
	if !plain("a") {
		t.Fatal("an entry that never expires is not kept as its value alone")
	}
	tb.PutWithTTL("a", 2, time.Minute)
	if plain("a") {
		t.Fatal("an entry that expires is kept without its expiry")
	}
	if v, ok := tb.Get("a"); !ok || v != 2 {
		t.Fatalf("Get = %d, %v; want 2", v, ok)
	}
	*clock = clock.Add(2 * time.Minute)
	if _, ok := tb.Get("a"); ok {
		t.Fatal("an expired entry is read")
	}
	if !tb.PutIfAbsent("a", 3, 0) || !plain("a") {
		t.Fatal("PutIfAbsent over an expired entry did not store a plain one")
	}
	if !CompareAndSwap(tb, "a", 3, 4) || !plain("a") {
		t.Fatal("CompareAndSwap did not replace the plain entry")
	}
	tb.PutWithTTL("b", 5, time.Minute)
	seen := map[string]int{}
	tb.Range(func(k string, v int) bool { seen[k] = v; return true })
	if len(seen) != 2 || seen["a"] != 4 || seen["b"] != 5 {
		t.Fatalf("Range saw %v", seen)
	}
	tb.Delete("a")
	if _, ok := tb.Get("a"); ok {
		t.Fatal("a deleted plain entry is read")
	}

	gen := uint64(0)
	memo := newTable[string, int](tableOptions{gen: func() uint64 { return gen }})
	memo.Put("k", 1)
	if v, ok := memo.Get("k"); !ok || v != 1 {
		t.Fatal("a memo entry under generation zero is not read")
	}
	gen = 1
	if _, ok := memo.Get("k"); ok {
		t.Fatal("a memo entry from an older generation is read")
	}
	memo.sweep(clock.UnixNano())
	if sh := memo.shard("k"); len(sh.plain)+len(sh.m) != 0 {
		t.Fatal("the sweep kept a memo entry from an older generation")
	}
}

// TestShareIDsGivesRecordsTheNamedRecordsCopy checks that a record built by a loader with ShareIDs
// holds the copy of an ID the named record holds, on a load and on a refresh, and keeps its own
// copy of an ID no record holds.
func TestShareIDsGivesRecordsTheNamedRecordsCopy(t *testing.T) {
	db := openDB(t)
	s := newStore()
	t.Cleanup(s.Close)
	owners, err := s.Register(Kind("owner"), nil, ownerLoader())
	if err != nil {
		t.Fatal(err)
	}
	items, err := s.Register(Kind("item"), []Kind{"owner"}, itemLoader(),
		ShareIDs(func(it *item, shared func(Kind, string) string) { it.OwnerID = shared(Kind("owner"), it.OwnerID) }))
	if err != nil {
		t.Fatal(err)
	}
	o1, gone := "o1", "o-gone"
	if err := db.Create(&ownerRow{ID: "o1", Name: "owner one"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create([]itemRow{{ID: "i1", OwnerID: &o1}, {ID: "i2", OwnerID: &o1}, {ID: "i3", OwnerID: &gone}}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.LoadAll(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	owner, _ := owners.Get("o1")
	same := func(id string) bool {
		it, ok := items.Get(id)
		return ok && unsafe.StringData(it.OwnerID) == unsafe.StringData(owner.ID)
	}
	if !same("i1") || !same("i2") {
		t.Fatal("a loaded item keeps its own copy of its owner's ID")
	}
	// The row changes first, so the refresh publishes a new record instead of keeping the loaded
	// one, which already shares the ID.
	if err := db.Model(&itemRow{}).Where("id = ?", "i1").Update("label", "changed").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.Refresh(context.Background(), []Ref{{Kind: "item", ID: "i1"}}); err != nil {
		t.Fatal(err)
	}
	if it, ok := items.Get("i1"); !ok || it.Label != "changed" {
		t.Fatalf("i1 = %+v, %v; want the refreshed row", it, ok)
	}
	if !same("i1") {
		t.Fatal("a refreshed item keeps its own copy of its owner's ID")
	}
	if it, ok := items.Get("i3"); !ok || it.OwnerID != "o-gone" {
		t.Fatalf("an item naming no held owner keeps its ID, got %+v", it)
	}
}
