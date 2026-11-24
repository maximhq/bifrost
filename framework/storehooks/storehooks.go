// Package storehooks lets a downstream build observe every runtime database
// pool the config and logs stores open, at the moment it opens.
//
// GORM compiles its callback chain without a lock, so a plugin registered on a
// pool that is already serving queries races with those queries. Stores call
// PoolOpened right after a pool is created and before it is shared, which is
// the one point where registering callbacks is safe. OSS registers nothing.
package storehooks

import (
	"sync"

	"gorm.io/gorm"
)

// Role names the store a pool belongs to.
type Role string

const (
	// RoleConfig is the config store's runtime pool.
	RoleConfig Role = "config"
	// RoleLogs is the logs store's runtime pool.
	RoleLogs Role = "logs"
)

// PoolOpenedFn is called with each new runtime pool. It must not block.
type PoolOpenedFn func(role Role, db *gorm.DB)

var (
	mu    sync.RWMutex
	hooks []PoolOpenedFn
)

// RegisterPoolOpened adds fn to the hooks run for every new runtime pool. Call
// it before the stores are created; pools opened earlier are not replayed.
func RegisterPoolOpened(fn PoolOpenedFn) {
	if fn == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	hooks = append(hooks, fn)
}

// PoolOpened runs every registered hook for a pool that was just opened and is
// not yet in use. Stores call it for each runtime pool, including one that
// replaces a refreshed pool.
func PoolOpened(role Role, db *gorm.DB) {
	if db == nil {
		return
	}
	mu.RLock()
	fns := append([]PoolOpenedFn(nil), hooks...)
	mu.RUnlock()
	for _, fn := range fns {
		fn(role, db)
	}
}
