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

// ConditionalStringKVStore is an optional process-local compare-and-swap
// capability for string bindings. Implementations must only replace an
// existing, unexpired value that equals expected; a missing or expired value
// must never be resurrected by a conditional write.
//
// The capability is intentionally separate from KVStore. A KVStore may be
// backed by a delegated cluster implementation whose local CAS cannot provide
// the process-local semantics required by sticky-key failover.
type ConditionalStringKVStore interface {
	CompareAndSwapStringWithTTL(key, expected, replacement string, ttl time.Duration) (bool, error)
}

const (
	DefaultSessionStickyTTL = time.Hour
)
