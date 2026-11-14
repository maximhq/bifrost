package queue

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
)

// Dependencies are the shared resources a queue implementation may build on.
// A queue never closes them.
type Dependencies struct {
	LogStore logstore.LogStore          // backing store for QueueTypeLogStore
	Locker   logstore.DistributedLocker // cross-instance lock; required to consume on ClickHouse
}

// Factory builds a queue from its config. cfg.Config holds the typed config
// for built-in types and a json.RawMessage for registered ones.
type Factory func(ctx context.Context, cfg *Config, deps Dependencies, logger schemas.Logger) (Queue, error)

var (
	registryMu sync.RWMutex
	registry   = map[QueueType]Factory{}
)

// Register makes a queue implementation available to NewQueue under t. It is
// meant to be called from an init function or at startup, and panics on an
// empty type, a nil factory, or a type registered twice, like database/sql's
// Register.
func Register(t QueueType, f Factory) {
	if t == "" {
		panic("queue: Register with empty type")
	}
	if f == nil {
		panic("queue: Register with nil factory for " + string(t))
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := registry[t]; dup {
		panic("queue: Register called twice for " + string(t))
	}
	registry[t] = f
}

// Registered returns the registered queue types, sorted.
func Registered() []QueueType {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]QueueType, 0, len(registry))
	for t := range registry {
		out = append(out, t)
	}
	slices.Sort(out)
	return out
}

// NewQueue builds the queue cfg selects. It returns nil, nil when cfg is nil
// or disabled, so callers can treat "no queue configured" uniformly.
func NewQueue(ctx context.Context, cfg *Config, deps Dependencies, logger schemas.Logger) (Queue, error) {
	if cfg == nil || !cfg.Enabled {
		return nil, nil
	}
	registryMu.RLock()
	f, ok := registry[cfg.Type]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown queue type: %q (registered: %v)", cfg.Type, Registered())
	}
	return f(ctx, cfg, deps, logger)
}
