package livestore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unsafe"

	"gorm.io/gorm"
)

// ldParent and ldChild are records of two kinds where the child depends on the parent.
type ldParent struct{ ID string }

type ldChild struct{ ID, ParentID string }

// staticLoader returns a loader serving recs as one page after running before.
func staticLoader[T any](id func(*T) string, recs []*T, before func() error) Loader[T] {
	return Loader[T]{
		ID: id,
		Page: func(ctx context.Context, _ *gorm.DB, after string, _ int) ([]*T, string, error) {
			if after != "" {
				return nil, "", nil
			}
			if before != nil {
				if err := before(); err != nil {
					return nil, "", err
				}
			}
			return recs, "", nil
		},
		ByID: func(context.Context, *gorm.DB, []string) ([]*T, error) { return nil, nil },
	}
}

// TestLoadAllReadsKindsConcurrently: two unrelated kinds' reads overlap. Each read waits for the
// other to start, so a kind-by-kind load would never get past the first.
func TestLoadAllReadsKindsConcurrently(t *testing.T) {
	s := newStore()
	t.Cleanup(s.Close)
	aStarted, bStarted := make(chan struct{}), make(chan struct{})
	wait := func(mine, other chan struct{}) func() error {
		return func() error {
			close(mine)
			select {
			case <-other:
				return nil
			case <-time.After(5 * time.Second):
				return errors.New("the other kind's read never started")
			}
		}
	}
	id := func(p *ldParent) string { return p.ID }
	if _, err := s.Register(Kind("a"), nil, staticLoader(id, []*ldParent{{ID: "a1"}}, wait(aStarted, bStarted))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Register(Kind("b"), nil, staticLoader(id, []*ldParent{{ID: "b1"}}, wait(bStarted, aStarted))); err != nil {
		t.Fatal(err)
	}
	if err := s.LoadAll(context.Background(), openDB(t)); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
}

// TestLoadAllSharesIDsInDependencyOrder: a child kind whose read finishes before its parent's still
// shares the parent's copy of the ID, because records are published, and IDs shared, in dependency
// order.
func TestLoadAllSharesIDsInDependencyOrder(t *testing.T) {
	s := newStore()
	t.Cleanup(s.Close)
	parentID := strings.Clone("p1")
	parents, err := s.Register(Kind("parent"), nil, staticLoader(func(p *ldParent) string { return p.ID }, []*ldParent{{ID: parentID}}, func() error {
		time.Sleep(50 * time.Millisecond) // the child's read finishes first
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	children, err := s.Register(Kind("child"), []Kind{Kind("parent")},
		staticLoader(func(c *ldChild) string { return c.ID }, []*ldChild{{ID: "c1", ParentID: strings.Clone("p1")}}, nil),
		ShareIDs(func(c *ldChild, shared func(Kind, string) string) { c.ParentID = shared(Kind("parent"), c.ParentID) }),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LoadAll(context.Background(), openDB(t)); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	p, _ := parents.Get("p1")
	c, ok := children.Get("c1")
	if !ok {
		t.Fatal("child not loaded")
	}
	if unsafe.StringData(c.ParentID) != unsafe.StringData(p.ID) {
		t.Fatal("the child does not hold its parent's copy of the ID")
	}
}

// TestLoadAllFailedReadPublishesNothing: a kind whose read fails fails LoadAll, and no kind is
// published, not even one whose read succeeded. The good kind comes first in dependency order and
// its read finishes before the broken one fails, so a load that published each kind as soon as it
// was read would publish it.
func TestLoadAllFailedReadPublishesNothing(t *testing.T) {
	s := newStore()
	t.Cleanup(s.Close)
	id := func(p *ldParent) string { return p.ID }
	goodRead := make(chan struct{})
	good, err := s.Register(Kind("good"), nil, staticLoader(id, []*ldParent{{ID: "g1"}}, func() error {
		close(goodRead)
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	broken := errors.New("broken read")
	failAfterGood := func() error {
		select {
		case <-goodRead:
			return broken
		case <-time.After(5 * time.Second):
			return errors.New("the good kind's read never ran")
		}
	}
	if _, err := s.Register(Kind("broken"), []Kind{Kind("good")}, staticLoader(id, nil, failAfterGood)); err != nil {
		t.Fatal(err)
	}
	if err := s.LoadAll(context.Background(), openDB(t)); !errors.Is(err, broken) {
		t.Fatalf("LoadAll = %v, want the read's error", err)
	}
	if _, ok := good.Get("g1"); ok {
		t.Fatal("a kind was published although LoadAll failed")
	}
}

// TestLoadAllKeepsInternedValuesUntilTheBootPublishes: a boot that reads for longer than the grace
// period, with the sweep running meanwhile, publishes its records holding their shared copies, and
// the pool keeps them: values are shared as records publish.
func TestLoadAllKeepsInternedValuesUntilTheBootPublishes(t *testing.T) {
	clock := setClock(t, time.Unix(1_000_000, 0))
	s := newStore()
	t.Cleanup(s.Close)
	tags, err := s.RegisterInterned(Kind("tag_set"), func(ts *tagSet) Hash {
		var h Hash
		copy(h[:], ts.Tag)
		return h
	})
	if err != nil {
		t.Fatal(err)
	}
	groups, err := s.Register(Kind("group"), nil, Loader[group]{
		ID: func(g *group) string { return g.ID },
		Page: func(context.Context, *gorm.DB, string, int) ([]*group, string, error) {
			g := &group{ID: "g1", Set: &tagSet{Tag: "a"}}
			// The boot reads for longer than the grace period, and the sweep runs meanwhile.
			*clock = clock.Add(internedGrace + time.Minute)
			s.sweep()
			return []*group{g}, "", nil
		},
		ByID: func(context.Context, *gorm.DB, []string) ([]*group, error) { return nil, nil },
	}, Interns(tags, func(g *group) **tagSet { return &g.Set }))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LoadAll(context.Background(), openDB(t)); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	g, ok := groups.Get("g1")
	if !ok {
		t.Fatal("g1 not loaded")
	}
	if _, ok := sharedCopy(tags, tags.hash(g.Set)); !ok {
		t.Fatal("the sweep dropped a value the boot interned before publishing the record that uses it")
	}
	*clock = clock.Add(internedGrace + time.Minute)
	s.sweep()
	if _, ok := sharedCopy(tags, tags.hash(g.Set)); !ok {
		t.Fatal("a value a published record uses must never be swept")
	}
}
