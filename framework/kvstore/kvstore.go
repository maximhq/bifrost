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
// expiresAt is an absolute Unix nanosecond timestamp; 0 means no expiration.
type SyncDelegate interface {
	OnSet(key string, valueJSON []byte, expiresAt int64)
	OnDelete(key string)
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

	expiresAt := expirationFromTTL(ttl)

	s.mu.Lock()
	s.data[key] = entry{value: value, expiresAt: expiresAt}
	s.mu.Unlock()

	if s.delegate != nil {
		valueJSON, _ := sonic.Marshal(value)
		s.delegate.OnSet(key, valueJSON, expiresAt)
	}
	return nil
}

// SetIfAbsent stores a value only if the key does not currently exist.
// Expired values are treated as absent.
func (s *Store) SetIfAbsent(key string, value any, ttl time.Duration) (bool, error) {
	if err := s.validateMutable(key, ttl); err != nil {
		return false, err
	}

	now := time.Now().UnixNano()
	expiresAt := expirationFromTTL(ttl)

	s.mu.Lock()
	if existing, ok := s.data[key]; ok && !isExpired(existing, now) {
		s.mu.Unlock()
		return false, nil
	}
	s.data[key] = entry{value: value, expiresAt: expiresAt}
	s.mu.Unlock()

	return true, nil
}

// SetRemote applies a remotely-gossiped entry without triggering OnSet.
// expiresAt must be an absolute Unix nanosecond timestamp (not a TTL duration).
func (s *Store) SetRemote(key string, valueJSON []byte, expiresAt int64) error {
	if key == "" {
		return ErrEmptyKey
	}
	if s.closed.Load() {
		return ErrClosed
	}

	value := s.decodeValue(key, valueJSON)

	s.mu.Lock()
	s.data[key] = entry{value: value, expiresAt: expiresAt}
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
		if latest, exists := s.data[key]; exists && isExpired(latest, now) {
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
		s.delegate.OnDelete(key)
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
		s.delegate.OnDelete(key)
	}
	return true, nil
}

// Touch updates the expiration TTL for an existing key.
func (s *Store) Touch(key string, ttl time.Duration) (bool, error) {
	if err := s.validateMutable(key, ttl); err != nil {
		return false, err
	}

	now := time.Now().UnixNano()
	expiresAt := expirationFromTTL(ttl)

	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.data[key]
	if !ok {
		return false, nil
	}
	if isExpired(e, now) {
		delete(s.data, key)
		return false, nil
	}

	e.expiresAt = expiresAt
	s.data[key] = e
	return true, nil
}

// Len returns the number of currently non-expired keys.
func (s *Store) Len() int {
	if s.closed.Load() {
		return 0
	}

	now := time.Now().UnixNano()
	total := 0

	s.mu.Lock()
	for k, v := range s.data {
		if isExpired(v, now) {
			delete(s.data, k)
			continue
		}
		total++
	}
	s.mu.Unlock()

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
	defer s.decoderMu.RUnlock()

	for prefix, decode := range s.decoders {
		if strings.HasPrefix(key, prefix) {
			if v, err := decode(valueJSON); err == nil {
				return v
			}
		}
	}
	return valueJSON
}

func expirationFromTTL(ttl time.Duration) int64 {
	if ttl == 0 {
		return noExpirationUnixNanos
	}
	return time.Now().Add(ttl).UnixNano()
}

func isExpired(e entry, nowUnixNano int64) bool {
	return e.expiresAt != noExpirationUnixNanos && nowUnixNano >= e.expiresAt
}
