package kvstore

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bytedance/sonic"
)

var (
	ErrClosed     = errors.New("kvstore is closed")
	ErrEmptyKey   = errors.New("key cannot be empty")
	ErrNotFound   = errors.New("key not found")
	ErrInvalidTTL = errors.New("ttl cannot be negative")
)

const (
	defaultCleanupInterval = 30 * time.Second
	noExpirationUnixNanos  = int64(0)
)

// Config controls in-memory KV store behavior.
type Config struct {
	// CleanupInterval controls how often expired entries are removed.
	// If <= 0, defaults to 30s.
	CleanupInterval time.Duration
	// DefaultTTL applies when Set is used.
	// A zero value means entries do not expire by default.
	DefaultTTL time.Duration
}

type entry struct {
	value     any
	writtenAt int64 // unix nanos, 0 means not written yet
	expiresAt int64 // unix nanos, 0 means no expiration
}

// Store is an in-memory KV store with optional TTL support.
type Store struct {
	mu   sync.RWMutex
	data map[string]entry

	defaultTTL      time.Duration
	cleanupInterval time.Duration

	closed    atomic.Bool
	stopCh    chan struct{}
	stopOnce  sync.Once
	cleanupWg sync.WaitGroup

	delegate  SyncDelegate
	decoders  map[string]TypeDecoder
	decoderMu sync.RWMutex
}

// SyncDelegate is notified of all mutations, enabling cross-node replication.
// All calls happen synchronously after the local mutation has succeeded.
// writtenAt / deletedAt are absolute Unix nanosecond timestamps used by remote
// nodes for last-write-wins conflict resolution.
// expiresAt is an absolute Unix nanosecond timestamp; 0 means no expiration.
type SyncDelegate interface {
	OnSet(key string, valueJSON []byte, writtenAt int64, expiresAt int64)
	OnDelete(key string, deletedAt int64)
}

// TypeDecoder reconstructs a concrete value from its JSON representation.
// Register decoders by key prefix via RegisterDecoder.
type TypeDecoder func(data []byte) (any, error)

// SetDelegate plugs in the cluster sync implementation.
func (s *Store) SetDelegate(d SyncDelegate) {
	s.delegate = d
}

// RegisterDecoder registers a decoder for keys matching the given prefix.
// Used by the receiving side to reconstruct concrete types from gossip payloads.
func (s *Store) RegisterDecoder(keyPrefix string, decoder TypeDecoder) {
	s.decoderMu.Lock()
	s.decoders[keyPrefix] = decoder
	s.decoderMu.Unlock()
}

// New creates a new in-memory KV store.
func New(cfg Config) (*Store, error) {
	if cfg.DefaultTTL < 0 {
		return nil, ErrInvalidTTL
	}

	cleanupInterval := cfg.CleanupInterval
	if cleanupInterval <= 0 {
		cleanupInterval = defaultCleanupInterval
	}

	s := &Store{
		data:            make(map[string]entry),
		defaultTTL:      cfg.DefaultTTL,
		cleanupInterval: cleanupInterval,
		stopCh:          make(chan struct{}),
		decoders:        make(map[string]TypeDecoder),
	}

	s.cleanupWg.Add(1)
	go s.cleanupLoop()

	return s, nil
}

// Set stores a value using the store's default TTL.
func (s *Store) Set(key string, value any) error {
	return s.SetWithTTL(key, value, s.defaultTTL)
}

// SetWithTTL stores a value with an explicit TTL.
// ttl=0 means no expiration.
func (s *Store) SetWithTTL(key string, value any, ttl time.Duration) error {
	if err := s.validateMutable(key, ttl); err != nil {
		return err
	}

	now := time.Now().UnixNano()
	var expiresAt int64
	if ttl > 0 {
		expiresAt = now + int64(ttl)
	}

	var valueJSON []byte
	var err error

	if s.delegate != nil {
		valueJSON, err = sonic.Marshal(value)
		if err != nil {
			return err
		}
	}

	s.mu.Lock()
	s.data[key] = entry{
		value:     value,
		writtenAt: now,
		expiresAt: expiresAt,
	}
	s.mu.Unlock()

	if s.delegate != nil {
		s.delegate.OnSet(key, valueJSON, now, expiresAt)
	}

	return nil
}

// SetNXWithTTL atomically sets a value with TTL only if the key does not exist.
// Returns true if the key was set, false if the key already existed.
// ttl=0 means no expiration.
func (s *Store) SetNXWithTTL(key string, value any, ttl time.Duration) (bool, error) {
	if err := s.validateMutable(key, ttl); err != nil {
		return false, err
	}

	now := time.Now().UnixNano()
	var expiresAt int64
	if ttl > 0 {
		expiresAt = now + int64(ttl)
	}

	var valueJSON []byte
	var err error

	if s.delegate != nil {
		valueJSON, err = sonic.Marshal(value)
		if err != nil {
			return false, err
		}
	}

	s.mu.Lock()
	
	// Check if key exists and is not expired
	if existing, ok := s.data[key]; ok {
		if !isExpired(existing, now) {
			s.mu.Unlock()
			return false, nil // Key already exists
		}
		// Key exists but is expired, allow overwrite
	}
	
	// Key doesn't exist or is expired, set it
	s.data[key] = entry{
		value:     value,
		writtenAt: now,
		expiresAt: expiresAt,
	}
	s.mu.Unlock()

	if s.delegate != nil {
		s.delegate.OnSet(key, valueJSON, now, expiresAt)
	}

	return true, nil
}

// SetRemote applies a remotely-gossiped entry without triggering OnSet.
// writtenAt and expiresAt must be absolute Unix nanosecond timestamps.
// If the local entry was written more recently than writtenAt the update is
// silently skipped (last-write-wins by wall clock on the writing node).
func (s *Store) SetRemote(key string, valueJSON []byte, writtenAt int64, expiresAt int64) error {
	if key == "" {
		return ErrEmptyKey
	}
	if s.closed.Load() {
		return ErrClosed
	}

	value := s.decodeValue(key, valueJSON)

	s.mu.Lock()
	if existing, ok := s.data[key]; ok && existing.writtenAt > writtenAt {
		s.mu.Unlock()
		return nil // stale gossip — local entry is newer
	}
	s.data[key] = entry{value: value, writtenAt: writtenAt, expiresAt: expiresAt}
	s.mu.Unlock()
	return nil
}

// Get retrieves a value by key.
func (s *Store) Get(key string) (any, error) {
	if key == "" {
		return nil, ErrEmptyKey
	}
	if s.closed.Load() {
		return nil, ErrClosed
	}

	now := time.Now().UnixNano()

	s.mu.RLock()
	e, ok := s.data[key]
	s.mu.RUnlock()

	if !ok {
		return nil, ErrNotFound
	}
	if isExpired(e, now) {
		s.mu.Lock()
		if latest, exists := s.data[key]; exists && isExpired(latest, time.Now().UnixNano()) {
			delete(s.data, key)
		}
		s.mu.Unlock()
		return nil, ErrNotFound
	}

	return e.value, nil
}

// GetAndDelete retrieves and deletes a key atomically.
func (s *Store) GetAndDelete(key string) (any, error) {
	if key == "" {
		return nil, ErrEmptyKey
	}
	if s.closed.Load() {
		return nil, ErrClosed
	}

	now := time.Now().UnixNano()

	s.mu.Lock()
	e, ok := s.data[key]
	if ok {
		delete(s.data, key)
	}
	s.mu.Unlock()

	if !ok || isExpired(e, now) {
		return nil, ErrNotFound
	}
	if s.delegate != nil {
		s.delegate.OnDelete(key, now)
	}
	return e.value, nil
}

// Delete removes a key.
func (s *Store) Delete(key string) (bool, error) {
	if key == "" {
		return false, ErrEmptyKey
	}
	if s.closed.Load() {
		return false, ErrClosed
	}

	deletedAt := time.Now().UnixNano()

	s.mu.Lock()
	_, ok := s.data[key]
	if ok {
		delete(s.data, key)
	}
	s.mu.Unlock()

	if !ok {
		return false, nil
	}
	if s.delegate != nil {
		s.delegate.OnDelete(key, deletedAt)
	}
	return true, nil
}

// DeleteRemote applies a remotely-gossiped delete without triggering OnDelete.
// deletedAt is the absolute Unix nanosecond timestamp when the delete was issued.
// The delete is skipped if the local entry was written after the delete intent
// (last-write-wins).
func (s *Store) DeleteRemote(key string, deletedAt int64) error {
	if key == "" {
		return ErrEmptyKey
	}
	if s.closed.Load() {
		return ErrClosed
	}

	s.mu.Lock()
	if existing, ok := s.data[key]; ok && existing.writtenAt > deletedAt {
		s.mu.Unlock()
		return nil // entry was written after the delete intent — write wins
	}
	delete(s.data, key)
	s.mu.Unlock()
	return nil
}

// Len returns the number of currently non-expired keys.
func (s *Store) Len() int {
	if s.closed.Load() {
		return 0
	}

	now := time.Now().UnixNano()
	total := 0

	s.mu.RLock()
	for _, v := range s.data {
		if isExpired(v, now) {
			continue
		}
		total++
	}
	s.mu.RUnlock()

	return total
}

// Close stops background cleanup and prevents further operations.
func (s *Store) Close() error {
	s.stopOnce.Do(func() {
		s.closed.Store(true)
		close(s.stopCh)
	})
	s.cleanupWg.Wait()
	return nil
}

func (s *Store) cleanupLoop() {
	defer s.cleanupWg.Done()

	ticker := time.NewTicker(s.cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.cleanupExpired()
		case <-s.stopCh:
			return
		}
	}
}

func (s *Store) cleanupExpired() {
	now := time.Now().UnixNano()

	s.mu.Lock()
	for k, v := range s.data {
		if isExpired(v, now) {
			delete(s.data, k)
		}
	}
	s.mu.Unlock()
}

func (s *Store) validateMutable(key string, ttl time.Duration) error {
	if key == "" {
		return ErrEmptyKey
	}
	if ttl < 0 {
		return ErrInvalidTTL
	}
	if s.closed.Load() {
		return ErrClosed
	}
	return nil
}

// decodeValue uses the registered decoder for the key's prefix, falling back
// to raw []byte if no decoder matches.
func (s *Store) decodeValue(key string, valueJSON []byte) any {
	s.decoderMu.RLock()

	var bestPrefix string
	var bestDecode TypeDecoder
	for prefix, decode := range s.decoders {
		if strings.HasPrefix(key, prefix) && len(prefix) > len(bestPrefix) {
			bestPrefix = prefix
			bestDecode = decode
		}
	}

	s.decoderMu.RUnlock()

	if bestDecode != nil {
		if v, err := bestDecode(valueJSON); err == nil {
			return v
		}
	}
	return valueJSON
}

func isExpired(e entry, nowUnixNano int64) bool {
	return e.expiresAt != noExpirationUnixNanos && nowUnixNano >= e.expiresAt
}
