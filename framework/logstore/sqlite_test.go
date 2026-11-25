package logstore

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// sqlitePragma reads an integer PRAGMA from the store's database.
func sqlitePragma(t *testing.T, store *RDBLogStore, name string) int64 {
	t.Helper()
	var value int64
	require.NoError(t, store.db.Raw("PRAGMA "+name).Row().Scan(&value))
	return value
}

// leaveFreePages fills a scratch table with n rows of about a page each, then
// deletes them, which puts their pages on the database's freelist.
func leaveFreePages(t *testing.T, db *sql.DB, n int) {
	t.Helper()
	_, err := db.Exec("CREATE TABLE filler (id INTEGER PRIMARY KEY, body BLOB)")
	require.NoError(t, err)
	_, err = db.Exec("WITH RECURSIVE g(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM g WHERE n < ?) INSERT INTO filler SELECT n, zeroblob(4000) FROM g", n)
	require.NoError(t, err)
	_, err = db.Exec("DELETE FROM filler")
	require.NoError(t, err)
}

// SQLite fixes a database's auto-vacuum mode when its first table is created,
// and in the default mode (NONE) pages freed by retention never leave the file
// (#9738). A new logs.db must therefore start in incremental mode.
func TestSQLiteLogStoreCreatesIncrementalAutoVacuumDatabase(t *testing.T) {
	store := newTestSQLiteStore(t)
	t.Cleanup(func() { _ = store.Close(context.Background()) })

	assert.Equal(t, int64(2), sqlitePragma(t, store, "auto_vacuum"), "0 is NONE, 1 is FULL, 2 is INCREMENTAL")
}

// A logs.db created before #9738 is in NONE mode. Opening it must not rewrite it
// (switching takes a VACUUM of the whole file), and reclaiming leaves it alone,
// since incremental_vacuum has nothing to work with in that mode.
func TestSQLiteLogStoreKeepsExistingAutoVacuumMode(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "logs.db")
	existing, err := sql.Open(sqlite.DriverName, path+"?_journal_mode=WAL")
	require.NoError(t, err)
	leaveFreePages(t, existing, 500)
	require.NoError(t, existing.Close())

	store, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: path}, testLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close(ctx) })
	assert.Equal(t, int64(0), sqlitePragma(t, store, "auto_vacuum"), "an existing database keeps its mode")
	free := sqlitePragma(t, store, "freelist_count")
	require.Positive(t, free)

	reclaimed, err := store.ReclaimFreeSpace(ctx)
	require.NoError(t, err)
	assert.Zero(t, reclaimed)
	assert.Equal(t, free, sqlitePragma(t, store, "freelist_count"))
}

// SQLite lets a database move from FULL to INCREMENTAL at any time, so asking for
// incremental mode on every open would quietly switch a logs.db an operator put
// in FULL. Only a new, empty file is given the mode.
func TestSQLiteLogStoreKeepsExistingFullAutoVacuum(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "logs.db")
	existing, err := sql.Open(sqlite.DriverName, path+"?_auto_vacuum=full&_journal_mode=WAL")
	require.NoError(t, err)
	_, err = existing.Exec("CREATE TABLE filler (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)
	var mode int64
	require.NoError(t, existing.QueryRow("PRAGMA auto_vacuum").Scan(&mode))
	require.Equal(t, int64(1), mode, "the setup must leave a FULL database")
	require.NoError(t, existing.Close())

	store, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: path}, testLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close(ctx) })
	assert.Equal(t, int64(1), sqlitePragma(t, store, "auto_vacuum"), "0 is NONE, 1 is FULL, 2 is INCREMENTAL")
}

// ReclaimFreeSpace drains the whole freelist, one incrementalVacuumPages chunk
// per write transaction, and reports the bytes given back.
func TestSQLiteReclaimFreeSpaceDrainsFreelist(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteStore(t)
	t.Cleanup(func() { _ = store.Close(ctx) })
	db, err := store.db.DB()
	require.NoError(t, err)
	leaveFreePages(t, db, 3*incrementalVacuumPages)
	free, pages := sqlitePragma(t, store, "freelist_count"), sqlitePragma(t, store, "page_count")
	require.Greater(t, free, int64(2*incrementalVacuumPages), "the freelist must span several chunks")

	reclaimed, err := store.ReclaimFreeSpace(ctx)
	require.NoError(t, err)
	assert.Zero(t, sqlitePragma(t, store, "freelist_count"))
	assert.LessOrEqual(t, sqlitePragma(t, store, "page_count"), pages-free, "every free page leaves the file")
	assert.Equal(t, free*sqlitePragma(t, store, "page_size"), reclaimed)
}

// A reader that holds a snapshot stops the passive checkpoint after a reclaim, so
// logs.db keeps its size. The next reclaim finds no free pages and must still
// complete the checkpoint, or logs.db waits for a later automatic checkpoint.
func TestSQLiteReclaimFreeSpaceCompletesCheckpointAReaderBlocked(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "logs.db")
	store, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: path}, testLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close(ctx) })
	db, err := store.db.DB()
	require.NoError(t, err)
	leaveFreePages(t, db, 3*incrementalVacuumPages)
	fileSize := func() int64 {
		t.Helper()
		info, err := os.Stat(path)
		require.NoError(t, err)
		return info.Size()
	}

	reader, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reader.Rollback() })
	var rows int64
	require.NoError(t, reader.QueryRow("SELECT count(*) FROM filler").Scan(&rows), "the read starts the snapshot")
	_, err = store.ReclaimFreeSpace(ctx)
	require.NoError(t, err)
	require.Zero(t, sqlitePragma(t, store, "freelist_count"))
	liveSize := sqlitePragma(t, store, "page_count") * sqlitePragma(t, store, "page_size")
	require.Greater(t, fileSize(), liveSize, "the reader must stop the checkpoint from truncating logs.db")
	require.NoError(t, reader.Rollback())

	reclaimed, err := store.ReclaimFreeSpace(ctx)
	require.NoError(t, err)
	assert.Zero(t, reclaimed, "the freelist is already empty")
	assert.Equal(t, liveSize, fileSize(), "logs.db must shrink to its live size once the reader is gone")
}

// Only SQLite keeps freed pages inside its file. On any other dialect
// ReclaimFreeSpace must return without touching the database.
func TestRDBReclaimFreeSpaceIsSQLiteOnly(t *testing.T) {
	onPostgres := &RDBLogStore{db: &gorm.DB{Config: &gorm.Config{Dialector: postgres.Dialector{Config: &postgres.Config{}}}}}
	reclaimed, err := onPostgres.ReclaimFreeSpace(context.Background())
	require.NoError(t, err)
	assert.Zero(t, reclaimed)
}
