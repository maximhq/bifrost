package queue

import (
	"context"
	"sync"

	"github.com/maximhq/bifrost/framework/logstore"
)

// processLocker is a DistributedLocker that only excludes goroutines of one
// process. It lets a single Bifrost instance consume from ClickHouse without a
// shared lock store; it is not safe when several instances share the queue.
type processLocker struct {
	mu    sync.Mutex
	locks map[string]chan struct{}
}

// NewProcessLocker returns a locker for single-instance deployments and tests.
// Use configstore.DistributedLockManager when instances share the queue.
func NewProcessLocker() logstore.DistributedLocker {
	return &processLocker{locks: map[string]chan struct{}{}}
}

func (l *processLocker) sem(key string) chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	ch, ok := l.locks[key]
	if !ok {
		ch = make(chan struct{}, 1)
		l.locks[key] = ch
	}
	return ch
}

func (l *processLocker) Acquire(ctx context.Context, key string) (context.Context, func(), error) {
	sem := l.sem(key)
	select {
	case sem <- struct{}{}:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	held, cancel := context.WithCancel(ctx)
	var once sync.Once
	return held, func() {
		once.Do(func() {
			cancel()
			<-sem
		})
	}, nil
}
