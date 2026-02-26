//go:build pooldebug

package pool

import (
	"strings"
	"testing"
)

func TestDebugDoubleRelease(t *testing.T) {
	p := New[testObj]("double-release-test", func() *testObj {
		return &testObj{}
	})

	obj := p.Get()
	p.Put(obj) // first release - fine

	// Second release should panic
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on double release, got none")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("expected string panic, got %T: %v", r, r)
		}
		if !strings.Contains(msg, "double-release-test") {
			t.Errorf("panic message should contain pool name, got: %s", msg)
		}
		if !strings.Contains(msg, "not tracked as active") {
			t.Errorf("panic message should mention tracking, got: %s", msg)
		}
	}()

	p.Put(obj) // double release - should panic
}

func TestDebugCheckActiveAfterRelease(t *testing.T) {
	p := New[testObj]("check-active-test", func() *testObj {
		return &testObj{}
	})

	obj := p.Get()
	p.CheckActive(obj) // should not panic - object is active

	p.Put(obj) // release

	// CheckActive should now panic
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on CheckActive after release, got none")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("expected string panic, got %T: %v", r, r)
		}
		if !strings.Contains(msg, "check-active-test") {
			t.Errorf("panic message should contain pool name, got: %s", msg)
		}
		if !strings.Contains(msg, "NOT active") {
			t.Errorf("panic message should mention NOT active, got: %s", msg)
		}
	}()

	p.CheckActive(obj)
}

func TestDebugCheckActiveNil(t *testing.T) {
	p := New[testObj]("check-nil-test", func() *testObj {
		return &testObj{}
	})
	// Should not panic on nil
	p.CheckActive(nil)
}

func TestDebugStats(t *testing.T) {
	p := New[testObj]("stats-test", func() *testObj {
		return &testObj{}
	})

	// Initial state
	s := p.Stats()
	if s.Name != "stats-test" {
		t.Errorf("expected name 'stats-test', got '%s'", s.Name)
	}
	if s.Active != 0 {
		t.Errorf("expected 0 active, got %d", s.Active)
	}

	// Acquire 3 objects
	obj1 := p.Get()
	obj2 := p.Get()
	obj3 := p.Get()

	s = p.Stats()
	if s.Acquires != 3 {
		t.Errorf("expected 3 acquires, got %d", s.Acquires)
	}
	if s.Active != 3 {
		t.Errorf("expected 3 active, got %d", s.Active)
	}
	if s.Creates != 3 {
		t.Errorf("expected 3 creates (all pool misses), got %d", s.Creates)
	}

	// Release 2
	p.Put(obj1)
	p.Put(obj2)

	s = p.Stats()
	if s.Releases != 2 {
		t.Errorf("expected 2 releases, got %d", s.Releases)
	}
	if s.Active != 1 {
		t.Errorf("expected 1 active, got %d", s.Active)
	}

	// Release last
	p.Put(obj3)

	s = p.Stats()
	if s.Active != 0 {
		t.Errorf("expected 0 active, got %d", s.Active)
	}

	// Acquire again - should be a pool hit (not a create)
	obj4 := p.Get()
	s = p.Stats()
	if s.Acquires != 4 {
		t.Errorf("expected 4 acquires, got %d", s.Acquires)
	}
	// Creates should still be 3 (obj4 came from pool)
	if s.Creates != 3 {
		t.Errorf("expected 3 creates (pool hit), got %d", s.Creates)
	}
	if s.HitRate == 0 {
		t.Error("expected non-zero hit rate")
	}

	p.Put(obj4)
}

func TestDebugActiveObjects(t *testing.T) {
	p := New[testObj]("active-objects-test", func() *testObj {
		return &testObj{}
	})

	obj1 := p.Get()
	obj2 := p.Get()

	active := p.ActiveObjects()
	if len(active) != 2 {
		t.Errorf("expected 2 active objects, got %d", len(active))
	}

	// Each entry should have a stack trace
	for addr, stack := range active {
		if addr == "" {
			t.Error("empty address")
		}
		if stack == "" {
			t.Error("empty stack trace")
		}
		if !strings.Contains(stack, "pool_debug_test.go") {
			t.Errorf("stack should reference test file, got: %s", stack)
		}
	}

	p.Put(obj1)
	p.Put(obj2)

	active = p.ActiveObjects()
	if len(active) != 0 {
		t.Errorf("expected 0 active objects after release, got %d", len(active))
	}
}

func TestDebugAllStatsRegistry(t *testing.T) {
	// Create a few pools
	_ = New[testObj]("registry-a", func() *testObj { return &testObj{} })
	_ = New[testObj]("registry-b", func() *testObj { return &testObj{} })

	stats := AllStats()
	if stats == nil {
		t.Fatal("AllStats() returned nil in debug mode")
	}

	// Should find our pools (plus any from other tests)
	foundA, foundB := false, false
	for _, s := range stats {
		if s.Name == "registry-a" {
			foundA = true
		}
		if s.Name == "registry-b" {
			foundB = true
		}
	}
	if !foundA || !foundB {
		t.Errorf("expected to find registry-a and registry-b in AllStats, found A=%v B=%v", foundA, foundB)
	}
}

func TestDebugPrewarmDoesNotInflateStats(t *testing.T) {
	p := New[testObj]("prewarm-stats-test", func() *testObj {
		return &testObj{}
	})

	p.Prewarm(10)

	s := p.Stats()
	// Prewarm should NOT inflate acquire/release counts
	if s.Acquires != 0 {
		t.Errorf("expected 0 acquires after prewarm, got %d", s.Acquires)
	}
	if s.Releases != 0 {
		t.Errorf("expected 0 releases after prewarm, got %d", s.Releases)
	}
	if s.Active != 0 {
		t.Errorf("expected 0 active after prewarm, got %d", s.Active)
	}
	// Creates should also be 0 since prewarm bypasses the pool's New func
	if s.Creates != 0 {
		t.Errorf("expected 0 creates after prewarm (bypasses pool.New), got %d", s.Creates)
	}
}
