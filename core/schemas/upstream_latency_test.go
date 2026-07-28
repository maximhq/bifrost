package schemas

import (
	"context"
	"sync"
	"testing"
	"time"
)

func newTestCtx() *BifrostContext {
	return NewBifrostContext(context.Background(), NoDeadline)
}

func TestUpstreamLatencyAccumulates(t *testing.T) {
	ctx := newTestCtx()
	ctx.ResetUpstreamLatency()

	AddUpstreamLatency(ctx, 100*time.Millisecond)
	AddUpstreamLatency(ctx, 50*time.Millisecond)

	got, ok := GetUpstreamLatency(ctx)
	if !ok {
		t.Fatal("accumulator missing after reset")
	}
	if got != 150*time.Millisecond {
		t.Fatalf("upstream = %v, want 150ms", got)
	}
}

// Absent and zero must stay distinguishable: a request that never measured
// upstream time is "unknown", not "100% overhead".
func TestUpstreamLatencyAbsentWithoutReset(t *testing.T) {
	ctx := newTestCtx()

	if _, ok := GetUpstreamLatency(ctx); ok {
		t.Fatal("accumulator reported present before reset")
	}
	// Must not panic — provider code calls this unconditionally.
	AddUpstreamLatency(ctx, time.Second)

	if _, ok := CalculateOverhead(ctx, time.Second); ok {
		t.Fatal("overhead reported without an accumulator")
	}
}

func TestUpstreamLatencyZeroIsPresent(t *testing.T) {
	ctx := newTestCtx()
	ctx.ResetUpstreamLatency()

	got, ok := GetUpstreamLatency(ctx)
	if !ok || got != 0 {
		t.Fatalf("got (%v, %v), want (0, true)", got, ok)
	}

	// A cache hit does zero upstream work, so all of the elapsed time is Bifrost's.
	overhead, ok := CalculateOverhead(ctx, 40*time.Millisecond)
	if !ok || overhead != 40*time.Millisecond {
		t.Fatalf("overhead = (%v, %v), want (40ms, true)", overhead, ok)
	}
}

// The reset is what keeps the process-global bifrost.ctx (used for every
// nil-context SDK call) from accumulating across unrelated requests.
func TestUpstreamLatencyResetClearsPreviousRequest(t *testing.T) {
	ctx := newTestCtx()

	ctx.ResetUpstreamLatency()
	AddUpstreamLatency(ctx, 900*time.Millisecond)

	ctx.ResetUpstreamLatency()
	AddUpstreamLatency(ctx, 10*time.Millisecond)

	got, _ := GetUpstreamLatency(ctx)
	if got != 10*time.Millisecond {
		t.Fatalf("upstream = %v, want 10ms — previous request leaked", got)
	}
}

func TestCalculateOverheadClampsNegative(t *testing.T) {
	ctx := newTestCtx()
	ctx.ResetUpstreamLatency()
	AddUpstreamLatency(ctx, 200*time.Millisecond)

	// Upstream exceeding total is possible: the two are measured by different
	// clocks started at different instants.
	overhead, ok := CalculateOverhead(ctx, 150*time.Millisecond)
	if !ok {
		t.Fatal("overhead not reported")
	}
	if overhead != 0 {
		t.Fatalf("overhead = %v, want 0 (clamped)", overhead)
	}
}

func TestCalculateOverheadSubtracts(t *testing.T) {
	ctx := newTestCtx()
	ctx.ResetUpstreamLatency()
	AddUpstreamLatency(ctx, 700*time.Millisecond)

	overhead, ok := CalculateOverhead(ctx, time.Second)
	if !ok || overhead != 300*time.Millisecond {
		t.Fatalf("overhead = (%v, %v), want (300ms, true)", overhead, ok)
	}
}

// A scoped plugin context delegates values to its root, so adds made from a
// plugin must land on the same accumulator the request reads at the end.
func TestUpstreamLatencyThroughScopedContext(t *testing.T) {
	root := newTestCtx()
	root.ResetUpstreamLatency()

	pluginName := "test-plugin"
	scoped := root.WithPluginScope(&pluginName)
	AddUpstreamLatency(scoped, 25*time.Millisecond)

	got, ok := GetUpstreamLatency(root)
	if !ok {
		t.Fatal("accumulator missing on root")
	}
	if got != 25*time.Millisecond {
		t.Fatalf("root upstream = %v, want 25ms — scoped add did not reach root", got)
	}
}

// Streaming keeps adding from the provider goroutine after the handler returns,
// concurrently with the transport reading. Run under -race.
func TestUpstreamLatencyConcurrentAdds(t *testing.T) {
	ctx := newTestCtx()
	ctx.ResetUpstreamLatency()

	const writers, addsEach = 8, 500

	var wg sync.WaitGroup
	wg.Add(writers)
	for range writers {
		go func() {
			defer wg.Done()
			for range addsEach {
				AddUpstreamLatency(ctx, time.Microsecond)
			}
		}()
	}

	// Concurrent reader, mirroring a connector sampling mid-stream.
	stop := make(chan struct{})
	var readerWG sync.WaitGroup
	readerWG.Add(1)
	go func() {
		defer readerWG.Done()
		for {
			select {
			case <-stop:
				return
			default:
				GetUpstreamLatency(ctx)
			}
		}
	}()

	wg.Wait()
	close(stop)
	readerWG.Wait()

	got, _ := GetUpstreamLatency(ctx)
	want := time.Duration(writers*addsEach) * time.Microsecond
	// Exact equality is the point: a lost update here is the bug this design exists
	// to avoid (the context has no atomic read-modify-write).
	if got != want {
		t.Fatalf("upstream = %v, want %v — lost update", got, want)
	}
}

func TestAddUpstreamLatencyIgnoresNonPositive(t *testing.T) {
	ctx := newTestCtx()
	ctx.ResetUpstreamLatency()

	AddUpstreamLatency(ctx, 0)
	AddUpstreamLatency(ctx, -5*time.Second)

	if got, _ := GetUpstreamLatency(ctx); got != 0 {
		t.Fatalf("upstream = %v, want 0", got)
	}
}

// The body copy is what survives a proxy hop, so it has to carry the same total
// the header does — and stay absent, not zero, when nothing was measured.
func TestPopulateUpstreamLatencyOnResponse(t *testing.T) {
	ctx := newTestCtx()
	ctx.ResetUpstreamLatency()
	AddUpstreamLatency(ctx, 120*time.Millisecond)
	AddUpstreamLatency(ctx, 80*time.Millisecond)

	resp := &BifrostResponse{ChatResponse: &BifrostChatResponse{}}
	resp.PopulateUpstreamLatency(ctx)

	got := resp.GetExtraFields().UpstreamLatency
	if got == nil {
		t.Fatal("UpstreamLatency not populated")
	}
	if *got != 200 {
		t.Fatalf("UpstreamLatency = %dms, want 200ms", *got)
	}
}

func TestPopulateUpstreamLatencyAbsentWithoutAccumulator(t *testing.T) {
	ctx := newTestCtx()

	resp := &BifrostResponse{ChatResponse: &BifrostChatResponse{}}
	resp.PopulateUpstreamLatency(ctx)

	if got := resp.GetExtraFields().UpstreamLatency; got != nil {
		t.Fatalf("UpstreamLatency = %dms, want nil — unmeasured must not read as zero", *got)
	}

	// Must not panic on a nil response; handleRequest calls this on its error paths.
	var nilResp *BifrostResponse
	nilResp.PopulateUpstreamLatency(ctx)
}

func TestUpstreamLatencyNilSafe(t *testing.T) {
	var ctx *BifrostContext

	// None of these may panic — they run on every request. The typed-nil cases
	// matter most: a nil *BifrostContext boxed into a context.Context interface is
	// not == nil, so a naive guard would let it through to a panicking Value call.
	ctx.ResetUpstreamLatency()
	ctx.StampUpstreamLatency()
	AddUpstreamLatency(ctx, time.Second)

	if _, ok := GetUpstreamLatency(ctx); ok {
		t.Fatal("typed-nil context reported an accumulator")
	}

	var untyped context.Context
	AddUpstreamLatency(untyped, time.Second)
	if _, ok := GetUpstreamLatency(untyped); ok {
		t.Fatal("nil context reported an accumulator")
	}
}
