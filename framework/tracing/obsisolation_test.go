package tracing

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// blockingObsPlugin is an observability plugin whose Inject blocks until released.
// It stands in for a connector pointed at an unreachable collector.
type blockingObsPlugin struct {
	name string
	// release gates Inject; a nil channel means Inject returns immediately.
	release chan struct{}
	// delay, when non-zero, is slept in Inject instead of waiting on release.
	delay time.Duration

	inFlight    atomic.Int64
	maxInFlight atomic.Int64
	started     atomic.Int64
	firstEntry  atomic.Int64 // UnixNano of the first Inject entry
}

func (p *blockingObsPlugin) GetName() string { return p.name }
func (p *blockingObsPlugin) Cleanup() error  { return nil }
func (p *blockingObsPlugin) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}
func (p *blockingObsPlugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	return req, nil, nil
}
func (p *blockingObsPlugin) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, bifrostErr, nil
}

func (p *blockingObsPlugin) Inject(_ context.Context, _ *schemas.Trace) error {
	p.started.Add(1)
	p.firstEntry.CompareAndSwap(0, time.Now().UnixNano())

	cur := p.inFlight.Add(1)
	for {
		observed := p.maxInFlight.Load()
		if cur <= observed || p.maxInFlight.CompareAndSwap(observed, cur) {
			break
		}
	}
	defer p.inFlight.Add(-1)

	if p.delay > 0 {
		time.Sleep(p.delay)
		return nil
	}
	if p.release != nil {
		<-p.release
	}
	return nil
}

// newTestTracer builds a tracer with a short flush interval so tests don't wait the full 10s tick to see a low-volume trace delivered.
func newTestTracer(store *TraceStore, flushInterval time.Duration) *Tracer {
	tracer := NewTracer(store, nil, nil)
	tracer.flushInterval = flushInterval
	return tracer
}

// TestSetObservabilityFlushIntervalSeconds covers the config-supplied interval: valid values
// pass through and non-positive falls back to the default.
func TestSetObservabilityFlushIntervalSeconds(t *testing.T) {
	cases := []struct {
		in   int
		want time.Duration
	}{
		{0, DefaultObservabilityFlushInterval},
		{-5, DefaultObservabilityFlushInterval},
		{30, 30 * time.Second},
		{600, 600 * time.Second},
	}
	for _, c := range cases {
		store := NewTraceStore(time.Minute, nil)
		tr := NewTracer(store, nil, nil)
		tr.SetObservabilityFlushIntervalSeconds(c.in)
		if tr.flushInterval != c.want {
			t.Fatalf("SetObservabilityFlushIntervalSeconds(%d) = %v, want %v", c.in, tr.flushInterval, c.want)
		}
		store.Stop()
	}
}

// TestCompleteAndFlushTrace_SlowPluginDoesNotDelayOthers is the core isolation guarantee:
// a connector doing blocking network I/O in Inject must not hold up delivery to another
// connector — including the logging plugin, whose Inject enqueues the row for the DB.
func TestCompleteAndFlushTrace_SlowPluginDoesNotDelayOthers(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	tracer := newTestTracer(store, 20*time.Millisecond)
	defer tracer.Stop()

	const slowInject = 750 * time.Millisecond
	slow := &blockingObsPlugin{name: "slow-connector", delay: slowInject}
	fast := &blockingObsPlugin{name: "fast-connector"}

	// Slow one registered first: the ordering that used to cause the stall.
	tracer.SetObservabilityPlugins([]schemas.ObservabilityPlugin{slow, fast})

	traceID := tracer.CreateTrace("")
	start := time.Now()
	tracer.CompleteAndFlushTrace(traceID)

	deadline := time.Now().Add(5 * time.Second)
	for fast.started.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if fast.started.Load() == 0 {
		t.Fatal("fast plugin was never injected")
	}

	elapsed := time.Unix(0, fast.firstEntry.Load()).Sub(start)
	if elapsed >= slowInject {
		t.Fatalf("fast plugin was blocked behind the slow one: entered Inject after %v (slow plugin takes %v)", elapsed, slowInject)
	}
}

// TestCompleteAndFlushTrace_CountTriggerFlushesBeforeTick verifies the count trigger: once a
// connector's buffer reaches batchSize it flushes immediately, without waiting for the
// interval tick, and nothing is dropped.
func TestCompleteAndFlushTrace_CountTriggerFlushesBeforeTick(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	// Long interval so the timer cannot be what flushes — only the size trigger can.
	tracer := newTestTracer(store, time.Hour)
	defer tracer.Stop()
	tracer.batchSize = 64

	counter := &blockingObsPlugin{name: "counter"}
	tracer.SetObservabilityPlugins([]schemas.ObservabilityPlugin{counter})

	// Exactly one full batch: the 64th append should trigger a flush on its own.
	for range 64 {
		tracer.CompleteAndFlushTrace(tracer.CreateTrace(""))
	}
	tracer.waitForFlushes(5 * time.Second)

	deadline := time.Now().Add(5 * time.Second)
	for counter.started.Load() < 64 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := counter.started.Load(); got != 64 {
		t.Fatalf("expected all 64 buffered traces delivered by the size trigger, got %d", got)
	}
	if dropped := tracer.ObservabilityDropCounts()["counter"]; dropped != 0 {
		t.Fatalf("expected no drops, got %d", dropped)
	}
}

// TestCompleteAndFlushTrace_HungPluginBuffersThenDeliversAll is the end-to-end promise:
// push 10000 traces at a plugin whose Inject hangs. They fill the buffer and pile up behind
// blocked flush goroutines — delivered nowhere, but dropped nowhere either. Once the plugin
// unblocks, every single one of the 10000 must drain through.
func TestCompleteAndFlushTrace_HungPluginBuffersThenDeliversAll(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	// Short interval so the trailing partial batch (10000 is not a multiple of 1024) is
	// flushed by the timer too, exercising both triggers under the hang.
	tracer := newTestTracer(store, 50*time.Millisecond)
	tracer.batchSize = 1024
	defer tracer.Stop()

	hung := &blockingObsPlugin{name: "hung", release: make(chan struct{})}
	// Release on every exit path so a failed assertion doesn't leave flush goroutines
	// blocked in Inject (pinning their batches) for the rest of the package run.
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(hung.release) }) }
	defer release()
	tracer.SetObservabilityPlugins([]schemas.ObservabilityPlugin{hung})

	const total = 10000
	for range total {
		tracer.CompleteAndFlushTrace(tracer.CreateTrace(""))
	}
	// All producer appends complete quickly even though every flush goroutine is stuck.
	if !tracer.waitForFlushes(5 * time.Second) {
		t.Fatal("producers did not finish appending")
	}

	// Wait — with a deadline, not a fixed sleep — until every count-triggered batch is
	// dispatched and blocked in Inject. total/1024 = 9 full batches are guaranteed regardless
	// of scheduler timing (the trailing 784 flush via the timer or the final drain). A delayed
	// scheduler only makes this poll take longer; it cannot let the test pass before the work
	// has happened. The bound is `>= minBatches` because a timer tick may also dispatch the
	// partial, which would only raise the count — never lower it.
	const minBatches = total / 1024 // 9
	deadline := time.Now().Add(5 * time.Second)
	for hung.inFlight.Load() < minBatches && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	// Each blocked flush goroutine is stuck on the first trace of its batch, so inFlight ==
	// started == number of dispatched batches: some traces are in Inject, the vast majority
	// wait behind them — and none are dropped.
	if got := hung.inFlight.Load(); got < minBatches {
		t.Fatalf("expected at least %d flush goroutines blocked in Inject, got %d", minBatches, got)
	}
	if s := hung.started.Load(); s >= total {
		t.Fatalf("while hung, expected some-but-not-all traces in Inject, got %d/%d", s, total)
	}
	if d := tracer.ObservabilityDropCounts()["hung"]; d != 0 {
		t.Fatalf("expected no drops while buffering, got %d", d)
	}

	// Release: every buffered / in-flight trace must now drain through.
	release()

	deadline = time.Now().Add(15 * time.Second)
	for hung.started.Load() < total && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if got := hung.started.Load(); got != total {
		t.Fatalf("expected all %d traces delivered after release, got %d", total, got)
	}
	if d := tracer.ObservabilityDropCounts()["hung"]; d != 0 {
		t.Fatalf("expected zero drops overall, got %d", d)
	}
}

// TestCompleteAndFlushTrace_TimerFlushesPartialBatch verifies the interval trigger: a
// sub-batch of traces that never reaches batchSize is still delivered on the next tick.
func TestCompleteAndFlushTrace_TimerFlushesPartialBatch(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	tracer := newTestTracer(store, 20*time.Millisecond)
	defer tracer.Stop()
	tracer.batchSize = 1024 // far above what we push, so only the timer can flush

	counter := &blockingObsPlugin{name: "counter"}
	tracer.SetObservabilityPlugins([]schemas.ObservabilityPlugin{counter})

	const n = 5
	for range n {
		tracer.CompleteAndFlushTrace(tracer.CreateTrace(""))
	}

	deadline := time.Now().Add(5 * time.Second)
	for counter.started.Load() < n && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := counter.started.Load(); got != n {
		t.Fatalf("expected the partial batch of %d flushed by the timer, got %d", n, got)
	}
}

// TestDrainObsPlugins_TimesOutOnHungPlugin ensures a connector blocked on an unreachable
// collector cannot hold up shutdown or a config reload. The blocking work lives in a flush
// goroutine, so the bounded wait that protects shutdown is drainObsPlugins.
func TestDrainObsPlugins_TimesOutOnHungPlugin(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	tracer := newTestTracer(store, 20*time.Millisecond)
	defer tracer.Stop()

	hung := &blockingObsPlugin{name: "hung-connector", release: make(chan struct{})}
	// Release on every exit path so a failed assertion doesn't leave a flush goroutine
	// blocked in Inject for the rest of the package run.
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(hung.release) }) }
	defer release()
	tracer.SetObservabilityPlugins([]schemas.ObservabilityPlugin{hung})
	tracer.CompleteAndFlushTrace(tracer.CreateTrace(""))

	deadline := time.Now().Add(5 * time.Second)
	for hung.started.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if hung.started.Load() == 0 {
		t.Fatal("hung plugin was never injected")
	}

	// The producer append completes promptly even while a flush goroutine is stuck in Inject.
	if completed := tracer.waitForFlushes(2 * time.Second); !completed {
		t.Fatal("waitForFlushes should complete once the snapshot is appended")
	}

	// Draining, however, must honour its timeout rather than block on the stuck Inject.
	start := time.Now()
	tracer.drainObsPlugins(200 * time.Millisecond)
	if elapsed := time.Since(start); elapsed < 200*time.Millisecond {
		t.Fatalf("drainObsPlugins returned before a blocked flush could have finished: %v", elapsed)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("drainObsPlugins did not honour its timeout: took %v", elapsed)
	}

	release()
}

// TestSetObservabilityPlugins_DedupesByName preserves the behaviour the old flush loop
// implemented with a per-flush `seen` map.
func TestSetObservabilityPlugins_DedupesByName(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	tracer := newTestTracer(store, 20*time.Millisecond)
	defer tracer.Stop()

	dup := &blockingObsPlugin{name: "dup-connector"}
	tracer.SetObservabilityPlugins([]schemas.ObservabilityPlugin{dup, dup, dup})

	// Dedup is structural: three identically-named plugins collapse to a single slot. Assert
	// that directly — it's deterministic — instead of waiting to see whether a duplicate slot
	// also fires.
	loaded := tracer.obsPlugins.Load()
	n := 0
	if loaded != nil {
		n = len(*loaded)
	}
	if n != 1 {
		t.Fatalf("expected exactly one slot after dedup, got %d", n)
	}

	// The single slot still delivers: one trace in, injected exactly once. Deadline-bounded
	// wait, no fixed sleep.
	tracer.CompleteAndFlushTrace(tracer.CreateTrace(""))
	deadline := time.Now().Add(5 * time.Second)
	for dup.started.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := dup.started.Load(); got != 1 {
		t.Fatalf("expected the deduped plugin injected once, got %d", got)
	}
}

// TestObsSlot_SizeTriggerFlushesBeforeCount verifies the memory guard: the byte cap gates
// delivery — traces buffer silently until their combined size crosses maxBatchBytes, at which
// point the whole batch flushes, long before the count cap (or the timer) would. The negative
// half (three traces stay buffered) is what distinguishes a real size cap from an
// implementation that just flushes every trace. Content lives in both trace- and span-level
// attributes, so trace-level byte accounting is exercised too.
func TestObsSlot_SizeTriggerFlushesBeforeCount(t *testing.T) {
	p := &blockingObsPlugin{name: "big"}
	slot := &obsPluginSlot{
		plugin:        p,
		name:          p.name,
		batchSize:     1_000_000, // count cap effectively unreachable
		maxBatchBytes: 64 * 1024, // 64 KiB size cap
		flushInterval: time.Hour, // timer never fires during the test
		stop:          make(chan struct{}),
	}
	slot.start(nil)
	defer slot.signalStop()

	// ~18 KiB per trace, split across trace- and span-level attributes: three stay under the
	// 64 KiB cap (~54 KiB), the fourth crosses it (~72 KiB).
	bigTrace := func() *schemas.Trace {
		return &schemas.Trace{
			Attributes: map[string]any{"trace.big": strings.Repeat("x", 9*1024)},
			Spans: []*schemas.Span{
				{Name: "s", Attributes: map[string]any{"span.big": strings.Repeat("x", 9*1024)}},
			},
		}
	}

	// Three traces must NOT flush — the cap gates, it doesn't fire per trace. Give any wrongly
	// triggered flush time to reach Inject before asserting nothing was delivered.
	for range 3 {
		slot.enqueue(bigTrace(), nil)
	}
	time.Sleep(100 * time.Millisecond)
	if got := p.started.Load(); got != 0 {
		t.Fatalf("byte cap flushed early: %d trace(s) delivered before the cap was reached", got)
	}

	// The fourth trace crosses the cap and flushes the whole buffered batch.
	slot.enqueue(bigTrace(), nil)
	deadline := time.Now().Add(2 * time.Second)
	for p.started.Load() < 4 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := p.started.Load(); got != 4 {
		t.Fatalf("expected the byte cap to flush all 4 buffered traces, got %d", got)
	}
}

// TestObsSlot_PostCloseEnqueueStillDelivers is the shutdown-window guarantee: a trace
// appended after the slot has been told to stop — a producer still holding a retired slot
// during a config reload — is flushed immediately rather than lost in a batch the timer will
// never drain again.
func TestObsSlot_PostCloseEnqueueStillDelivers(t *testing.T) {
	p := &blockingObsPlugin{name: "late"}
	slot := &obsPluginSlot{
		plugin:        p,
		name:          p.name,
		batchSize:     1000,      // never reached; only the timer or close flushes
		maxBatchBytes: 1 << 30,   // size cap effectively disabled, so the first trace buffers
		flushInterval: time.Hour, // never ticks during the test
		stop:          make(chan struct{}),
	}
	slot.start(nil)

	// One buffered before close (drained by the final flush), one after close (self-flushed).
	slot.enqueue(&schemas.Trace{}, nil)
	slot.signalStop()
	slot.enqueue(&schemas.Trace{}, nil)

	slot.wg.Wait()
	if got := p.started.Load(); got != 2 {
		t.Fatalf("expected both traces delivered (none lost across close), got %d", got)
	}
}
