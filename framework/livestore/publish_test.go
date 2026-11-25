package livestore

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"gorm.io/gorm"
)

// shelf and book are a parent and its children on a ChildIDsOf link: a book names its shelf.
type shelf struct{ ID string }

type book struct{ ID, ShelfID string }

// panel and gauge are a record that lists the records it holds, by ID, and those records, its
// children on a Cascade link: a gauge names its panel, and a panel names its gauges.
type panel struct {
	ID       string
	GaugeIDs []string
}

type gauge struct{ ID, PanelID string }

// execAll runs each statement on db and fails the test on an error.
func execAll(t *testing.T, db *gorm.DB, stmts ...string) {
	t.Helper()
	for _, stmt := range stmts {
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
}

// rowsLoader is a loader whose reads return the records scan builds from the rows a where clause
// selects; Page reads every row at once.
func rowsLoader[T any](id func(*T) string, scan func(ctx context.Context, db *gorm.DB, where string, args ...any) ([]*T, error)) Loader[T] {
	return Loader[T]{
		ID: id,
		Page: func(ctx context.Context, db *gorm.DB, after string, limit int) ([]*T, string, error) {
			recs, err := scan(ctx, db, "id > ?", after)
			return recs, "", err
		},
		ByID: func(ctx context.Context, db *gorm.DB, ids []string) ([]*T, error) {
			return scan(ctx, db, "id IN ?", ids)
		},
	}
}

// TestAChildWhoseRowLeftItsParentWhileReadIsNotPublished: a ChildIDsOf link reads a parent's child
// IDs and then the children's records, so a row that leaves the parent between the two reads comes
// back naming no parent. Published, it would be held with nothing to drop it, since no parent
// lists it. A new child read that way is not published, a held one is removed, and later refreshes
// of the parent leave both out.
func TestAChildWhoseRowLeftItsParentWhileReadIsNotPublished(t *testing.T) {
	ctx := context.Background()
	// The fixture's store and database, with this test's kinds registered beside its own.
	f := newFixture(t)
	db, s := f.db, f.s
	execAll(t, db,
		"CREATE TABLE shelves (id TEXT PRIMARY KEY)",
		"CREATE TABLE books (id TEXT PRIMARY KEY, shelf_id TEXT)",
		"INSERT INTO shelves (id) VALUES ('s1')",
		"INSERT INTO books (id, shelf_id) VALUES ('b1', 's1')",
	)
	var between atomic.Pointer[func()]
	shelves, err := s.Register(Kind("shelf"), nil, rowsLoader(func(sh *shelf) string { return sh.ID },
		func(ctx context.Context, db *gorm.DB, where string, args ...any) ([]*shelf, error) {
			var rows []*shelf
			err := db.WithContext(ctx).Table("shelves").Where(where, args...).Find(&rows).Error
			return rows, err
		}))
	if err != nil {
		t.Fatal(err)
	}
	books, err := s.Register(Kind("book"), nil, rowsLoader(func(b *book) string { return b.ID },
		func(ctx context.Context, db *gorm.DB, where string, args ...any) ([]*book, error) {
			var rows []struct {
				ID      string
				ShelfID *string
			}
			if err := db.WithContext(ctx).Table("books").Where(where, args...).Find(&rows).Error; err != nil {
				return nil, err
			}
			out := make([]*book, 0, len(rows))
			for _, r := range rows {
				b := &book{ID: r.ID}
				if r.ShelfID != nil {
					b.ShelfID = *r.ShelfID
				}
				out = append(out, b)
			}
			return out, nil
		}),
		ChildIDsOf(Kind("shelf"), func(b *book) string { return b.ShelfID }, func(ctx context.Context, db *gorm.DB, shelfIDs []string) ([]string, error) {
			var ids []string
			if err := db.WithContext(ctx).Table("books").Where("shelf_id IN ?", shelfIDs).Pluck("id", &ids).Error; err != nil {
				return nil, err
			}
			if f := between.Load(); f != nil {
				(*f)()
			}
			return ids, nil
		}, Cascade))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LoadAll(ctx, db); err != nil {
		t.Fatal(err)
	}
	held := func(id string) bool {
		_, ok := books.Get(id)
		return ok
	}
	if !held("b1") {
		t.Fatal("the load did not publish b1 on its shelf")
	}
	// leave moves book id off its shelf right after the nth read of the shelf's book IDs, and
	// reports whether that read happened, so a refresh that never got that far fails the test.
	leave := func(id string, nth int32) *atomic.Bool {
		var reads atomic.Int32
		var moved atomic.Bool
		f := func() {
			if reads.Add(1) == nth {
				execAll(t, db, "UPDATE books SET shelf_id = NULL WHERE id = '"+id+"'")
				moved.Store(true)
			}
		}
		between.Store(&f)
		return &moved
	}
	refreshShelf := func() {
		t.Helper()
		if err := s.Refresh(ctx, []Ref{{Kind: Kind("shelf"), ID: "s1"}}); err != nil {
			t.Fatal(err)
		}
	}

	// The first read finds b2, which no lock covers yet, so the refresh takes its lock and reads
	// again: the second read is the one it publishes.
	execAll(t, db, "INSERT INTO books (id, shelf_id) VALUES ('b2', 's1')")
	moved := leave("b2", 2)
	refreshShelf()
	if !moved.Load() {
		t.Fatal("the refresh did not read the shelf's book IDs a second time")
	}
	if held("b2") {
		t.Fatal("a new book read with no shelf was published")
	}
	if _, ok := shelves.Get("s1"); !ok || !held("b1") {
		t.Fatalf("the refresh dropped the shelf or b1: shelf held %v, b1 held %v", ok, held("b1"))
	}

	moved = leave("b1", 1)
	refreshShelf()
	if !moved.Load() {
		t.Fatal("the refresh did not read the shelf's book IDs")
	}
	if held("b1") {
		t.Fatal("a held book read with no shelf was kept, where no shelf lists it any more")
	}

	between.Store(nil)
	refreshShelf()
	if held("b1") || held("b2") {
		t.Fatal("a later refresh of the shelf brought back a book that names no shelf")
	}
}

// gaugeCall is one call a gauge subscriber received: the gauges it was told about, new or
// changed and removed, the ones of those the gauge collection already reflected while it ran
// (an added gauge it held, a removed one it no longer held), and the gauges the panel's record
// named while it ran.
type gaugeCall struct {
	added, removed []string
	applied        []string
	panelNamed     []string
}

// TestAReferencedKindsSubscribersSeeItsRecordsBeforeARecordNamesThem: a panel replaces the gauge it
// names in one refresh, as a holder replaces a limit it lists. A reader that goes from the panel
// to its gauges, and needs what the gauge subscriber derives from each, must never find the panel
// naming a gauge the subscriber has not seen yet, nor naming one already gone. So the subscriber
// is told about the new gauge while the panel still names the old one, and about the old gauge's
// removal once the panel names the new one: two calls, new records first. Each call comes after
// the gauge collection reflects the change it reports, so the subscriber can read it back.
func TestAReferencedKindsSubscribersSeeItsRecordsBeforeARecordNamesThem(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	db, s := f.db, f.s
	execAll(t, db,
		"CREATE TABLE panels (id TEXT PRIMARY KEY)",
		"CREATE TABLE gauges (id TEXT PRIMARY KEY, panel_id TEXT NOT NULL)",
		"INSERT INTO panels (id) VALUES ('p1')",
		"INSERT INTO gauges (id, panel_id) VALUES ('g-old', 'p1')",
	)
	scanGauges := func(ctx context.Context, db *gorm.DB, where string, args ...any) ([]*gauge, error) {
		var rows []*gauge
		err := db.WithContext(ctx).Table("gauges").Where(where, args...).Order("id").Find(&rows).Error
		return rows, err
	}
	gauges, err := s.Register(Kind("gauge"), nil, rowsLoader(func(g *gauge) string { return g.ID }, scanGauges),
		ChildOf(Kind("panel"), func(g *gauge) string { return g.PanelID }, func(ctx context.Context, db *gorm.DB, panelIDs []string) ([]*gauge, error) {
			return scanGauges(ctx, db, "panel_id IN ?", panelIDs)
		}, Cascade))
	if err != nil {
		t.Fatal(err)
	}
	panels, err := s.Register(Kind("panel"), []Kind{"gauge"}, rowsLoader(func(p *panel) string { return p.ID },
		func(ctx context.Context, db *gorm.DB, where string, args ...any) ([]*panel, error) {
			var ids []string
			if err := db.WithContext(ctx).Table("panels").Where(where, args...).Order("id").Pluck("id", &ids).Error; err != nil {
				return nil, err
			}
			out := make([]*panel, 0, len(ids))
			for _, id := range ids {
				held, err := scanGauges(ctx, db, "panel_id = ?", id)
				if err != nil {
					return nil, err
				}
				p := &panel{ID: id}
				for _, g := range held {
					p.GaugeIDs = append(p.GaugeIDs, g.ID)
				}
				out = append(out, p)
			}
			return out, nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LoadAll(ctx, db); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var calls []gaugeCall
	gauges.Subscribe(func(_ context.Context, changes []Change[gauge]) {
		call := gaugeCall{}
		for _, c := range changes {
			_, held := gauges.Get(c.ID)
			if c.New != nil {
				call.added = append(call.added, c.ID)
			} else {
				call.removed = append(call.removed, c.ID)
			}
			if held == (c.New != nil) {
				call.applied = append(call.applied, c.ID)
			}
		}
		if p, ok := panels.Get("p1"); ok {
			call.panelNamed = slices.Clone(p.GaugeIDs)
		}
		mu.Lock()
		calls = append(calls, call)
		mu.Unlock()
	})

	execAll(t, db, "DELETE FROM gauges WHERE id = 'g-old'", "INSERT INTO gauges (id, panel_id) VALUES ('g-new', 'p1')")
	if err := s.Refresh(ctx, []Ref{{Kind: Kind("panel"), ID: "p1"}}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []gaugeCall{
		{added: []string{"g-new"}, applied: []string{"g-new"}, panelNamed: []string{"g-old"}},
		{removed: []string{"g-old"}, applied: []string{"g-old"}, panelNamed: []string{"g-new"}},
	}
	if len(calls) != len(want) {
		t.Fatalf("gauge subscriber calls = %+v, want %+v", calls, want)
	}
	for i := range want {
		if !slices.Equal(calls[i].added, want[i].added) || !slices.Equal(calls[i].removed, want[i].removed) ||
			!slices.Equal(calls[i].applied, want[i].applied) || !slices.Equal(calls[i].panelNamed, want[i].panelNamed) {
			t.Fatalf("gauge subscriber calls = %+v, want %+v", calls, want)
		}
	}
}
