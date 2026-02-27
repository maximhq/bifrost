package configstore

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

// mockLogger implements schemas.Logger for testing
type mockLogger struct {
	debugMessages []string
	infoMessages  []string
	warnMessages  []string
	errorMessages []string
	mu            sync.Mutex
}

func newMockLogger() *mockLogger {
	return &mockLogger{
		debugMessages: make([]string, 0),
		infoMessages:  make([]string, 0),
		warnMessages:  make([]string, 0),
		errorMessages: make([]string, 0),
	}
}

func (l *mockLogger) Debug(msg string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.debugMessages = append(l.debugMessages, msg)
}

func (l *mockLogger) Info(msg string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.infoMessages = append(l.infoMessages, msg)
}

func (l *mockLogger) Warn(msg string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warnMessages = append(l.warnMessages, msg)
}

func (l *mockLogger) Error(msg string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.errorMessages = append(l.errorMessages, msg)
}

func (l *mockLogger) Fatal(msg string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.errorMessages = append(l.errorMessages, msg)
}

func (l *mockLogger) SetLevel(level schemas.LogLevel) {}

func (l *mockLogger) SetOutputType(outputType schemas.LoggerOutputType) {}
func (l *mockLogger) LogHTTPRequest(level schemas.LogLevel, msg string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

// setupLockTestStore creates a test RDBConfigStore with SQLite in-memory database
func setupLockTestStore(t *testing.T) *RDBConfigStore {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err, "Failed to create test database")

	// Force single connection to ensure all operations use the same in-memory database.
	// SQLite in-memory with ":memory:" creates separate DBs per connection, so we must
	// limit to one connection to preserve distributed lock semantics in concurrent tests.
	sqlDB, err := db.DB()
	require.NoError(t, err, "Failed to get underlying sql.DB")
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	err = db.AutoMigrate(&tables.TableDistributedLock{})
	require.NoError(t, err, "Failed to migrate test database")

	return &RDBConfigStore{
		db:     db,
		logger: newMockLogger(),
	}
}

// =============================================================================
// LockStore Interface Tests (RDBConfigStore implementation)
// =============================================================================

func TestTryAcquireLock_Success(t *testing.T) {
	store := setupLockTestStore(t)
	ctx := context.Background()

	lock := &tables.TableDistributedLock{
		LockKey:   "test-lock",
		HolderID:  "holder-1",
		ExpiresAt: time.Now().UTC().Add(30 * time.Second),
	}

	acquired, err := store.TryAcquireLock(ctx, lock)
	require.NoError(t, err)
	assert.True(t, acquired, "Lock should be acquired")

	// Verify lock exists in database
	storedLock, err := store.GetLock(ctx, "test-lock")
	require.NoError(t, err)
	assert.NotNil(t, storedLock)
	assert.Equal(t, "holder-1", storedLock.HolderID)
}

func TestTryAcquireLock_AlreadyHeld(t *testing.T) {
	store := setupLockTestStore(t)
	ctx := context.Background()

	// First holder acquires the lock
	lock1 := &tables.TableDistributedLock{
		LockKey:   "test-lock",
		HolderID:  "holder-1",
		ExpiresAt: time.Now().UTC().Add(30 * time.Second),
	}
	acquired, err := store.TryAcquireLock(ctx, lock1)
	require.NoError(t, err)
	assert.True(t, acquired)

	// Second holder tries to acquire the same lock
	lock2 := &tables.TableDistributedLock{
		LockKey:   "test-lock",
		HolderID:  "holder-2",
		ExpiresAt: time.Now().UTC().Add(30 * time.Second),
	}
	acquired, err = store.TryAcquireLock(ctx, lock2)
	require.NoError(t, err)
	assert.False(t, acquired, "Lock should not be acquired by second holder")

	// Verify original holder still owns the lock
	storedLock, err := store.GetLock(ctx, "test-lock")
	require.NoError(t, err)
	assert.Equal(t, "holder-1", storedLock.HolderID)
}

func TestTryAcquireLock_MultipleLocks(t *testing.T) {
	store := setupLockTestStore(t)
	ctx := context.Background()

	locks := []string{"lock-a", "lock-b", "lock-c"}

	for _, lockKey := range locks {
		lock := &tables.TableDistributedLock{
			LockKey:   lockKey,
			HolderID:  "holder-1",
			ExpiresAt: time.Now().UTC().Add(30 * time.Second),
		}
		acquired, err := store.TryAcquireLock(ctx, lock)
		require.NoError(t, err)
		assert.True(t, acquired, "Lock %s should be acquired", lockKey)
	}

	// Verify all locks exist
	for _, lockKey := range locks {
		storedLock, err := store.GetLock(ctx, lockKey)
		require.NoError(t, err)
		assert.NotNil(t, storedLock)
	}
}

func TestGetLock_NotFound(t *testing.T) {
	store := setupLockTestStore(t)
	ctx := context.Background()

	lock, err := store.GetLock(ctx, "non-existent-lock")
	require.NoError(t, err)
	assert.Nil(t, lock, "Should return nil for non-existent lock")
}

func TestUpdateLockExpiry_Success(t *testing.T) {
	store := setupLockTestStore(t)
	ctx := context.Background()

	// Create initial lock
	lock := &tables.TableDistributedLock{
		LockKey:   "test-lock",
		HolderID:  "holder-1",
		ExpiresAt: time.Now().UTC().Add(30 * time.Second),
	}
	acquired, err := store.TryAcquireLock(ctx, lock)
	require.NoError(t, err)
	require.True(t, acquired)

	// Extend expiry
	newExpiry := time.Now().UTC().Add(60 * time.Second)
	err = store.UpdateLockExpiry(ctx, "test-lock", "holder-1", newExpiry)
	require.NoError(t, err)

	// Verify expiry was updated
	storedLock, err := store.GetLock(ctx, "test-lock")
	require.NoError(t, err)
	assert.WithinDuration(t, newExpiry, storedLock.ExpiresAt, time.Second)
}

func TestUpdateLockExpiry_WrongHolder(t *testing.T) {
	store := setupLockTestStore(t)
	ctx := context.Background()

	// Create lock with holder-1
	lock := &tables.TableDistributedLock{
		LockKey:   "test-lock",
		HolderID:  "holder-1",
		ExpiresAt: time.Now().UTC().Add(30 * time.Second),
	}
	acquired, err := store.TryAcquireLock(ctx, lock)
	require.NoError(t, err)
	require.True(t, acquired)

	// Try to extend with wrong holder
	newExpiry := time.Now().UTC().Add(60 * time.Second)
	err = store.UpdateLockExpiry(ctx, "test-lock", "holder-2", newExpiry)
	assert.ErrorIs(t, err, ErrLockNotHeld)
}

func TestUpdateLockExpiry_ExpiredLock(t *testing.T) {
	store := setupLockTestStore(t)
	ctx := context.Background()

	// Create lock that's already expired
	lock := &tables.TableDistributedLock{
		LockKey:   "test-lock",
		HolderID:  "holder-1",
		ExpiresAt: time.Now().UTC().Add(-1 * time.Second),
	}
	// Directly insert the expired lock
	err := store.db.Create(lock).Error
	require.NoError(t, err)

	// Try to extend expired lock
	newExpiry := time.Now().UTC().Add(60 * time.Second)
	err = store.UpdateLockExpiry(ctx, "test-lock", "holder-1", newExpiry)
	assert.ErrorIs(t, err, ErrLockNotHeld)
}

func TestReleaseLock_Success(t *testing.T) {
	store := setupLockTestStore(t)
	ctx := context.Background()

	// Create lock
	lock := &tables.TableDistributedLock{
		LockKey:   "test-lock",
		HolderID:  "holder-1",
		ExpiresAt: time.Now().UTC().Add(30 * time.Second),
	}
	acquired, err := store.TryAcquireLock(ctx, lock)
	require.NoError(t, err)
	require.True(t, acquired)

	// Release lock
	released, err := store.ReleaseLock(ctx, "test-lock", "holder-1")
	require.NoError(t, err)
	assert.True(t, released)

	// Verify lock is gone
	storedLock, err := store.GetLock(ctx, "test-lock")
	require.NoError(t, err)
	assert.Nil(t, storedLock)
}

func TestReleaseLock_WrongHolder(t *testing.T) {
	store := setupLockTestStore(t)
	ctx := context.Background()

	// Create lock with holder-1
	lock := &tables.TableDistributedLock{
		LockKey:   "test-lock",
		HolderID:  "holder-1",
		ExpiresAt: time.Now().UTC().Add(30 * time.Second),
	}
	acquired, err := store.TryAcquireLock(ctx, lock)
	require.NoError(t, err)
	require.True(t, acquired)

	// Try to release with wrong holder
	released, err := store.ReleaseLock(ctx, "test-lock", "holder-2")
	require.NoError(t, err)
	assert.False(t, released, "Should not release lock with wrong holder")

	// Verify lock still exists
	storedLock, err := store.GetLock(ctx, "test-lock")
	require.NoError(t, err)
	assert.NotNil(t, storedLock)
	assert.Equal(t, "holder-1", storedLock.HolderID)
}

func TestReleaseLock_NotExists(t *testing.T) {
	store := setupLockTestStore(t)
	ctx := context.Background()

	released, err := store.ReleaseLock(ctx, "non-existent-lock", "holder-1")
	require.NoError(t, err)
	assert.False(t, released)
}

func TestCleanupExpiredLocks_Success(t *testing.T) {
	store := setupLockTestStore(t)
	ctx := context.Background()

	// Create some expired locks
	expiredLocks := []tables.TableDistributedLock{
		{LockKey: "expired-1", HolderID: "h1", ExpiresAt: time.Now().UTC().Add(-1 * time.Minute)},
		{LockKey: "expired-2", HolderID: "h2", ExpiresAt: time.Now().UTC().Add(-2 * time.Minute)},
	}

	// Create some valid locks
	validLocks := []tables.TableDistributedLock{
		{LockKey: "valid-1", HolderID: "h3", ExpiresAt: time.Now().UTC().Add(30 * time.Second)},
		{LockKey: "valid-2", HolderID: "h4", ExpiresAt: time.Now().UTC().Add(60 * time.Second)},
	}

	for _, l := range expiredLocks {
		err := store.db.Create(&l).Error
		require.NoError(t, err)
	}
	for _, l := range validLocks {
		err := store.db.Create(&l).Error
		require.NoError(t, err)
	}

	// Cleanup expired locks
	count, err := store.CleanupExpiredLocks(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(2), count, "Should cleanup 2 expired locks")

	// Verify expired locks are gone
	for _, l := range expiredLocks {
		lock, err := store.GetLock(ctx, l.LockKey)
		require.NoError(t, err)
		assert.Nil(t, lock)
	}

	// Verify valid locks still exist
	for _, l := range validLocks {
		lock, err := store.GetLock(ctx, l.LockKey)
		require.NoError(t, err)
		assert.NotNil(t, lock)
	}
}

func TestCleanupExpiredLocks_NoExpired(t *testing.T) {
	store := setupLockTestStore(t)
	ctx := context.Background()

	// Create only valid locks
	lock := &tables.TableDistributedLock{
		LockKey:   "valid-lock",
		HolderID:  "holder-1",
		ExpiresAt: time.Now().UTC().Add(30 * time.Second),
	}
	_, err := store.TryAcquireLock(ctx, lock)
	require.NoError(t, err)

	count, err := store.CleanupExpiredLocks(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)
}

func TestCleanupExpiredLockByKey_Success(t *testing.T) {
	store := setupLockTestStore(t)
	ctx := context.Background()

	// Create expired lock
	lock := tables.TableDistributedLock{
		LockKey:   "expired-lock",
		HolderID:  "holder-1",
		ExpiresAt: time.Now().UTC().Add(-1 * time.Minute),
	}
	err := store.db.Create(&lock).Error
	require.NoError(t, err)

	// Cleanup specific expired lock
	cleaned, err := store.CleanupExpiredLockByKey(ctx, "expired-lock")
	require.NoError(t, err)
	assert.True(t, cleaned)

	// Verify lock is gone
	storedLock, err := store.GetLock(ctx, "expired-lock")
	require.NoError(t, err)
	assert.Nil(t, storedLock)
}

func TestCleanupExpiredLockByKey_NotExpired(t *testing.T) {
	store := setupLockTestStore(t)
	ctx := context.Background()

	// Create valid lock
	lock := &tables.TableDistributedLock{
		LockKey:   "valid-lock",
		HolderID:  "holder-1",
		ExpiresAt: time.Now().UTC().Add(30 * time.Second),
	}
	_, err := store.TryAcquireLock(ctx, lock)
	require.NoError(t, err)

	// Try to cleanup non-expired lock
	cleaned, err := store.CleanupExpiredLockByKey(ctx, "valid-lock")
	require.NoError(t, err)
	assert.False(t, cleaned, "Should not cleanup non-expired lock")

	// Verify lock still exists
	storedLock, err := store.GetLock(ctx, "valid-lock")
	require.NoError(t, err)
	assert.NotNil(t, storedLock)
}

func TestCleanupExpiredLockByKey_NotExists(t *testing.T) {
	store := setupLockTestStore(t)
	ctx := context.Background()

	cleaned, err := store.CleanupExpiredLockByKey(ctx, "non-existent")
	require.NoError(t, err)
	assert.False(t, cleaned)
}

// =============================================================================
// DistributedLockManager Tests
// =============================================================================

func TestNewDistributedLockManager_DefaultOptions(t *testing.T) {
	store := setupLockTestStore(t)
	logger := newMockLogger()

	manager := NewDistributedLockManager(store, logger)

	assert.NotNil(t, manager)
	assert.Equal(t, DefaultLockTTL, manager.defaultTTL)
	assert.Equal(t, DefaultRetryInterval, manager.retryInterval)
	assert.Equal(t, DefaultMaxRetries, manager.maxRetries)
}

func TestNewDistributedLockManager_CustomOptions(t *testing.T) {
	store := setupLockTestStore(t)
	logger := newMockLogger()

	customTTL := 60 * time.Second
	customRetryInterval := 200 * time.Millisecond
	customMaxRetries := 50

	manager := NewDistributedLockManager(
		store,
		logger,
		WithDefaultTTL(customTTL),
		WithRetryInterval(customRetryInterval),
		WithMaxRetries(customMaxRetries),
	)

	assert.Equal(t, customTTL, manager.defaultTTL)
	assert.Equal(t, customRetryInterval, manager.retryInterval)
	assert.Equal(t, customMaxRetries, manager.maxRetries)
}

func TestDistributedLockManager_NewLock(t *testing.T) {
	store := setupLockTestStore(t)
	logger := newMockLogger()
	manager := NewDistributedLockManager(store, logger)

	lock, err := manager.NewLock("my-lock")
	require.NoError(t, err)

	assert.NotNil(t, lock)
	assert.Equal(t, "my-lock", lock.Key())
	assert.NotEmpty(t, lock.HolderID())
	assert.Equal(t, DefaultLockTTL, lock.ttl)
}

func TestDistributedLockManager_NewLockWithTTL(t *testing.T) {
	store := setupLockTestStore(t)
	logger := newMockLogger()
	manager := NewDistributedLockManager(store, logger)

	customTTL := 5 * time.Minute
	lock, err := manager.NewLockWithTTL("my-lock", customTTL)
	require.NoError(t, err)

	assert.Equal(t, customTTL, lock.ttl)
}

func TestDistributedLockManager_CleanupExpiredLocks(t *testing.T) {
	store := setupLockTestStore(t)
	logger := newMockLogger()
	manager := NewDistributedLockManager(store, logger)
	ctx := context.Background()

	// Create expired lock directly
	lock := tables.TableDistributedLock{
		LockKey:   "expired-lock",
		HolderID:  "holder-1",
		ExpiresAt: time.Now().UTC().Add(-1 * time.Minute),
	}
	err := store.db.Create(&lock).Error
	require.NoError(t, err)

	count, err := manager.CleanupExpiredLocks(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

// =============================================================================
// DistributedLock Tests
// =============================================================================

func TestDistributedLock_TryLock_Success(t *testing.T) {
	store := setupLockTestStore(t)
	logger := newMockLogger()
	manager := NewDistributedLockManager(store, logger)
	ctx := context.Background()

	lock, err := manager.NewLock("test-lock")
	require.NoError(t, err)

	acquired, err := lock.TryLock(ctx)
	require.NoError(t, err)
	assert.True(t, acquired)
}

func TestDistributedLock_TryLock_AlreadyHeld(t *testing.T) {
	store := setupLockTestStore(t)
	logger := newMockLogger()
	manager := NewDistributedLockManager(store, logger)
	ctx := context.Background()

	lock1, err := manager.NewLock("test-lock")
	require.NoError(t, err)
	lock2, err := manager.NewLock("test-lock")
	require.NoError(t, err)

	// First lock succeeds
	acquired, err := lock1.TryLock(ctx)
	require.NoError(t, err)
	assert.True(t, acquired)

	// Second lock fails
	acquired, err = lock2.TryLock(ctx)
	require.NoError(t, err)
	assert.False(t, acquired)
}

func TestDistributedLock_TryLock_CleansUpExpired(t *testing.T) {
	store := setupLockTestStore(t)
	logger := newMockLogger()
	manager := NewDistributedLockManager(store, logger)
	ctx := context.Background()

	// Create expired lock directly in database
	expiredLock := tables.TableDistributedLock{
		LockKey:   "test-lock",
		HolderID:  "old-holder",
		ExpiresAt: time.Now().UTC().Add(-1 * time.Minute),
	}
	err := store.db.Create(&expiredLock).Error
	require.NoError(t, err)

	// New lock should be able to acquire after cleanup
	lock, err := manager.NewLock("test-lock")
	require.NoError(t, err)
	acquired, err := lock.TryLock(ctx)
	require.NoError(t, err)
	assert.True(t, acquired, "Should acquire lock after expired cleanup")
}

func TestDistributedLock_Lock_Success(t *testing.T) {
	store := setupLockTestStore(t)
	logger := newMockLogger()
	manager := NewDistributedLockManager(store, logger,
		WithRetryInterval(10*time.Millisecond),
		WithMaxRetries(3),
	)
	ctx := context.Background()

	lock, err := manager.NewLock("test-lock")
	require.NoError(t, err)

	err = lock.Lock(ctx)
	require.NoError(t, err)

	// Verify lock is held
	held, err := lock.IsHeld(ctx)
	require.NoError(t, err)
	assert.True(t, held)
}

func TestDistributedLock_Lock_ContextCancelled(t *testing.T) {
	store := setupLockTestStore(t)
	logger := newMockLogger()
	manager := NewDistributedLockManager(store, logger)

	// First acquire the lock
	lock1, err := manager.NewLock("test-lock")
	require.NoError(t, err)
	ctx := context.Background()
	_, err = lock1.TryLock(ctx)
	require.NoError(t, err)

	// Try to acquire with cancelled context
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	lock2, err := manager.NewLock("test-lock")
	require.NoError(t, err)
	err = lock2.Lock(cancelCtx)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestDistributedLock_Lock_Timeout(t *testing.T) {
	store := setupLockTestStore(t)
	logger := newMockLogger()
	manager := NewDistributedLockManager(store, logger,
		WithRetryInterval(10*time.Millisecond),
		WithMaxRetries(3),
	)
	ctx := context.Background()

	// First lock holds
	lock1, err := manager.NewLock("test-lock")
	require.NoError(t, err)
	_, err = lock1.TryLock(ctx)
	require.NoError(t, err)

	// Second lock should fail after retries
	lock2, err := manager.NewLock("test-lock")
	require.NoError(t, err)
	err = lock2.Lock(ctx)
	assert.ErrorIs(t, err, ErrLockNotAcquired)
}

func TestDistributedLock_LockWithRetry_ExponentialBackoff(t *testing.T) {
	store := setupLockTestStore(t)
	logger := newMockLogger()
	manager := NewDistributedLockManager(store, logger)
	ctx := context.Background()

	// First lock holds
	lock1, err := manager.NewLock("test-lock")
	require.NoError(t, err)
	_, err = lock1.TryLock(ctx)
	require.NoError(t, err)

	// Second lock with limited retries
	lock2, err := manager.NewLock("test-lock")
	require.NoError(t, err)
	start := time.Now()
	err = lock2.LockWithRetry(ctx, 2)
	elapsed := time.Since(start)

	assert.ErrorIs(t, err, ErrLockNotAcquired)
	// With exponential backoff: 1s + 2s = 3s minimum
	assert.True(t, elapsed >= 3*time.Second, "Expected at least 3s delay, got %v", elapsed)
}

func TestDistributedLock_Unlock_Success(t *testing.T) {
	store := setupLockTestStore(t)
	logger := newMockLogger()
	manager := NewDistributedLockManager(store, logger)
	ctx := context.Background()

	lock, err := manager.NewLock("test-lock")
	require.NoError(t, err)
	_, err = lock.TryLock(ctx)
	require.NoError(t, err)

	err = lock.Unlock(ctx)
	require.NoError(t, err)

	// Verify lock is released
	storedLock, err := store.GetLock(ctx, "test-lock")
	require.NoError(t, err)
	assert.Nil(t, storedLock)
}

func TestDistributedLock_Unlock_NotHeld(t *testing.T) {
	store := setupLockTestStore(t)
	logger := newMockLogger()
	manager := NewDistributedLockManager(store, logger)
	ctx := context.Background()

	lock, err := manager.NewLock("test-lock")
	require.NoError(t, err)
	// Never acquired

	err = lock.Unlock(ctx)
	assert.ErrorIs(t, err, ErrLockNotHeld)
}

func TestDistributedLock_Unlock_AlreadyReleased(t *testing.T) {
	store := setupLockTestStore(t)
	logger := newMockLogger()
	manager := NewDistributedLockManager(store, logger)
	ctx := context.Background()

	lock, err := manager.NewLock("test-lock")
	require.NoError(t, err)
	_, err = lock.TryLock(ctx)
	require.NoError(t, err)

	// First unlock succeeds
	err = lock.Unlock(ctx)
	require.NoError(t, err)

	// Second unlock fails
	err = lock.Unlock(ctx)
	assert.ErrorIs(t, err, ErrLockNotHeld)
}

func TestDistributedLock_Extend_Success(t *testing.T) {
	store := setupLockTestStore(t)
	logger := newMockLogger()
	manager := NewDistributedLockManager(store, logger, WithDefaultTTL(10*time.Second))
	ctx := context.Background()

	lock, err := manager.NewLock("test-lock")
	require.NoError(t, err)
	_, err = lock.TryLock(ctx)
	require.NoError(t, err)

	// Get original expiry
	storedLock, err := store.GetLock(ctx, "test-lock")
	require.NoError(t, err)
	originalExpiry := storedLock.ExpiresAt

	// Wait a bit and extend
	time.Sleep(100 * time.Millisecond)
	err = lock.Extend(ctx)
	require.NoError(t, err)

	// Verify expiry was extended
	storedLock, err = store.GetLock(ctx, "test-lock")
	require.NoError(t, err)
	assert.True(t, storedLock.ExpiresAt.After(originalExpiry))
}

func TestDistributedLock_Extend_NotHeld(t *testing.T) {
	store := setupLockTestStore(t)
	logger := newMockLogger()
	manager := NewDistributedLockManager(store, logger)
	ctx := context.Background()

	lock, err := manager.NewLock("test-lock")
	require.NoError(t, err)
	// Never acquired

	err = lock.Extend(ctx)
	assert.ErrorIs(t, err, ErrLockNotHeld)
}

func TestDistributedLock_Extend_StolenLock(t *testing.T) {
	store := setupLockTestStore(t)
	logger := newMockLogger()
	manager := NewDistributedLockManager(store, logger)
	ctx := context.Background()

	lock, err := manager.NewLock("test-lock")
	require.NoError(t, err)
	_, err = lock.TryLock(ctx)
	require.NoError(t, err)

	// Simulate lock being stolen by another process
	err = store.db.Model(&tables.TableDistributedLock{}).
		Where("lock_key = ?", "test-lock").
		Update("holder_id", "another-holder").Error
	require.NoError(t, err)

	// Extend should fail
	err = lock.Extend(ctx)
	assert.Error(t, err)
}

func TestDistributedLock_IsHeld_True(t *testing.T) {
	store := setupLockTestStore(t)
	logger := newMockLogger()
	manager := NewDistributedLockManager(store, logger)
	ctx := context.Background()

	lock, err := manager.NewLock("test-lock")
	require.NoError(t, err)
	_, err = lock.TryLock(ctx)
	require.NoError(t, err)

	held, err := lock.IsHeld(ctx)
	require.NoError(t, err)
	assert.True(t, held)
}

func TestDistributedLock_IsHeld_NotAcquired(t *testing.T) {
	store := setupLockTestStore(t)
	logger := newMockLogger()
	manager := NewDistributedLockManager(store, logger)
	ctx := context.Background()

	lock, err := manager.NewLock("test-lock")
	require.NoError(t, err)
	// Never acquired

	held, err := lock.IsHeld(ctx)
	require.NoError(t, err)
	assert.False(t, held)
}

func TestDistributedLock_IsHeld_Expired(t *testing.T) {
	store := setupLockTestStore(t)
	logger := newMockLogger()
	manager := NewDistributedLockManager(store, logger, WithDefaultTTL(50*time.Millisecond))
	ctx := context.Background()

	lock, err := manager.NewLock("test-lock")
	require.NoError(t, err)
	_, err = lock.TryLock(ctx)
	require.NoError(t, err)

	// Wait for lock to expire
	time.Sleep(100 * time.Millisecond)

	held, err := lock.IsHeld(ctx)
	require.NoError(t, err)
	assert.False(t, held, "Lock should not be held after expiry")
}

func TestDistributedLock_IsHeld_StolenByAnotherHolder(t *testing.T) {
	store := setupLockTestStore(t)
	logger := newMockLogger()
	manager := NewDistributedLockManager(store, logger)
	ctx := context.Background()

	lock, err := manager.NewLock("test-lock")
	require.NoError(t, err)
	_, err = lock.TryLock(ctx)
	require.NoError(t, err)

	// Simulate lock being stolen by another process
	err = store.db.Model(&tables.TableDistributedLock{}).
		Where("lock_key = ?", "test-lock").
		Update("holder_id", "another-holder").Error
	require.NoError(t, err)

	held, err := lock.IsHeld(ctx)
	require.NoError(t, err)
	assert.False(t, held)
}

func TestDistributedLock_IsHeld_DeletedFromDB(t *testing.T) {
	store := setupLockTestStore(t)
	logger := newMockLogger()
	manager := NewDistributedLockManager(store, logger)
	ctx := context.Background()

	lock, err := manager.NewLock("test-lock")
	require.NoError(t, err)
	_, err = lock.TryLock(ctx)
	require.NoError(t, err)

	// Delete lock directly from database
	err = store.db.Where("lock_key = ?", "test-lock").Delete(&tables.TableDistributedLock{}).Error
	require.NoError(t, err)

	held, err := lock.IsHeld(ctx)
	require.NoError(t, err)
	assert.False(t, held)
}

// =============================================================================
// Concurrent Access Tests
// =============================================================================

func TestDistributedLock_ConcurrentAcquire(t *testing.T) {
	store := setupLockTestStore(t)
	logger := newMockLogger()
	manager := NewDistributedLockManager(store, logger,
		WithRetryInterval(10*time.Millisecond),
		WithMaxRetries(5),
	)
	ctx := context.Background()

	const numGoroutines = 10
	successCount := 0
	var mu sync.Mutex
	var wg sync.WaitGroup

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lock, err := manager.NewLock("contended-lock")
			if err != nil {
				return
			}
			acquired, err := lock.TryLock(ctx)
			if err == nil && acquired {
				mu.Lock()
				successCount++
				mu.Unlock()
			}
		}()
	}

	wg.Wait()

	// Only one goroutine should have acquired the lock
	assert.Equal(t, 1, successCount, "Exactly one goroutine should acquire the lock")
}

func TestDistributedLock_ConcurrentLockUnlock(t *testing.T) {
	store := setupLockTestStore(t)
	logger := newMockLogger()
	manager := NewDistributedLockManager(store, logger,
		WithRetryInterval(50*time.Millisecond),
		WithMaxRetries(100),
		WithDefaultTTL(5*time.Second),
	)
	ctx := context.Background()

	const numGoroutines = 5
	counter := 0
	var mu sync.Mutex
	var wg sync.WaitGroup

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			lock, err := manager.NewLock("counter-lock")
			if err != nil {
				return
			}

			// Each goroutine tries to increment the counter with lock protection
			err = lock.Lock(ctx)
			if err != nil {
				return
			}

			mu.Lock()
			counter++
			mu.Unlock()

			// Simulate some work
			time.Sleep(10 * time.Millisecond)

			_ = lock.Unlock(ctx)
		}(i)
	}

	wg.Wait()

	// All goroutines should have incremented the counter
	assert.Equal(t, numGoroutines, counter, "All goroutines should complete")
}

func TestDistributedLock_MultipleLocksPerManager(t *testing.T) {
	store := setupLockTestStore(t)
	logger := newMockLogger()
	manager := NewDistributedLockManager(store, logger)
	ctx := context.Background()

	const numLocks = 10
	var wg sync.WaitGroup
	errCh := make(chan error, numLocks*2) // Buffer for potential TryLock and Unlock errors

	for i := 0; i < numLocks; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			lockKey := fmt.Sprintf("lock-%d", id)
			lock, err := manager.NewLock(lockKey)
			if err != nil {
				errCh <- fmt.Errorf("lock %s NewLock error: %w", lockKey, err)
				return
			}

			acquired, err := lock.TryLock(ctx)
			if err != nil {
				errCh <- fmt.Errorf("lock %s TryLock error: %w", lockKey, err)
				return
			}
			if !acquired {
				errCh <- fmt.Errorf("lock %s should be acquired", lockKey)
				return
			}

			time.Sleep(10 * time.Millisecond)

			if err := lock.Unlock(ctx); err != nil {
				errCh <- fmt.Errorf("lock %s Unlock error: %w", lockKey, err)
				return
			}
		}(i)
	}

	wg.Wait()
	close(errCh)

	// Collect and report any errors from goroutines
	var errs []error
	for err := range errCh {
		errs = append(errs, err)
	}
	require.Empty(t, errs, "goroutines reported errors: %v", errs)
}

// =============================================================================
// Edge Case Tests
// =============================================================================

func TestDistributedLock_EmptyLockKey(t *testing.T) {
	store := setupLockTestStore(t)
	logger := newMockLogger()
	manager := NewDistributedLockManager(store, logger)

	lock, err := manager.NewLock("")

	assert.Nil(t, lock, "Empty lock key should return nil lock")
	assert.ErrorIs(t, err, ErrEmptyLockKey, "Empty lock key should return ErrEmptyLockKey")
}

func TestDistributedLock_LongLockKey(t *testing.T) {
	store := setupLockTestStore(t)
	logger := newMockLogger()
	manager := NewDistributedLockManager(store, logger)
	ctx := context.Background()

	// Create a 255 character lock key (max size per schema)
	longKey := ""
	for i := 0; i < 255; i++ {
		longKey += "a"
	}

	lock, err := manager.NewLock(longKey)
	require.NoError(t, err)

	acquired, err := lock.TryLock(ctx)
	require.NoError(t, err)
	assert.True(t, acquired)
}

func TestDistributedLock_SpecialCharactersInKey(t *testing.T) {
	store := setupLockTestStore(t)
	logger := newMockLogger()
	manager := NewDistributedLockManager(store, logger)
	ctx := context.Background()

	specialKeys := []string{
		"lock:with:colons",
		"lock/with/slashes",
		"lock-with-dashes",
		"lock_with_underscores",
		"lock.with.dots",
		"lock with spaces",
		"lock\twith\ttabs",
	}

	for _, key := range specialKeys {
		lock, err := manager.NewLock(key)
		require.NoError(t, err, "Key: %s", key)
		acquired, err := lock.TryLock(ctx)
		require.NoError(t, err, "Key: %s", key)
		assert.True(t, acquired, "Lock with key %s should be acquired", key)

		err = lock.Unlock(ctx)
		require.NoError(t, err)
	}
}

func TestDistributedLock_ZeroTTL(t *testing.T) {
	store := setupLockTestStore(t)
	logger := newMockLogger()
	manager := NewDistributedLockManager(store, logger, WithDefaultTTL(0))
	ctx := context.Background()

	lock, err := manager.NewLock("zero-ttl-lock")
	require.NoError(t, err)

	// Zero TTL should still work but lock will be immediately expired
	acquired, err := lock.TryLock(ctx)
	require.NoError(t, err)
	assert.True(t, acquired)

	// Lock should immediately appear not held due to zero TTL
	held, err := lock.IsHeld(ctx)
	require.NoError(t, err)
	assert.False(t, held, "Zero TTL lock should not be held")
}

func TestDistributedLock_VeryShortTTL(t *testing.T) {
	store := setupLockTestStore(t)
	logger := newMockLogger()
	manager := NewDistributedLockManager(store, logger, WithDefaultTTL(1*time.Millisecond))
	ctx := context.Background()

	lock, err := manager.NewLock("short-ttl-lock")
	require.NoError(t, err)

	acquired, err := lock.TryLock(ctx)
	require.NoError(t, err)
	assert.True(t, acquired)

	// Wait for TTL to expire
	time.Sleep(10 * time.Millisecond)

	// Another lock should be able to acquire
	lock2, err := manager.NewLock("short-ttl-lock")
	require.NoError(t, err)
	acquired, err = lock2.TryLock(ctx)
	require.NoError(t, err)
	assert.True(t, acquired, "Should acquire lock after TTL expires")
}

func TestDistributedLock_ReacquireAfterUnlock(t *testing.T) {
	store := setupLockTestStore(t)
	logger := newMockLogger()
	manager := NewDistributedLockManager(store, logger)
	ctx := context.Background()

	lock, err := manager.NewLock("test-lock")
	require.NoError(t, err)

	// First acquire
	acquired, err := lock.TryLock(ctx)
	require.NoError(t, err)
	assert.True(t, acquired)

	// Release
	err = lock.Unlock(ctx)
	require.NoError(t, err)

	// Same lock instance should NOT be able to reacquire (new holder ID needed)
	// But a new lock should work
	lock2, err := manager.NewLock("test-lock")
	require.NoError(t, err)
	acquired, err = lock2.TryLock(ctx)
	require.NoError(t, err)
	assert.True(t, acquired, "New lock instance should acquire after release")
}

func TestDistributedLock_ExtendMultipleTimes(t *testing.T) {
	store := setupLockTestStore(t)
	logger := newMockLogger()
	manager := NewDistributedLockManager(store, logger, WithDefaultTTL(100*time.Millisecond))
	ctx := context.Background()

	lock, err := manager.NewLock("test-lock")
	require.NoError(t, err)
	_, err = lock.TryLock(ctx)
	require.NoError(t, err)

	// Extend multiple times
	for i := 0; i < 5; i++ {
		time.Sleep(20 * time.Millisecond)
		err = lock.Extend(ctx)
		require.NoError(t, err, "Extension %d failed", i+1)
	}

	// Lock should still be held
	held, err := lock.IsHeld(ctx)
	require.NoError(t, err)
	assert.True(t, held)
}

// =============================================================================
// Constants and Defaults Tests
// =============================================================================

func TestConstants(t *testing.T) {
	assert.Equal(t, 30*time.Second, DefaultLockTTL)
	assert.Equal(t, 100*time.Millisecond, DefaultRetryInterval)
	assert.Equal(t, 100, DefaultMaxRetries)
	assert.Equal(t, 5*time.Minute, DefaultCleanupInterval)
}

func TestErrors(t *testing.T) {
	assert.Equal(t, "failed to acquire lock", ErrLockNotAcquired.Error())
	assert.Equal(t, "lock not held by this holder", ErrLockNotHeld.Error())
	assert.Equal(t, "lock has expired", ErrLockExpired.Error())
	assert.Equal(t, "empty lock key", ErrEmptyLockKey.Error())
}

// =============================================================================
// Key and HolderID Accessor Tests
// =============================================================================

func TestDistributedLock_Key(t *testing.T) {
	store := setupLockTestStore(t)
	logger := newMockLogger()
	manager := NewDistributedLockManager(store, logger)

	lock, err := manager.NewLock("my-unique-lock")
	require.NoError(t, err)
	assert.Equal(t, "my-unique-lock", lock.Key())
}

func TestDistributedLock_HolderID(t *testing.T) {
	store := setupLockTestStore(t)
	logger := newMockLogger()
	manager := NewDistributedLockManager(store, logger)

	lock1, err := manager.NewLock("lock-1")
	require.NoError(t, err)
	lock2, err := manager.NewLock("lock-2")
	require.NoError(t, err)

	// Each lock should have a unique holder ID
	assert.NotEmpty(t, lock1.HolderID())
	assert.NotEmpty(t, lock2.HolderID())
	assert.NotEqual(t, lock1.HolderID(), lock2.HolderID())
}
