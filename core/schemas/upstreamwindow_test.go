package schemas

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestUpstreamWindowUnknownUntilObserved(t *testing.T) {
	ctx := NewBifrostContext(context.Background(), NoDeadline)
	if GetUpstreamWindow(ctx) != nil {
		t.Fatal("window present before reset")
	}
	ctx.ResetUpstreamLatency()
	window := GetUpstreamWindow(ctx)
	if window == nil {
		t.Fatal("reset did not install a window")
	}
	if _, _, ok := window.Bounds(); ok {
		t.Fatal("bounds reported before any provider wait")
	}
	var nilWindow *UpstreamWindow
	if _, _, ok := nilWindow.Bounds(); ok {
		t.Fatal("nil window reported bounds")
	}
}

func TestUpstreamWindowKeepsFirstStartAndExtendsLastEnd(t *testing.T) {
	ctx := NewBifrostContext(context.Background(), NoDeadline)
	ctx.ResetUpstreamLatency()

	firstStart := time.Now()
	time.Sleep(5 * time.Millisecond)
	ObserveUpstreamWait(ctx, firstStart)
	first, firstEnd, ok := GetUpstreamWindow(ctx).Bounds()
	if !ok {
		t.Fatal("no bounds after a wait")
	}
	if got := first.Sub(firstStart); got < 0 || got > time.Microsecond {
		t.Fatalf("first start drifted by %v", got)
	}

	time.Sleep(5 * time.Millisecond)
	secondStart := time.Now()
	time.Sleep(5 * time.Millisecond)
	ObserveUpstreamWait(ctx, secondStart)
	first2, lastEnd, ok := GetUpstreamWindow(ctx).Bounds()
	if !ok || !first2.Equal(first) {
		t.Fatalf("first start changed: %v -> %v", first, first2)
	}
	if !lastEnd.After(firstEnd) {
		t.Fatalf("last end not extended: %v -> %v", firstEnd, lastEnd)
	}
	if total, _ := GetUpstreamLatency(ctx); total < 10*time.Millisecond {
		t.Fatalf("total %v does not include both waits", total)
	}
	if waited := GetUpstreamWindow(ctx).Waited(); waited < 10*time.Millisecond {
		t.Fatalf("window waited %v does not include both waits", waited)
	}
}

func TestUpstreamWindowExcludesNonProviderWaits(t *testing.T) {
	ctx := NewBifrostContext(context.Background(), NoDeadline)
	ctx.ResetUpstreamLatency()
	// A non-provider wait (for example an MCP tool call) adds to the upstream
	// total only.
	AddUpstreamLatency(ctx, 50*time.Millisecond)
	if waited := GetUpstreamWindow(ctx).Waited(); waited != 0 {
		t.Fatalf("window counted a non-provider wait: %v", waited)
	}
	var nilWindow *UpstreamWindow
	if nilWindow.Waited() != 0 {
		t.Fatal("nil window reported a wait")
	}
}

func TestUpstreamWindowConcurrentObservers(t *testing.T) {
	ctx := NewBifrostContext(context.Background(), NoDeadline)
	ctx.ResetUpstreamLatency()
	earliest := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ObserveUpstreamWait(ctx, time.Now())
		}()
	}
	wg.Wait()
	first, last, ok := GetUpstreamWindow(ctx).Bounds()
	if !ok || first.Before(earliest) || last.Before(first) {
		t.Fatalf("bounds %v..%v invalid (earliest %v)", first, last, earliest)
	}
}

func TestObserveUpstreamWaitWithoutAccumulator(t *testing.T) {
	// Telemetry only: a context without an accumulator must not panic.
	ObserveUpstreamWait(context.Background(), time.Now())
	var nilCtx *BifrostContext
	ObserveUpstreamWait(nilCtx, time.Now())
}
