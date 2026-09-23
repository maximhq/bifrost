// Package memtest holds assertions for memory-regression tests.
//
// It exists because two production memory bugs shipped in quick succession with
// opposite signatures, and neither was catchable by the other's technique:
//
//   - StripEmptyThinkingBlocks rewrote the whole request body once per stripped
//     content block. That was 23.6% of every byte the gateway allocated and 0%
//     of its live heap: pure churn, invisible to any heap check.
//   - The client-disconnect watcher goroutine outlived its request and pinned
//     the request-scoped context. That was ~0% of allocation and ~80% of the
//     live heap: pure retention, invisible to any allocation check.
//
// So the package offers both: AssertAllocScaling for churn, and
// AssertNoGoroutineLeak / AssertReleased for retention.
//
// This package must not import anything from core/providers. Provider test
// files import it, so a dependency the other way would be an import cycle.
package memtest

import (
	"runtime"
	"testing"
	"time"
	"weak"
)

// Defaults for AssertAllocScaling. Base and Factor pick two input sizes; the
// assertion compares how allocation grew against how the input grew.
//
// MaxGrowth sits between the two outcomes rather than near either. A linear
// implementation lands at about Factor (4), a quadratic one at about Factor
// squared (16). Measured on the real before/after of the strip bug: 3.9x and
// 13.6x. Eight separates them with room on both sides.
const (
	DefaultBase      = 50
	DefaultFactor    = 4
	DefaultMaxGrowth = 8.0
)

// Config tunes AssertAllocScalingWith. A zero value means the defaults above.
type Config struct {
	// Base is the smaller input scale handed to build.
	Base int
	// Factor multiplies Base for the larger input. Must be at least 2.
	Factor int
	// MaxGrowth is the allocation-growth ratio above which the test fails.
	MaxGrowth float64
	// AllowZeroAlloc permits a function that allocates nothing.
	//
	// Off by default, and that default is the point. A payload that fails to
	// reach the code path allocates nothing, and a growth ratio computed from
	// zero would report a pass. Silently passing a test whose subject never ran
	// is worse than having no test, so the zero case fails unless a caller
	// deliberately opts in for a genuinely allocation-free function.
	AllowZeroAlloc bool
}

func (c Config) withDefaults() Config {
	if c.Base <= 0 {
		c.Base = DefaultBase
	}
	if c.Factor < 2 {
		c.Factor = DefaultFactor
	}
	if c.MaxGrowth <= 0 {
		c.MaxGrowth = DefaultMaxGrowth
	}
	return c
}

// AllocBytesPerOp reports the bytes fn allocates per call.
//
// It runs fn under testing.Benchmark rather than diffing runtime.MemStats, so
// the figure is amortised over many iterations and is not perturbed by whatever
// else the test binary's goroutines happen to be doing.
func AllocBytesPerOp(fn func()) int64 {
	result := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			fn()
		}
	})
	return result.AllocedBytesPerOp()
}

// AssertAllocScaling fails when a function's allocation grows with the square of
// its input instead of with its input.
//
// build(n) must produce a payload whose SIZE scales with n, not merely a payload
// with n elements: the assertion is a ratio between the two measurements, so a
// builder that keeps the byte count flat makes the result meaningless. The
// helper checks this and fails loudly rather than reporting a false pass.
//
// This asserts a shape, not a byte count. An absolute threshold ("under 4 MB")
// encodes the machine and the Go version it was written on and silently rots; a
// growth ratio encodes the complexity class, which is the thing that must not
// regress.
//
// The canonical failure it catches: a loop calling sjson Set/Delete once per
// element, where each call reserialises the entire document.
func AssertAllocScaling(t *testing.T, build func(n int) []byte, fn func([]byte)) {
	t.Helper()
	AssertAllocScalingWith(t, Config{}, build, fn)
}

// AssertAllocScalingWith is AssertAllocScaling with explicit sizing.
func AssertAllocScalingWith(t *testing.T, cfg Config, build func(n int) []byte, fn func([]byte)) {
	t.Helper()
	cfg = cfg.withDefaults()

	small := build(cfg.Base)
	large := build(cfg.Base * cfg.Factor)
	if len(small) == 0 {
		t.Fatalf("build(%d) produced an empty payload", cfg.Base)
	}

	inputGrowth := float64(len(large)) / float64(len(small))
	if inputGrowth < float64(cfg.Factor)*0.8 {
		t.Fatalf("build does not scale the payload with n: %d -> %d bytes (%.2fx) for n %d -> %d (%dx). "+
			"The growth ratio below is only meaningful when the payload grows with n.",
			len(small), len(large), inputGrowth, cfg.Base, cfg.Base*cfg.Factor, cfg.Factor)
	}

	smallAlloc := AllocBytesPerOp(func() { fn(small) })
	if smallAlloc == 0 {
		if cfg.AllowZeroAlloc {
			return
		}
		t.Fatalf("the function allocated nothing on a %d-byte payload, so there is no growth to measure "+
			"and this test would pass without ever exercising its subject. Either the payload does not "+
			"reach the code path (check the preconditions the function gates on), or the function really "+
			"is allocation-free, in which case set Config.AllowZeroAlloc.", len(small))
	}
	largeAlloc := AllocBytesPerOp(func() { fn(large) })

	growth := float64(largeAlloc) / float64(smallAlloc)
	if growth > cfg.MaxGrowth {
		t.Errorf("allocation grew %.1fx for a %.1fx larger input (%d -> %d B/op), want at most %.1fx.\n"+
			"That is the O(elements x document) rewrite shape: each sjson Set/Delete reserialises the "+
			"whole document, so N edits cost N copies of it. Collect the edits and apply them in one pass.",
			growth, inputGrowth, smallAlloc, largeAlloc, cfg.MaxGrowth)
	}
}

// settleAttempts and settleInterval bound how long a retention assertion waits
// for cleanup that legitimately lands after the operation returns (a deferred
// cancel, a producer goroutine finishing its own teardown).
const (
	settleAttempts = 100
	settleInterval = 10 * time.Millisecond
)

// AssertNoGoroutineLeak fails when fn leaves goroutines running after it returns.
//
// This is the watcher-leak shape: a goroutine started per request whose exit
// depends on something the handler must remember to do. When it is forgotten the
// goroutine spins forever and pins everything its closure captured, which is how
// ~80% of a production heap became unreclaimable while GC ran perfectly.
//
// The settle loop matters. Cleanup often runs in a deferred block after the
// function under test has returned, so sampling immediately gives a false
// failure.
func AssertNoGoroutineLeak(t *testing.T, fn func()) {
	t.Helper()
	before := runtime.NumGoroutine()
	fn()
	for range settleAttempts {
		if runtime.NumGoroutine() <= before {
			return
		}
		time.Sleep(settleInterval)
	}
	t.Errorf("goroutine count went %d -> %d and stayed there %v after the operation completed; "+
		"something started per-operation is outliving it and pinning whatever it captured",
		before, runtime.NumGoroutine(), time.Duration(settleAttempts)*settleInterval)
}

// AssertReleased fails when the value produce returns is still reachable after
// the operation finishes.
//
// Unlike a heap-size check this proves unreachability rather than inferring it
// from a number that could have moved for any reason. Use it for objects that
// must not outlive a request: a context, an accumulator entry, a cache value.
//
// produce must both create the value and run whatever is supposed to release it,
// returning the pointer to probe. Keeping that inside a function literal is what
// lets the compiler drop the last strong reference before the GC below.
func AssertReleased[T any](t *testing.T, what string, produce func() *T) {
	t.Helper()
	ptr := weak.Make(produce())
	// Twice: the first cycle can leave the object queued rather than collected
	// when a finalizer or cleanup is attached to it.
	runtime.GC()
	runtime.GC()
	if ptr.Value() != nil {
		t.Errorf("%s is still reachable after the operation completed, so it cannot be collected "+
			"for as long as whatever holds it stays alive", what)
	}
}
