package configstore

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// A write transaction that reads before it writes must wait for a concurrent writer rather than
// fail. SQLite upgrades a deferred transaction's read lock to the write lock without consulting the
// busy timeout when another connection holds it, so the upgrade fails at once with "database is
// locked". The startup pricing sync holds the write lock for seconds on a fresh store, which is how
// UpdateClientMetadata failed right after LoadConfig. Write transactions therefore begin IMMEDIATE.
func TestSQLiteWriteTransactionWaitsForAConcurrentWriter(t *testing.T) {
	ctx := context.Background()
	store, err := newSqliteConfigStore(ctx, &SQLiteConfig{Path: filepath.Join(t.TempDir(), "config.db")}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	require.NoError(t, err)
	rdb := store.(*RDBConfigStore)
	t.Cleanup(func() { _ = rdb.Close(ctx) })
	require.NoError(t, store.UpdateClientConfig(ctx, &ClientConfig{LogRetentionDays: 30}))

	holding := make(chan struct{})
	released := make(chan error, 1)
	go func() {
		released <- rdb.DB().Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&tables.TableClientConfig{}).Where("1 = 1").Update("log_retention_days", 7).Error; err != nil {
				return err
			}
			close(holding)
			time.Sleep(300 * time.Millisecond)
			return nil
		})
	}()
	<-holding

	start := time.Now()
	require.NoError(t, store.UpdateClientMetadata(ctx, map[string]any{"onboarding_dismissed": true}), "the metadata write waits for the other writer instead of failing")
	require.GreaterOrEqual(t, time.Since(start), 100*time.Millisecond, "the metadata write waited for the lock")
	require.NoError(t, <-released)

	metadata, err := store.GetClientMetadata(ctx)
	require.NoError(t, err)
	require.Equal(t, true, metadata["onboarding_dismissed"])
	cfg, err := store.GetClientConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, 7, cfg.LogRetentionDays, "the other writer's change is kept")
}

// A SQLite transaction that reads before it writes must still be able to write after another
// connection in the same pool has committed in between. In WAL mode a deferred transaction only
// holds a read snapshot after that first read, and its upgrade to a write then fails at once with
// "database is locked": the busy timeout never applies to a stale snapshot. Startup workers write
// to the store while the first request-driven write runs, so every transaction has to take the
// write lock when it begins and let the busy timeout queue the others behind it.
func TestSQLiteTransactionsTakeTheWriteLockOnBegin(t *testing.T) {
	store, err := newSqliteConfigStore(context.Background(), &SQLiteConfig{Path: filepath.Join(t.TempDir(), "config.db")}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close(context.Background()) })
	db := store.(*RDBConfigStore).DB()
	require.NoError(t, db.Exec("CREATE TABLE lock_probe (id INTEGER PRIMARY KEY, n INTEGER NOT NULL)").Error)
	require.NoError(t, db.Exec("INSERT INTO lock_probe (id, n) VALUES (1, 0)").Error)

	otherWriter := make(chan error, 1)
	err = db.Transaction(func(tx *gorm.DB) error {
		var n int
		if err := tx.Raw("SELECT n FROM lock_probe WHERE id = 1").Scan(&n).Error; err != nil {
			return err
		}
		// Another pooled connection writes while this transaction is open. It must queue
		// behind this transaction (and complete after the commit), not slip in ahead of it.
		go func() { otherWriter <- db.Exec("UPDATE lock_probe SET n = n + 10 WHERE id = 1").Error }()
		select {
		case err := <-otherWriter:
			require.NoError(t, err)
			t.Log("the other writer committed while this transaction was open")
		case <-time.After(200 * time.Millisecond):
			t.Log("the other writer is waiting behind this transaction")
		}
		return tx.Exec("UPDATE lock_probe SET n = ? WHERE id = 1", n+1).Error
	})
	require.NoError(t, err, "a read-then-write transaction must not fail on a stale snapshot")

	select {
	case err := <-otherWriter:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("the other writer never completed")
	}
	var n int
	require.NoError(t, db.Raw("SELECT n FROM lock_probe WHERE id = 1").Scan(&n).Error)
	require.Equal(t, 11, n, "both writes must land, in order")
}

// The config database holds provider keys and other credentials, so a database file the store
// creates is readable and writable by the process owner only. A file that already exists keeps
// the mode its operator gave it.
func TestSQLiteConfigStoreCreatesOwnerOnlyDatabaseFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	ctx := context.Background()
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)

	created := filepath.Join(t.TempDir(), "config.db")
	store, err := newSqliteConfigStore(ctx, &SQLiteConfig{Path: created}, logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.(*RDBConfigStore).Close(ctx) })
	info, err := os.Stat(created)
	require.NoError(t, err)
	require.Zero(t, info.Mode().Perm()&0o077, "no group or other access, whatever the umask: mode %o", info.Mode().Perm())

	existing := filepath.Join(t.TempDir(), "config.db")
	require.NoError(t, os.WriteFile(existing, nil, 0o640))
	require.NoError(t, os.Chmod(existing, 0o640))
	kept, err := newSqliteConfigStore(ctx, &SQLiteConfig{Path: existing}, logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = kept.(*RDBConfigStore).Close(ctx) })
	info, err = os.Stat(existing)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o640), info.Mode().Perm())
}

// The driver treats everything after a "?" in the path as options, so such a path would open a
// different file from the one created here. It is refused instead of silently using another file.
func TestSQLiteConfigStoreRefusesAPathWithAQueryMarker(t *testing.T) {
	dir := t.TempDir()
	_, err := newSqliteConfigStore(context.Background(), &SQLiteConfig{Path: filepath.Join(dir, "config.db?x")}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	require.Error(t, err)
	require.Contains(t, err.Error(), "?")
	entries, readErr := os.ReadDir(dir)
	require.NoError(t, readErr)
	require.Empty(t, entries, "no file is created for a refused path")
}

// A path written as a SQLite URI ("file:config.db") names two different files: the one created
// here would be literally "file:config.db", while the driver opens "config.db" and creates it
// with its own default mode. It is refused, so the database is always the file made owner-only.
func TestSQLiteConfigStoreRefusesAFileURIPath(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	for _, path := range []string{"file:config.db", "FILE:config.db", "file:" + filepath.Join(dir, "config.db")} {
		_, err := newSqliteConfigStore(context.Background(), &SQLiteConfig{Path: path}, bifrost.NewDefaultLogger(schemas.LogLevelError))
		require.Error(t, err, "path %q", path)
		require.Contains(t, err.Error(), "file:", "path %q", path)
	}
	entries, readErr := os.ReadDir(dir)
	require.NoError(t, readErr)
	require.Empty(t, entries, "no file is created for a refused path")
}
