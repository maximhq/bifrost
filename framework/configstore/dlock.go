package configstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

// Default lock configuration values
const (
	DefaultLockTTL         = 30 * time.Second
	DefaultRetryInterval   = 100 * time.Millisecond
	DefaultMaxRetries      = 100
	DefaultCleanupInterval = 5 * time.Minute
)

// Lock errors
var (
	ErrLockNotAcquired = errors.New("failed to acquire lock")
	ErrLockNotHeld     = errors.New("lock not held by this holder")
	ErrLockExpired     = errors.New("lock has expired")
)

// LockStore defines the storage operations required for distributed locking.
// This interface abstracts the database operations, making the lock implementation
// testable and decoupled from the specific database implementation.
type LockStore interface {
	// TryAcquireLock attempts to insert a lock row. Returns true if the lock was acquired.
	// If the lock already exists and is not expired, returns false.
	TryAcquireLock(ctx context.Context, lock *tables.TableDistributedLock) (bool, error)

	// GetLock retrieves a lock by its key. Returns nil if the lock doesn't exist.
	GetLock(ctx context.Context, lockKey string) (*tables.TableDistributedLock, error)

	// UpdateLockExpiry updates the expiration time for an existing lock.
	// Only succeeds if the holder ID matches the current lock holder.
	UpdateLockExpiry(ctx context.Context, lockKey, holderID string, expiresAt time.Time) error

	// ReleaseLock deletes a lock if the holder ID matches.
	// Returns true if the lock was released, false if it wasn't held by the given holder.
	ReleaseLock(ctx context.Context, lockKey, holderID string) (bool, error)

	// CleanupExpiredLocks removes all locks that have expired.
	// Returns the number of locks cleaned up.
	CleanupExpiredLocks(ctx context.Context) (int64, error)

	// CleanupExpiredLockByKey atomically deletes a lock only if it has expired.
	// Returns true if an expired lock was deleted, false if the lock doesn't exist or hasn't expired.
	CleanupExpiredLockByKey(ctx context.Context, lockKey string) (bool, error)
}

// DistributedLockManager creates and manages distributed locks.
// It provides a factory for creating locks with consistent configuration.
type DistributedLockManager struct {
	store         LockStore
	logger        schemas.Logger
	defaultTTL    time.Duration
	retryInterval time.Duration
	maxRetries    int
}

// DistributedLockManagerOption is a function that configures a DistributedLockManager.
type DistributedLockManagerOption func(*DistributedLockManager)

// WithDefaultTTL sets the default TTL for locks created by this manager.
func WithDefaultTTL(ttl time.Duration) DistributedLockManagerOption {
	return func(m *DistributedLockManager) {
		m.defaultTTL = ttl
	}
}

// WithRetryInterval sets the interval between lock acquisition retries.
func WithRetryInterval(interval time.Duration) DistributedLockManagerOption {
	return func(m *DistributedLockManager) {
		m.retryInterval = interval
	}
}

// WithMaxRetries sets the maximum number of retries for lock acquisition.
func WithMaxRetries(maxRetries int) DistributedLockManagerOption {
	return func(m *DistributedLockManager) {
		m.maxRetries = maxRetries
	}
}

// NewDistributedLockManager creates a new lock manager with the given store and options.
func NewDistributedLockManager(store LockStore, logger schemas.Logger, opts ...DistributedLockManagerOption) *DistributedLockManager {
	m := &DistributedLockManager{
		store:         store,
		logger:        logger,
		defaultTTL:    DefaultLockTTL,
		retryInterval: DefaultRetryInterval,
		maxRetries:    DefaultMaxRetries,
	}

	for _, opt := range opts {
		opt(m)
	}

	return m
}

// NewLock creates a new DistributedLock for the given key.
// The lock is not acquired until Lock() or TryLock() is called.
func (m *DistributedLockManager) NewLock(lockKey string) *DistributedLock {
	return &DistributedLock{
		store:         m.store,
		logger:        m.logger,
		lockKey:       lockKey,
		holderID:      uuid.New().String(),
		ttl:           m.defaultTTL,
		retryInterval: m.retryInterval,
		maxRetries:    m.maxRetries,
	}
}

// NewLockWithTTL creates a new DistributedLock with a custom TTL.
func (m *DistributedLockManager) NewLockWithTTL(lockKey string, ttl time.Duration) *DistributedLock {
	lock := m.NewLock(lockKey)
	lock.ttl = ttl
	return lock
}

// CleanupExpiredLocks removes all expired locks from the store.
// This can be called periodically to clean up stale locks.
func (m *DistributedLockManager) CleanupExpiredLocks(ctx context.Context) (int64, error) {
	return m.store.CleanupExpiredLocks(ctx)
}

// DistributedLock represents a distributed lock that can be acquired and released
// across multiple processes or instances.
type DistributedLock struct {
	store         LockStore
	logger        schemas.Logger
	lockKey       string
	holderID      string
	ttl           time.Duration
	retryInterval time.Duration
	maxRetries    int
	acquired      bool
}

// Lock acquires the lock, blocking until it's available or the context is cancelled.
// It will retry up to maxRetries times with retryInterval between attempts.
func (l *DistributedLock) Lock(ctx context.Context) error {
	for i := 0; i <= l.maxRetries; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		acquired, err := l.TryLock(ctx)
		if err != nil {
			return fmt.Errorf("error acquiring lock: %w", err)
		}

		if acquired {
			return nil
		}

		// Wait before retrying
		if i < l.maxRetries {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(l.retryInterval):
			}
		}
	}

	return ErrLockNotAcquired
}

// LockWithRetry acquires the lock, blocking until it's available or the context is cancelled.
// It will retry up to maxRetries times with retryInterval between attempts.
func (l *DistributedLock) LockWithRetry(ctx context.Context, maxRetries int) error {
	for i := 0; i <= maxRetries; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		acquired, err := l.TryLock(ctx)
		if err != nil {
			return fmt.Errorf("error acquiring lock: %w", err)
		}
		if acquired {
			return nil
		}
		// Wait before retrying
		if i < maxRetries {
			// Exponential backoff capped to avoid overflow (max 32s).
			exp := i
			if exp > 5 {
				exp = 5
			}
			backoff := time.Duration(1<<uint(exp)) * time.Second
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
		}
	}
	return ErrLockNotAcquired
}

// TryLock attempts to acquire the lock without blocking.
// Returns true if the lock was acquired, false if it's held by another process.
func (l *DistributedLock) TryLock(ctx context.Context) (bool, error) {
	// First, try to clean up any expired locks for this key
	if err := l.cleanupExpiredLock(ctx); err != nil {
		l.logger.Debug("error cleaning up expired lock: %v", err)
	}

	lock := &tables.TableDistributedLock{
		LockKey:   l.lockKey,
		HolderID:  l.holderID,
		ExpiresAt: time.Now().UTC().Add(l.ttl),
	}

	acquired, err := l.store.TryAcquireLock(ctx, lock)
	if err != nil {
		return false, fmt.Errorf("error trying to acquire lock: %w", err)
	}

	if acquired {
		l.acquired = true
		l.logger.Debug("acquired lock %s with holder %s", l.lockKey, l.holderID)
	}

	return acquired, nil
}

// Unlock releases the lock if it's held by this holder.
// Returns an error if the lock is not held by this holder.
func (l *DistributedLock) Unlock(ctx context.Context) error {
	if !l.acquired {
		return ErrLockNotHeld
	}

	released, err := l.store.ReleaseLock(ctx, l.lockKey, l.holderID)
	if err != nil {
		return fmt.Errorf("error releasing lock: %w", err)
	}

	if !released {
		l.acquired = false
		return ErrLockNotHeld
	}

	l.acquired = false
	l.logger.Debug("released lock %s", l.lockKey)
	return nil
}

// Extend extends the lock's TTL. This is useful for long-running operations
// that need to hold the lock longer than the initial TTL.
// Returns an error if the lock is not held by this holder or has expired.
// Only clears l.acquired when ErrLockNotHeld is returned; transient errors
// leave l.acquired untouched so Unlock() can still attempt a proper release.
func (l *DistributedLock) Extend(ctx context.Context) error {
	if !l.acquired {
		return ErrLockNotHeld
	}

	newExpiresAt := time.Now().UTC().Add(l.ttl)
	if err := l.store.UpdateLockExpiry(ctx, l.lockKey, l.holderID, newExpiresAt); err != nil {
		if errors.Is(err, ErrLockNotHeld) {
			// Lock definitively not held - clear local state
			l.acquired = false
		}
		// Otherwise leave l.acquired untouched for transient errors
		return fmt.Errorf("error extending lock: %w", err)
	}

	l.logger.Debug("extended lock %s to %v", l.lockKey, newExpiresAt)
	return nil
}

// IsHeld checks if the lock is currently held by this holder.
// Note: This checks the local state and the database state.
// Returns (false, error) on transient database errors without clearing l.acquired,
// allowing Unlock() to still attempt a proper release.
func (l *DistributedLock) IsHeld(ctx context.Context) (bool, error) {
	if !l.acquired {
		return false, nil
	}

	lock, err := l.store.GetLock(ctx, l.lockKey)
	if err != nil {
		// Transient error - can't confirm state, leave l.acquired untouched
		return false, fmt.Errorf("error checking lock: %w", err)
	}

	if lock == nil {
		// Lock doesn't exist - definitively not held
		l.acquired = false
		return false, nil
	}

	// Check if we're still the holder and the lock hasn't expired
	if lock.HolderID != l.holderID || time.Now().UTC().After(lock.ExpiresAt) {
		l.acquired = false
		return false, nil
	}

	return true, nil
}

// Key returns the lock key.
func (l *DistributedLock) Key() string {
	return l.lockKey
}

// HolderID returns the unique identifier for this lock holder.
func (l *DistributedLock) HolderID() string {
	return l.holderID
}

// cleanupExpiredLock atomically removes the lock if it has expired.
// This is called before attempting to acquire a lock.
func (l *DistributedLock) cleanupExpiredLock(ctx context.Context) error {
	cleaned, err := l.store.CleanupExpiredLockByKey(ctx, l.lockKey)
	if err != nil {
		return fmt.Errorf("error cleaning up expired lock: %w", err)
	}

	if cleaned {
		l.logger.Debug("cleaned up expired lock %s", l.lockKey)
	}

	return nil
}
