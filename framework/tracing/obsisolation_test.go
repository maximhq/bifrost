package tracing

import (
	"context"
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

// TestCompleteAndFlushTrace_SlowPluginDoesNotDelayOthers is the core regression: the
// observability plugins used to be invoked sequentially on a single flush goroutine, so
// a connector doing blocking network I/O sat in front of every plugin after it —
// including the logging plugin, whose Inject is what enqueues the row for Postgres.
func TestCompleteAndFlushTrace_SlowPluginDoesNotDelayOthers(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	tracer := NewTracer(store, nil, nil)
	defer tracer.Stop()

	const slowInject = 750 * time.Millisecond
	slow := &blockingObsPlugin{name: "slow-connector", delay: slowInject}
	fast := &blockingObsPlugin{name: "fast-connector"}

	// Slow one registered first: the ordering that used to cause the stall.
	tracer.SetObservabilityPlugins([]schemas.ObservabilityPlugin{slow, fast}, nil)

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

// TestCompleteAndFlushTrace_BoundsInjectsPerPlugin verifies the concurrency cap. Without
// it, one goroutine and one full trace snapshot accumulate per request for as long as the
// collector hangs, and that heap/scheduler pressure is what starves the log batch writer.
func TestCompleteAndFlushTrace_BoundsInjectsPerPlugin(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	tracer := NewTracer(store, nil, nil)

	stuck := &blockingObsPlugin{name: "stuck-connector", release: make(chan struct{})}
	tracer.SetObservabilityPlugins([]schemas.ObservabilityPlugin{stuck}, nil)

	const overshoot = 250
	total := defaultSemaphoreSize + overshoot
	for range total {
		tracer.CompleteAndFlushTrace(tracer.CreateTrace(""))
	}

	// Wait for the semaphore to saturate and the excess to be skipped.
	deadline := time.Now().Add(10 * time.Second)
	for tracer.ObservabilityDropCounts()["stuck-connector"] == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	if got := stuck.maxInFlight.Load(); got > defaultSemaphoreSize {
		t.Fatalf("concurrent injects exceeded the cap: got %d, cap %d", got, defaultSemaphoreSize)
	}
	if dropped := tracer.ObservabilityDropCounts()["stuck-connector"]; dropped == 0 {
		t.Fatal("expected traces to be skipped once the plugin saturated, got 0")
	}

	close(stuck.release)
	tracer.Stop()
}

// TestWaitForFlushes_TimesOutOnHungPlugin ensures a connector blocked on an unreachable
// collector cannot hold up shutdown or a config reload.
func TestWaitForFlushes_TimesOutOnHungPlugin(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	tracer := NewTracer(store, nil, nil)

	hung := &blockingObsPlugin{name: "hung-connector", release: make(chan struct{})}
	tracer.SetObservabilityPlugins([]schemas.ObservabilityPlugin{hung}, nil)
	tracer.CompleteAndFlushTrace(tracer.CreateTrace(""))

	deadline := time.Now().Add(5 * time.Second)
	for hung.started.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if hung.started.Load() == 0 {
		t.Fatal("hung plugin was never injected")
	}

	start := time.Now()
	if completed := tracer.waitForFlushes(200 * time.Millisecond); completed {
		t.Fatal("waitForFlushes reported completion while a plugin was still blocked")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("waitForFlushes did not honour its timeout: took %v", elapsed)
	}

	close(hung.release)
	tracer.Stop()
}

// TestSetObservabilityPlugins_DedupesByName preserves the behaviour the old flush loop
// implemented with a per-flush `seen` map.
func TestSetObservabilityPlugins_DedupesByName(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	tracer := NewTracer(store, nil, nil)
	defer tracer.Stop()

	dup := &blockingObsPlugin{name: "dup-connector"}
	tracer.SetObservabilityPlugins([]schemas.ObservabilityPlugin{dup, dup, dup}, nil)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		tracer.CompleteAndFlushTrace(tracer.CreateTrace(""))
	}()
	wg.Wait()

	deadline := time.Now().Add(5 * time.Second)
	for dup.started.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	tracer.waitForFlushes(5 * time.Second)

	if got := dup.started.Load(); got != 1 {
		t.Fatalf("expected a duplicate-named plugin to be injected once, got %d", got)
	}
}

// ctxAwareObsPlugin's Inject respects ctx cancellation: it blocks until either released or
// the context is done, whichever comes first. Stands in for a well-behaved connector whose
// HTTP/gRPC client propagates ctx.
type ctxAwareObsPlugin struct {
	name    string
	release chan struct{}
	started atomic.Int64
}

func (p *ctxAwareObsPlugin) GetName() string { return p.name }
func (p *ctxAwareObsPlugin) Cleanup() error  { return nil }
func (p *ctxAwareObsPlugin) Inject(ctx context.Context, _ *schemas.Trace) error {
	p.started.Add(1)
	select {
	case <-p.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// TestSetObservabilityPlugins_HonoursDeclaredLimits verifies a plugin whose generic
// PluginConfig declares semaphore_size/inject_timeout gets a semaphore sized from that
// config instead of the tracer's default.
func TestSetObservabilityPlugins_HonoursDeclaredLimits(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	tracer := NewTracer(store, nil, nil)
	defer tracer.Stop()

	const customSemSize = 4
	plugin := &blockingObsPlugin{name: "limited-connector", release: make(chan struct{})}
	limits := map[string]schemas.ObservabilityLimits{
		"limited-connector": {SemaphoreSize: customSemSize, InjectTimeout: time.Minute},
	}
	tracer.SetObservabilityPlugins([]schemas.ObservabilityPlugin{plugin}, limits)

	loaded := tracer.obsPlugins.Load()
	if loaded == nil || len(*loaded) != 1 {
		t.Fatalf("expected exactly one slot, got %v", loaded)
	}
	slot := (*loaded)[0]
	if cap(slot.sem) != customSemSize {
		t.Fatalf("expected semaphore sized %d from declared limits, got %d", customSemSize, cap(slot.sem))
	}
	if slot.injectTimeout != time.Minute {
		t.Fatalf("expected inject timeout from declared limits, got %v", slot.injectTimeout)
	}

	close(plugin.release)
}

// TestSetObservabilityPlugins_DefaultsWhenLimitsNotDeclared verifies a plugin with no
// entry in the limits map falls back to the tracer's defaults.
func TestSetObservabilityPlugins_DefaultsWhenLimitsNotDeclared(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	tracer := NewTracer(store, nil, nil)
	defer tracer.Stop()

	plain := &blockingObsPlugin{name: "plain-connector"}
	tracer.SetObservabilityPlugins([]schemas.ObservabilityPlugin{plain}, nil)

	loaded := tracer.obsPlugins.Load()
	if loaded == nil || len(*loaded) != 1 {
		t.Fatalf("expected exactly one slot, got %v", loaded)
	}
	slot := (*loaded)[0]
	if cap(slot.sem) != defaultSemaphoreSize {
		t.Fatalf("expected default semaphore size %d, got %d", defaultSemaphoreSize, cap(slot.sem))
	}
	if slot.injectTimeout != defaultInjectTimeout {
		t.Fatalf("expected default inject timeout %v, got %v", defaultInjectTimeout, slot.injectTimeout)
	}
}

// TestCompleteAndFlushTrace_InjectTimeoutReleasesSlot verifies that a plugin honouring ctx
// cancellation has its Inject call unblocked once the configured inject timeout elapses,
// freeing the semaphore slot instead of holding it indefinitely.
func TestCompleteAndFlushTrace_InjectTimeoutReleasesSlot(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	tracer := NewTracer(store, nil, nil)
	defer tracer.Stop()

	// release is never closed: the plugin only returns because its 50ms inject timeout
	// (declared via the limits map passed to SetObservabilityPlugins) cancels ctx.
	plugin := &ctxAwareObsPlugin{name: "ctx-aware-connector", release: make(chan struct{})}
	// SemaphoreSize: 1 makes the test meaningful — with the tracer's default of 10,000,
	// both flushes below would acquire a slot immediately regardless of whether
	// cancellation actually released one.
	limits := map[string]schemas.ObservabilityLimits{
		"ctx-aware-connector": {SemaphoreSize: 1, InjectTimeout: 50 * time.Millisecond},
	}
	tracer.SetObservabilityPlugins([]schemas.ObservabilityPlugin{plugin}, limits)

	// SemaphoreSize is 1, so the second flush can only start once the first Inject's
	// ctx cancellation frees the slot. Submitting and waiting one at a time (rather
	// than firing both up front) is what actually exercises that release path — with
	// both queued together, the second could otherwise sit dropped by the non-blocking
	// acquire instead of proving the slot came free.
	start := time.Now()
	tracer.CompleteAndFlushTrace(tracer.CreateTrace(""))
	if completed := tracer.waitForFlushes(2 * time.Second); !completed {
		t.Fatal("expected the first flush to complete once its inject timeout elapsed")
	}
	if got := plugin.started.Load(); got != 1 {
		t.Fatalf("expected first flush to start Inject once, got %d", got)
	}

	tracer.CompleteAndFlushTrace(tracer.CreateTrace(""))
	if completed := tracer.waitForFlushes(2 * time.Second); !completed {
		t.Fatal("expected the second flush to complete once its inject timeout elapsed")
	}
	if got := plugin.started.Load(); got != 2 {
		t.Fatalf("expected the second flush's Inject to start once the first slot was released, got %d starts", got)
	}
	if dropped := tracer.ObservabilityDropCounts()["ctx-aware-connector"]; dropped != 0 {
		t.Fatalf("expected no traces dropped, got %d", dropped)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("flushes took %v, expected them to be bounded by the ~50ms inject timeout each", elapsed)
	}
}
