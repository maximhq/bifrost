package schemas

import "time"

// KVStore is a minimal interface for a key-value store used by Bifrost internals.
// The concrete implementation (e.g. framework/kvstore.Store) is injected by the
// caller and must satisfy this interface. Passing nil disables KV-backed features.
type KVStore interface {
	Get(key string) (any, error)
	SetWithTTL(key string, value any, ttl time.Duration) error
	SetNXWithTTL(key string, value any, ttl time.Duration) (bool, error)
	Delete(key string) (bool, error)
}

const (
	DefaultSessionStickyTTL = time.Hour
)
