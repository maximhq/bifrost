package heimdall

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestBudget_CallCap(t *testing.T) {
	b := NewBudget(0, 3)
	for i := 0; i < 3; i++ {
		if !b.Reserve() {
			t.Fatalf("call %d refused under a cap of 3", i+1)
		}
	}
	if b.Reserve() {
		t.Error("a fourth call was allowed")
	}
	if b.Calls() != 3 {
		t.Errorf("calls = %d, want 3", b.Calls())
	}
}

func TestBudget_CallCapWithUnpricedModel(t *testing.T) {
	// A model with no price adds nothing, so only the call cap can stop the run.
	b := NewBudget(1.0, 2)
	for i := 0; i < 2; i++ {
		if !b.Reserve() {
			t.Fatalf("call %d refused", i+1)
		}
		b.Add(0)
	}
	if b.Reserve() {
		t.Error("the call cap did not stop an unpriced run")
	}
	if b.Spent() != 0 {
		t.Errorf("spent = %v, want 0", b.Spent())
	}
}

func TestBudget_DollarCap(t *testing.T) {
	b := NewBudget(1.0, 0)
	if !b.Reserve() {
		t.Fatal("first call refused")
	}
	b.Add(0.6)
	if !b.Reserve() {
		t.Fatal("0.60 is under the cap of 1.00")
	}
	b.Add(0.6) // overshoots by one call, as documented
	if b.Reserve() {
		t.Error("a call was allowed after the cap was passed")
	}
	if got := b.Spent(); got < 1.19 || got > 1.21 {
		t.Errorf("spent = %v, want 1.2", got)
	}
}

func TestBudget_NegativeCostIgnored(t *testing.T) {
	b := NewBudget(1.0, 0)
	b.Add(-5)
	if b.Spent() != 0 {
		t.Errorf("spent = %v after a negative charge", b.Spent())
	}
}

func TestBudget_NoCapsMeansUnlimited(t *testing.T) {
	b := NewBudget(0, 0)
	for i := 0; i < 1000; i++ {
		if !b.Reserve() {
			t.Fatalf("an uncapped budget refused call %d", i+1)
		}
		b.Add(100)
	}
}

func TestBudget_CallCapIsExactUnderConcurrency(t *testing.T) {
	b := NewBudget(0, 25)
	var granted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				if b.Reserve() {
					granted.Add(1)
					b.Add(0.01)
				}
			}
		}()
	}
	wg.Wait()
	if granted.Load() != 25 || b.Calls() != 25 {
		t.Errorf("granted %d calls (budget says %d), want exactly 25", granted.Load(), b.Calls())
	}
}
