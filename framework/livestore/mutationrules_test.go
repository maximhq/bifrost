package livestore

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
)

// TestRetryErrorsWithinRereadsATransientError checks that a refresh with RetryErrorsWithin reads
// again after failed reads and publishes what the read that succeeds finds.
func TestRetryErrorsWithinRereadsATransientError(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	f.load(t)
	f.mustExec(t, f.db.Model(&groupRow{}).Where("id = ?", "g1").Update("name", "n1b"))
	reads := failReads(f, 2, nil)
	if err := f.s.Refresh(context.Background(), []Ref{{Kind: "group", ID: "g1"}}, RetryErrorsWithin(time.Minute, time.Millisecond)); err != nil {
		t.Fatalf("Refresh = %v, want a retry to read the row", err)
	}
	if g, _ := f.groups.Get("g1"); g.Name != "n1b" {
		t.Fatalf("g1 = %+v, want the row the retry read", g)
	}
	if n := reads.Load(); n != 3 {
		t.Fatalf("made %d reads, want two failed ones and the one that succeeded", n)
	}
}

// TestRetryErrorsWithinStopsAtItsLimit checks that a read that keeps failing is retried until the
// context's deadline, whatever the budget, or until the budget when the context has none, and then
// returns the read's own error. With a deadline no read starts after it. The limits are wide enough
// that a scheduler stall of a few tens of milliseconds cannot fail a correct build.
func TestRetryErrorsWithinStopsAtItsLimit(t *testing.T) {
	const wait = 5 * time.Millisecond
	for _, tc := range []struct {
		name            string
		timeout, budget time.Duration
	}{
		{name: "a deadline later than the budget", timeout: 400 * time.Millisecond, budget: 10 * time.Millisecond},
		{name: "a deadline sooner than the budget", timeout: 200 * time.Millisecond, budget: time.Hour},
		{name: "the budget, with no deadline", budget: 300 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.seed(t)
			f.load(t)
			// Every case can be cancelled, so the watchdog can stop a refresh that never stops
			// retrying before the next test replaces anything it reads.
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			start := time.Now()
			limit := start.Add(tc.budget)
			if tc.timeout > 0 {
				var cancelTimeout context.CancelFunc
				ctx, cancelTimeout = context.WithTimeout(ctx, tc.timeout)
				defer cancelTimeout()
				limit, _ = ctx.Deadline()
			}
			var starts []time.Time // appended only by the reads, which run on the refresh's goroutine
			failReads(f, math.MaxInt32, func() { starts = append(starts, time.Now()) })
			done := make(chan error, 1)
			go func() { done <- f.s.Refresh(ctx, []Ref{{Kind: "group", ID: "g1"}}, RetryErrorsWithin(tc.budget, wait)) }()
			var err error
			select {
			case err = <-done:
			case <-time.After(time.Until(limit) + 5*time.Second):
				cancel()
				// Wait for the refresh to return, so it cannot read retryWait while the next test
				// replaces it; the test binary's timeout bounds a refresh that ignores cancellation.
				<-done
				t.Fatal("Refresh was still retrying long after its limit")
			}
			if err == nil || !strings.Contains(err.Error(), "read failed") {
				t.Fatalf("Refresh = %v, want the read's error", err)
			}
			if len(starts) < 2 {
				t.Fatalf("made %d reads, want retries until the limit", len(starts))
			}
			// Half the time to the limit: a read that late shows the shorter budget did not end the
			// retries when the context has a later deadline.
			if last := starts[len(starts)-1]; last.Before(start.Add(limit.Sub(start) / 2)) {
				t.Fatalf("the last read started %v after the refresh, want retries up to near its limit %v", last.Sub(start), limit.Sub(start))
			}
			if tc.timeout > 0 {
				// A retry admitted before the deadline may reach its read after it, but none is
				// admitted once the deadline has passed, however the scheduler delays the reads: at
				// most one read starts at or after the deadline.
				late := 0
				for _, at := range starts {
					if !at.Before(limit) {
						late++
					}
				}
				if late > 1 {
					t.Fatalf("%d reads started at or after the context's deadline, want at most the one admitted before it", late)
				}
			}
		})
	}
}

// TestRetryErrorsWithinChecksItsLimitAfterTheWait checks that a retry whose wait ends past the
// limit is not read: the limit decides when the wait is over, not only before it starts. The wait
// is made to wake late, as a busy scheduler can, so with no context deadline to stop it the next
// read would otherwise start after the budget. The budget leaves the first retry ample time to be
// admitted, and the test requires that its wait ran, so the check before the wait cannot be the
// one that stops it.
func TestRetryErrorsWithinChecksItsLimitAfterTheWait(t *testing.T) {
	const budget, wait, late = 200 * time.Millisecond, 5 * time.Millisecond, 250 * time.Millisecond
	var waits atomic.Int32
	previous := retryWait
	retryWait = func(ctx context.Context, d time.Duration) error {
		waits.Add(1)
		return sleep(ctx, d+late)
	}
	t.Cleanup(func() { retryWait = previous })
	f := newFixture(t)
	f.seed(t)
	f.load(t)
	start := time.Now()
	var starts []time.Time // appended only by the reads, which run on the refresh's goroutine
	failReads(f, math.MaxInt32, func() { starts = append(starts, time.Now()) })
	err := f.s.Refresh(context.Background(), []Ref{{Kind: "group", ID: "g1"}}, RetryErrorsWithin(budget, wait))
	if err == nil || !strings.Contains(err.Error(), "read failed") {
		t.Fatalf("Refresh = %v, want the read's error", err)
	}
	if n := waits.Load(); n < 1 {
		t.Fatalf("the retry wait ran %d times, want the first retry admitted and its wait run", n)
	}
	for i, at := range starts {
		if !at.Before(start.Add(budget)) {
			t.Fatalf("read %d started %v after the refresh, at or past its %v budget", i, at.Sub(start), budget)
		}
	}
}

// TestRetryErrorsWithinNeverRetriesOnceTheContextEnds checks that RetryErrorsWithin, with budget
// to spare, reads no more once its context is cancelled, whether it ends during the failed read
// or during the wait before the next one, and returns the read's error.
func TestRetryErrorsWithinNeverRetriesOnceTheContextEnds(t *testing.T) {
	const budget = 200 * time.Millisecond
	t.Run("during the read", func(t *testing.T) {
		f := newFixture(t)
		f.seed(t)
		f.load(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		reads := failReads(f, 10, cancel)
		err := f.s.Refresh(ctx, []Ref{{Kind: "group", ID: "g1"}}, RetryErrorsWithin(budget, time.Millisecond))
		if err == nil || !strings.Contains(err.Error(), "read failed") {
			t.Fatalf("Refresh = %v, want the read's error", err)
		}
		if n := reads.Load(); n != 1 {
			t.Fatalf("made %d reads, want no retry once the context ended", n)
		}
	})
	t.Run("during the wait", func(t *testing.T) {
		f := newFixture(t)
		f.seed(t)
		f.load(t)
		for range 100 {
			base, cancel := context.WithCancel(context.Background())
			ctx := &endsAfterCheck{Context: base, cancel: cancel}
			reads := failReads(f, 10, func() { ctx.armed.Store(true) })
			err := f.s.Refresh(ctx, []Ref{{Kind: "group", ID: "g1"}}, RetryErrorsWithin(budget, time.Nanosecond))
			cancel()
			if err == nil || !strings.Contains(err.Error(), "read failed") {
				t.Fatalf("Refresh = %v, want the read's error", err)
			}
			if n := reads.Load(); n != 1 {
				t.Fatalf("made %d reads, want no retry once the context ended during the wait", n)
			}
		}
	})
}

// TestRetryErrorsWithinComposesWithTheOtherRetries checks that a missing row read after a failed
// read is still kept and reported under RetryMissing, whichever order the two come in, and that of
// RetryErrors and RetryErrorsWithin the one given last applies.
func TestRetryErrorsWithinComposesWithTheOtherRetries(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	f.load(t)
	ctx := context.Background()

	f.mustExec(t, f.db.Delete(&groupRow{ID: "g2"}))
	for _, opts := range [][]RefreshOption{
		{RetryErrorsWithin(time.Minute, time.Millisecond), RetryMissing(1, time.Millisecond)},
		{RetryMissing(1, time.Millisecond), RetryErrorsWithin(time.Minute, time.Millisecond)},
	} {
		failReads(f, 1, nil)
		err := f.s.Refresh(ctx, []Ref{{Kind: "group", ID: "g2"}}, opts...)
		var missing *MissingError
		if !errors.As(err, &missing) || len(missing.Refs) != 1 || missing.Refs[0].ID != "g2" {
			t.Fatalf("a missing row read after a failed read = %v, want a MissingError", err)
		}
		if _, ok := f.groups.Get("g2"); !ok {
			t.Fatal("RetryMissing must still keep the record when RetryErrorsWithin is given too")
		}
	}

	for _, tc := range []struct {
		name  string
		opts  []RefreshOption
		reads int32
	}{
		{name: "RetryErrors last", opts: []RefreshOption{RetryErrorsWithin(time.Minute, time.Millisecond), RetryErrors(0, 0)}, reads: 1},
		{name: "RetryErrorsWithin last", opts: []RefreshOption{RetryErrors(0, 0), RetryErrorsWithin(time.Minute, time.Millisecond)}, reads: 2},
	} {
		reads := failReads(f, 1, nil)
		err := f.s.Refresh(ctx, []Ref{{Kind: "group", ID: "g1"}}, tc.opts...)
		if got := reads.Load(); got != tc.reads || (err == nil) != (tc.reads == 2) {
			t.Fatalf("%s: made %d reads and returned %v, want %d reads", tc.name, got, err, tc.reads)
		}
	}
}

// pin is a record on a SetNull link to a group: deleting the group clears its reference.
type pin struct{ ID, GroupID string }

// TestRemovePublishesBeforeItsHoldersAreReread checks that a Remove ref drops its record and its
// Cascade children even when the re-read of its SetNull children fails, returning that read's
// error, and that those children are re-read with the call's retry options. A read of another row
// in the same call that fails does not hold the removal back either.
func TestRemovePublishesBeforeItsHoldersAreReread(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.seed(t)
	execAll(t, f.db,
		"CREATE TABLE pins (id TEXT PRIMARY KEY, group_id TEXT)",
		"INSERT INTO pins (id, group_id) VALUES ('p1', 'g1'), ('p2', 'g1'), ('p3', 'g2')",
	)
	var pinFailures, pinReads atomic.Int32
	scan := func(ctx context.Context, db *gorm.DB, where string, args ...any) ([]*pin, error) {
		var rows []struct {
			ID      string
			GroupID *string
		}
		if err := db.WithContext(ctx).Table("pins").Where(where, args...).Find(&rows).Error; err != nil {
			return nil, err
		}
		out := make([]*pin, 0, len(rows))
		for _, r := range rows {
			p := &pin{ID: r.ID}
			if r.GroupID != nil {
				p.GroupID = *r.GroupID
			}
			out = append(out, p)
		}
		return out, nil
	}
	load := rowsLoader(func(p *pin) string { return p.ID }, scan)
	load.ByID = func(ctx context.Context, db *gorm.DB, ids []string) ([]*pin, error) {
		pinReads.Add(1)
		if pinFailures.Add(-1) >= 0 {
			return nil, errors.New("pin read failed")
		}
		return scan(ctx, db, "id IN ?", ids)
	}
	pins, err := f.s.Register(Kind("pin"), []Kind{"group"}, load,
		ChildOf(Kind("group"), func(p *pin) string { return p.GroupID }, func(ctx context.Context, db *gorm.DB, groupIDs []string) ([]*pin, error) {
			return scan(ctx, db, "group_id IN ?", groupIDs)
		}, SetNull))
	if err != nil {
		t.Fatal(err)
	}
	f.load(t)
	deleteGroup := func(id string) {
		execAll(t, f.db,
			"UPDATE pins SET group_id = NULL WHERE group_id = '"+id+"'",
			"DELETE FROM items WHERE group_id = '"+id+"'",
			"DELETE FROM groups WHERE id = '"+id+"'",
		)
	}
	held := func(kind Kind, id string) bool {
		switch kind {
		case "group":
			_, ok := f.groups.Get(id)
			return ok
		case "item":
			_, ok := f.items.Get(id)
			return ok
		}
		_, ok := pins.Get(id)
		return ok
	}

	// Every read of the holders fails: the group and its items go anyway, and the error is returned.
	deleteGroup("g1")
	pinFailures.Store(math.MaxInt32)
	err = f.s.Refresh(ctx, []Ref{{Kind: "group", ID: "g1", Remove: true}})
	if err == nil || !strings.Contains(err.Error(), "pin read failed") {
		t.Fatalf("Refresh = %v, want the holder read's error", err)
	}
	if held("group", "g1") || held("item", "i1") || held("item", "i2") {
		t.Fatalf("a failed holder read kept the removal back: g1 %v, i1 %v, i2 %v", held("group", "g1"), held("item", "i1"), held("item", "i2"))
	}
	if p, ok := pins.Get("p1"); !ok || p.GroupID != "g1" {
		t.Fatalf("p1 = %+v, %v; want it kept as it was, since its re-read failed", p, ok)
	}
	pinFailures.Store(0)
	if err := f.s.Refresh(ctx, []Ref{{Kind: "pin", ID: "p1"}}); err != nil {
		t.Fatal(err)
	}
	if p, _ := pins.Get("p1"); p.GroupID != "" {
		t.Fatalf("p1 = %+v, want its next refresh to clear the reference", p)
	}

	// The holders are re-read with the call's options: one failure, then a retry that succeeds.
	deleteGroup("g2")
	pinFailures.Store(1)
	pinReads.Store(0)
	if err := f.s.Refresh(ctx, []Ref{{Kind: "group", ID: "g2", Remove: true}}, RetryErrors(2, time.Millisecond)); err != nil {
		t.Fatalf("Refresh = %v, want the retry to re-read the holder", err)
	}
	if held("group", "g2") || held("item", "i3") {
		t.Fatal("the removal of g2 or its item did not publish")
	}
	if p, ok := pins.Get("p3"); !ok || p.GroupID != "" || pinReads.Load() != 2 {
		t.Fatalf("p3 = %+v, %v after %d reads; want it re-read by a retry with the reference cleared", p, ok, pinReads.Load())
	}

	// A failed read of another row in the same call does not hold the removal back.
	deleteGroup("g3")
	failReads(f, math.MaxInt32, nil)
	err = f.s.Refresh(ctx, []Ref{{Kind: "group", ID: "g3", Remove: true}, {Kind: "group", ID: "g9"}})
	f.readErr.Store(nil)
	if err == nil || !strings.Contains(err.Error(), "read failed") {
		t.Fatalf("Refresh = %v, want the other row's read error", err)
	}
	if held("group", "g3") {
		t.Fatal("a failed read of another row kept the removal of g3 back")
	}
}

// entry is a record on an ordered Cascade link to a group, kept in Order within its group.
type entry struct {
	ID, GroupID string
	Order       int64
	Label       string
}
