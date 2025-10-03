package otel

import (
	"sync"
	"time"
)

// TTLSyncMap is a thread-safe map with automatic cleanup of expired entries
type TTLSyncMap struct {
	data          sync.Map
	ttl           time.Duration
	cleanupTicker *time.Ticker
	stopCleanup   chan struct{}
	cleanupWg     sync.WaitGroup
	stopOnce      sync.Once
}

// entry stores the value along with its expiration time
type entry struct {
	value     interface{}
	expiresAt time.Time
}

// NewTTLSyncMap creates a new TTL sync map with the specified TTL and cleanup interval
// ttl: time to live for each entry
// cleanupInterval: how often to check for expired entries (should be <= ttl)
func NewTTLSyncMap(ttl time.Duration, cleanupInterval time.Duration) *TTLSyncMap {
	if ttl <= 0 {
		ttl = time.Minute
	}
	if cleanupInterval <= 0 {
		cleanupInterval = ttl / 2
		if cleanupInterval <= 0 {
			cleanupInterval = time.Minute
		}
	}

	m := &TTLSyncMap{
		ttl:           ttl,
		cleanupTicker: time.NewTicker(cleanupInterval),
		stopCleanup:   make(chan struct{}),
	}

	// Start the cleanup goroutine
	m.cleanupWg.Add(1)
	go m.startCleanup()

	return m
}

// Set stores a key-value pair with TTL
func (m *TTLSyncMap) Set(key, value interface{}) {
	m.data.Store(key, &entry{
		value:     value,
		expiresAt: time.Now().Add(m.ttl),
	})
}

// Get retrieves a value by key, returns (value, true) if found and not expired,
// (nil, false) otherwise
func (m *TTLSyncMap) Get(key interface{}) (interface{}, bool) {
	val, ok := m.data.Load(key)
	if !ok {
		return nil, false
	}

	e := val.(*entry)
	if time.Now().After(e.expiresAt) {
		// Entry has expired, delete it
		m.data.Delete(key)
		return nil, false
	}

	return e.value, true
}

// Delete removes a key-value pair from the map
func (m *TTLSyncMap) Delete(key interface{}) {
	m.data.Delete(key)
}

// Refresh updates the expiration time of an existing entry
func (m *TTLSyncMap) Refresh(key interface{}) bool {
	val, ok := m.data.Load(key)
	if !ok {
		return false
	}
	e, _ := val.(*entry)
	if e == nil || time.Now().After(e.expiresAt) {
		m.data.Delete(key)
		return false
	}
	m.data.Store(key, &entry{
		value:     e.value,
		expiresAt: time.Now().Add(m.ttl),
	})
	return true
}

// GetOrSet retrieves a value by key if it exists and is not expired,
// otherwise sets the new value and returns it
func (m *TTLSyncMap) GetOrSet(key, value interface{}) (actual interface{}, loaded bool) {
	actual, loaded = m.Get(key)
	if !loaded {
		m.Set(key, value)
		actual = value
	}
	return actual, loaded
}

// Range calls f sequentially for each key and value present in the map.
// If f returns false, range stops the iteration.
// Only non-expired entries are included.
func (m *TTLSyncMap) Range(f func(key, value interface{}) bool) {
	now := time.Now()
	m.data.Range(func(key, val interface{}) bool {
		e := val.(*entry)
		if now.After(e.expiresAt) {
			// Skip expired entry and delete it
			m.data.Delete(key)
			return true
		}
		return f(key, e.value)
	})
}

// Len returns the number of non-expired entries in the map
func (m *TTLSyncMap) Len() int {
	count := 0
	m.Range(func(_, _ interface{}) bool {
		count++
		return true
	})
	return count
}

// startCleanup runs in a background goroutine to periodically remove expired entries
func (m *TTLSyncMap) startCleanup() {
	defer m.cleanupWg.Done()

	for {
		select {
		case <-m.cleanupTicker.C:
			m.cleanup()
		case <-m.stopCleanup:
			return
		}
	}
}

// cleanup removes all expired entries from the map
func (m *TTLSyncMap) cleanup() {
	now := time.Now()
	m.data.Range(func(key, val interface{}) bool {
		e := val.(*entry)
		if now.After(e.expiresAt) {
			m.data.Delete(key)
		}
		return true
	})
	if m.Len() > 10000 {
		logger.Warn("[otel] map cleanup done. current size: %d entries", m.Len())
	} else {
		logger.Debug("[otel] map cleanup done. current size: %d entries", m.Len())
	}
}

// Stop stops the cleanup goroutine and releases resources
// Call this when you're done with the map to prevent goroutine leaks
func (m *TTLSyncMap) Stop() {
	m.stopOnce.Do(func() {
		close(m.stopCleanup)
		m.cleanupTicker.Stop()
		m.cleanupWg.Wait()
	})
}

// Clear removes all entries from the map
func (m *TTLSyncMap) Clear() {
	m.data.Range(func(key, _ interface{}) bool {
		m.data.Delete(key)
		return true
	})
}
