//go:build !pooldebug

package pool

import "sync"

// Pool is a generic, type-safe object pool.
// In production builds, this is a zero-overhead wrapper around sync.Pool.
type Pool[T any] struct {
	sp sync.Pool
}

// New creates a new Pool with the given name and factory function.
// The name is used for identification in debug builds; ignored in production.
func New[T any](name string, factory func() *T) *Pool[T] {
	return &Pool[T]{
		sp: sync.Pool{
			New: func() interface{} {
				return factory()
			},
		},
	}
}

// Get acquires an object from the pool.
// If the pool is empty, the factory function creates a new one.
func (p *Pool[T]) Get() *T {
	return p.sp.Get().(*T)
}

// Put returns an object to the pool.
// The caller must reset the object's fields before calling Put.
func (p *Pool[T]) Put(obj *T) {
	if obj == nil {
		return
	}
	p.sp.Put(obj)
}

// Prewarm creates n objects via the factory and places them in the pool.
// Useful for avoiding allocation spikes at startup.
func (p *Pool[T]) Prewarm(n int) {
	for i := 0; i < n; i++ {
		p.sp.Put(p.sp.New())
	}
}

// CheckActive is a no-op in production builds.
// In debug builds, it panics if the object has been released.
func (p *Pool[T]) CheckActive(_ *T) {}

// Stats returns zero-value stats in production builds.
func (p *Pool[T]) Stats() PoolStats {
	return PoolStats{}
}
