package lib

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// governanceSyncPostgresDSN is the local Postgres the framework's Postgres tests use.
const governanceSyncPostgresDSN = "host=localhost user=bifrost password=bifrost_password dbname=bifrost port=5432 sslmode=disable"

// postgresGovernanceStore is a mock config store backed by a real Postgres
// pool, which is all the governance sync lock reads from the store.
type postgresGovernanceStore struct {
	*MockConfigStore
	db *gorm.DB
}

// DB returns the Postgres pool standing in for one replica's config store.
func (s *postgresGovernanceStore) DB() *gorm.DB { return s.db }

// openGovernanceSyncReplica opens a separate Postgres pool, as a separate
// replica would, or skips the test when no local Postgres is reachable.
func openGovernanceSyncReplica(t *testing.T) *postgresGovernanceStore {
	t.Helper()
	db, err := gorm.Open(postgres.Open(governanceSyncPostgresDSN), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err == nil {
		err = db.Exec("SELECT 1").Error
	}
	if err != nil {
		t.Skipf("postgres not available: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return &postgresGovernanceStore{MockConfigStore: NewMockConfigStore(), db: db}
}

// TestWithGovernanceSyncLock_SerializesReplicas verifies replicas starting
// together never run the governance reconcile at the same time: the overlap
// that let them deadlock on, and time out behind, each other's row locks.
func TestWithGovernanceSyncLock_SerializesReplicas(t *testing.T) {
	initTestLogger()
	const replicas = 4
	stores := make([]*postgresGovernanceStore, replicas)
	for i := range stores {
		stores[i] = openGovernanceSyncReplica(t)
	}

	var active, maxActive, completed atomic.Int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, replicas)
	for _, store := range stores {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- withGovernanceSyncLock(context.Background(), store, func() {
				now := active.Add(1)
				for {
					seen := maxActive.Load()
					if now <= seen || maxActive.CompareAndSwap(seen, now) {
						break
					}
				}
				time.Sleep(200 * time.Millisecond)
				active.Add(-1)
				completed.Add(1)
			})
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	assert.Equal(t, int32(replicas), completed.Load(), "every replica gets its turn")
	assert.Equal(t, int32(1), maxActive.Load(), "no two replicas reconcile at once")
}

// TestWithGovernanceSyncLock_WaitsForHolder verifies a replica waits while
// another session holds the lock and proceeds as soon as it is released.
func TestWithGovernanceSyncLock_WaitsForHolder(t *testing.T) {
	initTestLogger()
	holder := openGovernanceSyncReplica(t)
	waiter := openGovernanceSyncReplica(t)

	sqlDB, err := holder.db.DB()
	require.NoError(t, err)
	conn, err := sqlDB.Conn(context.Background())
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.ExecContext(context.Background(), "SELECT pg_advisory_lock($1)", governanceSyncAdvisoryLockKey)
	require.NoError(t, err)

	ran := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- withGovernanceSyncLock(context.Background(), waiter, func() { close(ran) })
	}()

	select {
	case <-ran:
		t.Fatal("reconcile ran while another replica held the lock")
	case <-time.After(1500 * time.Millisecond):
	}

	_, err = conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", governanceSyncAdvisoryLockKey)
	require.NoError(t, err)
	select {
	case <-ran:
	case <-time.After(10 * time.Second):
		t.Fatal("reconcile did not run after the lock was released")
	}
	require.NoError(t, <-done)
}
