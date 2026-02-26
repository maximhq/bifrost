//go:build pooldebug

package pool

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"
)

const stackDepth = 16

// objectTracker holds debug metadata for a single acquired object.
type objectTracker struct {
	acquireStack [stackDepth]uintptr
	releaseStack [stackDepth]uintptr
	acquireDepth int
	releaseDepth int
}

// Pool is a generic, type-safe object pool with full debug tracking.
// In debug builds, it detects double-release, use-after-release, and leaks.
type Pool[T any] struct {
	sp      sync.Pool
	name    string
	factory func() *T

	// active tracks objects that have been acquired but not yet released.
	// Key: uintptr of the object pointer. Value: *objectTracker.
	active sync.Map

	acquireCount atomic.Int64
	releaseCount atomic.Int64
	createCount  atomic.Int64
}

// New creates a new Pool with the given name and factory function.
// In debug builds, the pool registers itself in the global registry for AllStats().
func New[T any](name string, factory func() *T) *Pool[T] {
	p := &Pool[T]{
		name:    name,
		factory: factory,
	}
	p.sp = sync.Pool{
		New: func() interface{} {
			p.createCount.Add(1)
			return factory()
		},
	}
	register(p)
	return p
}

// Get acquires an object from the pool and tracks it as active.
func (p *Pool[T]) Get() *T {
	obj := p.sp.Get().(*T)
	ptr := uintptr(unsafe.Pointer(obj))

	tracker := &objectTracker{}
	tracker.acquireDepth = runtime.Callers(2, tracker.acquireStack[:])

	if prev, loaded := p.active.LoadOrStore(ptr, tracker); loaded {
		// A stale entry exists at this address. This happens when a previously
		// acquired object was leaked (never Put back), garbage-collected, and the
		// Go allocator reused the address for a new allocation. The uintptr key in
		// the sync.Map does not prevent GC of the original object.
		// Replace the stale entry with the new tracker and log the previous
		// acquire stack so the leak can be investigated.
		prevTracker := prev.(*objectTracker)
		fmt.Fprintf(os.Stderr,
			"[pool:%s] WARNING: Get() returned object %p whose address matches a leaked (never-released) object.\n"+
				"Previous acquire stack (the leak):\n%s\nCurrent acquire stack:\n%s\n",
			p.name, obj,
			formatFrames(prevTracker.acquireStack[:prevTracker.acquireDepth]),
			formatFrames(tracker.acquireStack[:tracker.acquireDepth]),
		)
		p.active.Store(ptr, tracker)
	}

	p.acquireCount.Add(1)
	return obj
}

// Put returns an object to the pool.
// Panics if the object is not currently tracked as active (double-release or foreign object).
func (p *Pool[T]) Put(obj *T) {
	if obj == nil {
		return
	}

	ptr := uintptr(unsafe.Pointer(obj))
	val, ok := p.active.LoadAndDelete(ptr)
	if !ok {
		// Build a helpful panic message with the current stack
		panic(fmt.Sprintf(
			"[pool:%s] Put() called on object %p that is not tracked as active.\n"+
				"This is either a double-release or a Put of an object not from this pool.\n"+
				"Current stack:\n%s",
			p.name, obj, formatCallers(2),
		))
	}

	// Record release stack on the tracker for diagnostics
	tracker := val.(*objectTracker)
	tracker.releaseDepth = runtime.Callers(2, tracker.releaseStack[:])

	p.releaseCount.Add(1)
	p.sp.Put(obj)
}

// Prewarm creates n objects via the factory and places them in the pool.
// These objects bypass debug tracking (no acquire/release/create counts).
func (p *Pool[T]) Prewarm(n int) {
	for i := 0; i < n; i++ {
		p.sp.Put(p.factory())
	}
}

// CheckActive panics if the given object has been released back to the pool.
// Use this at key points in your code to detect use-after-release.
func (p *Pool[T]) CheckActive(obj *T) {
	if obj == nil {
		return
	}
	ptr := uintptr(unsafe.Pointer(obj))
	if _, ok := p.active.Load(ptr); !ok {
		panic(fmt.Sprintf(
			"[pool:%s] CheckActive() failed: object %p is NOT active (already released or never acquired from this pool).\n"+
				"Current stack:\n%s",
			p.name, obj, formatCallers(2),
		))
	}
}

// Stats returns current statistics for this pool.
func (p *Pool[T]) Stats() PoolStats {
	return p.stats()
}

// stats implements the statsCollector interface for the global registry.
func (p *Pool[T]) stats() PoolStats {
	acquires := p.acquireCount.Load()
	creates := p.createCount.Load()

	var hitRate float64
	if acquires > 0 {
		hitRate = 1.0 - float64(creates)/float64(acquires)
		if hitRate < 0 {
			hitRate = 0 // can happen during prewarm since prewarm creates bypass acquire count
		}
	}

	activeCount := int64(0)
	p.active.Range(func(_, _ interface{}) bool {
		activeCount++
		return true
	})

	return PoolStats{
		Name:     p.name,
		Acquires: acquires,
		Releases: p.releaseCount.Load(),
		Creates:  creates,
		Active:   activeCount,
		HitRate:  hitRate,
	}
}

// ActiveObjects returns debug info about all currently active (acquired but not released) objects.
// Useful for leak investigation. Returns a map of pointer address to formatted acquire stack.
func (p *Pool[T]) ActiveObjects() map[string]string {
	result := make(map[string]string)
	p.active.Range(func(key, value interface{}) bool {
		ptr := key.(uintptr)
		tracker := value.(*objectTracker)
		result[fmt.Sprintf("%#x", ptr)] = formatFrames(tracker.acquireStack[:tracker.acquireDepth])
		return true
	})
	return result
}

// formatCallers captures and formats the call stack starting at the given skip level.
func formatCallers(skip int) string {
	var pcs [stackDepth]uintptr
	n := runtime.Callers(skip+1, pcs[:])
	return formatFrames(pcs[:n])
}

// formatFrames formats program counters into a readable stack trace.
func formatFrames(pcs []uintptr) string {
	frames := runtime.CallersFrames(pcs)
	var sb strings.Builder
	for {
		frame, more := frames.Next()
		fmt.Fprintf(&sb, "  %s\n    %s:%d\n", frame.Function, frame.File, frame.Line)
		if !more {
			break
		}
	}
	return sb.String()
}
