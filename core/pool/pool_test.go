package pool

import (
	"sync"
	"testing"
)

// testObj is a simple struct for testing pools.
type testObj struct {
	ID    int
	Name  string
	Items []string
}

func TestGetPut(t *testing.T) {
	p := New[testObj]("test", func() *testObj {
		return &testObj{}
	})

	obj := p.Get()
	if obj == nil {
		t.Fatal("Get() returned nil")
	}

	obj.ID = 42
	obj.Name = "hello"

	// Reset before put (mimics real Release functions)
	obj.ID = 0
	obj.Name = ""
	p.Put(obj)

	// Get again - should succeed
	obj2 := p.Get()
	if obj2 == nil {
		t.Fatal("second Get() returned nil")
	}
	// Reset and return
	obj2.ID = 0
	obj2.Name = ""
	p.Put(obj2)
}

func TestPutNil(t *testing.T) {
	p := New[testObj]("test-nil", func() *testObj {
		return &testObj{}
	})
	// Should not panic
	p.Put(nil)
}

func TestPrewarm(t *testing.T) {
	createCount := 0
	p := New[testObj]("test-prewarm", func() *testObj {
		createCount++
		return &testObj{}
	})

	p.Prewarm(10)

	// Get objects - they should come from the prewarm, not new factory calls
	// (factory may have been called during prewarm depending on implementation)
	objs := make([]*testObj, 10)
	for i := 0; i < 10; i++ {
		objs[i] = p.Get()
		if objs[i] == nil {
			t.Fatalf("Get() returned nil at index %d", i)
		}
	}

	// Return them all
	for _, obj := range objs {
		p.Put(obj)
	}
}

func TestConcurrentGetPut(t *testing.T) {
	p := New[testObj]("test-concurrent", func() *testObj {
		return &testObj{}
	})

	const goroutines = 100
	const iterations = 1000

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				obj := p.Get()
				obj.ID = i
				obj.Name = "test"
				// Reset and return
				obj.ID = 0
				obj.Name = ""
				obj.Items = nil
				p.Put(obj)
			}
		}()
	}

	wg.Wait()
}

func TestAllStats(t *testing.T) {
	// AllStats should return something (possibly nil in prod, non-nil in debug)
	// Just verify it doesn't panic
	_ = AllStats()
}

func BenchmarkPoolGetPut(b *testing.B) {
	p := New[testObj]("bench", func() *testObj {
		return &testObj{}
	})

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			obj := p.Get()
			obj.ID = 0
			obj.Name = ""
			p.Put(obj)
		}
	})
}

func BenchmarkRawSyncPool(b *testing.B) {
	p := sync.Pool{
		New: func() interface{} {
			return &testObj{}
		},
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			obj := p.Get().(*testObj)
			obj.ID = 0
			obj.Name = ""
			p.Put(obj)
		}
	})
}
