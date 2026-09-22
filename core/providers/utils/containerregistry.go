package utils

import (
	"sync"
	"time"
)

// Code-execution sandbox containers created implicitly by a server-side tool are
// billed once per container, not once per response that used it. A conversation
// that keeps calling the code interpreter reuses the same container across turns,
// so without deduplication every turn would be charged a fresh session.
//
// The registry is deliberately in-memory and process-local. A restart, or a second
// replica, re-bills a container that is still alive. That is accepted: the
// alternative is a durable table on the request path, and the error is bounded by
// how long a single container lives.
const (
	// containerClaimTTL bounds how long a claim suppresses re-billing.
	//
	// There is no correct value. The provider expires a container after a period of
	// inactivity and never tells the gateway it did so, so any fixed window is wrong
	// in one direction: too short re-bills a live container, too long misses a
	// genuine recreation. It is set long on purpose, so the error falls on the side
	// of under-billing rather than charging for a session that never happened.
	containerClaimTTL = time.Hour

	// containerClaimCapacity bounds memory. It is sized against peak concurrent
	// sandbox sessions rather than request rate, since one entry covers a container
	// for its whole life.
	containerClaimCapacity = 10000
)

// ContainerSessionRegistry records which code-execution containers have already
// been billed for a session. The zero value is not usable; use the package-level
// ContainerSessions.
type ContainerSessionRegistry struct {
	mu       sync.Mutex
	claimed  map[string]time.Time
	ttl      time.Duration
	capacity int
}

// ContainerSessions is the process-wide registry. It is package-level because the
// shared OpenAI handlers that observe container ids are free functions reached by
// every OpenAI-compatible provider, none of which carry a registry handle.
var ContainerSessions = NewContainerSessionRegistry(containerClaimTTL, containerClaimCapacity)

// NewContainerSessionRegistry builds a registry. Exported for tests, which need
// control over the TTL and capacity.
func NewContainerSessionRegistry(ttl time.Duration, capacity int) *ContainerSessionRegistry {
	return &ContainerSessionRegistry{
		claimed:  make(map[string]time.Time),
		ttl:      ttl,
		capacity: capacity,
	}
}

// Claim reports whether this is the first time the container has been seen, and
// records it. A false return means some earlier response already owes the session
// fee for this container.
//
// An empty key is never claimed, so a provider that reports no container id cannot
// accidentally bill one.
func (r *ContainerSessionRegistry) Claim(key string) bool {
	if r == nil || key == "" {
		return false
	}

	now := time.Now()

	r.mu.Lock()
	defer r.mu.Unlock()

	if claimedAt, ok := r.claimed[key]; ok && now.Sub(claimedAt) < r.ttl {
		return false
	}

	// Sweep before growing past the cap. Doing it here rather than on a ticker keeps
	// the registry free of a background goroutine, and so of a shutdown hook.
	if len(r.claimed) >= r.capacity {
		r.evictLocked(now)
	}

	r.claimed[key] = now
	return true
}

// evictLocked drops expired entries, and if that was not enough to get back under
// the cap, drops the oldest claims until it is. Evicting a live claim can re-bill
// its container, which is the same bounded error the TTL already accepts.
func (r *ContainerSessionRegistry) evictLocked(now time.Time) {
	for key, claimedAt := range r.claimed {
		if now.Sub(claimedAt) >= r.ttl {
			delete(r.claimed, key)
		}
	}
	for len(r.claimed) >= r.capacity {
		oldestKey := ""
		var oldest time.Time
		for key, claimedAt := range r.claimed {
			if oldestKey == "" || claimedAt.Before(oldest) {
				oldestKey, oldest = key, claimedAt
			}
		}
		if oldestKey == "" {
			return
		}
		delete(r.claimed, oldestKey)
	}
}

// Len reports the number of live claims. For tests and diagnostics.
func (r *ContainerSessionRegistry) Len() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.claimed)
}
