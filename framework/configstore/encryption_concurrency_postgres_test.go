package configstore

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// The startup backfill runs on every booting node against one shared database.
// These tests cover what that concurrency requires of it: nodes may queue behind
// each other, but must never deadlock and must never leave a row plaintext.

// newEncryptionPodStore opens an independent pool against the schema that
// setupPostgresDeadlockStore already migrated, standing in for another node.
func newEncryptionPodStore(t *testing.T) *RDBConfigStore {
	t.Helper()
	db, err := gorm.Open(postgres.Open(postgresDSN), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Skipf("postgres not available: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	store := &RDBConfigStore{logger: bifrost.NewDefaultLogger(schemas.LogLevelWarn)}
	store.db.Store(db)
	store.migrateOnFreshFn = func(ctx context.Context, fn func(context.Context, *gorm.DB) error) error {
		return fn(ctx, store.DB())
	}
	store.refreshPoolFn = func(ctx context.Context) error { return nil }
	return store
}

// TestEncryptPlaintextOAuthConfigs_WaitsForHolderThenConverges holds every row
// under an open transaction and asserts the walk queues behind the holder rather
// than skipping past it. Each row is written in its own transaction, so the wait
// is one row long and no lock is held across another, which is what keeps the
// blocking design deadlock-free while still leaving nothing plaintext.
func TestEncryptPlaintextOAuthConfigs_WaitsForHolderThenConverges(t *testing.T) {
	store := setupPostgresDeadlockStore(t)
	seedPlaintextOAuthConfigs(t, store.DB(), 20, "secret-")

	holder := newEncryptionPodStore(t)
	tx := holder.DB().Begin()
	require.NoError(t, tx.Error)

	var held []tables.TableOauthConfig
	require.NoError(t, tx.Clauses(clause.Locking{Strength: "UPDATE"}).Find(&held).Error)
	require.Len(t, held, 20, "the holder must own every row for this to prove anything")

	// Release mid-walk. A skipping walk would already have run past these rows
	// and reported success; a blocking one picks them all up.
	time.AfterFunc(2*time.Second, func() { _ = tx.Rollback() })

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	other := newEncryptionPodStore(t)
	count, err := other.encryptPlaintextOAuthConfigs(ctx)
	require.NoError(t, err)
	assert.Equal(t, 20, count, "rows held at claim time must still be encrypted, not skipped")
	assert.Zero(t, plaintextOAuthConfigCount(t, store.DB()),
		"the walk must not report done while rows are still plaintext")
}

// TestEncryptPlaintextOAuthConfigs_ConcurrentPodsDoNotDeadlock reproduces the
// reported startup crash: several nodes booting at once against the same table.
// Unordered batches let two nodes take the same rows in opposite orders, which
// Postgres resolves by killing one with SQLSTATE 40P01 — a fatal error at boot.
func TestEncryptPlaintextOAuthConfigs_ConcurrentPodsDoNotDeadlock(t *testing.T) {
	store := setupPostgresDeadlockStore(t)

	const (
		pods  = 5
		total = encryptionBatchSize * 4
	)
	seedPlaintextOAuthConfigs(t, store.DB(), total, "secret-")

	stores := make([]*RDBConfigStore, pods)
	for i := range stores {
		stores[i] = newEncryptionPodStore(t)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	start := make(chan struct{})
	counts := make(chan int, pods)
	errs := make(chan error, pods)
	var wg sync.WaitGroup
	for _, s := range stores {
		wg.Add(1)
		go func(s *RDBConfigStore) {
			defer wg.Done()
			<-start
			count, err := s.encryptPlaintextOAuthConfigs(ctx)
			counts <- count
			errs <- err
		}(s)
	}
	close(start)
	wg.Wait()
	close(counts)
	close(errs)

	for err := range errs {
		if isPostgresDeadlock(err) {
			t.Fatalf("startup encryption deadlocked across nodes: %v", err)
		}
		require.NoError(t, err)
	}

	// Every row is claimed by exactly one node, so the per-node counts partition
	// the table rather than overlapping.
	claimed := 0
	for c := range counts {
		claimed += c
	}
	assert.Equal(t, total, claimed, "each row should be encrypted by exactly one node")

	var remaining int64
	require.NoError(t, store.DB().Table("oauth_configs").
		Where("encryption_status = ? OR encryption_status IS NULL OR encryption_status = ''", encryptionStatusPlainText).
		Count(&remaining).Error)
	assert.Zero(t, remaining)

	var found tables.TableOauthConfig
	require.NoError(t, store.DB().Where("id = ?", oauthConfigID(total-1)).First(&found).Error)
	assert.Equal(t, fmt.Sprintf("secret-%d", total-1), found.ClientSecret.GetValue())
}

// mockVaultHook installs a vault store hook that sleeps for latency, and
// reports how many times it ran and the peak number of concurrent calls.
func mockVaultHook(t *testing.T, latency time.Duration) (puts, peak *atomic.Int64) {
	t.Helper()
	var calls, high, inFlight atomic.Int64
	schemas.VaultStoreHook = func(ctx context.Context, path string, value *string) error {
		calls.Add(1)
		cur := inFlight.Add(1)
		for {
			p := high.Load()
			if cur <= p || high.CompareAndSwap(p, cur) {
				break
			}
		}
		time.Sleep(latency)
		inFlight.Add(-1)
		return nil
	}
	schemas.VaultRemoveHook = func(ctx context.Context, path string) error { return nil }
	t.Cleanup(func() {
		schemas.VaultStoreHook = nil
		schemas.VaultRemoveHook = nil
	})
	return &calls, &high
}

// seedPlaintextKeys inserts n plaintext config_keys via raw SQL, plus the
// provider row their foreign key requires.
func seedPlaintextKeys(t *testing.T, db *gorm.DB, n int) {
	t.Helper()
	require.NoError(t, db.Exec("DELETE FROM config_keys").Error)
	require.NoError(t, db.Exec(`
		INSERT INTO config_providers (id, name, created_at, updated_at)
		VALUES (1, 'openai', now(), now()) ON CONFLICT (id) DO NOTHING`).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO config_keys (name, provider_id, provider, key_id, value, encryption_status, created_at, updated_at)
		SELECT 'key-' || lpad(g::text, 6, '0'), 1, 'openai', 'key-id-' || lpad(g::text, 6, '0'),
		       'sk-plaintext-' || g, 'plain_text', now(), now()
		FROM generate_series(0, ?) g`, n-1).Error)
}

func plaintextKeyCount(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Table("config_keys").
		Where("encryption_status = ? OR encryption_status IS NULL OR encryption_status = ''", encryptionStatusPlainText).
		Count(&n).Error)
	return n
}

// TestEncryptPlaintextKeys_OverlapsVaultWrites pins the reason rows are claimed
// concurrently: a vault write is a network round trip, and running them one at
// a time makes boot take rows*latency. It also pins that the hook runs only
// after the row lock is held, so a row is never hooked twice.
func TestEncryptPlaintextKeys_OverlapsVaultWrites(t *testing.T) {
	store := setupPostgresDeadlockStore(t)
	const rows = 48
	seedPlaintextKeys(t, store.DB(), rows)

	puts, peak := mockVaultHook(t, 50*time.Millisecond)
	require.Greater(t, encryptConcurrency(), 1, "vault writes must enable concurrency")

	start := time.Now()
	count, err := store.encryptPlaintextKeys(context.Background())
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Equal(t, rows, count)
	assert.Zero(t, plaintextKeyCount(t, store.DB()))
	assert.Equal(t, int64(rows), puts.Load(), "each row's hook must run exactly once")
	assert.Greater(t, peak.Load(), int64(1), "vault writes must overlap, not queue")
	assert.Less(t, elapsed, rows*50*time.Millisecond,
		"a serial walk would take rows*latency; the concurrent one must beat it")
}

// TestEncryptPlaintextKeys_ConcurrentPodsHookEachRowOnce is the multi-node form
// of the same guarantee: claiming under a row lock before hooking means two
// nodes never both pay a row's vault writes.
func TestEncryptPlaintextKeys_ConcurrentPodsHookEachRowOnce(t *testing.T) {
	store := setupPostgresDeadlockStore(t)
	const (
		rows = 40
		pods = 3
	)
	seedPlaintextKeys(t, store.DB(), rows)
	puts, _ := mockVaultHook(t, 20*time.Millisecond)

	stores := make([]*RDBConfigStore, pods)
	for i := range stores {
		stores[i] = newEncryptionPodStore(t)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	counts := make([]int, pods)
	errs := make([]error, pods)
	begin := make(chan struct{})
	var wg sync.WaitGroup
	for i := range stores {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-begin
			counts[i], errs[i] = stores[i].encryptPlaintextKeys(ctx)
		}(i)
	}
	close(begin)
	wg.Wait()

	total := 0
	for i, err := range errs {
		if isPostgresDeadlock(err) {
			t.Fatalf("concurrent claims deadlocked: %v", err)
		}
		require.NoError(t, err)
		total += counts[i]
	}
	assert.Equal(t, rows, total, "each row should be encrypted by exactly one node")
	assert.Equal(t, int64(rows), puts.Load(), "a row's vault writes must not be paid twice")
	assert.Zero(t, plaintextKeyCount(t, store.DB()))
}

// TestEncryptConcurrency_SequentialWithoutVault pins that deployments without
// vault writes keep the simple one-at-a-time path.
func TestEncryptConcurrency_SequentialWithoutVault(t *testing.T) {
	require.False(t, schemas.VaultStoreWriteEnabled())
	assert.Equal(t, 1, encryptConcurrency())
}

// TestEncryptPlaintextKeys_ResolvesVaultRefsOncePerRow guards the cost of the
// scan. Rows written while vault writes were on but encryption was off hold a
// "vault." ref with encryption_status still 'plain_text', and SecretVar.Scan
// resolves such a ref over the network on every load — regardless of the status
// column. The walk must therefore read ids only when scanning ahead, so a row's
// secrets are fetched once, by the claim that actually encrypts it.
func TestEncryptPlaintextKeys_ResolvesVaultRefsOncePerRow(t *testing.T) {
	store := setupPostgresDeadlockStore(t)
	const rows = 10

	require.NoError(t, store.DB().Exec("DELETE FROM config_keys").Error)
	require.NoError(t, store.DB().Exec(`
		INSERT INTO config_providers (id, name, created_at, updated_at)
		VALUES (1, 'openai', now(), now()) ON CONFLICT (id) DO NOTHING`).Error)
	require.NoError(t, store.DB().Exec(`
		INSERT INTO config_keys (name, provider_id, provider, key_id, value, encryption_status, created_at, updated_at)
		SELECT 'k-' || g, 1, 'openai', 'kid-' || g, 'vault.bifrost/config_keys/kid-' || g,
		       'plain_text', now(), now()
		FROM generate_series(0, ?) g`, rows-1).Error)

	var resolves, stores atomic.Int64
	schemas.VaultResolveHook = func(ctx context.Context, value *string) error {
		resolves.Add(1)
		*value = "resolved-secret"
		return nil
	}
	schemas.VaultStoreHook = func(ctx context.Context, path string, value *string) error {
		stores.Add(1)
		return nil
	}
	schemas.VaultRemoveHook = func(ctx context.Context, path string) error { return nil }
	t.Cleanup(func() {
		schemas.VaultResolveHook = nil
		schemas.VaultStoreHook = nil
		schemas.VaultRemoveHook = nil
	})

	count, err := store.encryptPlaintextKeys(context.Background())
	require.NoError(t, err)
	assert.Equal(t, rows, count)
	assert.Equal(t, int64(rows), resolves.Load(),
		"scanning ahead must read ids only; loading whole rows resolves every ref twice")
	assert.Zero(t, stores.Load(), "a value already stored in the vault must not be written back")
	assert.Zero(t, plaintextKeyCount(t, store.DB()), "vault-ref rows must still leave plain_text")
}
