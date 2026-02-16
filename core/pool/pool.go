// Package pool provides a pool of resources for Bifrost.
package pool

import "sync"

// PoolStats holds statistics for a single pool.
// In production builds (!pooldebug), all fields are zero.
type PoolStats struct {
	Name     string  `json:"name"`
	Acquires int64   `json:"acquires"`
	Releases int64   `json:"releases"`
	Creates  int64   `json:"creates"`  // factory calls = pool misses
	Active   int64   `json:"active"`   // acquired but not yet released
	HitRate  float64 `json:"hit_rate"` // 1 - (Creates / Acquires)
}

// registry holds references to all pool stat collectors for AllStats().
var (
	registryMu sync.Mutex
	registry   []statsCollector
)

// statsCollector is implemented by both prod and debug pool impls.
type statsCollector interface {
	stats() PoolStats
}

func register(c statsCollector) {
	registryMu.Lock()
	registry = append(registry, c)
	registryMu.Unlock()
}

// AllStats returns statistics for every registered Pool.
// Returns nil in production builds (no pools register in prod mode).
func AllStats() []PoolStats {
	registryMu.Lock()
	collectors := make([]statsCollector, len(registry))
	copy(collectors, registry)
	registryMu.Unlock()

	if len(collectors) == 0 {
		return nil
	}

	result := make([]PoolStats, len(collectors))
	for i, c := range collectors {
		result[i] = c.stats()
	}
	return result
}
